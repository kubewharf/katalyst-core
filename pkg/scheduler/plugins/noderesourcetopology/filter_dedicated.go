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
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	pluginv1alpha1 "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

func (tm *TopologyMatch) dedicatedCoresWithNUMABindingSingleNUMANodeContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(5).Infof("dedicated with NUMABinding Single NUMA node container handler for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeInfo.Node().Name)

	return singleNUMAContainerLevelHandler(pod, zones, nodeInfo, tm.resourcePolicy, nativeAlignedResources)
}

func (tm *TopologyMatch) dedicatedCoresWithNUMAExclusiveSingleNUMANodeContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(5).Infof("dedicated with NUMAExclusive Single NUMA node container handler for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeInfo.Node().Name)

	return singleNUMAContainerLevelHandler(pod, zones, nodeInfo, tm.resourcePolicy, tm.alignedResources)
}

func (tm *TopologyMatch) dedicatedCoresWithNUMABindingNumericContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(5).Infof("dedicated with NUMABinding numeric container handler for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeInfo.Node().Name)

	return numericContainerLevelHandler(pod, zones, nodeInfo, tm.resourcePolicy, tm.alignedResources)
}

func (tm *TopologyMatch) dedicatedCoresWithNUMAExclusiveNumericContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo) *framework.Status {
	klog.V(5).Infof("dedicated with NUMAExclusive numeric container handler for pod %s/%s, node %s", pod.Namespace, pod.Name, nodeInfo.Node().Name)

	return numericContainerLevelHandler(pod, zones, nodeInfo, tm.resourcePolicy, tm.alignedResources)
}

func numericContainerLevelHandler(pod *v1.Pod, zones []*v1alpha1.TopologyZone, nodeInfo *framework.NodeInfo, policy consts.ResourcePluginPolicyName, alignedResource sets.String) *framework.Status {
	var (
		NUMANodeMap = TopologyZonesToNUMANodeMap(zones)
		numaBinding = util.IsNumaBinding(pod)
		exclusive   = util.IsExclusive(pod)
	)

	// init containers are skipped both in native and dynamic policy
	// sidecar containers are skipped in dynamic policy
	for _, container := range pod.Spec.Containers {
		containerType, _, err := util.GetContainerTypeAndIndex(pod, &container)
		if err != nil {
			klog.Error(err)
			return framework.NewStatus(framework.Unschedulable, "cannot get container type")
		}
		if policy == consts.ResourcePluginPolicyNameDynamic && containerType == pluginv1alpha1.ContainerType_SIDECAR {
			continue
		}

		resourceTopologyHints, match := resourceAvailable(container.Resources.Requests, NUMANodeMap, nodeInfo, alignedResource, numaBinding, exclusive)
		if !match {
			klog.V(2).InfoS("cannot align container", "name", container.Name, "kind", "app")
			return framework.NewStatus(framework.Unschedulable, "cannot align container")
		}

		// subtract the resources requested by the container from the given NUMAs.
		err = subtractFromNUMAs(NUMANodeMap, resourceTopologyHints, container, alignedResource, numaBinding, exclusive)
		if err != nil {
			klog.Errorf("subtractFromNUMAs fail, container: %s, node: %s, err: %v", container.Name, nodeInfo.Node().Name, err)
			return framework.NewStatus(framework.Error, "subtract resource fail")
		}
	}

	return nil
}

func resourceAvailable(resources v1.ResourceList, numaNodeMap map[int]NUMANode, nodeInfo *framework.NodeInfo, alignedResource sets.String, numaBinding, exclusive bool) (map[string]topologymanager.TopologyHint, bool) {
	resourceBestHints := make(map[string][]topologymanager.TopologyHint)

	for resourceName, quantity := range resources {
		var th []topologymanager.TopologyHint
		if alignedResource.Has(resourceName.String()) {
			th = resourceHints(resourceName, quantity, numaNodeMap, numaBinding, exclusive)
		} else {
			th = resourceHints(resourceName, quantity, numaNodeMap, false, false)
		}
		if th == nil {
			// resource not NUMAAffinity
			continue
		}
		// resource not available, can not be scheduled
		if len(th) == 0 {
			klog.V(4).Infof("resource %s request %v not available for node %s", resourceName, quantity.String(), nodeInfo.Node().Name)
			return nil, false
		}
		resourceBestHints[resourceName.String()] = th
	}

	// merge and choose best hints
	return merge(resourceBestHints, alignedResource)
}

