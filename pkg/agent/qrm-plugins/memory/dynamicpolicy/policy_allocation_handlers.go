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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.numaBindingAllocationHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores, persistCheckpoint)
	default:
		return p.allocateNUMAsWithoutNUMABindingPods(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores, persistCheckpoint)
	}
}

func (p *DynamicPolicy) systemCoresAllocationHandler(_ context.Context, req *pluginapi.ResourceRequest, persistCheckpoint bool) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("systemCoresAllocationHandler got nil request")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		resourcesMachineState := p.state.GetMachineState()
		defaultSystemCoresNUMAs := p.getDefaultSystemCoresNUMAs(resourcesMachineState[v1.ResourceMemory])
		// allocate system_cores pod with NUMA binding
		// todo: currently we only set cpuset.mems for system_cores pods with numa binding to NUMAs without dedicated and NUMA binding and NUMA exclusive pod,
		// 		in the future, we set them according to their cpuset_pool annotation.
		return p.allocateTargetNUMAs(req, apiconsts.PodAnnotationQoSLevelSystemCores, defaultSystemCoresNUMAs, persistCheckpoint)
	default:
		return p.allocateTargetNUMAs(req, apiconsts.PodAnnotationQoSLevelSystemCores, p.topology.CPUDetails.NUMANodes(), persistCheckpoint)
	}
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod: %s/%s, container: %s request to memory inplace update resize, but not support reclaimed cores",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("not support inplace update resize for reclaiemd cores")
	}

	return p.reclaimedCoresBestEffortNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
}

func (p *DynamicPolicy) dedicatedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod: %s/%s, container: %s request to memory inplace update resize, but not support dedicated cores",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("not support inplace update resize for didecated cores")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.numaBindingAllocationHandler(ctx, req, apiconsts.PodAnnotationQoSLevelDedicatedCores, persistCheckpoint)
	default:
		return p.dedicatedCoresWithoutNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	}
}

func (p *DynamicPolicy) numaBindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, qosLevel string, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		// sidecar container admit after main container
		return p.numaBindingAllocationSidecarHandler(ctx, req, qosLevel, persistCheckpoint)
	}

	// use the pod aggregated request to instead of main container.
	podAggregatedRequest, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	memoryState := machineState[v1.ResourceMemory]

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		if allocationInfo.AggregatedQuantity >= uint64(podAggregatedRequest) && !util.PodInplaceUpdateResizing(req) {
			general.InfoS("already allocated and meet requirement",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", podAggregatedRequest,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)

			resp, packErr := packAllocationResponse(allocationInfo, req, nil)
			if packErr != nil {
				general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, packErr)
				return nil, fmt.Errorf("packAllocationResponse failed with error: %v", packErr)
			}
			return resp, nil
		}
		general.InfoS("not meet requirement, clear record and re-allocate",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", podAggregatedRequest,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		// remove the main container of this pod (the main container involve the whole pod requests), and the
		// sidecar container request in state is zero.
		containerEntries := podEntries[req.PodUid]
		delete(containerEntries, req.ContainerName)

		var stateErr error
		memoryState, stateErr = state.GenerateMemoryStateFromPodEntries(p.state.GetMachineInfo(), podEntries, p.state.GetReservedMemory())
		if stateErr != nil {
			general.ErrorS(stateErr, "generateMemoryMachineStateByPodEntries failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", podAggregatedRequest,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)
			return nil, fmt.Errorf("generateMemoryMachineStateByPodEntries failed with error: %v", stateErr)
		}
	} else if util.PodInplaceUpdateResizing(req) {
		general.Errorf("pod %s/%s, container: %s request to memory inplace update resize, but no origin allocation info",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("no origin allocation info")
	}

	// call calculateMemoryAllocation to update memoryState in-place,
	// and we can use this adjusted state to pack allocation results
	err = p.calculateMemoryAllocation(req, memoryState, qosLevel, podAggregatedRequest)
	if err != nil {
		general.ErrorS(err, "unable to allocate Memory",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq", podAggregatedRequest)
		return nil, err
	}

	topologyAwareAllocations := make(map[int]uint64)
	result := machine.NewCPUSet()
	var aggregatedQuantity uint64 = 0
	for numaNode, numaNodeState := range memoryState {
		if numaNodeState.PodEntries[req.PodUid][req.ContainerName] != nil &&
			numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity > 0 {
			result = result.Union(machine.NewCPUSet(numaNode))
			aggregatedQuantity += numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity
			topologyAwareAllocations[numaNode] = numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity
		}
	}

	general.InfoS("allocate memory successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"reqMemoryQuantity", podAggregatedRequest,
		"numaAllocationResult", result.String())

	allocationInfo = &state.AllocationInfo{
		AllocationMeta:           state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
		AggregatedQuantity:       aggregatedQuantity,
		NumaAllocationResult:     result.Clone(),
		TopologyAwareAllocations: topologyAwareAllocations,
	}

	if !qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) {
		if len(req.Hint.Nodes) != 1 {
			return nil, fmt.Errorf("numa binding without numa exclusive allocation result numa node size is %d, "+
				"not equal to 1", len(req.Hint.Nodes))
		}
		allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes[0])
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo, persistCheckpoint)

	podResourceEntries = p.state.GetPodResourceEntries()
	machineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate memoryState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(machineState, persistCheckpoint)

	err = p.adjustAllocationEntries(persistCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req, nil)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	// update the numa allocation result for numa binding pod
	err = p.updateSpecifiedNUMAAllocation(ctx, allocationInfo)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s updateSpecifiedNUMAAllocation failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("updateSpecifiedNUMAAllocation failed with error: %v", err)
	}

	return resp, nil
}

