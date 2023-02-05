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
	"fmt"
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

// getEvictOverSuppressionTolerancePods get needed to evict pod which is over the suppression tolerance rate
func (p *cpuPressureEvictionPlugin) getEvictOverSuppressionTolerancePods(activePods []*v1.Pod) []*v1alpha1.EvictPod {
	entries := p.state.GetPodEntries()

	// only reclaim pool support suppression tolerance eviction
	poolCPUSet, err := entries.GetPoolCPUset(state.PoolNameReclaim)
	if err != nil {
		klog.Errorf("[cpu-pressure-eviction-plugin.getEvictOverSuppressionTolerancePods] get reclaim pool failed: %s", err)
		return []*v1alpha1.EvictPod{}
	}
	poolSize := poolCPUSet.Size()

	// skip evict pods if pool size is zero
	if poolSize == 0 {
		return []*v1alpha1.EvictPod{}
	}

	filteredPods := native.FilterPods(activePods, p.qosConf.CheckReclaimedQoSForPod)
	if len(filteredPods) == 0 {
		return []*v1alpha1.EvictPod{}
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
		if podSuppressionToleranceRate := p.getPodSuppressionToleranceRate(pod); podSuppressionToleranceRate < poolSuppressionRate {
			last, _ := p.lastOverSuppressionToleranceTime.LoadOrStore(key, now)
			lastDuration := now.Sub(last.(time.Time))
			klog.Infof("[cpu-pressure-eviction-plugin.getEvictOverSuppressionTolerancePods] current pool suppression rate %.2f, "+
				"and it is over than suppression tolerance rate %.2f of pod %s, last duration: %s secs", poolSuppressionRate,
				podSuppressionToleranceRate, key, now.Sub(last.(time.Time)))
			// a pod will only be evicted if its cpu suppression lasts longer than minCPUSuppressionToleranceDuration
			if lastDuration > p.minCPUSuppressionToleranceDuration {
				evictPods = append(evictPods, &v1alpha1.EvictPod{
					Pod: pod,
					Reason: fmt.Sprintf("current pool suppression rate %.2f is over than the "+
						"pod suppression tolerance rate %.2f", poolSuppressionRate, podSuppressionToleranceRate),
				})
				totalCPURequest.Sub(native.GetCPUQuantity(native.SumUpPodRequestResources(pod)))
			}
		} else {
			p.lastOverSuppressionToleranceTime.Delete(key)
		}
	}

	// clear inactive filtered pod from lastOverSuppressionToleranceTime
	filteredPodsMap := native.GetPodNamespaceNameKeyMap(filteredPods)
	p.lastOverSuppressionToleranceTime.Range(func(key, _ interface{}) bool {
		if _, ok := filteredPodsMap[key.(string)]; !ok {
			p.lastOverSuppressionToleranceTime.Delete(key)
		}
		return true
	})

	return evictPods
}

// getPodSuppressionToleranceRate returns pod suppression tolerance rate,
// and it is limited by max cpu suppression tolerance rate.
func (p *cpuPressureEvictionPlugin) getPodSuppressionToleranceRate(pod *v1.Pod) float64 {
	rate, err := qos.GetPodCPUSuppressionToleranceRate(p.qosConf, pod)
	if err != nil {
		klog.Errorf("[cpu-pressure-eviction-plugin.getPodSuppressionToleranceRate] pod %s get cpu suppression tolerance rate failed: %s", native.GenerateUniqObjectNameKey(pod), err)
		return p.maxCPUSuppressionToleranceRate
	} else {
		return math.Min(rate, p.maxCPUSuppressionToleranceRate)
	}
}
