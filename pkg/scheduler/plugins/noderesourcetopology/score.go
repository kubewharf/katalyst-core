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

package noderesourcetopology

import (
	"context"
	"fmt"

	"gonum.org/v1/gonum/stat"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

const (
	defaultWeight = int64(1)
)

type scoreStrategyFn func(v1.ResourceList, v1.ResourceList, resourceToWeightMap, sets.String) int64

// resourceToWeightMap contains resource name and weight.
type resourceToWeightMap map[v1.ResourceName]int64

// weight return the weight of the resource and defaultWeight if weight not specified
func (rw resourceToWeightMap) weight(r v1.ResourceName) int64 {
	w, ok := (rw)[r]
	if !ok {
		return defaultWeight
	}

	if w < 1 {
		return defaultWeight
	}

	return w
}

func (tm *TopologyMatch) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	if !tm.topologyMatchSupport(pod) {
		klog.V(5).Infof("pod %v not support topology match", pod.Name)
		return framework.MaxNodeScore, nil
	}

	var (
		nodeInfo          *framework.NodeInfo
		err               error
		nodeResourceCache *cache.ResourceTopology
	)
	nodeInfo, err = tm.sharedLister.NodeInfos().Get(nodeName)
	if err != nil {
		klog.Errorf("get node %v from snapshot fail: %v", nodeName, err)
		return 0, framework.AsStatus(err)
	}
	if consts.ResourcePluginPolicyNameDynamic == tm.resourcePolicy {
		// only dedicated pods will participate in the calculation
		nodeResourceCache = cache.GetCache().GetNodeResourceTopology(nodeName, tm.dedicatedPodsFilter(nodeInfo))
	} else {
		nodeResourceCache = cache.GetCache().GetNodeResourceTopology(nodeName, nil)
	}

	if nodeResourceCache == nil {
		klog.Warningf("node %s nodeCache is nil", nodeName)
		return 0, nil
	}
	handler := tm.scoringHandler(pod, nodeResourceCache)
	if handler == nil {
		klog.V(5).Infof("pod %v not match scoring handler", pod.Name)
		return 0, nil
	}

	return handler(pod, nodeResourceCache.TopologyZone)
}

func (tm *TopologyMatch) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

func (tm *TopologyMatch) scoringHandler(pod *v1.Pod, nodeResourceTopology *cache.ResourceTopology) scoringFn {
	if tm.resourcePolicy == consts.ResourcePluginPolicyNameNative {
		// only single-numa-node supported
		if qos.GetPodQOS(pod) == v1.PodQOSGuaranteed && util.IsRequestFullCPU(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodePodLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone) (int64, *framework.Status) {
					return podScopeScore(pod, zones, tm.scoreStrategyFunc, tm.resourceToWeightMap)
				}
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone) (int64, *framework.Status) {
					return containerScopeScore(pod, zones, tm.scoreStrategyFunc, tm.resourceToWeightMap, scoreForEachNUMANode, nil)
				}
			default:
				// not support
				return nil
			}
		}
	}

	if tm.resourcePolicy == consts.ResourcePluginPolicyNameDynamic {
		// dedicated_cores + numa_binding
		if util.IsDedicatedPod(pod) && util.IsNumaBinding(pod) && !util.IsExclusive(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel,
				v1alpha1.TopologyPolicyNumericContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone) (int64, *framework.Status) {
					return containerScopeScore(pod, zones, tm.scoreStrategyFunc, tm.resourceToWeightMap, scoreForEachNUMANode, tm.alignedResources)
				}
			default:
				return nil
			}
		}

		if util.IsDedicatedPod(pod) && util.IsNumaBinding(pod) && util.IsExclusive(pod) {
			switch nodeResourceTopology.TopologyPolicy {
			case v1alpha1.TopologyPolicySingleNUMANodeContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone) (int64, *framework.Status) {
					return containerScopeScore(pod, zones, tm.scoreStrategyFunc, tm.resourceToWeightMap, scoreForEachNUMANode, tm.alignedResources)
				}

			case v1alpha1.TopologyPolicyNumericContainerLevel:
				return func(pod *v1.Pod, zones []*v1alpha1.TopologyZone) (int64, *framework.Status) {
					return containerScopeScore(pod, zones, tm.scoreStrategyFunc, tm.resourceToWeightMap, scoreForNUMANodes, tm.alignedResources)
				}
			default:
				return nil
			}
		}
	}

	return nil
}

func containerScopeScore(
	pod *v1.Pod,
	nodeTopologyZones []*v1alpha1.TopologyZone,
	scorerFn scoreStrategyFn,
	resourceToWeightMap resourceToWeightMap,
	numaScoreFunc NUMAScoreFunc,
	alignedResource sets.String,
) (int64, *framework.Status) {
	var (
		containers  = append(pod.Spec.InitContainers, pod.Spec.Containers...)
		contScore   = make([]float64, len(containers))
		isExclusive = util.IsExclusive(pod)
	)

	NUMANodeList := TopologyZonesToNUMANodeList(nodeTopologyZones)
	for i, container := range containers {
		identifier := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, container.Name)
		contScore[i] = float64(numaScoreFunc(container.Resources.Requests, NUMANodeList, scorerFn, resourceToWeightMap, alignedResource, isExclusive))
		klog.V(6).InfoS("container scope scoring", "container", identifier, "score", contScore[i])
	}
	finalScore := int64(stat.Mean(contScore, nil))
	klog.V(5).InfoS("container scope scoring final node score", "finalScore", finalScore)
	return finalScore, nil
}