func (p *DynamicPolicy) reclaimedCoresBestEffortNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		// sidecar container admit after main container
		return p.numaBindingAllocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelReclaimedCores, persistCheckpoint)
	}

	// use the pod aggregated request to instead of the main container.
	podAggregatedRequest, _, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return nil, fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	memoryState := machineState[v1.ResourceMemory]

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		if allocationInfo.AggregatedQuantity >= uint64(podAggregatedRequest) {
			general.InfoS("already allocated and meet requirement",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", allocationInfo.AggregatedQuantity,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)
			return packAllocationResponse(allocationInfo, req, nil)
		}

		general.InfoS("not meet requirement, clear record and re-allocate",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", podAggregatedRequest,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		// remove the main container of this pod (the main container involve the whole pod requests), and the
		// sidecar container request in state is zero.
		containerEntries := podEntries[req.PodUid]
		delete(containerEntries, req.ContainerName)

		var stateErr error
		memoryState, stateErr = state.GenerateMemoryStateFromPodEntries(p.state.GetMachineInfo(), podEntries, p.state.GetReservedMemory())
		if stateErr != nil {
			general.ErrorS(stateErr, "generateMemoryMachineStateByPodEntries failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", podAggregatedRequest,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)
			return nil, fmt.Errorf("generateMemoryMachineStateByPodEntries failed with error: %v", stateErr)
		}
	}

	allocationInfo = &state.AllocationInfo{
		AllocationMeta:     state.GenerateMemoryContainerAllocationMeta(req, apiconsts.PodAnnotationQoSLevelReclaimedCores),
		AggregatedQuantity: uint64(podAggregatedRequest),
	}

	nonReclaimActualBindingNUMAs := memoryState.GetNUMANodesWithoutReclaimedActualNUMABindingPods()
	reclaimActualBindingNUMAs := memoryState.GetNUMANodesWithoutReclaimedNonActualNUMABindingPods()

	var numaAllocationResult machine.CPUSet
	if req.Hint != nil && len(req.Hint.Nodes) == 1 &&
		(reclaimActualBindingNUMAs.Contains(int(req.Hint.Nodes[0])) ||
			!nonReclaimActualBindingNUMAs.Equals(machine.NewCPUSet(int(req.Hint.Nodes[0])))) {
		allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes[0])
		numaAllocationResult = machine.NewCPUSet(int(req.Hint.Nodes[0]))
	} else {
		numaAllocationResult = nonReclaimActualBindingNUMAs
	}

	if numaAllocationResult.IsEmpty() {
		return nil, fmt.Errorf("allocate memory failed with empty numa allocation result")
	}

	allocationInfo.NumaAllocationResult = numaAllocationResult

	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo, persistCheckpoint)

	machineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), p.state.GetPodResourceEntries(), p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate memoryState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(machineState, persistCheckpoint)

	err = p.adjustAllocationEntries(persistCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req, p.getReclaimedResourceAllocationAnnotations(allocationInfo))
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	// we only support updating the NUMA allocation results for pods with explicit NUMA binding annotation
	if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) {
		err = p.updateSpecifiedNUMAAllocation(ctx, allocationInfo)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s updateSpecifiedNUMAAllocation failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("updateSpecifiedNUMAAllocation failed with error: %v", err)
		}
	}

	general.InfoS("allocate memory successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"reqMemoryQuantity", podAggregatedRequest,
		"numaAllocationResult", numaAllocationResult.String())

	return resp, nil
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	_ *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// numaBindingAllocationSidecarHandler allocates for sidecar
// currently, we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) numaBindingAllocationSidecarHandler(_ context.Context,
	req *pluginapi.ResourceRequest, qosLevel string, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	podResourceEntries := p.state.GetPodResourceEntries()

	podEntries := podResourceEntries[v1.ResourceMemory]
	if podEntries[req.PodUid] == nil {
		general.Infof("there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	// todo: consider sidecar without reconcile in vpa
	mainContainerAllocationInfo, ok := podEntries.GetMainContainerAllocation(req.PodUid)
	if !ok {
		general.Infof("main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	allocationInfo := &state.AllocationInfo{
		AllocationMeta:           state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
		AggregatedQuantity:       0,   // not count sidecar quantity
		TopologyAwareAllocations: nil, // not count sidecar quantity
	}

	applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo)

	// update pod entries directly. if one of subsequent steps is failed,
	// we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo, persistCheckpoint)
	podResourceEntries = p.state.GetPodResourceEntries()
	resourcesState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Infof("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(resourcesState, persistCheckpoint)

	resp, err := packAllocationResponse(allocationInfo, req, nil)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}
	return resp, nil
}

// allocateNUMAsWithoutNUMABindingPods works both for sharedCoresAllocationHandler and reclaimedCoresAllocationHandler,
// and it will store the allocation in states.
func (p *DynamicPolicy) allocateNUMAsWithoutNUMABindingPods(_ context.Context,
	req *pluginapi.ResourceRequest, qosLevel string, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	machineState := p.state.GetMachineState()
	resourceState := machineState[v1.ResourceMemory]
	numaWithoutNUMABindingPods := resourceState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.String(), numaWithoutNUMABindingPods.String())
	}

	// use real container request size here
	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	allocationInfo = &state.AllocationInfo{
		AllocationMeta:       state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
		NumaAllocationResult: numaWithoutNUMABindingPods.Clone(),
		AggregatedQuantity:   uint64(reqInt),
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
	podResourceEntries := p.state.GetPodResourceEntries()

	machineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, machineState, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate resourceState by updated pod entries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req, nil)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	p.state.SetMachineState(machineState, persistCheckpoint)
	return resp, nil
}

