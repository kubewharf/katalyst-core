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
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	pooledCPUs := machineState.GetAvailableCPUSetExcludeDedicatedCoresPods(p.reservedCPUs)

	if pooledCPUs.IsEmpty() {
		klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s get empty pooledCPUs",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("get empty pooledCPUs")
	}

	pooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, pooledCPUs)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s GetTopologyAwareAssignmentsByCPUSet failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GetTopologyAwareAssignmentsByCPUSet failed with error: %v", err)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)

	needSet := true
	if allocationInfo == nil {
		klog.Infof("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s is met firstly, do ramp up with pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo = &state.AllocationInfo{
			PodUid:                           req.PodUid,
			PodNamespace:                     req.PodNamespace,
			PodName:                          req.PodName,
			ContainerName:                    req.ContainerName,
			ContainerType:                    req.ContainerType.String(),
			ContainerIndex:                   req.ContainerIndex,
			RampUp:                           true,
			OwnerPoolName:                    "", // fill OwnerPoolName with empty string when ramping up
			PodRole:                          req.PodRole,
			PodType:                          req.PodType,
			AllocationResult:                 pooledCPUs,
			OriginalAllocationResult:         pooledCPUs.Clone(),
			TopologyAwareAssignments:         pooledCPUsTopologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments),
			InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
			Labels:                           general.DeepCopyMap(req.Labels),
			Annotations:                      general.DeepCopyMap(req.Annotations),
			QoSLevel:                         apiconsts.PodAnnotationQoSLevelSharedCores,
			RequestQuantity:                  reqInt,
		}
	} else if allocationInfo.RampUp {
		klog.Infof("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s is still in ramp up, allocate pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo.AllocationResult = pooledCPUs
		allocationInfo.OriginalAllocationResult = pooledCPUs.Clone()
		allocationInfo.TopologyAwareAssignments = pooledCPUsTopologyAwareAssignments
		allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)
	} else {
		// need to adjust pools and putContainersAndAdjustAllocationEntries will set the allocationInfo after adjusted
		err = p.putContainersAndAdjustAllocationEntries([]*state.AllocationInfo{allocationInfo})
		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s putContainerAndReGeneratePool failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("putContainerAndReGeneratePool failed with error: %v", err)
		}

		allocationInfo = p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		if allocationInfo == nil {
			klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s get nil allocationInfo after called putContainerAndReGeneratePool",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("putContainerAndReGeneratePool failed with error: %v", err)
		}

		needSet = false
	}

	if needSet {
		// update pod entries directly.
		// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
		p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
		podEntries := p.state.GetPodEntries()

		updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
		}

		p.state.SetMachineState(updatedMachineState)
	}

	resp, err := packCPUResourceAllocationResponseByAllocationInfo(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	return resp, nil
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)

	reclaimedAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReclaim, "")
	if reclaimedAllocationInfo == nil {
		klog.Errorf("[CPUDynamicPolicy.reclaimedCoresAllocationHandler] allocation for pod: %s/%s, container: %s is failed, because pool: %s is not ready",
			req.PodNamespace, req.PodName, req.ContainerName, state.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not ready", state.PoolNameReclaim)
	} else if reclaimedAllocationInfo.AllocationResult.Size() == 0 {
		klog.Errorf("[CPUDynamicPolicy.reclaimedCoresAllocationHandler] allocation for pod: %s/%s, container: %s is failed, because pool: %s is empty",
			req.PodNamespace, req.PodName, req.ContainerName, state.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not empty", state.PoolNameReclaim)
	}

	if allocationInfo != nil {
		klog.Infof("[CPUDynamicPolicy.reclaimedCoresAllocationHandler] pod: %s/%s, container: %s with old allocation result: %s, allocate by reclaimedCPUSet: %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.AllocationResult.String(), reclaimedAllocationInfo.AllocationResult.String())
	} else {
		klog.Infof("[CPUDynamicPolicy.reclaimedCoresAllocationHandler] pod: %s/%s, container: %s is firstly met, allocate by reclaimedCPUSet: %s",
			req.PodNamespace, req.PodName, req.ContainerName, reclaimedAllocationInfo.AllocationResult.String())

		allocationInfo = &state.AllocationInfo{
			PodUid:          req.PodUid,
			PodNamespace:    req.PodNamespace,
			PodName:         req.PodName,
			ContainerName:   req.ContainerName,
			ContainerType:   req.ContainerType.String(),
			ContainerIndex:  req.ContainerIndex,
			OwnerPoolName:   state.PoolNameReclaim,
			PodRole:         req.PodRole,
			PodType:         req.PodType,
			InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
			Labels:          general.DeepCopyMap(req.Labels),
			Annotations:     general.DeepCopyMap(req.Annotations),
			QoSLevel:        apiconsts.PodAnnotationQoSLevelReclaimedCores,
			RequestQuantity: reqInt,
		}
	}

	allocationInfo.AllocationResult = reclaimedAllocationInfo.AllocationResult.Clone()
	allocationInfo.OriginalAllocationResult = reclaimedAllocationInfo.OriginalAllocationResult.Clone()
	allocationInfo.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(reclaimedAllocationInfo.TopologyAwareAssignments)
	allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(reclaimedAllocationInfo.OriginalTopologyAwareAssignments)

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.sharedCoresAllocationHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
	}

	resp, err := packCPUResourceAllocationResponseByAllocationInfo(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.reclaimedCoresAllocationHandler] pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	p.state.SetMachineState(updatedMachineState)

	return resp, nil
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

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.dedicatedCoresWithNUMABindingAllocationSidecarHandler(ctx, req)
	}

	var machineState state.NUMANodeMap

	oldAllocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)

	if oldAllocationInfo == nil {
		machineState = p.state.GetMachineState()
	} else {
		p.state.Delete(req.PodUid, req.ContainerName)
		podEntries := p.state.GetPodEntries()

		var err error
		machineState, err = state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
		}
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	result, err := p.allocateCPUs(reqInt, req.Hint, machineState)
	if err != nil {
		klog.ErrorS(err, "[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] Unable to allocate CPUs",
			"podNamespace", req.PodNamespace, "podName", req.PodName, "containerName", req.ContainerName, "numCPUs", reqInt)
		return nil, err
	}

	klog.InfoS("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"numCPUs", reqInt,
		"result", result.String())

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, result)
	if err != nil {
		klog.ErrorS(err, "[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] Unable to calculate topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt,
			"result cpuset", result.String())
		return nil, err
	}

	allocationInfo := &state.AllocationInfo{
		PodUid:                           req.PodUid,
		PodNamespace:                     req.PodNamespace,
		PodName:                          req.PodName,
		ContainerName:                    req.ContainerName,
		ContainerType:                    req.ContainerType.String(),
		ContainerIndex:                   req.ContainerIndex,
		RampUp:                           true,
		PodRole:                          req.PodRole,
		PodType:                          req.PodType,
		OwnerPoolName:                    state.PoolNameDedicated,
		AllocationResult:                 result.Clone(),
		OriginalAllocationResult:         result.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		QoSLevel:                         apiconsts.PodAnnotationQoSLevelDedicatedCores,
		Labels:                           general.DeepCopyMap(req.Labels),
		Annotations:                      general.DeepCopyMap(req.Annotations),
		RequestQuantity:                  reqInt,
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)

	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
	}

	p.state.SetMachineState(updatedMachineState)

	err = p.adjustAllocationEntries()
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s putContainersAndAdjustAllocationEntriesWithoutAllocation failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := packCPUResourceAllocationResponseByAllocationInfo(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationHandler] pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	return resp, nil
}

