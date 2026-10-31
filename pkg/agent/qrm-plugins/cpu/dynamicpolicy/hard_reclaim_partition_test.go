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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// coresInNUMA returns the cpuset of the [start,end) cores (both SMT siblings per
// core) of the given NUMA, ordered by ascending core id. It lets tests build
// core-aligned inputs regardless of the sibling-id offset the dummy topology
// uses, so a "reclaim" or "donor" fixture never accidentally holds a half core.
func coresInNUMA(topology *machine.CPUTopology, numaID, start, end int) machine.CPUSet {
	cores := topology.CPUDetails.CoresInNUMANodes(numaID).ToSliceInt()
	if start < 0 {
		start = 0
	}
	if end > len(cores) {
		end = len(cores)
	}
	if start >= end {
		return machine.NewCPUSet()
	}
	return topology.CPUDetails.CPUsInCores(cores[start:end]...)
}

// requireCoreAligned fails when reclaim holds a partial physical core.
func requireCoreAligned(t *testing.T, topology *machine.CPUTopology, reclaim machine.CPUSet) {
	t.Helper()
	require.NoErrorf(t, assertCoreAligned(reclaim, topology),
		"reclaim %s must be core-aligned", reclaim.String())
}

func TestPlanHardReclaimPartitionKeepsSixCPUsOnEvery32CPUNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	require.Equal(t, 32, numa0.Size())
	require.Equal(t, 32, numa1.Size())

	for _, tc := range []struct {
		name            string
		free            machine.CPUSet
		currentReclaim  machine.CPUSet
		donors          []hardReclaimPartitionDonor
		reclaimEligible machine.CPUSet
	}{
		{
			// shared / SNB: the whole NUMA is free, reclaim carves three complete
			// cores per NUMA.
			name:            "shared and SNB",
			free:            numa0.Union(numa1),
			currentReclaim:  machine.NewCPUSet(),
			reclaimEligible: numa0.Union(numa1),
		},
		{
			// ordinary DNB: two free cores plus one core of donor excess per NUMA.
			name: "ordinary DNB",
			free: coresInNUMA(topology, 0, 0, 2).Union(coresInNUMA(topology, 1, 0, 2)),
			donors: []hardReclaimPartitionDonor{
				{key: "dnb-0", cpus: coresInNUMA(topology, 0, 2, 7), requestQuantity: 8},
				{key: "dnb-1", cpus: coresInNUMA(topology, 1, 2, 7), requestQuantity: 8},
			},
			reclaimEligible: numa0.Union(numa1),
		},
		{
			// exclusive DNB: reclaim comes purely from three free cores; the donor
			// holds exactly its request so no excess is handed back.
			name: "exclusive DNB",
			free: coresInNUMA(topology, 0, 0, 3).Union(coresInNUMA(topology, 1, 0, 3)),
			donors: []hardReclaimPartitionDonor{
				{key: "exclusive-0", cpus: coresInNUMA(topology, 0, 3, 16), requestQuantity: 26},
				{key: "exclusive-1", cpus: coresInNUMA(topology, 1, 3, 16), requestQuantity: 26},
			},
			reclaimEligible: numa0.Union(numa1),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
				topology:        topology,
				targetByNUMA:    map[int]int{0: 6, 1: 6},
				currentReclaim:  tc.currentReclaim,
				free:            tc.free,
				reclaimEligible: tc.reclaimEligible,
				donors:          tc.donors,
			})
			require.NoError(t, err)
			require.Equal(t, 6, plan.reclaim.Intersection(numa0).Size())
			require.Equal(t, 6, plan.reclaim.Intersection(numa1).Size())
			requireCoreAligned(t, topology, plan.reclaim)
		})
	}
}

// TestPlanHardReclaimPartitionSelectsCompletePhysicalCores reconstructs the exact
// node symptom: a naive lowest-id fill on an SMT2 topology strands the high
// siblings and yields {0-20,96-102}-style half cores. The core-aligned selection
// must instead keep both siblings of every chosen core.
func TestPlanHardReclaimPartitionSelectsCompletePhysicalCores(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		currentReclaim:  machine.NewCPUSet(),
		free:            numa0,
		reclaimEligible: numa0,
	})
	require.NoError(t, err)
	require.Equal(t, 6, plan.reclaim.Size())
	requireCoreAligned(t, topology, plan.reclaim)
}

// TestPlanHardReclaimPartitionIsIdempotentOnAlignedInput proves an already
// core-aligned currentReclaim is reused byte-for-byte (no churn).
func TestPlanHardReclaimPartitionIsIdempotentOnAlignedInput(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	current := coresInNUMA(topology, 0, 0, 3)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		currentReclaim:  current,
		free:            numa0.Difference(current),
		reclaimEligible: numa0,
	})
	require.NoError(t, err)
	require.True(t, plan.reclaim.Equals(current), "aligned reclaim must be reused: got %s want %s",
		plan.reclaim.String(), current.String())
}

// TestPlanHardReclaimPartitionNonSMTZeroDrift proves that on a non-SMT topology
// (CPUsPerCore()==1) every cpu is its own core, so selection reduces to a
// prefer-first lowest-id take with no behavioral drift.
func TestPlanHardReclaimPartitionNonSMTZeroDrift(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(16, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 1, topology.CPUsPerCore())
	numa := topology.CPUDetails.CPUsInNUMANodes(0)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 5},
		currentReclaim:  machine.NewCPUSet(),
		free:            numa,
		reclaimEligible: numa,
	})
	require.NoError(t, err)
	require.Equal(t, 5, plan.reclaim.Size())
	requireCoreAligned(t, topology, plan.reclaim)
}

