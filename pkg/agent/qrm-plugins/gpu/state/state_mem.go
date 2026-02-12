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

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

// gpuPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type gpuPluginState struct {
	sync.RWMutex

	qrmConf                        *qrm.QRMPluginsConfiguration
	defaultResourceStateGenerators *DefaultResourceStateGeneratorRegistry

	machineState       AllocationResourcesMap
	podResourceEntries PodResourceEntries
}

func NewGPUPluginState(
	conf *qrm.QRMPluginsConfiguration,
	resourceStateGeneratorRegistry *DefaultResourceStateGeneratorRegistry,
) (State, error) {
	generalLog.InfoS("initializing new gpu plugin in-memory state store")

	defaultMachineState, err := GenerateMachineState(resourceStateGeneratorRegistry)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %w", err)
	}

	return &gpuPluginState{
		qrmConf:                        conf,
		machineState:                   defaultMachineState,
		defaultResourceStateGenerators: resourceStateGeneratorRegistry,
		podResourceEntries:             make(PodResourceEntries),
	}, nil
}

func (s *gpuPluginState) SetMachineState(allocationResourcesMap AllocationResourcesMap, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.machineState = allocationResourcesMap.Clone()
	generalLog.InfoS("updated gpu plugin machine state",
		"GPUMap", allocationResourcesMap.String())
}

func (s *gpuPluginState) SetResourceState(resourceName v1.ResourceName, allocationMap AllocationMap, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.machineState[resourceName] = allocationMap.Clone()
	generalLog.InfoS("updated gpu plugin resource state",
		"resourceName", resourceName,
		"allocationMap", allocationMap.String())
}

func (s *gpuPluginState) SetPodResourceEntries(podResourceEntries PodResourceEntries, _ bool) {
	s.Lock()
	defer s.Unlock()
	s.podResourceEntries = podResourceEntries.Clone()
}

func (s *gpuPluginState) SetAllocationInfo(
	resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo, _ bool,
) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podResourceEntries[resourceName]; !ok {
		s.podResourceEntries[resourceName] = make(PodEntries)
	}

	if _, ok := s.podResourceEntries[resourceName][podUID]; !ok {
		s.podResourceEntries[resourceName][podUID] = make(ContainerEntries)
	}

	s.podResourceEntries[resourceName][podUID][containerName] = allocationInfo.Clone()
	generalLog.InfoS("updated gpu plugin pod resource entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *gpuPluginState) Delete(resourceName v1.ResourceName, podUID, containerName string, _ bool) {
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

	generalLog.InfoS("deleted container entry", "podUID", podUID, "containerName", containerName)
}

func (s *gpuPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	machineState, err := GenerateMachineState(s.defaultResourceStateGenerators)
	if err != nil {
		generalLog.ErrorS(err, "failed to generate machine state")
	}
	s.machineState = machineState
	s.podResourceEntries = make(PodResourceEntries)

	generalLog.InfoS("cleared state")
}

func (s *gpuPluginState) StoreState() error {
	// nothing to do
	return nil
}

func (s *gpuPluginState) GetMachineState() AllocationResourcesMap {
	s.RLock()
	defer s.RUnlock()

	return s.machineState.Clone()
}

func (s *gpuPluginState) GetPodResourceEntries() PodResourceEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podResourceEntries.Clone()
}

func (s *gpuPluginState) GetPodEntries(resourceName v1.ResourceName) PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.podResourceEntries[resourceName].Clone()
}

func (s *gpuPluginState) GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	if res, ok := s.podResourceEntries[resourceName][podUID][containerName]; ok {
		return res.Clone()
	}

	return nil
}
