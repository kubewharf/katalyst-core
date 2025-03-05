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
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

func (p *DynamicPolicy) sharedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest,
	persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil req")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.sharedCoresWithNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	default:
		return p.sharedCoresWithoutNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	}
}

func (p *DynamicPolicy) sharedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("sharedCoresAllocationHandler got nil request")
	}

	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	machineState := p.state.GetMachineState()
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated),
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))
	// cores that are forbidden from user binding need to be deducted from the pool.
	forbiddenCPUs, err := state.GetUnitedPoolsCPUs(state.ForbiddenPools, p.state.GetPodEntries())
	if err != nil {
		return nil, fmt.Errorf("getForbiddenCPUs failed with error: %v", err)
	}

	pooledCPUs.Difference(forbiddenCPUs)

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
	originAllocationInfo := allocationInfo.Clone()
	err = updateAllocationInfoByReq(req, allocationInfo)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s updateAllocationInfoByReq failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("updateAllocationInfoByReq failed with error: %v", err)
	}

	if allocationInfo == nil {
		general.Infof("pod: %s/%s, container: %s is met firstly, do ramp up with pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		shouldRampUp := p.shouldSharedCoresRampUp(req.PodUid)

		allocationInfo = &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
				commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
			RampUp:                           shouldRampUp,
			AllocationResult:                 pooledCPUs,
			OriginalAllocationResult:         pooledCPUs.Clone(),
			TopologyAwareAssignments:         pooledCPUsTopologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments),
			InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
			RequestQuantity:                  reqFloat64,
		}

		if !shouldRampUp {
			targetPoolName := allocationInfo.GetSpecifiedPoolName()
			poolAllocationInfo := p.state.GetAllocationInfo(targetPoolName, commonstate.FakedContainerName)

			if poolAllocationInfo == nil {
				general.Infof("pod: %s/%s, container: %s is active, but its specified pool entry doesn't exist, try to ramp up it",
					req.PodNamespace, req.PodName, req.ContainerName)
				allocationInfo.RampUp = true
			} else {
				p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
				_, err = p.doAndCheckPutAllocationInfo(allocationInfo, false, persistCheckpoint)
				if err != nil {
					return nil, err
				}

				needSet = false
			}
		}
	} else if allocationInfo.RampUp {
		if util.PodInplaceUpdateResizing(req) {
			general.Errorf("pod: %s/%s, container: %s is still in ramp up, not allow to inplace update resize",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("pod is still ramp up, not allow to inplace update resize")
		}

		general.Infof("pod: %s/%s, container: %s is still in ramp up, allocate pooled cpus: %s",
			req.PodNamespace, req.PodName, req.ContainerName, pooledCPUs.String())

		allocationInfo.AllocationResult = pooledCPUs
		allocationInfo.OriginalAllocationResult = pooledCPUs.Clone()
		allocationInfo.TopologyAwareAssignments = pooledCPUsTopologyAwareAssignments
		allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)
	} else {
		if util.PodInplaceUpdateResizing(req) {
			general.Infof("pod: %s/%s, container: %s request to inplace update resize (%.02f->%.02f)",
				req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.RequestQuantity, reqFloat64)
			allocationInfo.RequestQuantity = reqFloat64

			p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
			_, err := p.doAndCheckPutAllocationInfoPodResizingAware(originAllocationInfo, allocationInfo, false, true, persistCheckpoint)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s doAndCheckPutAllocationInfoPodResizingAware failed: %q",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				p.state.SetAllocationInfo(originAllocationInfo.PodUid, originAllocationInfo.ContainerName, originAllocationInfo, persistCheckpoint)
				return nil, err
			}
		} else {
			_, err := p.doAndCheckPutAllocationInfo(allocationInfo, true, persistCheckpoint)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s doAndCheckPutAllocationInfo failed: %q",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, err
			}
		}
		needSet = false
	}

	if needSet {
		// update pod entries directly.
		// if one of subsequent steps is failed,
		// we will delete current allocationInfo from podEntries in defer function of allocation function.
		p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
		podEntries := p.state.GetPodEntries()

		updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
		p.state.SetMachineState(updatedMachineState, persistCheckpoint)
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) reclaimedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("reclaimedCoresAllocationHandler got nil request")
	}

	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelReclaimedCores, persistCheckpoint)
	}

	if util.PodInplaceUpdateResizing(req) {
		return nil, fmt.Errorf("not support inplace update resize for reclaimed cores")
	}

	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	err = updateAllocationInfoByReq(req, allocationInfo)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s updateAllocationInfoByReq failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("updateAllocationInfoByReq failed with error: %v", err)
	}

	reclaimedAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	if reclaimedAllocationInfo == nil {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is not ready",
			req.PodNamespace, req.PodName, req.ContainerName, commonstate.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not ready", commonstate.PoolNameReclaim)
	} else if reclaimedAllocationInfo.AllocationResult.Size() == 0 {
		general.Errorf("allocation for pod: %s/%s, container: %s is failed, because pool: %s is empty",
			req.PodNamespace, req.PodName, req.ContainerName, commonstate.PoolNameReclaim)

		return nil, fmt.Errorf("pool: %s is not empty", commonstate.PoolNameReclaim)
	}

	machineState := p.state.GetMachineState()
	// calculate NUMAs without actual numa_binding reclaimed pods
	nonReclaimActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))

	if allocationInfo != nil {
		general.Infof("pod: %s/%s, container: %s with old allocation result: %s, allocate by reclaimedCPUSet: %s",
			req.PodNamespace, req.PodName, req.ContainerName, allocationInfo.AllocationResult.String(), reclaimedAllocationInfo.AllocationResult.String())
	} else {
		general.Infof("pod: %s/%s, container: %s is firstly met, allocate by reclaimedCPUSet: %s",
			req.PodNamespace, req.PodName, req.ContainerName, reclaimedAllocationInfo.AllocationResult.String())

		allocationInfo = &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
				commonstate.PoolNameReclaim, apiconsts.PodAnnotationQoSLevelReclaimedCores),
			InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
			RequestQuantity: reqFloat64,
		}

		// calculate NUMAs without non-actual numa_binding reclaimed pods
		reclaimActualBindingNUMAs := machineState.GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding))
		// set reclaimed numa_binding NUMA ID to allocationInfo
		if req.Hint != nil && len(req.Hint.Nodes) == 1 && (reclaimActualBindingNUMAs.Contains(int(req.Hint.Nodes[0])) ||
			!nonReclaimActualBindingNUMAs.Equals(machine.NewCPUSet(int(req.Hint.Nodes[0])))) {
			allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes[0])
		}
	}

	// update reclaimed allocation result by pool entry
	err = p.updateReclaimAllocationResultByPoolEntry(allocationInfo, reclaimedAllocationInfo, nonReclaimActualBindingNUMAs)
	if err != nil {
		return nil, err
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)

	// update reclaim non-actual numa_binding reclaim cores allocations if it needs to transfer a non-RNB numa to RNB numa
	podEntries := p.state.GetPodEntries()
	if allocationInfo.CheckActualNUMABinding() &&
		nonReclaimActualBindingNUMAs.Intersection(allocationInfo.AllocationResult).Size() > 0 {
		updatedNonReclaimActualBindingNUMAs := nonReclaimActualBindingNUMAs.Difference(allocationInfo.AllocationResult)
		err := p.updateNonActualNUMABindingReclaimCoresAllocations(podEntries, updatedNonReclaimActualBindingNUMAs, reclaimedAllocationInfo)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s updateNonActualNUMABindingReclaimCoresAllocations failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, err
		}
	}

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState, persistCheckpoint)

	return resp, nil
}

// updateReclaimAllocationResultByPoolEntry updates non-actual numa binding reclaimed allocation result by pool entry
func (p *DynamicPolicy) updateNonActualNUMABindingReclaimCoresAllocations(podEntries state.PodEntries,
	nonReclaimActualBindingNUMAs machine.CPUSet, poolEntry *state.AllocationInfo,
) error {
	nonActualNUMABindingAllocations := podEntries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedNonActualNUMABinding))
	for _, containerEntries := range nonActualNUMABindingAllocations {
		for _, allocationInfo := range containerEntries {
			err := p.updateReclaimAllocationResultByPoolEntry(allocationInfo, poolEntry, nonReclaimActualBindingNUMAs)
			if err != nil {
				return fmt.Errorf("updateReclaimAllocationResultByPoolEntry with error: %v", err)
			}
		}
	}
	return nil
}

