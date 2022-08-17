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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GenerateResourcesMachineStateFromPodEntries is used to generate NUMANodeResourcesMap
// for all memory-related resources based on given pod entries
func GenerateResourcesMachineStateFromPodEntries(machineInfo *info.MachineInfo,
	podResourceEntries PodResourceEntries, reservedMemory map[v1.ResourceName]map[int]uint64) (NUMANodeResourcesMap, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("GenerateResourcesMachineStateFromPodEntries got nil machineInfo")
	}

	// todo: currently only support memory, we will support huge page later.
	defaultResourcesMachineState := make(NUMANodeResourcesMap)
	for _, resourceName := range []v1.ResourceName{v1.ResourceMemory} {
		machineState, err := generateMachineStateByPodEntries(machineInfo, podResourceEntries[resourceName], reservedMemory, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GetDefaultMachineState for resource: %s failed with error: %v", resourceName, err)
		}

		defaultResourcesMachineState[resourceName] = machineState
	}

	return defaultResourcesMachineState, nil
}

// GenerateMemoryMachineStateFromPodEntries is used to generate NUMANodeMap struct
// based on pod entries only for v1.ResourceMemory
func GenerateMemoryMachineStateFromPodEntries(machineInfo *info.MachineInfo, podEntries PodEntries,
	reservedMemory map[v1.ResourceName]map[int]uint64) (NUMANodeMap, error) {
	machineState, err := GetDefaultMachineState(machineInfo, reservedMemory, v1.ResourceMemory)
	if err != nil {
		return nil, fmt.Errorf("GetDefaultMachineState failed with error: %v", err)
	}

	for numaId, numaNodeState := range machineState {
		var allocatedMemQuantityInNumaNode uint64 = 0

		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					curContainerAllocatedQuantityInNumaNode := allocationInfo.TopologyAwareAllocations[numaId]
					if curContainerAllocatedQuantityInNumaNode == 0 && allocationInfo.NumaAllocationResult.Intersection(machine.NewCPUSet(numaId)).IsEmpty() {
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
			klog.Warningf("[GenerateMemoryMachineStateFromPodEntries] invalid allocated memory: %d in NUMA: %d"+
				" with allocatable memory size: %d, total memory size: %d, reserved memory size: %d",
				numaNodeState.Allocated, numaId, numaNodeState.Allocatable, numaNodeState.TotalMemSize, numaNodeState.SystemReserved)

			numaNodeState.Allocatable = numaNodeState.Allocated
		}
		numaNodeState.Free = numaNodeState.Allocatable - numaNodeState.Allocated

		machineState[numaId] = numaNodeState
	}

	return machineState, nil
}

// generateMachineStateByPodEntries is used to generate NUMANodeMap struct
// based on pod entries only for all memory-related resources
func generateMachineStateByPodEntries(machineInfo *info.MachineInfo,
	podEntries PodEntries, reservedMemory map[v1.ResourceName]map[int]uint64, resourceName v1.ResourceName) (NUMANodeMap, error) {
	switch resourceName {
	case v1.ResourceMemory:
		return GenerateMemoryMachineStateFromPodEntries(machineInfo, podEntries, reservedMemory)
	default:
		return nil, fmt.Errorf("unsupported resource name: %s", resourceName)
	}
}
