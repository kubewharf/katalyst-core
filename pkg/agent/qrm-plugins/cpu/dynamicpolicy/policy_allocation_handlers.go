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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, state.CheckDedicated)
	if pooledCPUs.IsEmpty() {
		general.Errorf("pod: %s/%s, container: %s get empty pooledCPUs", req.PodNamespace, req.PodName, req.ContainerName)
		return nil, fmt.Errorf("get empty pooledCPUs")
	}

	pooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, pooledCPUs)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GetTopologyAwareAssignmentsByCPUSet failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GetTopologyAwareAssignmentsByCPUSet failed with error: %v", err)
	}

	needSet := true
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		general.Infof("pod: %s/%s, container: %s is met firstly, do ramp up with pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo = &state.AllocationInfo{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType.String(),
			ContainerIndex: req.ContainerIndex,
			RampUp:         true,
			// fill OwnerPoolName with empty string when ramping up
			OwnerPoolName:                    advisorapi.EmptyOwnerPoolName,
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
		general.Infof("pod: %s/%s, container: %s is still in ramp up, allocate pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo.AllocationResult = pooledCPUs
		allocationInfo.OriginalAllocationResult = pooledCPUs.Clone()
		allocationInfo.TopologyAwareAssignments = pooledCPUsTopologyAwareAssignments
		allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)
	} else {
		// need to adjust pools and setAllocationsAndAdjustAllocationEntries will set the allocationInfo after adjusted
		err = p.setAllocationsAndAdjustAllocationEntries([]*state.AllocationInfo{allocationInfo})
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s putContainerAndReGeneratePool failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("putContainerAndReGeneratePool failed with error: %v", err)
		}

		allocationInfo = p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		if allocationInfo == nil {
			general.Errorf("pod: %s/%s, container: %s get nil allocationInfo after putContainerAndReGeneratePool",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("putContainerAndReGeneratePool failed with error: %v", err)
		}
		needSet = false
	}

	if needSet {
		// update pod entries directly.
		// if one of subsequent steps is failed,
		// we will delete current allocationInfo from podEntries in defer function of allocation function.
		p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
		podEntries := p.state.GetPodEntries()

		updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
		p.state.SetMachineState(updatedMachineState)
	}

	resp, err := rePackAllocations(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s rePackAllocations failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	reclaimedAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReclaim, advisorapi.FakedContainerID)
	if reclaimedAllocationInfo == nil {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is not ready",
			req.PodNamespace, req.PodName, req.ContainerName, state.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not ready", state.PoolNameReclaim)
	} else if reclaimedAllocationInfo.AllocationResult.Size() == 0 {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is empty",
			req.PodNamespace, req.PodName, req.ContainerName, state.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not empty", state.PoolNameReclaim)
	}

	if allocationInfo != nil {
		general.Infof("pod: %s/%s, container: %s with old allocation result: %s, allocate by reclaimedCPUSet: %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.AllocationResult.String(), reclaimedAllocationInfo.AllocationResult.String())
	} else {
		general.Infof("pod: %s/%s, container: %s is firstly met, allocate by reclaimedCPUSet: %s",
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

	updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	resp, err := rePackAllocations(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s rePackAllocations failed with error: %v",
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

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	_ *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
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
		machineState, err = state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	result, err := p.allocateNumaBindingCPUs(reqInt, req.Hint, machineState)
	if err != nil {
		general.ErrorS(err, "unable to allocate CPUs",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt)
		return nil, err
	}

	general.InfoS("allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"numCPUs", reqInt,
		"result", result.String())

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, result)
	if err != nil {
		general.ErrorS(err, "unable to calculate topologyAwareAssignments",
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

	updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState)

	err = p.adjustAllocationEntries()
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s putContainersAndAdjustAllocationEntriesWithoutAllocation failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := rePackAllocations(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

// dedicatedCoresWithNUMABindingAllocationSidecarHandler currently we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationSidecarHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	if podEntries[req.PodUid] == nil {
		general.Infof("there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
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
		general.Infof("main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
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

	updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState)

	resp, err := rePackAllocations(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s rePackAllocations failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) allocateNumaBindingCPUs(numCPUs int, hint *pluginapi.TopologyHint,
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

	general.InfoS("allocate by hints",
		"hints", hint.Nodes,
		"alignedAvailableCPUs", alignedAvailableCPUs.String(),
		"alignedAllocatedCPUs", alignedCPUs)

	result = result.Union(alignedCPUs)
	leftNumCPUs := numCPUs - result.Size()
	if leftNumCPUs > 0 {
		general.Errorf("result cpus: %s in hint NUMA nodes: %d with size: %d can't meet cpus request: %d",
			result.String(), hint.Nodes, result.Size(), numCPUs)

		return machine.NewCPUSet(), fmt.Errorf("results can't meet cpus request")
	}
	return result, nil
}

// setAllocationsAndAdjustAllocationEntries calculates and generates the latest checkpoint
// - unlike adjustAllocationEntries, it will also consider AllocationInfo
func (p *DynamicPolicy) setAllocationsAndAdjustAllocationEntries(allocationInfos []*state.AllocationInfo) error {
	if len(allocationInfos) == 0 {
		return nil
	}

	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	var poolsQuantityMap map[string]int
	if p.enableCPUSysAdvisor {
		// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates
		poolsQuantityMap = machine.GetQuantityMap(entries.GetCPUSetMapForPools(state.ResidentPools))
	} else {
		// else we do sum(containers req) for each pool to get pools ratio
		poolsQuantityMap = state.GetSharedQuantityMapFromPodEntries(entries, allocationInfos)
	}

	for _, allocationInfo := range allocationInfos {
		if allocationInfo == nil {
			return fmt.Errorf("found nil allocationInfo in input parameter")
		} else if !state.CheckShared(allocationInfo) {
			return fmt.Errorf("put container with invalid qos level: %s into pool", allocationInfo.QoSLevel)
		}

		poolName := allocationInfo.GetSpecifiedPoolName()
		if poolName == advisorapi.EmptyOwnerPoolName {
			return fmt.Errorf("allocationInfo points to empty poolName")
		}

		reqInt := state.GetContainerRequestedCores(allocationInfo)
		poolsQuantityMap[poolName] += reqInt
	}

	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, allocationInfos)
	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState)
	if err != nil {
		return fmt.Errorf("adjustPoolsAndIsolatedEntries failed with error: %v", err)
	}

	return nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint
func (p *DynamicPolicy) adjustAllocationEntries() error {
	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// since adjustAllocationEntries will cause re-generate pools,
	// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates,
	// else we do sum(containers req) for each pool to get pools ratio
	var poolsQuantityMap map[string]int
	if p.enableCPUSysAdvisor {
		poolsQuantityMap = machine.GetQuantityMap(entries.GetCPUSetMapForPools(state.ResidentPools))
	} else {
		poolsQuantityMap = state.GetSharedQuantityMapFromPodEntries(entries, nil)
	}
	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, nil)

	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState)
	if err != nil {
		return fmt.Errorf("adjustPoolsAndIsolatedEntries failed with error: %v", err)
	}

	return nil
}

// adjustPoolsAndIsolatedEntries works for the following steps
// 1. calculate pools and isolated cpusets according to expectant quantities
// 2. make reclaimed overlap with numa-binding
// 3. apply them to local state
// 4. clean pools
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntries(poolsQuantityMap map[string]int,
	isolatedQuantityMap map[string]map[string]int, entries state.PodEntries, machineState state.NUMANodeMap) error {
	availableCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, state.CheckNumaBinding)

	poolsCPUSet, isolatedCPUSet, err := p.generatePoolsAndIsolation(poolsQuantityMap, isolatedQuantityMap, availableCPUs)
	if err != nil {
		return fmt.Errorf("generatePoolsAndIsolation failed with error: %v", err)
	}

	err = p.reclaimOverlapNUMABinding(poolsCPUSet, entries)
	if err != nil {
		return fmt.Errorf("reclaimOverlapNUMABinding failed with error: %v", err)
	}

	err = p.applyPoolsAndIsolatedInfo(poolsCPUSet, isolatedCPUSet, entries, machineState)
	if err != nil {
		return fmt.Errorf("applyPoolsAndIsolatedInfo failed with error: %v", err)
	}

	err = p.cleanPools()
	if err != nil {
		return fmt.Errorf("cleanPools failed with error: %v", err)
	}

	return nil
}

// reclaimOverlapNUMABinding unions intersection of current reclaim pool and non-ramp-up dedicated_cores numa_binding containers
func (p *DynamicPolicy) reclaimOverlapNUMABinding(poolsCPUSet map[string]machine.CPUSet, entries state.PodEntries) error {
	// reclaimOverlapNUMABinding only works with cpu advisor and reclaim enabled
	if !(p.enableCPUSysAdvisor && p.reclaimedResourceConfig.EnableReclaim()) {
		return nil
	}

	if entries.CheckPoolEmpty(state.PoolNameReclaim) {
		return fmt.Errorf("reclaim pool misses in current entries")
	}

	curReclaimCPUSet := entries[state.PoolNameReclaim][advisorapi.FakedContainerID].AllocationResult.Clone()
	nonOverlapReclaimCPUSet := poolsCPUSet[state.PoolNameReclaim].Clone()
	general.Infof("curReclaimCPUSet: %s", curReclaimCPUSet.String())

	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {
			if !(allocationInfo != nil && state.CheckNumaBinding(allocationInfo) && allocationInfo.CheckMainContainer()) {
				continue
			} else if allocationInfo.RampUp {
				general.Infof("dedicated numa_binding pod: %s/%s container: %s is in ramp up, not to overlap reclaim pool with it",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				continue
			}

			poolsCPUSet[state.PoolNameReclaim] = poolsCPUSet[state.PoolNameReclaim].Union(curReclaimCPUSet.Intersection(allocationInfo.AllocationResult))
		}
	}

	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("reclaim pool is empty after overlapping with dedicated_cores numa_binding containers")
	}

	general.Infof("nonOverlapReclaimCPUSet: %s, finalReclaimCPUSet: %s", nonOverlapReclaimCPUSet.String(), poolsCPUSet[state.PoolNameReclaim].String())
	return nil
}

// applyPoolsAndIsolatedInfo generates the latest checkpoint by pools and isolated cpusets calculation results.
// 1. construct entries for dedicated containers
// 2. construct entries for all pools
// 3. construct entries for shared and reclaimed containers
// todo this function is almost the same as applyBlocks, can they merge together?
func (p *DynamicPolicy) applyPoolsAndIsolatedInfo(poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, curEntries state.PodEntries, machineState state.NUMANodeMap) error {
	newPodEntries := make(state.PodEntries)
	unionDedicatedIsolatedCPUSet := machine.NewCPUSet()

	// walk through all isolated CPUSet map to store those pods/containers in pod entries
	for podUID, containerEntries := range isolatedCPUSet {
		for containerName, isolatedCPUs := range containerEntries {
			allocationInfo := curEntries[podUID][containerName]
			if allocationInfo == nil {
				general.Errorf("isolated pod: %s, container: %s without entry in current checkpoint", podUID, containerName)
				continue
			} else if !state.CheckNumaBinding(allocationInfo) {
				general.Errorf("isolated pod: %s, container: %s isn't dedicated_cores without NUMA binding", podUID, containerName)
				continue
			}

			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, isolatedCPUs)
			if err != nil {
				general.ErrorS(err, "Unable to calculate topologyAwareAssignments",
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName,
					"result cpuset", isolatedCPUs.String())
				continue
			}

			general.InfoS("isolate info",
				"podNamespace", allocationInfo.PodNamespace,
				"podName", allocationInfo.PodName,
				"containerName", allocationInfo.ContainerName,
				"result cpuset", isolatedCPUs.String(),
				"result cpuset size", isolatedCPUs.Size(),
				"qosLevel", allocationInfo.QoSLevel)

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			newPodEntries[podUID][containerName].OwnerPoolName = state.PoolNameDedicated
			newPodEntries[podUID][containerName].AllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].OriginalAllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
			newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

			unionDedicatedIsolatedCPUSet = unionDedicatedIsolatedCPUSet.Union(isolatedCPUs)
		}
	}

	_ = p.emitter.StoreInt64(util.MetricNameIsolatedPodNum, int64(len(newPodEntries)), metrics.MetricTypeNameRaw)
	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("entry: %s is empty", state.PoolNameShare)
	}

	// walk through all pools CPUSet map to store those pools in pod entries
	for poolName, cset := range poolsCPUSet {
		general.Infof("try to apply pool %s: %s", poolName, cset.String())

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, cset)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
				poolName, cset.String(), err)
		}

		allocationInfo := curEntries[poolName][advisorapi.FakedContainerID]
		if allocationInfo != nil {
			general.Infof("pool: %s allocation result transform from %s to %s",
				poolName, allocationInfo.AllocationResult.String(), cset.String())
		}

		if newPodEntries[poolName] == nil {
			newPodEntries[poolName] = make(state.ContainerEntries)
		}
		newPodEntries[poolName][advisorapi.FakedContainerID] = &state.AllocationInfo{
			PodUid:                           poolName,
			OwnerPoolName:                    poolName,
			AllocationResult:                 cset.Clone(),
			OriginalAllocationResult:         cset.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}

		_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(cset.Size()), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "poolName", Val: poolName})
	}

	// rampUpCPUs includes common reclaimed pool
	rampUpCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, state.CheckNumaBinding).Difference(unionDedicatedIsolatedCPUSet)
	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// walk through current pod entries to handle container-related entries (besides pooled entries)
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			}

			reqInt := state.GetContainerRequestedCores(allocationInfo)
			if newPodEntries[podUID][containerName] != nil {
				// adapt to old checkpoint without RequestQuantity property
				newPodEntries[podUID][containerName].RequestQuantity = reqInt
				general.Infof("pod: %s/%s, container: %s, qosLevel: %s is isolated, ignore original allocationInfo",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
				continue
			}

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			switch allocationInfo.QoSLevel {
			case apiconsts.PodAnnotationQoSLevelDedicatedCores:
				// todo: currently for numa_binding containers, we just clone checkpoint already exist
				//  if qos aware will adjust cpuset for them, we will make adaption here
				if state.CheckNumaBinding(allocationInfo) {
					continue containerLoop
				}

				// dedicated_cores with numa_binding is not isolated, we will try to isolate it in next adjustment.
				general.Warningf("pod: %s/%s, container: %s isa dedicated_cores with numa_binding but not isolated, "+
					"we put it into fallback pool: %s temporary",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, rampUpCPUs.String())

				newPodEntries[podUID][containerName].OwnerPoolName = state.PoolNameFallback
				newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)

			case apiconsts.PodAnnotationQoSLevelSharedCores, apiconsts.PodAnnotationQoSLevelReclaimedCores:
				ownerPoolName := allocationInfo.GetPoolName()

				if allocationInfo.RampUp {
					general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					newPodEntries[podUID][containerName].OwnerPoolName = advisorapi.EmptyOwnerPoolName
					newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				} else if newPodEntries[ownerPoolName][advisorapi.FakedContainerID] == nil {
					general.Warningf("pod: %s/%s container: %s get owner pool: %s allocationInfo failed. reuse its allocation result: %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName, allocationInfo.AllocationResult.String())
				} else {
					poolEntry := newPodEntries[ownerPoolName][advisorapi.FakedContainerID]
					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newPodEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newPodEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	machineState, err = state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, newPodEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}
	p.state.SetPodEntries(newPodEntries)
	p.state.SetMachineState(machineState)

	return nil
}

// generatePoolsAndIsolation is used to generate cpuset pools and isolated cpuset
// 1. allocate isolated cpuset for pod/containers, and divide total cores evenly if not possible to allocate
// 2. use the left cores to allocate among different pools
// 3. apportion to other pools if reclaimed is disabled
func (p *DynamicPolicy) generatePoolsAndIsolation(poolsQuantityMap map[string]int,
	isolatedQuantityMap map[string]map[string]int, availableCPUs machine.CPUSet) (poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, err error) {
	// clear pool map with zero quantity
	for poolName, quantity := range poolsQuantityMap {
		if quantity == 0 {
			general.Warningf("pool: %s with 0 quantity, skip generate", poolName)
			delete(poolsQuantityMap, poolName)
		}
	}

	// clear isolated map with zero quantity
	for podUID, containerEntries := range isolatedQuantityMap {
		for containerName, quantity := range containerEntries {
			if quantity == 0 {
				general.Warningf("isolated pod: %s, container: %s with 0 quantity, skip generate it", podUID, containerName)
				delete(containerEntries, containerName)
			}
		}
		if len(containerEntries) == 0 {
			general.Warningf(" isolated pod: %s all container entries skipped", podUID)
			delete(isolatedQuantityMap, podUID)
		}
	}

	availableSize := availableCPUs.Size()

	poolsCPUSet = make(map[string]machine.CPUSet)
	poolsTotalQuantity := general.SumUpMapValues(poolsQuantityMap)

	isolatedCPUSet = make(map[string]map[string]machine.CPUSet)
	isolatedTotalQuantity := general.SumUpMultipleMapValues(isolatedQuantityMap)

	general.Infof("isolatedTotalQuantity: %d, poolsTotalQuantity: %d, availableSize: %d",
		isolatedTotalQuantity, poolsTotalQuantity, availableSize)

	var tErr error
	if poolsTotalQuantity+isolatedTotalQuantity <= availableSize {
		general.Infof("all pools and isolated containers could be allocated")

		isolatedCPUSet, availableCPUs, tErr = p.takeCPUsForContainers(isolatedQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate isolated cpus for dedicated_cores failed with error: %v", tErr)
			return
		}

		poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
			return
		}
	} else if poolsTotalQuantity <= availableSize {
		general.Infof("all pools could be allocated, all isolated containers would be put to pools")

		poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
			return
		}
	} else if poolsTotalQuantity > 0 {
		general.Infof("can't allocate for all pools")

		totalProportionalPoolsQuantity := 0
		proportionalPoolsQuantityMap := make(map[string]int)

		for poolName, poolQuantity := range poolsQuantityMap {
			proportionalSize := general.Max(getProportionalSize(poolQuantity, poolsTotalQuantity, availableSize), 1)
			proportionalPoolsQuantityMap[poolName] = proportionalSize
			totalProportionalPoolsQuantity += proportionalSize
		}

		// corner case: after divide, the total count goes to be bigger than available total
		for totalProportionalPoolsQuantity > availableSize {
			curTotalProportionalPoolsQuantity := totalProportionalPoolsQuantity

			for poolName, quantity := range proportionalPoolsQuantityMap {
				if quantity > 1 && totalProportionalPoolsQuantity > 0 {
					quantity--
					totalProportionalPoolsQuantity--
					proportionalPoolsQuantityMap[poolName] = quantity
				}
			}

			// availableSize can't satisfy every pool has at least one cpu
			if curTotalProportionalPoolsQuantity == totalProportionalPoolsQuantity {
				break
			}
		}

		general.Infof("poolsQuantityMap: %v, proportionalPoolsQuantityMap: %v", poolsQuantityMap, proportionalPoolsQuantityMap)

		// availableSize can't satisfy every pool has at least one cpu,
		// we make all pools equals to availableCPUs in this case.
		if totalProportionalPoolsQuantity > availableSize {
			for poolName := range poolsQuantityMap {
				poolsCPUSet[poolName] = availableCPUs.Clone()
			}
		} else {
			poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(proportionalPoolsQuantityMap, availableCPUs)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		}
	}

	if poolsCPUSet[state.PoolNameReserve].IsEmpty() {
		poolsCPUSet[state.PoolNameReserve] = p.reservedCPUs.Clone()
		general.Infof("set pool %s:%s", state.PoolNameReserve, poolsCPUSet[state.PoolNameReserve].String())
	} else {
		err = fmt.Errorf("static pool %s result: %s is generated dynamically", state.PoolNameReserve, poolsCPUSet[state.PoolNameReserve].String())
		return
	}

	poolsCPUSet[state.PoolNameReclaim] = poolsCPUSet[state.PoolNameReclaim].Union(availableCPUs)
	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		// for reclaimed pool, we must make them exist when the node isn't in hybrid mode even if cause overlap
		allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
		reclaimedCPUSet, _, tErr := calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
		if tErr != nil {
			err = fmt.Errorf("fallback takeByNUMABalance faild in generatePoolsAndIsolation for reclaimedCPUSet with error: %v", tErr)
			return
		}

		general.Infof("fallback takeByNUMABalance in generatePoolsAndIsolation for reclaimedCPUSet: %s", reclaimedCPUSet.String())
		poolsCPUSet[state.PoolNameReclaim] = reclaimedCPUSet
	}

	enableReclaim := p.reclaimedResourceConfig.EnableReclaim()
	if !enableReclaim && poolsCPUSet[state.PoolNameReclaim].Size() > reservedReclaimedCPUsSize {
		poolsCPUSet[state.PoolNameReclaim] = p.apportionReclaimedPool(poolsCPUSet, poolsCPUSet[state.PoolNameReclaim].Clone())
		general.Infof("apportionReclaimedPool finished, current %s pool: %s",
			state.PoolNameReclaim, poolsCPUSet[state.PoolNameReclaim].String())
	}

	return
}

