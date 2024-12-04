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

package dynamicpolicy

import (
	"context"
	"fmt"
	"math"
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

var errNoAvailableCPUHints = fmt.Errorf("no available cpu hints")

type memBWHintUpdate struct {
	updatedPreferrence bool
	leftAllocatable    int
}

func (p *DynamicPolicy) sharedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) {
		return p.sharedCoresWithNUMABindingHintHandler(ctx, req)
	}

	// TODO: support sidecar follow main container for non-binding share cores in future
	if req.ContainerType == pluginapi.ContainerType_MAIN {
		ok, err := p.checkNonBindingShareCoresCpuResource(req)
		if err != nil {
			general.Errorf("failed to check share cores cpu resource for pod: %s/%s, container: %s",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("failed to check share cores cpu resource: %q", err)
		}

		if !ok {
			_ = p.emitter.StoreInt64(util.MetricNameShareCoresNoEnoughResourceFailed, 1, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"resource":      v1.ResourceCPU.String(),
				"podNamespace":  req.PodNamespace,
				"podName":       req.PodName,
				"containerName": req.ContainerName,
			})...)
			return nil, errNoAvailableCPUHints
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) reclaimedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	if util.PodInplaceUpdateResizing(req) {
		return nil, fmt.Errorf("not support inplace update resize for reclaimed cores")
	}

	if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) &&
		p.enableReclaimNUMABinding {
		return p.reclaimedCoresWithNUMABindingHintHandler(ctx, req)
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) dedicatedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresHintHandler got nil req")
	}

	if util.PodInplaceUpdateResizing(req) {
		return nil, fmt.Errorf("not support inplace update resize for dedicated cores")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.dedicatedCoresWithNUMABindingHintHandler(ctx, req)
	default:
		return p.dedicatedCoresWithoutNUMABindingHintHandler(ctx, req)
	}
}

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	_, request, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = cpuutil.RegenerateHints(allocationInfo, false)

		// regenerateHints failed. need to clear container record and re-calculate.
		if hints == nil {
			podEntries := p.state.GetPodEntries()
			delete(podEntries[req.PodUid], req.ContainerName)
			if len(podEntries[req.PodUid]) == 0 {
				delete(podEntries, req.PodUid)
			}

			var err error
			machineState, err = generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	}

	// if hints exists in extra state-file, prefer to use them
	if hints == nil {
		availableNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter(
			(*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))

		var extraErr error
		hints, extraErr = util.GetHintsFromExtraStateFile(req.PodName, string(v1.ResourceCPU), p.extraStateFileAbsPath, availableNUMAs)
		if extraErr != nil {
			general.Infof("pod: %s/%s, container: %s GetHintsFromExtraStateFile failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, extraErr)
		}
	}

	// otherwise, calculate hint for container without allocated memory
	if hints == nil {
		var calculateErr error
		// calculate hint for container without allocated cpus
		hints, calculateErr = p.calculateHints(request, machineState, req)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), hints)
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingHintHandler(_ context.Context,
	_ *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// calculateHints is a helper function to calculate the topology hints
// with the given container requests.
func (p *DynamicPolicy) calculateHints(request float64,
	machineState state.NUMANodeMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req in calculateHints")
	}

	numaNodes := make([]int, 0, len(machineState))
	for numaNode := range machineState {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	minNUMAsCountNeeded, _, err := util.GetNUMANodesCountToFitCPUReq(request, p.machineInfo.CPUTopology)
	if err != nil {
		return nil, fmt.Errorf("GetNUMANodesCountToFitCPUReq failed with error: %v", err)
	}

	numaBinding := qosutil.AnnotationsIndicateNUMABinding(req.Annotations)
	numaExclusive := qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations)

	// because it's hard to control memory allocation accurately,
	// we only support numa_binding but not exclusive container with request smaller than 1 NUMA
	if numaBinding && !numaExclusive && minNUMAsCountNeeded > 1 {
		return nil, fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	numasPerSocket, err := p.machineInfo.NUMAsPerSocket()
	if err != nil {
		return nil, fmt.Errorf("NUMAsPerSocket failed with error: %v", err)
	}

	numaToAvailableCPUCount := make(map[int]int, len(numaNodes))

	for _, nodeID := range numaNodes {
		if machineState[nodeID] == nil {
			general.Warningf("NUMA: %d has nil state", nodeID)
			numaToAvailableCPUCount[nodeID] = 0
			continue
		}

		if numaExclusive && machineState[nodeID].AllocatedCPUSet.Size() > 0 {
			numaToAvailableCPUCount[nodeID] = 0
			general.Warningf("numa_exclusive container skip NUMA: %d allocated: %d",
				nodeID, machineState[nodeID].AllocatedCPUSet.Size())
		} else {
			numaToAvailableCPUCount[nodeID] = machineState[nodeID].GetAvailableCPUSet(p.reservedCPUs).Size()
		}
	}

	general.Infof("calculate hints with req: %.3f, numaToAvailableCPUCount: %+v",
		request, numaToAvailableCPUCount)

	numaBound := len(numaNodes)
	if numaBound > machine.LargeNUMAsPoint {
		// [TODO]: to discuss refine minNUMAsCountNeeded+1
		numaBound = minNUMAsCountNeeded + 1
	}

	preferredHintIndexes := []int{}
	var availableNumaHints []*pluginapi.TopologyHint
	machine.IterateBitMasks(numaNodes, numaBound, func(mask machine.BitMask) {
		maskCount := mask.Count()
		if maskCount < minNUMAsCountNeeded {
			return
		} else if numaBinding && !numaExclusive && maskCount > 1 {
			// because it's hard to control memory allocation accurately,
			// we only support numa_binding but not exclusive container with request smaller than 1 NUMA
			return
		}

		maskBits := mask.GetBits()
		numaCountNeeded := mask.Count()

		allAvailableCPUsCountInMask := 0
		for _, nodeID := range maskBits {
			allAvailableCPUsCountInMask += numaToAvailableCPUCount[nodeID]
		}

		if !cpuutil.CPUIsSufficient(request, float64(allAvailableCPUsCountInMask)) {
			return
		}

		crossSockets, err := machine.CheckNUMACrossSockets(maskBits, p.machineInfo.CPUTopology)
		if err != nil {
			return
		} else if numaCountNeeded <= numasPerSocket && crossSockets {
			return
		}

		preferred := maskCount == minNUMAsCountNeeded
		availableNumaHints = append(availableNumaHints, &pluginapi.TopologyHint{
			Nodes:     machine.MaskToUInt64Array(mask),
			Preferred: preferred,
		})

		if preferred {
			preferredHintIndexes = append(preferredHintIndexes, len(availableNumaHints)-1)
		}
	})

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need manage multi resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("calculateHints got no available cpu hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, errNoAvailableCPUHints
	}

	if numaBound > machine.MBWNUMAsPoint {
		numaAllocatedMemBW, err := getNUMAAllocatedMemBW(machineState, p.metaServer, p.getContainerRequestedCores)

		general.InfoS("getNUMAAllocatedMemBW",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"numaAllocatedMemBW", numaAllocatedMemBW)

		if err != nil {
			general.Errorf("getNUMAAllocatedMemBW failed with error: %v", err)
			_ = p.emitter.StoreInt64(util.MetricNameGetNUMAAllocatedMemBWFailed, 1, metrics.MetricTypeNameRaw)
		} else {
			p.updatePreferredCPUHintsByMemBW(preferredHintIndexes, availableNumaHints,
				request, numaAllocatedMemBW, req, numaExclusive)
		}
	}

	hints := map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): {
			Hints: availableNumaHints,
		},
	}

	return hints, nil
}

