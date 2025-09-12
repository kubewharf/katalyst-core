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
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/preoccupation"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

var errNoAvailableMemoryHints = fmt.Errorf("no available memory hints")

func (p *DynamicPolicy) sharedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) {
		return p.numaBindingHintHandler(ctx, req)
	}

	// TODO: support sidecar follow main container for non-binding share cores in future
	if req.ContainerType == pluginapi.ContainerType_MAIN {
		if p.enableNonBindingShareCoresMemoryResourceCheck {
			ok, err := p.checkNonBindingShareCoresMemoryResource(req)
			if err != nil {
				general.Errorf("failed to check share cores resource: %q", err)
				return nil, fmt.Errorf("failed to check share cores resource: %q", err)
			}

			if !ok {
				_ = p.emitter.StoreInt64(util.MetricNameShareCoresNoEnoughResourceFailed, 1, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
					"resource":      v1.ResourceMemory.String(),
					"podNamespace":  req.PodNamespace,
					"podName":       req.PodName,
					"containerName": req.ContainerName,
				})...)
				return nil, errNoAvailableMemoryHints
			}
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceMemory): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) systemCoresHintHandler(_ context.Context, req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceMemory): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) reclaimedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod: %s/%s, container: %s request to memory inplace update resize, but not support reclaimed cores",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("not support inplace update resize for reclaimed cores")
	}

	if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) &&
		p.enableReclaimNUMABinding {
		return p.reclaimedCoresWithNUMABindingHintHandler(ctx, req)
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceMemory): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) dedicatedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresHintHandler got nil req")
	}

	if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod: %s/%s, container: %s request to memory inplace update resize, but not support dedicated cores",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("not support inplace update resize for didecated cores")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.numaBindingHintHandler(ctx, req)
	default:
		return p.dedicatedCoresWithoutNUMABindingHintHandler(ctx, req)
	}
}

