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

package strategy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const EvictionNameSuppression = "cpu-pressure-suppression-plugin"

type CPUPressureSuppression struct {
	conf  *config.Configuration
	state state.ReadonlyState

	lastToleranceTime sync.Map
}

func NewCPUPressureSuppressionEviction(_ metrics.MetricEmitter, _ *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState) (CPUPressureEviction, error) {
	return &CPUPressureSuppression{
		conf:  conf,
		state: state,
	}, nil
}

func (p *CPUPressureSuppression) Start(context.Context) error { return nil }
func (p *CPUPressureSuppression) Name() string                { return EvictionNameSuppression }
func (p *CPUPressureSuppression) ThresholdMet(_ context.Context, _ *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{}, nil
}
func (p *CPUPressureSuppression) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (p *CPUPressureSuppression) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	dynamicConfig := p.conf.GetDynamicConfiguration()
	if !dynamicConfig.EnableSuppressionEviction {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}
	general.InfoS("cpu suppression enabled")

	// only reclaim pool support suppression tolerance eviction
	entries := p.state.GetPodEntries()
	poolCPUSet, err := entries.GetCPUSetForPool(state.PoolNameReclaim)
	if err != nil {
		return nil, fmt.Errorf("get reclaim pool failed: %s", err)
	}

	// skip evict pods if pool size is zero
	poolSize := poolCPUSet.Size()
	if poolSize == 0 {
		general.Errorf("reclaim pool set size is empty")
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	filteredPods := native.FilterPods(request.ActivePods, p.conf.CheckReclaimedQoSForPod)
	if len(filteredPods) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	// prioritize evicting the pod whose cpu request is larger and priority is lower
	general.NewMultiSorter(
		general.ReverseCmpFunc(native.PodCPURequestCmpFunc),
		general.ReverseCmpFunc(native.PodPriorityCmpFunc),
		native.PodUniqKeyCmpFunc,
	).Sort(native.NewPodSourceImpList(filteredPods))

	// sum all pod cpu request
	totalCPURequest := resource.Quantity{}
	for _, pod := range filteredPods {
		totalCPURequest.Add(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
	}
	general.Infof("total reclaim cpu request is %v, reclaim pool size is %v", totalCPURequest.String(), poolSize)

	now := time.Now()
	var evictPods []*v1alpha1.EvictPod
	for _, pod := range filteredPods {
		key := native.GenerateUniqObjectNameKey(pod)
		poolSuppressionRate := float64(totalCPURequest.Value()) / float64(poolSize)

		if podToleranceRate := p.getPodToleranceRate(pod, dynamicConfig.MaxSuppressionToleranceRate); podToleranceRate < poolSuppressionRate {
			last, _ := p.lastToleranceTime.LoadOrStore(key, now)
			lastDuration := now.Sub(last.(time.Time))
			general.Infof("current pool suppression rate %.2f, "+
				"and it is over than suppression tolerance rate %.2f of pod %s, last duration: %s secs", poolSuppressionRate,
				podToleranceRate, key, now.Sub(last.(time.Time)))

			// a pod will only be evicted if its cpu suppression lasts longer than minToleranceDuration
			if lastDuration > dynamicConfig.MinSuppressionToleranceDuration {
				evictPods = append(evictPods, &v1alpha1.EvictPod{
					Pod: pod,
					Reason: fmt.Sprintf("current pool suppression rate %.2f is over than the "+
						"pod suppression tolerance rate %.2f", poolSuppressionRate, podToleranceRate),
				})
				totalCPURequest.Sub(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
			}
		} else {
			p.lastToleranceTime.Delete(key)
		}
	}

	// clear inactive filtered pod from lastToleranceTime
	filteredPodsMap := native.GetPodNamespaceNameKeyMap(filteredPods)
	p.lastToleranceTime.Range(func(key, _ interface{}) bool {
		if _, ok := filteredPodsMap[key.(string)]; !ok {
			p.lastToleranceTime.Delete(key)
		}
		return true
	})

	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

// getPodToleranceRate returns pod suppression tolerance rate,
// and it is limited by max cpu suppression tolerance rate.
func (p *CPUPressureSuppression) getPodToleranceRate(pod *v1.Pod, maxToleranceRate float64) float64 {
	rate, err := qos.GetPodCPUSuppressionToleranceRate(p.conf.QoSConfiguration, pod)
	if err != nil {
		general.Errorf("pod %s get cpu suppression tolerance rate failed: %s",
			native.GenerateUniqObjectNameKey(pod), err)
		return maxToleranceRate
	} else {
		return math.Min(rate, maxToleranceRate)
	}
}