func (p *DynamicPolicy) reclaimedCoresWithNUMABindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	// calc the hints with the pod aggregated request
	_, request, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	numaHeadroomState := p.state.GetNUMAHeadroom()
	podEntries := p.state.GetPodEntries()

	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = cpuutil.RegenerateHints(allocationInfo, false)
		if hints == nil {
			machineState, err = p.clearContainerAndRegenerateMachineState(podEntries, req)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s clearContainerAndRegenerateMachineState failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	}

	if hints == nil {
		var calculateErr error
		hints, calculateErr = p.calculateHintsForNUMABindingReclaimedCores(request, podEntries, machineState, numaHeadroomState)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHintsForNUMABindingReclaimedCores failed with error: %v", calculateErr)
		}
	}

	general.Infof("memory hints for pod:%s/%s, container: %s success, hints: %v",
		req.PodNamespace, req.PodName, req.ContainerName, hints)

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), hints)
}

func (p *DynamicPolicy) calculateHintsForNUMABindingReclaimedCores(reqFloat float64, podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]float64,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	// Determine the set of NUMA nodes currently hosting non-RNB pods
	nonActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))

	// Calculate the total requested resources for non-RNB reclaimed pods
	nonActualBindingReclaimedRequestedQuantity := state.GetRequestedQuantityFromPodEntries(podEntries,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding),
		p.getContainerRequestedCores)

	// Compute the total available headroom for non-RNB NUMA nodes
	nonActualBindingReclaimedNUMAHeadroom := state.GetReclaimedNUMAHeadroom(numaHeadroomState, nonActualBindingNUMAs)

	// Identify candidate NUMA nodes for RNB (Reclaimed NUMA Binding) cores
	// This includes both RNB NUMA nodes and NUMA nodes that can shrink from the non-RNB set
	candidateNUMANodes := p.filterNUMANodesByNonBindingReclaimedRequestedQuantity(nonActualBindingReclaimedRequestedQuantity,
		nonActualBindingReclaimedNUMAHeadroom, nonActualBindingNUMAs, numaHeadroomState)

	// Sort them based on the other qos numa binding pods and their headroom
	p.sortCandidateNUMANodesForReclaimed(candidateNUMANodes, machineState, numaHeadroomState)

	candidateLeft, maxCPULeft := p.calculateNUMANodesLeft(candidateNUMANodes, machineState, numaHeadroomState, reqFloat)

	hints := &pluginapi.ListOfTopologyHints{}

	nonBindingReclaimedLeft := nonActualBindingReclaimedNUMAHeadroom - nonActualBindingReclaimedRequestedQuantity - reqFloat
	if maxCPULeft >= 0 {
		p.populateBestEffortHintsByAvailableNUMANodes(hints, candidateNUMANodes, candidateLeft,
			0)
	} else if nonBindingReclaimedLeft <= 0 {
		p.populateBestEffortHintsByAvailableNUMANodes(hints, candidateNUMANodes, candidateLeft,
			nonBindingReclaimedLeft)
	}

	general.InfoS("calculate numa hints for reclaimed cores success",
		"nonActualBindingNUMAs", nonActualBindingNUMAs.String(),
		"nonActualBindingReclaimedRequestedQuantity", nonActualBindingReclaimedRequestedQuantity,
		"nonActualBindingReclaimedNUMAHeadroom", nonActualBindingReclaimedNUMAHeadroom,
		"numaHeadroomState", numaHeadroomState,
		"candidateNUMANodes", candidateNUMANodes,
		"nonBindingReclaimedLeft", nonBindingReclaimedLeft,
		"candidateLeft", candidateLeft,
		"maxCPULeft", maxCPULeft)

	// Finally, add non-RNB NUMA nodes as preferred hints, but these will only be selected if no RNB NUMA nodes meet the requirements
	if nonActualBindingNUMAs.Size() > 0 {
		util.PopulatePreferHintsByNUMANodes(hints, nonActualBindingNUMAs.ToSliceInt())
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): hints,
	}, nil
}

