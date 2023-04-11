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

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	return p.allocateNUMAsWithoutNUMABindingPods(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores)
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	// [TODO]: currently we set all numas as cpuset.mems for reclaimed_cores containers,
	// we will support adjusting cpuset.mems for reclaimed_cores dynamically according to memory advisor.
	// [Notice]: before supporting dynamic adjustment, not to hybrid reclaimed_cores with dedicated_cores numa_binding containers.
	return p.allocateAllNUMAs(ctx, req, apiconsts.PodAnnotationQoSLevelReclaimedCores)
}

func (p *DynamicPolicy) dedicatedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.dedicatedCoresWithNUMABindingAllocationHandler(ctx, req)
	default:
		return p.dedicatedCoresWithoutNUMABindingAllocationHandler(ctx, req)
	}
}

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.dedicatedCoresWithNUMABindingAllocationSidecarHandler(ctx, req)
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	resourcesMachineState := p.state.GetMachineState()
	podResourceEntries := p.state.GetPodResourceEntries()
	machineState := resourcesMachineState[v1.ResourceMemory]
	podEntries := podResourceEntries[v1.ResourceMemory]
	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)

	if allocationInfo != nil && allocationInfo.AggregatedQuantity >= uint64(reqInt) {
		klog.InfoS("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		resp, err := packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo, req)
		if err != nil {
			klog.Errorf("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v", err)
		}

		return resp, nil
	} else if allocationInfo != nil {
		klog.InfoS("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] not meet requirement, clear record and re-allocate",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		delete(podEntries, req.PodUid)
		var err error
		machineState, err = state.GenerateMemoryMachineStateFromPodEntries(p.state.GetMachineInfo(), podEntries, p.state.GetReservedMemory())
		if err != nil {
			klog.ErrorS(err, "[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] generateMemoryMachineStateByPodEntries failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"memoryReq(bytes)", reqInt,
				"currentResult(bytes)", allocationInfo.AggregatedQuantity)
			return nil, fmt.Errorf("generateMemoryMachineStateByPodEntries failed with error: %v", err)
		}
	}

	err = p.allocateMemory(req, machineState, apiconsts.PodAnnotationQoSLevelDedicatedCores)
	if err != nil {
		klog.ErrorS(err, "Unable to allocate Memorys",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq", reqInt)
		return nil, err
	}

	topologyAwareAllocations := make(map[int]uint64)

	result := machine.NewCPUSet()
	var aggregatedQuantity uint64 = 0
	for numaNode, numaNodeState := range machineState {
		if numaNodeState.PodEntries[req.PodUid][req.ContainerName] != nil &&
			numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity > 0 {
			result = result.Union(machine.NewCPUSet(numaNode))
			aggregatedQuantity += numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity
			topologyAwareAllocations[numaNode] = numaNodeState.PodEntries[req.PodUid][req.ContainerName].AggregatedQuantity
		}
	}

	klog.InfoS("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] allocate memory successfully",
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
		QoSLevel:                 apiconsts.PodAnnotationQoSLevelDedicatedCores,
	}

	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo)

	podResourceEntries = p.state.GetPodResourceEntries()
	resourcesMachineState, err = state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())

	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(resourcesMachineState)

	err = p.adjustAllocationEntries()
	if err != nil {
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo, req)
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	return resp, nil
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// dedicatedCoresWithNUMABindingAllocationSidecarHandler allocates for sidecar
// currently, we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationSidecarHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	if podEntries[req.PodUid] == nil {
		klog.Infof("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	var mainContainerAllocationInfo *state.AllocationInfo
	for _, siblingAllocationInfo := range podEntries[req.PodUid] {
		if siblingAllocationInfo.ContainerType == pluginapi.ContainerType_MAIN.String() {
			mainContainerAllocationInfo = siblingAllocationInfo
			break
		}
	}

	// todo: consider sidecar without reconcile in vpa
	if mainContainerAllocationInfo == nil {
		klog.Infof("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
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
		QoSLevel:                 apiconsts.PodAnnotationQoSLevelDedicatedCores,
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName, allocationInfo)
	podResourceEntries = p.state.GetPodResourceEntries()
	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())

	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] pod: %s/%s, container: %s GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}
	p.state.SetMachineState(resourcesMachineState)

	resp, err := packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo, req)

	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	return resp, nil

}

// allocateNUMAsWithoutNUMABindingPods is used both for sharedCoresAllocationHandler and reclaimedCoresAllocationHandler
func (p *DynamicPolicy) allocateNUMAsWithoutNUMABindingPods(ctx context.Context, req *pluginapi.ResourceRequest, qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	resourcesMachineState := p.state.GetMachineState()
	machineState := resourcesMachineState[v1.ResourceMemory]
	numaWithoutNUMABindingPods := machineState.GetNUMANodesWithoutNUMABindingPods()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		klog.Infof("[MemoryDynamicPolicy.allocateNUMAsWithoutNUMABindingPods] pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
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

	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.allocateNUMAsWithoutNUMABindingPods] pod: %s/%s, container: %s GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	resp, err := packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo, req)
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.allocateNUMAsWithoutNUMABindingPods] pod: %s/%s, container: %s packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	p.state.SetMachineState(resourcesMachineState)

	return resp, nil
}