func (p *DynamicPolicy) numaBindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceMemory): nil,
			})
	}

	podAggregatedRequest, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	resourcesMachineState := p.state.GetMachineState()
	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		if allocationInfo.NumaAllocationResult.Size() != 1 {
			general.Errorf("pod: %s/%s, container: %s is share cores with numa binding, but its numa set length is %d",
				req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.Size())
			return nil, fmt.Errorf("invalid numa set size")
		}
		hints = regenerateHints(allocationInfo, util.PodInplaceUpdateResizing(req))

		// clear the current container and regenerate machine state in follow cases:
		// 1. regenerateHints failed.
		// 2. the container is inplace update resizing.
		// hints it as a new container
		if hints == nil {
			var err error
			resourcesMachineState, err = p.clearContainerAndRegenerateMachineState(req)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}

		if allocationInfo.NumaAllocationResult.Size() != 1 {
			general.Errorf("pod: %s/%s, container: %s is snb, but its numa size is %d",
				req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.Size())
			return nil, fmt.Errorf("invalid hints for inplace update pod")
		}

		machineMemoryState := resourcesMachineState[v1.ResourceMemory]
		nodeID := allocationInfo.NumaAllocationResult.ToSliceInt()[0]
		nodeMemoryState := machineMemoryState[nodeID]

		// the main container aggregated quantity involve all container requests of the pod in memory admit.
		originPodAggregatedRequest := allocationInfo.AggregatedQuantity
		general.Infof("pod: %s/%s, main container: %s request to memory inplace update resize (%d->%d)",
			req.PodNamespace, req.PodName, req.ContainerName, originPodAggregatedRequest, podAggregatedRequest)

		if uint64(podAggregatedRequest) > nodeMemoryState.Free { // no left resource to scale out
			// TODO maybe support snb NUMA migrate inplace update resize later
			isInplaceUpdateResizeNumaMigration := false
			if isInplaceUpdateResizeNumaMigration {
				general.Infof("pod: %s/%s, container: %s request to memory inplace update resize (%d->%d), but no enough memory"+
					"to scale out in current numa (resize request extra: %d, free: %d), migrate numa",
					req.PodNamespace, req.PodName, req.ContainerName, originPodAggregatedRequest, podAggregatedRequest, uint64(podAggregatedRequest)-originPodAggregatedRequest, nodeMemoryState.Free)

				var calculateErr error
				// recalculate hints for the whole pod
				hints, calculateErr = p.calculateHints(uint64(podAggregatedRequest), resourcesMachineState, req)
				if calculateErr != nil {
					general.Errorf("failed to calculate hints for pod: %s/%s, container: %s, error: %v",
						req.PodNamespace, req.PodName, req.ContainerName, calculateErr)
					return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
				}
			} else {
				general.Infof("pod: %s/%s, container: %s request to memory inplace update resize (%d->%d, diff: %d), but no enough memory(%d)",
					req.PodNamespace, req.PodName, req.ContainerName, originPodAggregatedRequest, podAggregatedRequest, uint64(podAggregatedRequest)-originPodAggregatedRequest, nodeMemoryState.Free)
				return nil, fmt.Errorf("inplace update resize scale out failed with no enough resource")
			}
		} else {
			general.Infof("pod: %s/%s, container: %s request inplace update resize, there is enough resource for it in current NUMA",
				req.PodNamespace, req.PodName, req.ContainerName)
			hints = regenerateHints(allocationInfo, false)
		}
	} else {
		// if hints exists in extra state-file, prefer to use them
		availableNUMAs := resourcesMachineState[v1.ResourceMemory].GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()

		var extraErr error
		hints, extraErr = util.GetHintsFromExtraStateFile(req.PodName, string(v1.ResourceMemory),
			p.extraStateFileAbsPath, availableNUMAs)
		if extraErr != nil {
			general.Infof("pod: %s/%s, container: %s GetHintsFromExtraStateFile failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, extraErr)
		}
	}

	if hints == nil {
		// otherwise, calculate hint for container without allocated memory
		var calculateErr error
		// calculate hint for container without allocated memory
		hints, calculateErr = p.calculateHints(uint64(podAggregatedRequest), resourcesMachineState, req)
		if calculateErr != nil {
			general.Errorf("failed to calculate hints for pod: %s/%s, container: %s, error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, calculateErr)
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	general.Infof("memory hints for pod:%s/%s, container: %s success, hints: %v",
		req.PodNamespace, req.PodName, req.ContainerName, hints)

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory), hints)
}

func (p *DynamicPolicy) reclaimedCoresWithNUMABindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceMemory): nil,
			})
	}

	podAggregatedRequest, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	resourcesMachineState := p.state.GetMachineState()
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	numaHeadroomState := p.state.GetNUMAHeadroom()

	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = regenerateHints(allocationInfo, false)

		// clear the current container and regenerate machine state in follow cases:
		// 1. regenerateHints failed.
		// 2. the container is inplace update resizing.
		// hints it as a new container
		if hints == nil {
			var err error
			resourcesMachineState, err = p.clearContainerAndRegenerateMachineState(req)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	}

	if hints == nil {
		// otherwise, calculate hint for container without allocated memory
		var calculateErr error
		// calculate hint for container without allocated memory
		hints, calculateErr = p.calculateHintsForNUMABindingReclaimedCores(int64(podAggregatedRequest),
			resourcesMachineState, podEntries, numaHeadroomState)
		if calculateErr != nil {
			general.Errorf("failed to calculate hints for pod: %s/%s, container: %s, error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, calculateErr)
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	general.Infof("memory hints for pod:%s/%s, container: %s success, hints: %v",
		req.PodNamespace, req.PodName, req.ContainerName, hints)

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory), hints)
}