// allocateTargetNUMAs returns target numa nodes as allocation results,
// and it will store the allocation in states.
func (p *DynamicPolicy) allocateTargetNUMAs(req *pluginapi.ResourceRequest,
	qosLevel string, targetNUMAs machine.CPUSet, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && !allocationInfo.NumaAllocationResult.Equals(targetNUMAs) {
		general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.String(), targetNUMAs.String())
	}

	allocationInfo = &state.AllocationInfo{
		AllocationMeta:       state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
		NumaAllocationResult: targetNUMAs.Clone(),
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
	podResourceEntries := p.state.GetPodResourceEntries()

	machineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req, nil)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	p.state.SetMachineState(machineState, persistCheckpoint)
	return resp, nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint,
// and it will be called when entries without numa binding should be adjusted
// according to current entries and machine state.
func (p *DynamicPolicy) adjustAllocationEntries(persistCheckpoint bool) error {
	startTime := time.Now()
	general.Infof("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	resourcesMachineState := p.state.GetMachineState()
	podResourceEntries := p.state.GetPodResourceEntries()
	machineState := resourcesMachineState[v1.ResourceMemory]
	podEntries := podResourceEntries[v1.ResourceMemory]

	// for numaSetChangedContainers, we should reset their allocation info and
	// trigger necessary Knob actions (like dropping caches or migrate memory
	// to make sure already-allocated memory cooperate with the new numaset)
	numaSetChangedContainers := make(map[string]map[string]*state.AllocationInfo)
	p.adjustAllocationEntriesForSharedCores(numaSetChangedContainers, podEntries, machineState)
	p.adjustAllocationEntriesForDedicatedCores(numaSetChangedContainers, podEntries, machineState)
	p.adjustAllocationEntriesForSystemCores(numaSetChangedContainers, podEntries, machineState)
	p.adjustAllocationEntriesForReclaimedCores(numaSetChangedContainers, podEntries, machineState)

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetMachineState(), p.state.GetReservedMemory())
	if err != nil {
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries, false)
	p.state.SetMachineState(resourcesMachineState, false)
	if persistCheckpoint {
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed")
		}
	}

	err = p.migratePagesForNUMASetChangedContainers(numaSetChangedContainers)
	if err != nil {
		return fmt.Errorf("migratePagesForNUMASetChangedContainers failed with error: %v", err)
	}

	return nil
}