// dedicatedCoresWithNUMABindingAllocationSidecarHandler currently we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationSidecarHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	if podEntries[req.PodUid] == nil {
		klog.Infof("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
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
		klog.Infof("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	allocationInfo := &state.AllocationInfo{
		PodUid:                           req.PodUid,
		PodNamespace:                     req.PodNamespace,
		PodName:                          req.PodName,
		ContainerName:                    req.ContainerName,
		ContainerType:                    req.ContainerType.String(),
		ContainerIndex:                   req.ContainerIndex,
		PodRole:                          req.PodRole,
		PodType:                          req.PodType,
		AllocationResult:                 mainContainerAllocationInfo.AllocationResult.Clone(),
		OriginalAllocationResult:         mainContainerAllocationInfo.OriginalAllocationResult.Clone(),
		TopologyAwareAssignments:         machine.DeepcopyCPUAssignment(mainContainerAllocationInfo.TopologyAwareAssignments),
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(mainContainerAllocationInfo.OriginalTopologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		QoSLevel:                         apiconsts.PodAnnotationQoSLevelDedicatedCores,
		Labels:                           general.DeepCopyMap(req.Labels),
		Annotations:                      general.DeepCopyMap(req.Annotations),
		RequestQuantity:                  reqInt,
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podEntries = p.state.GetPodEntries()

	updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
	}

	p.state.SetMachineState(updatedMachineState)

	resp, err := packCPUResourceAllocationResponseByAllocationInfo(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingAllocationSidecarHandler] pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}

	return resp, nil
}

func (p *DynamicPolicy) allocateCPUs(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap) (machine.CPUSet, error) {

	if hint == nil {
		return machine.NewCPUSet(), fmt.Errorf("hint is nil")
	} else if len(hint.Nodes) == 0 {
		return machine.NewCPUSet(), fmt.Errorf("hint is empty")
	}

	result := machine.NewCPUSet()
	alignedAvailableCPUs := machine.CPUSet{}
	for _, numaNode := range hint.Nodes {
		alignedAvailableCPUs = alignedAvailableCPUs.Union(machineState[int(numaNode)].GetAvailableCPUSet(p.reservedCPUs))
	}

	// todo: currently we hack dedicated_cores with NUMA binding take up whole NUMA,
	//  and we will modify strategy here if assumption above breaks.
	alignedCPUs := alignedAvailableCPUs.Clone()

	klog.InfoS("[CPUDynamicPolicy.allocateCPUs] allocate by hints",
		"hints", hint.Nodes,
		"alignedAvailableCPUs", alignedAvailableCPUs.String(),
		"alignedAllocatedCPUs", alignedCPUs)

	result = result.Union(alignedCPUs)

	leftNumCPUs := numCPUs - result.Size()
	if leftNumCPUs > 0 {
		klog.Errorf("[CPUDynamicPolicy.allocateCPUs] result cpus: %s in hint NUMA nodes: %d with size: %d cann't meet cpus request: %d",
			result.String(), hint.Nodes, result.Size(), numCPUs)

		return machine.NewCPUSet(), fmt.Errorf("results can't meet cpus request")
	}

	return result, nil
}

func packCPUResourceAllocationResponseByAllocationInfo(allocationInfo *state.AllocationInfo, resourceName,
	ociPropertyName string, isNodeResource, isScalarResource bool, req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
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
		ResourceName:   resourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				resourceName: {
					OciPropertyName:   ociPropertyName,
					IsNodeResource:    isNodeResource,
					IsScalarResource:  isScalarResource,
					AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
					AllocationResult:  allocationInfo.AllocationResult.String(),
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
