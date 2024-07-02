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

package loadaware

import (
	"context"
	"fmt"
	"math"
	"time"

	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	quotav1 "k8s.io/apiserver/pkg/quota/v1"
	resourceapi "k8s.io/kubernetes/pkg/api/v1/resource"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func (p *Plugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

func (p *Plugin) Score(_ context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	if !p.IsLoadAwareEnabled(pod) {
		return 0, nil
	}

	if p.enablePortrait() {
		return p.scoreByPortrait(pod, nodeName)
	}

	return p.scoreByNPD(pod, nodeName)
}

func (p *Plugin) scoreByNPD(pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	nodeInfo, err := p.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Unschedulable, fmt.Sprintf("get node %v from Snapshot: %v", nodeName, err))
	}

	node := nodeInfo.Node()
	if node == nil {
		return 0, framework.NewStatus(framework.Unschedulable, "node not found")
	}
	npd, err := p.npdLister.Get(nodeName)
	if err != nil {
		return 0, nil
	}

	timeStamp := getLoadAwareScopeUpdateTime(npd)
	if p.args.NodeMetricsExpiredSeconds != nil && time.Now().After(timeStamp.Add(time.Duration(*p.args.NodeMetricsExpiredSeconds)*time.Second)) {
		return 0, nil
	}

	loadAwareUsage := p.getLoadAwareResourceList(npd)

	// estimated the recent assign pod usage
	estimatedUsed := estimatedPodUsed(pod, p.args.ResourceToWeightMap, p.args.ResourceToScalingFactorMap)
	estimatedAssignedPodUsage := p.estimatedAssignedPodUsage(nodeName, timeStamp)
	finalEstimatedUsed := quotav1.Add(estimatedUsed, estimatedAssignedPodUsage)
	// add estimated usage to avg_15min_usage
	finalNodeUsedOfIndicators := make(map[config.IndicatorType]v1.ResourceList)
	for indicator := range p.args.CalculateIndicatorWeight {
		if loadAwareUsage != nil {
			used := loadAwareUsage[string(indicator)]
			if indicator == consts.Usage15MinAvgKey {
				used = quotav1.Add(used, finalEstimatedUsed)
			}
			finalNodeUsedOfIndicators[indicator] = used
		}
	}
	score := loadAwareSchedulingScorer(finalNodeUsedOfIndicators, node.Status.Allocatable, p.args.ResourceToWeightMap, p.args.CalculateIndicatorWeight)
	klog.V(6).Infof("loadAware score node: %v resourceUsage: %v, score: %v", node.Name, finalNodeUsedOfIndicators, score)
	return score, nil
}

func (p *Plugin) scoreByPortrait(pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	if pod == nil {
		return framework.MinNodeScore, nil
	}
	nodeInfo, err := p.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Unschedulable, fmt.Sprintf("get node %v from Snapshot: %v", nodeName, err))
	}

	nodePredictUsage, err := p.getNodePredictUsage(pod, nodeName)
	if err != nil {
		klog.Error(err)
		return framework.MinNodeScore, nil
	}

	var scoreSum, weightSum int64

	for _, resourceName := range []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory} {
		targetUsage, ok := p.args.ResourceToTargetMap[resourceName]
		if !ok {
			continue
		}
		weight, ok := p.args.ResourceToWeightMap[resourceName]
		if !ok {
			continue
		}

		total := nodeInfo.Node().Status.Allocatable[resourceName]
		if total.IsZero() {
			continue
		}
		var totalValue int64
		if resourceName == v1.ResourceCPU {
			totalValue = total.MilliValue()
		} else {
			totalValue = total.Value()
		}

		maxUsage := nodePredictUsage.max(resourceName)
		usageRatio := maxUsage / float64(totalValue) * 100

		score, err := targetLoadPacking(float64(targetUsage), usageRatio)
		if err != nil {
			klog.Errorf("pod %v node %v targetLoadPacking fail: %v", pod.Name, nodeName, err)
			return framework.MinNodeScore, nil
		}

		klog.V(6).Infof("loadAware score pod %v, node %v, resource %v, target: %v, maxUsage: %v, total: %v, usageRatio: %v, score: %v",
			pod.Name, nodeInfo.Node().Name, resourceName, targetUsage, maxUsage, totalValue, usageRatio, score)
		scoreSum += score
		weightSum += weight
	}

	if weightSum <= 0 {
		err = fmt.Errorf("resource weight is zero, resourceWightMap: %v", p.args.ResourceToWeightMap)
		klog.Error(err)
		return framework.MinNodeScore, nil
	}
	score := scoreSum / weightSum
	klog.V(6).Infof("loadAware score pod %v, node %v, finalScore: %v",
		pod.Name, nodeInfo.Node().Name, score)
	return score, nil
}

func (p *Plugin) estimatedAssignedPodUsage(nodeName string, updateTime time.Time) v1.ResourceList {
	var (
		estimatedUsed = make(map[v1.ResourceName]int64)
		result        = v1.ResourceList{}
	)
	p.cache.RLock()
	nodeCache, ok := p.cache.NodePodInfo[nodeName]
	p.cache.RUnlock()
	if !ok {
		return result
	}

	nodeCache.RLock()
	defer nodeCache.RUnlock()
	for _, podInfo := range nodeCache.PodInfoMap {
		if isNeedToEstimatedUsage(podInfo, updateTime) {
			estimated := estimatedPodUsed(podInfo.pod, p.args.ResourceToWeightMap, p.args.ResourceToScalingFactorMap)
			for resourceName, quantity := range estimated {
				if resourceName == v1.ResourceCPU {
					estimatedUsed[resourceName] += quantity.MilliValue()
				} else {
					estimatedUsed[resourceName] += quantity.Value()
				}
			}
		}
	}
	// transfer map[ResourceName]int64 to ResourceList
	for resourceName, value := range estimatedUsed {
		if resourceName == v1.ResourceCPU {
			result[resourceName] = *resource.NewMilliQuantity(value, resource.DecimalSI)
		} else {
			result[resourceName] = *resource.NewQuantity(value, resource.DecimalSI)
		}
	}
	return result
}

