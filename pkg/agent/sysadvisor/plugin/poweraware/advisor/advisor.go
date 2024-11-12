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

package advisor

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
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
	// 9 seconds between actions since RAPL/HSMP capping needs 4-6 seconds to stabilize itself
	// and malachite realtime metric server imposes delay of up to 2 seconds
	intervalSpecFetch = time.Second * 9

	metricPowerAwareCurrentPowerInWatt = "power_current_watt"
	metricPowerAwareDesiredPowerInWatt = "power_desired_watt"
	metricPowerAwareActionPlan         = "power_action_plan"
	metricTagNameActionPlanOp          = "op"
	metricTagNameActionPlanMode        = "mode"
)

// PowerAwareAdvisor is the interface that runs the whole power advisory process
type PowerAwareAdvisor interface {
	// Run depicts the whole process taking in power related inputs, generating action plans, and delegating the executions
	Run(ctx context.Context)
	// Init initializes components
	Init() error
}

type powerAwareAdvisor struct {
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

func (p *powerAwareAdvisor) Init() error {
	if p.powerReader == nil {
		return errors.New("no power reader is provided")
	}
	if err := p.powerReader.Init(); err != nil {
		return errors.Wrap(err, "failed to initialize power reader")
	}

	if p.podEvictor == nil {
		return errors.New("no pod eviction server is provided")
	}
	if err := p.podEvictor.Init(); err != nil {
		return errors.Wrap(err, "failed to initialize evict service")
	}

	if p.powerCapper == nil {
		return errors.New("no power capping server is provided")
	}
	if err := p.powerCapper.Init(); err != nil {
		return errors.Wrap(err, "failed to initialize power capping server")
	}

	return nil
}

func (p *powerAwareAdvisor) Run(ctx context.Context) {
	general.Infof("pap: advisor Run started")
	if err := p.podEvictor.Start(); err != nil {
		general.Errorf("pap: failed to start pod evict service: %v", err)
		return
	}
	if err := p.powerCapper.Start(); err != nil {
		general.Errorf("pap: failed to start power capping service: %v", err)
		return
	}

	defer p.cleanup()
	defer p.powerCapper.Reset()

	wait.Until(func() { p.run(ctx) }, intervalSpecFetch, ctx.Done())

	general.Infof("pap: advisor Run exited")
}

func (p *powerAwareAdvisor) cleanup() {
	p.powerReader.Cleanup()
	if err := p.podEvictor.Stop(); err != nil {
		general.Errorf("pap: failed to stop power pod evictor: %v", err)
	}
	if err := p.powerCapper.Stop(); err != nil {
		general.Errorf("pap: failed to stop power capper: %v", err)
	}
}

func (p *powerAwareAdvisor) run(ctx context.Context) {
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

	if spec.InternalOpNoop == powerSpec.InternalOp {
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

func NewAdvisor(dryRun bool,
	annotationKeyPrefix string,
	podEvictor evictor.PodEvictor,
	emitter metrics.MetricEmitter,
	nodeFetcher node.NodeFetcher,
	qosConfig *generic.QoSConfiguration,
	podFetcher pod.PodFetcher,
	reader reader.PowerReader,
	capper capper.PowerCapper,
) PowerAwareAdvisor {
	return &powerAwareAdvisor{
		emitter:     emitter,
		specFetcher: spec.NewFetcher(nodeFetcher, annotationKeyPrefix),
		powerReader: reader,
		podEvictor:  podEvictor,
		powerCapper: capper,
		reconciler:  newReconciler(dryRun, emitter, evictor.NewPowerLoadEvict(qosConfig, podFetcher, podEvictor), capper),
		inFreqCap:   false,
	}
}