func getNUMAAllocatedMemBW(machineState state.NUMANodeMap, metaServer *metaserver.MetaServer, getContainerRequestedCores state.GetContainerRequestedCoresFunc) (map[int]int, error) {
	numaAllocatedMemBW := make(map[int]int)
	podUIDToMemBWReq := make(map[string]int)
	podUIDToBindingNUMAs := make(map[string]sets.Int)

	if metaServer == nil {
		return nil, fmt.Errorf("getNUMAAllocatedMemBW got nil metaServer")
	}

	for numaID, numaState := range machineState {
		if numaState == nil {
			general.Errorf("numaState is nil, NUMA: %d", numaID)
			continue
		}

		for _, entries := range numaState.PodEntries {
			for _, allocationInfo := range entries {
				if !(allocationInfo.CheckDedicatedNUMABinding() && allocationInfo.CheckMainContainer()) {
					continue
				}

				if _, found := podUIDToMemBWReq[allocationInfo.PodUid]; !found {
					containerMemoryBandwidthRequest, err := spd.GetContainerMemoryBandwidthRequest(metaServer, metav1.ObjectMeta{
						UID:         types.UID(allocationInfo.PodUid),
						Namespace:   allocationInfo.PodNamespace,
						Name:        allocationInfo.PodName,
						Labels:      allocationInfo.Labels,
						Annotations: allocationInfo.Annotations,
					}, int(math.Ceil(getContainerRequestedCores(allocationInfo))))
					if err != nil {
						return nil, fmt.Errorf("GetContainerMemoryBandwidthRequest for pod: %s/%s, container: %s failed with error: %v",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
					}

					podUIDToMemBWReq[allocationInfo.PodUid] = containerMemoryBandwidthRequest
				}

				if podUIDToBindingNUMAs[allocationInfo.PodUid] == nil {
					podUIDToBindingNUMAs[allocationInfo.PodUid] = sets.NewInt()
				}

				podUIDToBindingNUMAs[allocationInfo.PodUid].Insert(numaID)
			}
		}
	}

	for podUID, numaSet := range podUIDToBindingNUMAs {
		podMemBWReq, found := podUIDToMemBWReq[podUID]

		if !found {
			return nil, fmt.Errorf("pod: %s is found in podUIDToBindingNUMAs, but not found in podUIDToMemBWReq", podUID)
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

func (p *DynamicPolicy) updatePreferredCPUHintsByMemBW(preferredHintIndexes []int, cpuHints []*pluginapi.TopologyHint, request float64,
	numaAllocatedMemBW map[int]int, req *pluginapi.ResourceRequest, numaExclusive bool,
) {
	if len(preferredHintIndexes) == 0 {
		general.Infof("there is no preferred hints, skip update")
		return
	} else if req == nil {
		general.Errorf("empty req")
		return
	}

	containerMemoryBandwidthRequest, err := spd.GetContainerMemoryBandwidthRequest(p.metaServer,
		metav1.ObjectMeta{
			UID:         types.UID(req.PodUid),
			Namespace:   req.PodNamespace,
			Name:        req.PodName,
			Labels:      req.Labels,
			Annotations: req.Annotations,
		}, int(math.Ceil(request)))
	if err != nil {
		general.Errorf("GetContainerMemoryBandwidthRequest failed with error: %v", err)
		return
	}

	general.InfoS("GetContainerMemoryBandwidthRequest",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerMemoryBandwidthRequest", containerMemoryBandwidthRequest)

	memBWHintUpdates := make([]*memBWHintUpdate, 0, len(preferredHintIndexes))
	allFalse := true
	maxLeftAllocatable := math.MinInt
	for _, i := range preferredHintIndexes {
		if len(cpuHints[i].Nodes) == 0 {
			continue
		}

		memBWHintUpdateResult, err := getPreferenceByMemBW(cpuHints[i].Nodes, containerMemoryBandwidthRequest,
			numaAllocatedMemBW, p.machineInfo,
			p.metaServer, req)
		if err != nil {
			general.Errorf("getPreferenceByMemBW for hints: %#v failed with error: %v", cpuHints[i].Nodes, err)
			_ = p.emitter.StoreInt64(util.MetricNameGetMemBWPreferenceFailed, 1, metrics.MetricTypeNameRaw)
			return
		}

		if memBWHintUpdateResult.updatedPreferrence {
			allFalse = false
		}

		memBWHintUpdates = append(memBWHintUpdates, memBWHintUpdateResult)

		general.Infof("hint: %+v updated preference: %v, leftAllocatable: %d",
			cpuHints[i].Nodes, memBWHintUpdateResult.updatedPreferrence, memBWHintUpdateResult.leftAllocatable)

		maxLeftAllocatable = general.Max(maxLeftAllocatable, memBWHintUpdateResult.leftAllocatable)
	}

	updatePreferredCPUHintsByMemBWInPlace(memBWHintUpdates, allFalse, numaExclusive, cpuHints, preferredHintIndexes, maxLeftAllocatable)
}

func updatePreferredCPUHintsByMemBWInPlace(memBWHintUpdates []*memBWHintUpdate,
	allFalse, numaExclusive bool, cpuHints []*pluginapi.TopologyHint,
	preferredHintIndexes []int, maxLeftAllocatable int,
) {
	if !allFalse {
		general.Infof("not all updated hints indicate false")
		for ui, hi := range preferredHintIndexes {
			if cpuHints[hi].Preferred != memBWHintUpdates[ui].updatedPreferrence {
				general.Infof("set hint: %+v preference from %v to %v", cpuHints[hi].Nodes, cpuHints[hi].Preferred, memBWHintUpdates[ui].updatedPreferrence)
				cpuHints[hi].Preferred = memBWHintUpdates[ui].updatedPreferrence
			}
		}
		return
	}

	general.Infof("all updated hints indicate false")

	if !numaExclusive {
		general.Infof("candidate isn't numa exclusive, keep all preferred hints")
		return
	}

	for ui, hi := range preferredHintIndexes {
		if memBWHintUpdates[ui].leftAllocatable == maxLeftAllocatable {
			general.Infof("hint: %+v with max left allocatable memory bw: %d, set itspreference to true",
				cpuHints[hi].Nodes, maxLeftAllocatable)
			cpuHints[hi].Preferred = true
		} else {
			cpuHints[hi].Preferred = false
		}
	}

	return
}

func getPreferenceByMemBW(targetNUMANodesUInt64 []uint64,
	containerMemoryBandwidthRequest int, numaAllocatedMemBW map[int]int,
	machineInfo *machine.KatalystMachineInfo,
	metaServer *metaserver.MetaServer, req *pluginapi.ResourceRequest,
) (*memBWHintUpdate, error) {
	if req == nil {
		return nil, fmt.Errorf("empty req")
	} else if len(targetNUMANodesUInt64) == 0 {
		return nil, fmt.Errorf("empty targetNUMANodes")
	} else if machineInfo == nil || machineInfo.ExtraTopologyInfo == nil {
		return nil, fmt.Errorf("invalid machineInfo")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	}

	ret := &memBWHintUpdate{
		updatedPreferrence: true,
	}

	targetNUMANodes := make([]int, len(targetNUMANodesUInt64))
	for i, numaID := range targetNUMANodesUInt64 {
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

	groupID := 0
	groupNUMAsAllocatedMemBW := make(map[int]int)
	groupNUMAsAllocatableMemBW := make(map[int]int)
	visNUMAs := sets.NewInt()

	// aggregate each target NUMA and all its sibling NUMAs into a group.
	// calculate allocated and allocable memory bandwidth for each group.
	// currently, if there is one group whose allocated memory bandwidth is greater than its allocatable memory bandwidth,
	// we will set preferrence of the hint to false.
	// for the future, we can gather group statistics of each hint,
	// and to get the most suitable hint, then set its preferrence to true.
	for _, numaID := range targetNUMANodes {
		if visNUMAs.Has(numaID) {
			continue
		}

		groupNUMAsAllocatableMemBW[groupID] += int(machineInfo.ExtraTopologyInfo.SiblingNumaAvgMBWAllocatableMap[numaID])
		groupNUMAsAllocatedMemBW[groupID] += copiedNUMAAllocatedMemBW[numaID]
		visNUMAs.Insert(numaID)
		for _, siblingNUMAID := range machineInfo.ExtraTopologyInfo.SiblingNumaMap[numaID].UnsortedList() {
			groupNUMAsAllocatedMemBW[groupID] += copiedNUMAAllocatedMemBW[siblingNUMAID]
			groupNUMAsAllocatableMemBW[groupID] += int(machineInfo.ExtraTopologyInfo.SiblingNumaAvgMBWAllocatableMap[siblingNUMAID])
			visNUMAs.Insert(siblingNUMAID)
		}

		general.InfoS("getPreferenceByMemBW",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"groupID", groupID,
			"targetNUMANodes", targetNUMANodes,
			"groupNUMAsAllocatedMemBW", groupNUMAsAllocatedMemBW[groupID],
			"groupNUMAsAllocatableMemBW", groupNUMAsAllocatableMemBW[groupID])

		if ret.updatedPreferrence && groupNUMAsAllocatedMemBW[groupID] > groupNUMAsAllocatableMemBW[groupID] {
			ret.updatedPreferrence = false
		}

		ret.leftAllocatable += (groupNUMAsAllocatableMemBW[groupID] - groupNUMAsAllocatedMemBW[groupID])
		groupID++
	}

	return ret, nil
}

func (p *DynamicPolicy) sharedCoresWithNUMABindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	// calc the hints with the pod aggregated request
	_, request, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	podEntries := p.state.GetPodEntries()

	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = cpuutil.RegenerateHints(allocationInfo, util.PodInplaceUpdateResizing(req))

		// clear the current container and regenerate machine state in follow cases:
		// 1. regenerateHints failed.
		// 2. the container is inplace update resizing.
		// hints it as a new container
		if hints == nil {
			machineState, err = p.clearContainerAndRegenerateMachineState(podEntries, req)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s clearContainerAndRegenerateMachineState failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}

		general.Infof("pod: %s/%s, container: %s, inplace update resize: %v", req.PodNamespace, req.PodName, req.ContainerName, util.PodInplaceUpdateResizing(req))
		numaSet := allocationInfo.GetAllocationResultNUMASet()
		if numaSet.Size() != 1 {
			general.Errorf("pod: %s/%s, container: %s is snb, but its numa set size is %d",
				req.PodNamespace, req.PodName, req.ContainerName, numaSet.Size())
			return nil, fmt.Errorf("snb port not support cross numa")
		}
		nodeID := numaSet.ToSliceInt()[0]
		availableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(p.reservedCPUs)

		general.Infof("pod: %s/%s, container: %s request cpu inplace update resize on numa %d (available: %.3f, request: %.3f)",
			req.PodNamespace, req.PodName, req.ContainerName, nodeID, availableCPUQuantity, request)
		if !cpuutil.CPUIsSufficient(request, availableCPUQuantity) { // no left resource to scale out
			general.Infof("pod: %s/%s, container: %s request cpu inplace update resize, but no enough resource for it in current NUMA, checking migratable",
				req.PodNamespace, req.PodName, req.ContainerName)
			// TODO move this var to config
			isInplaceUpdateResizeNumaMigration := false
			if isInplaceUpdateResizeNumaMigration {
				general.Infof("pod: %s/%s, container: %s request inplace update resize and no enough resource in current NUMA, try to migrate it to new NUMA",
					req.PodNamespace, req.PodName, req.ContainerName)
				var calculateErr error
				hints, calculateErr = p.calculateHintsForNUMABindingSharedCores(request, podEntries, machineState, req)
				if calculateErr != nil {
					general.Errorf("pod: %s/%s, container: %s request inplace update resize and no enough resource in current NUMA, failed to migrate it to new NUMA",
						req.PodNamespace, req.PodName, req.ContainerName)
					return nil, fmt.Errorf("calculateHintsForNUMABindingSharedCores failed in inplace update resize mode with error: %v", calculateErr)
				}
			} else {
				general.Errorf("pod: %s/%s, container: %s request inplace update resize, but no enough resource for it in current NUMA",
					req.PodNamespace, req.PodName, req.ContainerName)
				return nil, errNoAvailableCPUHints
			}
		} else {
			general.Infof("pod: %s/%s, container: %s request inplace update resize, there is enough resource for it in current NUMA",
				req.PodNamespace, req.PodName, req.ContainerName)
			hints = cpuutil.RegenerateHints(allocationInfo, false)
		}
	} else {
		var calculateErr error
		hints, calculateErr = p.calculateHintsForNUMABindingSharedCores(request, podEntries, machineState, req)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHintsForNUMABindingSharedCores failed with error: %v", calculateErr)
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), hints)
}

func (p *DynamicPolicy) clearContainerAndRegenerateMachineState(podEntries state.PodEntries, req *pluginapi.ResourceRequest) (state.NUMANodeMap, error) {
	delete(podEntries[req.PodUid], req.ContainerName)
	if len(podEntries[req.PodUid]) == 0 {
		delete(podEntries, req.PodUid)
	}

	var err error
	machineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	return machineState, nil
}

func (p *DynamicPolicy) populateHintsByPreferPolicy(numaNodes []int, preferPolicy string,
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap, request float64,
) {
	preferIndexes, maxLeft, minLeft := []int{}, float64(-1), math.MaxFloat64

	for _, nodeID := range numaNodes {
		availableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(p.reservedCPUs)
		if !cpuutil.CPUIsSufficient(request, availableCPUQuantity) {
			general.Warningf("numa_binding shared_cores container skip NUMA: %d available: %.3f request: %.3f",
				nodeID, availableCPUQuantity, request)
			continue
		}

		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes: []uint64{uint64(nodeID)},
		})

		curLeft := availableCPUQuantity - request

		general.Infof("NUMA: %d, left cpu quantity: %.3f", nodeID, curLeft)

		if preferPolicy == cpuconsts.CPUNUMAHintPreferPolicyPacking {
			if curLeft < minLeft {
				minLeft = curLeft
				preferIndexes = []int{len(hints.Hints) - 1}
			} else if curLeft == minLeft {
				preferIndexes = append(preferIndexes, len(hints.Hints)-1)
			}
		} else {
			if curLeft > maxLeft {
				maxLeft = curLeft
				preferIndexes = []int{len(hints.Hints) - 1}
			} else if curLeft == maxLeft {
				preferIndexes = append(preferIndexes, len(hints.Hints)-1)
			}
		}
	}

	if len(preferIndexes) >= 0 {
		for _, preferIndex := range preferIndexes {
			hints.Hints[preferIndex].Preferred = true
		}
	}
}

func (p *DynamicPolicy) filterNUMANodesByHintPreferLowThreshold(request float64,
	machineState state.NUMANodeMap, numaNodes []int,
) ([]int, []int) {
	filteredNUMANodes := make([]int, 0, len(numaNodes))
	filteredOutNUMANodes := make([]int, 0, len(numaNodes))

	for _, nodeID := range numaNodes {
		availableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(p.reservedCPUs)
		allocatableCPUQuantity := machineState[nodeID].GetFilteredDefaultCPUSet(nil, nil).Difference(p.reservedCPUs).Size()

		if allocatableCPUQuantity == 0 {
			general.Warningf("numa: %d allocatable cpu quantity is zero", nodeID)
			continue
		}

		availableRatio := availableCPUQuantity / float64(allocatableCPUQuantity)

		general.Infof("NUMA: %d, availableCPUQuantity: %.3f, allocatableCPUQuantity: %d, availableRatio: %.3f, cpuNUMAHintPreferLowThreshold:%.3f",
			nodeID, availableCPUQuantity, allocatableCPUQuantity, availableRatio, p.cpuNUMAHintPreferLowThreshold)

		if availableRatio >= p.cpuNUMAHintPreferLowThreshold {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		} else {
			filteredOutNUMANodes = append(filteredOutNUMANodes, nodeID)
		}
	}

	return filteredNUMANodes, filteredOutNUMANodes
}

func (p *DynamicPolicy) filterNUMANodesByNonBindingSharedRequestedQuantity(nonBindingSharedRequestedQuantity,
	nonBindingNUMAsCPUQuantity int,
	nonBindingNUMAs machine.CPUSet,
	machineState state.NUMANodeMap, numaNodes []int,
) []int {
	filteredNUMANodes := make([]int, 0, len(numaNodes))

	for _, nodeID := range numaNodes {
		if nonBindingNUMAs.Contains(nodeID) {
			allocatableCPUQuantity := machineState[nodeID].GetFilteredDefaultCPUSet(nil, nil).Difference(p.reservedCPUs).Size()

			// take this non-binding NUMA for candidate shared_cores with numa_binding,
			// won't cause non-binding shared_cores in short supply
			if nonBindingNUMAsCPUQuantity-allocatableCPUQuantity >= nonBindingSharedRequestedQuantity {
				filteredNUMANodes = append(filteredNUMANodes, nodeID)
			} else {
				general.Infof("filter out NUMA: %d since taking it will cause non-binding shared_cores in short supply;"+
					" nonBindingNUMAsCPUQuantity: %d, targetNUMAAllocatableCPUQuantity: %d, nonBindingSharedRequestedQuantity: %d",
					nodeID, nonBindingNUMAsCPUQuantity, allocatableCPUQuantity, nonBindingSharedRequestedQuantity)
			}
		} else {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		}
	}

	return filteredNUMANodes
}

func (p *DynamicPolicy) calculateHintsForNUMABindingSharedCores(request float64, podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	nonBindingNUMAsCPUQuantity := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding)).Size()
	nonBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))
	nonBindingSharedRequestedQuantity := state.GetNonBindingSharedRequestedQuantityFromPodEntries(podEntries, nil, p.getContainerRequestedCores)

	reqAnnotations := req.Annotations
	numaNodes := p.filterNUMANodesByNonBindingSharedRequestedQuantity(nonBindingSharedRequestedQuantity,
		nonBindingNUMAsCPUQuantity, nonBindingNUMAs, machineState,
		machineState.GetFilteredNUMASetWithAnnotations(state.WrapAllocationMetaFilterWithAnnotations(commonstate.CheckNUMABindingSharedCoresAntiAffinity), reqAnnotations).ToSliceInt())

	hints := &pluginapi.ListOfTopologyHints{}

	minNUMAsCountNeeded, _, err := util.GetNUMANodesCountToFitCPUReq(request, p.machineInfo.CPUTopology)
	if err != nil {
		return nil, fmt.Errorf("GetNUMANodesCountToFitCPUReq failed with error: %v", err)
	}

	// if a numa_binding shared_cores has request larger than 1 NUMA,
	// its performance may degrade to be like non-binding shared_cores
	if minNUMAsCountNeeded > 1 {
		return nil, fmt.Errorf("numa_binding shared_cores container has request larger than 1 NUMA")
	}
	switch p.cpuNUMAHintPreferPolicy {
	case cpuconsts.CPUNUMAHintPreferPolicyPacking, cpuconsts.CPUNUMAHintPreferPolicySpreading:
		general.Infof("apply %s policy on NUMAs: %+v", p.cpuNUMAHintPreferPolicy, numaNodes)
		p.populateHintsByPreferPolicy(numaNodes, p.cpuNUMAHintPreferPolicy, hints, machineState, request)
	case cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking:
		filteredNUMANodes, filteredOutNUMANodes := p.filterNUMANodesByHintPreferLowThreshold(request, machineState, numaNodes)

		if len(filteredNUMANodes) > 0 {
			general.Infof("dynamically apply packing policy on NUMAs: %+v", filteredNUMANodes)
			p.populateHintsByPreferPolicy(filteredNUMANodes, cpuconsts.CPUNUMAHintPreferPolicyPacking, hints, machineState, request)
			p.populateHintsByAvailableNUMANodes(filteredOutNUMANodes, hints, false)
		} else {
			general.Infof("empty filteredNUMANodes, dynamically apply spreading policy on NUMAs: %+v", numaNodes)
			p.populateHintsByPreferPolicy(numaNodes, cpuconsts.CPUNUMAHintPreferPolicySpreading, hints, machineState, request)
		}
	default:
		general.Infof("unknown policy: %s, apply default spreading policy on NUMAs: %+v", p.cpuNUMAHintPreferPolicy, numaNodes)
		p.populateHintsByPreferPolicy(numaNodes, cpuconsts.CPUNUMAHintPreferPolicySpreading, hints, machineState, request)
	}

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need manage multi resource in one plugin.
	if len(hints.Hints) == 0 {
		general.Warningf("calculateHints got no available cpu hints for snb pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, errNoAvailableCPUHints
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): hints,
	}, nil
}