func (p *DynamicPolicy) dedicatedCoresAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	if util.PodInplaceUpdateResizing(req) {
		return nil, fmt.Errorf("not support inplace update resize for dedicated cores")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.dedicatedCoresWithNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	default:
		return p.dedicatedCoresWithoutNUMABindingAllocationHandler(ctx, req, persistCheckpoint)
	}
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingAllocationHandler(_ context.Context,
	_ *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelDedicatedCores, persistCheckpoint)
	}

	var machineState state.NUMANodeMap
	oldAllocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if oldAllocationInfo == nil {
		machineState = p.state.GetMachineState()
	} else {
		p.state.Delete(req.PodUid, req.ContainerName, persistCheckpoint)
		podEntries := p.state.GetPodEntries()

		var err error
		machineState, err = generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	result, err := p.allocateNumaBindingCPUs(reqInt, req.Hint, machineState, req.Annotations)
	if err != nil {
		general.ErrorS(err, "unable to allocate CPUs",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUsInt", reqInt,
			"numCPUsFloat64", reqFloat64)
		return nil, err
	}

	// avoid running services on forbidden CPUs.
	forbiddenCPUs, err := state.GetUnitedPoolsCPUs(state.ForbiddenPools, p.state.GetPodEntries())
	if err != nil {
		return nil, fmt.Errorf("getForbiddenCPUs failed with error: %v", err)
	}
	result = result.Difference(forbiddenCPUs)

	general.InfoS("allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"result", result.String())

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, result)
	if err != nil {
		general.ErrorS(err, "unable to calculate topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUsInt", reqInt,
			"numCPUsFloat64", reqFloat64,
			"result cpuset", result.String())
		return nil, err
	}

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.PoolNameDedicated, apiconsts.PodAnnotationQoSLevelDedicatedCores),
		RampUp:                           true,
		AllocationResult:                 result.Clone(),
		OriginalAllocationResult:         result.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		RequestQuantity:                  reqFloat64,
	}

	if !qosutil.AnnotationsIndicateNUMAExclusive(req.Annotations) {
		if len(req.Hint.Nodes) != 1 {
			return nil, fmt.Errorf("numa binding without numa exclusive allocation result numa node size is %d, "+
				"not equal to 1", len(req.Hint.Nodes))
		}
		allocationInfo.SetSpecifiedNUMABindingNUMAID(req.Hint.Nodes[0])
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState, persistCheckpoint)

	err = p.adjustAllocationEntries(persistCheckpoint)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s putContainersAndAdjustAllocationEntriesWithoutAllocation failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("adjustAllocationEntries failed with error: %v", err)
	}

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

// allocationSidecarHandler currently we set cpuset of sidecar to the cpuset of its main container
func (p *DynamicPolicy) allocationSidecarHandler(_ context.Context,
	req *pluginapi.ResourceRequest, qosLevel string, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	_, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	if podEntries[req.PodUid] == nil {
		general.Infof("there is no pod entry, pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	mainContainerAllocationInfo := podEntries[req.PodUid].GetMainContainerEntry()

	// todo: consider sidecar without reconcile in vpa
	if mainContainerAllocationInfo == nil {
		general.Infof("main container is not found for pod: %s/%s, sidecar: %s, waiting next reconcile",
			req.PodNamespace, req.PodName, req.ContainerName)
		return &pluginapi.ResourceAllocationResponse{}, nil
	}

	// the sidecar container also support inplace update resize, update the allocation and machine state here
	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
		RequestQuantity: reqFloat64,
	}
	p.applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo)

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
	podEntries = p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState, persistCheckpoint)

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) sharedCoresWithNUMABindingAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest, persistCheckpoint bool,
) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSharedCores, persistCheckpoint)
	}

	// there is no need to delete old allocationInfo for the container if it exists,
	// allocateSharedNumaBindingCPUs will re-calculate pool size and avoid counting same entry twice
	allocationInfo, err := p.allocateSharedNumaBindingCPUs(req, req.Hint, persistCheckpoint)
	if err != nil || allocationInfo == nil {
		general.ErrorS(err, "unable to allocate CPUs",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nil, err
	}

	general.InfoS("allocate CPUs successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"result", allocationInfo.AllocationResult.String())

	// there is no need to call SetPodEntries and SetMachineState,
	// since they are already done in doAndCheckPutAllocationInfo of allocateSharedNumaBindingCPUs

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) allocateNumaBindingCPUs(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string,
) (machine.CPUSet, error) {
	if hint == nil {
		return machine.NewCPUSet(), fmt.Errorf("hint is nil")
	} else if len(hint.Nodes) == 0 {
		return machine.NewCPUSet(), fmt.Errorf("hint is empty")
	} else if qosutil.AnnotationsIndicateNUMABinding(reqAnnotations) &&
		!qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations) &&
		len(hint.Nodes) > 1 {
		return machine.NewCPUSet(), fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	result := machine.NewCPUSet()
	alignedAvailableCPUs := machine.CPUSet{}
	for _, numaNode := range hint.Nodes {
		alignedAvailableCPUs = alignedAvailableCPUs.Union(machineState[int(numaNode)].GetAvailableCPUSet(p.reservedCPUs))
	}

	var alignedCPUs machine.CPUSet

	if qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations) {
		// todo: currently we hack dedicated_cores with NUMA binding take up whole NUMA,
		//  and we will modify strategy here if assumption above breaks.
		alignedCPUs = alignedAvailableCPUs.Clone()
	} else {
		var err error
		alignedCPUs, err = calculator.TakeByTopology(p.machineInfo, alignedAvailableCPUs, numCPUs)
		if err != nil {
			general.ErrorS(err, "take cpu for NUMA not exclusive binding container failed",
				"hints", hint.Nodes,
				"alignedAvailableCPUs", alignedAvailableCPUs.String())

			return machine.NewCPUSet(),
				fmt.Errorf("take cpu for NUMA not exclusive binding container failed with err: %v", err)
		}
	}

	general.InfoS("allocate by hints",
		"hints", hint.Nodes,
		"alignedAvailableCPUs", alignedAvailableCPUs.String(),
		"alignedAllocatedCPUs", alignedCPUs)

	// currently, result equals to alignedCPUs,
	// maybe extend cpus not aligned to meet requirement later
	result = result.Union(alignedCPUs)
	leftNumCPUs := numCPUs - result.Size()
	if leftNumCPUs > 0 {
		general.Errorf("result cpus: %s in hint NUMA nodes: %d with size: %d can't meet cpus request: %d",
			result.String(), hint.Nodes, result.Size(), numCPUs)

		return machine.NewCPUSet(), fmt.Errorf("results can't meet cpus request")
	}
	return result, nil
}

func (p *DynamicPolicy) allocateSharedNumaBindingCPUs(req *pluginapi.ResourceRequest,
	hint *pluginapi.TopologyHint, persistCheckpoint bool,
) (*state.AllocationInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req")
	} else if hint == nil {
		return nil, fmt.Errorf("hint is nil")
	} else if len(hint.Nodes) == 0 {
		return nil, fmt.Errorf("hint is empty")
	} else if len(hint.Nodes) > 1 {
		return nil, fmt.Errorf("shared_cores with numa_binding container has request larger than 1 NUMA")
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("allocateSharedNumaBindingCPUs by hints",
		"hints", hint.Nodes,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64)

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			// it will be put to correct pool in doAndCheckPutAllocationInfo
			commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		InitTimestamp:   time.Now().Format(util.QRMTimeFormat),
		RequestQuantity: reqFloat64,
	}
	allocationInfo.SetSpecifiedNUMABindingNUMAID(hint.Nodes[0])

	if util.PodInplaceUpdateResizing(req) {
		originAllocationInfo := p.state.GetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName)
		if originAllocationInfo == nil {
			general.Errorf("pod: %s/%s, container: %s request to cpu inplace update resize alloation, but no origin allocation info, reject it",
				req.PodNamespace, req.PodName, req.ContainerName)
			return nil, fmt.Errorf("no origion cpu allocation info for inplace update resize")
		}

		general.Infof("pod: %s/%s, container: %s request to cpu inplace update resize allocation (%.02f->%.02f)",
			req.PodNamespace, req.PodName, req.ContainerName, originAllocationInfo.RequestQuantity, allocationInfo.RequestQuantity)
		p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
		checkedAllocationInfo, err := p.doAndCheckPutAllocationInfoPodResizingAware(originAllocationInfo, allocationInfo, false, true, persistCheckpoint)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s request to cpu inplace update resize allocation, but doAndCheckPutAllocationInfoPodResizingAware failed: %q",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			p.state.SetAllocationInfo(originAllocationInfo.PodUid, originAllocationInfo.ContainerName, originAllocationInfo, persistCheckpoint)
			return nil, fmt.Errorf("doAndCheckPutAllocationInfo failed with error: %v", err)
		}
		return checkedAllocationInfo, nil
	} else {
		p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
		checkedAllocationInfo, err := p.doAndCheckPutAllocationInfo(allocationInfo, true, persistCheckpoint)
		if err != nil {
			return nil, fmt.Errorf("doAndCheckPutAllocationInfo failed with error: %v", err)
		}
		return checkedAllocationInfo, nil
	}
}