// adjustAllocationEntries calculates and generates latest checkopoint,
// and it will be called when entries without numa binding should be adjusted according to current entries and machine state.
func (p *DynamicPolicy) adjustAllocationEntries() error {
	resourcesMachineState := p.state.GetMachineState()
	podResourceEntries := p.state.GetPodResourceEntries()
	machineState := resourcesMachineState[v1.ResourceMemory]
	podEntries := podResourceEntries[v1.ResourceMemory]

	numaWithoutNUMABindingPods := machineState.GetNUMANodesWithoutNUMABindingPods()
	klog.Infof("[MemoryDynamicPolicy.adjustAllocationEntries] numaWithoutNUMABindingPods: %s", numaWithoutNUMABindingPods.String())

	numaSetChangedContainers := make(map[string]map[string]bool)
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				klog.Errorf("[MemoryDynamicPolicy.adjustAllocationEntries] pod: %s, container: %s has nil allocationInfo",
					podUID, containerName)
				continue
			} else if containerName == "" {
				klog.Errorf("[MemoryDynamicPolicy.adjustAllocationEntries] pod: %s has empty containerName entry",
					podUID)
				continue
			} else if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] == apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				// not to adjust NUMA binding containers
				continue
			} else if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores {
				// [TODO]: consider strategy here after supporting cpuset.mems dynamic adjustment
				continue
			}

			// todo: currently we only set cpuset.mems to NUMAs without NUMA binding for pods isn't NUMA binding
			//  when cgroup memory policy becomes ready, we will allocate quantity for each pod meticulously.
			if !allocationInfo.NumaAllocationResult.Equals(numaWithoutNUMABindingPods) {
				if numaSetChangedContainers[podUID] == nil {
					numaSetChangedContainers[podUID] = make(map[string]bool)
				}

				numaSetChangedContainers[podUID][containerName] = true

				klog.Infof("[MemoryDynamicPolicy.adjustAllocationEntries] pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.NumaAllocationResult.String(), numaWithoutNUMABindingPods.String())
			}

			allocationInfo.AggregatedQuantity = 0
			allocationInfo.NumaAllocationResult = numaWithoutNUMABindingPods.Clone()
			allocationInfo.TopologyAwareAllocations = nil
		}
	}

	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())

	if err != nil {
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)

	// drop cache for containers whose numaset changed
	for podUID, containers := range numaSetChangedContainers {
		for containerName := range containers {
			go func(curPodUID, curContainerName string) {
				containerId, err := p.metaServer.GetContainerID(curPodUID, curContainerName)
				if err != nil {
					klog.Errorf("[MemoryDynamicPolicy.adjustAllocationEntries] get container id of pod: %s container: %s failed with error: %v",
						curPodUID, curContainerName, err)
					return
				}

				err = cgroupcmutils.DropCacheWithTimeoutForContainer(curPodUID, containerId, 30)
				if err != nil {
					klog.Errorf("[MemoryDynamicPolicy.adjustAllocationEntries] drop cache of pod: %s container: %s failed with error: %v",
						curPodUID, curContainerName, err)
					return
				}

				klog.Infof("[MemoryDynamicPolicy.adjustAllocationEntries] drop cache of pod: %s container: %s successfully", curPodUID, curContainerName)
			}(podUID, containerName)
		}
	}

	return nil
}

func (p *DynamicPolicy) allocateMemory(req *pluginapi.ResourceRequest, machineState state.NUMANodeMap, qosLevel string) error {
	if req.Hint == nil {
		return fmt.Errorf("hint is nil")
	}

	memoryReq, err := getReqQuantityFromResourceReq(req)

	if err != nil {
		return fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	hintNumaNodes := machine.NewCPUSet(util.HintToIntArray(req.Hint)...)
	klog.InfoS("[MemoryDynamicPolicy.allocateMemory] to allocate by hints",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"hints", hintNumaNodes.String(),
		"reqMemoryQuantity", memoryReq)

	// todo: currently we hack dedicated_cores with NUMA binding take up whole NUMA,
	//  and we will modify strategy here if assumption above breaks.
	leftQuantity := allocateAllFreeMemoryInNumaNodes(req, machineState, hintNumaNodes.ToSliceInt(), uint64(memoryReq), qosLevel)

	if leftQuantity > 0 {
		klog.Errorf("[MemoryDynamicPolicy.allocateMemory] hint NUMA nodes: %s can't meet memory request: %d bytes, leftQuantity: %s",
			hintNumaNodes.String(), memoryReq, leftQuantity)

		return fmt.Errorf("results can't meet memory request")
	}

	return nil
}

// allocateAllFreeMemoryInNumaNodes tries to allocate all memories in the numa list to
// the given container, and returns the remaining un-satisfied quantity.
func allocateAllFreeMemoryInNumaNodes(req *pluginapi.ResourceRequest,
	machineState state.NUMANodeMap, numaNodes []int, reqQuantity uint64, qosLevel string) (leftQuantity uint64) {
	for _, numaNode := range numaNodes {
		var curNumaNodeAllocated uint64 = 0

		numaNodeState := machineState[numaNode]
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

	return reqQuantity
}

func packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo *state.AllocationInfo, req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo got nil allocationInfo")
	} else if req == nil {
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo got nil request")
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

func (p *DynamicPolicy) allocateAllNUMAs(ctx context.Context, req *pluginapi.ResourceRequest, qosLevel string) (*pluginapi.ResourceAllocationResponse, error) {
	if !pluginapi.SupportedKatalystQoSLevels.Has(qosLevel) {
		return nil, fmt.Errorf("invalid qosLevel: %s", qosLevel)
	}

	allNUMAs := p.topology.CPUDetails.NUMANodes()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && !allocationInfo.NumaAllocationResult.Equals(allNUMAs) {
		klog.Infof("[MemoryDynamicPolicy.allocateNUMAsWithoutNUMABindingPods] pod: %s/%s, container: %s change cpuset.mems from: %s to %s",
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

	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.allocateAllNUMAs] pod: %s/%s, container: %s GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	resp, err := packMemoryResourceAllocationResponseByAllocationInfo(allocationInfo, req)
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.allocateAllNUMAs] pod: %s/%s, container: %s packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("packMemoryResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	p.state.SetMachineState(resourcesMachineState)

	return resp, nil
}
