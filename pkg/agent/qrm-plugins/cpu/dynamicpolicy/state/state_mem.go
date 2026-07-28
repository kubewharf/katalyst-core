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
	"reflect"
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
	stateRevision  uint64
	advisorEpoch   uint64
	adviceSequence uint64
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

func (s *cpuPluginState) GetStateRevision() uint64 {
	s.RLock()
	defer s.RUnlock()
	return s.stateRevision
}

func (s *cpuPluginState) GetCommittedStateSnapshot() CommittedStateSnapshot {
	s.RLock()
	defer s.RUnlock()
	return CommittedStateSnapshot{
		StateRevision:                         s.stateRevision,
		MachineState:                          s.machineState.Clone(),
		NUMAHeadroom:                          general.DeepCopyIntToFloat64Map(s.numaHeadroom),
		PodEntries:                            s.podEntries.Clone(),
		AllowSharedCoresOverlapReclaimedCores: s.allowSharedCoresOverlapReclaimedCores,
		DisableDedicatedCoresOverlapReclaimedCores: s.disableDedicatedCoresOverlapReclaimedCores,
	}
}

func (s *cpuPluginState) mutateCommittedState(mutator func()) {
	s.Lock()
	defer s.Unlock()
	mutator()
}

func (s *cpuPluginState) checkpointLocked() *CPUPluginCheckpoint {
	checkpoint := NewCPUPluginCheckpoint()
	checkpoint.MachineState = s.machineState.Clone()
	checkpoint.NUMAHeadroom = general.DeepCopyIntToFloat64Map(s.numaHeadroom)
	checkpoint.PodEntries = s.podEntries.Clone()
	checkpoint.AllowSharedCoresOverlapReclaimedCores = s.allowSharedCoresOverlapReclaimedCores
	checkpoint.DisableDedicatedCoresOverlapReclaimedCores = s.disableDedicatedCoresOverlapReclaimedCores
	checkpoint.StateRevision = s.stateRevision
	checkpoint.AdvisorEpoch = s.advisorEpoch
	checkpoint.AdviceSequence = s.adviceSequence
	return checkpoint
}

func (s *cpuPluginState) applyCheckpointLocked(checkpoint *CPUPluginCheckpoint) {
	s.machineState = checkpoint.MachineState.Clone()
	s.numaHeadroom = general.DeepCopyIntToFloat64Map(checkpoint.NUMAHeadroom)
	s.podEntries = checkpoint.PodEntries.Clone()
	s.allowSharedCoresOverlapReclaimedCores = checkpoint.AllowSharedCoresOverlapReclaimedCores
	s.disableDedicatedCoresOverlapReclaimedCores = checkpoint.DisableDedicatedCoresOverlapReclaimedCores
	s.advisorEpoch = checkpoint.AdvisorEpoch
	s.adviceSequence = checkpoint.AdviceSequence
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

func (s *cpuPluginState) SetMachineState(numaNodeMap NUMANodeMap) {
	s.Lock()
	defer s.Unlock()

	if reflect.DeepEqual(s.machineState, numaNodeMap) {
		return
	}
	s.machineState = numaNodeMap.Clone()
	s.stateRevision++
	if klog.V(6).Enabled() {
		klog.InfoS("[cpu_plugin] Updated cpu plugin machine state", "numaNodeMap", numaNodeMap.String())
	}
}

func (s *cpuPluginState) SetNUMAHeadroom(numaHeadroom map[int]float64) {
	s.Lock()
	defer s.Unlock()

	if reflect.DeepEqual(s.numaHeadroom, numaHeadroom) {
		return
	}
	s.numaHeadroom = general.DeepCopyIntToFloat64Map(numaHeadroom)
	s.stateRevision++
	klog.InfoS("[cpu_plugin] Updated cpu plugin numa headroom", "numaHeadroom", numaHeadroom)
}

func (s *cpuPluginState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	s.Lock()
	defer s.Unlock()
	if allocationInfo == nil {
		general.Warningf("skip setting nil allocation info for pod %s container %s", podUID, containerName)
		return
	}
	if entries, ok := s.podEntries[podUID]; ok && reflect.DeepEqual(entries[containerName], allocationInfo) {
		return
	}

	if _, ok := s.podEntries[podUID]; !ok {
		s.podEntries[podUID] = make(ContainerEntries)
	}

	s.podEntries[podUID][containerName] = allocationInfo.Clone()
	s.stateRevision++
	klog.InfoS("[cpu_plugin] updated cpu plugin pod entries",
		"podUID", podUID,
		"containerName", containerName,
		"allocationInfo", allocationInfo.String())
}

func (s *cpuPluginState) SetPodEntries(podEntries PodEntries) {
	s.Lock()
	defer s.Unlock()

	if reflect.DeepEqual(s.podEntries, podEntries) {
		return
	}
	s.podEntries = podEntries.Clone()
	s.stateRevision++
	if klog.V(6).Enabled() {
		klog.InfoS("[cpu_plugin] Updated cpu plugin pod entries",
			"podEntries", podEntries.String())
	}
}

// CommitState atomically updates the pod entries and machine state in memory.
// Persistence and rollback are owned by stateCheckpoint, which delegates this
// single in-memory mutation before storing its checkpoint.
func (s *cpuPluginState) CommitState(
	podEntries PodEntries, machineState NUMANodeMap, expectedRevision uint64, _ bool,
) error {
	s.Lock()
	defer s.Unlock()

	if s.stateRevision != expectedRevision {
		return fmt.Errorf("%w: expected %d, current %d", ErrStateRevisionConflict, expectedRevision, s.stateRevision)
	}
	if reflect.DeepEqual(s.podEntries, podEntries) && reflect.DeepEqual(s.machineState, machineState) {
		return nil
	}
	s.podEntries = podEntries.Clone()
	s.machineState = machineState.Clone()
	s.stateRevision++
	return nil
}

func (s *cpuPluginState) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores bool) {
	s.Lock()
	defer s.Unlock()

	klog.InfoS("[cpu_plugin] Updated allowSharedCoresOverlapReclaimedCores",
		"allowSharedCoresOverlapReclaimedCores", allowSharedCoresOverlapReclaimedCores)

	if s.allowSharedCoresOverlapReclaimedCores == allowSharedCoresOverlapReclaimedCores {
		return
	}
	s.allowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	s.stateRevision++
}

