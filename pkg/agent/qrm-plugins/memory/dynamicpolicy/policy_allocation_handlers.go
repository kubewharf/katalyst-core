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
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.numaBindingAllocationHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores)
	default:
		return p.allocateNUMAsWithoutNUMABindingPods(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores)
	}

}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	// TODO: currently we set all numas as cpuset.mems for reclaimed_cores containers,
	// 	we will support adjusting cpuset.mems for reclaimed_cores dynamically according to memory advisor.
	// Notice: before supporting dynamic adjustment, not to hybrid reclaimed_cores
	//  with dedicated_cores numa_binding containers.
	return p.allocateAllNUMAs(req, apiconsts.PodAnnotationQoSLevelReclaimedCores)
}

func (p *DynamicPolicy) dedicatedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.numaBindingAllocationHandler(ctx, req, apiconsts.PodAnnotationQoSLevelDedicatedCores)
	default:
		return p.dedicatedCoresWithoutNUMABindingAllocationHandler(ctx, req)
	}
}

func (p *DynamicPolicy) numaBindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.numaBindingAllocationSidecarHandler(ctx, req, qosLevel)
	}

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	memoryState := machineState[v1.ResourceMemory]

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && allocationInfo.AggregatedQuantity >= uint64(reqInt) {
		general.InfoS("already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		resp, packErr := packAllocationResponse(allocationInfo, req)
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %v", packErr)
		}
		return resp, nil
	} else if allocationInfo != nil {
		general.InfoS("not meet requirement, clear record and re-allocate",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)
		delete(podEntries, req.PodUid)

		var stateErr error
		memoryState, stateErr = state.GenerateMemoryStateFromPodEntries(p.state.GetMachineInfo(), podEntries, p.state.GetReservedMemory())
		if stateErr != nil {
			general.ErrorS(stateErr, "generateMemoryMachineStateByPodEntries failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", reqInt,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)
			return nil, fmt.Errorf("generateMemoryMachineStateByPodEntries failed with error: %v", stateErr)
		}
	}

	// call calculateMemoryAllocation to update memoryState in-place,
	// and we can use this adjusted state to pack allocation results
	err = p.calculateMemoryAllocation(req, memoryState, apiconsts.PodAnnotationQoSLevelDedicatedCores)
	if err != nil {
		general.ErrorS(err, "unable to allocate Memory",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq", reqInt)
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
		"reqMemoryQuantity", reqInt,
		"numaAllocationResult", result.String())

	allocationInfo = &state.AllocationInfo{
		PodUid:                   req.PodUid,
		PodNamespace:             req.PodNamespace,
		PodName:                  req.PodName,
		ContainerName:            req.ContainerName,
		ContainerType:            req.ContainerType.String(),
		ContainerIndex:           req.ContainerIndex,
		PodRole:                  req.PodRole,
		PodType:                  req.PodType,
		AggregatedQuantity:       aggregatedQuantity,
		NumaAllocationResult:     result.Clone(),
		TopologyAwareAllocations: topologyAwareAllocations,
		Labels:                   general.DeepCopyMap(req.Labels),
		Annotations:              general.DeepCopyMap(req.Annotations),
		QoSLevel:                 qosLevel,
	}
	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo)

	podResourceEntries = p.state.GetPodResourceEntries()
	machineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate memoryState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(machineState)

	err = p.adjustAllocationEntries()
	if err != nil {
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	_ *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// numaBindingAllocationSidecarHandler allocates for sidecar
// currently, we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) numaBindingAllocationSidecarHandler(_ context.Context,
	req *pluginapi.ResourceRequest, qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
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
		PodUid:                   req.PodUid,
		PodNamespace:             req.PodNamespace,
		PodName:                  req.PodName,
		ContainerName:            req.ContainerName,
		ContainerType:            req.ContainerType.String(),
		ContainerIndex:           req.ContainerIndex,
		PodRole:                  req.PodRole,
		PodType:                  req.PodType,
		AggregatedQuantity:       0,                                                        // not count sidecar quantity
		NumaAllocationResult:     mainContainerAllocationInfo.NumaAllocationResult.Clone(), // pin sidecar to same cpuset.mems of the main container
		TopologyAwareAllocations: nil,                                                      // not count sidecar quantity
		Labels:                   general.DeepCopyMap(req.Labels),
		Annotations:              general.DeepCopyMap(req.Annotations),
		QoSLevel:                 qosLevel,
	}

	// update pod entries directly. if one of subsequent steps is failed,
	// we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo)
	podResourceEntries = p.state.GetPodResourceEntries()
	resourcesState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Infof("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(resourcesState)

	resp, err := packAllocationResponse(allocationInfo, req)
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
	req *pluginapi.ResourceRequest, qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	machineState := p.state.GetMachineState()
	resourceState := machineState[v1.ResourceMemory]
	numaWithoutNUMABindingPods := resourceState.GetNUMANodesWithoutNUMABindingPods()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.String(), numaWithoutNUMABindingPods.String())
	}

	allocationInfo = &state.AllocationInfo{
		PodUid:               req.PodUid,
		PodNamespace:         req.PodNamespace,
		PodName:              req.PodName,
		ContainerName:        req.ContainerName,
		ContainerType:        req.ContainerType.String(),
		ContainerIndex:       req.ContainerIndex,
		PodRole:              req.PodRole,
		PodType:              req.PodType,
		NumaAllocationResult: numaWithoutNUMABindingPods.Clone(),
		Labels:               general.DeepCopyMap(req.Labels),
		Annotations:          general.DeepCopyMap(req.Annotations),
		QoSLevel:             qosLevel,
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podResourceEntries := p.state.GetPodResourceEntries()

	machineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate resourceState by updated pod entries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	p.state.SetMachineState(machineState)
	return resp, nil
}

