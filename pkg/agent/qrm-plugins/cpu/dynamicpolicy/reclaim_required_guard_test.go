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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func nonEmptyReclaimGuardEntries() state.PodEntries {
	return state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationResult: machine.MustParse("0-3"),
			},
		},
	}
}

func TestValidateRequiredReclaimPoolContract(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, validateRequiredReclaimPool(emptyReclaimEntries()), errReclaimFallbackEmptyRequired)
	require.NoError(t, validateRequiredReclaimPool(nonEmptyReclaimGuardEntries()))
}

// TestDisabledReclaimRejectsEmptyWithoutPublishing pins the terminal state
// contract: with reclaim disabled, a candidate that yields an empty reclaim pool
// must be rejected and must neither advance the revision nor mutate the committed
// reclaim pool. Failure stays no-write.
func TestDisabledReclaimRejectsEmptyWithoutPublishing(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = false

	committedEntries := precommitPartitionEntries(machine.NewCPUSet(4, 52), machine.NewCPUSet(2, 3))
	committedMachineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology, committedEntries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(
		committedEntries, committedMachineState, false, true, false))

	beforeRevision := p.state.GetRevision()
	beforeReclaim := reclaimPoolCPUSet(p.state.GetPodEntries())
	require.False(t, beforeReclaim.IsEmpty())

	candidate := precommitPartitionEntries(machine.NewCPUSet(), machine.NewCPUSet(2, 3))
	_, _, err = p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		disableDedicated: true,
		persist:          false,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
	})
	require.ErrorIs(t, err, errReclaimFallbackEmptyRequired)
	require.Equal(t, beforeRevision, p.state.GetRevision(), "failed empty candidate must not advance revision")
	require.True(t, reclaimPoolCPUSet(p.state.GetPodEntries()).Equals(beforeReclaim),
		"failed empty candidate must not overwrite the committed reclaim pool")
}

// TestDeriveDefaultReclaimPreferenceZeroSizeRecoversWholeCore pins the zero-target
// contract: a configured reclaim size of 0 does not leave an empty seed; it recovers
// exactly one complete physical core so the partition always exists. The seed is
// checked for core/SMT alignment and NUMA consistency, not merely its size.
func TestDeriveDefaultReclaimPreferenceZeroSizeRecoversWholeCore(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	cpusPerCore := topology.CPUsPerCore()

	seed, size, assignments, err := deriveDefaultReclaimPreference(
		topology, topology.CPUDetails, machine.NewCPUSet(), 0)
	require.NoError(t, err)
	require.Equal(t, cpusPerCore, size, "zero configured target must seed one whole core")
	require.Equal(t, cpusPerCore, seed.Size())
	require.True(t, seed.IsSubsetOf(topology.CPUDetails.CPUs()), "seed must stay on-machine")
	assertWholeCoreAlignedAndNUMAConsistent(t, topology, seed, assignments)
}

// TestDeriveDefaultReclaimPreferenceNegativeSizeRecoversWholeCore: a negative
// configured reclaim size is clamped exactly like 0 — one complete core.
func TestDeriveDefaultReclaimPreferenceNegativeSizeRecoversWholeCore(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	cpusPerCore := topology.CPUsPerCore()

	seed, size, assignments, err := deriveDefaultReclaimPreference(
		topology, topology.CPUDetails, machine.NewCPUSet(), -4)
	require.NoError(t, err)
	require.Equal(t, cpusPerCore, size, "negative configured target must seed one whole core")
	require.Equal(t, cpusPerCore, seed.Size())
	assertWholeCoreAlignedAndNUMAConsistent(t, topology, seed, assignments)
}

// TestDeriveDefaultReclaimPreferenceNoCapacityFailsClosed: when every CPU is
// reserved away (no core-aligned capacity), deriving even a one-core seed must
// fail closed rather than return an empty-but-"successful" partition.
func TestDeriveDefaultReclaimPreferenceNoCapacityFailsClosed(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)

	_, _, _, err = deriveDefaultReclaimPreference(
		topology, topology.CPUDetails, topology.CPUDetails.CPUs(), topology.CPUsPerCore())
	require.Error(t, err, "no core-aligned capacity must fail closed, not seed empty")
}

// assertWholeCoreAlignedAndNUMAConsistent verifies the seed occupies exactly one
// physical core (SMT siblings paired on the same CoreID) and lives entirely
// within a single NUMA node, mirrored by the NUMA assignments.
func assertWholeCoreAlignedAndNUMAConsistent(t *testing.T, topology *machine.CPUTopology,
	seed machine.CPUSet, assignments map[int]machine.CPUSet,
) {
	t.Helper()
	cpuList := seed.ToSliceInt()
	require.NotEmpty(t, cpuList)
	coreID := topology.CPUDetails[cpuList[0]].CoreID
	numaID := topology.CPUDetails[cpuList[0]].NUMANodeID
	for _, cpu := range cpuList[1:] {
		require.Equal(t, coreID, topology.CPUDetails[cpu].CoreID,
			"seed CPUs must all share one physical core")
		require.Equal(t, numaID, topology.CPUDetails[cpu].NUMANodeID,
			"seed CPUs must all live on one NUMA node")
	}
	require.Len(t, assignments, 1, "exactly one NUMA bucket expected")
	require.Contains(t, assignments, numaID)
	require.True(t, assignments[numaID].Equals(seed),
		"NUMA assignment must equal the seed cpuset")
}