// calculateMemoryAllocation will not store the allocation in states, instead,
// it will update the passed by machineState in-place; so the function will be
// called `calculateXXX` rather than `allocateXXX`
func (p *DynamicPolicy) calculateMemoryAllocation(req *pluginapi.ResourceRequest, machineState state.NUMANodeMap, qosLevel string, podAggregatedRequest int) error {
	if req.Hint == nil {
		return fmt.Errorf("hint is nil")
	} else if len(req.Hint.Nodes) == 0 {
		return fmt.Errorf("hint is empty")
	} else if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) &&
		!qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) &&
		len(req.Hint.Nodes) > 1 {
		return fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	hintNumaNodes := machine.NewCPUSet(util.HintToIntArray(req.Hint)...)
	general.InfoS("allocate by hints",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"hints", hintNumaNodes.String(),
		"reqMemoryQuantity", podAggregatedRequest)

	var leftQuantity uint64
	var err error

	if qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) {
		leftQuantity, err = calculateExclusiveMemory(req, machineState, hintNumaNodes.ToSliceInt(), uint64(podAggregatedRequest), qosLevel)
		if err != nil {
			return fmt.Errorf("calculateExclusiveMemory failed with error: %v", err)
		}
	} else {
		leftQuantity, err = calculateMemoryInNumaNodes(req, machineState, hintNumaNodes.ToSliceInt(), uint64(podAggregatedRequest), qosLevel)
		if err != nil {
			return fmt.Errorf("calculateMemoryInNumaNodes failed with error: %v", err)
		}
	}

	if leftQuantity > 0 {
		general.Errorf("hint NUMA nodes: %s can't meet memory request: %d bytes, leftQuantity: %d bytes",
			hintNumaNodes.String(), podAggregatedRequest, leftQuantity)
		return fmt.Errorf("results can't meet memory request")
	}

	return nil
}