func resourceHints(resourceName v1.ResourceName, quantity resource.Quantity, numaNodeMap map[int]NUMANode, numaBinding, exclusive bool) []topologymanager.TopologyHint {
	if !hasNUMAAffinity(resourceName, numaNodeMap) {
		return nil
	}

	numaNodes := make([]int, 0, len(numaNodeMap))
	for id := range numaNodeMap {
		numaNodes = append(numaNodes, id)
	}
	sort.Ints(numaNodes)

	minNumasCountNeeded := minNumaNodeCount(resourceName, quantity, numaNodeMap)
	numasPerSocket := numaPerSocket(numaNodeMap)
	hints := make([]topologymanager.TopologyHint, 0)
	// calculate resource available for all bits combinations
	bitmask.IterateBitMasks(numaNodes, func(mask bitmask.BitMask) {
		maskCount := mask.Count()
		sockets := sets.NewString()
		// all resource on numa can not satisfy
		if maskCount < minNumasCountNeeded {
			return
		}

		// numaBinding only support one numa node
		if numaBinding && !exclusive && maskCount > 1 {
			return
		}

		var resourceAvailable resource.Quantity
		maskBits := mask.GetBits()
		// add all resource in numaNodes
		for i, nodeID := range maskBits {
			numaNode := numaNodeMap[nodeID]
			numaAvailable := numaNode.Available[resourceName]
			numaAllocatable := numaNode.Allocatable[resourceName]
			sockets.Insert(numaNode.SocketID)
			// numa should be idle for numa exclusive container
			if exclusive && !numaAvailable.Equal(numaAllocatable) {
				return
			}
			if i == 0 {
				resourceAvailable = numaAvailable
			} else {
				resourceAvailable.Add(numaAvailable)
			}
		}

		if resourceAvailable.Cmp(quantity) < 0 {
			return
		}

		// cross socket is not allowed if maskCount less than numaPerSocket
		// only valid for numaBinding resource
		if numaBinding && maskCount <= numasPerSocket && sockets.Len() > 1 {
			return
		}

		hints = append(hints, topologymanager.TopologyHint{
			NUMANodeAffinity: mask,
			Preferred:        maskCount == minNumasCountNeeded,
		})
	})

	return hints
}

func merge(resourceTopologyHints map[string][]topologymanager.TopologyHint, alignedResource sets.String) (map[string]topologymanager.TopologyHint, bool) {
	filteredHints, resourceNames := splitResourceTopologyHint(resourceTopologyHints)

	bestHints := findBestNumericPermutation(filteredHints, getAlignResourceIndexes(resourceNames, alignedResource.UnsortedList()))

	if bestHints == nil {
		return nil, false
	}

	result := make(map[string]topologymanager.TopologyHint)
	for i := range resourceNames {
		result[resourceNames[i]] = bestHints[i]
	}
	return result, true
}

func splitResourceTopologyHint(resourceTopologyHints map[string][]topologymanager.TopologyHint) ([][]topologymanager.TopologyHint, []string) {
	var (
		topologyHints [][]topologymanager.TopologyHint
		resourceNames []string
	)
	for resourceName, hints := range resourceTopologyHints {
		topologyHints = append(topologyHints, hints)
		resourceNames = append(resourceNames, resourceName)
	}

	return topologyHints, resourceNames
}

// findBestNumericPermutation the policy must be consistent with numericPolicy in katalyst enhance-kubelet
func findBestNumericPermutation(filteredHints [][]topologymanager.TopologyHint, alignResourceIndexes []int) []topologymanager.TopologyHint {
	var bestHints []topologymanager.TopologyHint

	iterateAllProviderTopologyHints(filteredHints, func(permutation []topologymanager.TopologyHint) {
		var alignHasPreferred bool
		for i := 0; i < len(alignResourceIndexes)-1; i++ {
			cur := alignResourceIndexes[i]
			next := alignResourceIndexes[i+1]

			if !numaAffinityAligned(permutation[cur], permutation[next]) {
				return
			}
			alignHasPreferred = permutation[cur].Preferred || permutation[next].Preferred
		}
		if len(alignResourceIndexes) == 1 {
			alignHasPreferred = permutation[alignResourceIndexes[0]].Preferred
		}

		if len(alignResourceIndexes) > 0 && !alignHasPreferred {
			return
		}

		if !isSubsetPermutation(permutation) {
			return
		}
		if bestHints == nil {
			bestHints = DeepCopyTopologyHints(permutation)
			return
		}

		// If preferredHint count beside align resources in this permutation is larger than
		// that in current bestHints, always choose more preferredHint permutation.
		if preferredCountBesideAlign(permutation, alignResourceIndexes) >
			preferredCountBesideAlign(bestHints, alignResourceIndexes) {
			bestHints = DeepCopyTopologyHints(permutation)
			return
		}

		// Only Consider permutation that have smaller maxNumaCount than the
		// maxNumaCount in the current bestHint.
		if getMaxNumaCount(permutation) < getMaxNumaCount(bestHints) {
			bestHints = DeepCopyTopologyHints(permutation)
			return
		}
	})

	return bestHints
}

func hasNUMAAffinity(resourceName v1.ResourceName, numaNodeMap map[int]NUMANode) bool {
	for i := range numaNodeMap {
		_, ok := numaNodeMap[i].Capacity[resourceName]
		if ok {
			return true
		}
	}

	// resource not available at numa level, just ignore it
	return false
}