// allocateAllNUMAs returns all numa node as allocation results,
// and it will store the allocation in states.
func (p *DynamicPolicy) allocateAllNUMAs(req *pluginapi.ResourceRequest,
	qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	allNUMAs := p.topology.CPUDetails.NUMANodes()
	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && !allocationInfo.NumaAllocationResult.Equals(allNUMAs) {
		general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.NumaAllocationResult.String(), allNUMAs.String())
	}

	allocationInfo = &state.AllocationInfo{
		PodUid:               req.PodUid,
		PodNamespace:         req.PodNamespace,
		PodName:              req.PodName,
		ContainerName:        req.ContainerName,
		ContainerType:        req.ContainerType.String(),
		ContainerIndex:       req.ContainerIndex,
		PodRole:              req.PodRole,
		PodType:              req.PodType,
		NumaAllocationResult: allNUMAs.Clone(),
		Labels:               general.DeepCopyMap(req.Labels),
		Annotations:          general.DeepCopyMap(req.Annotations),
		QoSLevel:             qosLevel,
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podResourceEntries := p.state.GetPodResourceEntries()

	machineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	resp, err := packAllocationResponse(allocationInfo, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packAllocationResponse failed with error: %v", err)
	}

	p.state.SetMachineState(machineState)
	return resp, nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint,
// and it will be called when entries without numa binding should be adjusted
// according to current entries and machine state.
func (p *DynamicPolicy) adjustAllocationEntries() error {
	resourcesMachineState := p.state.GetMachineState()
	podResourceEntries := p.state.GetPodResourceEntries()
	machineState := resourcesMachineState[v1.ResourceMemory]
	podEntries := podResourceEntries[v1.ResourceMemory]

	numaWithoutNUMABindingPods := machineState.GetNUMANodesWithoutNUMABindingPods()
	general.Infof("numaWithoutNUMABindingPods: %s", numaWithoutNUMABindingPods.String())

	// for numaSetChangedContainers, we should reset their allocation info and
	// trigger necessary Knob actions (like dropping caches or migrate memory
	// to make sure already-allocated memory cooperate with the new numaset)
	numaSetChangedContainers := make(map[string]map[string]bool)
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			} else if containerName == "" {
				general.Errorf("pod: %s has empty containerName entry", podUID)
				continue
			} else if allocationInfo.CheckNumaBinding() {
				// not to adjust NUMA binding containers
				continue
			} else if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores {
				// todo: consider strategy here after supporting cpuset.mems dynamic adjustment
				continue
			}

			// todo: currently we only set cpuset.mems to NUMAs without NUMA binding for pods isn't NUMA binding
			//  when cgroup memory policy becomes ready, we will allocate quantity for each pod meticulously.
			if !allocationInfo.NumaAllocationResult.IsSubsetOf(numaWithoutNUMABindingPods) {
				if numaSetChangedContainers[podUID] == nil {
					numaSetChangedContainers[podUID] = make(map[string]bool)
				}
				numaSetChangedContainers[podUID][containerName] = true
			}

			if !allocationInfo.NumaAllocationResult.Equals(numaWithoutNUMABindingPods) {
				general.Infof("pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
					allocationInfo.NumaAllocationResult.String(), numaWithoutNUMABindingPods.String())
			}

			allocationInfo.AggregatedQuantity = 0
			allocationInfo.NumaAllocationResult = numaWithoutNUMABindingPods.Clone()
			allocationInfo.TopologyAwareAllocations = nil
		}
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)

	// drop cache and migrate pages for containers whose numaset changed
	for podUID, containers := range numaSetChangedContainers {
		for containerName := range containers {
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

			if !numaWithoutNUMABindingPods.IsEmpty() {
				migratePagesWorkName := util.GetContainerAsyncWorkName(podUID, containerName,
					memoryPluginAsyncWorkTopicMovePage)
				// start a asynchronous work to migrate pages for containers whose numaset changed and doesn't require numa_binding
				err = p.asyncWorkers.AddWork(migratePagesWorkName,
					&asyncworker.Work{
						Fn: MovePagesForContainer,
						Params: []interface{}{podUID, containerID,
							p.topology.CPUDetails.NUMANodes(),
							numaWithoutNUMABindingPods.Clone()},
						DeliveredAt: time.Now()}, asyncworker.DuplicateWorkPolicyOverride)

				if err != nil {
					general.Errorf("add work: %s pod: %s container: %s failed with error: %v", migratePagesWorkName, podUID, containerName, err)
				}
			}

			dropCacheWorkName := util.GetContainerAsyncWorkName(podUID, containerName,
				memoryPluginAsyncWorkTopicDropCache)
			// start a asynchronous work to drop cache for the container whose numaset changed and doesn't require numa_binding
			err = p.asyncWorkers.AddWork(dropCacheWorkName,
				&asyncworker.Work{
					Fn:          cgroupmgr.DropCacheWithTimeoutForContainer,
					Params:      []interface{}{podUID, containerID, dropCacheTimeoutSeconds, GetFullyDropCacheBytes(container)},
					DeliveredAt: time.Now()}, asyncworker.DuplicateWorkPolicyOverride)

			if err != nil {
				general.Errorf("add work: %s pod: %s container: %s failed with error: %v", dropCacheWorkName, podUID, containerName, err)
			}
		}
	}

	return nil
}