// calculateExclusiveMemory tries to allocate all memories in the numa list to
// the given container, and returns the remaining un-satisfied quantity.
// calculateExclusiveMemory will not store the allocation in states, instead,
// it will update the passed by machineState in-place; so the function will be
// called `calculateXXX` rather than `allocateXXX`
func calculateExclusiveMemory(req *pluginapi.ResourceRequest,
	machineState state.NUMANodeMap, numaNodes []int, reqQuantity uint64, qosLevel string,
) (leftQuantity uint64, err error) {
	for _, numaNode := range numaNodes {
		var curNumaNodeAllocated uint64 = 0

		numaNodeState := machineState[numaNode]
		if numaNodeState == nil {
			return reqQuantity, fmt.Errorf("NUMA: %d has nil state", numaNode)
		}

		if numaNodeState.Free > 0 {
			curNumaNodeAllocated = numaNodeState.Free
			if reqQuantity < numaNodeState.Free {
				reqQuantity = 0
			} else {
				reqQuantity -= numaNodeState.Free
			}
			numaNodeState.Free = 0
			numaNodeState.Allocated = numaNodeState.Allocatable
		}

		if curNumaNodeAllocated == 0 {
			continue
		}

		if numaNodeState.PodEntries == nil {
			numaNodeState.PodEntries = make(state.PodEntries)
		}

		if numaNodeState.PodEntries[req.PodUid] == nil {
			numaNodeState.PodEntries[req.PodUid] = make(state.ContainerEntries)
		}

		numaNodeState.PodEntries[req.PodUid][req.ContainerName] = &state.AllocationInfo{
			AllocationMeta:       state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
			AggregatedQuantity:   curNumaNodeAllocated,
			NumaAllocationResult: machine.NewCPUSet(numaNode),
			TopologyAwareAllocations: map[int]uint64{
				numaNode: curNumaNodeAllocated,
			},
		}
	}

	return reqQuantity, nil
}

// calculateMemoryInNumaNodes tries to allocate memories in the numa list to
// the given container, and returns the remaining un-satisfied quantity.
func calculateMemoryInNumaNodes(req *pluginapi.ResourceRequest,
	machineState state.NUMANodeMap, numaNodes []int,
	reqQuantity uint64, qosLevel string,
) (leftQuantity uint64, err error) {
	for _, numaNode := range numaNodes {
		var curNumaNodeAllocated uint64 = 0

		numaNodeState := machineState[numaNode]
		if numaNodeState == nil {
			return reqQuantity, fmt.Errorf("NUMA: %d has nil state", numaNode)
		}

		if numaNodeState.Free > 0 {
			if reqQuantity < numaNodeState.Free {
				curNumaNodeAllocated = reqQuantity
				reqQuantity = 0
			} else {
				curNumaNodeAllocated = numaNodeState.Free
				reqQuantity -= numaNodeState.Free
			}
			numaNodeState.Free -= curNumaNodeAllocated
			numaNodeState.Allocated += curNumaNodeAllocated
		}

		if curNumaNodeAllocated == 0 {
			continue
		}

		if numaNodeState.PodEntries == nil {
			numaNodeState.PodEntries = make(state.PodEntries)
		}

		if numaNodeState.PodEntries[req.PodUid] == nil {
			numaNodeState.PodEntries[req.PodUid] = make(state.ContainerEntries)
		}

		numaNodeState.PodEntries[req.PodUid][req.ContainerName] = &state.AllocationInfo{
			AllocationMeta:       state.GenerateMemoryContainerAllocationMeta(req, qosLevel),
			AggregatedQuantity:   curNumaNodeAllocated,
			NumaAllocationResult: machine.NewCPUSet(numaNode),
			TopologyAwareAllocations: map[int]uint64{
				numaNode: curNumaNodeAllocated,
			},
		}
	}

	return reqQuantity, nil
}

