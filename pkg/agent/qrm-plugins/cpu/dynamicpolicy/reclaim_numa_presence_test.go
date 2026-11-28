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

// replay96NUMATopology mirrors the production 96-logical-CPU / 2-socket / 2-NUMA
// SMT2 layout: NUMA0 = 0-23,48-71 and NUMA1 = 24-47,72-95.
func replay96NUMATopology(t *testing.T) *machine.CPUTopology {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	require.True(t, topology.CPUDetails.CPUsInNUMANodes(0).Equals(machine.MustParse("0-23,48-71")))
	require.True(t, topology.CPUDetails.CPUsInNUMANodes(1).Equals(machine.MustParse("24-47,72-95")))
	return topology
}

// TestReclaimPoolAdjustmentKeepsEveryNUMAReclaimBucketNonEmpty replays a recorded
// capacity shape: reclaim owned one whole core per NUMA (0,48 / 24,72) and a
// shared-NUMA-binding member on NUMA1 then took one CPU from that core. The
// vacated NUMA1 reclaim core must be reseeded inside NUMA1; a node-level top-up
// that leaves NUMA1 empty is a contract violation.
func TestReclaimPoolAdjustmentKeepsEveryNUMAReclaimBucketNonEmpty(t *testing.T) {
	t.Parallel()

	topology := replay96NUMATopology(t)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.MustParse("0,24,48,72")
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 48),
		1: machine.NewCPUSet(24, 72),
	}
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.MustParse("0,24,48,72"),
			},
		},
	}, false))

	pools, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{"snbseed-NUMA1": {1: 1}},
		map[string]map[string]int{},
		machine.MustParse("0-5,7-53,55-95"),
		map[string]float64{},
		generatePoolsAndIsolationOptions{
			enforceReclaimMinimum: true,
			dynamicConf:           p.config.dynamicConfig.GetDynamicConfiguration(),
		},
	)
	require.NoError(t, err)

	reclaim := pools[commonstate.PoolNameReclaim]
	require.Equal(t, 4, reclaim.Size(), "reclaim quantity must stay at the configured minimum: %s", reclaim)
	require.NoError(t, assertCoreAligned(reclaim, topology), "reclaim=%s", reclaim)

	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		perNUMA := reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		require.Falsef(t, perNUMA.IsEmpty(),
			"NUMA %d reclaim bucket must not be empty: reclaim=%s", numaID, reclaim)
		require.NoErrorf(t, assertCoreAligned(perNUMA, topology),
			"NUMA %d reclaim bucket must be whole-core: %s", numaID, perNUMA)
	}
	// The preserved NUMA0 core must survive; the NUMA1 core must be a different
	// whole core, because CPU24 now belongs to the binding member.
	require.True(t, machine.MustParse("0,48").IsSubsetOf(reclaim), "reclaim=%s", reclaim)
	require.False(t, reclaim.Contains(24), "reclaim must not keep a CPU owned by the binding member: %s", reclaim)
}

// TestValidateReclaimNUMAPresenceRejectsEmptiedNUMA pins the terminal guard: a
// committed reclaim cpuset on one NUMA may not become empty in the candidate.
func TestValidateReclaimNUMAPresenceRejectsEmptiedNUMA(t *testing.T) {
	t.Parallel()

	topology := replay96NUMATopology(t)
	committed := machine.MustParse("0,24,48,72")

	err := validateReclaimNUMAPresence(committed, machine.MustParse("0-1,48-49"), machine.NewCPUSet(), topology)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NUMA 1")

	// Keeping one whole core on each NUMA is accepted.
	require.NoError(t, validateReclaimNUMAPresence(committed, machine.MustParse("0,48,25,73"), machine.NewCPUSet(), topology))
	// A node without committed reclaim has no per-NUMA obligation to preserve.
	require.NoError(t, validateReclaimNUMAPresence(machine.NewCPUSet(), machine.MustParse("0-1,48-49"), machine.NewCPUSet(), topology))
}