// putAllocationsAndAdjustAllocationEntries calculates and generates the latest checkpoint
// - unlike adjustAllocationEntries, it will also consider AllocationInfo
func (p *DynamicPolicy) putAllocationsAndAdjustAllocationEntries(allocationInfos []*state.AllocationInfo, incrByReq bool, persistCheckpoint bool) error {
	return p.putAllocationsAndAdjustAllocationEntriesResizeAware(nil, allocationInfos, incrByReq, false, persistCheckpoint)
}

func (p *DynamicPolicy) putAllocationsAndAdjustAllocationEntriesResizeAware(originAllocationInfos, allocationInfos []*state.AllocationInfo, incrByReq, podInplaceUpdateResizing, persistCheckpoint bool) error {
	if len(allocationInfos) == 0 {
		return nil
	}
	if podInplaceUpdateResizing {
		if len(originAllocationInfos) != 1 && len(allocationInfos) != 1 {
			general.Errorf("cannot adjust allocation entries for invalid allocation infos")
			return fmt.Errorf("invalid inplace update resize allocation infos length")
		}
	}

	entries := p.state.GetPodEntries()

	for _, allocationInfo := range allocationInfos {
		if allocationInfo == nil {
			return fmt.Errorf("found nil allocationInfo in input parameter")
		} else if !allocationInfo.CheckShared() {
			return fmt.Errorf("put container with invalid qos level: %s into pool", allocationInfo.QoSLevel)
		} else if entries[allocationInfo.PodUid][allocationInfo.ContainerName] == nil {
			return fmt.Errorf("entry %s/%s, %s isn't found in state",
				allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
		}

		poolName := allocationInfo.GetSpecifiedPoolName()
		if poolName == commonstate.EmptyOwnerPoolName {
			return fmt.Errorf("allocationInfo points to empty poolName")
		}
	}

	machineState := p.state.GetMachineState()

	var poolsQuantityMap map[string]map[int]int
	if p.enableCPUAdvisor &&
		!cpuutil.AdvisorDegradation(p.advisorMonitor.GetHealthy(), p.dynamicConfig.GetDynamicConfiguration().EnableReclaim) {
		// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates
		csetMap, err := entries.GetFilteredPoolsCPUSetMap(state.ResidentPools)
		if err != nil {
			return fmt.Errorf("GetFilteredPoolsCPUSetMap failed with error: %v", err)
		}

		poolsQuantityMap = machine.ParseCPUAssignmentQuantityMap(csetMap)
		if podInplaceUpdateResizing {
			// adjust pool resize
			originAllocationInfo := originAllocationInfos[0]
			allocationInfo := allocationInfos[0]

			poolName, targetNumaID, resizeReqFloat64, err := p.calcPoolResizeRequest(originAllocationInfo, allocationInfo, entries)
			if err != nil {
				return fmt.Errorf("calcPoolResizeRequest cannot calc pool resize request: %q", err)
			}

			// update the pool size
			poolsQuantityMap[poolName][targetNumaID] += int(math.Ceil(resizeReqFloat64))
			// return err will abort the procedure,
			// so there is no need to revert modifications made in parameter poolsQuantityMap
			if len(poolsQuantityMap[poolName]) > 1 {
				return fmt.Errorf("pool %s cross NUMA: %+v", poolName, poolsQuantityMap[poolName])
			}
		} else if incrByReq {
			err := state.CountAllocationInfosToPoolsQuantityMap(allocationInfos, poolsQuantityMap, p.getContainerRequestedCores)
			if err != nil {
				return fmt.Errorf("CountAllocationInfosToPoolsQuantityMap failed with error: %v", err)
			}
		}
	} else {
		// else we do sum(containers req) for each pool to get pools ratio
		var err error
		poolsQuantityMap, err = state.GetSharedQuantityMapFromPodEntries(entries, allocationInfos, p.getContainerRequestedCores)
		if err != nil {
			return fmt.Errorf("GetSharedQuantityMapFromPodEntries failed with error: %v", err)
		}

		if incrByReq || podInplaceUpdateResizing {
			if podInplaceUpdateResizing {
				general.Infof("pod: %s/%s, container: %s request to re-calc pool size for cpu inplace update resize",
					allocationInfos[0].PodNamespace, allocationInfos[0].PodName, allocationInfos[0].ContainerName)
			}
			// if advisor is disabled, qrm can re-calc the pool size exactly. we don't need to adjust the pool size.
			err := state.CountAllocationInfosToPoolsQuantityMap(allocationInfos, poolsQuantityMap, p.getContainerRequestedCores)
			if err != nil {
				return fmt.Errorf("CountAllocationInfosToPoolsQuantityMap failed with error: %v", err)
			}
		}
	}

	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, allocationInfos, p.getContainerRequestedCores)
	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap,
		entries, machineState, persistCheckpoint)
	if err != nil {
		return fmt.Errorf("adjustPoolsAndIsolatedEntries failed with error: %v", err)
	}

	return nil
}

func (p *DynamicPolicy) calcPoolResizeRequest(originAllocation, allocation *state.AllocationInfo, podEntries state.PodEntries) (string, int, float64, error) {
	poolName := allocation.GetPoolName()
	targetNumaID := commonstate.FakedNUMAID

	originPodAggregatedRequest, ok := originAllocation.GetPodAggregatedRequest()
	if !ok {
		containerEntries, ok := podEntries[originAllocation.PodUid]
		if !ok {
			general.Warningf("pod %s/%s container entries not exist", originAllocation.PodNamespace, originAllocation.PodName)
			originPodAggregatedRequest = 0
		} else {
			podAggregatedRequestSum := float64(0)
			for containerName, containerEntry := range containerEntries {
				if containerName == originAllocation.ContainerName {
					podAggregatedRequestSum += originAllocation.RequestQuantity
				} else {
					podAggregatedRequestSum += containerEntry.RequestQuantity
				}
			}
			originPodAggregatedRequest = podAggregatedRequestSum
		}
	}

	podAggregatedRequest, ok := allocation.GetPodAggregatedRequest()
	if !ok {
		containerEntries, ok := podEntries[originAllocation.PodUid]
		if !ok {
			general.Warningf("pod %s/%s container entries not exist", originAllocation.PodNamespace, originAllocation.PodName)
			podAggregatedRequest = 0
		} else {
			podAggregatedRequestSum := float64(0)
			for _, containerEntry := range containerEntries {
				podAggregatedRequestSum += containerEntry.RequestQuantity
			}
			podAggregatedRequest = podAggregatedRequestSum
		}
	}

	poolResizeQuantity := podAggregatedRequest - originPodAggregatedRequest
	if poolResizeQuantity < 0 {
		// We don't need to adjust pool size in inplace update scale in mode, wait advisor to adjust the pool size later.
		general.Infof("pod: %s/%s, container: %s request cpu inplace update scale in (%.02f->%.02f)",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName, originPodAggregatedRequest, podAggregatedRequest)
		poolResizeQuantity = 0
	} else {
		// We should adjust pool size in inplace update scale out mode with resizeReqFloat64, and then wait advisor to adjust the pool size later.
		general.Infof("pod: %s/%s, container: %s request cpu inplace update scale out (%.02f->%.02f)",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName, originPodAggregatedRequest, podAggregatedRequest)
	}

	// only support share cores inplace update resize now (include non-binding share cores and share cores with NUMA binding)
	if allocation.CheckSharedNUMABinding() {
		// check snb numa migrate for inplace update resize
		originTargetNumaID, err := state.GetSharedNUMABindingTargetNuma(originAllocation)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to get origin target NUMA")
		}
		targetNumaID, err = state.GetSharedNUMABindingTargetNuma(allocation)
		if err != nil {
			return "", 0, 0, fmt.Errorf("failed to get target NUMA")
		}

		// the pod is migrated to a new NUMA if the NUMA changed.
		// the new pool should scale out the whole request size.
		// the old pool would be adjusted by advisor later.
		if originTargetNumaID != targetNumaID {
			poolResizeQuantity = podAggregatedRequest
			general.Infof("pod %s/%s request inplace update resize and it was migrate to a new NUMA (%d->%d), AggregatedPodRequest(%.02f)",
				allocation.PodNamespace, allocation.PodName, originTargetNumaID, targetNumaID, podAggregatedRequest)
		}

		// get snb pool name
		poolName, err = allocation.GetSpecifiedNUMABindingPoolName()
		if err != nil {
			return "", 0, 0, fmt.Errorf("GetSpecifiedNUMABindingPoolName for %s/%s/%s failed with error: %v",
				allocation.PodNamespace, allocation.PodName, allocation.ContainerName, err)
		}
	}

	if poolName == commonstate.EmptyOwnerPoolName {
		return "", 0, 0, fmt.Errorf("get poolName failed for %s/%s/%s",
			allocation.PodNamespace, allocation.PodName, allocation.ContainerName)
	}

	return poolName, targetNumaID, poolResizeQuantity, nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint
func (p *DynamicPolicy) adjustAllocationEntries(persistCheckpoint bool) error {
	startTime := time.Now()
	general.Infof("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// since adjustAllocationEntries will cause re-generate pools,
	// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates,
	// else we do sum(containers req) for each pool to get pools ratio
	var poolsQuantityMap map[string]map[int]int
	if p.enableCPUAdvisor &&
		!cpuutil.AdvisorDegradation(p.advisorMonitor.GetHealthy(), p.dynamicConfig.GetDynamicConfiguration().EnableReclaim) {
		poolsCPUSetMap, err := entries.GetFilteredPoolsCPUSetMap(state.ResidentPools)
		if err != nil {
			return fmt.Errorf("GetFilteredPoolsCPUSetMap failed with error: %v", err)
		}
		poolsQuantityMap = machine.ParseCPUAssignmentQuantityMap(poolsCPUSetMap)
	} else {
		var err error
		poolsQuantityMap, err = state.GetSharedQuantityMapFromPodEntries(entries, nil, p.getContainerRequestedCores)
		if err != nil {
			return fmt.Errorf("GetSharedQuantityMapFromPodEntries failed with error: %v", err)
		}
	}
	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, nil, p.getContainerRequestedCores)

	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint)
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
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntries(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
) error {
	availableCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs, nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding))

	// deduct the cpus that is forbidden from being used by user containers.
	forbiddenPoolCPUs, err := state.GetUnitedPoolsCPUs(state.ForbiddenPools, entries)
	if err != nil {
		return fmt.Errorf("get forbidden united pools‘ cpus failed with error: %v", err)
	}
	availableCPUs = availableCPUs.Difference(forbiddenPoolCPUs)

	reclaimOverlapShareRatio, err := p.getReclaimOverlapShareRatio(entries)
	if err != nil {
		return fmt.Errorf("reclaimOverlapShareRatio failed with error: %v", err)
	}

	general.Infof("poolsQuantityMap: %#v, availableCPUs: %v, reclaimOverlapShareRatio: %#v", poolsQuantityMap, availableCPUs, reclaimOverlapShareRatio)

	poolsCPUSet, isolatedCPUSet, err := p.generatePoolsAndIsolation(poolsQuantityMap, isolatedQuantityMap, availableCPUs, reclaimOverlapShareRatio)
	if err != nil {
		return fmt.Errorf("generatePoolsAndIsolation failed with error: %v", err)
	}

	err = p.reclaimOverlapNUMABinding(poolsCPUSet, entries)
	if err != nil {
		return fmt.Errorf("reclaimOverlapNUMABinding failed with error: %v", err)
	}

	err = p.applyPoolsAndIsolatedInfo(poolsCPUSet, isolatedCPUSet, entries,
		machineState, state.GetSharedBindingNUMAsFromQuantityMap(poolsQuantityMap), persistCheckpoint)
	if err != nil {
		return fmt.Errorf("applyPoolsAndIsolatedInfo failed with error: %v", err)
	}

	err = p.cleanPools()
	if err != nil {
		return fmt.Errorf("cleanPools failed with error: %v", err)
	}

	return nil
}

// reclaimOverlapNUMABinding unions calculated reclaim pool in empty NUMAs
// with the intersection of previous reclaim pool and non-ramp-up dedicated_cores numa_binding containers
func (p *DynamicPolicy) reclaimOverlapNUMABinding(poolsCPUSet map[string]machine.CPUSet, entries state.PodEntries) error {
	// reclaimOverlapNUMABinding only works with cpu advisor and reclaim enabled
	if !(p.enableCPUAdvisor && p.dynamicConfig.GetDynamicConfiguration().EnableReclaim) {
		return nil
	}

	if entries.CheckPoolEmpty(commonstate.PoolNameReclaim) {
		return fmt.Errorf("reclaim pool misses in current entries")
	}

	curReclaimCPUSet := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.Clone()
	nonOverlapReclaimCPUSet := poolsCPUSet[commonstate.PoolNameReclaim].Clone()
	general.Infof("curReclaimCPUSet: %s", curReclaimCPUSet.String())

	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {
			if !(allocationInfo != nil && allocationInfo.CheckDedicatedNUMABinding() && allocationInfo.CheckMainContainer()) {
				continue
			} else if allocationInfo.RampUp {
				general.Infof("dedicated numa_binding pod: %s/%s container: %s is in ramp up, not to overlap reclaim pool with it",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				continue
			}

			poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(curReclaimCPUSet.Intersection(allocationInfo.AllocationResult))
		}
	}

	if poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("reclaim pool is empty after overlapping with dedicated_cores numa_binding containers")
	}

	general.Infof("nonOverlapReclaimCPUSet: %s, finalReclaimCPUSet: %s", nonOverlapReclaimCPUSet.String(), poolsCPUSet[commonstate.PoolNameReclaim].String())
	return nil
}