// packAllocationResponse fills pluginapi.ResourceAllocationResponse with information from AllocationInfo and pluginapi.ResourceRequest
func packAllocationResponse(allocationInfo *state.AllocationInfo, req *pluginapi.ResourceRequest, resourceAllocationAnnotations map[string]string) (*pluginapi.ResourceAllocationResponse, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil allocationInfo")
	} else if req == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil request")
	}

	return &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   string(v1.ResourceMemory),
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceMemory): {
					OciPropertyName:   util.OCIPropertyNameCPUSetMems,
					IsNodeResource:    false,
					IsScalarResource:  true,
					Annotations:       resourceAllocationAnnotations,
					AllocatedQuantity: float64(allocationInfo.AggregatedQuantity),
					AllocationResult:  allocationInfo.NumaAllocationResult.String(),
					ResourceHints: &pluginapi.ListOfTopologyHints{
						Hints: []*pluginapi.TopologyHint{
							req.Hint,
						},
					},
				},
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}

func (p *DynamicPolicy) adjustAllocationEntriesForSharedCores(numaSetChangedContainers map[string]map[string]*state.AllocationInfo,
	podEntries state.PodEntries, machineState state.NUMANodeMap,
) {
	numaWithoutNUMABindingPods := machineState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()
	general.Infof("numaWithoutNUMABindingPods: %s", numaWithoutNUMABindingPods.String())

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if !allocationInfo.CheckShared() {
				continue
			}

			if !allocationInfo.CheckNUMABinding() {
				// update container to target numa set for non-binding share cores
				p.updateNUMASetChangedContainers(numaSetChangedContainers, allocationInfo, numaWithoutNUMABindingPods)

				// update AggregatedQuantity for non-binding share cores
				allocationInfo.AggregatedQuantity = p.getContainerRequestedMemoryBytes(allocationInfo)
			} else {
				// memory of sidecar in snb pod is belonged to main container so we don't need to adjust it
				if allocationInfo.CheckSideCar() {
					continue
				}

				if len(allocationInfo.TopologyAwareAllocations) != 1 {
					general.Errorf("pod: %s/%s, container: %s topologyAwareAllocations length is not 1: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.TopologyAwareAllocations)
					continue
				}

				// only for refresh memory request for old inplace update resized pods.
				// update AggregatedQuantity && TopologyAwareAllocations for snb
				allocationInfo.AggregatedQuantity = p.getContainerRequestedMemoryBytes(allocationInfo)
				for numaId, quantity := range allocationInfo.TopologyAwareAllocations {
					if quantity != allocationInfo.AggregatedQuantity {
						allocationInfo.TopologyAwareAllocations[numaId] = allocationInfo.AggregatedQuantity
					}
				}
			}
		}
	}
}

func (p *DynamicPolicy) adjustAllocationEntriesForDedicatedCores(numaSetChangedContainers map[string]map[string]*state.AllocationInfo,
	podEntries state.PodEntries, machineState state.NUMANodeMap,
) {
	numaWithoutNUMABindingPods := machineState.GetNUMANodesWithoutSharedOrDedicatedNUMABindingPods()
	general.Infof("numaWithoutNUMABindingPods: %s", numaWithoutNUMABindingPods.String())

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if !allocationInfo.CheckDedicated() {
				continue
			}

			if !allocationInfo.CheckNUMABinding() {
				// not to adjust NUMA binding containers
				// update container to target numa set for non-binding share cores
				p.updateNUMASetChangedContainers(numaSetChangedContainers, allocationInfo, numaWithoutNUMABindingPods)
			}
		}
	}
}

