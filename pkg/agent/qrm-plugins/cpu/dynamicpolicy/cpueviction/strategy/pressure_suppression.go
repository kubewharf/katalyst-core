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
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

type CPUPressureSuppression struct {
	qosConf *generic.QoSConfiguration
	state   state.State

	lastToleranceTime    sync.Map
	minToleranceDuration time.Duration
	maxToleranceRate     float64
}

func NewCPUPressureSuppressionEviction(_ metrics.MetricEmitter, _ *metaserver.MetaServer,
	conf *config.Configuration, state state.State) CPUPressureForceEviction {
	return &CPUPressureSuppression{
		qosConf: conf.QoSConfiguration,
		state:   state,

		minToleranceDuration: conf.MinCPUSuppressionToleranceDuration,
		maxToleranceRate:     conf.MaxCPUSuppressionToleranceRate,
	}

}

func (p *CPUPressureSuppression) Start(context.Context) error { return nil }
func (p *CPUPressureSuppression) Name() string                { return "pressure-suppression" }

func (p *CPUPressureSuppression) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	// only reclaim pool support suppression tolerance eviction
	entries := p.state.GetPodEntries()
	poolCPUSet, err := entries.GetCPUSetForPool(state.PoolNameReclaim)
	if err != nil {
		return nil, fmt.Errorf("get reclaim pool failed: %s", err)
	}

	// skip evict pods if pool size is zero
	poolSize := poolCPUSet.Size()
	if poolSize == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	filteredPods := native.FilterPods(request.ActivePods, p.qosConf.CheckReclaimedQoSForPod)
	if len(filteredPods) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	// prioritize evicting the pod whose cpu request is larger and priority is lower
	general.NewMultiSorter(
		general.ReverseCmpFunc(native.PodCPURequestCmpFunc),
		general.ReverseCmpFunc(native.PodPriorityCmpFunc),
	).Sort(native.NewPodSourceImpList(filteredPods))

	// sum all pod cpu request
	totalCPURequest := resource.Quantity{}
	for _, pod := range filteredPods {
		totalCPURequest.Add(native.GetCPUQuantity(native.SumUpPodRequestResources(pod)))
	}

	now := time.Now()
	var evictPods []*v1alpha1.EvictPod
	for _, pod := range filteredPods {
		key := native.GenerateUniqObjectNameKey(pod)
		poolSuppressionRate := float64(totalCPURequest.Value()) / float64(poolSize)

		if podToleranceRate := p.getPodToleranceRate(pod); podToleranceRate < poolSuppressionRate {
			last, _ := p.lastToleranceTime.LoadOrStore(key, now)
			lastDuration := now.Sub(last.(time.Time))
			klog.Infof("[pressure-suppression] current pool suppression rate %.2f, "+
				"and it is over than suppression tolerance rate %.2f of pod %s, last duration: %s secs", poolSuppressionRate,
				podToleranceRate, key, now.Sub(last.(time.Time)))

			// a pod will only be evicted if its cpu suppression lasts longer than minToleranceDuration
			if lastDuration > p.minToleranceDuration {
				evictPods = append(evictPods, &v1alpha1.EvictPod{
					Pod: pod,
					Reason: fmt.Sprintf("current pool suppression rate %.2f is over than the "+
						"pod suppression tolerance rate %.2f", poolSuppressionRate, podToleranceRate),
				})
				totalCPURequest.Sub(native.GetCPUQuantity(native.SumUpPodRequestResources(pod)))
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
func (p *CPUPressureSuppression) getPodToleranceRate(pod *v1.Pod) float64 {
	rate, err := qos.GetPodCPUSuppressionToleranceRate(p.qosConf, pod)
	if err != nil {
		klog.Errorf("[pressure-suppression] pod %s get cpu suppression tolerance rate failed: %s",
			native.GenerateUniqObjectNameKey(pod), err)
		return p.maxToleranceRate
	} else {
		return math.Min(rate, p.maxToleranceRate)
	}
}