func podScopeScore(
	pod *v1.Pod,
	zones []*v1alpha1.TopologyZone,
	scorerFn scoreStrategyFn,
	resourceToWeightMap resourceToWeightMap,
) (int64, *framework.Status) {
	resources := util.GetPodEffectiveRequest(pod)

	NUMANodeList := TopologyZonesToNUMANodeList(zones)
	finalScore := scoreForEachNUMANode(resources, NUMANodeList, scorerFn, resourceToWeightMap, nativeAlignedResources, false)
	klog.V(5).InfoS("pod scope scoring final node score", "finalScore", finalScore)
	return finalScore, nil
}

type NUMAScoreFunc func(v1.ResourceList, NUMANodeList, scoreStrategyFn, resourceToWeightMap, sets.String, bool) int64

func scoreForNUMANodes(requested v1.ResourceList, numaList NUMANodeList, score scoreStrategyFn, resourceToWeightMap resourceToWeightMap, alignedResource sets.String, isExclusive bool) int64 {
	// only alignedResource will be scored under numeric policy
	// score for numaList which satisfy min NUMANodes count for all alignResources.
	minNUMANodeCountForAlignResources := 1
	numaNodeMap := make(map[int]NUMANode)
	numaNodes := make([]int, 0, len(numaList))
	for _, numaNode := range numaList {
		numaNodeMap[numaNode.NUMAID] = numaNode
		numaNodes = append(numaNodes, numaNode.NUMAID)
	}
	for resourceName, quantity := range requested {
		if alignedResource.Has(resourceName.String()) {
			minCount := minNumaNodeCount(resourceName, quantity, numaNodeMap)
			if minNUMANodeCountForAlignResources < minCount {
				minNUMANodeCountForAlignResources = minCount
			}
		}
	}

	scores := make(map[string]int64)
	minScore := int64(0)
	bitmask.IterateBitMasks(numaNodes, func(mask bitmask.BitMask) {
		maskCount := mask.Count()
		// if maskCount is greater than minNUMANodeCountForAlignResources, the hint will not be preferred.
		if maskCount != minNUMANodeCountForAlignResources {
			return
		}

		var resourceAvailable v1.ResourceList
		maskBits := mask.GetBits()
		for _, nodeID := range maskBits {
			numaNode := numaNodeMap[nodeID]
			numaAvailable := numaNode.Available
			if isExclusive {
				numaAvailable = exclusiveAvailable(numaNode, alignedResource)
			}
			resourceAvailable = mergeResourceList(resourceAvailable, numaAvailable)
		}

		numaScore := score(requested, resourceAvailable, resourceToWeightMap, alignedResource)
		if (minScore == 0) || (numaScore != 0 && numaScore < minScore) {
			minScore = numaScore
		}
		scores[mask.String()] = numaScore
	})

	klog.V(6).Infof("numa score result: %v, numaScores: %v", minScore, scores)

	return minScore
}

func scoreForEachNUMANode(requested v1.ResourceList, numaList NUMANodeList, score scoreStrategyFn, resourceToWeightMap resourceToWeightMap, alignedResource sets.String, isExclusive bool) int64 {
	numaScores := make([]int64, len(numaList))
	minScore := int64(0)

	for _, numa := range numaList {
		available := numa.Available
		if isExclusive {
			available = exclusiveAvailable(numa, alignedResource)
		}
		numaScore := score(requested, available, resourceToWeightMap, alignedResource)
		// if NUMA's score is 0, i.e. not fit at all, it won't be taken under consideration by Kubelet.
		if (minScore == 0) || (numaScore != 0 && numaScore < minScore) {
			minScore = numaScore
		}
		numaScores[numa.NUMAID] = numaScore
		klog.V(6).InfoS("numa score result", "numaID", numa.NUMAID, "score", numaScore)
	}
	return minScore
}

func mergeResourceList(a, b v1.ResourceList) v1.ResourceList {
	if a == nil {
		return b.DeepCopy()
	}
	ret := a.DeepCopy()

	for resourceName, quantity := range b {
		q, ok := ret[resourceName]
		if ok {
			q.Add(quantity)
		} else {
			q = quantity.DeepCopy()
		}
		ret[resourceName] = q
	}
	return ret
}

func exclusiveAvailable(numa NUMANode, alignedResource sets.String) v1.ResourceList {
	numaAvailable := numa.Available.DeepCopy()

	for _, resourceName := range alignedResource.UnsortedList() {
		available, ok := numaAvailable[v1.ResourceName(resourceName)]
		if !ok {
			continue
		}
		// if there are resource allocated on numaNode, set available to zero
		if !available.Equal(numa.Allocatable[v1.ResourceName(resourceName)]) {
			numaAvailable[v1.ResourceName(resourceName)] = *resource.NewQuantity(0, available.Format)
		}
	}

	return numaAvailable
}