// calculateMemoryAllocation will not store the allocation in states, instead,
// it will update the passed by machineState in-place; so the function will be
// called `calculateXXX` rather than `allocateXXX`
func (p *DynamicPolicy) calculateMemoryAllocation(req *pluginapi.ResourceRequest, machineState state.NUMANodeMap, qosLevel string) error {
	if req.Hint == nil {
		return fmt.Errorf("hint is nil")
	} else if len(req.Hint.Nodes) == 0 {
		return fmt.Errorf("hint is empty")
	} else if qosutil.AnnotationsIndicateNUMABinding(req.Annotations) &&
		!qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) &&
		len(req.Hint.Nodes) > 1 {
		return fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	memoryReq, _, err := util.GetQuantityFromResourceReq(req)

	if err != nil {
		return fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	hintNumaNodes := machine.NewCPUSet(util.HintToIntArray(req.Hint)...)
	general.InfoS("allocate by hints",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"hints", hintNumaNodes.String(),
		"reqMemoryQuantity", memoryReq)

	var leftQuantity uint64

	if qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) {
		leftQuantity, err = calculateExclusiveMemory(req, machineState, hintNumaNodes.ToSliceInt(), uint64(memoryReq), qosLevel)
		if err != nil {
			return fmt.Errorf("calculateExclusiveMemory failed with error: %v", err)
		}
	} else {
		leftQuantity, err = calculateMemoryInNumaNodes(req, machineState, hintNumaNodes.ToSliceInt(), uint64(memoryReq), qosLevel)
		if err != nil {
			return fmt.Errorf("calculateMemoryInNumaNodes failed with error: %v", err)
		}
	}

	if leftQuantity > 0 {
		general.Errorf("hint NUMA nodes: %s can't meet memory request: %d bytes, leftQuantity: %d bytes",
			hintNumaNodes.String(), memoryReq, leftQuantity)
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
	machineState state.NUMANodeMap, numaNodes []int, reqQuantity uint64, qosLevel string) (leftQuantity uint64, err error) {
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
			PodUid:               req.PodUid,
			PodNamespace:         req.PodNamespace,
			PodName:              req.PodName,
			ContainerName:        req.ContainerName,
			ContainerType:        req.ContainerType.String(),
			ContainerIndex:       req.ContainerIndex,
			PodRole:              req.PodRole,
			PodType:              req.PodType,
			AggregatedQuantity:   curNumaNodeAllocated,
			NumaAllocationResult: machine.NewCPUSet(numaNode),
			TopologyAwareAllocations: map[int]uint64{
				numaNode: curNumaNodeAllocated,
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
			QoSLevel:    qosLevel,
		}
	}

	return reqQuantity, nil
}

// calculateMemoryInNumaNodes tries to allocate memories in the numa list to
// the given container, and returns the remaining un-satisfied quantity.
func calculateMemoryInNumaNodes(req *pluginapi.ResourceRequest,
	machineState state.NUMANodeMap, numaNodes []int,
	reqQuantity uint64, qosLevel string) (leftQuantity uint64, err error) {

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
			PodUid:               req.PodUid,
			PodNamespace:         req.PodNamespace,
			PodName:              req.PodName,
			ContainerName:        req.ContainerName,
			ContainerType:        req.ContainerType.String(),
			ContainerIndex:       req.ContainerIndex,
			PodRole:              req.PodRole,
			PodType:              req.PodType,
			AggregatedQuantity:   curNumaNodeAllocated,
			NumaAllocationResult: machine.NewCPUSet(numaNode),
			TopologyAwareAllocations: map[int]uint64{
				numaNode: curNumaNodeAllocated,
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
			QoSLevel:    qosLevel,
		}
	}

	return reqQuantity, nil
}

// packAllocationResponse fills pluginapi.ResourceAllocationResponse with information from AllocationInfo and pluginapi.ResourceRequest
func packAllocationResponse(allocationInfo *state.AllocationInfo, req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
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