func TestPlanHardReclaimPartitionSubtractsSatisfiedFloorBeforeAllocation(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	// three complete cores already reclaimed; the fourth core comes from free.
	currentFloor := coresInNUMA(topology, 0, 0, 3)
	free := coresInNUMA(topology, 0, 3, 5)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 8},
		currentReclaim:  currentFloor,
		free:            free,
		reclaimEligible: numa,
	})
	require.NoError(t, err)
	require.Equal(t, 8, plan.reclaim.Size(), "advisor quantity already includes the six-CPU floor")
	require.True(t, currentFloor.IsSubsetOf(plan.reclaim))
	require.Equal(t, 2, plan.reclaim.Difference(currentFloor).Size())
	requireCoreAligned(t, topology, plan.reclaim)
}

func TestPlanHardReclaimPartitionDonatesSameNUMADedicatedExcess(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 2)      // two free cores
	dedicated := coresInNUMA(topology, 0, 2, 6) // four dedicated cores, request 6 cpus (three cores)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{{
			key: "dnb", cpus: dedicated, requestQuantity: 6,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 6, plan.reclaim.Size())
	require.Equal(t, 6, plan.donorCPUs["dnb"].Size())
	require.Equal(t, 2, plan.reclaim.Intersection(dedicated).Size())
	require.True(t, plan.reclaim.Intersection(plan.donorCPUs["dnb"]).IsEmpty())
	requireCoreAligned(t, topology, plan.reclaim)
	// the donor's retained set must also stay core-aligned: excess is handed back
	// in complete cores, never a lone SMT sibling.
	requireCoreAligned(t, topology, plan.donorCPUs["dnb"])
}

func TestPlanHardReclaimPartitionRejectsDonationBelowCeilRequest(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 2)      // two free cores (four cpus)
	dedicated := coresInNUMA(topology, 0, 2, 6) // four dedicated cores (eight cpus)

	// request floor ceil(6.2)=7 leaves only one cpu of excess, which is less than a
	// complete core, so no core can be handed back; the fourth reclaim core cannot
	// be satisfied.
	_, err = planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{{
			key: "dnb", cpus: dedicated, requestQuantity: 6.2,
		}},
	})
	require.ErrorContains(t, err, "NUMA 0 needs 2 more reclaim CPUs")
}

func TestPlanHardReclaimPartitionChecksRequestFloorForLegacyOverlappingReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	free := coresInNUMA(topology, 0, 0, 2)
	dedicated := coresInNUMA(topology, 0, 2, 6)
	currentReclaim := coresInNUMA(topology, 0, 2, 3) // one core already overlapping the donor

	// even with an overlapping legacy reclaim core, the request floor ceil(6.2)=7
	// leaves under one core of donatable excess, so the extra reclaim core is
	// rejected rather than stealing below the request floor.
	_, err = planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		currentReclaim:  currentReclaim,
		free:            free,
		reclaimEligible: numa,
		donors: []hardReclaimPartitionDonor{{
			key: "dnb", cpus: dedicated, requestQuantity: 6.2,
		}},
	})
	require.ErrorContains(t, err, "NUMA 0 needs")
}

// TestPlanHardReclaimPartitionStaysCoreAlignedWithIsolationShrinkingEligible
// proves an isolation pool (which is never a donor and never reclaim, it only
// consumes cpus and so shrinks reclaimEligible) cannot perturb reclaim core
// alignment: reclaim still carves complete cores out of whatever residual the
// NUMA offers.
func TestPlanHardReclaimPartitionStaysCoreAlignedWithIsolationShrinkingEligible(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	// isolation holds two complete cores; reclaim is only eligible on the rest.
	isolation := coresInNUMA(topology, 0, 0, 2)
	eligible := numa.Difference(isolation)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 6},
		free:            eligible,
		reclaimEligible: eligible,
	})
	require.NoError(t, err)
	require.Equal(t, 6, plan.reclaim.Size())
	require.True(t, plan.reclaim.Intersection(isolation).IsEmpty(),
		"reclaim must not overlap isolation %s", isolation.String())
	requireCoreAligned(t, topology, plan.reclaim)
}

// TestPlanHardReclaimPartitionNeverSelectsAHalfCoreFromEligible proves that when
// the eligible residual contains a lone SMT sibling (its peer was consumed by
// some non-reclaim pool), a core-aligned target selects only the complete cores
// and leaves the orphan sibling untouched — no half core is ever emitted.
func TestPlanHardReclaimPartitionNeverSelectsAHalfCoreFromEligible(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	// two complete cores plus a single stranded sibling => five eligible cpus.
	twoCores := coresInNUMA(topology, 0, 0, 2)
	strandedCore := coresInNUMA(topology, 0, 2, 3).ToSliceInt()
	eligible := twoCores.Union(machine.NewCPUSet(strandedCore[0]))
	require.Equal(t, 5, eligible.Size())

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 4}, // core-aligned demand over an odd residual
		free:            eligible,
		reclaimEligible: eligible,
	})
	require.NoError(t, err)
	require.Equal(t, 4, plan.reclaim.Size())
	require.True(t, plan.reclaim.Equals(twoCores),
		"reclaim must be the two complete cores %s, got %s", twoCores.String(), plan.reclaim.String())
	requireCoreAligned(t, topology, plan.reclaim)
}