// applyPoolsAndIsolatedInfo generates the latest checkpoint by pools and isolated cpusets calculation results.
// 1. construct entries for isolated containers (probably be dedicated_cores not numa_binding )
// 2. construct entries for all pools
// 3. construct entries for shared_cores, reclaimed_cores, numa_binding dedicated_cores containers
func (p *DynamicPolicy) applyPoolsAndIsolatedInfo(poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, curEntries state.PodEntries,
	machineState state.NUMANodeMap, sharedBindingNUMAs sets.Int, persistCheckpoint bool,
) error {
	newPodEntries := make(state.PodEntries)
	unionDedicatedIsolatedCPUSet := machine.NewCPUSet()

	// calculate NUMAs without actual numa_binding reclaimed pods
	nonReclaimActualBindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
	// 1. construct entries for isolated containers (probably be dedicated_cores not numa_binding )
	for podUID, containerEntries := range isolatedCPUSet {
		for containerName, isolatedCPUs := range containerEntries {
			allocationInfo := curEntries[podUID][containerName]
			if allocationInfo == nil {
				general.Errorf("isolated pod: %s, container: %s without entry in current checkpoint", podUID, containerName)
				continue
			} else if !allocationInfo.CheckDedicated() || allocationInfo.CheckNUMABinding() {
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
			newPodEntries[podUID][containerName].OwnerPoolName = commonstate.PoolNameDedicated
			newPodEntries[podUID][containerName].AllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].OriginalAllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
			newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

			unionDedicatedIsolatedCPUSet = unionDedicatedIsolatedCPUSet.Union(isolatedCPUs)
		}
	}

	// 2. construct entries for all pools
	if poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("entry: %s is empty", commonstate.PoolNameReclaim)
	}

	for poolName, cset := range poolsCPUSet {
		general.Infof("try to apply pool %s: %s", poolName, cset.String())
		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, cset)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
				poolName, cset.String(), err)
		}

		allocationInfo := curEntries[poolName][commonstate.FakedContainerName]
		if allocationInfo != nil {
			general.Infof("pool: %s allocation result transform from %s(size: %d) to %s(size: %d)",
				poolName, allocationInfo.AllocationResult.String(), allocationInfo.AllocationResult.Size(),
				cset.String(), cset.Size())
		}

		if newPodEntries[poolName] == nil {
			newPodEntries[poolName] = make(state.ContainerEntries)
		}
		newPodEntries[poolName][commonstate.FakedContainerName] = &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(poolName),
			AllocationResult:                 cset.Clone(),
			OriginalAllocationResult:         cset.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}

		for numaID, cpus := range topologyAwareAssignments {
			_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(cpus.Size()),
				metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "poolName", Val: poolName},
				metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
		}
	}

	// revise reclaim pool size to avoid reclaimed_cores and numa_binding containers
	// in NUMAs without cpuset actual binding
	err := p.reviseReclaimPool(newPodEntries, nonReclaimActualBindingNUMAs, unionDedicatedIsolatedCPUSet)
	if err != nil {
		return err
	}

	sharedBindingNUMACPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(sharedBindingNUMAs.UnsortedList()...)
	// rampUpCPUs include reclaim pool in NUMAs without NUMA_binding cpus
	rampUpCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
		nil,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding)).
		Difference(unionDedicatedIsolatedCPUSet).
		Difference(sharedBindingNUMACPUs)
	forbiddenPoolsCPUs, err := state.GetUnitedPoolsCPUs(state.ForbiddenPools, newPodEntries)
	if err != nil {
		return fmt.Errorf("get forbidden united pools‘ cpus failed with error: %v", err)
	}
	rampUpCPUs = rampUpCPUs.Difference(forbiddenPoolsCPUs)

	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// 3. construct entries for shared_cores, reclaimed_cores, numa_binding dedicated_cores containers
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

			if newPodEntries[podUID][containerName] != nil {
				general.Infof("pod: %s/%s, container: %s, qosLevel: %s is isolated, ignore original allocationInfo",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
				continue
			}

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			// adapt to old checkpoint without RequestQuantity property
			newPodEntries[podUID][containerName].RequestQuantity = p.getContainerRequestedCores(allocationInfo)
			switch allocationInfo.QoSLevel {
			case apiconsts.PodAnnotationQoSLevelDedicatedCores:
				newPodEntries[podUID][containerName].OwnerPoolName = allocationInfo.GetPoolName()

				// for numa_binding containers, we just clone checkpoint already exist
				if allocationInfo.CheckDedicatedNUMABinding() {
					continue containerLoop
				}

				// dedicated_cores without numa_binding is not isolated, we will try to isolate it in next adjustment.
				general.Warningf("pod: %s/%s, container: %s is dedicated_cores without numa_binding but not isolated, "+
					"we put it into fallback pool: %s temporary",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, rampUpCPUs.String())

				newPodEntries[podUID][containerName].OwnerPoolName = commonstate.PoolNameFallback
				newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)

			case apiconsts.PodAnnotationQoSLevelSystemCores:
				poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(newPodEntries, allocationInfo)
				if err != nil {
					return fmt.Errorf("pod: %s/%s, container: %s is system_cores, "+
						"getSystemPoolCPUSetAndNumaAwareAssignments failed with error: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, err)
				}

				newPodEntries[podUID][containerName].AllocationResult = poolCPUSet
				newPodEntries[podUID][containerName].OriginalAllocationResult = poolCPUSet.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

			case apiconsts.PodAnnotationQoSLevelSharedCores:
				var ownerPoolName string
				if allocationInfo.CheckSharedNUMABinding() {
					ownerPoolName = allocationInfo.GetOwnerPoolName()

					if ownerPoolName == commonstate.EmptyOwnerPoolName {
						var err error
						// why do we integrate GetOwnerPoolName + GetSpecifiedNUMABindingPoolName into GetPoolName for SharedNUMABinding containers?
						// it's because we reply on GetSpecifiedPoolName (in GetPoolName) when calling CheckNUMABindingSharedCoresAntiAffinity,
						// At that time, NUMA hint for the candidate container isn't confirmed, so we can't implement NUMA hint aware logic in GetSpecifiedPoolName.
						ownerPoolName, err = allocationInfo.GetSpecifiedNUMABindingPoolName()
						if err != nil {
							return fmt.Errorf("pod: %s/%s, container: %s is shared_cores with numa_binding, "+
								"GetSpecifiedNUMABindingPoolName failed with error: %v",
								allocationInfo.PodNamespace, allocationInfo.PodName,
								allocationInfo.ContainerName, err)
						}
					} // else already in a numa_binding share pool or isolated
				} else {
					ownerPoolName = allocationInfo.GetPoolName()
				}

				if allocationInfo.RampUp {
					general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					newPodEntries[podUID][containerName].OwnerPoolName = commonstate.EmptyOwnerPoolName
					newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				} else {
					poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newPodEntries)
					if err != nil {
						return err
					}

					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					if allocationInfo.CheckSharedNUMABinding() {
						poolEntry.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
						// set SharedNUMABinding declarations to pool entry containing SharedNUMABinding containers,
						// in order to differentiate them from non-binding share cores pools during GetFilteredPoolsCPUSetMap.
						poolEntry.Annotations = general.MergeMap(poolEntry.Annotations, map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						})
					}

					newPodEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newPodEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			case apiconsts.PodAnnotationQoSLevelReclaimedCores:
				poolEntry, err := p.getAllocationPoolEntry(allocationInfo, allocationInfo.OwnerPoolName, newPodEntries)
				if err != nil {
					return err
				}

				err = p.updateReclaimAllocationResultByPoolEntry(newPodEntries[podUID][containerName], poolEntry, nonReclaimActualBindingNUMAs)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	machineState, err = generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, newPodEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}
	p.state.SetPodEntries(newPodEntries, false)
	p.state.SetMachineState(machineState, false)
	if persistCheckpoint {
		if err = p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed")
		}
	}

	return nil
}

func (p *DynamicPolicy) generateNUMABindingPoolsCPUSetInPlace(poolsCPUSet map[string]machine.CPUSet,
	poolsQuantityMap map[string]map[int]int, availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	numaToPoolQuantityMap := make(map[int]map[string]int)
	originalAvailableCPUSet := availableCPUs.Clone()
	enableReclaim := p.dynamicConfig.GetDynamicConfiguration().EnableReclaim

	for poolName, numaToQuantity := range poolsQuantityMap {
		for numaID, quantity := range numaToQuantity {
			if numaID == commonstate.FakedNUMAID {
				// only deal with numa_binding pools
				continue
			}

			if numaToPoolQuantityMap[numaID] == nil {
				numaToPoolQuantityMap[numaID] = make(map[string]int)
			}

			numaToPoolQuantityMap[numaID][poolName] = quantity
		}
	}

	for numaID, numaPoolsToQuantityMap := range numaToPoolQuantityMap {
		numaPoolsTotalQuantity := general.SumUpMapValues(numaPoolsToQuantityMap)
		numaCPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).Difference(p.reservedCPUs)
		numaAvailableCPUs := numaCPUs.Intersection(availableCPUs)
		availableSize := numaAvailableCPUs.Size()

		general.Infof("numaID: %d, numaPoolsTotalQuantity: %d, availableSize: %d, enableReclaim: %v",
			numaID, numaPoolsTotalQuantity, availableSize, enableReclaim)

		var tErr error
		var leftCPUs machine.CPUSet
		if numaPoolsTotalQuantity <= availableSize && enableReclaim && !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			leftCPUs, tErr = p.takeCPUsForPoolsInPlace(numaPoolsToQuantityMap, poolsCPUSet, numaAvailableCPUs)
			if tErr != nil {
				return originalAvailableCPUSet, fmt.Errorf("allocate cpus for numa_binding pools in NUMA: %d failed with error: %v",
					numaID, tErr)
			}
		} else {
			// numaPoolsTotalQuantity > availableSize || !enableReclaim || p.state.GetAllowSharedCoresOverlapReclaimedCores()
			// both allocate all numaAvailableCPUs proportionally
			leftCPUs, tErr = p.generateProportionalPoolsCPUSetInPlace(numaPoolsToQuantityMap, poolsCPUSet, numaAvailableCPUs)

			if tErr != nil {
				return originalAvailableCPUSet, fmt.Errorf("generateProportionalPoolsCPUSetInPlace for numa_binding pools in NUMA: %d failed with error: %v",
					numaID, tErr)
			}
		}

		availableCPUs = availableCPUs.Difference(numaCPUs).Union(leftCPUs)
	}

	return availableCPUs, nil
}