// apportionReclaimedPool tries to allocate reclaimed cores to none-reclaimed pools.
// if we disable reclaim on current node, this could be used a down-grade strategy
// to disable reclaimed workloads in emergency
func (p *DynamicPolicy) apportionReclaimedPool(poolsCPUSet map[string]machine.CPUSet, reclaimedCPUs machine.CPUSet) machine.CPUSet {
	totalSize := 0
	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		}
		totalSize += poolCPUs.Size()
	}

	availableSize := reclaimedCPUs.Size() - reservedReclaimedCPUsSize
	if availableSize <= 0 || totalSize == 0 {
		return reclaimedCPUs
	}

	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		}
		proportionalSize := general.Max(getProportionalSize(poolCPUs.Size(), totalSize, availableSize), 1)

		var err error
		var cpuset machine.CPUSet
		cpuset, reclaimedCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, reclaimedCPUs, proportionalSize)
		if err != nil {
			general.Errorf("take %d cpus from reclaimedCPUs: %s, size: %d failed with error: %v",
				proportionalSize, reclaimedCPUs.String(), reclaimedCPUs.Size(), err)
			return reclaimedCPUs
		}

		poolsCPUSet[poolName] = poolCPUs.Union(cpuset)
		general.Infof("take %s to %s; prev: %s, current: %s", cpuset.String(), poolName, poolCPUs.String(), poolsCPUSet[poolName].String())

		if reclaimedCPUs.Size() <= reservedReclaimedCPUsSize {
			break
		}
	}

	return reclaimedCPUs
}

