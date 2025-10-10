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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// gpuPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type gpuPluginState struct {
	sync.RWMutex

	qrmConf             *qrm.QRMPluginsConfiguration
	gpuTopologyProvider machine.GPUTopologyProvider

	machineState GPUMap
	podEntries   PodEntries
}

func NewGPUPluginState(
	conf *qrm.QRMPluginsConfiguration,
	gpuTopologyProvider machine.GPUTopologyProvider,
) (State, error) {
	generalLog.InfoS("initializing new gpu plugin in-memory state store")

	defaultMachineState, err := GenerateMachineState(conf, gpuTopologyProvider)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	return &gpuPluginState{
		qrmConf:             conf,
		machineState:        defaultMachineState,
		gpuTopologyProvider: gpuTopologyProvider,
		podEntries:          make(PodEntries),
	}, nil
}

func (s *gpuPluginState) SetMachineState(gpuMap GPUMap, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.machineState = gpuMap.Clone()
	generalLog.InfoS("updated gpu plugin machine state",
		"GPUMap", gpuMap.String())
}

func (s *gpuPluginState) SetPodEntries(podEntries PodEntries, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.podEntries = podEntries.Clone()
}

func (s *gpuPluginState) SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, _ bool) {
	s.Lock()
	defer s.Unlock()

	s.podEntries.SetAllocationInfo(podUID, containerName, allocationInfo)
	generalLog.InfoS("updated gpu plugin pod resource entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *gpuPluginState) Delete(podUID, containerName string, _ bool) {
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

func (s *gpuPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	machineState, err := GenerateMachineState(s.qrmConf, s.gpuTopologyProvider)
	if err != nil {
		generalLog.ErrorS(err, "failed to generate machine state")
	}
	s.machineState = machineState
	s.podEntries = make(PodEntries)

	generalLog.InfoS("cleared state")
}

func (s *gpuPluginState) StoreState() error {
	// nothing to do
	return nil
}

func (s *gpuPluginState) GetMachineState() GPUMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *gpuPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone()
}

func (s *gpuPluginState) GetAllocationInfo(podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.GetAllocationInfo(podUID, containerName)
}
