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

package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/controller/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/controller/action/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// 8 seconds between actions since RAPL/HSMP capping needs 4-6 seconds to stabilize itself
	intervalSpecFetch = time.Second * 8

	metricPowerAwareCurrentPowerInWatt = "power_current_watt"
	metricPowerAwareDesiredPowerInWatt = "power_desired_watt"
)

type PowerAwareController interface {
	Run(ctx context.Context)
}

type powerAwareController struct {
	emitter     metrics.MetricEmitter
	specFetcher spec.SpecFetcher
	powerReader reader.PowerReader
	reconciler  PowerReconciler
	powerCapper capper.PowerCapper
	podEvictor  evictor.PodEvictor

	// inFreqCap is flag whether node is state of power capping via CPU frequency adjustment
	// it is checked for power capping reset when alert is gone
	inFreqCap bool
}

func (p *powerAwareController) Run(ctx context.Context) {
	if p.powerReader == nil {
		general.Errorf("pap: no power reader is provided; contrroller stopped")
		return
	}
	if err := p.powerReader.Init(); err != nil {
		klog.Errorf("pap: failed to initialize power reader: %v; controller stopped", err)
		return
	}

	if p.podEvictor == nil {
		klog.Errorf("pap: no pod eviction server is provided; controller stopped")
		return
	}
	p.podEvictor.Reset(ctx)

	if p.powerCapper == nil {
		klog.Errorf("pap: no power capping server is provided; controller stopped")
		return
	}
	if err := p.powerCapper.Init(); err != nil {
		klog.Errorf("pap: failed to initialize power capping: %v; controller roller stopped", err)
		return
	}

	wait.Until(func() { p.run(ctx) }, intervalSpecFetch, ctx.Done())

	general.Infof("pap: controller Run exit")
	p.powerReader.Cleanup()
	p.powerCapper.Reset()
}

func (p *powerAwareController) run(ctx context.Context) {
	powerSpec, err := p.specFetcher.GetPowerSpec(ctx)
	if err != nil {
		klog.Errorf("pap: getting power spec failed: %#v", err)
		return
	}

	klog.V(6).Infof("pap: current power spec: %#v", *powerSpec)

	// remove power capping limit if any, on NONE alert and capping was involved
	if powerSpec.Alert == spec.PowerAlertOK {
		if p.inFreqCap {
			p.inFreqCap = false
			p.powerCapper.Reset()
		}
		return
	}

	if spec.InternalOpPause == powerSpec.InternalOp {
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
	_ = p.emitter.StoreInt64(metricPowerAwareDesiredPowerInWatt, int64(powerSpec.Budget), metrics.MetricTypeNameRaw)

	freqCapped, err := p.reconciler.Reconcile(ctx, powerSpec, currentWatts)
	if err != nil {
		general.Errorf("pap: reconcile error: %v", err)
		// todo: report to metric dashboard
		return
	}

	if freqCapped {
		p.inFreqCap = true
	}
}

func NewController(dryRun bool,
	podEvictor evictor.PodEvictor,
	emitter metrics.MetricEmitter,
	nodeFetcher node.NodeFetcher,
	qosConfig *generic.QoSConfiguration,
	podFetcher pod.PodFetcher,
	reader reader.PowerReader,
	capper capper.PowerCapper,
) PowerAwareController {
	return &powerAwareController{
		emitter:     emitter,
		specFetcher: spec.NewFetcher(nodeFetcher),
		powerReader: reader,
		podEvictor:  podEvictor,
		powerCapper: capper,
		reconciler: &powerReconciler{
			dryRun:      dryRun,
			priorAction: action.PowerAction{},
			evictor:     evictor.NewPowerLoadEvict(qosConfig, podFetcher, podEvictor),
			capper:      capper,
			strategy:    strategy.NewRuleBasedPowerStrategy(),
		},
		inFreqCap: false,
	}
}