func TestPlanHardReclaimPartitionSharesRequestFloorAcrossNUMADonors(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	donor0 := coresInNUMA(topology, 0, 0, 3) // three cores (six cpus)
	donor1 := coresInNUMA(topology, 1, 0, 3) // three cores (six cpus)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 2, 1: 2},
		reclaimEligible: numa0.Union(numa1),
		donors: []hardReclaimPartitionDonor{
			{key: "dnb-numa0", groupKey: "pod/main", cpus: donor0, requestQuantity: 8},
			{key: "dnb-numa1", groupKey: "pod/main", cpus: donor1, requestQuantity: 8},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, plan.reclaim.Intersection(numa0).Size())
	require.Equal(t, 2, plan.reclaim.Intersection(numa1).Size())
	require.Equal(t, 8, plan.donorCPUs["dnb-numa0"].Size()+plan.donorCPUs["dnb-numa1"].Size())
	requireCoreAligned(t, topology, plan.reclaim)
}

func TestPlanHardReclaimPartitionBuildsCoreAcrossFreeAndDonor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	core := coresInNUMA(topology, 0, 0, 1)
	threads := core.ToSliceInt()

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 2},
		free:            machine.NewCPUSet(threads[0]),
		reclaimEligible: core,
		donors: []hardReclaimPartitionDonor{{
			key: "donor", cpus: machine.NewCPUSet(threads[1]), requestQuantity: 0,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, core, plan.reclaim)
	require.True(t, plan.donorCPUs["donor"].IsEmpty())
}

func TestPlanHardReclaimPartitionCountsOverlappingBlocksByOwnerUnion(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	core := coresInNUMA(topology, 0, 0, 1)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 2},
		reclaimEligible: core,
		donors: []hardReclaimPartitionDonor{
			{key: "block-a", groupKey: "pod/main", cpus: core, requestQuantity: 0},
			{key: "block-b", groupKey: "pod/main", cpus: core, requestQuantity: 0},
		},
	})

	require.NoError(t, err)
	require.Equal(t, core, plan.reclaim)
	require.True(t, plan.donorCPUs["block-a"].IsEmpty())
	require.True(t, plan.donorCPUs["block-b"].IsEmpty())
}

func TestPlanHardReclaimPartitionRejectsOverlappingDifferentOwners(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	core := coresInNUMA(topology, 0, 0, 1)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 2},
		reclaimEligible: core,
		donors: []hardReclaimPartitionDonor{
			{key: "owner-a", groupKey: "pod-a/main", cpus: core, requestQuantity: 0},
			{key: "owner-b", groupKey: "pod-b/main", cpus: core, requestQuantity: 0},
		},
	})

	require.Nil(t, plan)
	require.ErrorContains(t, err, "overlapping donor ownership")
}

func TestPlanHardReclaimPartitionFrontierAvoidsGreedyDonorDeadEnd(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(6, 1, 1)
	require.NoError(t, err)
	core0 := coresInNUMA(topology, 0, 0, 1)
	core1 := coresInNUMA(topology, 0, 1, 2)
	core2 := coresInNUMA(topology, 0, 2, 3)
	core0Threads := core0.ToSliceInt()
	donorA := core1.Union(machine.NewCPUSet(core0Threads[0]))
	donorB := core2.Union(machine.NewCPUSet(core0Threads[1]))

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 4},
		reclaimEligible: topology.CPUDetails.CPUs(),
		donors: []hardReclaimPartitionDonor{
			{key: "donor-a", groupKey: "pod-a/main", cpus: donorA, requestQuantity: 1},
			{key: "donor-b", groupKey: "pod-b/main", cpus: donorB, requestQuantity: 1},
		},
	})

	require.NoError(t, err)
	require.Equal(t, core1.Union(core2), plan.reclaim)
	requireCoreAligned(t, topology, plan.reclaim)
}

func TestCoreAlignmentDistinguishesReusedCoreIDsAcrossNUMAs(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs:      4,
		NumCores:     2,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 1, SocketID: 1, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			3: {NUMANodeID: 1, SocketID: 1, CoreID: 0},
		},
	}

	completed, err := completeCoresForCPUSet(topology, machine.NewCPUSet(0))
	require.NoError(t, err)
	require.True(t, completed.Equals(machine.NewCPUSet(0, 2)), "completed=%s", completed)

	candidates := coreAlignedCandidates(
		topology, topology.CPUDetails.CPUs(), machine.NewCPUSet())
	require.Len(t, candidates, 2)
	require.NoError(t, assertCoreAligned(machine.NewCPUSet(0, 2), topology))
	require.ErrorContains(t, assertCoreAligned(machine.NewCPUSet(0, 1), topology), "not core-aligned")
}

func TestSelectHardReclaimCoresReportsFrontierTruncation(t *testing.T) {
	t.Parallel()

	candidates := make([]coreAlignedCandidate, 0, 9)
	groupCPUs := make(map[string]machine.CPUSet, 9)
	groupDonationLimit := make(map[string]int, 9)
	for i := 0; i < 9; i++ {
		cpu := machine.NewCPUSet(i)
		candidates = append(candidates, coreAlignedCandidate{coreID: i, cpus: cpu})
		groupKey := fmt.Sprintf("group-%d", i)
		groupCPUs[groupKey] = cpu
		groupDonationLimit[groupKey] = 1
	}

	selected, err := selectHardReclaimCoresWithFrontier(
		candidates, 10, machine.NewCPUSet(), groupCPUs, groupDonationLimit)

	require.True(t, selected.IsEmpty())
	require.ErrorContains(t, err, "search frontier truncated")
}

