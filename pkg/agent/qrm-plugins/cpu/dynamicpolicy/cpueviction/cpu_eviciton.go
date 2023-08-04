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

package cpueviction

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const cpuPressureEvictionPluginName = "cpu-pressure-eviction-plugin"

type cpuPressureEviction struct {
	ctx    context.Context
	cancel context.CancelFunc

	sync.Mutex
	started bool

	// the order in those list also indicates the priority of those eviction strategies
	forceEvictionList     []strategy.CPUPressureForceEviction
	thresholdEvictionList []strategy.CPUPressureThresholdEviction

	emitter metrics.MetricEmitter
}

func NewCPUPressureEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState) (agent.Component, error) {
	plugin, err := newCPUPressureEviction(emitter, metaServer, conf, state)
	if err != nil {
		return nil, fmt.Errorf("create cpu eviction plugin failed: %s", err)
	}

	return &agent.PluginWrapper{GenericPlugin: plugin}, nil
}

func newCPUPressureEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState) (skeleton.GenericPlugin, error) {
	wrappedEmitter := emitter.WithTags(cpuPressureEvictionPluginName)

	pressureLoadEviction, err := strategy.NewCPUPressureLoadEviction(emitter, metaServer, conf, state)
	if err != nil {
		return nil, fmt.Errorf("create CPUPressureLoadEviction plugin failed, err:%v", err)
	}

	plugin := &cpuPressureEviction{
		forceEvictionList: []strategy.CPUPressureForceEviction{
			strategy.NewCPUPressureSuppressionEviction(emitter, metaServer, conf, state),
		},
		thresholdEvictionList: []strategy.CPUPressureThresholdEviction{
			pressureLoadEviction,
		},
		emitter: wrappedEmitter,
	}

	return skeleton.NewRegistrationPluginWrapper(plugin, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
}

func (p *cpuPressureEviction) Name() string {
	return cpuPressureEvictionPluginName
}

func (p *cpuPressureEviction) Start() (err error) {
	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		return
	}

	general.Infof("%s", p.Name())
	p.ctx, p.cancel = context.WithCancel(context.Background())
	for _, s := range p.forceEvictionList {
		if startErr := s.Start(p.ctx); startErr != nil {
			return fmt.Errorf("start %v failed: %v", s.Name(), startErr)
		}
	}
	for _, s := range p.thresholdEvictionList {
		if startErr := s.Start(p.ctx); startErr != nil {
			return fmt.Errorf("start %v failed: %v", s.Name(), startErr)
		}
	}

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.ctx.Done())
	return
}

func (p *cpuPressureEviction) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exist
	if !p.started {
		return nil
	}

	general.Infof("%s", p.Name())
	p.cancel()
	return nil
}

// GetEvictPods works as the following logic
// - walk through all strategies, union all the target pods (after removing duplicated pods)
// - if any strategy fails, return error immediately
func (p *cpuPressureEviction) GetEvictPods(ctx context.Context,
	request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	var evictPods map[types.UID]*pluginapi.EvictPod
	generateEvictPods := func() []*pluginapi.EvictPod {
		var ret []*pluginapi.EvictPod
		for _, pod := range evictPods {
			ret = append(ret, pod)
		}
		return ret
	}

	for _, s := range p.forceEvictionList {
		resp, err := s.GetEvictPods(ctx, request)
		if err != nil {
			return &pluginapi.GetEvictPodsResponse{}, fmt.Errorf("%v GetEvictPods failed: %v", s.Name(), err)
		}

		for _, pod := range resp.EvictPods {
			evictPods[pod.Pod.UID] = pod
		}
	}

	return &pluginapi.GetEvictPodsResponse{
		EvictPods: generateEvictPods(),
	}, nil
}

// GetTopEvictionPods works as the following logic
// - walk through all strategies, until the amount of different targets reach topN
// - if any strategy fails, just ignore
func (p *cpuPressureEviction) GetTopEvictionPods(ctx context.Context,
	request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	var targets map[types.UID]*v1.Pod
	generateTargetPods := func() []*v1.Pod {
		var ret []*v1.Pod
		for _, pod := range targets {
			ret = append(ret, pod)
		}
		return ret
	}

	for _, s := range p.thresholdEvictionList {
		resp, err := s.GetTopEvictionPods(ctx, request)
		if err != nil {
			general.Errorf("%v GetTopEvictionPods failed: %v", s.Name(), err)
		} else {
			for _, pod := range resp.TargetPods {
				targets[pod.UID] = pod
				if uint64(len(targets)) >= request.TopN {
					return &pluginapi.GetTopEvictionPodsResponse{
						TargetPods: generateTargetPods(),
					}, nil
				}
			}
		}
	}

	return &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: generateTargetPods(),
	}, nil
}

// ThresholdMet works as the following logic
// - if some strategy falls into hardMet, return it immediately
// - else if some strategy falls into softMet, return the first softMet
// - else, return as not-met
func (p *cpuPressureEviction) ThresholdMet(ctx context.Context,
	empty *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	var softMetResponse *pluginapi.ThresholdMetResponse

	for _, s := range p.thresholdEvictionList {
		resp, err := s.ThresholdMet(ctx, empty)

		if err != nil {
			general.Errorf("%v ThresholdMet failed: %v", s.Name(), err)
		} else if resp.MetType == pluginapi.ThresholdMetType_HARD_MET {
			return resp, nil
		} else if softMetResponse == nil {
			softMetResponse = resp
		}
	}

	if softMetResponse != nil {
		return softMetResponse, nil
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}
