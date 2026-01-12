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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/preoccupation"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

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
			return nil, cpuutil.ErrNoAvailableCPUHints
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

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = cpuutil.RegenerateHints(allocationInfo, false)

		// regenerateHints failed. need to clear container record and re-calculate.
		if hints == nil {
			delete(podEntries[req.PodUid], req.ContainerName)
			if len(podEntries[req.PodUid]) == 0 {
				delete(podEntries, req.PodUid)
			}

			var err error
			machineState, err = generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, machineState)
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
			(*commonstate.AllocationMeta).CheckDedicatedNUMABindingNUMAExclusive))

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
		hints, calculateErr = p.calculateHints(request, podEntries, machineState, req)
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
func (p *DynamicPolicy) calculateHints(
	request float64, podEntries state.PodEntries,
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

	availableNUMAs := p.filterNUMANodesByNonBinding(request, podEntries, machineState, req)
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
		} else if numaBinding && !availableNUMAs.Contains(nodeID) {
			numaToAvailableCPUCount[nodeID] = 0
			general.Warningf("numa_binding container skip NUMA: %d, allocated: %d",
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

	// todo support numa_binding without numa_exclusive in the future
	if numaBinding && numaExclusive {
		err = p.preferAvailableNumaHintsByPreOccupation(req, machineState, availableNumaHints)
		if err != nil {
			return nil, fmt.Errorf("preferAvailableNumaHintsByPreOccupation failed with error: %v", err)
		}
	}

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need manage multi resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("calculateHints got no available cpu hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, cpuutil.ErrNoAvailableCPUHints
	}

	hints := &pluginapi.ListOfTopologyHints{
		Hints: availableNumaHints,
	}

	err = p.dedicatedCoresNUMABindingHintOptimizer.OptimizeHints(
		hintoptimizer.Request{
			ResourceRequest: req,
			CPURequest:      request,
		}, hints)
	if err != nil {
		return nil, err
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): hints,
	}, nil
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

	general.Infof("cpu hints for pod:%s/%s, container: %s success, hints: %v",
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
		nonActualBindingReclaimedNUMAHeadroom, nonActualBindingNUMAs, machineState, numaHeadroomState)

	candidateLeft := p.calculateNUMANodesLeft(candidateNUMANodes, machineState, numaHeadroomState, reqFloat)

	hints := &pluginapi.ListOfTopologyHints{}

	p.populateBestEffortHintsByAvailableNUMANodes(hints, candidateLeft)

	// If no valid hints are generated and this is not a single-NUMA scenario, return an error
	if len(hints.Hints) == 0 && !(p.metaServer.NumNUMANodes == 1 && nonActualBindingNUMAs.Size() > 0) {
		return nil, cpuutil.ErrNoAvailableCPUHints
	}

	general.InfoS("calculate numa hints for reclaimed cores success",
		"nonActualBindingNUMAs", nonActualBindingNUMAs.String(),
		"nonActualBindingReclaimedRequestedQuantity", nonActualBindingReclaimedRequestedQuantity,
		"nonActualBindingReclaimedNUMAHeadroom", nonActualBindingReclaimedNUMAHeadroom,
		"numaHeadroomState", numaHeadroomState,
		"candidateNUMANodes", candidateNUMANodes,
		"candidateLeft", candidateLeft)

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): hints,
	}, nil
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
					return nil, cpuutil.ErrNoAvailableCPUHints
				}
			} else {
				general.Infof("pod: %s/%s, container: %s request inplace update resize, there is enough resource for it in current NUMA",
					req.PodNamespace, req.PodName, req.ContainerName)
				hints = cpuutil.RegenerateHints(allocationInfo, false)
			}
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
	machineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	return machineState, nil
}