func TestPruneHardReclaimCoreSelectionStatesCapsTotalStateCount(t *testing.T) {
	t.Parallel()

	states := make(map[string]hardReclaimCoreSelectionState, hardReclaimCoreSelectionMaxStates+1)
	for i := 0; i <= hardReclaimCoreSelectionMaxStates; i++ {
		states[fmt.Sprint(i)] = hardReclaimCoreSelectionState{
			selected:       machine.NewCPUSet(i),
			selectedByNUMA: []int{i},
		}
	}

	got, truncated := pruneHardReclaimCoreSelectionStates(states)

	require.True(t, truncated)
	require.Len(t, got, hardReclaimCoreSelectionMaxStates)
}

func TestPlanHardReclaimPartitionEnforcesDonorFloorAcrossNUMAs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	donor0 := coresInNUMA(topology, 0, 0, 2)
	donor1 := coresInNUMA(topology, 1, 0, 2)
	donorUnion := donor0.Union(donor1)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 4, 1: 4},
		reclaimEligible: donorUnion,
		donors: []hardReclaimPartitionDonor{
			{key: "numa-0", groupKey: "pod/main", cpus: donor0, requestQuantity: 4},
			{key: "numa-1", groupKey: "pod/main", cpus: donor1, requestQuantity: 4},
		},
	})

	require.Nil(t, plan)
	require.ErrorContains(t, err, "NUMA 1 needs 4 more reclaim CPUs")
}

func TestPlanHardReclaimPartitionJointlyAllocatesSharedDonorQuotaAcrossNUMAs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	numa0Donor := coresInNUMA(topology, 0, 0, 1)
	numa0Free := coresInNUMA(topology, 0, 1, 2)
	numa1Donor := coresInNUMA(topology, 1, 0, 1)

	plan, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 2, 1: 2},
		currentReclaim:  numa0Donor,
		free:            numa0Free,
		reclaimEligible: numa0Donor.Union(numa0Free).Union(numa1Donor),
		donors: []hardReclaimPartitionDonor{
			{key: "numa-0", groupKey: "pod/main", cpus: numa0Donor, requestQuantity: 2},
			{key: "numa-1", groupKey: "pod/main", cpus: numa1Donor, requestQuantity: 2},
		},
	})

	require.NoError(t, err)
	require.Equal(t, numa0Free.Union(numa1Donor), plan.reclaim)
	require.Equal(t, 2, plan.donorCPUs["numa-0"].Union(plan.donorCPUs["numa-1"]).Size())
}

func productionNUMA2ReplacementFixture(t *testing.T) (
	*machine.CPUTopology,
	[]partitionDemand,
	machine.CPUSet,
	machine.CPUSet,
) {
	t.Helper()

	_, topology, demands, _ := loadHardReclaimFixture(t, "affected-numa2-boundary")
	return topology, demands, demands[0].preferred.Clone(), demands[1].preferred.Clone()
}

func TestPlanHardReclaimPartitionRepairsDedicatedBoundaryByReplacement(t *testing.T) {
	t.Parallel()

	topology, demands, reclaimBefore, dedicatedBefore := productionNUMA2ReplacementFixture(t)
	numa2 := topology.CPUDetails.CPUsInNUMANodes(2)

	_, err := planHardReclaimPartition(hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{2: 8},
		currentReclaim:  reclaimBefore,
		free:            numa2.Difference(reclaimBefore.Union(dedicatedBefore)),
		reclaimEligible: numa2,
		donors: []hardReclaimPartitionDonor{{
			key:             "dedicated-numa-2",
			groupKey:        "pod/main",
			cpus:            dedicatedBefore,
			requestQuantity: 62,
		}},
	})
	require.EqualError(t, err, "NUMA 2 needs 2 more reclaim CPUs")
	var selectionErr *hardReclaimSelectionError
	require.ErrorAs(t, err, &selectionErr)
	require.Equal(t, hardReclaimFailureDonorFloor, selectionErr.reason)

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands,
		topology.CPUDetails.CPUs(),
		topology,
		defaultHardReclaimReplacementOptions(),
	)
	require.NoError(t, err)
	require.NotNil(t, proof)

	expectedReclaim := machine.NewCPUSet(32, 44, 46, 47, 96, 108, 110, 111)
	expectedDedicated := machine.NewCPUSet(
		33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 45,
		97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 109,
	)
	require.Equal(t, expectedReclaim, assignments["reclaim-numa-2"])
	require.Equal(t, expectedDedicated, assignments["dedicated-numa-2"])
	require.Equal(t, 8, assignments["reclaim-numa-2"].Size())
	require.Equal(t, 24, assignments["dedicated-numa-2"].Size())
	require.True(t, assignments["reclaim-numa-2"].Intersection(assignments["dedicated-numa-2"]).IsEmpty())
	require.True(t, assignments["reclaim-numa-2"].Union(assignments["dedicated-numa-2"]).
		Equals(numa2))
	requireCoreAligned(t, topology, assignments["reclaim-numa-2"])
	require.Equal(t, reclaimBefore, proof.reclaimBefore)
	require.Equal(t, expectedReclaim, proof.reclaimAfter)
	require.Equal(t, dedicatedBefore, proof.dedicatedBeforeByGroup["pod/main"])
	require.Equal(t, expectedDedicated, proof.dedicatedAfterByGroup["pod/main"])
}

