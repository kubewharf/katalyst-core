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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// networkPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type networkPluginState struct {
	sync.RWMutex

	qrmConf           *qrm.QRMPluginsConfiguration
	machineInfo       *info.MachineInfo
	nics              []machine.InterfaceInfo
	reservedBandwidth map[string]uint32

	machineState NICMap
	podEntries   PodEntries
}

func NewNetworkPluginState(conf *qrm.QRMPluginsConfiguration, machineInfo *info.MachineInfo, nics []machine.InterfaceInfo, reservedBandwidth map[string]uint32) (*networkPluginState, error) {
	generalLog.InfoS("initializing new network plugin in-memory state store")

	defaultMachineState, err := GenerateMachineState(conf, nics, reservedBandwidth)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	return &networkPluginState{
		qrmConf:           conf,
		machineState:      defaultMachineState,
		machineInfo:       machineInfo.Clone(),
		reservedBandwidth: reservedBandwidth,
		podEntries:        make(PodEntries),
	}, nil
}

func (s *networkPluginState) GetReservedBandwidth() map[string]uint32 {
	s.RLock()
	defer s.RUnlock()

	clonedReservedBandwidth := make(map[string]uint32)
	for iface, bandwidth := range s.reservedBandwidth {
		clonedReservedBandwidth[iface] = bandwidth
	}

	return clonedReservedBandwidth
}

func (s *networkPluginState) GetMachineState() NICMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *networkPluginState) GetMachineInfo() *info.MachineInfo {
	s.RLock()
	defer s.RUnlock()

	return s.machineInfo.Clone()
}

func (s *networkPluginState) GetEnabledNICs() []machine.InterfaceInfo {
	s.RLock()
	defer s.RUnlock()

	clonedNics := make([]machine.InterfaceInfo, len(s.nics))
	copy(clonedNics, s.nics)

	return clonedNics
}

func (s *networkPluginState) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	if res, ok := s.podEntries[podUID][containerName]; ok {
		return res.Clone()
	}
	return nil
}

func (s *networkPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *networkPluginState) SetMachineState(nicMap NICMap) {
	s.Lock()
	defer s.Unlock()

	s.machineState = nicMap.Clone()
	generalLog.InfoS("updated network plugin machine state",
		"NICMap", nicMap.String())
}

func (s *networkPluginState) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	generalLog.InfoS("updated network plugin pod resource entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *networkPluginState) SetPodEntries(podEntries PodEntries) {
	s.Lock()
	defer s.Unlock()

	s.podEntries = podEntries.Clone()
	generalLog.InfoS("updated network plugin pod resource entries",
		"podEntries", podEntries.String())
}

func (s *networkPluginState) Delete(podUID, containerName string) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		return
	}

	delete(s.podEntries[podUID], containerName)
	if len(s.podEntries[podUID]) == 0 {
		delete(s.podEntries, podUID)
	}
	generalLog.InfoS("deleted container entry", "podUID", podUID, "containerName", containerName)
}

func (s *networkPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	s.machineState, _ = GenerateMachineState(s.qrmConf, s.nics, s.reservedBandwidth)
	s.podEntries = make(PodEntries)

	generalLog.InfoS("cleared state")
}