func (p *DynamicPolicy) populateHintsByAvailableNUMANodes(numaNodes []int,
	hints *pluginapi.ListOfTopologyHints, preferred bool,
) {
	for _, nodeID := range numaNodes {
		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes:     []uint64{uint64(nodeID)},
			Preferred: preferred,
		})
	}
}

func (p *DynamicPolicy) filterNUMANodesByNonBindingReclaimedRequestedQuantity(nonBindingReclaimedRequestedQuantity,
	nonBindingNUMAsCPUQuantity float64,
	nonBindingNUMAs machine.CPUSet,
	numaHeadroomState map[int]float64,
) []int {
	candidateNUMANodes := make([]int, 0, len(numaHeadroomState))
	for numaID, headroom := range numaHeadroomState {
		if headroom > 0 {
			candidateNUMANodes = append(candidateNUMANodes, numaID)
		}
	}

	filteredNUMANodes := make([]int, 0, len(candidateNUMANodes))
	for _, nodeID := range candidateNUMANodes {
		if nonBindingNUMAs.Contains(nodeID) {
			allocatableCPUQuantity := numaHeadroomState[nodeID]
			// take this non-binding NUMA for candidate reclaimed_cores with numa_binding,
			// won't cause non-actual numa binding reclaimed_cores in short supply
			if cpuutil.CPUIsSufficient(nonBindingReclaimedRequestedQuantity, nonBindingNUMAsCPUQuantity-allocatableCPUQuantity) {
				filteredNUMANodes = append(filteredNUMANodes, nodeID)
			} else {
				general.Infof("filter out NUMA: %d since taking it will cause normal reclaimed_cores in short supply;"+
					" nonBindingNUMAsCPUQuantity: %.3f, targetNUMAAllocatableCPUQuantity: %.3f, nonBindingReclaimedRequestedQuantity: %.3f",
					nodeID, nonBindingNUMAsCPUQuantity, allocatableCPUQuantity, nonBindingReclaimedRequestedQuantity)
			}
		} else {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		}
	}

	return filteredNUMANodes
}

