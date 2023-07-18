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
	"sync"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cpuPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type cpuPluginState struct {
	sync.RWMutex

	cpuTopology *machine.CPUTopology

	podEntries     PodEntries
	machineState   NUMANodeMap
	socketTopology map[int]string
}

var _ State = &cpuPluginState{}

func GetDefaultMachineState(topology *machine.CPUTopology) NUMANodeMap {
	if topology == nil {
		return nil
	}

	defaultMachineState := make(NUMANodeMap)
	for _, numaNode := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		defaultMachineState[numaNode] = &NUMANodeState{
			DefaultCPUSet:   topology.CPUDetails.CPUsInNUMANodes(numaNode).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries:      make(PodEntries),
		}
	}
	return defaultMachineState
}

func NewCPUPluginState(topology *machine.CPUTopology) State {
	klog.InfoS("[cpu_plugin] initializing new cpu plugin in-memory state store")
	return &cpuPluginState{
		podEntries:     make(PodEntries),
		machineState:   GetDefaultMachineState(topology),
		socketTopology: topology.GetSocketTopology(),
		cpuTopology:    topology,
	}
}

func (s *cpuPluginState) GetMachineState() NUMANodeMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *cpuPluginState) GetAllocationInfo(podUID string, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	if res, ok := s.podEntries[podUID][containerName]; ok {
		return res.Clone()
	}
	return nil
}

func (s *cpuPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *cpuPluginState) SetMachineState(numaNodeMap NUMANodeMap) {
	s.Lock()
	defer s.Unlock()

	s.machineState = numaNodeMap.Clone()
	klog.InfoS("[cpu_plugin] Updated cpu plugin machine state", "numaNodeMap", numaNodeMap.String())
}

func (s *cpuPluginState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	klog.InfoS("[cpu_plugin] updated cpu plugin pod entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *cpuPluginState) SetPodEntries(podEntries PodEntries) {
	s.Lock()
	defer s.Unlock()

	s.podEntries = podEntries.Clone()
	klog.InfoS("[cpu_plugin] Updated cpu plugin pod entries",
		"podEntries", podEntries.String())
}

func (s *cpuPluginState) Delete(podUID string, containerName string) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		return
	}

	delete(s.podEntries[podUID], containerName)
	if len(s.podEntries[podUID]) == 0 {
		delete(s.podEntries, podUID)
	}
	klog.V(2).InfoS("[cpu_plugin] deleted container entry",
		"podUID", podUID,
		"containerName", containerName)
}

func (s *cpuPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState = GetDefaultMachineState(s.cpuTopology)
	s.socketTopology = s.cpuTopology.GetSocketTopology()
	s.podEntries = make(PodEntries)
	klog.V(2).InfoS("[cpu_plugin] cleared state")
}
