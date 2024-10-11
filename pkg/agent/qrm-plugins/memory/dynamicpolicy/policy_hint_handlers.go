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

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
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
		hints = regenerateHints(uint64(podAggregatedRequest), allocationInfo, util.PodInplaceUpdateResizing(req))

		// clear the current container and regenerate machine state in follow cases:
		// 1. regenerateHints failed.
		// 2. the container is inplace update resizing.
		// hints it as a new container
		if hints == nil || util.PodInplaceUpdateResizing(req) {
			var err error
			resourcesMachineState, err = p.clearContainerAndRegenerateMachineState(req)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	} else if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod: %s/%s, container: %s request to inplace update resize, but no origin allocation info",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("no origin allocation info")
	}

	// if hints exists in extra state-file, prefer to use them
	if hints == nil {
		availableNUMAs := resourcesMachineState[v1.ResourceMemory].GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()

		var extraErr error
		hints, extraErr = util.GetHintsFromExtraStateFile(req.PodName, string(v1.ResourceMemory),
			p.extraStateFileAbsPath, availableNUMAs)
		if extraErr != nil {
			general.Infof("pod: %s/%s, container: %s GetHintsFromExtraStateFile failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, extraErr)
		}
	}

	// check or regenerate hints for inplace update mode
	if util.PodInplaceUpdateResizing(req) {
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
			isInplaceUpdateResizeNumaMigratable := false
			if isInplaceUpdateResizeNumaMigratable {
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
		}
	} else if hints == nil {
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

func (p *DynamicPolicy) clearContainerAndRegenerateMachineState(req *pluginapi.ResourceRequest) (state.NUMANodeResourcesMap, error) {
	podResourceEntries := p.state.GetPodResourceEntries()

	for _, podEntries := range podResourceEntries {
		delete(podEntries[req.PodUid], req.ContainerName)
		if len(podEntries[req.PodUid]) == 0 {
			delete(podEntries, req.PodUid)
		}
	}

	var err error
	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
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
	sort.Ints(numaNodes)

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

// regenerateHints regenerates hints for container that'd already been allocated memory,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func regenerateHints(reqInt uint64, allocationInfo *state.AllocationInfo, podInplaceUpdateResizing bool) map[string]*pluginapi.ListOfTopologyHints {
	hints := map[string]*pluginapi.ListOfTopologyHints{}

	allocatedInt := allocationInfo.AggregatedQuantity
	if allocatedInt < reqInt && !podInplaceUpdateResizing {
		general.ErrorS(nil, "memory's already allocated with smaller quantity than requested",
			"podUID", allocationInfo.PodUid, "containerName", allocationInfo.ContainerName,
			"requestedResource", reqInt, "allocatedSize", allocatedInt)
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