// sortCandidateNUMANodesForReclaimed returns a sorted slice of candidate NUMAs for reclaimed_cores.
func (p *DynamicPolicy) sortCandidateNUMANodesForReclaimed(numaNodes []int,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]float64,
) {
	// sort candidate NUMAs by the following rules:
	// 1. NUMAs with numa binding shared or dedicated pods binding to it will be placed ahead of NUMAs without numa binding shared or dedicated pods binding to it.
	// 2. NUMAs with higher headroom will be placed ahead of NUMAs with lower headroom.
	nonSharedOrDedicatedNUMABindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))
	sort.SliceStable(numaNodes, func(i, j int) bool {
		hasNUMABindingPodI := !nonSharedOrDedicatedNUMABindingNUMAs.Contains(numaNodes[i])
		hasNUMABindingPodJ := !nonSharedOrDedicatedNUMABindingNUMAs.Contains(numaNodes[j])
		if hasNUMABindingPodI != hasNUMABindingPodJ {
			return hasNUMABindingPodI && !hasNUMABindingPodJ
		} else {
			return numaHeadroomState[numaNodes[i]] > numaHeadroomState[numaNodes[j]]
		}
	})
}

func (p *DynamicPolicy) calculateNUMANodesLeft(numaNodes []int,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]float64, reqFloat float64,
) (map[int]float64, float64) {
	numaNodesCPULeft := make(map[int]float64, len(numaNodes))
	maxLeft := -math.MaxFloat64
	for _, nodeID := range numaNodes {
		allocatedQuantity := state.GetRequestedQuantityFromPodEntries(machineState[nodeID].PodEntries,
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding),
			p.getContainerRequestedCores)
		availableCPUQuantity := numaHeadroomState[nodeID] - allocatedQuantity
		numaNodesCPULeft[nodeID] = availableCPUQuantity - reqFloat
		if availableCPUQuantity > maxLeft {
			maxLeft = availableCPUQuantity
		}
	}
	return numaNodesCPULeft, maxLeft
}

