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
	"sync"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cpuPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type memoryPluginState struct {
	sync.RWMutex
	podResourceEntries PodResourceEntries
	machineState       NUMANodeResourcesMap
	socketTopology     map[int]string
	machineInfo        *info.MachineInfo
	reservedMemory     map[v1.ResourceName]map[int]uint64
}

var _ State = &memoryPluginState{}

func GetDefaultResourcesMachineState(machineInfo *info.MachineInfo,
	reservedMemory map[v1.ResourceName]map[int]uint64) (NUMANodeResourcesMap, error) {

	if machineInfo == nil {
		return nil, fmt.Errorf("GetDefaultResourcesMachineState got nil machineInfo")
	}

	// todo: currently only support memory, we will support huge page later.
	defaultResourcesMachineState := make(NUMANodeResourcesMap)
	for _, resourceName := range []v1.ResourceName{v1.ResourceMemory} {
		machineState, err := GetDefaultMachineState(machineInfo, reservedMemory, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GetDefaultMachineState for resource: %s failed with error: %v", resourceName, err)
		}

		defaultResourcesMachineState[resourceName] = machineState
	}
	return defaultResourcesMachineState, nil
}

func GetDefaultMachineState(machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64, resourceName v1.ResourceName) (NUMANodeMap, error) {
	defaultMachineState := make(NUMANodeMap)

	switch resourceName {
	case v1.ResourceMemory:
		for _, node := range machineInfo.Topology {
			totalMemSizeQuantity := node.Memory
			numaReservedMemQuantity := reservedMemory[resourceName][node.Id]

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

func NewMemoryPluginState(topology *machine.CPUTopology, machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64) (State, error) {
	klog.InfoS("[memory_plugin] initializing new memory plugin in-memory state store")

	socketTopology := make(map[int]string)
	for _, socketID := range topology.CPUDetails.Sockets().ToSliceInt() {
		socketTopology[socketID] = topology.CPUDetails.NUMANodesInSockets(socketID).String()
	}

	defaultMachineState, err := GetDefaultResourcesMachineState(machineInfo, reservedMemory)
	if err != nil {
		return nil, fmt.Errorf("GetDefaultResourcesMachineState failed with error: %v", err)
	}

	return &memoryPluginState{
		podResourceEntries: make(PodResourceEntries),
		machineState:       defaultMachineState,
		socketTopology:     socketTopology,
		machineInfo:        machineInfo.Clone(),
		reservedMemory:     reservedMemory,
	}, nil
}

func (s *memoryPluginState) GetReservedMemory() map[v1.ResourceName]map[int]uint64 {
	s.RLock()
	defer s.RUnlock()

	clonedReservedMemory := make(map[v1.ResourceName]map[int]uint64)
	for resourceName, numaReserved := range s.reservedMemory {
		clonedReservedMemory[resourceName] = make(map[int]uint64)

		for numaId, reservedQuantity := range numaReserved {
			clonedReservedMemory[resourceName][numaId] = reservedQuantity
		}
	}

	return clonedReservedMemory
}

func (s *memoryPluginState) GetMachineState() NUMANodeResourcesMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *memoryPluginState) GetMachineInfo() *info.MachineInfo {
	s.RLock()
	defer s.RUnlock()

	return s.machineInfo.Clone()
}

func (s *memoryPluginState) GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	if res, ok := s.podResourceEntries[resourceName][podUID][containerName]; ok {
		return res.Clone()
	}
	return nil
}

func (s *memoryPluginState) GetPodResourceEntries() PodResourceEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podResourceEntries.Clone()
}

func (s *memoryPluginState) SetMachineState(numaNodeResourcesMap NUMANodeResourcesMap) {
	s.Lock()
	defer s.Unlock()

	s.machineState = numaNodeResourcesMap.Clone()
	klog.InfoS("[memory_plugin] Updated memory plugin machine state",
		"numaNodeResourcesMap", numaNodeResourcesMap.String())
}

func (s *memoryPluginState) SetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podResourceEntries[resourceName]; !ok {
		s.podResourceEntries[resourceName] = make(PodEntries)
	}

	if _, ok := s.podResourceEntries[resourceName][podUID]; !ok {
		s.podResourceEntries[resourceName][podUID] = make(ContainerEntries)
	}

	s.podResourceEntries[resourceName][podUID][containerName] = allocationInfo.Clone()
	klog.InfoS("[memory_plugin] updated memory plugin pod resource entries",
		"resourceName", resourceName,
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *memoryPluginState) SetPodResourceEntries(podResourceEntries PodResourceEntries) {
	s.Lock()
	defer s.Unlock()

	s.podResourceEntries = podResourceEntries.Clone()
	klog.InfoS("[memory_plugin] Updated memory plugin pod resource entries",
		"podResourceEntries", podResourceEntries.String())
}

// Delete deletes corresponding Blocks from ContainerMemoryAssignments
func (s *memoryPluginState) Delete(resourceName v1.ResourceName, podUID, containerName string) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podResourceEntries[resourceName]; !ok {
		return
	}

	if _, ok := s.podResourceEntries[resourceName][podUID]; !ok {
		return
	}

	delete(s.podResourceEntries[resourceName][podUID], containerName)
	if len(s.podResourceEntries[resourceName][podUID]) == 0 {
		delete(s.podResourceEntries[resourceName], podUID)
	}
	klog.V(2).InfoS("[memory_plugin] deleted container entry", "resourceName", resourceName, "podUID", podUID, "containerName", containerName)
}

// ClearState clears machineState and ContainerMemoryAssignments
func (s *memoryPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState, _ = GetDefaultResourcesMachineState(s.machineInfo, s.reservedMemory)
	s.podResourceEntries = make(PodResourceEntries)
	s.socketTopology = make(map[int]string)

	klog.V(2).InfoS("[memory_plugin] cleared state")
}
