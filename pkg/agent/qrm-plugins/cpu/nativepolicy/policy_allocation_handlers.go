/*
Copyright 2022 The Katalyst Authors.
Copyright 2017 The Kubernetes Authors.

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

package nativepolicy

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/nativepolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	// ErrorSMTAlignment represents the type of an SMTAlignmentError.
	ErrorSMTAlignment = "SMTAlignmentError"
)

// SMTAlignmentError represents an error due to SMT alignment
type SMTAlignmentError struct {
	RequestedCPUs int
	CpusPerCore   int
}

func (e SMTAlignmentError) Error() string {
	return fmt.Sprintf("SMT Alignment Error: requested %d cpus not multiple cpus per core = %d", e.RequestedCPUs, e.CpusPerCore)
}

func (e SMTAlignmentError) Type() string {
	return ErrorSMTAlignment
}

func (p *NativePolicy) dedicatedCoresAllocationHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	if p.enableFullPhysicalCPUsOnly && ((reqInt % p.machineInfo.CPUsPerCore()) != 0) {
		// Since CPU plugin has been enabled requesting strict SMT alignment, it means a guaranteed pod can only be admitted
		// if the CPU requested is a multiple of the number of virtual cpus per physical cores.
		// In case CPU request is not a multiple of the number of virtual cpus per physical cores the Pod will be put
		// in Failed state, with SMTAlignmentError as reason. Since the allocation happens in terms of physical cores
		// and the scheduler is responsible for ensuring that the workload goes to a node that has enough CPUs,
		// the pod would be placed on a node where there are enough physical cores available to be allocated.
		// Just like the behavior in case of static policy, takeByTopology will try to first allocate CPUs from the same socket
		// and only in case the request cannot be satisfied on a single socket, CPU allocation is done for a workload to occupy all
		// CPUs on a physical core. Allocation of individual threads would never have to occur.
		return nil, SMTAlignmentError{
			RequestedCPUs: reqInt,
			CpusPerCore:   p.machineInfo.CPUsPerCore(),
		}
	}

	machineState := p.state.GetMachineState()

	// Allocate CPUs according to the NUMA affinity contained in the hint.
	result, err := p.allocateCPUs(machineState, reqInt, req.Hint, p.cpusToReuse[req.PodUid])
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
			"cpuset", result.String())
		return nil, err
	}

	allocationInfo := &state.AllocationInfo{
		PodUid:                           req.PodUid,
		PodNamespace:                     req.PodNamespace,
		PodName:                          req.PodName,
		ContainerName:                    req.ContainerName,
		ContainerType:                    req.ContainerType.String(),
		ContainerIndex:                   req.ContainerIndex,
		PodType:                          req.PodType,
		OwnerPoolName:                    state.PoolNameDedicated,
		AllocationResult:                 result.Clone(),
		OriginalAllocationResult:         result.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		Labels:                           general.DeepCopyMap(req.Labels),
		Annotations:                      general.DeepCopyMap(req.Annotations),
		RequestQuantity:                  reqInt,
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState)

	resp, err := util.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *NativePolicy) sharedPoolAllocationHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresAllocationHandler got nil req")
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	defaultCPUSet := p.state.GetMachineState().GetDefaultCPUSet()
	if defaultCPUSet.IsEmpty() {
		return nil, errors.New("default cpuset is empty")
	}

	general.InfoS("allocate default cpuset successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"numCPUs", reqInt,
		"result", defaultCPUSet.String())

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, defaultCPUSet)
	if err != nil {
		general.ErrorS(err, "unable to calculate topologyAwareAssignments",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt,
			"cpuset", defaultCPUSet.String())
		return nil, err
	}

	allocationInfo := &state.AllocationInfo{
		PodUid:                           req.PodUid,
		PodNamespace:                     req.PodNamespace,
		PodName:                          req.PodName,
		ContainerName:                    req.ContainerName,
		ContainerType:                    req.ContainerType.String(),
		ContainerIndex:                   req.ContainerIndex,
		PodType:                          req.PodType,
		OwnerPoolName:                    state.PoolNameShare,
		AllocationResult:                 defaultCPUSet.Clone(),
		OriginalAllocationResult:         defaultCPUSet.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		InitTimestamp:                    time.Now().Format(util.QRMTimeFormat),
		Labels:                           general.DeepCopyMap(req.Labels),
		Annotations:                      general.DeepCopyMap(req.Annotations),
		RequestQuantity:                  reqInt,
	}

	// update pod entries directly.
	// if one of subsequent steps is failed, we will delete current allocationInfo from podEntries in defer function of allocation function.
	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo)
	podEntries := p.state.GetPodEntries()

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}
	p.state.SetMachineState(updatedMachineState)

	resp, err := util.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs, false, true, req)
	if err != nil {
		general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
	}
	return resp, nil
}

func (p *NativePolicy) allocateCPUs(machineState state.NUMANodeMap, numCPUs int, hint *pluginapi.TopologyHint, reusableCPUs machine.CPUSet) (machine.CPUSet, error) {
	klog.InfoS("AllocateCPUs", "numCPUs", numCPUs, "hint", hint)

	allocatableCPUs := machineState.GetAvailableCPUSet(p.reservedCPUs).Union(reusableCPUs)

	// If there are aligned CPUs in numaAffinity, attempt to take those first.
	result := machine.NewCPUSet()
	if hint != nil {
		alignedCPUs := machine.NewCPUSet()
		for _, numaNode := range hint.Nodes {
			alignedCPUs = alignedCPUs.Union(machineState[int(numaNode)].GetAvailableCPUSet(p.reservedCPUs))
		}

		numAlignedToAlloc := alignedCPUs.Size()
		if numCPUs < numAlignedToAlloc {
			numAlignedToAlloc = numCPUs
		}

		alignedCPUs, err := p.takeByTopology(alignedCPUs, numAlignedToAlloc)
		if err != nil {
			return machine.NewCPUSet(), err
		}

		result = result.Union(alignedCPUs)
	}

	// Get any remaining CPUs from what's leftover after attempting to grab aligned ones.
	remainingCPUs, err := p.takeByTopology(allocatableCPUs.Difference(result), numCPUs-result.Size())
	if err != nil {
		return machine.NewCPUSet(), err
	}
	result = result.Union(remainingCPUs)

	klog.InfoS("AllocateCPUs", "result", result)
	return result, nil
}

func (p *NativePolicy) takeByTopology(availableCPUs machine.CPUSet, numCPUs int) (machine.CPUSet, error) {
	if p.enableDistributeCPUsAcrossNUMA {
		cpuGroupSize := 1
		if p.enableFullPhysicalCPUsOnly {
			cpuGroupSize = p.machineInfo.CPUsPerCore()
		}
		return calculator.TakeByTopologyNUMADistributed(p.machineInfo.CPUTopology, availableCPUs, numCPUs, cpuGroupSize)
	}
	return calculator.TakeByTopologyNUMAPacked(p.machineInfo.CPUTopology, availableCPUs, numCPUs)
}

func (p *NativePolicy) updateCPUsToReuse(req *pluginapi.ResourceRequest, cset machine.CPUSet) {
	// If pod entries to m.cpusToReuse other than the current pod exist, delete them.
	for podUID := range p.cpusToReuse {
		if podUID != req.PodUid {
			delete(p.cpusToReuse, podUID)
		}
	}
	// If no cpuset exists for cpusToReuse by this pod yet, create one.
	if _, ok := p.cpusToReuse[req.PodUid]; !ok {
		p.cpusToReuse[req.PodUid] = machine.NewCPUSet()
	}
	// Check if the container is an init container.
	// If so, add its cpuset to the cpuset of reusable CPUs for any new allocations.
	if req.ContainerType == pluginapi.ContainerType_INIT {
		p.cpusToReuse[req.PodUid] = p.cpusToReuse[req.PodUid].Union(cset)
		return
	}
	// Otherwise it is an app container.
	// Remove its cpuset from the cpuset of reusable CPUs for any new allocations.
	p.cpusToReuse[req.PodUid] = p.cpusToReuse[req.PodUid].Difference(cset)
}
