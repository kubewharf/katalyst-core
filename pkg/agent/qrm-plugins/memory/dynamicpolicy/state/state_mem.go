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

// memoryPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type memoryPluginState struct {
	sync.RWMutex

	socketTopology map[int]string
	machineInfo    *info.MachineInfo
	reservedMemory map[v1.ResourceName]map[int]uint64

	machineState       NUMANodeResourcesMap
	podResourceEntries PodResourceEntries
}

var _ State = &memoryPluginState{}

func NewMemoryPluginState(topology *machine.CPUTopology, machineInfo *info.MachineInfo, reservedMemory map[v1.ResourceName]map[int]uint64) (State, error) {
	klog.InfoS("[memory_plugin] initializing new memory plugin in-memory state store")

	socketTopology := make(map[int]string)
	for _, socketID := range topology.CPUDetails.Sockets().ToSliceInt() {
		socketTopology[socketID] = topology.CPUDetails.NUMANodesInSockets(socketID).String()
	}

	defaultMachineState, err := GenerateMachineState(machineInfo, reservedMemory)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
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

func (s *memoryPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState, _ = GenerateMachineState(s.machineInfo, s.reservedMemory)
	s.podResourceEntries = make(PodResourceEntries)
	s.socketTopology = make(map[int]string)

	klog.V(2).InfoS("[memory_plugin] cleared state")
}
