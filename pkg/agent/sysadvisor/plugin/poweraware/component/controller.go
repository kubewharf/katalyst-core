/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package component

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/reader"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// 8 seconds between actions since RAPL capping needs 4-6 seconds to stabilize itself
const (
	intervalSpecFetch = time.Second * 8

	metricPowerAwareCurrentPowerInWatt = "power_current_watt"
	metricPowerAwareDesiredPowerInWatt = "power_desired_watt"
)

type PowerAwareController interface {
	Run(ctx context.Context)
}

type powerAwareController struct {
	emitter                metrics.MetricEmitter
	specFetcher            SpecFetcher
	powerReader            reader.PowerReader
	reconciler             PowerReconciler
	powerLimitInitResetter power.InitResetter
	inFreqCap              bool
}

func (p *powerAwareController) Run(ctx context.Context) {
	if err := p.powerReader.Init(); err != nil {
		klog.Errorf("pap: failed to initialize power reader: %v; exited", err)
		return
	}
	if err := p.powerLimitInitResetter.Init(); err != nil {
		klog.Errorf("pap: failed to initialize power capping: %v; exited", err)
		return
	}

	wait.Until(func() { p.run(ctx) }, intervalSpecFetch, ctx.Done())

	klog.V(6).Info("pap: Run exit")
	p.powerReader.Cleanup()
	p.powerLimitInitResetter.Reset()
}

func (p *powerAwareController) run(ctx context.Context) {
	spec, err := p.specFetcher.GetPowerSpec(ctx)
	if err != nil {
		klog.Errorf("pap: getting power spec failed: %#v", err)
		return
	}

	klog.V(6).Infof("pap: current power spec: %#v", *spec)

	// remove power capping limit if any, on NONE alert
	// only reset when an alert is gone
	if spec.Alert == types.PowerAlertOK {
		if p.inFreqCap {
			p.inFreqCap = false
			p.powerLimitInitResetter.Reset()
		}
		return
	}

	if types.InternalOpPause == spec.InternalOp {
		return
	}

	klog.V(6).Info("pap: run to get power reading")

	currentWatts, err := p.powerReader.Get(ctx)
	if err != nil {
		klog.Errorf("pap: reading power failed: %#v", err)
		return
	}

	klog.V(6).Infof("pap: current power usage: %d watts", currentWatts)

	// report metrics: current power reading, desired power value
	_ = p.emitter.StoreInt64(metricPowerAwareCurrentPowerInWatt, int64(currentWatts), metrics.MetricTypeNameRaw)
	_ = p.emitter.StoreInt64(metricPowerAwareDesiredPowerInWatt, int64(spec.Budget), metrics.MetricTypeNameRaw)

	freqCapped, err := p.reconciler.Reconcile(ctx, spec, currentWatts)
	if err != nil {
		// not to log error, as there would be too many such logs - denial of service risk
		klog.V(6).Infof("pap: reconcile error - %s", err)
		// todo: report to metric dashboard
		return
	}

	if freqCapped {
		p.inFreqCap = true
	}
}

func GetPodEvictorBasedOnConfig(conf *config.Configuration, emitter metrics.MetricEmitter) (podEvictor plugin.PodEvictor, err error) {
	if !general.IsNameEnabled(plugin.EvictionPluginNameNodePowerPressure,
		evictionmanager.InnerEvictionPluginsDisabledByDefault,
		conf.GenericEvictionConfiguration.InnerPlugins,
	) {
		general.Warningf(" %s is disabled", plugin.EvictionPluginNameNodePowerPressure)
		podEvictor = &NoopPodEvictor{}
		return
	}

	podEvictor, err = startPowerPressurePodEvictorService(conf, emitter)
	return
}

func startPowerPressurePodEvictorService(conf *config.Configuration, emitter metrics.MetricEmitter) (plugin.PodEvictor, error) {
	podEvictor, service, err := plugin.NewPowerPressureEvictPluginServer(conf, emitter)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create power pressure eviction plugin server")
	}

	if err := service.Start(); err != nil {
		return nil, errors.Wrap(err, "failed to start power pressure eviction plugin server")
	}

	return podEvictor, nil
}

func NewController(podEvictor plugin.PodEvictor,
	dryRun bool,
	emitter metrics.MetricEmitter,
	nodeFetcher node.NodeFetcher,
	qosConfig *generic.QoSConfiguration,
	podFetcher pod.PodFetcher,
	limiter power.PowerLimiter,
) PowerAwareController {
	return &powerAwareController{
		emitter:     emitter,
		specFetcher: &specFetcherByNodeAnnotation{nodeFetcher: nodeFetcher},
		powerReader: reader.NewIPMIPowerReader(),
		reconciler: &powerReconciler{
			dryRun:      dryRun,
			priorAction: PowerAction{},
			evictor: &loadEvictor{
				qosConfig:  qosConfig,
				podFetcher: podFetcher,
				podEvictor: podEvictor,
			},
			capper:   capper.NewPowerCapper(limiter),
			strategy: &ruleBasedPowerStrategy{coefficient: linearDecay{b: defaultDecayB}},
		},
		powerLimitInitResetter: limiter,
		inFreqCap:              false,
	}
}
