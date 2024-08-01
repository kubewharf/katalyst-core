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

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/reader"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
)

// 8 seconds between actions since RAPL capping needs 4-6 seconds to stablize itself
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
	inAlert                bool
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

	klog.V(6).Infof("pap: Run: inAlert: %v", p.inAlert)

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
	klog.V(6).Infof("pap: inAlert: %v", p.inAlert)

	// remove power capping limit if any, on NONE alert
	// only reset when an alert is gone
	if spec.Alert == types.PowerAlertOK {
		if p.inAlert {
			p.inAlert = false
			p.powerLimitInitResetter.Reset()
		}
		return
	}

	p.inAlert = true

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

	p.reconciler.Reconcile(ctx, spec, currentWatts)
}

func NewController(dryRun bool, emitter metrics.MetricEmitter,
	nodeFetcher node.NodeFetcher, podFetcher pod.PodFetcher,
	qosConfig *generic.QoSConfiguration, limiter power.PowerLimiter,
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
				podKiller:  &dummyPodKiller{},
			},
			capper:   capper.NewPowerCapper(limiter),
			strategy: &ruleBasedPowerStrategy{coefficient: linearDecay{b: defaultDecayB}},
		},
		powerLimitInitResetter: limiter,
		inAlert:                false,
	}
}
