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

	info "github.com/google/cadvisor/info/v1"
)

type sriovPluginState struct {
	sync.RWMutex

	machineInfo  *info.MachineInfo
	machineState VfState
	podEntries   PodEntries
}

func NewSriovPluginState(machineInfo *info.MachineInfo) (*sriovPluginState, error) {
	return &sriovPluginState{
		machineInfo: machineInfo,
	}, nil
}

func (s *sriovPluginState) SetMachineState(state VfState) {
	s.Lock()
	defer s.Unlock()

	s.machineState = state
}

func (s *sriovPluginState) SetPodEntries(podEntries PodEntries) {
	s.Lock()
	defer s.Unlock()

	s.podEntries = podEntries
}

func (s *sriovPluginState) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()
	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	generalLog.InfoS("updated network plugin pod resource entries",
		"podUID", podUID, "allocationInfo", allocationInfo.String())
}

func (s *sriovPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState = make(VfState)
	s.podEntries = make(PodEntries)

	generalLog.InfoS("cleared state")
}

func (s *sriovPluginState) GetMachineState() VfState {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *sriovPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *sriovPluginState) GetMachineInfo() *info.MachineInfo {
	s.RLock()
	defer s.RUnlock()

	return s.machineInfo.Clone()
}
