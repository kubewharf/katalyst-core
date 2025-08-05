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

package memorybandwidth

import (
	"fmt"
	"math"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const HintOptimizerNameMemoryBandwidth = "memory_bandwidth"

// hintMemoryBandwidthAvailability holds the remaining allocatable memory bandwidth for a topology hint
// and its original index in the list of hints.
type hintMemoryBandwidthAvailability struct {
	RemainingBandwidth int  // Remaining memory bandwidth that can be allocated for this hint.
	OriginalHintIndex  int  // Original index of this hint in the input pluginapi.ListOfTopologyHints.Hints array.
	Ignore             bool // Ignore this hint.
}

// memoryBandwidthOptimizer implements the hintoptimizer.HintOptimizer interface.
// It optimizes topology hints based on memory bandwidth considerations, aiming to select hints
// that provide sufficient memory bandwidth for pods while considering overall system utilization.
type memoryBandwidthOptimizer struct {
	metaServer         *metaserver.MetaServer // metaServer provides access to pod and node metadata, including SPD information.
	emitter            metrics.MetricEmitter  // emitter is used for emitting metrics related to the optimizer's operation.
	state              state.State            // state is used to access the cpu plugin state.
	ignoreNegativeHint bool                   // ignoreNegativeHint, if true, causes hints that would result in negative remaining bandwidth to be ignored.
	preferSpreading    bool                   // preferSpreading influences sorting: if true, hints with more available bandwidth are preferred (spreading); otherwise, hints with less (but non-negative) available bandwidth are preferred (packing).
}

func (o *memoryBandwidthOptimizer) Run(<-chan struct{}) {}

// NewMemoryBandwidthHintOptimizer creates a new instance of memoryBandwidthOptimizer.
// It initializes the optimizer with the necessary components like MetaServer, KatalystMachineInfo, and MetricEmitter.
func NewMemoryBandwidthHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	return &memoryBandwidthOptimizer{
		metaServer:         options.MetaServer,
		emitter:            options.Emitter,
		state:              options.State,
		ignoreNegativeHint: options.Conf.MemoryBandwidthIgnoreNegativeHint,
		preferSpreading:    options.Conf.MemoryBandwidthPreferSpreading,
	}, nil
}