func (s *cpuPluginState) GetAllowSharedCoresOverlapReclaimedCores() bool {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetAllowSharedCoresOverlapReclaimedCores()
}

func (s *cpuPluginState) SetDisableDedicatedCoresOverlapReclaimedCores(disableDedicatedCoresOverlapReclaimedCores bool) {
	s.Lock()
	defer s.Unlock()

	klog.InfoS("[cpu_plugin] Updated disableDedicatedCoresOverlapReclaimedCores",
		"disableDedicatedCoresOverlapReclaimedCores", disableDedicatedCoresOverlapReclaimedCores)

	if s.disableDedicatedCoresOverlapReclaimedCores == disableDedicatedCoresOverlapReclaimedCores {
		return
	}
	s.disableDedicatedCoresOverlapReclaimedCores = disableDedicatedCoresOverlapReclaimedCores
	s.stateRevision++
}

func (s *cpuPluginState) GetDisableDedicatedCoresOverlapReclaimedCores() bool {
	s.RLock()
	defer s.RUnlock()

	return s.cpuPluginStateData.GetDisableDedicatedCoresOverlapReclaimedCores()
}

func (s *cpuPluginState) Delete(podUID string, containerName string) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.podEntries[podUID]; !ok {
		return
	}
	if _, ok := s.podEntries[podUID][containerName]; !ok {
		return
	}

	delete(s.podEntries[podUID], containerName)
	if len(s.podEntries[podUID]) == 0 {
		delete(s.podEntries, podUID)
	}
	s.stateRevision++
	klog.V(2).InfoS("[cpu_plugin] deleted container entry",
		"podUID", podUID,
		"containerName", containerName)
}

func (s *cpuPluginState) ClearState() {
	s.Lock()
	defer s.Unlock()

	defaultMachineState := GetDefaultMachineState(s.cpuTopology)
	if reflect.DeepEqual(s.machineState, defaultMachineState) && len(s.podEntries) == 0 {
		return
	}
	s.machineState = defaultMachineState
	s.socketTopology = s.cpuTopology.GetSocketTopology()
	s.podEntries = make(PodEntries)
	s.stateRevision++
	klog.V(2).InfoS("[cpu_plugin] cleared state")
}