func (p *DynamicPolicy) clearContainerAndRegenerateMachineState(req *pluginapi.ResourceRequest) (state.NUMANodeResourcesMap, error) {
	podResourceEntries := p.state.GetPodResourceEntries()

	for _, podEntries := range podResourceEntries {
		delete(podEntries[req.PodUid], req.ContainerName)
		if len(podEntries[req.PodUid]) == 0 {
			delete(podEntries, req.PodUid)
		}
	}

	var err error
	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	return resourcesMachineState, nil
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingHintHandler(_ context.Context,
	_ *pluginapi.ResourceRequest,
) (*pluginapi.ResourceHintsResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// calculateHints is a helper function to calculate the topology hints
// with the given container requests.
func (p *DynamicPolicy) calculateHints(reqInt uint64,
	resourcesMachineState state.NUMANodeResourcesMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	machineState := resourcesMachineState[v1.ResourceMemory]

	if len(machineState) == 0 {
		return nil, fmt.Errorf("calculateHints with empty machineState")
	}

	numaNodes := make([]int, 0, len(machineState))
	for numaNode := range machineState {
		numaNodes = append(numaNodes, numaNode)
	}

	if p.enableSNBHighNumaPreference {
		general.Infof("SNB high numa preference is enabled,high numa node is preferential when calculating memory hints")
		sort.Slice(numaNodes, func(i, j int) bool {
			return numaNodes[i] > numaNodes[j]
		})
	} else {
		sort.Ints(numaNodes)
	}

	bytesPerNUMA, err := machineState.BytesPerNUMA()
	if err != nil {
		return nil, fmt.Errorf("getBytesPerNUMAFromMachineState failed with error: %v", err)
	}

	minNUMAsCountNeeded, _, err := util.GetNUMANodesCountToFitMemoryReq(reqInt, bytesPerNUMA, len(machineState))
	if err != nil {
		return nil, fmt.Errorf("GetNUMANodesCountToFitMemoryReq failed with error: %v", err)
	}
	reqAnnotations := req.Annotations
	numaBinding := qosutil.AnnotationsIndicateNUMABinding(reqAnnotations)
	numaExclusive := qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations)

	// because it's hard to control memory allocation accurately,
	// we only support numa_binding but not exclusive container with request smaller than 1 NUMA
	if numaBinding && !numaExclusive && minNUMAsCountNeeded > 1 {
		return nil, fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	numaPerSocket, err := p.topology.NUMAsPerSocket()
	if err != nil {
		return nil, fmt.Errorf("NUMAsPerSocket failed with error: %v", err)
	}

	numaToFreeMemoryBytes := make(map[int]uint64, len(numaNodes))

	for _, nodeID := range numaNodes {
		if machineState[nodeID] == nil {
			general.Warningf("NUMA: %d has nil state", nodeID)
			numaToFreeMemoryBytes[nodeID] = 0
			continue
		}

		if numaExclusive && machineState[nodeID].Allocated > 0 {
			numaToFreeMemoryBytes[nodeID] = 0
			general.Warningf("numa_exclusive container skip NUMA: %d allocated: %d",
				nodeID, machineState[nodeID].Allocated)
		} else {
			numaToFreeMemoryBytes[nodeID] = machineState[nodeID].Free
		}
	}

	general.Infof("calculate hints with req: %d, numaToFreeMemoryBytes: %+v",
		reqInt, numaToFreeMemoryBytes)

	numaBound := len(numaNodes)
	if numaBound > machine.LargeNUMAsPoint {
		// [TODO]: to discuss refine minNUMAsCountNeeded+1
		numaBound = minNUMAsCountNeeded + 1
	}

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

		var freeBytesInMask uint64 = 0
		for _, nodeID := range maskBits {
			freeBytesInMask += numaToFreeMemoryBytes[nodeID]
		}

		if freeBytesInMask < reqInt {
			return
		}

		crossSockets, err := machine.CheckNUMACrossSockets(maskBits, p.topology)
		if err != nil {
			return
		} else if numaCountNeeded <= numaPerSocket && crossSockets {
			return
		}

		availableNumaHints = append(availableNumaHints, &pluginapi.TopologyHint{
			Nodes:     machine.MaskToUInt64Array(mask),
			Preferred: len(maskBits) == minNUMAsCountNeeded,
		})
	})

	// todo support numa_binding without numa_exclusive in the future
	if numaBinding && numaExclusive {
		err = p.preferAvailableNumaHintsByPreOccupation(req, machineState, availableNumaHints)
		if err != nil {
			return nil, fmt.Errorf("preferAvailableNumaHintsByPreOccupation failed with error: %v", err)
		}
	}

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//	     we should resolve this issue if we need manage multi resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("calculateHints got no available memory hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, errNoAvailableMemoryHints
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceMemory): {
			Hints: availableNumaHints,
		},
	}, nil
}