// takeCPUsForPools tries to allocate cpuset for each given pool,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset pool map and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForPools(poolsQuantityMap map[string]int,
	availableCPUs machine.CPUSet) (map[string]machine.CPUSet, machine.CPUSet, error) {
	poolsCPUSet := make(map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	// to avoid random map iteration sequence to generate pools randomly
	sortedPoolNames := general.GetSortedMapKeys(poolsQuantityMap)
	for _, poolName := range sortedPoolNames {
		req := poolsQuantityMap[poolName]
		general.Infof("allocated for pool: %s with req: %d", poolName, req)

		var err error
		var cset machine.CPUSet
		cset, availableCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, availableCPUs, req)
		if err != nil {
			return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pool: %s of req: %d failed with error: %v",
				poolName, req, err)
		}
		poolsCPUSet[poolName] = cset
	}
	return poolsCPUSet, availableCPUs, nil
}

// takeCPUsForContainers tries to allocate cpuset for the given pod/container combinations,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset map for pod/container combinations and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForContainers(containersQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet) (map[string]map[string]machine.CPUSet, machine.CPUSet, error) {
	containersCPUSet := make(map[string]map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	for podUID, containerQuantities := range containersQuantityMap {
		if len(containerQuantities) > 0 {
			containersCPUSet[podUID] = make(map[string]machine.CPUSet)
		}

		for containerName, quantity := range containerQuantities {
			general.Infof("allocated for pod: %s container: %s with req: %d", podUID, containerName, quantity)

			var err error
			var cset machine.CPUSet
			cset, availableCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, availableCPUs, quantity)
			if err != nil {
				return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pod: %s container: %s of req: %d failed with error: %v",
					podUID, containerName, quantity, err)
			}
			containersCPUSet[podUID][containerName] = cset
		}
	}
	return containersCPUSet, availableCPUs, nil
}

// rePackAllocations regenerates allocations for container that'd already been allocated cpu,
// and rePackAllocations will assemble allocations based on already-existed AllocationInfo,
// without any calculation logics at all
func rePackAllocations(allocationInfo *state.AllocationInfo, resourceName, ociPropertyName string,
	isNodeResource, isScalarResource bool, req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("rePackAllocations got nil allocationInfo")
	} else if req == nil {
		return nil, fmt.Errorf("rePackAllocations got nil request")
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

func getProportionalSize(oldPoolSize, oldTotalSize, newTotalSize int) int {
	return int(float64(newTotalSize) * (float64(oldPoolSize) / float64(oldTotalSize)))
}
