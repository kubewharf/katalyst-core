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

package planner

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// CPUStateCandidate accumulates NUMA-level CPU state updates on top of an
// immutable CPUStateSnapshot.
//
// CPUStateCandidate is not safe for concurrent use. Mutating methods lazily
// clone NUMA nodes before changing them; NUMA nodes that remain clean may be
// shared by reference from the base snapshot when Materialize is called.
type CPUStateCandidate struct {
	snapshot     *CPUStateSnapshot
	podEntries   state.PodEntries
	machineState state.NUMANodeMap
	dirtyPods    bool
	dirtyNUMAs   map[int]struct{}
}

// NewCPUStateCandidate returns a CPUStateCandidate based on snapshot.
//
// The supplied snapshot is treated as immutable for the lifetime of the
// candidate. CPUStateCandidate is not safe for concurrent use.
func NewCPUStateCandidate(snapshot *CPUStateSnapshot) *CPUStateCandidate {
	if snapshot == nil {
		snapshot = &CPUStateSnapshot{}
	}

	return &CPUStateCandidate{
		snapshot:     snapshot,
		podEntries:   snapshot.PodEntries.Clone(),
		machineState: make(state.NUMANodeMap),
		dirtyNUMAs:   make(map[int]struct{}),
	}
}

// UpdatePodEntry updates one pod/container entry in the candidate.
//
// The input AllocationInfo is cloned before being stored. CPUStateCandidate is
// not safe for concurrent use.
func (c *CPUStateCandidate) UpdatePodEntry(podUID, containerName string, allocationInfo *state.AllocationInfo) {
	if c == nil {
		return
	}

	c.mutablePodEntries()
	if c.podEntries[podUID] == nil {
		c.podEntries[podUID] = make(state.ContainerEntries)
	}
	c.podEntries[podUID][containerName] = allocationInfo.Clone()
	c.dirtyPods = true
}

// UpdateNUMADefaultCPUSet updates the default CPU set for numaID.
//
// The input CPU set is cloned before being stored. The target NUMA state is
// lazily initialized when absent, so calling this method on a zero-value
// CPUStateCandidate does not panic. CPUStateCandidate is not safe for
// concurrent use.
func (c *CPUStateCandidate) UpdateNUMADefaultCPUSet(numaID int, defaultCPUSet machine.CPUSet) {
	if c == nil {
		return
	}

	numaState := c.mutableNUMAState(numaID)
	numaState.DefaultCPUSet = defaultCPUSet.Clone()
}

// IsNUMADirty reports whether numaID has been cloned or created by this
// candidate. CPUStateCandidate is not safe for concurrent use.
func (c *CPUStateCandidate) IsNUMADirty(numaID int) bool {
	if c == nil {
		return false
	}

	_, ok := c.dirtyNUMAs[numaID]
	return ok
}

// Materialize builds a CPUStateSnapshot from the candidate's accumulated
// changes.
//
// PodEntries are cloned. Clean NUMA nodes are intentionally shared by reference
// from the immutable base snapshot, while dirty NUMA nodes are cloned so the
// returned snapshot cannot mutate candidate-local dirty state. CPUStateCandidate
// is not safe for concurrent use.
func (c *CPUStateCandidate) Materialize() CPUStateSnapshot {
	if c == nil {
		return CPUStateSnapshot{}
	}

	var snapshot CPUStateSnapshot
	if c.snapshot != nil {
		snapshot = *c.snapshot
	}

	materialized := CPUStateSnapshot{
		InMemoryRevision: snapshot.InMemoryRevision,
		PodEntries:       c.materializedPodEntries(snapshot.PodEntries),
		MachineState:     make(state.NUMANodeMap, len(snapshot.MachineState)+len(c.machineState)),
	}

	for numaID, numaState := range snapshot.MachineState {
		materialized.MachineState[numaID] = numaState
	}
	for numaID, numaState := range c.machineState {
		materialized.MachineState[numaID] = numaState.Clone()
	}

	return materialized
}

func (c *CPUStateCandidate) mutablePodEntries() {
	if c.podEntries == nil {
		if c.snapshot != nil {
			c.podEntries = c.snapshot.PodEntries.Clone()
		}
		if c.podEntries == nil {
			c.podEntries = make(state.PodEntries)
		}
	}
}

func (c *CPUStateCandidate) materializedPodEntries(snapshotEntries state.PodEntries) state.PodEntries {
	if c.dirtyPods {
		return c.podEntries.Clone()
	}
	return snapshotEntries.Clone()
}

func (c *CPUStateCandidate) mutableNUMAState(numaID int) *state.NUMANodeState {
	if c.machineState == nil {
		c.machineState = make(state.NUMANodeMap)
	}
	if c.dirtyNUMAs == nil {
		c.dirtyNUMAs = make(map[int]struct{})
	}

	if numaState, ok := c.machineState[numaID]; ok {
		return numaState
	}

	var numaState *state.NUMANodeState
	if c.snapshot != nil && c.snapshot.MachineState != nil {
		numaState = c.snapshot.MachineState[numaID].Clone()
	}
	if numaState == nil {
		numaState = &state.NUMANodeState{}
	}

	c.machineState[numaID] = numaState
	c.dirtyNUMAs[numaID] = struct{}{}

	return numaState
}