// generatePoolsAndIsolation is used to generate cpuset pools and isolated cpuset
// 1. allocate isolated cpuset for pod/containers, and divide total cores evenly if not possible to allocate
// 2. use the left cores to allocate among different pools
// 3. apportion to other pools if reclaimed is disabled
func (p *DynamicPolicy) generatePoolsAndIsolation(poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int, availableCPUs machine.CPUSet,
	reclaimOverlapShareRatio map[string]float64) (poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, err error,
) {
	poolsBindingNUMAs := sets.NewInt()
	poolsToSkip := make([]string, 0, len(poolsQuantityMap))
	nonBindingPoolsQuantityMap := make(map[string]int)
	for poolName, numaToQuantity := range poolsQuantityMap {
		if len(numaToQuantity) > 1 {
			err = fmt.Errorf("pool: %s cross NUMAs: %+v", poolName, numaToQuantity)
			return
		} else if len(numaToQuantity) == 1 {
			for numaID, quantity := range numaToQuantity {
				if quantity == 0 {
					poolsToSkip = append(poolsToSkip, poolName)
				} else {
					if numaID != commonstate.FakedNUMAID {
						poolsBindingNUMAs.Insert(numaID)
					} else {
						nonBindingPoolsQuantityMap[poolName] = quantity
					}
				}
			}
		} else {
			poolsToSkip = append(poolsToSkip, poolName)
		}
	}

	for _, poolName := range poolsToSkip {
		general.Warningf("pool: %s with 0 quantity, skip generate", poolName)
		delete(poolsQuantityMap, poolName)
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

	poolsCPUSet = make(map[string]machine.CPUSet)
	var nbpErr error
	availableCPUs, nbpErr = p.generateNUMABindingPoolsCPUSetInPlace(poolsCPUSet, poolsQuantityMap, availableCPUs)
	if nbpErr != nil {
		err = fmt.Errorf("generateNUMABindingPoolsCPUSetInPlace failed with error: %v", nbpErr)
		return
	}

	nonBindingAvailableCPUs := machine.NewCPUSet()
	for _, numaID := range p.machineInfo.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		if poolsBindingNUMAs.Has(numaID) {
			continue
		}

		nonBindingAvailableCPUs = nonBindingAvailableCPUs.Union(p.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).Intersection(availableCPUs))
	}
	availableCPUs = availableCPUs.Difference(nonBindingAvailableCPUs)

	nonBindingAvailableSize := nonBindingAvailableCPUs.Size()
	nonBindingPoolsTotalQuantity := general.SumUpMapValues(nonBindingPoolsQuantityMap)

	isolatedCPUSet = make(map[string]map[string]machine.CPUSet)
	isolatedTotalQuantity := general.SumUpMultipleMapValues(isolatedQuantityMap)

	general.Infof("isolatedTotalQuantity: %d, nonBindingPoolsTotalQuantity: %d, nonBindingAvailableSize: %d",
		isolatedTotalQuantity, nonBindingPoolsTotalQuantity, nonBindingAvailableSize)

	var tErr error
	if nonBindingPoolsTotalQuantity+isolatedTotalQuantity <= nonBindingAvailableSize {
		general.Infof("all pools and isolated containers could be allocated")

		isolatedCPUSet, nonBindingAvailableCPUs, tErr = p.takeCPUsForContainers(isolatedQuantityMap, nonBindingAvailableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate isolated cpus for dedicated_cores failed with error: %v", tErr)
			return
		}

		if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			nonBindingAvailableCPUs, tErr = p.takeCPUsForPoolsInPlace(nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		} else {
			general.Infof("allowSharedCoresOverlapReclaimedCores is true, take all nonBindingAvailableCPUs for pools")
			nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlace(nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs)

			if tErr != nil {
				err = fmt.Errorf("generateProportionalPoolsCPUSetInPlace pools failed with error: %v", tErr)
				return
			}
		}
	} else if nonBindingPoolsTotalQuantity <= nonBindingAvailableSize {
		general.Infof("all pools could be allocated, all isolated containers would be put to pools")

		if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
			nonBindingAvailableCPUs, tErr = p.takeCPUsForPoolsInPlace(nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		} else {
			general.Infof("allowSharedCoresOverlapReclaimedCores is true, take all nonBindingAvailableCPUs for pools")
			nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlace(nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs)

			if tErr != nil {
				err = fmt.Errorf("generateProportionalPoolsCPUSetInPlace pools failed with error: %v", tErr)
				return
			}
		}
	} else if nonBindingPoolsTotalQuantity > 0 {
		general.Infof("can't allocate for all pools")

		nonBindingAvailableCPUs, tErr = p.generateProportionalPoolsCPUSetInPlace(nonBindingPoolsQuantityMap, poolsCPUSet, nonBindingAvailableCPUs)

		if tErr != nil {
			err = fmt.Errorf("generateProportionalPoolsCPUSetInPlace pools failed with error: %v", tErr)
			return
		}
	}

	availableCPUs = availableCPUs.Union(nonBindingAvailableCPUs)

	// deal with reserve pool
	if poolsCPUSet[commonstate.PoolNameReserve].IsEmpty() {
		poolsCPUSet[commonstate.PoolNameReserve] = p.reservedCPUs.Clone()
		general.Infof("set pool %s:%s", commonstate.PoolNameReserve, poolsCPUSet[commonstate.PoolNameReserve].String())
	} else {
		err = fmt.Errorf("static pool %s result: %s is generated dynamically", commonstate.PoolNameReserve, poolsCPUSet[commonstate.PoolNameReserve].String())
		return
	}

	// deal with reclaim pool
	poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(availableCPUs)

	general.Infof("poolsCPUSet: %+v", poolsCPUSet)

	if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
		enableReclaim := p.dynamicConfig.GetDynamicConfiguration().EnableReclaim
		if !enableReclaim && poolsCPUSet[commonstate.PoolNameReclaim].Size() > p.reservedReclaimedCPUsSize {
			poolsCPUSet[commonstate.PoolNameReclaim] = p.apportionReclaimedPool(
				poolsCPUSet, poolsCPUSet[commonstate.PoolNameReclaim].Clone(), nonBindingPoolsQuantityMap)
			general.Infof("apportionReclaimedPool finished, current %s pool: %s",
				commonstate.PoolNameReclaim, poolsCPUSet[commonstate.PoolNameReclaim].String())
		}
	} else {
		// p.state.GetAllowSharedCoresOverlapReclaimedCores() == true
		for poolName, cset := range poolsCPUSet {
			if ratio, found := reclaimOverlapShareRatio[poolName]; found && ratio > 0 {

				req := int(math.Ceil(float64(cset.Size()) * ratio))

				// if p.state.GetAllowSharedCoresOverlapReclaimedCores() == false, we will take cpus for reclaim pool lastly,
				// else we also should take cpus for reclaim pool reversely overlapping with share type pool to aviod cpuset jumping obviously
				var tErr error
				overlapCPUs, _, tErr := calculator.TakeByNUMABalanceReversely(p.machineInfo, cset, req)
				if tErr != nil {
					err = fmt.Errorf("take overlapCPUs from: %s to %s by ratio: %.4f failed with err: %v",
						poolName, commonstate.PoolNameReclaim, ratio, tErr)
					return
				}

				general.Infof("merge overlapCPUs: %s from pool: %s to %s by ratio: %.4f",
					overlapCPUs.String(), poolName, commonstate.PoolNameReclaim, ratio)
				poolsCPUSet[commonstate.PoolNameReclaim] = poolsCPUSet[commonstate.PoolNameReclaim].Union(overlapCPUs)
			}
		}
	}

	if poolsCPUSet[commonstate.PoolNameReclaim].IsEmpty() {
		// for reclaimed pool, we must make them exist when the node isn't in hybrid mode even if cause overlap
		general.Infof("fallback takeByNUMABalance in generatePoolsAndIsolation for reclaimedCPUSet: %s", p.reservedReclaimedCPUSet.String())
		poolsCPUSet[commonstate.PoolNameReclaim] = p.reservedReclaimedCPUSet.Clone()
	}

	// deal with forbidden pools
	currentPodEntries := p.state.GetPodEntries()
	for _, poolName := range state.ForbiddenPools.List() {
		cset, err := currentPodEntries.GetCPUSetForPool(poolName)
		if err != nil {
			general.Infof("can't get CPUSet for pool %s: %v", poolName, err)
			continue
		}
		poolsCPUSet[poolName] = cset.Clone()
	}

	return
}

func (p *DynamicPolicy) generateProportionalPoolsCPUSetInPlace(poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet, availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	availableSize := availableCPUs.Size()

	proportionalPoolsQuantityMap, totalProportionalPoolsQuantity := getProportionalPoolsQuantityMap(poolsQuantityMap, availableSize)

	general.Infof("poolsQuantityMap: %v, proportionalPoolsQuantityMap: %v", poolsQuantityMap, proportionalPoolsQuantityMap)

	// availableSize can't satisfy every pool has at least one cpu,
	// we make all pools equals to availableCPUs in this case.
	if totalProportionalPoolsQuantity > availableSize {
		for poolName := range poolsQuantityMap {
			if _, found := poolsCPUSet[poolName]; found {
				return availableCPUs.Clone(), fmt.Errorf("duplicated pool: %s", poolName)
			}

			poolsCPUSet[poolName] = availableCPUs.Clone()
		}

		return machine.NewCPUSet(), nil
	} else {
		var err error
		availableCPUs, err = p.takeCPUsForPoolsInPlace(proportionalPoolsQuantityMap, poolsCPUSet, availableCPUs)
		if err != nil {
			return availableCPUs, err
		}
	}

	return availableCPUs, nil
}