// calculateHints is a helper function to calculate the topology hints
// with the given container requests.
func (p *DynamicPolicy) calculateHintsForNUMABindingReclaimedCores(reqInt int64,
	resourcesMachineState state.NUMANodeResourcesMap,
	podEntries state.PodEntries,
	numaHeadroomState map[int]int64,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	machineState := resourcesMachineState[v1.ResourceMemory]

	if len(machineState) == 0 {
		return nil, fmt.Errorf("calculateHints with empty machineState")
	}

	// Determine the set of NUMA nodes currently hosting non-RNB pods
	nonActualBindingNUMAs := machineState.GetNUMANodesWithoutReclaimedActualNUMABindingPods()

	// Calculate the total requested resources for non-RNB reclaimed pods
	nonActualBindingReclaimedRequestedQuantity := state.GetRequestedQuantityFromPodEntries(podEntries,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding))

	// Compute the total available headroom for non-RNB NUMA nodes
	nonActualBindingReclaimedNUMAHeadroom := state.GetReclaimedNUMAHeadroom(numaHeadroomState, nonActualBindingNUMAs)

	// Identify candidate NUMA nodes for RNB (Reclaimed NUMA Binding) cores
	// This includes both RNB NUMA nodes and NUMA nodes that can shrink from the non-RNB set
	candidateNUMANodes := p.filterNUMANodesByNonBindingReclaimedRequestedQuantity(nonActualBindingReclaimedRequestedQuantity,
		nonActualBindingReclaimedNUMAHeadroom, nonActualBindingNUMAs, machineState, numaHeadroomState)

	candidateLeft := p.calculateNUMANodesLeft(candidateNUMANodes, machineState, numaHeadroomState, reqInt)

	hints := &pluginapi.ListOfTopologyHints{}

	p.populateBestEffortHintsByAvailableNUMANodes(hints, candidateLeft)

	// If no valid hints are generated and this is not a single-NUMA scenario, return an error
	if len(hints.Hints) == 0 && !(p.metaServer.NumNUMANodes == 1 && nonActualBindingNUMAs.Size() > 0) {
		return nil, errNoAvailableMemoryHints
	}

	general.InfoS("calculate numa hints for reclaimed cores success",
		"nonActualNUMABindingNUMAs", nonActualBindingNUMAs.String(),
		"nonActualBindingReclaimedRequestedQuantity", nonActualBindingReclaimedRequestedQuantity,
		"nonActualNUMABindingReclaimedNUMAHeadroom", nonActualBindingReclaimedNUMAHeadroom,
		"numaHeadroomState", numaHeadroomState,
		"candidateNUMANodes", candidateNUMANodes,
		"candidateLeft", candidateLeft)

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceMemory): hints,
	}, nil
}