func (p *DynamicPolicy) filterNUMANodesByNonBinding(
	request float64, podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	req *pluginapi.ResourceRequest,
) machine.CPUSet {
	if req == nil {
		return machine.NewCPUSet()
	}

	nonBindingNUMAsCPUQuantity := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding)).Size()
	nonBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))
	nonBindingSharedRequestedQuantity := state.GetNonBindingSharedRequestedQuantityFromPodEntries(podEntries, nil, p.getContainerRequestedCores)

	return p.filterNUMANodesByNonBindingSharedRequestedQuantity(
		request, nonBindingSharedRequestedQuantity, nonBindingNUMAsCPUQuantity, nonBindingNUMAs, machineState,
		machineState.GetFilteredNUMASetWithAnnotations(state.WrapAllocationMetaFilterWithAnnotations(commonstate.CheckNUMABindingAntiAffinity), req.Annotations).ToSliceInt())
}

func (p *DynamicPolicy) filterNUMANodesByNonBindingSharedRequestedQuantity(
	request float64,
	nonBindingSharedRequestedQuantity, nonBindingNUMAsCPUQuantity int,
	nonBindingNUMAs machine.CPUSet,
	machineState state.NUMANodeMap, numaNodes []int,
) machine.CPUSet {
	filteredNUMANodes := make([]int, 0, len(numaNodes))

	for _, nodeID := range numaNodes {
		allocatableCPUQuantity := machineState[nodeID].GetAvailableCPUQuantity(p.reservedCPUs)
		if nonBindingNUMAs.Contains(nodeID) {
			// take this non-binding NUMA for candidate shared_cores with numa_binding,
			// won't cause non-binding shared_cores in short supply
			if cpuutil.CPUIsSufficient(float64(nonBindingSharedRequestedQuantity), float64(nonBindingNUMAsCPUQuantity)-allocatableCPUQuantity) {
				filteredNUMANodes = append(filteredNUMANodes, nodeID)
			} else {
				general.Infof("filter out NUMA: %d since taking it will cause non-binding shared_cores in short supply;"+
					" nonBindingNUMAsCPUQuantity: %d, targetNUMAAllocatableCPUQuantity: %f, nonBindingSharedRequestedQuantity: %d, request: %f",
					nodeID, nonBindingNUMAsCPUQuantity, allocatableCPUQuantity, nonBindingSharedRequestedQuantity, request)
			}
		} else if cpuutil.CPUIsSufficient(request, allocatableCPUQuantity) {
			filteredNUMANodes = append(filteredNUMANodes, nodeID)
		}
	}

	return machine.NewCPUSet(filteredNUMANodes...)
}

func (p *DynamicPolicy) calculateHintsForNUMABindingSharedCores(request float64, podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	numaNodes := p.filterNUMANodesByNonBinding(request, podEntries, machineState, req).ToSliceInt()

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

	if p.enableSNBHighNumaPreference {
		general.Infof("SNB high numa preference is enabled,high numa node is preferential when calculating cpu hints")
		sort.Slice(numaNodes, func(i, j int) bool {
			return numaNodes[i] > numaNodes[j]
		})
	}

	// populate hints by available numa nodes
	cpuutil.PopulateHintsByAvailableNUMANodes(numaNodes, hints, true)

	// optimize hints by shared_cores numa_binding hint optimizer
	err = p.sharedCoresNUMABindingHintOptimizer.OptimizeHints(
		hintoptimizer.Request{
			ResourceRequest: req,
			CPURequest:      request,
		}, hints)
	if err != nil {
		return nil, err
	}

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need manage multi resource in one plugin.
	if len(hints.Hints) == 0 {
		general.Warningf("calculateHints got no available cpu hints for snb pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, cpuutil.ErrNoAvailableCPUHints
	}

	// populate hints by already existed numa binding result
	if p.dynamicConfig.GetDynamicConfiguration().PreferUseExistNUMAHintResult {
		err = p.populateHintsByAlreadyExistedNUMABindingResult(req, hints)
		if err != nil {
			general.Warningf("populateHintsByAlreadyExistedNUMABindingResult failed with error: %v", err)
			return nil, err
		}
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): hints,
	}, nil
}

