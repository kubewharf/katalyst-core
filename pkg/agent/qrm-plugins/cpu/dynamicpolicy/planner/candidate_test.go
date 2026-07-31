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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCPUStateCandidateMaterializeSharesCleanNUMAAndClonesDirtyNUMAByContract(t *testing.T) {
	t.Parallel()

	dirtyNUMA := &state.NUMANodeState{
		DefaultCPUSet:   machine.NewCPUSet(0, 1),
		AllocatedCPUSet: machine.NewCPUSet(2),
	}
	cleanNUMA := &state.NUMANodeState{
		DefaultCPUSet:   machine.NewCPUSet(4, 5),
		AllocatedCPUSet: machine.NewCPUSet(6),
	}
	snapshot := &CPUStateSnapshot{
		InMemoryRevision: 7,
		PodEntries:       state.PodEntries{},
		MachineState: state.NUMANodeMap{
			0: dirtyNUMA,
			1: cleanNUMA,
		},
	}
	candidate := NewCPUStateCandidate(snapshot)
	updatedCPUSet := machine.NewCPUSet(8, 9)

	candidate.UpdateNUMADefaultCPUSet(0, updatedCPUSet)
	materialized := candidate.Materialize()

	require.True(t, candidate.IsNUMADirty(0))
	require.False(t, candidate.IsNUMADirty(1))
	require.Equal(t, uint64(7), materialized.InMemoryRevision)
	require.NotSame(t, dirtyNUMA, materialized.MachineState[0])
	require.Same(t, cleanNUMA, materialized.MachineState[1])
	require.Equal(t, machine.NewCPUSet(8, 9), materialized.MachineState[0].DefaultCPUSet)
	require.Equal(t, machine.NewCPUSet(0, 1), snapshot.MachineState[0].DefaultCPUSet)
	require.Equal(t, machine.NewCPUSet(4, 5), snapshot.MachineState[1].DefaultCPUSet)
}

func TestCPUStateCandidateUpdateNUMADefaultCPUSetLazilyInitializesZeroValue(t *testing.T) {
	t.Parallel()

	candidate := &CPUStateCandidate{}

	require.NotPanics(t, func() {
		candidate.UpdateNUMADefaultCPUSet(0, machine.NewCPUSet(1, 2))
	})
	require.True(t, candidate.IsNUMADirty(0))

	materialized := candidate.Materialize()
	require.Equal(t, machine.NewCPUSet(1, 2), materialized.MachineState[0].DefaultCPUSet)
}

func TestCPUStateCandidateUpdateNUMADefaultCPUSetAddsNUMAWhenMachineStateNil(t *testing.T) {
	t.Parallel()

	snapshot := &CPUStateSnapshot{
		MachineState: nil,
	}
	candidate := NewCPUStateCandidate(snapshot)

	candidate.UpdateNUMADefaultCPUSet(1, machine.NewCPUSet(3, 4))
	materialized := candidate.Materialize()

	require.True(t, candidate.IsNUMADirty(1))
	require.NotNil(t, materialized.MachineState[1])
	require.Equal(t, machine.NewCPUSet(3, 4), materialized.MachineState[1].DefaultCPUSet)
	require.Nil(t, snapshot.MachineState)
}

func TestCPUStateCandidateUpdateNUMADefaultCPUSetHandlesNilNUMA(t *testing.T) {
	t.Parallel()

	snapshot := &CPUStateSnapshot{
		MachineState: state.NUMANodeMap{
			0: nil,
		},
	}
	candidate := NewCPUStateCandidate(snapshot)

	candidate.UpdateNUMADefaultCPUSet(0, machine.NewCPUSet(1, 2))
	materialized := candidate.Materialize()

	require.True(t, candidate.IsNUMADirty(0))
	require.NotNil(t, materialized.MachineState[0])
	require.Equal(t, machine.NewCPUSet(1, 2), materialized.MachineState[0].DefaultCPUSet)
	require.Nil(t, snapshot.MachineState[0])
}

func TestCPUStateCandidateUpdateNUMADefaultCPUSetClonesInputCPUSet(t *testing.T) {
	t.Parallel()

	snapshot := &CPUStateSnapshot{
		MachineState: state.NUMANodeMap{
			0: {
				DefaultCPUSet: machine.NewCPUSet(0, 1),
			},
		},
	}
	candidate := NewCPUStateCandidate(snapshot)
	updatedCPUSet := machine.NewCPUSet(2, 3)

	candidate.UpdateNUMADefaultCPUSet(0, updatedCPUSet)
	updatedCPUSet.Add(4)
	materialized := candidate.Materialize()

	require.Equal(t, machine.NewCPUSet(2, 3), materialized.MachineState[0].DefaultCPUSet)
	require.Equal(t, machine.NewCPUSet(0, 1), snapshot.MachineState[0].DefaultCPUSet)
}

func TestCPUStateCandidateMaterializeReturnsIndependentDirtyClone(t *testing.T) {
	t.Parallel()

	snapshot := &CPUStateSnapshot{
		MachineState: state.NUMANodeMap{
			0: {
				DefaultCPUSet:   machine.NewCPUSet(0, 1),
				AllocatedCPUSet: machine.NewCPUSet(2),
			},
		},
	}
	candidate := NewCPUStateCandidate(snapshot)
	candidate.UpdateNUMADefaultCPUSet(0, machine.NewCPUSet(3, 4))

	materialized := candidate.Materialize()
	materialized.MachineState[0].DefaultCPUSet.Add(5)
	materialized.MachineState[0].AllocatedCPUSet.Add(6)
	rematerialized := candidate.Materialize()

	require.Equal(t, machine.NewCPUSet(3, 4), rematerialized.MachineState[0].DefaultCPUSet)
	require.Equal(t, machine.NewCPUSet(2), rematerialized.MachineState[0].AllocatedCPUSet)
	require.Equal(t, machine.NewCPUSet(0, 1), snapshot.MachineState[0].DefaultCPUSet)
	require.Equal(t, machine.NewCPUSet(2), snapshot.MachineState[0].AllocatedCPUSet)
}

func TestCPUStateCandidateMaterializeClonesPodEntries(t *testing.T) {
	t.Parallel()

	snapshot := &CPUStateSnapshot{
		PodEntries: state.PodEntries{
			"pod-a": {
				"container-a": nil,
			},
		},
		MachineState: state.NUMANodeMap{},
	}
	candidate := NewCPUStateCandidate(snapshot)

	materialized := candidate.Materialize()
	materialized.PodEntries["pod-b"] = state.ContainerEntries{}
	materialized.PodEntries["pod-a"]["container-b"] = nil

	require.NotContains(t, snapshot.PodEntries, "pod-b")
	require.NotContains(t, snapshot.PodEntries["pod-a"], "container-b")
}
