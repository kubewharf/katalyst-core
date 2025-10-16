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
	"time"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/preoccupation"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
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

// WrapAllocationMetaFilter takes a filter function that operates on
// AllocationMeta and returns a wrapper function that applies the same filter
// to an AllocationInfo by extracting its AllocationMeta.
func WrapAllocationMetaFilter(metaFilter func(meta *commonstate.AllocationMeta) bool) func(info *AllocationInfo) bool {
	return func(info *AllocationInfo) bool {
		if info == nil {
			return false // Handle nil cases safely.
		}
		return metaFilter(&info.AllocationMeta)
	}
}

// GetReclaimedNUMAHeadroom returns the total reclaimed headroom for the given NUMA set.
func GetReclaimedNUMAHeadroom(numaHeadroom map[int]int64, numaSet machine.CPUSet) int64 {
	res := int64(0)

	for _, numaID := range numaSet.ToSliceNoSortInt() {
		res += numaHeadroom[numaID]
	}

	return res
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
	podResourceEntries PodResourceEntries, originResourcesMachineState NUMANodeResourcesMap,
	reserved map[v1.ResourceName]map[int]uint64,
) (NUMANodeResourcesMap, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries got nil machineInfo")
	}

	if originResourcesMachineState == nil {
		originResourcesMachineState = make(NUMANodeResourcesMap)
	}

	// todo: currently only support memory, we will support huge page later.
	currentResourcesMachineState := make(NUMANodeResourcesMap)
	for _, resourceName := range []v1.ResourceName{v1.ResourceMemory} {
		machineState, err := GenerateResourceStateFromPodEntries(machineInfo, podResourceEntries[resourceName],
			originResourcesMachineState[resourceName], reserved, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceState for resource: %s failed with error: %v", resourceName, err)
		}

		currentResourcesMachineState[resourceName] = machineState
	}
	return currentResourcesMachineState, nil
}

// updateMachineStatePreOccPodEntries update the pre-occupation pod from pod entries and origin machine state
func updateMachineStatePreOccPodEntries(currentMachineState, originMachineState NUMANodeMap) {
	// override pre-occupation pod from pod entries
	now := time.Now()
	for numaID, numaState := range currentMachineState {
		preOccPodEntries := make(PodEntries)
		originalPodEntries := make(PodEntries)
		if originState, ok := originMachineState[numaID]; ok {
			if originState.PodEntries != nil {
				originalPodEntries = originState.PodEntries
			}
			if originState.PreOccPodEntries != nil {
				preOccPodEntries = originState.PreOccPodEntries
			}
		}

		for podUID, containerEntries := range originalPodEntries {
			// skip pod that already in current machine state
			if _, ok := numaState.PodEntries[podUID]; ok {
				continue
			}

			for containerName, allocationInfo := range containerEntries {
				if allocationInfo == nil {
					general.Warningf("nil allocationInfo in podEntries")
					continue
				}

				// skip unneeded pre-occupation allocation info
				if !preoccupation.PreOccAllocationFilter(allocationInfo.AllocationMeta) {
					continue
				}

				if _, ok := preOccPodEntries[podUID]; !ok {
					preOccPodEntries[podUID] = make(ContainerEntries)
				}

				if _, ok := preOccPodEntries[podUID][containerName]; !ok {
					preOccPodEntries[podUID][containerName] = allocationInfo
				}
			}
		}

		for podUID, containerEntries := range preOccPodEntries {
			for containerName, preOccAllocationInfo := range containerEntries {
				if preOccAllocationInfo == nil {
					general.Warningf("nil preOccAllocationInfo in podEntries")
					continue
				}

				if preoccupation.PreOccAllocationExpired(preOccAllocationInfo.AllocationMeta, now) {
					numaState.DeletePreOccAllocationInfo(podUID, containerName)
				} else {
					preoccupation.SetPreOccDeleteTimestamp(&preOccAllocationInfo.AllocationMeta, now)
					numaState.SetPreOccAllocationInfo(podUID, containerName, preOccAllocationInfo)
				}
			}
		}
	}
}

// GenerateResourceStateFromPodEntries returns NUMANodeMap for given resource based on
// machine info and reserved resources along with existed pod entries
func GenerateResourceStateFromPodEntries(machineInfo *info.MachineInfo,
	podEntries PodEntries, originMachineState NUMANodeMap, reserved map[v1.ResourceName]map[int]uint64, resourceName v1.ResourceName,
) (NUMANodeMap, error) {
	switch resourceName {
	case v1.ResourceMemory:
		currentMachineState, err := GenerateMemoryStateFromPodEntries(machineInfo, podEntries, reserved)
		if err != nil {
			return nil, err
		}

		updateMachineStatePreOccPodEntries(currentMachineState, originMachineState)
		return currentMachineState, nil
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

// GetRequestedQuantityFromPodEntries returns total quantity of reclaim without numa_binding requested
func GetRequestedQuantityFromPodEntries(podEntries PodEntries, allocationFilter func(info *AllocationInfo) bool) int64 {
	var req int64 = 0

	for _, entries := range podEntries {
		for _, allocationInfo := range entries {
			if allocationInfo == nil || !allocationFilter(allocationInfo) {
				continue
			}

			req += int64(allocationInfo.AggregatedQuantity)
		}
	}

	return req
}