func TestHardReclaimReplacementPreservesUnderprovisionedDonorSize(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	dedicated := coresInNUMA(topology, 0, 0, 24)
	reclaim := coresInNUMA(topology, 0, 24, 28)
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: reclaim.Size(), eligible: numa,
			preferred: reclaim, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "dedicated", requestGroupKey: "pod/main", quantity: dedicated.Size(),
			requestQuantity: 62, eligible: numa, preferred: dedicated,
			class: advisorBlockClassDedicated,
		},
	}
	assignments := map[string]machine.CPUSet{
		"reclaim":   reclaim,
		"dedicated": dedicated,
	}

	proof, err := validateHardReclaimReplacement(
		demands, assignments, topology, map[int]int{0: reclaim.Size()})

	require.NoError(t, err)
	require.Equal(t, 48, proof.dedicatedBeforeByGroup["pod/main"].Size())
	require.Equal(t, 48, proof.dedicatedAfterByGroup["pod/main"].Size())
}

func TestHardReclaimReplacementRejectsAdditionalDonorLoss(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	before := coresInNUMA(topology, 0, 0, 24)
	after := coresInNUMA(topology, 0, 0, 23)
	demands := []partitionDemand{{
		key: "dedicated", requestGroupKey: "pod/main", quantity: after.Size(),
		requestQuantity: 62, eligible: numa, preferred: before,
		class: advisorBlockClassDedicated,
	}}

	_, err = validateHardReclaimReplacement(
		demands,
		map[string]machine.CPUSet{"dedicated": after},
		topology,
		map[int]int{},
	)

	require.ErrorContains(t, err, `dedicated group "pod/main"`)
	require.ErrorContains(t, err, "changed NUMA 0 ownership from 48 to 46")
}

func TestHardReclaimReplacementRejectsCrossNUMACompensation(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	before := coresInNUMA(topology, 0, 0, 1)
	after := coresInNUMA(topology, 1, 0, 1)
	demands := []partitionDemand{{
		key: "dedicated", requestGroupKey: "pod/main", quantity: 2,
		requestQuantity: 2, eligible: topology.CPUDetails.CPUs(), preferred: before,
		class: advisorBlockClassDedicated,
	}}

	_, err = validateHardReclaimReplacement(
		demands,
		map[string]machine.CPUSet{"dedicated": after},
		topology,
		map[int]int{},
	)

	require.ErrorContains(t, err, "changed NUMA 0 ownership")
}

func TestHardReclaimReplacementRejectsPartialFinalReclaimCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	partial := machine.NewCPUSet(numa.ToSliceInt()[0])
	demands := []partitionDemand{{
		key: "reclaim", quantity: 1, eligible: numa, preferred: partial,
		class: advisorBlockClassMandatoryReclaim,
	}}

	_, err = validateHardReclaimReplacement(
		demands,
		map[string]machine.CPUSet{"reclaim": partial},
		topology,
		map[int]int{0: 1},
	)

	require.ErrorContains(t, err, "not core-aligned")
}

func TestHardReclaimReplacementDoesNotShareCreditAcrossOwnerGroups(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(8, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	demands := []partitionDemand{
		{
			key: "owner-a", requestGroupKey: "pod-a/main", quantity: 1,
			requestQuantity: 2, eligible: numa, preferred: machine.NewCPUSet(0, 1),
			class: advisorBlockClassDedicated,
		},
		{
			key: "owner-b", requestGroupKey: "pod-b/main", quantity: 3,
			eligible: numa, preferred: machine.NewCPUSet(2, 3),
			class: advisorBlockClassDedicated,
		},
	}

	_, err = validateHardReclaimReplacement(
		demands,
		map[string]machine.CPUSet{
			"owner-a": machine.NewCPUSet(0),
			"owner-b": machine.NewCPUSet(1, 2, 3),
		},
		topology,
		map[int]int{},
	)

	require.ErrorContains(t, err, `dedicated group "pod-a/main"`)
	require.ErrorContains(t, err, "changed NUMA 0 ownership from 2 to 1")
}

func TestHardReclaimReplacementDeduplicatesAliasesByRequestGroup(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	old := machine.NewCPUSet(0, 1)
	demands := []partitionDemand{
		{
			key: "alias-a", requestGroupKey: "pod/main", quantity: 1,
			requestQuantity: 2, eligible: numa, preferred: old,
			class: advisorBlockClassDedicated,
		},
		{
			key: "alias-b", requestGroupKey: "pod/main", quantity: 1,
			requestQuantity: 2, eligible: numa, preferred: old,
			class: advisorBlockClassDedicated,
		},
	}

	proof, err := validateHardReclaimReplacement(
		demands,
		map[string]machine.CPUSet{
			"alias-a": machine.NewCPUSet(0),
			"alias-b": machine.NewCPUSet(1),
		},
		topology,
		map[int]int{},
	)

	require.NoError(t, err)
	require.Equal(t, 2, proof.dedicatedBeforeByGroup["pod/main"].Size())
	require.Equal(t, 2, proof.dedicatedAfterByGroup["pod/main"].Size())
}

func TestHardReclaimReplacementIsDeterministic(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)
	reversed := []partitionDemand{demands[1], demands[0]}

	first, _, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())
	require.NoError(t, err)
	second, _, err := solveHardReclaimWithReplacement(
		reversed, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())
	require.NoError(t, err)

	require.Equal(t, first, second)
}

func TestSolveHardReclaimResidualCandidatesReusesExactGraph(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	available := machine.NewCPUSet(0, 1, 2, 3)
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: available,
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: available,
			class: advisorBlockClassShared,
		},
	}
	states := []hardReclaimReplacementGlobalState{
		{reclaimAfter: machine.NewCPUSet(0)},
		{reclaimAfter: machine.NewCPUSet(0)},
	}
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	searchBudget := defaultPartitionSearchBudget()

	assignments, proof, err := solveHardReclaimResidualCandidates(
		states, demands, available, topology, map[int]int{0: 1},
		defaultHardReclaimReplacementOptions(), cache, &searchBudget)
	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Len(t, assignments, 2)
	require.Equal(t, 1, cache.misses)
	require.Equal(t, 1, cache.hits)
}

