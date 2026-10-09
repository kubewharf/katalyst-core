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
	"math"
	"sync"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cpuPluginState is an in-memory implementation of State;
// everytime we want to read or write states, those requests will always
// go to in-memory State, and then go to disk State, i.e. in write-back mode
type cpuPluginState struct {
	sync.RWMutex

	cpuTopology *machine.CPUTopology

	// cpuPluginStateData holds the mutable, lock-free portion of the plugin
	// state (pod entries, machine state, NUMA headroom, overlap flag). The
	// outer cpuPluginState wraps every read with an RLock+Clone and every
	// write with a Lock+Clone to keep its long-standing external contract:
	// callers receive fully-owned copies. The lock-free reader methods
	// promoted from cpuPluginStateData are intentionally shadowed below to
	// preserve those semantics.
	cpuPluginStateData

	socketTopology map[int]string
}

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

func NewCPUPluginState(topology *machine.CPUTopology) *cpuPluginState {
	klog.InfoS("[cpu_plugin] initializing new cpu plugin in-memory state store")
	return &cpuPluginState{
		cpuPluginStateData: cpuPluginStateData{
			podEntries:   make(PodEntries),
			machineState: GetDefaultMachineState(topology),
		},
		socketTopology: topology.GetSocketTopology(),
		cpuTopology:    topology,
	}
}

func (s *cpuPluginState) GetMachineState() NUMANodeMap {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetMachineState().Clone()
}

func (s *cpuPluginState) GetNUMAHeadroom() map[int]float64 {
	s.RLock()
	defer s.RUnlock()

	return general.DeepCopyIntToFloat64Map(s.cpuPluginStateData.GetNUMAHeadroom())
}

func (s *cpuPluginState) GetAllocationInfo(podUID string, containerName string) *AllocationInfo {
	s.RLock()
	defer s.RUnlock()

	allocationInfo := s.cpuPluginStateData.GetAllocationInfo(podUID, containerName)
	if allocationInfo == nil {
		return nil
	}
	return allocationInfo.Clone()
}

func (s *cpuPluginState) GetPodEntries() PodEntries {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetPodEntries().Clone()
}

// GetAdvisorStateSnapshot returns one immutable generation for a synchronous
// advisor request. Entries, machine state, and revision must be captured under
// the same read lock so the response can never be committed as a newer state.
func (s *cpuPluginState) GetAdvisorStateSnapshot() (PodEntries, NUMANodeMap, uint64) {
	s.RLock()
	defer s.RUnlock()

	return s.podEntries.Clone(), s.machineState.Clone(), s.revision
}

func (s *cpuPluginState) revisionExhaustedLocked(operation string) error {
	if s.revision != math.MaxUint64 {
		return nil
	}
	return fmt.Errorf("%s: %w: current=%d", operation, ErrStateRevisionOverflow, s.revision)
}

func (s *cpuPluginState) SetMachineState(numaNodeMap NUMANodeMap) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("SetMachineState"); err != nil {
		return err
	}
	s.machineState = numaNodeMap.Clone()
	s.revision++
	if klog.V(6).Enabled() {
		klog.InfoS("[cpu_plugin] Updated cpu plugin machine state", "numaNodeMap", numaNodeMap.String())
	}
	return nil
}

func (s *cpuPluginState) SetNUMAHeadroom(numaHeadroom map[int]float64) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("SetNUMAHeadroom"); err != nil {
		return err
	}
	s.numaHeadroom = general.DeepCopyIntToFloat64Map(numaHeadroom)
	klog.InfoS("[cpu_plugin] Updated cpu plugin numa headroom", "numaHeadroom", numaHeadroom)
	return nil
}

func (s *cpuPluginState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) error {
	s.Lock()
	defer s.Unlock()
	if allocationInfo == nil {
		return fmt.Errorf("set allocation info for pod %q container %q: allocation info is nil", podUID, containerName)
	}
	if err := s.revisionExhaustedLocked("SetAllocationInfo"); err != nil {
		return err
	}

	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	s.revision++
	klog.InfoS("[cpu_plugin] updated cpu plugin pod entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
	return nil
}

func (s *cpuPluginState) SetPodEntries(podEntries PodEntries) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("SetPodEntries"); err != nil {
		return err
	}
	s.podEntries = podEntries.Clone()
	s.revision++
	if klog.V(6).Enabled() {
		klog.InfoS("[cpu_plugin] Updated cpu plugin pod entries",
			"podEntries", podEntries.String())
	}
	return nil
}

