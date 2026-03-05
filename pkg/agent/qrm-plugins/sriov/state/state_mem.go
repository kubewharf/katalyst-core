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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type stateMemory struct {
	sync.RWMutex

	machineState VFState
	podEntries   PodEntries
}

func NewStateMemory(conf *global.MachineInfoConfiguration, allNics []machine.InterfaceInfo) (*stateMemory, error) {
	vfList, err := machine.GetSriovVFList(conf, allNics)
	if err != nil {
		return nil, fmt.Errorf("failed to get network vf list: %v", err)
	}

	state := &stateMemory{
		machineState: make(VFState, 0, len(vfList)),
		podEntries:   make(PodEntries),
	}

	for _, vf := range vfList {
		state.machineState = append(state.machineState, VFInfo{
			RepName:  vf.RepName,
			Index:    vf.Index,
			PCIAddr:  vf.PCIAddr,
			PFName:   vf.PFInfo.Name,
			NumaNode: vf.PFInfo.NumaNode,
			NSName:   vf.PFInfo.NSName,
		})
	}

	state.machineState.Sort()

	return state, nil
}

func (s *stateMemory) SetMachineState(state VFState) {
	s.Lock()
	defer s.Unlock()

	s.machineState = state
}

func (s *stateMemory) SetPodEntries(podEntries PodEntries) {
	s.Lock()
	defer s.Unlock()

	s.podEntries = podEntries
}

func (s *stateMemory) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()
	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	generalLog.InfoS("updated sriov plugin pod resource entries",
		"podUID", podUID, "containerName", containerName, "allocationInfo", allocationInfo.String())
}

func (s *stateMemory) Delete(podUID string) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		return
	}

	delete(s.podEntries, podUID)
	generalLog.InfoS("deleted container entry", "podUID", podUID)
}

func (s *stateMemory) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState = make(VFState, 0)
	s.podEntries = make(PodEntries)

	generalLog.InfoS("cleared state")
}

func (s *stateMemory) GetMachineState() VFState {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *stateMemory) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *stateMemory) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	if res, ok := s.podEntries[podUID][containerName]; ok {
		return res.Clone()
	}
	return nil
}