// adjustAllocationEntriesForSystemCores adjusts the allocation entries for system cores pods.
func (p *DynamicPolicy) adjustAllocationEntriesForSystemCores(numaSetChangedContainers map[string]map[string]*state.AllocationInfo,
	podEntries state.PodEntries, machineState state.NUMANodeMap,
) {
	defaultSystemCoresNUMAs := p.getDefaultSystemCoresNUMAs(machineState)

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if !allocationInfo.CheckSystem() {
				continue
			}

			if allocationInfo.CheckNUMABinding() {
				// update container to target numa set for system_cores pod with NUMA binding
				// todo: currently we only update cpuset.mems for system_cores pods to NUMAs without dedicated and NUMA binding and NUMA exclusive pod,
				// 		in the future, we will update cpuset.mems for system_cores according to their cpuset_pool annotation.
				p.updateNUMASetChangedContainers(numaSetChangedContainers, allocationInfo, defaultSystemCoresNUMAs)
			}
		}
	}
}

func (p *DynamicPolicy) adjustAllocationEntriesForReclaimedCores(numaSetChangedContainers map[string]map[string]*state.AllocationInfo,
	podEntries state.PodEntries, machineState state.NUMANodeMap,
) {
	nonReclaimActualBindingNUMAs := machineState.GetNUMANodesWithoutReclaimedActualNUMABindingPods()
	reclaimDisableNUMASet := p.getReclaimDisableNUMASet(podEntries)
	targetNumaSet := nonReclaimActualBindingNUMAs.Difference(reclaimDisableNUMASet)
	if targetNumaSet.IsEmpty() {
		general.Errorf("targetNumaSet is empty, nonReclaimActualBindingNUMAs: %s, reclaimDisableNUMASet: %s",
			nonReclaimActualBindingNUMAs.String(), reclaimDisableNUMASet.String())
	}

	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if !allocationInfo.CheckReclaimed() {
				continue
			}

			if !allocationInfo.CheckActualNUMABinding() && !targetNumaSet.IsEmpty() {
				// update container to target numa set for reclaimed_cores pod without NUMA binding
				p.updateNUMASetChangedContainers(numaSetChangedContainers, allocationInfo, targetNumaSet)
			}
		}
	}
}

// getReclaimDisableNUMASet returns the NUMA set disabled for reclaim_cores pod.
func (p *DynamicPolicy) getReclaimDisableNUMASet(
	podEntries state.PodEntries,
) machine.CPUSet {
	numaSet := machine.NewCPUSet()
	nodeReclaim := p.dynamicConf.GetDynamicConfiguration().EnableReclaim
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == commonstate.FakedContainerName {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			}

			// only for dedicated_cores pod with NUMA binding and NUMA exclusive pod
			if !allocationInfo.CheckMainContainer() ||
				!(allocationInfo.CheckDedicatedNUMABinding() && allocationInfo.CheckNumaExclusive()) {
				continue
			}

			reclaimEnable, err := helper.PodEnableReclaim(context.Background(), p.metaServer, podUID, nodeReclaim)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s get reclaim enable failed: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
				continue
			}

			if !reclaimEnable {
				numaSet = numaSet.Union(allocationInfo.NumaAllocationResult)
			}
		}
	}
	return numaSet
}

func (p *DynamicPolicy) updateNUMASetChangedContainers(numaSetChangedContainers map[string]map[string]*state.AllocationInfo,
	allocationInfo *state.AllocationInfo, targetNumaSet machine.CPUSet,
) {
	if numaSetChangedContainers == nil || allocationInfo == nil {
		return
	}

	// todo: currently we only set cpuset.mems to NUMAs without NUMA binding for pods isn't NUMA binding
	//  when cgroup memory policy becomes ready, we will allocate quantity for each pod meticulously.
	if !allocationInfo.NumaAllocationResult.IsSubsetOf(targetNumaSet) {
		if numaSetChangedContainers[allocationInfo.PodUid] == nil {
			numaSetChangedContainers[allocationInfo.PodUid] = make(map[string]*state.AllocationInfo)
		}
		numaSetChangedContainers[allocationInfo.PodUid][allocationInfo.ContainerName] = allocationInfo
	}

	if !allocationInfo.NumaAllocationResult.Equals(targetNumaSet) {
		general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
			allocationInfo.NumaAllocationResult.String(), targetNumaSet.String())
	}

	allocationInfo.NumaAllocationResult = targetNumaSet.Clone()
	allocationInfo.TopologyAwareAllocations = nil
}