func (p *DynamicPolicy) calculateNUMANodesLeft(numaNodes []int,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]int64, req int64,
) map[int]int64 {
	numaNodesCPULeft := make(map[int]int64, len(numaNodes))
	for _, nodeID := range numaNodes {
		allocatedQuantity := state.GetRequestedQuantityFromPodEntries(machineState[nodeID].PodEntries,
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
		availableCPUQuantity := numaHeadroomState[nodeID] - allocatedQuantity
		numaNodesCPULeft[nodeID] = availableCPUQuantity - req
	}
	return numaNodesCPULeft
}

func (p *DynamicPolicy) populateBestEffortHintsByAvailableNUMANodes(
	hints *pluginapi.ListOfTopologyHints,
	candidateLeft map[int]int64,
) {
	type nodeHint struct {
		nodeID  int
		curLeft int64
	}

	var nodeHints []nodeHint
	// Collect nodes that meet the requirement
	for nodeID := range candidateLeft {
		// Collect node and its available left memory
		nodeHints = append(nodeHints, nodeHint{nodeID: nodeID, curLeft: candidateLeft[nodeID]})
	}

	// Sort nodes by available resources (curLeft) in descending order
	sort.Slice(nodeHints, func(i, j int) bool {
		return nodeHints[i].curLeft > nodeHints[j].curLeft
	})

	// Pre-size hint list capacity to avoid frequent resizing
	hintList := make([]*pluginapi.TopologyHint, 0, len(nodeHints))
	// Add sorted hints to the hint list
	for _, nh := range nodeHints {
		if nh.curLeft < 0 {
			hintList = append(hintList, &pluginapi.TopologyHint{
				Nodes:     []uint64{uint64(nh.nodeID)},
				Preferred: false,
			})
		} else {
			hintList = append(hintList, &pluginapi.TopologyHint{
				Nodes:     []uint64{uint64(nh.nodeID)},
				Preferred: true,
			})
		}
	}

	// Update the hints map
	hints.Hints = hintList
}

func (p *DynamicPolicy) populateReclaimedHints(numaNodes []int,
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap,
	numaHeadroomState map[int]int64, reqInt int64,
) {
	for _, nodeID := range numaNodes {
		allocatedQuantity := state.GetRequestedQuantityFromPodEntries(machineState[nodeID].PodEntries,
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
		availableMemoryQuantity := numaHeadroomState[nodeID] - allocatedQuantity
		if reqInt > availableMemoryQuantity {
			general.Warningf("numa_binding reclaimed_cores container skip NUMA: %d available: %d",
				nodeID, availableMemoryQuantity)
			continue
		}

		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes:     []uint64{uint64(nodeID)},
			Preferred: true,
		})
	}
}

// sortCandidateNUMANodesForReclaimed sorted slice of candidate NUMAs for reclaimed_cores.
func (p *DynamicPolicy) sortCandidateNUMANodesForReclaimed(candidates []int,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]int64,
) {
	// sort candidate NUMAs by the following rules:
	// 1. NUMAs with numa binding shared or dedicated pods binding to it will be placed ahead of NUMAs without numa binding shared or dedicated pods binding to it.
	// 2. NUMAs with higher headroom will be placed ahead of NUMAs with lower headroom.
	nonSharedOrDedicatedNUMABindingNUMAs := machineState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()
	sort.SliceStable(candidates, func(i, j int) bool {
		hasNUMABindingPodI := !nonSharedOrDedicatedNUMABindingNUMAs.Contains(candidates[i])
		hasNUMABindingPodJ := !nonSharedOrDedicatedNUMABindingNUMAs.Contains(candidates[j])
		if hasNUMABindingPodI != hasNUMABindingPodJ {
			return hasNUMABindingPodI && !hasNUMABindingPodJ
		} else {
			return numaHeadroomState[candidates[i]] > numaHeadroomState[candidates[j]]
		}
	})
}