func (s *cpuPluginState) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores bool) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("SetAllowSharedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	klog.InfoS("[cpu_plugin] Updated allowSharedCoresOverlapReclaimedCores",
		"allowSharedCoresOverlapReclaimedCores", allowSharedCoresOverlapReclaimedCores)

	s.allowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	s.revision++
	return nil
}

func (s *cpuPluginState) SetDisableDedicatedCoresOverlapReclaimedCores(disableDedicatedCoresOverlapReclaimedCores bool) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("SetDisableDedicatedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	klog.InfoS("[cpu_plugin] Updated disableDedicatedCoresOverlapReclaimedCores",
		"disableDedicatedCoresOverlapReclaimedCores", disableDedicatedCoresOverlapReclaimedCores)

	s.disableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	s.revision++
	return nil
}

// CommitAdvisorState atomically replaces the state fields produced by one advisor response.
func (s *cpuPluginState) CommitAdvisorState(
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	_ bool,
	_ ...*WritePermit,
) error {
	s.Lock()
	defer s.Unlock()

	if s.revision == math.MaxUint64 {
		return fmt.Errorf("%w: current=%d", ErrStateRevisionOverflow, s.revision)
	}
	s.podEntries = podEntries.Clone()
	s.machineState = machineState.Clone()
	s.allowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	s.disableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	s.revision++
	return nil
}

// CommitAdvisorStateIfRevision atomically replaces advisor-derived state only
// when the caller's snapshot revision still matches current state.
func (s *cpuPluginState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	_ bool,
	_ ...*WritePermit,
) error {
	s.Lock()
	defer s.Unlock()

	if s.revision != expectedRevision {
		return fmt.Errorf("%w: expected=%d actual=%d", ErrStaleStateRevision, expectedRevision, s.revision)
	}
	if s.revision == math.MaxUint64 {
		return fmt.Errorf("%w: current=%d", ErrStateRevisionOverflow, s.revision)
	}

	s.podEntries = podEntries.Clone()
	s.machineState = machineState.Clone()
	s.allowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	s.disableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	s.revision++
	return nil
}

func (s *cpuPluginState) restoreAdvisorState(
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	revision uint64,
) {
	s.Lock()
	defer s.Unlock()

	s.podEntries = podEntries.Clone()
	s.machineState = machineState.Clone()
	s.allowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	s.disableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	s.revision = revision
}

func (s *cpuPluginState) snapshotData() cpuPluginStateData {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.Clone()
}

func (s *cpuPluginState) restoreData(snapshot cpuPluginStateData) {
	s.Lock()
	defer s.Unlock()

	s.cpuPluginStateData = snapshot.Clone()
}

func (s *cpuPluginState) GetAllowSharedCoresOverlapReclaimedCores() bool {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetAllowSharedCoresOverlapReclaimedCores()
}

func (s *cpuPluginState) GetDisableDedicatedCoresOverlapReclaimedCores() bool {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetDisableDedicatedCoresOverlapReclaimedCores()
}

func (s *cpuPluginState) GetRevision() uint64 {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetRevision()
}

func (s *cpuPluginState) Delete(podUID string, containerName string) error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("Delete"); err != nil {
		return err
	}
	if _, ok := s.podEntries[podUID]; !ok {
		return nil
	}

	delete(s.podEntries[podUID], containerName)
	if len(s.podEntries[podUID]) == 0 {
		delete(s.podEntries, podUID)
	}
	s.revision++
	klog.V(2).InfoS("[cpu_plugin] deleted container entry",
		"podUID", podUID,
		"containerName", containerName)
	return nil
}

func (s *cpuPluginState) ClearState() error {
	s.Lock()
	defer s.Unlock()

	if err := s.revisionExhaustedLocked("ClearState"); err != nil {
		return err
	}
	s.machineState = GetDefaultMachineState(s.cpuTopology)
	s.socketTopology = s.cpuTopology.GetSocketTopology()
	s.podEntries = make(PodEntries)
	s.revision++
	klog.V(2).InfoS("[cpu_plugin] cleared state")
	return nil
}