func TestHardReclaimReplacementExcludesSharedOwnershipFromCandidates(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"shared", "bound-share"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
			require.NoError(t, err)
			demands := []partitionDemand{
				{
					key: "reclaim", quantity: 2, eligible: machine.NewCPUSet(0, 1),
					preferred: machine.NewCPUSet(0), class: advisorBlockClassMandatoryReclaim,
				},
				{
					key: name, quantity: 1, eligible: machine.NewCPUSet(1, 2, 3),
					preferred: machine.NewCPUSet(1), class: advisorBlockClassShared,
				},
			}

			assignments, proof, err := solveHardReclaimWithReplacement(
				demands, topology.CPUDetails.CPUs(), topology,
				defaultHardReclaimReplacementOptions())

			require.Nil(t, assignments)
			require.Nil(t, proof)
			var selectionErr *hardReclaimSelectionError
			require.ErrorAs(t, err, &selectionErr)
			require.Equal(t, hardReclaimFailureInsufficientWholeCore, selectionErr.reason)
		})
	}
}

func TestHardReclaimReplacementDefaultBudgetHandlesTwo16CoreNUMAsChoosingFour(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(32, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	require.Equal(t, 16, numa0.Size())
	require.Equal(t, 16, numa1.Size())
	demands := []partitionDemand{
		{
			key: "reclaim-0", quantity: 4, eligible: numa0,
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "reclaim-1", quantity: 4, eligible: numa1,
			class: advisorBlockClassMandatoryReclaim,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	require.NoError(t, err)
	require.NotNil(t, assignments)
	require.NotNil(t, proof)
	require.Equal(t, 4, assignments["reclaim-0"].Size())
	require.Equal(t, 4, assignments["reclaim-1"].Size())
	require.True(t, assignments["reclaim-0"].IsSubsetOf(numa0))
	require.True(t, assignments["reclaim-1"].IsSubsetOf(numa1))
}

func TestHardReclaimReplacementPreservesPerDemandNUMACounts(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	numa0CPUs := numa0.ToSliceInt()
	numa1CPUs := numa1.ToSliceInt()
	dedicatedBefore := machine.NewCPUSet(numa0CPUs[1], numa1CPUs[1])
	demands := []partitionDemand{
		{
			key: "reclaim-0", quantity: 1, eligible: numa0,
			preferred: machine.NewCPUSet(numa0CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "reclaim-1", quantity: 1, eligible: numa1,
			preferred: machine.NewCPUSet(numa1CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "dedicated", requestGroupKey: "pod/main", quantity: 2,
			requestQuantity: 2, eligible: numa0.Union(numa1),
			preferred: dedicatedBefore, class: advisorBlockClassDedicated,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Equal(t, 1, assignments["dedicated"].Intersection(numa0).Size())
	require.Equal(t, 1, assignments["dedicated"].Intersection(numa1).Size())
}

func TestHardReclaimReplacementRejectsBudgetExhaustion(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands,
		topology.CPUDetails.CPUs(),
		topology,
		hardReclaimReplacementOptions{maxCandidateStates: 1, maxTerminalSolves: 1},
	)

	require.Nil(t, assignments)
	require.Nil(t, proof)
	var selectionErr *hardReclaimSelectionError
	require.True(t, errors.As(err, &selectionErr))
	require.Equal(t, hardReclaimFailureSearchBudget, selectionErr.reason)
}

func TestHardReclaimReplacementRejectsFeasibleTruncatedTerminalSet(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	demands := []partitionDemand{{
		key:       "reclaim",
		quantity:  1,
		eligible:  numa,
		preferred: machine.NewCPUSet(0),
		class:     advisorBlockClassMandatoryReclaim,
	}}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands,
		numa,
		topology,
		hardReclaimReplacementOptions{
			maxCandidateStates: 100,
			maxTerminalSolves:  1,
		},
	)

	require.Nil(t, assignments)
	require.Nil(t, proof)
	var selectionErr *hardReclaimSelectionError
	require.ErrorAs(t, err, &selectionErr)
	require.Equal(t, hardReclaimFailureSearchBudget, selectionErr.reason)
}

func TestHardReclaimReplacementDoesNotExpandAliasPreferredIntoLocalQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	aliasesPreferred := machine.NewCPUSet(0, 1)
	for _, class := range []advisorBlockClass{
		advisorBlockClassDedicated,
		advisorBlockClassShared,
	} {
		class := class
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()
			demands := []partitionDemand{
				{
					key: "reclaim", quantity: 1, eligible: aliasesPreferred,
					preferred: machine.NewCPUSet(0), class: advisorBlockClassMandatoryReclaim,
				},
				{
					key: "alias-a", requestGroupKey: "pod/main", quantity: 1,
					requestQuantity: 2, eligible: numa, preferred: aliasesPreferred,
					class: class,
				},
				{
					key: "alias-b", requestGroupKey: "pod/main", quantity: 1,
					requestQuantity: 2, eligible: numa, preferred: aliasesPreferred,
					class: class,
				},
			}

			assignments, proof, err := solveHardReclaimWithReplacement(
				demands, numa, topology, defaultHardReclaimReplacementOptions())

			require.NoError(t, err)
			require.NotNil(t, proof)
			require.Equal(t, 1, assignments["alias-a"].Size())
			require.Equal(t, 1, assignments["alias-b"].Size())
			require.Equal(t, 2, assignments["alias-a"].Union(assignments["alias-b"]).Size())
		})
	}
}

func TestHardReclaimReplacementOptimizesTouchedGroupUnionAcrossNUMAs(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs:      6,
		NumCores:     6,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 1, SocketID: 1, CoreID: 3},
			4: {NUMANodeID: 1, SocketID: 1, CoreID: 4},
			5: {NUMANodeID: 1, SocketID: 1, CoreID: 5},
		},
	}
	all := topology.CPUDetails.CPUs()
	demands := []partitionDemand{
		{
			key: "reclaim-0", quantity: 1, eligible: machine.NewCPUSet(0, 1),
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "reclaim-1", quantity: 1, eligible: machine.NewCPUSet(3, 4),
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "group-a", requestGroupKey: "group-a", quantity: 2,
			requestQuantity: 2, eligible: all, preferred: machine.NewCPUSet(0, 4),
			class: advisorBlockClassDedicated,
		},
		{
			key: "group-b", requestGroupKey: "group-b", quantity: 2,
			requestQuantity: 2, eligible: all, preferred: machine.NewCPUSet(1, 3),
			class: advisorBlockClassDedicated,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, all, topology, defaultHardReclaimReplacementOptions())

	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Equal(t, machine.NewCPUSet(0, 4), proof.reclaimAfter)
	touched := 0
	for groupKey, before := range proof.dedicatedBeforeByGroup {
		if !before.Equals(proof.dedicatedAfterByGroup[groupKey]) {
			touched++
		}
	}
	require.Equal(t, 1, touched)
	require.Equal(t, 2, assignments["group-a"].Size())
	require.Equal(t, 2, assignments["group-b"].Size())
}