func numaPerSocket(numaNodeMap map[int]NUMANode) int {
	sockets := sets.NewString()
	for i := range numaNodeMap {
		sockets.Insert(numaNodeMap[i].SocketID)
	}
	return len(numaNodeMap) / len(sockets)
}

// numaAffinityAligned checks a,b TopologyHint could be aligned or not.
func numaAffinityAligned(a, b topologymanager.TopologyHint) bool {
	if a.NUMANodeAffinity == nil && b.NUMANodeAffinity == nil {
		return a.Preferred == b.Preferred
	} else if a.NUMANodeAffinity == nil { // b.NUMANodeAffinity != nil
		// if a.Preferred, there is no NUMA preference for A, so it can be aligned with b.
		return a.Preferred
	} else if b.NUMANodeAffinity == nil { // a.NUMANodeAffinity != nil
		// if b.Preferred, there is no NUMA preference for b, so it can be aligned with a.
		return b.Preferred
	}

	return a.NUMANodeAffinity.IsEqual(b.NUMANodeAffinity)
}

// Iterate over all permutations of hints in 'allProviderHints [][]TopologyHint'.
//
// This procedure is implemented as a recursive function over the set of hints
// in 'allproviderHints[i]'. It applies the function 'callback' to each
// permutation as it is found. It is the equivalent of:
//
// for i := 0; i < len(providerHints[0]); i++
//
//	for j := 0; j < len(providerHints[1]); j++
//	    for k := 0; k < len(providerHints[2]); k++
//	        ...
//	        for z := 0; z < len(providerHints[-1]); z++
//	            permutation := []TopologyHint{
//	                providerHints[0][i],
//	                providerHints[1][j],
//	                providerHints[2][k],
//	                ...
//	                providerHints[-1][z]
//	            }
//	            callback(permutation)
func iterateAllProviderTopologyHints(allProviderHints [][]topologymanager.TopologyHint, callback func([]topologymanager.TopologyHint)) {
	// Internal helper function to accumulate the permutation before calling the callback.
	var iterate func(i int, accum []topologymanager.TopologyHint)
	iterate = func(i int, accum []topologymanager.TopologyHint) {
		// Base case: we have looped through all providers and have a full permutation.
		if i == len(allProviderHints) {
			callback(accum)
			return
		}

		// Loop through all hints for provider 'i', and recurse to build
		// the permutation of this hint with all hints from providers 'i++'.
		for j := range allProviderHints[i] {
			iterate(i+1, append(accum, allProviderHints[i][j]))
		}
	}
	iterate(0, []topologymanager.TopologyHint{})
}

// isSubsetPermutation checks whether permutation meets that bigger numa-node hints always
// contain smaller numa-node hints or not.
func isSubsetPermutation(permutation []topologymanager.TopologyHint) bool {
	var filters []topologymanager.TopologyHint
	for _, hint := range permutation {
		if hint.NUMANodeAffinity != nil {
			filters = append(filters, hint)
		}
	}

	// Sort from small numa node count to big count.
	sort.Slice(filters, func(i, j int) bool {
		return filters[i].NUMANodeAffinity.Count() <= filters[j].NUMANodeAffinity.Count()
	})

	for i := 0; i < len(filters)-1; i++ {
		cur := filters[i]
		next := filters[i+1]
		if !bitmask.And(next.NUMANodeAffinity, cur.NUMANodeAffinity).IsEqual(cur.NUMANodeAffinity) {
			return false
		}
	}

	return true
}

// DeepCopyTopologyHints returns deep copied hints of source hints
func DeepCopyTopologyHints(srcHints []topologymanager.TopologyHint) []topologymanager.TopologyHint {
	if srcHints == nil {
		return nil
	}

	dstHints := make([]topologymanager.TopologyHint, 0, len(srcHints))

	return append(dstHints, srcHints...)
}

// preferredCountBesideAlign counts the preferred hints beside align resources.
func preferredCountBesideAlign(hints []topologymanager.TopologyHint, alignIndexes []int) int {
	var result int
	alignIndexesMap := map[int]bool{}
	for _, index := range alignIndexes {
		alignIndexesMap[index] = true
	}
	for i, hint := range hints {
		if _, ok := alignIndexesMap[i]; ok {
			continue
		}
		if hint.Preferred {
			result++
		}
	}
	return result
}

// getMaxNumaCount returns the max numa count in the given hints.
func getMaxNumaCount(permutation []topologymanager.TopologyHint) int {
	var result int
	for _, hint := range permutation {
		if hint.NUMANodeAffinity == nil {
			continue
		}
		if hint.NUMANodeAffinity.Count() > result {
			result = hint.NUMANodeAffinity.Count()
		}
	}
	return result
}

// getAlignResourceIndexes gets align resource indexes in resources array.
func getAlignResourceIndexes(resources []string, alignResourceNames []string) []int {
	resourceIndexes := make(map[string]int)
	for i, rn := range resources {
		resourceIndexes[rn] = i
	}
	var result []int
	for _, align := range alignResourceNames {
		index, ok := resourceIndexes[align]
		if ok {
			result = append(result, index)
		}
	}
	return result
}