func (p *DynamicPolicy) filterNUMANodesByNonBindingReclaimedRequestedQuantity(nonBindingReclaimedRequestedQuantity,
	nonBindingNUMAsMemoryQuantity int64,
	nonBindingNUMAs machine.CPUSet,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]int64,
) []int {
	candidateNUMANodes := make([]int, 0, len(numaHeadroomState))
	for nodeID, headroom := range numaHeadroomState {
		if headroom > 0 {
			candidateNUMANodes = append(candidateNUMANodes, nodeID)
		}
	}

	// Sort candidate NUMA nodes based on the other qos numa binding pods and their headroom
	p.sortCandidateNUMANodesForReclaimed(candidateNUMANodes, machineState, numaHeadroomState)

	nonBindingNUMAs = nonBindingNUMAs.Clone()
	filteredNUMANodes := make([]int, 0, len(candidateNUMANodes))
	for _, nodeID := range candidateNUMANodes {
		if nonBindingNUMAs.Contains(nodeID) {
			allocatableMemoryQuantity := numaHeadroomState[nodeID]
			// take this non-binding NUMA for candidate reclaimed_cores with numa_binding,
			// won't cause non-actual numa binding reclaimed_cores in short supply
			if nonBindingReclaimedRequestedQuantity <= nonBindingNUMAsMemoryQuantity-allocatableMemoryQuantity || nonBindingNUMAs.Size() > 1 {
				filteredNUMANodes = append(filteredNUMANodes, nodeID)
				nonBindingNUMAs = nonBindingNUMAs.Difference(machine.NewCPUSet(nodeID))
			} else {
				general.Infof("filter out NUMA: %d since taking it will cause normal reclaimed_cores in short supply;"+
					" nonBindingNUMAsMemoryQuantity: %d, allocatableMemoryQuantity: %d, nonBindingReclaimedRequestedQuantity: %d",
					nodeID, nonBindingNUMAsMemoryQuantity, allocatableMemoryQuantity, nonBindingReclaimedRequestedQuantity)
			}
		} else {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		}
	}

	return filteredNUMANodes
}

// regenerateHints regenerates hints for container that'd already been allocated memory,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func regenerateHints(allocationInfo *state.AllocationInfo, regenerate bool) map[string]*pluginapi.ListOfTopologyHints {
	hints := map[string]*pluginapi.ListOfTopologyHints{}

	if regenerate {
		general.ErrorS(nil, "need to regenerate hints",
			"podNamespace", allocationInfo.PodNamespace,
			"podName", allocationInfo.PodName,
			"podUID", allocationInfo.PodUid, "containerName", allocationInfo.ContainerName)
		return nil
	}

	allocatedNumaNodes := make([]uint64, 0, len(allocationInfo.TopologyAwareAllocations))
	for numaNode, quantity := range allocationInfo.TopologyAwareAllocations {
		if quantity > 0 {
			allocatedNumaNodes = append(allocatedNumaNodes, uint64(numaNode))
		}
	}

	general.InfoS("regenerating topology hints, memory was already allocated to pod",
		"podNamespace", allocationInfo.PodNamespace,
		"podName", allocationInfo.PodName,
		"containerName", allocationInfo.ContainerName,
		"hint", allocatedNumaNodes)
	hints[string(v1.ResourceMemory)] = &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{
				Nodes:     allocatedNumaNodes,
				Preferred: true,
			},
		},
	}
	return hints
}

func (p *DynamicPolicy) preferAvailableNumaHintsByPreOccupation(req *pluginapi.ResourceRequest,
	machineState state.NUMANodeMap, hints []*pluginapi.TopologyHint,
) error {
	preOccupationNUMAs := machine.NewCPUSet()
	withoutPreOccupationNUMAs := machine.NewCPUSet()
	now := time.Now()
	for numaID, numaState := range machineState {
		if numaState == nil {
			continue
		}

		occupied := false
		for uid, podEntries := range numaState.PreOccPodEntries {
			for _, containerEntries := range podEntries {
				if containerEntries == nil ||
					preoccupation.PreOccAllocationExpired(containerEntries.AllocationMeta, now) {
					continue
				}

				occupied = true
				if preoccupation.IsPreOccupiedContainer(req, containerEntries.AllocationMeta) {
					general.Infof("numa %d is pre occupied by pod %s/%s/%s/%s", numaID, uid, req.PodNamespace, req.PodName, req.ContainerName)
					preOccupationNUMAs.Add(numaID)
					break
				}
			}
		}

		if !occupied {
			withoutPreOccupationNUMAs.Add(numaID)
		}
	}

	success, err := preoccupation.PreOccupationTopologyHints(hints, preOccupationNUMAs, withoutPreOccupationNUMAs)
	if err != nil {
		return err
	}

	if success {
		general.Infof("%s/%s/%s/%s successfully prefer available numa hints by pre occupation: %v",
			req.PodNamespace, req.PodName, req.ContainerName, req.PodUid, hints)
	}

	return nil
}
