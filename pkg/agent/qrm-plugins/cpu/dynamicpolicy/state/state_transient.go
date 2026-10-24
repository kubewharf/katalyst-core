/*
Copyright 2026 The Katalyst Authors.

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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// transientState adapts the in-memory store to State for speculative planning.
// Persistence flags are intentionally ignored and StoreState is a no-op.
type transientState struct {
	*cpuPluginState
	writeGate WriteGate
}

func NewTransientState(topology *machine.CPUTopology) State {
	return &transientState{cpuPluginState: NewCPUPluginState(topology)}
}

func (s *transientState) SetWritePermit(gate WriteGate) {
	s.writeGate = gate
}

func (s *transientState) permitWrite(operation string, permits ...*WritePermit) error {
	if len(permits) > 1 {
		return fmt.Errorf("%s received %d write permits", operation, len(permits))
	}
	var permit *WritePermit
	if len(permits) == 1 {
		permit = permits[0]
	}
	if permit != nil && permit.consumed {
		return fmt.Errorf("%s write permit was already consumed", operation)
	}
	if s.writeGate == nil {
		if permit != nil {
			return fmt.Errorf("%s write permit has no installed gate", operation)
		}
		return nil
	}
	if err := s.writeGate(s.GetRevision(), operation, permit); err != nil {
		return err
	}
	if permit != nil {
		permit.consumed = true
	}
	return nil
}

func (s *transientState) SetMachineState(v NUMANodeMap, _ bool) error {
	if err := s.permitWrite("SetMachineState"); err != nil {
		return err
	}
	return s.cpuPluginState.SetMachineState(v)
}

func (s *transientState) SetNUMAHeadroom(v map[int]float64, _ bool) error {
	return s.SetNUMAHeadroomWithPermit(v, false, nil)
}

func (s *transientState) SetNUMAHeadroomWithPermit(
	v map[int]float64,
	_ bool,
	permit *WritePermit,
) error {
	if err := s.permitWrite("SetNUMAHeadroom", permit); err != nil {
		return err
	}
	return s.cpuPluginState.SetNUMAHeadroom(v)
}

func (s *transientState) SetPodEntries(v PodEntries, _ bool) error {
	if err := s.permitWrite("SetPodEntries"); err != nil {
		return err
	}
	return s.cpuPluginState.SetPodEntries(v)
}

func (s *transientState) SetAllocationInfo(podUID, containerName string, allocation *AllocationInfo, _ bool) error {
	if err := s.permitWrite("SetAllocationInfo"); err != nil {
		return err
	}
	return s.cpuPluginState.SetAllocationInfo(podUID, containerName, allocation)
}

func (s *transientState) SetAllowSharedCoresOverlapReclaimedCores(v bool, _ bool) error {
	if err := s.permitWrite("SetAllowSharedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	return s.cpuPluginState.SetAllowSharedCoresOverlapReclaimedCores(v)
}

func (s *transientState) SetDisableDedicatedCoresOverlapReclaimedCores(v bool, _ bool) error {
	if err := s.permitWrite("SetDisableDedicatedCoresOverlapReclaimedCores"); err != nil {
		return err
	}
	return s.cpuPluginState.SetDisableDedicatedCoresOverlapReclaimedCores(v)
}

func (s *transientState) CommitAdvisorState(
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, _ bool,
	permits ...*WritePermit,
) error {
	if err := s.permitWrite("CommitAdvisorState", permits...); err != nil {
		return err
	}
	return s.cpuPluginState.CommitAdvisorState(
		podEntries, machineState, allowOverlap, disableDedicatedOverlap, false)
}

func (s *transientState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries PodEntries,
	machineState NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, _ bool,
	permits ...*WritePermit,
) error {
	if err := s.permitWrite("CommitAdvisorStateIfRevision", permits...); err != nil {
		return err
	}
	return s.cpuPluginState.CommitAdvisorStateIfRevision(
		expectedRevision, podEntries, machineState, allowOverlap, disableDedicatedOverlap, false)
}

func (s *transientState) Delete(podUID, containerName string, _ bool) error {
	if err := s.permitWrite("Delete"); err != nil {
		return err
	}
	return s.cpuPluginState.Delete(podUID, containerName)
}

func (s *transientState) ClearState() error {
	if err := s.permitWrite("ClearState"); err != nil {
		return err
	}
	return s.cpuPluginState.ClearState()
}

func (s *transientState) StoreState() error {
	return nil
}
