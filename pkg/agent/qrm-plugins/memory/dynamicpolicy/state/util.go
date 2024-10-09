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

package state

import (
	"fmt"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GenerateMemoryContainerAllocationMeta generates allocation metadata specifically for a memory resource request.
// This function is a wrapper around GenerateGenericContainerAllocationMeta, but it automatically computes the owner pool
// name using a QoS level and a specific annotation from the request.
// Parameters:
// - req: The resource request for memory, containing information about the pod and container.
// - qosLevel: The QoS (Quality of Service) level for the container.
// Returns:
// - A pointer to a commonstate.AllocationMeta struct generated based on the memory-specific logic.
func GenerateMemoryContainerAllocationMeta(req *pluginapi.ResourceRequest, qosLevel string) commonstate.AllocationMeta {
	return commonstate.GenerateGenericContainerAllocationMeta(req,
		// Determine the pool name based on QoS level and a CPU enhancement annotation from the request.
		commonstate.GetSpecifiedPoolName(qosLevel, req.Annotations[consts.PodAnnotationCPUEnhancementCPUSet]),
		qosLevel)
}

// GenerateMachineState returns NUMANodeResourcesMap based on
// machine info and reserved resources
func GenerateMachineState(machineInfo *info.MachineInfo, reserved map[v1.ResourceName]map[int]uint64) (NUMANodeResourcesMap, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("GenerateMachineState got nil machineInfo")
	}

	// todo: currently only support memory, we will support huge page later.
	defaultResourcesMachineState := make(NUMANodeResourcesMap)
	for _, resourceName := range []v1.ResourceName{v1.ResourceMemory} {
		machineState, err := GenerateResourceState(machineInfo, reserved, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceState for resource: %s failed with error: %v", resourceName, err)
		}

		defaultResourcesMachineState[resourceName] = machineState
	}
	return defaultResourcesMachineState, nil
}

// GenerateResourceState returns NUMANodeMap for given resource based on
// machine info and reserved resources
func GenerateResourceState(machineInfo *info.MachineInfo, reserved map[v1.ResourceName]map[int]uint64, resourceName v1.ResourceName) (NUMANodeMap, error) {
	defaultMachineState := make(NUMANodeMap)

	switch resourceName {
	case v1.ResourceMemory:
		for _, node := range machineInfo.Topology {
			totalMemSizeQuantity := node.Memory
			numaReservedMemQuantity := reserved[resourceName][node.Id]

			if totalMemSizeQuantity < numaReservedMemQuantity {
				return nil, fmt.Errorf("invalid reserved memory: %d in NUMA: %d with total memory size: %d", numaReservedMemQuantity, node.Id, totalMemSizeQuantity)
			}

			allocatableQuantity := totalMemSizeQuantity - numaReservedMemQuantity
			freeQuantity := allocatableQuantity

			defaultMachineState[node.Id] = &NUMANodeState{
				TotalMemSize:   totalMemSizeQuantity,
				SystemReserved: numaReservedMemQuantity,
				Allocatable:    allocatableQuantity,
				Allocated:      0,
				Free:           freeQuantity,
				PodEntries:     make(PodEntries),
			}
		}
	default:
		return nil, fmt.Errorf("unsupported resource name: %s", resourceName)
	}

	return defaultMachineState, nil
}

// GenerateMachineStateFromPodEntries returns NUMANodeResourcesMap based on
// machine info and reserved resources (along with existed pod entries)
func GenerateMachineStateFromPodEntries(machineInfo *info.MachineInfo,
	podResourceEntries PodResourceEntries, reserved map[v1.ResourceName]map[int]uint64,
) (NUMANodeResourcesMap, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries got nil machineInfo")
	}

	// todo: currently only support memory, we will support huge page later.
	defaultResourcesMachineState := make(NUMANodeResourcesMap)
	for _, resourceName := range []v1.ResourceName{v1.ResourceMemory} {
		machineState, err := GenerateResourceStateFromPodEntries(machineInfo, podResourceEntries[resourceName], reserved, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceState for resource: %s failed with error: %v", resourceName, err)
		}

		defaultResourcesMachineState[resourceName] = machineState
	}
	return defaultResourcesMachineState, nil
}

// GenerateResourceStateFromPodEntries returns NUMANodeMap for given resource based on
// machine info and reserved resources along with existed pod entries
func GenerateResourceStateFromPodEntries(machineInfo *info.MachineInfo,
	podEntries PodEntries, reserved map[v1.ResourceName]map[int]uint64, resourceName v1.ResourceName,
) (NUMANodeMap, error) {
	switch resourceName {
	case v1.ResourceMemory:
		return GenerateMemoryStateFromPodEntries(machineInfo, podEntries, reserved)
	default:
		return nil, fmt.Errorf("unsupported resource name: %s", resourceName)
	}
}

// GenerateMemoryStateFromPodEntries returns NUMANodeMap for memory based on
// machine info and reserved resources along with existed pod entries
func GenerateMemoryStateFromPodEntries(machineInfo *info.MachineInfo,
	podEntries PodEntries, reserved map[v1.ResourceName]map[int]uint64,
) (NUMANodeMap, error) {
	machineState, err := GenerateResourceState(machineInfo, reserved, v1.ResourceMemory)
	if err != nil {
		return nil, fmt.Errorf("GenerateResourceState failed with error: %v", err)
	}

	for numaId, numaNodeState := range machineState {
		var allocatedMemQuantityInNumaNode uint64 = 0

		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					curContainerAllocatedQuantityInNumaNode := allocationInfo.TopologyAwareAllocations[numaId]
					if curContainerAllocatedQuantityInNumaNode == 0 &&
						allocationInfo.NumaAllocationResult.Intersection(machine.NewCPUSet(numaId)).IsEmpty() {
						continue
					}

					allocatedMemQuantityInNumaNode += curContainerAllocatedQuantityInNumaNode
					numaNodeAllocationInfo := allocationInfo.Clone()
					numaNodeAllocationInfo.NumaAllocationResult = machine.NewCPUSet(numaId)

					if curContainerAllocatedQuantityInNumaNode != 0 {
						numaNodeAllocationInfo.AggregatedQuantity = curContainerAllocatedQuantityInNumaNode
						numaNodeAllocationInfo.TopologyAwareAllocations = map[int]uint64{
							numaId: curContainerAllocatedQuantityInNumaNode,
						}
					}
					numaNodeState.SetAllocationInfo(podUID, containerName, numaNodeAllocationInfo)
				}
			}
		}

		numaNodeState.Allocated = allocatedMemQuantityInNumaNode
		if numaNodeState.Allocatable < numaNodeState.Allocated {
			klog.Warningf("[GenerateMemoryStateFromPodEntries] invalid allocated memory: %d in NUMA: %d"+
				" with allocatable memory size: %d, total memory size: %d, reserved memory size: %d",
				numaNodeState.Allocated, numaId, numaNodeState.Allocatable, numaNodeState.TotalMemSize, numaNodeState.SystemReserved)
			numaNodeState.Allocatable = numaNodeState.Allocated
		}
		numaNodeState.Free = numaNodeState.Allocatable - numaNodeState.Allocated

		machineState[numaId] = numaNodeState
	}

	return machineState, nil
}