func (p *DynamicPolicy) populateHintsByAlreadyExistedNUMABindingResult(req *pluginapi.ResourceRequest, hints *pluginapi.ListOfTopologyHints) error {
	result, err := p.getSharedCoresNUMABindingResultFromAnnotation(req)
	if err != nil {
		return err
	}

	// skip empty result
	if result.IsEmpty() {
		return nil
	}

	index := -1
	for i, hint := range hints.Hints {
		hintNUMASet, err := machine.NewCPUSetUint64(hint.Nodes...)
		if err != nil {
			return err
		}

		if result.Equals(hintNUMASet) {
			index = i
			break
		}
	}

	if index == -1 {
		general.Warningf("failed to find already existed numa binding result %s from hints %v for pod: %s/%s, container: %s",
			result, hints.Hints, req.PodNamespace, req.PodName, req.ContainerName)
	} else {
		general.Infof("found already existed numa binding result %s from hints %v for pod: %s/%s, container: %s",
			result, hints.Hints, req.PodNamespace, req.PodName, req.ContainerName)
		for i, hint := range hints.Hints {
			if i == index {
				hint.Preferred = true
			} else {
				hint.Preferred = false
			}
		}
	}

	return nil
}

func (p *DynamicPolicy) getSharedCoresNUMABindingResultFromAnnotation(req *pluginapi.ResourceRequest) (machine.CPUSet, error) {
	result, ok := req.Annotations[p.sharedCoresNUMABindingResultAnnotationKey]
	if !ok {
		return machine.CPUSet{}, nil
	}

	numaSet, err := machine.Parse(result)
	if err != nil {
		return machine.CPUSet{}, err
	}

	return numaSet, nil
}

func (p *DynamicPolicy) filterNUMANodesByNonBindingReclaimedRequestedQuantity(nonBindingReclaimedRequestedQuantity,
	nonBindingNUMAsCPUQuantity float64,
	nonBindingNUMAs machine.CPUSet,
	machineState state.NUMANodeMap,
	numaHeadroomState map[int]float64,
) []int {
	candidateNUMANodes := make([]int, 0, len(numaHeadroomState))
	for numaID, headroom := range numaHeadroomState {
		if headroom > 0 {
			candidateNUMANodes = append(candidateNUMANodes, numaID)
		}
	}

	// Sort candidate NUMA nodes based on the other qos numa binding pods and their headroom
	p.sortCandidateNUMANodesForReclaimed(candidateNUMANodes, machineState, numaHeadroomState)

	nonBindingNUMAs = nonBindingNUMAs.Clone()
	filteredNUMANodes := make([]int, 0, len(candidateNUMANodes))
	for _, nodeID := range candidateNUMANodes {
		if nonBindingNUMAs.Contains(nodeID) {
			allocatableCPUQuantity := numaHeadroomState[nodeID]
			// take this non-binding NUMA for candidate reclaimed_cores with numa_binding,
			// won't cause non-actual numa binding reclaimed_cores in short supply
			if cpuutil.CPUIsSufficient(nonBindingReclaimedRequestedQuantity, nonBindingNUMAsCPUQuantity-allocatableCPUQuantity) || nonBindingNUMAs.Size() > 1 {
				filteredNUMANodes = append(filteredNUMANodes, nodeID)
				nonBindingNUMAs = nonBindingNUMAs.Difference(machine.NewCPUSet(nodeID))
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
) map[int]float64 {
	numaNodesCPULeft := make(map[int]float64, len(numaNodes))
	for _, nodeID := range numaNodes {
		allocatedQuantity := state.GetRequestedQuantityFromPodEntries(machineState[nodeID].PodEntries,
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding),
			p.getContainerRequestedCores)
		availableCPUQuantity := numaHeadroomState[nodeID] - allocatedQuantity
		numaNodesCPULeft[nodeID] = availableCPUQuantity - reqFloat
	}
	return numaNodesCPULeft
}

func (p *DynamicPolicy) populateBestEffortHintsByAvailableNUMANodes(
	hints *pluginapi.ListOfTopologyHints,
	candidateLeft map[int]float64,
) {
	type nodeHint struct {
		nodeID  int
		curLeft float64
	}

	var nodeHints []nodeHint
	// Collect nodes that meet the requirement
	for nodeID := range candidateLeft {
		// Collect node and its available left CPU
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