func TestSelectHardReclaimCoresAcceptsFeasibleFromTruncatedFrontier(t *testing.T) {
	t.Parallel()

	candidates := make([]coreAlignedCandidate, 0, hardReclaimCoreSelectionFrontierWidth+1)
	groupCPUs := make(map[string]machine.CPUSet, hardReclaimCoreSelectionFrontierWidth+1)
	groupDonationLimit := make(map[string]int, hardReclaimCoreSelectionFrontierWidth+1)
	for i := 0; i <= hardReclaimCoreSelectionFrontierWidth; i++ {
		cpu := machine.NewCPUSet(i)
		candidates = append(candidates, coreAlignedCandidate{coreID: i, cpus: cpu})
		groupKey := fmt.Sprintf("group-%d", i)
		groupCPUs[groupKey] = cpu
		groupDonationLimit[groupKey] = 1
	}

	// With target=1 and 65 single-CPU candidates, the beam truncates at width=64
	// but retains feasible terminal states (any single CPU hits the target).
	// The planner must accept the best retained feasible solution rather than
	// returning search_budget.
	selected, err := selectHardReclaimCoresWithFrontier(
		candidates, 1, machine.NewCPUSet(), groupCPUs, groupDonationLimit)

	require.NoError(t, err)
	require.False(t, selected.IsEmpty())
	require.Equal(t, 1, selected.Size())
}

func TestHardReclaimReplacementPropagatesResidualSolverBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: machine.NewCPUSet(0, 1),
			preferred: machine.NewCPUSet(1), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: machine.NewCPUSet(0, 2, 3),
			preferred: machine.NewCPUSet(2), class: advisorBlockClassShared,
		},
	}

	for _, tc := range []struct {
		name      string
		edgeLimit int
	}{
		{name: "one terminal exceeds budget before another is feasible", edgeLimit: 3},
		{name: "all terminals exceed budget", edgeLimit: 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assignments, proof, err := solveHardReclaimWithReplacement(
				demands,
				numa,
				topology,
				hardReclaimReplacementOptions{
					maxCandidateStates:          100,
					maxTerminalSolves:           10,
					maxPartitionAssignmentEdges: tc.edgeLimit,
					maxPartitionFlowOperations:  partitionFlowOperationBudget,
				},
			)

			require.Nil(t, assignments)
			require.Nil(t, proof)
			var selectionErr *hardReclaimSelectionError
			require.ErrorAs(t, err, &selectionErr)
			require.Equal(t, hardReclaimFailureSearchBudget, selectionErr.reason)
		})
	}
}