func (p *Plugin) getLoadAwareResourceList(npd *v1alpha1.NodeProfileDescriptor) map[string]v1.ResourceList {
	if npd == nil {
		return nil
	}
	res := make(map[string]v1.ResourceList)

	for i := range npd.Status.NodeMetrics {
		if npd.Status.NodeMetrics[i].Scope == loadAwareMetricScope {
			for _, metricValue := range npd.Status.NodeMetrics[i].Metrics {
				key := metricWindowToKey(metricValue.Window)
				if _, ok := res[key]; !ok {
					res[key] = v1.ResourceList{}
				}
				res[key][v1.ResourceName(metricValue.MetricName)] = metricValue.Value
			}
		}
	}

	return res
}

func estimatedPodUsed(pod *v1.Pod, resourceWeights map[v1.ResourceName]int64, scalingFactors map[v1.ResourceName]int64) v1.ResourceList {
	requests, limits := resourceapi.PodRequestsAndLimits(pod)
	estimatedUsed := v1.ResourceList{}
	for resourceName := range resourceWeights {
		value := estimatedUsedByResource(requests, limits, resourceName, scalingFactors[resourceName])
		if resourceName == v1.ResourceCPU {
			estimatedUsed[resourceName] = *resource.NewMilliQuantity(value, resource.DecimalSI)
		} else {
			estimatedUsed[resourceName] = *resource.NewQuantity(value, resource.DecimalSI)
		}
	}
	return estimatedUsed
}

func isNeedToEstimatedUsage(podInfo *PodInfo, updateTime time.Time) bool {
	return podInfo.startTime.After(updateTime)
}

func getLoadAwareScopeUpdateTime(npd *v1alpha1.NodeProfileDescriptor) time.Time {
	// all nodeMetrics in loadAware scope have same timestamp
	for i := range npd.Status.NodeMetrics {
		if npd.Status.NodeMetrics[i].Scope != loadAwareMetricScope {
			continue
		}

		if len(npd.Status.NodeMetrics[i].Metrics) <= 0 {
			break
		}
		return npd.Status.NodeMetrics[i].Metrics[0].Timestamp.Time
	}
	return time.Now()
}

func estimatedUsedByResource(requests, limits v1.ResourceList, resourceName v1.ResourceName, scalingFactor int64) int64 {
	limitQuantity := limits[resourceName]
	requestQuantity := requests[resourceName]
	var quantity resource.Quantity
	if limitQuantity.Cmp(requestQuantity) > 0 {
		scalingFactor = 100
		quantity = limitQuantity
	} else {
		quantity = requestQuantity
	}

	if quantity.IsZero() {
		switch resourceName {
		case v1.ResourceCPU:
			return DefaultMilliCPURequest
		case v1.ResourceMemory:
			return DefaultMemoryRequest
		}
		return 0
	}

	var estimatedUsed int64
	switch resourceName {
	case v1.ResourceCPU:
		estimatedUsed = int64(math.Round(float64(quantity.MilliValue()) * float64(scalingFactor) / 100))
	default:
		estimatedUsed = int64(math.Round(float64(quantity.Value()) * float64(scalingFactor) / 100))
	}
	return estimatedUsed
}

// first calculate cpu/memory score according to avg_15min, max_1hour, max_1day  and its weight
// then calculate final score with cpuScore and memoryScore with its weight
func loadAwareSchedulingScorer(usedOfIndicators map[config.IndicatorType]v1.ResourceList, allocatable v1.ResourceList, resourceWeight map[v1.ResourceName]int64, indicatorRatio map[config.IndicatorType]int64) int64 {
	var nodeScore, weightSum int64
	// cpu and memory weight
	for resourceName, weight := range resourceWeight {
		resourceSumScore := int64(0)
		ratioSum := int64(0)
		// calculate cpu/memory score by avg_15min, max_1hour, max_1day
		for indicatorName, ratio := range indicatorRatio {
			alloc, ok := allocatable[resourceName]
			if !ok {
				continue
			}
			resList := usedOfIndicators[indicatorName]
			if resList == nil {
				continue
			}
			quantity, ok := resList[resourceName]
			if !ok {
				continue
			}
			resourceScore := int64(0)
			if resourceName == v1.ResourceCPU {
				resourceScore = leastUsedScore(quantity.MilliValue(), alloc.MilliValue())
			} else {
				resourceScore = leastUsedScore(quantity.Value(), alloc.Value())
			}
			resourceSumScore += resourceScore * ratio
			ratioSum += ratio
		}
		nodeScore += (resourceSumScore / ratioSum) * weight
		weightSum += weight
	}

	return nodeScore / weightSum
}

func leastUsedScore(used, capacity int64) int64 {
	if capacity == 0 {
		return 0
	}
	if used > capacity {
		return 0
	}
	return ((capacity - used) * framework.MaxNodeScore) / capacity
}

func metricWindowToKey(window *metav1.Duration) string {
	if window.Duration == 5*time.Minute {
		return consts.Usage5MinAvgKey
	}
	if window.Duration == 15*time.Minute {
		return consts.Usage15MinAvgKey
	}
	if window.Duration == time.Hour {
		return consts.Usage1HourMaxKey
	}
	if window.Duration == 24*time.Hour {
		return consts.Usage1DayMaxKey
	}
	return ""
}