func (p *DynamicPolicy) migratePagesForNUMASetChangedContainers(numaSetChangedContainers map[string]map[string]*state.AllocationInfo) error {
	movePagesWorkers, ok := p.asyncLimitedWorkersMap[memoryPluginAsyncWorkTopicMovePage]
	if !ok {
		return fmt.Errorf("asyncLimitedWorkers for %s not found", memoryPluginAsyncWorkTopicMovePage)
	}

	// drop cache and migrate pages for containers whose numaset changed
	for podUID, containers := range numaSetChangedContainers {
		for containerName, allocationInfo := range containers {
			containerID, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				general.Errorf("get container id of pod: %s container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			container, err := p.metaServer.GetContainerSpec(podUID, containerName)
			if err != nil || container == nil {
				general.Errorf("get container spec for pod: %s, container: %s failed with error: %v", podUID, containerName, err)
				continue
			}

			if !allocationInfo.NumaAllocationResult.IsEmpty() {
				movePagesWorkName := util.GetContainerAsyncWorkName(podUID, containerName,
					memoryPluginAsyncWorkTopicMovePage)
				// start a asynchronous work to migrate pages for containers whose numaset changed and doesn't require numa_binding
				err = movePagesWorkers.AddWork(
					&asyncworker.Work{
						Name: movePagesWorkName,
						UID:  uuid.NewUUID(),
						Fn:   MovePagesForContainer,
						Params: []interface{}{
							podUID, containerID,
							p.topology.CPUDetails.NUMANodes(),
							allocationInfo.NumaAllocationResult.Clone(),
						},
						DeliveredAt: time.Now(),
					}, asyncworker.DuplicateWorkPolicyOverride)
				if err != nil {
					general.Errorf("add work: %s pod: %s container: %s failed with error: %v", movePagesWorkName, podUID, containerName, err)
				}
			}
		}
	}

	return nil
}

// getDefaultSystemCoresNUMAs returns the default system cores NUMAs.
func (p *DynamicPolicy) getDefaultSystemCoresNUMAs(machineState state.NUMANodeMap) machine.CPUSet {
	numaNodesWithoutNUMABindingAndNUMAExclusivePods := machineState.GetNUMANodesWithoutDedicatedNUMABindingAndNUMAExclusivePods()
	general.Infof("numaNodesWithoutNUMABindingAndNUMAExclusivePods: %s", numaNodesWithoutNUMABindingAndNUMAExclusivePods.String())
	if numaNodesWithoutNUMABindingAndNUMAExclusivePods.IsEmpty() {
		// if there is no numa nodes without NUMA binding and NUMA exclusive pods, we will use all numa nodes.
		return p.topology.CPUDetails.NUMANodes()
	}
	return numaNodesWithoutNUMABindingAndNUMAExclusivePods
}

// getReclaimedResourceAllocationAnnotations return the resource allocation annotations for the given allocation.
func (p *DynamicPolicy) getReclaimedResourceAllocationAnnotations(allocation *state.AllocationInfo) map[string]string {
	resourceAllocationAnnotations := make(map[string]string)
	if allocation.CheckReclaimedActualNUMABinding() {
		resourceAllocationAnnotations[p.numaBindResultResourceAllocationAnnotationKey] = allocation.NumaAllocationResult.String()
	}

	if len(resourceAllocationAnnotations) == 0 {
		return nil
	}

	return resourceAllocationAnnotations
}

// updateSpecifiedNUMAAllocation update NUMA allocation by allocation reactor.
func (p *DynamicPolicy) updateSpecifiedNUMAAllocation(ctx context.Context, allocation *state.AllocationInfo) error {
	return p.numaAllocationReactor.UpdateAllocation(ctx, allocation)
}