func (p *DynamicPolicy) populateBestEffortHintsByAvailableNUMANodes(
	hints *pluginapi.ListOfTopologyHints,
	numaNodes []int,
	candidateLeft map[int]float64,
	minLeft float64,
) {
	type nodeHint struct {
		nodeID  int
		curLeft float64
	}

	var nodeHints []nodeHint
	// Collect nodes that meet the requirement
	for _, nodeID := range numaNodes {
		curLeft := candidateLeft[nodeID]

		// Skip this NUMA node if it doesn't greater than non-binding reclaim NUMA headroom left
		if curLeft < minLeft {
			general.Warningf("Skipping NUMA: %d, insufficient left CPUs: %.3f", nodeID, curLeft)
			continue
		}

		// Collect node and its available left CPU
		nodeHints = append(nodeHints, nodeHint{nodeID: nodeID, curLeft: curLeft})
	}

	// Sort nodes by available resources (curLeft) in descending order
	sort.Slice(nodeHints, func(i, j int) bool {
		return nodeHints[i].curLeft > nodeHints[j].curLeft
	})

	// Pre-size hint list capacity to avoid frequent resizing
	hintList := make([]*pluginapi.TopologyHint, 0, len(nodeHints))
	// Add sorted hints to the hint list
	for _, nh := range nodeHints {
		hintList = append(hintList, &pluginapi.TopologyHint{
			Nodes:     []uint64{uint64(nh.nodeID)},
			Preferred: true,
		})
	}

	// Update the hints map
	hints.Hints = hintList
}