func getProportionalPoolsQuantityMap(originalPoolsQuantityMap map[string]int, availableSize int) (map[string]int, int) {
	totalProportionalPoolsQuantity := 0
	originalPoolsTotalQuantity := general.SumUpMapValues(originalPoolsQuantityMap)
	proportionalPoolsQuantityMap := make(map[string]int)

	for poolName, poolQuantity := range originalPoolsQuantityMap {
		proportionalSize := general.Max(getProportionalSize(poolQuantity, originalPoolsTotalQuantity, availableSize, true /*ceil*/), 1)
		proportionalPoolsQuantityMap[poolName] = proportionalSize
		totalProportionalPoolsQuantity += proportionalSize
	}

	poolNames := make([]string, 0, len(proportionalPoolsQuantityMap))

	for poolName := range proportionalPoolsQuantityMap {
		poolNames = append(poolNames, poolName)
	}

	sort.Slice(poolNames, func(x, y int) bool {
		// sort in descending order
		return proportionalPoolsQuantityMap[poolNames[x]] > proportionalPoolsQuantityMap[poolNames[y]]
	})

	// corner case: after divide, the total count goes to be bigger than available total
	for totalProportionalPoolsQuantity > availableSize {
		curTotalProportionalPoolsQuantity := totalProportionalPoolsQuantity

		for _, poolName := range poolNames {
			quantity := proportionalPoolsQuantityMap[poolName]

			if quantity > 1 && totalProportionalPoolsQuantity > 0 {
				quantity--
				totalProportionalPoolsQuantity--
				proportionalPoolsQuantityMap[poolName] = quantity

				if totalProportionalPoolsQuantity == availableSize {
					break
				}
			}
		}

		// availableSize can't satisfy every pool has at least one cpu
		if curTotalProportionalPoolsQuantity == totalProportionalPoolsQuantity {
			break
		}
	}

	return proportionalPoolsQuantityMap, totalProportionalPoolsQuantity
}

// apportionReclaimedPool tries to allocate reclaimed cores to none-binding && none-reclaimed pools.
// if we disable reclaim on current node, this could be used a down-grade strategy
// to disable reclaimed workloads in emergency
func (p *DynamicPolicy) apportionReclaimedPool(poolsCPUSet map[string]machine.CPUSet, reclaimedCPUs machine.CPUSet, nonBindingPoolsQuantityMap map[string]int) machine.CPUSet {
	totalSize := 0
	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		} else if _, found := nonBindingPoolsQuantityMap[poolName]; !found {
			// numa-binding && none-reclaimed pools already handled in generateNUMABindingPoolsCPUSetInPlace
			continue
		}
		totalSize += poolCPUs.Size()
	}

	availableSize := reclaimedCPUs.Size() - p.reservedReclaimedCPUsSize
	if availableSize <= 0 || totalSize == 0 {
		return reclaimedCPUs
	}

	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		} else if _, found := nonBindingPoolsQuantityMap[poolName]; !found {
			// numa-binding && none-reclaimed pools already handled in generateNUMABindingPoolsCPUSetInPlace
			continue
		}

		proportionalSize := general.Max(getProportionalSize(poolCPUs.Size(), totalSize, availableSize, false /*ceil*/), 1)

		var err error
		var cpuset machine.CPUSet
		cpuset, reclaimedCPUs, err = calculator.TakeHTByNUMABalance(p.machineInfo, reclaimedCPUs, proportionalSize)
		if err != nil {
			general.Errorf("take %d cpus from reclaimedCPUs: %s, size: %d failed with error: %v",
				proportionalSize, reclaimedCPUs.String(), reclaimedCPUs.Size(), err)
			return reclaimedCPUs
		}

		poolsCPUSet[poolName] = poolCPUs.Union(cpuset)
		general.Infof("take %s to %s; prev: %s, current: %s", cpuset.String(), poolName, poolCPUs.String(), poolsCPUSet[poolName].String())

		if reclaimedCPUs.Size() <= p.reservedReclaimedCPUsSize {
			break
		}
	}

	return reclaimedCPUs
}

func (p *DynamicPolicy) takeCPUsForPoolsInPlace(poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
) (machine.CPUSet, error) {
	originalAvailableCPUSet := availableCPUs.Clone()
	var poolsCPUSetToAdd map[string]machine.CPUSet
	var tErr error
	poolsCPUSetToAdd, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
	if tErr != nil {
		return originalAvailableCPUSet, fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
	}

	for poolName, cset := range poolsCPUSetToAdd {
		if _, found := poolsCPUSet[poolName]; found {
			return originalAvailableCPUSet, fmt.Errorf("duplicated pool: %s", poolName)
		}

		poolsCPUSet[poolName] = cset
	}

	return availableCPUs, nil
}

// takeCPUsForPools tries to allocate cpuset for each given pool,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset pool map and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForPools(poolsQuantityMap map[string]int,
	availableCPUs machine.CPUSet,
) (map[string]machine.CPUSet, machine.CPUSet, error) {
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
	availableCPUs machine.CPUSet,
) (map[string]map[string]machine.CPUSet, machine.CPUSet, error) {
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

func (p *DynamicPolicy) shouldSharedCoresRampUp(podUID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pod, err := p.metaServer.GetPod(ctx, podUID)

	if err != nil {
		general.Errorf("get pod failed with error: %v, try to ramp up it", err)
		return true
	} else if pod == nil {
		general.Infof("can't get pod: %s from metaServer, try to ramp up it", podUID)
		return true
	} else if !native.PodIsPending(pod) {
		general.Infof("pod: %s/%s isn't pending(not admit firstly), not try to ramp up it", pod.Namespace, pod.Name)
		return false
	} else {
		general.Infof("pod: %s/%s isn't active, try to ramp up it", pod.Namespace, pod.Name)
		return true
	}
}

func (p *DynamicPolicy) doAndCheckPutAllocationInfoPodResizingAware(originAllocationInfo, allocationInfo *state.AllocationInfo, incrByReq, podInplaceUpdateResizing, persistCheckpoint bool) (*state.AllocationInfo, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("doAndCheckPutAllocationInfo got nil allocationInfo")
	}

	// need to adjust pools and putAllocationsAndAdjustAllocationEntries will set the allocationInfo after adjusted
	err := p.putAllocationsAndAdjustAllocationEntriesResizeAware([]*state.AllocationInfo{originAllocationInfo}, []*state.AllocationInfo{allocationInfo}, incrByReq, podInplaceUpdateResizing, persistCheckpoint)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s putAllocationsAndAdjustAllocationEntriesResizeAware failed with error: %v",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
		return nil, fmt.Errorf("putAllocationsAndAdjustAllocationEntries failed with error: %v", err)
	}

	checkedAllocationInfo := p.state.GetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName)
	if checkedAllocationInfo == nil {
		general.Errorf("pod: %s/%s, container: %s get nil allocationInfo after putAllocationsAndAdjustAllocationEntries",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
		return nil, fmt.Errorf("putAllocationsAndAdjustAllocationEntries failed with error: %v", err)
	}

	return checkedAllocationInfo, nil
}

func (p *DynamicPolicy) doAndCheckPutAllocationInfo(allocationInfo *state.AllocationInfo, incrByReq, persistCheckpoint bool) (*state.AllocationInfo, error) {
	return p.doAndCheckPutAllocationInfoPodResizingAware(nil, allocationInfo, incrByReq, false, persistCheckpoint)
}

func (p *DynamicPolicy) getReclaimOverlapShareRatio(entries state.PodEntries) (map[string]float64, error) {
	if !p.state.GetAllowSharedCoresOverlapReclaimedCores() {
		return nil, nil
	}

	if entries.CheckPoolEmpty(commonstate.PoolNameReclaim) {
		return nil, fmt.Errorf("reclaim pool misses in current entries")
	}

	reclaimOverlapShareRatio := make(map[string]float64)

	curReclaimCPUSet := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult

	// Iterate through all pools to calculate overlap ratios
	for poolName, subEntries := range entries {
		if !subEntries.IsPoolEntry() {
			continue
		}

		allocationInfo := subEntries.GetPoolEntry()

		if allocationInfo != nil && commonstate.GetPoolType(poolName) == commonstate.PoolNameShare {
			if allocationInfo.AllocationResult.IsEmpty() {
				continue
			}

			shareTypePoolSize := allocationInfo.AllocationResult.Size()
			overlapSize := allocationInfo.AllocationResult.Intersection(curReclaimCPUSet).Size()

			if overlapSize == 0 {
				continue
			}

			reclaimOverlapShareRatio[poolName] = float64(overlapSize) / float64(shareTypePoolSize)
		}
	}

	// If no overlap was found, calculate non-overlap ratios
	if len(reclaimOverlapShareRatio) == 0 {
		reclaimNonOverlapShareRatio := make(map[string]float64)

		// Iterate over all sub-entries to compute non-overlap ratios
		for _, subEntries := range entries {
			if subEntries.IsPoolEntry() {
				continue
			}

			for _, allocationInfo := range subEntries {
				if allocationInfo == nil || allocationInfo.AllocationResult.IsEmpty() {
					continue
				}

				// Only process shared pools
				poolName := allocationInfo.GetPoolName()
				if commonstate.GetPoolType(poolName) == commonstate.PoolNameShare {
					requestQuantity := allocationInfo.RequestQuantity
					if requestQuantity > 0 {
						reclaimNonOverlapShareRatio[poolName] += requestQuantity / float64(allocationInfo.AllocationResult.Size())
					}
				}
			}
		}

		// Convert non-overlap ratios to overlap ratios
		for poolName, ratio := range reclaimNonOverlapShareRatio {
			reclaimOverlapShareRatio[poolName] = 1.0 - ratio
		}
	}
	return reclaimOverlapShareRatio, nil
}