// OptimizeHints adjusts the preference of topology hints based on memory bandwidth availability.
// It aims to select hints that offer sufficient memory bandwidth for the requesting pod, considering current allocations and system capacity.
// The process involves:
// 1. Checking for valid state, request, and hints.
// 2. Getting the currently allocated memory bandwidth on each NUMA node.
// 3. Preparing pod metadata for fetching SPD (Service Profile Descriptor) information.
// 4. Getting the container's memory bandwidth request based on its CPU request and SPD.
// 5. Collecting indexes of hints that are currently marked as preferred.
// 6. Calculating the remaining allocatable memory bandwidth for each preferred hint.
// 7. Sorting the hints based on their memory bandwidth availability and other criteria.
// 8. Re-evaluating and setting the 'Preferred' status on the original hints list.
func (o *memoryBandwidthOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints)
	if err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}

	// Prepare pod metadata for fetching SPD (Service Profile Descriptor) information
	podMeta := o.preparePodMeta(request)

	// Get the container's memory bandwidth request based on its CPU request and SPD
	containerMemoryBandwidthRequest, err := spd.GetContainerMemoryBandwidthRequest(o.metaServer,
		podMeta, int(math.Ceil(request.CPURequest)))
	if err != nil {
		general.Errorf("GetContainerMemoryBandwidthRequest for pod %s/%s failed: %v", request.PodNamespace, request.PodName, err)
		// Continue without memory bandwidth optimization if we can't get container's request
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	general.InfoS("GetContainerMemoryBandwidthRequest result",
		"podNamespace", request.PodNamespace,
		"podName", request.PodName,
		"containerMemoryBandwidthRequest", containerMemoryBandwidthRequest)
	if containerMemoryBandwidthRequest <= 0 {
		general.Errorf("GetContainerMemoryBandwidthRequest for pod %s/%s returns no larger than 0", request.PodNamespace, request.PodName)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	// Get the currently allocated memory bandwidth on each NUMA node
	numaAllocatedMemBW, err := o.getNUMAAllocatedMemBW(o.state.GetMachineState())
	if err != nil {
		general.Errorf("getNUMAAllocatedMemBW failed for pod %s/%s: %v", request.PodNamespace, request.PodName, err)
		_ = o.emitter.StoreInt64(qrmutil.MetricNameGetNUMAAllocatedMemBWFailed, 1, metrics.MetricTypeNameRaw)
		// Continue without memory bandwidth optimization if we can't get allocated bandwidth
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	general.InfoS("getNUMAAllocatedMemBW result",
		"podNamespace", request.PodNamespace,
		"podName", request.PodName,
		"numaAllocatedMemBW", numaAllocatedMemBW)

	// Collect indexes of hints that are currently marked as preferred
	preferredHintIndexes := o.getPreferredHintIndexes(hints)

	// If no hints are initially preferred, there's nothing to optimize among them
	if len(preferredHintIndexes) == 0 {
		general.Infof("no preferred hints to left for pod %s/%s", request.PodNamespace, request.PodName)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	// Calculate memory bandwidth availability for each preferred hint
	hintAvailabilityList, err := o.calculateHintAvailabilityList(request, hints, preferredHintIndexes, containerMemoryBandwidthRequest, numaAllocatedMemBW, podMeta)
	if err != nil {
		return err // Error already logged in calculateMemBWLeftAllocatableList or it's a simple fmt.Errorf
	}

	// Sort the list of allocatable bandwidths based on the defined logic
	o.sortMemBWLeftAllocatableList(hintAvailabilityList, hints)

	// Re-evaluate and set the 'Preferred' status on the original hints list
	o.reevaluatePreferredHints(request, hints, hintAvailabilityList)

	return nil
}

// getNUMAAllocatedMemBW calculates the total allocated memory bandwidth on each NUMA node based on the current machine state.
// It iterates through all pods and their containers, sums up their memory bandwidth requests, and distributes them among the NUMA nodes they are bound to.
func (o *memoryBandwidthOptimizer) getNUMAAllocatedMemBW(machineState state.NUMANodeMap) (map[int]int, error) {
	if o.metaServer == nil {
		return nil, fmt.Errorf("getNUMAAllocatedMemBW got nil metaServer")
	}

	numaAllocatedMemBW := make(map[int]int)
	podUIDToMemBWReq := make(map[string]int)
	podUIDToBindingNUMAs := make(map[string]sets.Int)

	for numaID, numaState := range machineState {
		if numaState == nil {
			general.Errorf("numaState is nil, NUMA: %d", numaID)
			continue
		}
		for podUID, containerEntries := range numaState.PodEntries {
			for _, allocationInfo := range containerEntries {
				if allocationInfo == nil {
					continue
				}

				if !(allocationInfo.CheckNUMABinding() && allocationInfo.CheckMainContainer()) {
					continue
				}

				if _, found := podUIDToMemBWReq[allocationInfo.PodUid]; !found {
					containerMemoryBandwidthRequest, err := spd.GetContainerMemoryBandwidthRequest(o.metaServer, metav1.ObjectMeta{
						UID:         types.UID(allocationInfo.PodUid),
						Namespace:   allocationInfo.PodNamespace,
						Name:        allocationInfo.PodName,
						Labels:      allocationInfo.Labels, // Use labels/annotations from allocationInfo
						Annotations: allocationInfo.Annotations,
					}, int(math.Ceil(cpuutil.GetContainerRequestedCores(o.metaServer, allocationInfo)))) // RequestQuantity from current allocationInfo is fine
					if err != nil {
						return nil, fmt.Errorf("GetContainerMemoryBandwidthRequest for pod: %s/%s, container: %s failed: %v",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
					}
					podUIDToMemBWReq[allocationInfo.PodUid] = containerMemoryBandwidthRequest
				}

				if podUIDToBindingNUMAs[podUID] == nil { // Use podUID from outer loop key
					podUIDToBindingNUMAs[podUID] = sets.NewInt()
				}
				podUIDToBindingNUMAs[podUID].Insert(numaID)
			}
		}
	}

	for podUID, numaSet := range podUIDToBindingNUMAs {
		podMemBWReq, found := podUIDToMemBWReq[podUID]
		if !found {
			// This might happen if we couldn't get fullPodAllocationInfo earlier
			general.Warningf("pod: %s is found in podUIDToBindingNUMAs, but not found in podUIDToMemBWReq, skipping", podUID)
			continue
		}

		numaCount := numaSet.Len()
		if numaCount == 0 {
			continue
		}

		perNUMAMemoryBandwidthRequest := podMemBWReq / numaCount
		for _, numaID := range numaSet.UnsortedList() {
			numaAllocatedMemBW[numaID] += perNUMAMemoryBandwidthRequest
		}
	}

	return numaAllocatedMemBW, nil
}

// calculateHintMemoryBandwidthAvailability calculates the memory bandwidth availability for a given topology hint.
// It considers the container's memory bandwidth request, the currently allocated bandwidth on NUMA nodes, and the machine's topology information.
// The function returns a hintMemoryBandwidthAvailability object containing the remaining allocatable bandwidth and the original hint index.
func (o *memoryBandwidthOptimizer) calculateHintMemoryBandwidthAvailability(
	hints *pluginapi.ListOfTopologyHints,
	hintIndex int, // Index of the current hint
	containerMemoryBandwidthRequest int, // Request of the current container being placed
	numaAllocatedMemBW map[int]int, // Allocated BW on each NUMA node by other containers
	machineInfo *machine.KatalystMachineInfo,
	podMeta metav1.ObjectMeta,
) (*hintMemoryBandwidthAvailability, error) {
	if hints == nil || len(hints.Hints[hintIndex].Nodes) == 0 {
		return nil, fmt.Errorf("got empty numaHintNodes")
	}

	result := &hintMemoryBandwidthAvailability{
		OriginalHintIndex:  hintIndex,
		RemainingBandwidth: math.MaxInt, // Initialize with a large value to correctly find the minimum
	}

	targetNUMANodes := make([]int, len(hints.Hints[hintIndex].Nodes))
	for i, numaID := range hints.Hints[hintIndex].Nodes {
		var err error
		targetNUMANodes[i], err = general.CovertUInt64ToInt(numaID)
		if err != nil {
			return nil, fmt.Errorf("convert NUMA: %d to int failed with error: %v", numaID, err)
		}
	}

	perNUMAMemoryBandwidthRequest := containerMemoryBandwidthRequest / len(targetNUMANodes)
	copiedNUMAAllocatedMemBW := general.DeepCopyIntToIntMap(numaAllocatedMemBW)

	for _, numaID := range targetNUMANodes {
		copiedNUMAAllocatedMemBW[numaID] += perNUMAMemoryBandwidthRequest
	}

	// aggregate each target NUMA and all its sibling NUMAs into a group.
	// calculate allocated and allocable memory bandwidth for each group.
	// currently, if there is one group whose allocated memory bandwidth is greater than its allocatable memory bandwidth,
	// we will set preferrence of the hint to false.
	// for the future, we can gather group statistics of each hint,
	// and to get the most suitable hint, then set its preferrence to true.
	visNUMAs := sets.NewInt()
	numaMBWCapacityMap := helper.GetNumaAvgMBWCapacityMap(o.metaServer.MetricsFetcher, machineInfo.ExtraTopologyInfo.SiblingNumaAvgMBWCapacityMap)
	numaMBWAllocatableMap := helper.GetNumaAvgMBWAllocatableMap(o.metaServer.MetricsFetcher, machineInfo.ExtraTopologyInfo.SiblingNumaInfo, numaMBWCapacityMap)
	for _, numaID := range targetNUMANodes {
		if visNUMAs.Has(numaID) {
			continue
		}

		groupNUMAsAllocatableMemBW := int(numaMBWAllocatableMap[numaID])
		groupNUMAsAllocatedMemBW := copiedNUMAAllocatedMemBW[numaID]
		visNUMAs.Insert(numaID)
		for _, siblingNUMAID := range machineInfo.ExtraTopologyInfo.SiblingNumaMap[numaID].UnsortedList() {
			groupNUMAsAllocatedMemBW += copiedNUMAAllocatedMemBW[siblingNUMAID]
			groupNUMAsAllocatableMemBW += int(numaMBWAllocatableMap[siblingNUMAID])
			visNUMAs.Insert(siblingNUMAID)
		}

		general.InfoS("getPreferenceByMemBW",
			"podNamespace", podMeta.Namespace,
			"podName", podMeta.Name,
			"targetNUMANodes", targetNUMANodes,
			"groupNUMAsAllocatedMemBW", groupNUMAsAllocatedMemBW,
			"groupNUMAsAllocatableMemBW", groupNUMAsAllocatableMemBW)

		result.RemainingBandwidth = general.Min(result.RemainingBandwidth, groupNUMAsAllocatableMemBW-groupNUMAsAllocatedMemBW)
	}

	return result, nil
}

// preparePodMeta creates a metav1.ObjectMeta object from the hintoptimizer.Request.
// This metadata is used for fetching SPD (Service Profile Descriptor) information.
func (o *memoryBandwidthOptimizer) preparePodMeta(optReq hintoptimizer.Request) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		UID:         types.UID(optReq.PodUid),
		Namespace:   optReq.PodNamespace,
		Name:        optReq.PodName,
		Labels:      optReq.Labels, // Use labels/annotations from the request
		Annotations: optReq.Annotations,
	}
}

// getPreferredHintIndexes iterates through the ListOfTopologyHints and returns a slice
// containing the indexes of all hints that are currently marked as 'Preferred'.
func (o *memoryBandwidthOptimizer) getPreferredHintIndexes(hints *pluginapi.ListOfTopologyHints) []int {
	var preferredHintIndexes []int
	for i, hint := range hints.Hints {
		if hint.Preferred {
			preferredHintIndexes = append(preferredHintIndexes, i)
		}
	}
	return preferredHintIndexes
}

// calculateHintAvailabilityList calculates the remaining allocatable memory bandwidth for each preferred hint.
// It iterates through the preferred hints, calculates their individual memory bandwidth availability using calculateHintMemoryBandwidthAvailability,
// and collects them into a list. If a hint's calculation fails, it's assigned a very low availability score.
// Hints that would result in negative remaining bandwidth might be skipped based on the `ignoreNegativeHint` flag.
func (o *memoryBandwidthOptimizer) calculateHintAvailabilityList(optReq hintoptimizer.Request, hints *pluginapi.ListOfTopologyHints, preferredHintIndexes []int, containerMemoryBandwidthRequest int, numaAllocatedMemBW map[int]int, podMeta metav1.ObjectMeta) ([]*hintMemoryBandwidthAvailability, error) {
	allIgnore := true
	hintAvailabilityList := make([]*hintMemoryBandwidthAvailability, 0, len(preferredHintIndexes))
	for _, i := range preferredHintIndexes {
		// Skip hints with no associated NUMA nodes
		if len(hints.Hints[i].Nodes) == 0 {
			continue
		}

		// Calculate memory bandwidth availability for the current hint
		hintAvailability, preferenceErr := o.calculateHintMemoryBandwidthAvailability(
			hints, i, containerMemoryBandwidthRequest,
			numaAllocatedMemBW, o.metaServer.KatalystMachineInfo, podMeta)
		if preferenceErr != nil {
			general.Errorf("calculateHintMemoryBandwidthAvailability for pod %s/%s, hint nodes %v failed: %v",
				optReq.PodNamespace, optReq.PodName, hints.Hints[i].Nodes, preferenceErr)
			_ = o.emitter.StoreInt64(qrmutil.MetricNameGetMemBWPreferenceFailed, 1, metrics.MetricTypeNameRaw)
			// Assign a very low score if calculation fails, making it less preferred
			hintAvailability = &hintMemoryBandwidthAvailability{
				RemainingBandwidth: math.MinInt,
				OriginalHintIndex:  i,
			}
		}

		// Optionally skip hints that would result in negative left allocatable bandwidth
		if o.ignoreNegativeHint && hintAvailability.RemainingBandwidth < 0 {
			general.Warningf("ignoreNegativeHint is true, remaining bandwidth %v is negative, skip hint: %+v",
				hintAvailability.RemainingBandwidth,
				hints.Hints[i].Nodes)
			hintAvailability.Ignore = true
		} else {
			allIgnore = false
		}

		hintAvailabilityList = append(hintAvailabilityList, hintAvailability)
		general.Infof("hint: %+v, remaining bandwidth: %d",
			hints.Hints[i].Nodes, hintAvailability.RemainingBandwidth)
	}

	if allIgnore {
		return nil, cpuutil.ErrNoAvailableMemoryBandwidthHints
	}

	return hintAvailabilityList, nil
}

// sortMemBWLeftAllocatableList sorts a list of hintMemoryBandwidthAvailability objects.
// The sorting criteria are, in order of priority:
// 1. Remaining allocatable memory bandwidth (ascending or descending based on `preferSpreading`).
//   - If not spreading (packing), non-negative values are preferred over negative values.
//
// 2. Current 'Preferred' status (already preferred hints come first).
// 3. Number of NUMA nodes in the hint (fewer nodes are preferred to reduce fragmentation).
// 4. Original hint index (for stability).
func (o *memoryBandwidthOptimizer) sortMemBWLeftAllocatableList(hintAvailabilityList []*hintMemoryBandwidthAvailability, hints *pluginapi.ListOfTopologyHints) {
	sort.Slice(hintAvailabilityList, func(i, j int) bool {
		// Primary sort: by remaining allocatable memory bandwidth.
		if hintAvailabilityList[i].RemainingBandwidth != hintAvailabilityList[j].RemainingBandwidth {
			if o.preferSpreading { // If spreading, prefer hints with more remaining allocatable bandwidth (descending).
				return hintAvailabilityList[i].RemainingBandwidth > hintAvailabilityList[j].RemainingBandwidth
			} else { // If not spreading (packing), prefer hints with less remaining allocatable bandwidth (ascending).
				if hintAvailabilityList[i].RemainingBandwidth >= 0 && hintAvailabilityList[j].RemainingBandwidth >= 0 {
					// If both are non-negative, sort by value.
					return hintAvailabilityList[i].RemainingBandwidth < hintAvailabilityList[j].RemainingBandwidth
				} else {
					// If one is negative and the other is non-negative, sort by value.
					return hintAvailabilityList[i].RemainingBandwidth > hintAvailabilityList[j].RemainingBandwidth
				}
			}
		}

		// Secondary sort: by current 'Preferred' status. Already preferred hints come first.
		if hints.Hints[hintAvailabilityList[i].OriginalHintIndex].Preferred != hints.Hints[hintAvailabilityList[j].OriginalHintIndex].Preferred {
			return hints.Hints[hintAvailabilityList[i].OriginalHintIndex].Preferred // true (preferred) comes before false
		}

		// Tertiary sort: by the number of NUMA nodes. Hints with fewer nodes are preferred (less fragmentation).
		if len(hints.Hints[hintAvailabilityList[i].OriginalHintIndex].Nodes) != len(hints.Hints[hintAvailabilityList[j].OriginalHintIndex].Nodes) {
			return len(hints.Hints[hintAvailabilityList[i].OriginalHintIndex].Nodes) < len(hints.Hints[hintAvailabilityList[j].OriginalHintIndex].Nodes)
		}

		// Quaternary sort: by original hint index for stability.
		return hintAvailabilityList[i].OriginalHintIndex < hintAvailabilityList[j].OriginalHintIndex
	})
}

// reevaluatePreferredHints updates the 'Preferred' status of hints in the original ListOfTopologyHints.
// It iterates through the sorted list of hintMemoryBandwidthAvailability.
// A hint is marked as preferred if its RemainingBandwidth is non-negative, indicating sufficient memory bandwidth.
func (o *memoryBandwidthOptimizer) reevaluatePreferredHints(optReq hintoptimizer.Request, hints *pluginapi.ListOfTopologyHints, hintAvailabilityList []*hintMemoryBandwidthAvailability) {
	reevaluatedPreferredHints := make([]*pluginapi.TopologyHint, 0, len(hintAvailabilityList))
	general.Infof("origin hints: %+v", hints.Hints)
	for _, availability := range hintAvailabilityList {
		if availability.Ignore {
			general.Infof("ignore hint availability: %+v", availability)
			continue
		}

		hint := hints.Hints[availability.OriginalHintIndex]
		// If the remaining allocatable memory bandwidth is non-negative, it meets the criteria.
		if availability.RemainingBandwidth >= 0 {
			hint.Preferred = true
			general.Infof("Pod %s/%s: Re-preferred hint on NUMAs %v due to sufficient memory bandwidth availability (remaining: %d)",
				optReq.PodNamespace, optReq.PodName, hints.Hints[availability.OriginalHintIndex].Nodes, availability.RemainingBandwidth)
		} else {
			// If not meeting the criteria, ensure it's marked as not preferred.
			hint.Preferred = false
		}
		reevaluatedPreferredHints = append(reevaluatedPreferredHints, hint)
	}
	general.Infof("re-preferred hints: %+v", reevaluatedPreferredHints)
	hints.Hints = reevaluatedPreferredHints
}

var _ hintoptimizer.HintOptimizer = &memoryBandwidthOptimizer{}