func TestHardReclaimReplacementRetriesConflictingGlobalCandidate(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	numa0CPUs := numa0.ToSliceInt()
	numa1CPUs := numa1.ToSliceInt()
	demands := []partitionDemand{
		{
			key: "reclaim-0", quantity: 1, eligible: numa0,
			preferred: machine.NewCPUSet(numa0CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "reclaim-1", quantity: 1, eligible: numa1,
			preferred: machine.NewCPUSet(numa1CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "dedicated", requestGroupKey: "pod/main", quantity: 1,
			requestQuantity: 1, eligible: numa0,
			preferred: machine.NewCPUSet(numa0CPUs[0]), class: advisorBlockClassDedicated,
		},
		{
			key: "shared", quantity: 1, eligible: machine.NewCPUSet(numa0CPUs[1], numa1CPUs[0]),
			class: advisorBlockClassShared,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Equal(t, machine.NewCPUSet(numa0CPUs[0], numa1CPUs[1]), proof.reclaimAfter)
	require.Equal(t, machine.NewCPUSet(numa0CPUs[1]), assignments["dedicated"])
	require.Equal(t, machine.NewCPUSet(numa1CPUs[0]), assignments["shared"])
}

func TestHardReclaimReplacementAllowsSharedDemandToMigrateAcrossNUMAs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	numa0CPUs := numa0.ToSliceInt()
	numa1CPUs := numa1.ToSliceInt()
	demands := []partitionDemand{
		{
			key: "reclaim-0", quantity: 1, eligible: numa0,
			preferred: machine.NewCPUSet(numa0CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "reclaim-1", quantity: 1, eligible: numa1,
			preferred: machine.NewCPUSet(numa1CPUs[0]), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 2, eligible: numa0.Union(numa1),
			preferred: numa0, class: advisorBlockClassShared,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Equal(t, 1, assignments["shared"].Intersection(numa0).Size())
	require.Equal(t, 1, assignments["shared"].Intersection(numa1).Size())
}

func TestHardReclaimReplacementComparatorPrefersRetainedReclaimBeforePartialCoreChurn(t *testing.T) {
	t.Parallel()

	leftProof := &hardReclaimReplacementProof{
		reclaimBefore:          machine.NewCPUSet(0, 1),
		reclaimAfter:           machine.NewCPUSet(0, 2),
		dedicatedBeforeByGroup: map[string]machine.CPUSet{},
		dedicatedAfterByGroup:  map[string]machine.CPUSet{},
		partialBeforeCores:     1,
	}
	rightProof := &hardReclaimReplacementProof{
		reclaimBefore:          machine.NewCPUSet(0, 1),
		reclaimAfter:           machine.NewCPUSet(2, 3),
		dedicatedBeforeByGroup: map[string]machine.CPUSet{},
		dedicatedAfterByGroup:  map[string]machine.CPUSet{},
		partialBeforeCores:     0,
	}

	require.True(t, hardReclaimReplacementResultLess(
		map[string]machine.CPUSet{}, leftProof,
		map[string]machine.CPUSet{}, rightProof))
}

func TestHardReclaimReplacementResetsAssignmentEdgesPerResidualGraph(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: numa,
			preferred: machine.NewCPUSet(0), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: numa,
			preferred: machine.NewCPUSet(1), class: advisorBlockClassShared,
		},
	}

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands,
		numa,
		topology,
		hardReclaimReplacementOptions{
			maxCandidateStates:          100,
			maxTerminalSolves:           10,
			maxPartitionAssignmentEdges: 4,
			maxPartitionFlowOperations:  partitionFlowOperationBudget,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, proof)
	require.Len(t, assignments, 2)
}

func TestHardReclaimReplacementRejectsOneOversizedResidualGraph(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)
	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, topology.CPUDetails.CPUs(), topology,
		hardReclaimReplacementOptions{
			maxCandidateStates:          100_000,
			maxTerminalSolves:           4096,
			maxPartitionAssignmentEdges: 1,
			maxPartitionFlowOperations:  partitionFlowOperationBudget,
		},
	)

	require.Nil(t, assignments)
	require.Nil(t, proof)
	require.ErrorIs(t, err, errPartitionAssignmentEdgeBudget)
}

func TestHardReclaimReplacementSharesFlowOperationsAcrossResidualGraphs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: numa,
			preferred: machine.NewCPUSet(0), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: numa,
			preferred: machine.NewCPUSet(1), class: advisorBlockClassShared,
		},
	}
	options := hardReclaimReplacementOptions{
		maxCandidateStates:          100,
		maxTerminalSolves:           10,
		maxPartitionAssignmentEdges: partitionAssignmentEdgeBudget,
	}

	residualGraphs, err := enumerateHardReclaimReplacementInNUMA(
		demands, numa, topology, 0, 1, options)
	require.NoError(t, err)
	require.Greater(t, len(residualGraphs), 1)

	maxSingleGraphFlowOperations := 0
	totalFlowOperations := 0
	for _, residualGraph := range residualGraphs {
		graphBudget := defaultPartitionGraphBudget()
		searchBudget := defaultPartitionSearchBudget()
		_, solveErr := solveDisjointPartitionsWithBudgets(
			hardReclaimResidualDemands(
				demands, numa, residualGraph.proof.reclaimAfter),
			topology,
			&graphBudget,
			&searchBudget,
		)
		require.NoError(t, solveErr)
		if searchBudget.flowOperations > maxSingleGraphFlowOperations {
			maxSingleGraphFlowOperations = searchBudget.flowOperations
		}
		totalFlowOperations += searchBudget.flowOperations
	}
	flowLimit := maxSingleGraphFlowOperations + 1
	require.Greater(t, totalFlowOperations, flowLimit)

	for _, residualGraph := range residualGraphs {
		graphBudget := defaultPartitionGraphBudget()
		searchBudget := partitionSearchBudget{maxFlowOperations: flowLimit}
		_, solveErr := solveDisjointPartitionsWithBudgets(
			hardReclaimResidualDemands(
				demands, numa, residualGraph.proof.reclaimAfter),
			topology,
			&graphBudget,
			&searchBudget,
		)
		require.NoError(t, solveErr)
		require.Less(t, searchBudget.flowOperations, flowLimit)
	}
	t.Logf(
		"flow limit %d exceeds every residual graph (max %d), but not their cumulative cost %d",
		flowLimit, maxSingleGraphFlowOperations, totalFlowOperations)

	assignments, proof, err := solveHardReclaimWithReplacement(
		demands, numa, topology,
		hardReclaimReplacementOptions{
			maxCandidateStates:          options.maxCandidateStates,
			maxTerminalSolves:           options.maxTerminalSolves,
			maxPartitionAssignmentEdges: options.maxPartitionAssignmentEdges,
			maxPartitionFlowOperations:  flowLimit,
		},
	)

	require.Nil(t, assignments)
	require.Nil(t, proof)
	require.ErrorIs(t, err, errPartitionFlowOperationBudget)
}