func (p *DynamicPolicy) systemCoresHintHandler(_ context.Context, request *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return util.PackResourceHintsResponse(request, string(v1.ResourceCPU),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) systemCoresAllocationHandler(ctx context.Context, req *pluginapi.ResourceRequest, persistCheckpoint bool) (*pluginapi.ResourceAllocationResponse, error) {
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return p.allocationSidecarHandler(ctx, req, apiconsts.PodAnnotationQoSLevelSystemCores, persistCheckpoint)
	}

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSystemCores),
		InitTimestamp: time.Now().Format(util.QRMTimeFormat),
	}

	poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(p.state.GetPodEntries(), allocationInfo)
	if err != nil {
		general.ErrorS(err, "unable to get system pool cpuset and topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nil, err
	}

	systemPoolName, err := allocationInfo.GetSpecifiedSystemPoolName()
	if err != nil {
		return nil, err
	}

	general.InfoS("allocate system pool cpuset successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"poolName", systemPoolName,
		"result", poolCPUSet.String(),
		"topologyAwareAssignments", topologyAwareAssignments)

	allocationInfo.OwnerPoolName = systemPoolName
	allocationInfo.AllocationResult = poolCPUSet
	allocationInfo.OriginalAllocationResult = poolCPUSet.Clone()
	allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
	allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)

	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, persistCheckpoint)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s generateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("generateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState, persistCheckpoint)

	resp, err := cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *DynamicPolicy) getSystemPoolCPUSetAndNumaAwareAssignments(podEntries state.PodEntries,
	allocationInfo *state.AllocationInfo,
) (machine.CPUSet, map[int]machine.CPUSet, error) {
	if allocationInfo == nil {
		return machine.CPUSet{}, nil, fmt.Errorf("allocationInfo is nil")
	}

	poolCPUSet := machine.NewCPUSet()
	specifiedPoolName := allocationInfo.GetSpecifiedPoolName()
	if specifiedPoolName != commonstate.EmptyOwnerPoolName {
		for pool, entries := range podEntries {
			if !entries.IsPoolEntry() {
				continue
			}

			if pool == specifiedPoolName || strings.HasPrefix(pool, specifiedPoolName) {
				poolCPUSet = poolCPUSet.Union(entries.GetPoolEntry().AllocationResult)
				general.Infof("pod: %s/%s, container: %s get system pool cpuset from pool: %s, cpuset: %s", allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, pool, entries.GetPoolEntry().AllocationResult.String())
			}
		}
	}

	// if pool set is empty, try to get default cpuset
	if poolCPUSet.IsEmpty() {
		// if the pod is numa binding, get the default cpuset from machine state
		if allocationInfo.CheckNUMABinding() {
			poolCPUSet = p.state.GetMachineState().GetAvailableCPUSet(p.reservedCPUs)
		}

		// if the default cpuset is empty or no numa binding, use all cpuset as default cpuset
		if poolCPUSet.IsEmpty() {
			poolCPUSet = p.machineInfo.CPUDetails.CPUs()
		}
		general.Infof("pod: %s/%s, container: %s get system pool cpuset from default cpuset: %s", allocationInfo.PodNamespace, allocationInfo.PodName,
			allocationInfo.ContainerName, poolCPUSet.String())
	}

	if poolCPUSet.IsEmpty() {
		return machine.CPUSet{}, nil, fmt.Errorf("no system pool cpuset for pool %s", specifiedPoolName)
	}

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, poolCPUSet)
	if err != nil {
		return machine.CPUSet{}, nil, fmt.Errorf("unable to get numa aware assignments: %v", err)
	}

	return poolCPUSet, topologyAwareAssignments, nil
}

func (p *DynamicPolicy) getAllocationPoolEntry(allocationInfo *state.AllocationInfo, ownerPoolName string, entries state.PodEntries) (*state.AllocationInfo, error) {
	poolEntry := entries[ownerPoolName][commonstate.FakedContainerName]
	if poolEntry != nil {
		return poolEntry, nil
	}

	errMsg := fmt.Sprintf("cpu advisor doesn't return entry for pool: %s and it's referred by pod: %s/%s, container: %s, qosLevel: %s",
		ownerPoolName, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)

	general.Errorf(errMsg)

	_ = p.emitter.StoreInt64(util.MetricNameOrphanContainer, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "podNamespace", Val: allocationInfo.PodNamespace},
		metrics.MetricTag{Key: "podName", Val: allocationInfo.PodName},
		metrics.MetricTag{Key: "containerName", Val: allocationInfo.ContainerName},
		metrics.MetricTag{Key: "poolName", Val: ownerPoolName})
	return nil, fmt.Errorf(errMsg)
}

func (p *DynamicPolicy) updateReclaimAllocationResultByPoolEntry(allocationInfo *state.AllocationInfo,
	poolEntry *state.AllocationInfo, nonReclaimActualBindingNUMAs machine.CPUSet,
) error {
	numaID, err := allocationInfo.GetSpecifiedNUMABindingNUMAID()
	if err != nil {
		return err
	}

	getActualNUMABindingResult := func(topologyAwareAssignments map[int]machine.CPUSet) (machine.CPUSet, map[int]machine.CPUSet, error) {
		var (
			actualTopologyAwareAssignments map[int]machine.CPUSet
			actualAllocationResult         machine.CPUSet
		)
		if numaID != commonstate.FakedNUMAID {
			cpuSet, ok := topologyAwareAssignments[numaID]
			if !ok {
				return machine.CPUSet{}, nil, fmt.Errorf("pod: %s/%s container: %s is reclaimed_cores with numa_binding specified numa: %d not found in topologyAwareAssignments: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, numaID, topologyAwareAssignments)
			}
			actualAllocationResult = cpuSet.Clone()
			actualTopologyAwareAssignments = map[int]machine.CPUSet{
				numaID: cpuSet.Clone(),
			}
		} else {
			// set non-RNB numa allocation info
			numaSet := machine.NewCPUSet()
			newTopologyAwareAssignments := make(map[int]machine.CPUSet)
			for numaNode, cpuSet := range topologyAwareAssignments {
				if !nonReclaimActualBindingNUMAs.Contains(numaNode) {
					continue
				}

				if cpuSet.Size() > 0 {
					numaSet = numaSet.Union(cpuSet)
				}

				newTopologyAwareAssignments[numaNode] = cpuSet.Clone()
			}
			actualAllocationResult = numaSet
			actualTopologyAwareAssignments = newTopologyAwareAssignments
		}
		return actualAllocationResult, actualTopologyAwareAssignments, nil
	}

	actualAllocationResult, actualTopologyAwareAssignments, err := getActualNUMABindingResult(machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments))
	if err != nil {
		return fmt.Errorf("get actual NUMA binding result: %v", err)
	}

	actualOriginalAllocationResult, actualOriginalTopologyAwareAssignments, err := getActualNUMABindingResult(machine.DeepcopyCPUAssignment(poolEntry.OriginalTopologyAwareAssignments))
	if err != nil {
		return fmt.Errorf("get original actual NUMA binding result: %v", err)
	}

	general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
		allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, poolEntry.OwnerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

	allocationInfo.OwnerPoolName = poolEntry.OwnerPoolName
	allocationInfo.AllocationResult = actualAllocationResult
	allocationInfo.OriginalAllocationResult = actualOriginalAllocationResult
	allocationInfo.TopologyAwareAssignments = actualTopologyAwareAssignments
	allocationInfo.OriginalTopologyAwareAssignments = actualOriginalTopologyAwareAssignments
	return nil
}
