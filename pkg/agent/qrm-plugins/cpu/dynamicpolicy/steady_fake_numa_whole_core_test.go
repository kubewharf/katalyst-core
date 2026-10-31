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
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSteadyFakeNUMAMigrationChurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		old  machine.CPUSet
		next machine.CPUSet
		want int
	}{
		{"pure expansion", machine.NewCPUSet(0, 1), machine.NewCPUSet(0, 1, 2, 3), 0},
		{"pure shrink", machine.NewCPUSet(0, 1, 2, 3), machine.NewCPUSet(0, 1), 0},
		{"one replacement", machine.NewCPUSet(0, 1), machine.NewCPUSet(0, 2), 2},
		{"expansion with replacement", machine.NewCPUSet(0, 1), machine.NewCPUSet(0, 2, 3, 4), 2},
		{"shrink with replacement", machine.NewCPUSet(0, 1, 2, 3), machine.NewCPUSet(0, 4), 2},
		{
			"five replacements are exactly ten CPU IDs",
			machine.NewCPUSet(0, 1, 2, 3, 4),
			machine.NewCPUSet(5, 6, 7, 8, 9),
			10,
		},
		{
			"eight replacements are exactly sixteen CPU IDs",
			machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			machine.NewCPUSet(8, 9, 10, 11, 12, 13, 14, 15),
			16,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, steadyFakeNUMAMigrationChurn(tt.old, tt.next))
		})
	}
}

func TestSolveSteadyFakeNUMAWholeCoreDelegatesFinalProjection(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	fake := coresInNUMA(topology, 0, 0, 2)
	demands := stagedMigrationDemands(all, fake, fake.Size())
	called := false

	got, err := solveSteadyFakeNUMAWholeCoreWithFloorsAndProject(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(fake, all),
		nil,
		topology,
		func(
			_ []partitionDemand,
			_ []string,
			committed steadyFakeNUMACommittedSnapshot,
			desired map[string]machine.CPUSet,
			_ []partitionCoreFloorConstraint,
			_ *machine.CPUTopology,
		) (map[string]machine.CPUSet, error) {
			called = true
			require.Equal(t, fake, committed.reclaim)
			return desired, nil
		},
	)

	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, fake, got["fake"])
}

func TestProjectSteadyFakeNUMAStageConvergesInBoundedStages(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 6)
	target := coresInNUMA(topology, 0, 6, 12)
	desired := map[string]machine.CPUSet{
		"fake":  target,
		"share": all.Difference(target),
	}

	for cycle := 0; cycle < 3 && !current.Equals(target); cycle++ {
		demands := []partitionDemand{
			{
				key: "fake", quantity: target.Size(), eligible: all,
				preferred: current, class: advisorBlockClassMandatoryReclaim,
			},
			{
				key: "share", quantity: all.Size() - target.Size(), eligible: all,
				preferred: all.Difference(current), class: advisorBlockClassShared,
			},
		}
		next, solveErr := projectSteadyFakeNUMAStage(
			demands, []string{"fake"},
			newSteadyFakeNUMACommittedSnapshotForTest(current, all),
			desired, nil, topology)
		require.NoError(t, solveErr)
		require.NoError(t, assertCoreAligned(next["fake"], topology))
		require.Equal(t, target.Size(), next["fake"].Size())
		require.LessOrEqual(t,
			steadyFakeNUMAMigrationChurn(current, next["fake"]),
			steadyFakeNUMAMaxMigratedCPUs)
		require.NotEqual(t, current, next["fake"])
		current = next["fake"]
	}
	require.Equal(t, target, current)
}

func TestProjectSteadyFakeNUMAStageSupportsSMT1SMT2AndSMT4(t *testing.T) {
	t.Parallel()

	smt1, err := machine.GenerateDummyCPUTopologyWithoutSMT(12, 1, 1)
	require.NoError(t, err)
	smt2, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	smt4 := testSteadyFakeNUMATopology(12, 4)

	for _, tc := range []struct {
		name     string
		topology *machine.CPUTopology
		cores    int
	}{
		{name: "SMT1", topology: smt1, cores: 6},
		{name: "SMT2", topology: smt2, cores: 6},
		{name: "SMT4", topology: smt4, cores: 3},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := tc.topology.CPUDetails.CPUs()
			current := coresInNUMA(tc.topology, 0, 0, tc.cores)
			target := coresInNUMA(tc.topology, 0, tc.cores, 2*tc.cores)
			desired := map[string]machine.CPUSet{
				"fake":  target,
				"share": all.Difference(target),
			}
			maxCycles := steadyFakeNUMAMigrationChurn(current, target)/
				steadyFakeNUMAMaxMigratedCPUs + 1

			for cycle := 0; cycle < maxCycles && !current.Equals(target); cycle++ {
				demands := stagedMigrationDemands(all, current, target.Size())
				next, solveErr := projectSteadyFakeNUMAStage(
					demands, []string{"fake"},
					newSteadyFakeNUMACommittedSnapshotForTest(current, all),
					desired, nil, tc.topology)
				require.NoError(t, solveErr)
				require.NoError(t, assertCoreAligned(next["fake"], tc.topology))
				require.Equal(t, target.Size(), next["fake"].Size())
				require.LessOrEqual(t,
					steadyFakeNUMAMigrationChurn(current, next["fake"]),
					steadyFakeNUMAMaxMigratedCPUs)
				current = next["fake"]
			}
			require.Equal(t, target, current)
		})
	}
}

func TestProjectSteadyFakeNUMAStagePreservesNUMAQuotaAndDonorFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current0 := coresInNUMA(topology, 0, 0, 3)
	current1 := coresInNUMA(topology, 1, 0, 3)
	target0 := coresInNUMA(topology, 0, 3, 6)
	target1 := coresInNUMA(topology, 1, 3, 6)
	current := current0.Union(current1)
	target := target0.Union(target1)
	donorTarget := coresInNUMA(topology, 0, 6, 7)
	desired := map[string]machine.CPUSet{
		"fake-0": target0,
		"fake-1": target1,
		"donor":  donorTarget,
		"share":  all.Difference(target).Difference(donorTarget),
	}
	demands := []partitionDemand{
		{
			key: "fake-0", quantity: target0.Size(),
			eligible:  topology.CPUDetails.CPUsInNUMANodes(0),
			preferred: current0, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "fake-1", quantity: target1.Size(),
			eligible:  topology.CPUDetails.CPUsInNUMANodes(1),
			preferred: current1, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "donor", quantity: donorTarget.Size(), eligible: all,
			preferred: donorTarget, class: advisorBlockClassDedicated,
		},
		{
			key: "share", quantity: all.Size() - target.Size() - donorTarget.Size(),
			eligible: all, preferred: all.Difference(current).Difference(donorTarget),
			class: advisorBlockClassShared,
		},
	}

	next, err := projectSteadyFakeNUMAStage(
		demands,
		[]string{"fake-0", "fake-1"},
		newSteadyFakeNUMACommittedSnapshotForTest(current, all),
		desired,
		nil,
		topology,
	)

	require.NoError(t, err)
	require.Equal(t, target0.Size(), next["fake-0"].Size())
	require.Equal(t, target1.Size(), next["fake-1"].Size())
	require.NoError(t, assertCoreAligned(next["fake-0"].Union(next["fake-1"]), topology))
	require.NoError(t, assertCoreAligned(next["donor"], topology))
	require.LessOrEqual(t,
		steadyFakeNUMAMigrationChurn(current, next["fake-0"].Union(next["fake-1"])),
		steadyFakeNUMAMaxMigratedCPUs)
}

func TestProjectSteadyFakeNUMAStageUsesLatestCommittedStateAndIsIdempotent(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	initial := coresInNUMA(topology, 0, 0, 6)
	targetA := coresInNUMA(topology, 0, 6, 12)
	targetB := initial
	desiredA := map[string]machine.CPUSet{
		"fake": targetA, "share": all.Difference(targetA),
	}

	demands := stagedMigrationDemands(all, initial, targetA.Size())
	first, err := projectSteadyFakeNUMAStage(
		demands, []string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(initial, all),
		desiredA, nil, topology)
	require.NoError(t, err)
	retry, err := projectSteadyFakeNUMAStage(
		demands, []string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(initial, all),
		desiredA, nil, topology)
	require.NoError(t, err)
	require.Equal(t, first, retry)

	committed := first["fake"]
	nextTowardA, err := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, committed, targetA.Size()),
		[]string{"fake"}, newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desiredA, nil, topology)
	require.NoError(t, err)
	require.NotEqual(t, committed, nextTowardA["fake"])

	desiredB := map[string]machine.CPUSet{
		"fake": targetB, "share": all.Difference(targetB),
	}
	redirected, err := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, committed, targetB.Size()),
		[]string{"fake"}, newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desiredB, nil, topology)
	require.NoError(t, err)
	require.Equal(t, targetB, redirected["fake"])
	require.NotEqual(t, nextTowardA["fake"], redirected["fake"])
	require.LessOrEqual(t,
		steadyFakeNUMAMigrationChurn(committed, redirected["fake"]),
		steadyFakeNUMAMaxMigratedCPUs)
	converged, err := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, targetB, targetB.Size()),
		[]string{"fake"}, newSteadyFakeNUMACommittedSnapshotForTest(targetB, all),
		desiredB, nil, topology)
	require.NoError(t, err)
	require.Equal(t, desiredB, converged)
}

func TestProjectSteadyFakeNUMAStageDoesNotChargePureResize(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	small := coresInNUMA(topology, 0, 0, 2)
	large := coresInNUMA(topology, 0, 0, 6)

	for _, tc := range []struct {
		name      string
		committed machine.CPUSet
		target    machine.CPUSet
	}{
		{name: "expansion", committed: small, target: large},
		{name: "shrink", committed: large, target: small},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			desired := map[string]machine.CPUSet{
				"fake": tc.target, "share": all.Difference(tc.target),
			}
			next, solveErr := projectSteadyFakeNUMAStage(
				stagedMigrationDemands(all, tc.committed, tc.target.Size()),
				[]string{"fake"},
				newSteadyFakeNUMACommittedSnapshotForTest(tc.committed, all),
				desired, nil, topology)
			require.NoError(t, solveErr)
			require.Equal(t, desired, next)
			require.Zero(t, steadyFakeNUMAMigrationChurn(tc.committed, next["fake"]))
		})
	}
}

func TestProjectSteadyFakeNUMAStageAtomicallyReplacesFragmentedCommittedState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := machine.NewCPUSet()
	for _, coreID := range topology.CPUDetails.Cores().ToSliceInt()[:20] {
		committed.Add(topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0])
	}
	target := coresInNUMA(topology, 0, 0, 10)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}

	next, err := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, committed, target.Size()),
		[]string{"fake"}, newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desired, nil, topology)

	require.NoError(t, err)
	require.Equal(t, desired, next)
}

func TestProjectSteadyFakeNUMAStageRepairsFragmentedCommittedBeforeFastPath(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	target := coresInNUMA(topology, 0, 0, 3)
	desired := map[string]machine.CPUSet{
		"fake":  target,
		"share": all.Difference(target),
	}

	fragmented := machine.NewCPUSet()
	for _, coreID := range topology.CPUDetails.Cores().ToSliceInt()[2:] {
		fragmented.Add(topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0])
	}
	got, projectErr := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, fragmented, target.Size()),
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(fragmented, all),
		desired, nil, topology)
	require.NoError(t, projectErr)
	require.Equal(t, desired, got)
}

func TestProjectSteadyFakeNUMAStageAtomicallyRepairsMultiOwnerFragmentedRepairBeyondBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	cores := topology.CPUDetails.Cores().ToSliceInt()
	require.GreaterOrEqual(t, len(cores), 16)

	fragmented0 := machine.NewCPUSet()
	fragmented1 := machine.NewCPUSet()
	for _, coreID := range cores[:8] {
		siblings := topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()
		fragmented0.Add(siblings[0])
		fragmented1.Add(siblings[1])
	}
	target0 := topology.CPUDetails.CPUsInCores(cores[8:12]...)
	target1 := topology.CPUDetails.CPUsInCores(cores[12:16]...)
	target := target0.Union(target1)
	committedReclaim := fragmented0.Union(fragmented1)
	require.NoError(t, assertCoreAligned(committedReclaim, topology))
	require.Equal(t, 32, steadyFakeNUMAMigrationChurn(committedReclaim, target))

	demands := []partitionDemand{
		{
			key: "fake-0", quantity: target0.Size(), eligible: all,
			preferred: fragmented0, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "fake-1", quantity: target1.Size(), eligible: all,
			preferred: fragmented1, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - target.Size(), eligible: all,
			preferred: all.Difference(committedReclaim), class: advisorBlockClassShared,
		},
	}
	desired := map[string]machine.CPUSet{
		"fake-0": target0,
		"fake-1": target1,
		"share":  all.Difference(target),
	}
	committed := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake-0", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: fragmented0, eligible: all,
			},
			{
				blockID: "fake-1", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: fragmented1, eligible: all,
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: commonstate.FakedNUMAID,
				cpus:   all.Difference(committedReclaim), eligible: all,
			},
		},
		rawReclaimAggregate: committedReclaim,
		reclaim:             committedReclaim,
	}

	got, projectErr := projectSteadyFakeNUMAStage(
		demands, []string{"fake-0", "fake-1"}, committed, desired, nil, topology)

	require.NoError(t, projectErr)
	require.Equal(t, desired, got)
}

func TestProjectSteadyFakeNUMAStageRejectsCommittedCPUsOutsideTopology(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	target := coresInNUMA(topology, 0, 0, 3)
	committed := target.Union(machine.NewCPUSet(100, 101))
	desired := map[string]machine.CPUSet{
		"fake":  target,
		"share": all.Difference(target),
	}

	got, projectErr := projectSteadyFakeNUMAStage(
		stagedMigrationDemands(all, committed, target.Size()),
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desired, nil, topology)

	require.Nil(t, got)
	require.ErrorContains(t, projectErr, "outside machine topology")
}

func TestProjectSteadyFakeNUMAStagePreservesPreferredDonorCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	desiredFake := coresInNUMA(topology, 0, 0, 3)
	currentFake := coresInNUMA(topology, 0, 3, 6)
	preferredDonor := coresInNUMA(topology, 0, 0, 1)
	desired := map[string]machine.CPUSet{
		"fake":  desiredFake,
		"donor": coresInNUMA(topology, 0, 3, 4),
		"share": all.Difference(desiredFake).Difference(coresInNUMA(topology, 0, 3, 4)),
	}
	demands := []partitionDemand{
		{
			key: "fake", quantity: desiredFake.Size(), eligible: all,
			preferred: currentFake, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "donor", quantity: preferredDonor.Size(), eligible: all,
			preferred: preferredDonor, class: advisorBlockClassDedicated,
		},
		{
			key: "share", quantity: all.Size() - desiredFake.Size() - preferredDonor.Size(),
			eligible: all, preferred: all.Difference(currentFake).Difference(preferredDonor),
			class: advisorBlockClassShared,
		},
	}

	next, err := projectSteadyFakeNUMAStage(
		demands, []string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(currentFake, all),
		desired, nil, topology)

	require.NoError(t, err)
	require.True(t, preferredDonor.IsSubsetOf(next["donor"]),
		"stage displaced preferred donor core %s: donor=%s fake=%s",
		preferredDonor, next["donor"], next["fake"])
	require.LessOrEqual(t,
		steadyFakeNUMAMigrationChurn(currentFake, next["fake"]),
		steadyFakeNUMAMaxMigratedCPUs)
}

func TestProjectSteadyFakeNUMAStageFailsClosedWhenSearchIsTruncatedAfterFindingBest(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 8)
	target := coresInNUMA(topology, 0, 8, 16)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}
	demands := stagedMigrationDemands(all, current, target.Size())

	for _, tc := range []struct {
		name   string
		budget steadyFakeNUMASearchBudget
		want   string
	}{
		{
			name: "candidate budget",
			budget: steadyFakeNUMASearchBudget{
				maxSolveAttempts:    10_000,
				maxCandidateActions: 4,
			},
			want: "staged migration search budget 4 exhausted",
		},
		{
			name: "solve budget",
			budget: steadyFakeNUMASearchBudget{
				maxSolveAttempts:    1,
				maxCandidateActions: 10_000,
			},
			want: "staged migration solve budget 1 exhausted",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, projectErr := projectSteadyFakeNUMAStageWithBudget(
				demands, []string{"fake"},
				newSteadyFakeNUMACommittedSnapshotForTest(current, all),
				desired, nil, topology, tc.budget)

			require.Nil(t, got,
				"a truncated search must not return a provisional best assignment")
			require.ErrorContains(t, projectErr, tc.want)
		})
	}
}

func TestProjectSteadyFakeNUMAStageDoesNotSwallowPinsForUnionBudgetExhaustion(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 8)
	target := coresInNUMA(topology, 0, 8, 16)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}
	demands := stagedMigrationDemands(all, current, target.Size())
	calls := 0

	got, projectErr := projectSteadyFakeNUMAStageWithBudgetAndPins(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(current, all),
		desired,
		nil,
		topology,
		steadyFakeNUMASearchBudget{
			maxSolveAttempts:    10_000,
			maxCandidateActions: 10_000,
		},
		func(
			target machine.CPUSet,
			fakeKeys []string,
			demandByKey map[string]partitionDemand,
			desired map[string]machine.CPUSet,
			topology *machine.CPUTopology,
			_ *steadyFakeNUMASearchTracker,
		) (map[string][]machine.CPUSet, error) {
			calls++
			if calls == 2 {
				return nil, newSteadyFakeNUMAPinBudgetExhaustedError(7)
			}
			return steadyFakeNUMAPinsForUnion(
				target, fakeKeys, demandByKey, desired, topology)
		},
	)

	require.Equal(t, 2, calls, "the first candidate must establish a provisional best")
	require.Nil(t, got, "pin-search truncation must invalidate a provisional best")
	require.ErrorContains(t, projectErr, "pin assignment budget 7 exhausted")
}

func TestProjectSteadyFakeNUMAStageSharesCandidateAndPinBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(10, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 5)
	target := coresInNUMA(topology, 0, 5, 10)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}

	got, projectErr := projectSteadyFakeNUMAStageWithBudget(
		stagedMigrationDemands(all, current, target.Size()),
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(current, all),
		desired,
		nil,
		topology,
		steadyFakeNUMASearchBudget{
			maxSolveAttempts:    10_000,
			maxCandidateActions: 25,
		},
	)

	require.Nil(t, got, "a pin search that consumes the shared budget must fail closed")
	require.ErrorContains(t, projectErr, "staged migration search budget 25 exhausted")
}

func TestProjectSteadyFakeNUMAStageAtomicallyReplacesInvalidCommittedSourceSnapshotBeyondBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 8)
	target := coresInNUMA(topology, 0, 8, 16)
	desired := map[string]machine.CPUSet{
		"fake":  target,
		"donor": coresInNUMA(topology, 0, 16, 17),
		"share": all.Difference(target).Difference(coresInNUMA(topology, 0, 16, 17)),
	}

	demands := []partitionDemand{
		{
			key: "fake", quantity: target.Size(), eligible: all,
			preferred: current, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "donor", quantity: 1, eligible: all,
			preferred: desired["donor"], class: advisorBlockClassDedicated,
		},
		{
			key: "share", quantity: all.Size() - target.Size() - 1, eligible: all,
			preferred: desired["share"], class: advisorBlockClassShared,
		},
	}
	base := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: current, eligible: all,
			},
			{
				blockID: "donor", class: advisorBlockClassDedicated,
				numaID: 0, cpus: desired["donor"], eligible: all,
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: 0, cpus: desired["share"], eligible: all,
			},
		},
		rawReclaimAggregate: current,
		reclaim:             current,
	}
	for _, tc := range []struct {
		name string
		edit func(*steadyFakeNUMACommittedSnapshot)
	}{
		{
			name: "reclaim union mismatch",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot) {
				snapshot.reclaim = snapshot.reclaim.Difference(machine.NewCPUSet(7))
			},
		},
		{
			name: "fake eligibility violation",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot) {
				committedAssignmentForTest(t, snapshot, "fake").eligible = all.Difference(machine.NewCPUSet(0))
			},
		},
		{
			name: "donor overlap",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot) {
				committedAssignmentForTest(t, snapshot, "donor").cpus = machine.NewCPUSet(0)
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			committed := cloneSteadyFakeNUMACommittedSnapshotForTest(base)
			tc.edit(&committed)
			got, projectErr := projectSteadyFakeNUMAStage(
				demands, []string{"fake"}, committed, desired, nil, topology)

			require.NoError(t, projectErr)
			require.Equal(t, desired, got,
				"invalid committed ownership must be replaced atomically without entering staged search")
		})
	}
}

func TestProjectSteadyFakeNUMAStageAtomicallyReplacesCommittedNUMAFloorViolationBeyondBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 4)
	target0 := coresInNUMA(topology, 0, 4, 6)
	target1 := coresInNUMA(topology, 0, 6, 8)
	target := target0.Union(target1)
	fragmentedFloor := machine.NewCPUSet()
	fragmentedPeer := machine.NewCPUSet()
	for _, coreID := range topology.CPUDetails.CoresInNUMANodes(0).ToSliceInt()[:4] {
		threads := topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()
		fragmentedFloor.Add(threads[0])
		fragmentedPeer.Add(threads[1])
	}
	demands := []partitionDemand{
		{
			key: "fake-0", quantity: target0.Size(),
			eligible:  all,
			preferred: target0, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "fake-1", quantity: target1.Size(),
			eligible:  all,
			preferred: target1, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - target.Size(), eligible: all,
			preferred: all.Difference(current), class: advisorBlockClassShared,
		},
	}
	desired := map[string]machine.CPUSet{
		"fake-0": target0, "fake-1": target1, "share": all.Difference(target),
	}
	committed := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake-0", class: advisorBlockClassMandatoryReclaim,
				numaID: 0, cpus: fragmentedFloor, eligible: all,
			},
			{
				blockID: "fake-1", class: advisorBlockClassMandatoryReclaim,
				numaID: 0, cpus: fragmentedPeer, eligible: all,
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: 0, cpus: all.Difference(current), eligible: all,
			},
		},
		rawReclaimAggregate: current,
		reclaim:             current,
	}

	got, projectErr := projectSteadyFakeNUMAStage(
		demands, []string{"fake-0", "fake-1"}, committed, desired,
		[]partitionCoreFloorConstraint{{
			demandKey: "fake-0", committedBlockID: "fake-0",
		}}, topology)

	require.NoError(t, projectErr)
	require.Equal(t, desired, got,
		"invalid committed floor ownership must be replaced atomically without staged search")
}

func TestProjectSteadyFakeNUMAStagePreservesOriginalDemandContractDuringInvalidCommittedRepair(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	fakeCommitted := coresInNUMA(topology, 0, 0, 1)
	dedicatedCommitted := all.Difference(fakeCommitted)
	fakeDesired := coresInNUMA(topology, 0, 1, 2)
	realDesired := coresInNUMA(topology, 0, 3, 4)
	dedicatedDesired := all.Difference(fakeDesired).Difference(realDesired)

	demands := []partitionDemand{
		{
			key: "fake", quantity: fakeDesired.Size(),
			eligible: all, preferred: fakeCommitted,
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real", quantity: realDesired.Size(),
			eligible: realDesired,
			class:    advisorBlockClassMandatoryReclaim,
		},
		{
			key: "dedicated", quantity: dedicatedDesired.Size(),
			eligible: all, preferred: dedicatedCommitted,
			class: advisorBlockClassDedicated,
		},
	}
	desired := map[string]machine.CPUSet{
		"fake":      fakeDesired,
		"real":      realDesired,
		"dedicated": dedicatedDesired,
	}
	committed := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: fakeCommitted, eligible: all,
			},
			{
				blockID: "real", class: advisorBlockClassMandatoryReclaim,
				numaID: 0, cpus: machine.NewCPUSet(), eligible: realDesired,
			},
			{
				blockID: "dedicated", class: advisorBlockClassDedicated,
				numaID: 0, cpus: dedicatedCommitted, eligible: all,
			},
		},
		rawReclaimAggregate: fakeCommitted,
		reclaim:             fakeCommitted,
	}

	got, projectErr := projectSteadyFakeNUMAStage(
		demands, []string{"fake", "real"}, committed, desired,
		[]partitionCoreFloorConstraint{{
			demandKey: "real", committedBlockID: "real",
		}}, topology)

	require.NoError(t, projectErr)
	for _, demand := range demands {
		require.Equal(t, demand.quantity, got[demand.key].Size(), "demand %q", demand.key)
	}
	require.Equal(t, desired, got,
		"a repair that weakens an original demand must fall back to the atomic desired assignment")
}

func TestProjectSteadyFakeNUMAStageAllowsBudgetedAtomicRepairOfInvalidCommittedSnapshot(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	current := coresInNUMA(topology, 0, 0, 1)
	target := coresInNUMA(topology, 0, 1, 2)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}
	demands := stagedMigrationDemands(all, current, target.Size())
	committed := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: current,
				eligible: all.Difference(machine.NewCPUSet(current.ToSliceInt()[0])),
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: commonstate.FakedNUMAID, cpus: all.Difference(current), eligible: all,
			},
		},
		rawReclaimAggregate: current,
		reclaim:             current,
	}

	got, projectErr := projectSteadyFakeNUMAStage(
		demands, []string{"fake"}, committed, desired, nil, topology)

	require.NoError(t, projectErr)
	require.Equal(t, current, got["fake"])
	require.Equal(t, all.Difference(current), got["share"])
	require.NoError(t, assertCoreAligned(got["fake"], topology))
	require.Zero(t, steadyFakeNUMAMigrationChurn(current, got["fake"]))
}

func TestProjectSteadyFakeNUMAStageAtomicRepairUsesReplacementChurnForPureShrink(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := coresInNUMA(topology, 0, 0, 6)
	target := coresInNUMA(topology, 0, 0, 1)
	desired := map[string]machine.CPUSet{
		"fake": target, "share": all.Difference(target),
	}
	demands := stagedMigrationDemands(all, committed, target.Size())
	committedSnapshot := newSteadyFakeNUMACommittedSnapshotForTest(
		committed,
		all.Difference(machine.NewCPUSet(committed.Difference(target).ToSliceInt()[0])),
	)

	got, projectErr := projectSteadyFakeNUMAStage(
		demands, []string{"fake"}, committedSnapshot, desired, nil, topology)

	require.NoError(t, projectErr)
	require.Equal(t, desired, got)
	require.Zero(t, steadyFakeNUMAMigrationChurn(committed, target),
		"pure shrink has no replacement churn even when its symmetric difference exceeds the budget")
}

func stagedMigrationDemands(
	all, committed machine.CPUSet,
	fakeQuantity int,
) []partitionDemand {
	return []partitionDemand{
		{
			key: "fake", quantity: fakeQuantity, eligible: all,
			preferred: committed, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - fakeQuantity, eligible: all,
			preferred: all.Difference(committed), class: advisorBlockClassShared,
		},
	}
}

func testSteadyFakeNUMATopology(coreCount, cpusPerCore int) *machine.CPUTopology {
	details := make(machine.CPUDetails, coreCount*cpusPerCore)
	for coreID := 0; coreID < coreCount; coreID++ {
		for thread := 0; thread < cpusPerCore; thread++ {
			cpuID := coreID*cpusPerCore + thread
			details[cpuID] = machine.CPUTopoInfo{
				NUMANodeID: 0,
				SocketID:   0,
				CoreID:     coreID,
			}
		}
	}
	return &machine.CPUTopology{
		NumCPUs:      coreCount * cpusPerCore,
		NumCores:     coreCount,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails:   details,
	}
}

func TestSolveSteadyFakeNUMAWholeCoreRepairsFragmentedPreferred(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	complete := coresInNUMA(topology, 0, 0, 3)
	partialA := coresInNUMA(topology, 0, 3, 4).ToSliceInt()
	partialB := coresInNUMA(topology, 0, 4, 5).ToSliceInt()
	oldFake := complete.Union(machine.NewCPUSet(partialA[0], partialB[0]))

	demands := []partitionDemand{
		{
			key:       "fake",
			quantity:  oldFake.Size(),
			eligible:  all,
			preferred: oldFake,
			class:     advisorBlockClassMandatoryReclaim,
		},
		{
			key:       "share",
			quantity:  all.Size() - oldFake.Size(),
			eligible:  all,
			preferred: all.Difference(oldFake),
			class:     advisorBlockClassShared,
		},
	}

	got, err := solveSteadyFakeNUMAWholeCore(demands, []string{"fake"}, topology)
	require.NoError(t, err)
	require.Equal(t, oldFake.Size(), got["fake"].Size())
	require.NoError(t, assertCoreAligned(got["fake"], topology))
	require.LessOrEqual(t, oldFake.Difference(got["fake"]).Size(), steadyFakeNUMAMaxMigratedCPUs)
	require.True(t, got["fake"].Intersection(got["share"]).IsEmpty())
	require.Equal(t, all, got["fake"].Union(got["share"]))
}

func TestSolveSteadyFakeNUMAWholeCoreKeepsAlignedBaseline(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake := coresInNUMA(topology, 0, 2, 6)

	demands := []partitionDemand{
		{
			key:       "fake",
			quantity:  oldFake.Size(),
			eligible:  all,
			preferred: oldFake,
			class:     advisorBlockClassMandatoryReclaim,
		},
		{
			key:       "share",
			quantity:  all.Size() - oldFake.Size(),
			eligible:  all,
			preferred: all.Difference(oldFake),
			class:     advisorBlockClassShared,
		},
	}
	committed := newSteadyFakeNUMACommittedSnapshotForTest(oldFake, all)
	got, err := solveSteadyFakeNUMAWholeCoreWithFloorsAndProject(
		demands, []string{"fake"}, committed, nil, topology,
		projectSteadyFakeNUMAStage)

	require.NoError(t, err)
	require.Equal(t, oldFake, got["fake"])
}

func TestSolveSteadyFakeNUMAWholeCorePreservesNarrowDonor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	narrow := coresInNUMA(topology, 0, 0, 1)
	oldFake := coresInNUMA(topology, 0, 1, 3)

	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{
			key:       "narrow",
			quantity:  narrow.Size(),
			eligible:  narrow,
			preferred: narrow,
			class:     advisorBlockClassDedicated,
		},
		{
			key:       "fake",
			quantity:  oldFake.Size(),
			eligible:  all,
			preferred: oldFake,
			class:     advisorBlockClassMandatoryReclaim,
		},
		{
			key:      "share",
			quantity: all.Size() - narrow.Size() - oldFake.Size(),
			eligible: all,
			class:    advisorBlockClassShared,
		},
	}, []string{"fake"}, topology)

	require.NoError(t, err)
	require.Equal(t, narrow, got["narrow"])
	require.True(t, got["fake"].Intersection(narrow).IsEmpty())
	require.NoError(t, assertCoreAligned(got["fake"], topology))
}

func TestSolveSteadyFakeNUMAWholeCoreRejectsIllegalBaseline(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()

	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{
			key:      "fake",
			quantity: 6,
			eligible: all,
			class:    advisorBlockClassMandatoryReclaim,
		},
		{
			key:      "share",
			quantity: 4,
			eligible: all,
			class:    advisorBlockClassShared,
		},
	}, []string{"fake"}, topology)

	require.Nil(t, got)
	require.ErrorContains(t, err, "steady fake-NUMA baseline")
}

func TestSolveSteadyFakeNUMAWholeCoreRejectsOddQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()

	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{
			key:      "fake",
			quantity: 5,
			eligible: all,
			class:    advisorBlockClassMandatoryReclaim,
		},
		{
			key:      "share",
			quantity: all.Size() - 5,
			eligible: all,
			class:    advisorBlockClassShared,
		},
	}, []string{"fake"}, topology)

	require.Nil(t, got)
	require.ErrorContains(t, err, "not a whole-core multiple")
}

func TestSolveSteadyFakeNUMAWholeCoreAtomicallyRepairsIncompleteCommittedState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake := machine.NewCPUSet()
	for _, coreID := range topology.CPUDetails.Cores().ToSliceInt()[:20] {
		oldFake.Add(topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0])
	}

	demands := []partitionDemand{
		{
			key:       "fake",
			quantity:  oldFake.Size(),
			eligible:  all,
			preferred: oldFake,
			class:     advisorBlockClassMandatoryReclaim,
		},
		{
			key:       "share",
			quantity:  all.Size() - oldFake.Size(),
			eligible:  all,
			preferred: all.Difference(oldFake),
			class:     advisorBlockClassShared,
		},
	}
	got, err := solveSteadyFakeNUMAWholeCoreWithFloorsAndProject(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(oldFake, all),
		nil,
		topology,
		projectSteadyFakeNUMAStage,
	)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, oldFake.Size(), got["fake"].Size())
	require.NoError(t, assertCoreAligned(got["fake"], topology))
}

func TestSolveSteadyFakeNUMAWholeCoreAllowsAlignedExpansionBeyondMigrationBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	fakeEligible := coresInNUMA(topology, 0, 0, 5)

	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{
			key: "fake", quantity: 10, eligible: fakeEligible,
			preferred: machine.NewCPUSet(), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - 10, eligible: all.Difference(fakeEligible),
			preferred: all.Difference(fakeEligible), class: advisorBlockClassShared,
		},
	}, []string{"fake"}, topology)

	require.NoError(t, err)
	require.Equal(t, fakeEligible, got["fake"])
}

func TestSolveSteadyFakeNUMAWholeCoreUsesDonorUnion(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	cores := topology.CPUDetails.Cores().ToSliceInt()
	oldFake := machine.NewCPUSet()
	for _, coreID := range cores[:4] {
		oldFake.Add(topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0])
	}
	donorA := all.Difference(oldFake).Intersection(coresInNUMA(topology, 0, 0, 4))
	donorB := all.Difference(oldFake).Difference(donorA)

	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{
			key:       "fake",
			quantity:  oldFake.Size(),
			eligible:  all,
			preferred: oldFake,
			class:     advisorBlockClassMandatoryReclaim,
		},
		{
			key:       "donor-a",
			quantity:  donorA.Size(),
			eligible:  all,
			preferred: donorA,
			class:     advisorBlockClassShared,
		},
		{
			key:       "donor-b",
			quantity:  donorB.Size(),
			eligible:  all,
			preferred: donorB,
			class:     advisorBlockClassDedicated,
		},
	}, []string{"fake"}, topology)

	require.NoError(t, err)
	require.NoError(t, assertCoreAligned(got["fake"], topology))
	require.Equal(t, donorA.Size(), got["donor-a"].Size())
	require.Equal(t, donorB.Size(), got["donor-b"].Size())
	require.Equal(t, all, got["fake"].Union(got["donor-a"]).Union(got["donor-b"]))
}

func TestSolveSteadyFakeNUMAWholeCoreIsDeterministicAcrossDemandOrder(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake := machine.NewCPUSet(0, 1, 2, 3)
	base := []partitionDemand{
		{key: "fake", quantity: 4, eligible: all, preferred: oldFake, class: advisorBlockClassMandatoryReclaim},
		{key: "share-a", quantity: 4, eligible: all, class: advisorBlockClassShared},
		{key: "share-b", quantity: 8, eligible: all, class: advisorBlockClassShared},
	}
	var want map[string]machine.CPUSet
	for seed := int64(0); seed < 20; seed++ {
		demands := append([]partitionDemand(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(demands), func(i, j int) {
			demands[i], demands[j] = demands[j], demands[i]
		})
		got, solveErr := solveSteadyFakeNUMAWholeCore(
			demands, []string{"fake"}, topology)
		require.NoError(t, solveErr)
		if seed == 0 {
			want = got
		} else {
			require.Equal(t, want, got)
		}
	}
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasPreservesOldCounts(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	eligible := topology.CPUDetails.CPUs()
	old := coresInNUMA(topology, 0, 0, 2).
		Union(coresInNUMA(topology, 1, 0, 3))

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		old.Size(), old, eligible, topology, nil, nil)

	require.NoError(t, err)
	require.Equal(t, map[int]int{0: 4, 1: 6}, quotas)
}

func TestPlanWholeCoreCapacityQuotasScalesAcross1024CPUShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		capacities []int
	}{
		{name: "two NUMAs equal", capacities: []int{512, 512}},
		{name: "two NUMAs asymmetric", capacities: []int{256, 768}},
		{name: "four NUMAs equal", capacities: []int{256, 256, 256, 256}},
		{name: "four NUMAs asymmetric", capacities: []int{128, 192, 320, 384}},
		{name: "eight NUMAs equal", capacities: []int{128, 128, 128, 128, 128, 128, 128, 128}},
		{name: "eight NUMAs asymmetric", capacities: []int{64, 96, 128, 128, 128, 160, 160, 160}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			numaIDs := make([]int, len(tc.capacities))
			capacityByNUMA := make(map[int]int, len(tc.capacities))
			minimumByNUMA := make(map[int]int, len(tc.capacities))
			oldQuotaByNUMA := make(map[int]int, len(tc.capacities))
			for numaID, capacity := range tc.capacities {
				numaIDs[numaID] = numaID
				capacityByNUMA[numaID] = capacity
				oldQuotaByNUMA[numaID] = capacity
			}

			quotas, err := planWholeCoreCapacityQuotas(
				768, 2, numaIDs, capacityByNUMA, minimumByNUMA, oldQuotaByNUMA, true)
			require.NoError(t, err)
			require.NoError(t, validateWholeCoreQuotaSaturation(
				quotas, capacityByNUMA, minimumByNUMA, numaIDs, 2))

			total := 0
			for numaID, quota := range quotas {
				require.Zero(t, quota%2)
				require.LessOrEqual(t, quota, capacityByNUMA[numaID])
				total += quota
			}
			require.Equal(t, 768, total)
		})
	}
}

func TestExpandSteadyFakeNUMAReclaimPhaseBalancesEqualCapacityNUMAs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	all := topology.CPUDetails.CPUs()
	oldPreferred := coresInNUMA(topology, 0, 0, 7).
		Union(coresInNUMA(topology, 1, 0, 21))
	require.Equal(t, 14, oldPreferred.Intersection(
		topology.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 42, oldPreferred.Intersection(
		topology.CPUDetails.CPUsInNUMANodes(1)).Size())

	demands, blockIDByDemandKey, _, err := expandSteadyFakeNUMAReclaimPhase(
		[]advisorBlockDescriptor{{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     56,
			ComponentKey: "fake",
			Eligible:     all,
			OldPreferred: oldPreferred,
		}},
		all,
		topology,
		nil,
	)
	require.NoError(t, err)

	quotaByNUMA := make(map[int]int)
	for _, demand := range demands {
		if blockIDByDemandKey[demand.key] != "fake" {
			continue
		}
		numaIDs := topology.CPUDetails.KeepOnly(demand.eligible).NUMANodes().ToSliceInt()
		require.Len(t, numaIDs, 1)
		quotaByNUMA[numaIDs[0]] += demand.quantity
	}
	require.Equal(t, map[int]int{0: 28, 1: 28}, quotaByNUMA)
}

func TestSolveAdvisorDescriptorPhaseSteadyFakeNUMAStagesTowardBalancedTarget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := coresInNUMA(topology, 0, 0, 7).
		Union(coresInNUMA(topology, 1, 0, 21))
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 56, ComponentKey: "fake",
			Eligible: all, OldPreferred: committed,
		},
		{
			BlockID: "share", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: all.Size() - 56, ComponentKey: "share",
			Eligible: all, OldPreferred: all.Difference(committed),
		},
	}
	setCommittedDescriptorOwnershipForTest(descriptors)
	result := make(map[string]machine.CPUSet)
	transition := steadyFakeNUMAMigrationCheckpointTransition{
		kind: steadyFakeNUMAMigrationCheckpointKeep,
	}

	remaining, err := p.solveAdvisorDescriptorPhaseWithCheckpointTransition(
		descriptors, all, result, true, false, &transition)

	require.NoError(t, err)
	require.True(t, remaining.IsEmpty())
	require.NotNil(t, transition.target,
		"real steady planning must freeze the balanced final target before staging")
	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace, transition.kind)
	target := transition.target.target
	targetByNUMA := map[int]int{
		0: target.Intersection(topology.CPUDetails.CPUsInNUMANodes(0)).Size(),
		1: target.Intersection(topology.CPUDetails.CPUsInNUMANodes(1)).Size(),
	}
	require.Equal(t, map[int]int{0: 28, 1: 28}, targetByNUMA)

	next := result["fake"]
	require.Equal(t, 56, next.Size())
	require.NoError(t, assertCoreAligned(next, topology))
	require.LessOrEqual(t,
		steadyFakeNUMAMigrationChurn(committed, next),
		steadyFakeNUMAMaxMigratedCPUs)
	require.Less(t,
		steadyFakeNUMAMigrationChurn(next, target),
		steadyFakeNUMAMigrationChurn(committed, target))
}

func TestExpandSteadyFakeNUMAReclaimPhaseExcludesRealMandatoryNUMAFromFakePreference(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	realPreferred := coresInNUMA(topology, 0, 0, 1)
	fakePreferred := realPreferred.
		Union(coresInNUMA(topology, 0, 1, 2)).
		Union(coresInNUMA(topology, 1, 0, 1))

	demands, blockIDByDemandKey, _, err := expandSteadyFakeNUMAReclaimPhase(
		[]advisorBlockDescriptor{
			{
				BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
				Quantity: 2, ComponentKey: "real-0",
				Eligible: topology.CPUDetails.CPUsInNUMANodes(0), OldPreferred: realPreferred,
			},
			{
				BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
				Quantity: 4, ComponentKey: "fake", Eligible: all, OldPreferred: fakePreferred,
			},
		},
		all,
		topology,
		nil,
	)
	require.NoError(t, err)

	fakeDemandPreferred := machine.NewCPUSet()
	for _, demand := range demands {
		if blockIDByDemandKey[demand.key] == "fake" {
			fakeDemandPreferred = fakeDemandPreferred.Union(demand.preferred)
		}
	}
	require.False(t, fakeDemandPreferred.IsEmpty())
	require.True(t, fakeDemandPreferred.Equals(
		fakePreferred.Intersection(topology.CPUDetails.CPUsInNUMANodes(1))),
		"fake-NUMA preference must exclude the complete NUMA owned by a real mandatory descriptor")
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasUsesCoreCapacityBeforeOddOldCounts(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()
	old := machine.NewCPUSet(numa0[0], numa0[1], numa0[2], numa1[0], numa1[1], numa1[2])

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		old.Size(), old, topology.CPUDetails.CPUs(), topology, nil, nil)

	require.NoError(t, err)
	require.Equal(t, map[int]int{0: 2, 1: 4}, quotas)
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasFailsWhenFloorsExceedQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		2,
		machine.NewCPUSet(),
		topology.CPUDetails.CPUs(),
		topology,
		nil,
		map[int]int{0: 2, 1: 2},
	)

	require.Nil(t, quotas)
	require.ErrorContains(t, err, "smaller than required steady minimum")
}

func TestSolveAdvisorDescriptorPhaseSteadyFakeNUMAPreservesQuotaAndAlignsUnion(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake := coresInNUMA(topology, 0, 0, 2).
		Union(coresInNUMA(topology, 1, 0, 3))
	oldShare := all.Difference(oldFake)
	result := make(map[string]machine.CPUSet)

	remaining, err := p.solveAdvisorDescriptorPhase([]advisorBlockDescriptor{
		{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     oldFake.Size(),
			ComponentKey: "fake",
			Eligible:     all,
			OldPreferred: oldFake,
		},
		{
			BlockID:      "share",
			Class:        advisorBlockClassShared,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     oldShare.Size(),
			ComponentKey: "share",
			Eligible:     all,
			OldPreferred: oldShare,
		},
	}, all, result, true, false)

	require.NoError(t, err)
	require.True(t, remaining.IsEmpty())
	require.NoError(t, assertCoreAligned(result["fake"], topology))
	require.Equal(t, 4, result["fake"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 6, result["fake"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(1)).Size())
}

func TestSolveAdvisorDescriptorPhaseSteadyFakeNUMARepairsProductionShape(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	cores := topology.CPUDetails.Cores().ToSliceInt()
	require.GreaterOrEqual(t, len(cores), 106)
	oldFake := topology.CPUDetails.CPUsInCores(cores[:100]...)
	for _, coreID := range cores[100:104] {
		siblings := topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()
		oldFake.Add(siblings[0])
	}
	require.Equal(t, 204, oldFake.Size())
	require.Equal(t, 4, fragmentedLogicalCPUCount(oldFake, topology))
	result := make(map[string]machine.CPUSet)

	_, err = p.solveAdvisorDescriptorPhase([]advisorBlockDescriptor{
		{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     oldFake.Size(),
			ComponentKey: "fake",
			Eligible:     all,
			Committed:    oldFake,
			OldPreferred: oldFake,
		},
		{
			BlockID:      "share",
			Class:        advisorBlockClassShared,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     all.Size() - oldFake.Size(),
			ComponentKey: "share",
			Eligible:     all,
			OldPreferred: all.Difference(oldFake),
		},
	}, all, result, true, false)

	require.NoError(t, err)
	require.Equal(t, 204, result["fake"].Size())
	require.Equal(t, 102, wholeCoreCount(result["fake"], topology))
	require.Zero(t, fragmentedLogicalCPUCount(result["fake"], topology))
	require.LessOrEqual(t,
		oldFake.Difference(result["fake"]).Union(result["fake"].Difference(oldFake)).Size(),
		steadyFakeNUMAMaxMigratedCPUs)
}

func TestSolveSteadyFakeNUMADesiredWholeCoreRepairsDC05FlowFixture(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed, err := machine.Parse("0-100,112,128-228,240")
	require.NoError(t, err)
	ordinaryDesired, err := machine.Parse(
		"1,6-8,11-28,32-44,48-60,80-81,96-97,112-113,129,134-154,160-170,176-186,192-193")
	require.NoError(t, err)
	require.Equal(t, 204, committed.Size())
	require.NoError(t, assertCoreAligned(committed, topology))
	require.Equal(t, 100, ordinaryDesired.Size())
	require.Greater(t, fragmentedLogicalCPUCount(ordinaryDesired, topology), 0)

	demands := []partitionDemand{
		{
			key: "fake", quantity: ordinaryDesired.Size(), eligible: all,
			preferred: committed, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - ordinaryDesired.Size(), eligible: all,
			preferred: all.Difference(committed), class: advisorBlockClassShared,
		},
	}
	baseline := map[string]machine.CPUSet{
		"fake":  ordinaryDesired,
		"share": all.Difference(ordinaryDesired),
	}

	got, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, []string{"fake"}, nil, topology, baseline)

	require.NoError(t, err)
	require.Equal(t, ordinaryDesired.Size(), got["fake"].Size())
	require.NoError(t, assertCoreAligned(got["fake"], topology))
	require.True(t, got["fake"].Intersection(got["share"]).IsEmpty())
	require.Equal(t, all, got["fake"].Union(got["share"]))
}

func TestSolveSteadyFakeNUMADesiredWholeCoreRepairsDC05ExpandedEightNUMAFixture(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake, err := machine.Parse("1,16-20,32-127,129,144-148,160-255")
	require.NoError(t, err)
	failedBaseline, err := machine.Parse(
		"1,6-8,11-28,32-44,48-60,68-73,80-127,129,134-154,160-170,176-186,208-255")
	require.NoError(t, err)
	quantitiesByNUMA := map[int]int{
		0: 20,
		1: 24,
		2: 24,
		3: 24,
		4: 6,
		5: 32,
		6: 32,
		7: 32,
	}
	demands := make([]partitionDemand, 0, len(quantitiesByNUMA)+1)
	fakeKeys := make([]string, 0, len(quantitiesByNUMA))
	for numaID, quantity := range quantitiesByNUMA {
		key := fmt.Sprintf("fake-%d", numaID)
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quantity,
			eligible:  numaCPUs,
			preferred: oldFake.Intersection(numaCPUs),
			class:     advisorBlockClassMandatoryReclaim,
		})
		fakeKeys = append(fakeKeys, key)
	}
	demands = append(demands, partitionDemand{
		key:       "share",
		quantity:  all.Size() - failedBaseline.Size(),
		eligible:  all,
		preferred: all.Difference(oldFake),
		class:     advisorBlockClassShared,
	})

	baseline, err := solveDisjointPartitions(demands, topology)
	require.NoError(t, err)
	baselineReclaim := unionPartitionAssignments(baseline, fakeKeys)
	require.Equal(t, failedBaseline.Size(), baselineReclaim.Size())
	require.Greater(t, fragmentedLogicalCPUCount(baselineReclaim, topology), 0)

	got, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, nil, topology, baseline)

	require.NoError(t, err)
	reclaim := unionPartitionAssignments(got, fakeKeys)
	require.Equal(t, oldFake.Size()-10, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.True(t, reclaim.Intersection(got["share"]).IsEmpty())
	require.Equal(t, all, reclaim.Union(got["share"]))
}

func TestSolveSteadyFakeNUMADesiredWholeCoreRepairsDC05ShrinkFixture(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed, err := machine.Parse(
		"0-1,6,16-18,32-129,134,144-146,160-255")
	require.NoError(t, err)
	failedBaseline, err := machine.Parse(
		"1,6-8,11-28,32-44,48-60,68-73,80-127,129,134-154,160-170,176-186,208-255")
	require.NoError(t, err)
	quantitiesByNUMA := map[int]int{
		0: 20,
		1: 24,
		2: 24,
		3: 24,
		4: 6,
		5: 32,
		6: 32,
		7: 32,
	}
	demands := make([]partitionDemand, 0, len(quantitiesByNUMA)+1)
	fakeKeys := make([]string, 0, len(quantitiesByNUMA))
	for numaID, quantity := range quantitiesByNUMA {
		key := fmt.Sprintf("fake-%d", numaID)
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quantity,
			eligible:  numaCPUs,
			preferred: committed.Intersection(numaCPUs),
			class:     advisorBlockClassMandatoryReclaim,
		})
		fakeKeys = append(fakeKeys, key)
	}
	demands = append(demands, partitionDemand{
		key:       "share",
		quantity:  all.Size() - failedBaseline.Size(),
		eligible:  all,
		preferred: all.Difference(committed),
		class:     advisorBlockClassShared,
	})

	baseline, err := solveDisjointPartitions(demands, topology)
	require.NoError(t, err)
	baselineReclaim := unionPartitionAssignments(baseline, fakeKeys)
	require.Equal(t, failedBaseline.Size(), baselineReclaim.Size())
	require.Greater(t, fragmentedLogicalCPUCount(baselineReclaim, topology), 0)

	got, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, nil, topology, baseline)

	require.NoError(t, err)
	reclaim := unionPartitionAssignments(got, fakeKeys)
	require.Equal(t, failedBaseline.Size(), reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.True(t, reclaim.Intersection(got["share"]).IsEmpty())
	require.Equal(t, all, reclaim.Union(got["share"]))
}

func TestProjectSteadyFakeNUMAStageRejectsDC05RampUpWhenSearchBudgetExhausted(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed, err := machine.Parse(
		"1,17-18,33-34,49-50,68-127,129,145-146,161-162,177-178,196-255")
	require.NoError(t, err)
	failedBaseline, err := machine.Parse(
		"1,6-8,11-28,32-44,48-60,68-69,80-81,96-97,112-113,129,134-154,160-170,176-186")
	require.NoError(t, err)
	quantitiesByNUMA := map[int]int{
		0: 20,
		1: 24,
		2: 24,
		3: 24,
		4: 2,
		5: 2,
		6: 2,
		7: 2,
	}
	demands := make([]partitionDemand, 0, len(quantitiesByNUMA)+1)
	fakeKeys := make([]string, 0, len(quantitiesByNUMA))
	for numaID, quantity := range quantitiesByNUMA {
		if quantity == 0 {
			continue
		}
		key := fmt.Sprintf("fake-%d", numaID)
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quantity,
			eligible:  numaCPUs,
			preferred: committed.Intersection(numaCPUs),
			class:     advisorBlockClassMandatoryReclaim,
		})
		fakeKeys = append(fakeKeys, key)
	}
	demands = append(demands, partitionDemand{
		key:       "share",
		quantity:  all.Size() - failedBaseline.Size(),
		eligible:  all,
		preferred: all.Difference(committed),
		class:     advisorBlockClassShared,
	})
	baseline := map[string]machine.CPUSet{"share": all.Difference(failedBaseline)}
	for _, demand := range demands {
		if demand.class == advisorBlockClassMandatoryReclaim {
			baseline[demand.key] = failedBaseline.Intersection(demand.eligible)
		}
	}
	require.Greater(t, fragmentedLogicalCPUCount(
		unionPartitionAssignments(baseline, fakeKeys), topology), 0)

	desired, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, nil, topology, baseline)
	require.NoError(t, err)
	staged, err := projectSteadyFakeNUMAStageWithBudget(
		demands, fakeKeys,
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desired, nil, topology,
		steadyFakeNUMASearchBudget{maxSolveAttempts: 1, maxCandidateActions: 1})

	require.ErrorContains(t, err, "budget")
	require.Nil(t, staged, "budget exhaustion must fail closed without a provisional assignment")
}

func TestProjectSteadyFakeNUMAStageRejectsDC05RampDownWhenSearchBudgetExhausted(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed, err := machine.Parse(
		"1,17-18,33-34,49-50,68-127,129,145-146,161-162,177-178,196-255")
	require.NoError(t, err)
	failedBaseline, err := machine.Parse(
		"1,6-8,11-28,32-44,49,68-69,80-81,96-97,112-113,129,134-154,160-170,177")
	require.NoError(t, err)

	demands := []partitionDemand{
		{
			key: "real-0", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(0),
			preferred: machine.NewCPUSet(1, 129), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real-1-floor", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(1),
			preferred: machine.NewCPUSet(17, 145), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real-1-residual", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(1),
			preferred: machine.NewCPUSet(18, 146), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real-2-floor", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(2),
			preferred: machine.NewCPUSet(33, 161), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real-2-residual", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(2),
			preferred: machine.NewCPUSet(34, 162), class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "real-3", quantity: 2,
			eligible:  topology.CPUDetails.CPUsInNUMANodes(3),
			preferred: machine.NewCPUSet(49, 50, 177, 178), class: advisorBlockClassMandatoryReclaim,
		},
	}
	fakeKeys := []string{
		"real-0", "real-1-floor", "real-1-residual",
		"real-2-floor", "real-2-residual", "real-3",
	}
	globalOnlyPreferred := committed.Difference(machine.NewCPUSet(
		1, 17, 18, 33, 34, 49, 50, 129, 145, 146, 161, 162, 177, 178))
	quantitiesByNUMA := map[int]int{0: 18, 1: 20, 2: 20, 4: 2, 5: 2, 6: 2, 7: 2}
	for numaID, quantity := range quantitiesByNUMA {
		key := fmt.Sprintf("fake-%d", numaID)
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		demands = append(demands, partitionDemand{
			key:       key,
			quantity:  quantity,
			eligible:  numaCPUs,
			preferred: globalOnlyPreferred.Intersection(numaCPUs),
			class:     advisorBlockClassMandatoryReclaim,
		})
		fakeKeys = append(fakeKeys, key)
	}
	demands = append(demands, partitionDemand{
		key:       "share",
		quantity:  all.Size() - failedBaseline.Size(),
		eligible:  all,
		preferred: all.Difference(committed),
		class:     advisorBlockClassShared,
	})
	baseline := map[string]machine.CPUSet{
		"real-0":          machine.NewCPUSet(1, 129),
		"real-1-floor":    machine.NewCPUSet(17, 145),
		"real-1-residual": machine.NewCPUSet(18, 146),
		"real-2-floor":    machine.NewCPUSet(33, 161),
		"real-2-residual": machine.NewCPUSet(34, 162),
		"real-3":          machine.NewCPUSet(49, 177),
		"share":           all.Difference(failedBaseline),
	}
	realBaseline := baseline["real-0"].Union(baseline["real-1-floor"]).
		Union(baseline["real-1-residual"]).Union(baseline["real-2-floor"]).
		Union(baseline["real-2-residual"]).Union(baseline["real-3"])
	for numaID := range quantitiesByNUMA {
		key := fmt.Sprintf("fake-%d", numaID)
		baseline[key] = failedBaseline.Intersection(
			topology.CPUDetails.CPUsInNUMANodes(numaID)).Difference(realBaseline)
	}
	require.Equal(t, 78, unionPartitionAssignments(baseline, fakeKeys).Size())
	require.Greater(t, fragmentedLogicalCPUCount(
		unionPartitionAssignments(baseline, fakeKeys), topology), 0)

	desired, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, fakeKeys, []partitionCoreFloorConstraint{
			{demandKey: "real-0"},
			{demandKey: "real-1-floor"},
			{demandKey: "real-2-floor"},
			{demandKey: "real-3"},
		}, topology, baseline)
	require.NoError(t, err)
	staged, err := projectSteadyFakeNUMAStageWithBudget(
		demands, fakeKeys,
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		desired, nil, topology,
		steadyFakeNUMASearchBudget{maxSolveAttempts: 1, maxCandidateActions: 1})

	require.ErrorContains(t, err, "budget")
	require.Nil(t, staged, "budget exhaustion must fail closed without a provisional assignment")
}

func TestSolveAdvisorDescriptorPhaseSteadyFakeNUMARepairsDC05RampUpTransitionWithRealMandatoryFixture(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	globalPreferred, err := machine.Parse(
		"1,17-18,33-34,49-50,68-127,129,145-146,161-162,177-178,196-255")
	require.NoError(t, err)

	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 0, Quantity: 2, ComponentKey: "real-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(1, 129),
		},
		{
			BlockID: "real-1", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 1, Quantity: 4, ComponentKey: "real-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(17, 18, 145, 146),
		},
		{
			BlockID: "real-2", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 2, Quantity: 4, ComponentKey: "real-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(33, 34, 161, 162),
		},
		{
			BlockID: "real-3", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 3, Quantity: 4, ComponentKey: "real-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(49, 50, 177, 178),
		},
		{
			BlockID: "global", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 86, ComponentKey: "global",
			Eligible:     all,
			OldPreferred: globalPreferred,
		},
		{
			BlockID: "dedicated-0", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(2, 130),
		},
		{
			BlockID: "dedicated-1", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(4, 132),
		},
		{
			BlockID: "dedicated-2", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(3, 131),
		},
		{
			BlockID: "dedicated-3", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(5, 133),
		},
		{
			BlockID: "share", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: all.Size() - 100 - 8,
			ComponentKey: "share", Eligible: all,
			OldPreferred: all.Difference(globalPreferred).
				Difference(machine.NewCPUSet(2, 3, 4, 5, 130, 131, 132, 133)),
		},
	}
	result := make(map[string]machine.CPUSet)

	remaining, err := p.solveAdvisorDescriptorPhase(descriptors, all, result, true, false)

	require.NoError(t, err)
	reclaim := result["real-0"].Union(result["real-1"]).
		Union(result["real-2"]).Union(result["real-3"]).Union(result["global"])
	require.Equal(t, 100, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.True(t, remaining.IsEmpty())
	require.Equal(t, all, reclaim.Union(result["share"]).
		Union(result["dedicated-0"]).Union(result["dedicated-1"]).
		Union(result["dedicated-2"]).Union(result["dedicated-3"]))
}

func TestSolveAdvisorDescriptorPhaseSteadyFakeNUMARepairsDC05RampDownWithRealMandatoryFixture(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	globalPreferred, err := machine.Parse(
		"1,17-18,33-34,49-50,68-127,129,145-146,161-162,177-178,196-255")
	require.NoError(t, err)

	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 0, Quantity: 2, ComponentKey: "real-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(1, 129),
		},
		{
			BlockID: "real-1", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 1, Quantity: 4, ComponentKey: "real-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(17, 18, 145, 146),
		},
		{
			BlockID: "real-2", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 2, Quantity: 4, ComponentKey: "real-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(33, 34, 161, 162),
		},
		{
			BlockID: "real-3", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 3, Quantity: 2, ComponentKey: "real-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(49, 177),
		},
		{
			BlockID: "global", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 66, ComponentKey: "global",
			Eligible:     all,
			OldPreferred: globalPreferred,
		},
		{
			BlockID: "dedicated-0", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(2, 130),
		},
		{
			BlockID: "dedicated-1", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(4, 132),
		},
		{
			BlockID: "dedicated-2", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(3, 131),
		},
		{
			BlockID: "dedicated-3", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(5, 133),
		},
		{
			BlockID: "share", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: all.Size() - 78 - 8,
			ComponentKey: "share", Eligible: all,
			OldPreferred: all.Difference(globalPreferred).
				Difference(machine.NewCPUSet(2, 3, 4, 5, 130, 131, 132, 133)),
		},
	}
	result := make(map[string]machine.CPUSet)

	remaining, err := p.solveAdvisorDescriptorPhase(descriptors, all, result, true, false)

	require.NoError(t, err)
	reclaim := result["real-0"].Union(result["real-1"]).
		Union(result["real-2"]).Union(result["real-3"]).Union(result["global"])
	require.Equal(t, 78, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.True(t, remaining.IsEmpty())
	require.Equal(t, all, reclaim.Union(result["share"]).
		Union(result["dedicated-0"]).Union(result["dedicated-1"]).
		Union(result["dedicated-2"]).Union(result["dedicated-3"]))
}

func TestSolveSteadyFakeNUMADesiredWholeCoreRepairsHighlyFragmentedPreferred(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	fragmented := machine.NewCPUSet()
	for _, coreID := range topology.CPUDetails.Cores().ToSliceInt()[:20] {
		fragmented.Add(topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0])
	}
	demands := []partitionDemand{
		{
			key: "fake", quantity: fragmented.Size(), eligible: all,
			preferred: fragmented, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: all.Size() - fragmented.Size(), eligible: all,
			preferred: all.Difference(fragmented), class: advisorBlockClassShared,
		},
	}
	baseline := map[string]machine.CPUSet{
		"fake": fragmented, "share": all.Difference(fragmented),
	}

	got, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, []string{"fake"}, nil, topology, baseline)

	require.NoError(t, err)
	require.Equal(t, fragmented.Size(), got["fake"].Size())
	require.NoError(t, assertCoreAligned(got["fake"], topology))
}

func TestSolveAdvisorDescriptorPhaseRepairsDC05CheckpointWithinEightChangedCPUs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	oldFake, err := machine.Parse("0-104,112,128-224,240")
	require.NoError(t, err)
	oldByNUMA := make(map[int]int)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		oldByNUMA[numaID] = oldFake.Intersection(
			topology.CPUDetails.CPUsInNUMANodes(numaID)).Size()
	}
	result := make(map[string]machine.CPUSet)

	_, err = p.solveAdvisorDescriptorPhase([]advisorBlockDescriptor{
		{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     oldFake.Size(),
			ComponentKey: "fake",
			Eligible:     all,
			Committed:    oldFake,
			OldPreferred: oldFake,
		},
		{
			BlockID:      "share",
			Class:        advisorBlockClassShared,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     all.Size() - oldFake.Size(),
			ComponentKey: "share",
			Eligible:     all,
			OldPreferred: all.Difference(oldFake),
		},
	}, all, result, true, false)

	require.NoError(t, err)
	require.Equal(t, oldFake.Size(), result["fake"].Size())
	require.NoError(t, assertCoreAligned(result["fake"], topology))
	require.LessOrEqual(t,
		oldFake.Difference(result["fake"]).Union(result["fake"].Difference(oldFake)).Size(),
		steadyFakeNUMAMaxMigratedCPUs)
	for numaID, oldQuantity := range oldByNUMA {
		require.Equal(t, oldQuantity, result["fake"].Intersection(
			topology.CPUDetails.CPUsInNUMANodes(numaID)).Size(), "NUMA %d", numaID)
	}
}

func TestSolveAdvisorDescriptorPhaseConvergesEightNUMASMT2NoResetState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	globalPreferred, err := machine.Parse(
		"68-127,192-255")
	require.NoError(t, err)

	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 0, Quantity: 20, ComponentKey: "real-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(1, 129),
		},
		{
			BlockID: "dedicated-0", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(2, 130),
		},
		{
			BlockID: "dedicated-1", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(5, 133),
		},
		{
			BlockID: "dedicated-2", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(4, 132),
		},
		{
			BlockID: "dedicated-3", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(3, 131),
		},
		{
			BlockID: "snb-0", Class: advisorBlockClassShared,
			NUMAID: 0, Quantity: 4, ComponentKey: "snb-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(0, 128),
		},
		{
			BlockID: "real-1", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 1, Quantity: 24, ComponentKey: "real-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(17, 18, 145, 146),
		},
		{
			BlockID: "snb-1", Class: advisorBlockClassShared,
			NUMAID: 1, Quantity: 4, ComponentKey: "snb-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(16, 144),
		},
		{
			BlockID: "real-2", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 2, Quantity: 22, ComponentKey: "real-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(33, 34, 161, 162),
		},
		{
			BlockID: "snb-2", Class: advisorBlockClassShared,
			NUMAID: 2, Quantity: 10, ComponentKey: "snb-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(32, 160),
		},
		{
			BlockID: "real-3", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 3, Quantity: 24, ComponentKey: "real-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(49, 50, 177, 178),
		},
		{
			BlockID: "snb-3", Class: advisorBlockClassShared,
			NUMAID: 3, Quantity: 4, ComponentKey: "snb-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(48, 176),
		},
		{
			BlockID: "global", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 102, ComponentKey: "global",
			Eligible:     all,
			OldPreferred: globalPreferred,
		},
	}
	baselineDescriptors := append([]advisorBlockDescriptor(nil), descriptors...)
	baselineReclaim, baselineKeys := runSteadyFakeNUMAConvergencePathForTest(
		t, topology, all, baselineDescriptors, "real-0")

	equivalentDescriptors := append([]advisorBlockDescriptor(nil), descriptors...)
	for i := range equivalentDescriptors {
		equivalentDescriptors[i].BlockID = "equivalent-" + equivalentDescriptors[i].BlockID
	}
	equivalentReclaim, equivalentKeys := runSteadyFakeNUMAConvergencePathForTest(
		t, topology, all, equivalentDescriptors, "equivalent-real-0")

	require.NotEqual(t, baselineKeys, equivalentKeys,
		"changing external block IDs must rebuild internal demand keys")
	require.Equal(t, baselineReclaim, equivalentReclaim,
		"equivalent external block IDs must converge to the same reclaim CPUSet")
}

func runSteadyFakeNUMAConvergencePathForTest(
	t *testing.T,
	topology *machine.CPUTopology,
	all machine.CPUSet,
	descriptors []advisorBlockDescriptor,
	real0BlockID string,
) (machine.CPUSet, [2]string) {
	t.Helper()

	dir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	checkpointPath := steadyFakeNUMACheckpointFile(p)
	realCommitted := machine.NewCPUSet()
	globalCommitted := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassMandatoryReclaim {
			continue
		}
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			globalCommitted = globalCommitted.Union(descriptor.OldPreferred)
		} else {
			realCommitted = realCommitted.Union(descriptor.OldPreferred)
		}
	}
	require.True(t, globalCommitted.Intersection(realCommitted).IsEmpty(),
		"global committed CPUs must be disjoint from real-NUMA committed blocks")

	demands, blockIDByDemandKey, floors, err := expandSteadyFakeNUMAReclaimPhase(
		descriptors, all, topology, nil)
	require.NoError(t, err)
	var real0FloorKey string
	for _, floor := range floors {
		if floor.committedBlockID == real0BlockID {
			real0FloorKey = floor.demandKey
			break
		}
	}
	require.NotEmpty(t, real0FloorKey)
	remainingDemands := make(map[string]partitionDemand)
	for _, demand := range demands {
		if blockIDByDemandKey[demand.key] == real0BlockID {
			remainingDemands[demand.key] = demand
		}
	}
	floor, found := remainingDemands[real0FloorKey]
	require.True(t, found)
	delete(remainingDemands, real0FloorKey)
	require.Len(t, remainingDemands, 1)
	var real0ResidualKey string
	var residual partitionDemand
	for key, demand := range remainingDemands {
		real0ResidualKey = key
		residual = demand
		delete(remainingDemands, key)
	}
	require.Empty(t, remainingDemands)
	require.Equal(t, machine.NewCPUSet(1, 129), floor.preferred)
	require.Equal(t, machine.NewCPUSet(1, 129), residual.preferred)

	var reclaimDemandKeys []string
	for _, demand := range demands {
		if demand.class == advisorBlockClassMandatoryReclaim {
			reclaimDemandKeys = append(reclaimDemandKeys, demand.key)
		}
	}
	baseline, err := solveDisjointPartitionsWithCoreFloors(demands, floors, topology)
	require.NoError(t, err)
	targetAssignments, err := solveSteadyFakeNUMADesiredWholeCore(
		demands, reclaimDemandKeys, floors, topology, baseline)
	require.NoError(t, err)
	targetReclaim := unionPartitionAssignments(targetAssignments, reclaimDemandKeys)
	removable := coreAlignedCandidates(
		topology, targetReclaim.Difference(realCommitted), machine.NewCPUSet())
	addable := coreAlignedCandidates(
		topology, all.Difference(targetReclaim), machine.NewCPUSet())
	require.GreaterOrEqual(t, len(removable), 3)
	require.GreaterOrEqual(t, len(addable), 3)
	initial := targetReclaim.Clone()
	for i := 0; i < 3; i++ {
		initial = initial.Difference(removable[i].cpus).Union(addable[i].cpus)
	}
	require.Equal(t, 12, steadyFakeNUMAMigrationChurn(initial, targetReclaim))
	for i := range descriptors {
		descriptors[i].Committed = machine.NewCPUSet()
		if descriptors[i].Class != advisorBlockClassMandatoryReclaim {
			continue
		}
		if descriptors[i].NUMAID == commonstate.FakedNUMAID {
			descriptors[i].Committed = initial.Clone()
		} else {
			descriptors[i].Committed = descriptors[i].OldPreferred.Clone()
		}
	}
	committed := initial
	var durableTarget machine.CPUSet
	converged := false
	maxCycles := 0
	for cycle := 0; ; cycle++ {
		if maxCycles > 0 {
			require.Less(t, cycle, maxCycles,
				"no-reset migration exceeded its initial-churn bound")
		}
		previous := committed
		cycleResult := make(map[string]machine.CPUSet)

		transition := steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointKeep,
		}
		beforeTarget := cloneSteadyFakeNUMAMigrationTargetForTest(
			p.steadyFakeNUMAMigration.current())
		remaining, solveErr := p.solveAdvisorDescriptorPhaseWithCheckpointTransition(
			descriptors, all, cycleResult, true, false, &transition)
		require.NoError(t, solveErr)
		require.Equal(t, beforeTarget, p.steadyFakeNUMAMigration.current(),
			"planning must not mutate the in-memory checkpoint")
		require.True(t, remaining.IsSubsetOf(all))
		requireExactDescriptorQuantitiesForTest(t, descriptors, cycleResult)
		requireDisjointEligibleAssignmentsForTest(t, descriptors, cycleResult)
		require.Contains(t, cycleResult, real0BlockID)
		require.NotContains(t, cycleResult, real0FloorKey)
		require.NotContains(t, cycleResult, real0ResidualKey,
			"internal floor/residual keys must be rebuilt into the external block ID")

		next := unionMandatoryReclaimBlocksFromResultForTest(descriptors, cycleResult)
		require.NoError(t, assertCoreAligned(next, topology))
		require.LessOrEqual(t,
			steadyFakeNUMAMigrationChurn(previous, next),
			steadyFakeNUMAMaxMigratedCPUs)
		requireRealNUMAFloorsForTest(t, topology, descriptors, cycleResult)
		require.NoError(t, p.steadyFakeNUMAMigration.applyTransition(transition))

		if cycle == 0 {
			require.NotNil(t, p.steadyFakeNUMAMigration.current())
			durableTarget = p.steadyFakeNUMAMigration.current().target.Clone()
			initialChurn := steadyFakeNUMAMigrationChurn(initial, durableTarget)
			require.Greater(t, initialChurn,
				steadyFakeNUMAMaxMigratedCPUs)
			maxCycles = 2 + initialChurn/steadyFakeNUMAMaxMigratedCPUs
			require.NotEqual(t, durableTarget, next,
				"the production shape must exercise at least one intermediate stage")
		}

		committed = next
		updateDescriptorOldPreferredForTest(descriptors, cycleResult)
		convergedThisCycle := committed.Equals(durableTarget) &&
			p.steadyFakeNUMAMigration.current() == nil
		if convergedThisCycle {
			require.Nil(t, p.steadyFakeNUMAMigration.current(),
				"convergence must clear the in-memory durable target")
			require.NoFileExists(t, checkpointPath,
				"convergence must remove the durable target checkpoint")
		} else {
			require.NotNil(t, p.steadyFakeNUMAMigration.current(),
				"every intermediate stage must retain the durable target")
			require.Equal(t, durableTarget, p.steadyFakeNUMAMigration.current().target,
				"every intermediate stage must retain the first-round target")
			require.FileExists(t, checkpointPath,
				"every intermediate stage must persist the durable target")
		}

		restarted, restartErr := getTestDynamicPolicyWithoutInitialization(topology, dir)
		require.NoError(t, restartErr)
		if convergedThisCycle {
			require.Nil(t, restarted.steadyFakeNUMAMigration.current(),
				"a restart after convergence must not restore a durable target")
			require.NoFileExists(t, checkpointPath)
			converged = true
			break
		}
		require.NotNil(t, restarted.steadyFakeNUMAMigration.current(),
			"an intermediate-stage restart must restore the durable target")
		require.Equal(t, durableTarget, restarted.steadyFakeNUMAMigration.current().target,
			"an intermediate-stage restart must restore the first-round target")
		p = restarted
	}
	require.True(t, converged, "no-reset migration did not converge within %d cycles", maxCycles)
	return committed, [2]string{real0FloorKey, real0ResidualKey}
}

func requireExactDescriptorQuantitiesForTest(
	t *testing.T,
	descriptors []advisorBlockDescriptor,
	result map[string]machine.CPUSet,
) {
	t.Helper()
	for _, descriptor := range descriptors {
		require.Equal(t, descriptor.Quantity, result[descriptor.BlockID].Size(),
			"block %q", descriptor.BlockID)
	}
}

func requireDisjointEligibleAssignmentsForTest(
	t *testing.T,
	descriptors []advisorBlockDescriptor,
	result map[string]machine.CPUSet,
) {
	t.Helper()
	used := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		cpus := result[descriptor.BlockID]
		require.True(t, cpus.IsSubsetOf(descriptor.Eligible), "block %q", descriptor.BlockID)
		require.True(t, used.Intersection(cpus).IsEmpty(), "block %q overlaps", descriptor.BlockID)
		used = used.Union(cpus)
	}
}

func requireRealNUMAFloorsForTest(
	t *testing.T,
	topology *machine.CPUTopology,
	descriptors []advisorBlockDescriptor,
	result map[string]machine.CPUSet,
) {
	t.Helper()
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassMandatoryReclaim ||
			descriptor.NUMAID == commonstate.FakedNUMAID {
			continue
		}
		cpus := result[descriptor.BlockID]
		require.NotEmpty(t, coreAlignedCandidates(topology, cpus, cpus),
			"real-NUMA reclaim block %q lost its complete-core floor", descriptor.BlockID)
	}
}

func unionMandatoryReclaimBlocksFromResultForTest(
	descriptors []advisorBlockDescriptor,
	result map[string]machine.CPUSet,
) machine.CPUSet {
	reclaim := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if descriptor.Class == advisorBlockClassMandatoryReclaim {
			reclaim = reclaim.Union(result[descriptor.BlockID])
		}
	}
	return reclaim
}

func updateDescriptorOldPreferredForTest(
	descriptors []advisorBlockDescriptor,
	result map[string]machine.CPUSet,
) {
	for i := range descriptors {
		descriptors[i].OldPreferred = result[descriptors[i].BlockID].Clone()
	}
	setCommittedDescriptorOwnershipForTest(descriptors)
}

func setCommittedDescriptorOwnershipForTest(descriptors []advisorBlockDescriptor) {
	rawAggregate := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if descriptor.Class == advisorBlockClassMandatoryReclaim {
			rawAggregate = rawAggregate.Union(descriptor.OldPreferred)
		}
	}
	for i := range descriptors {
		descriptors[i].Committed = descriptors[i].OldPreferred.Clone()
		if descriptors[i].Class == advisorBlockClassMandatoryReclaim &&
			descriptors[i].NUMAID == commonstate.FakedNUMAID {
			descriptors[i].Committed = rawAggregate.Clone()
		}
	}
}

func TestSolveAdvisorDescriptorPhaseAtomicallyRepairsDC05GlobalSeedpoolBeyondFixedBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	globalPreferred, err := machine.Parse(
		"1,17-18,33-34,49-50,68-127,129,145-146,161-162,177-178,192-255")
	require.NoError(t, err)

	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 0, Quantity: 20, ComponentKey: "real-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(1, 129),
		},
		{
			BlockID: "dedicated-0", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(2, 130),
		},
		{
			BlockID: "dedicated-1", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(5, 133),
		},
		{
			BlockID: "dedicated-2", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(4, 132),
		},
		{
			BlockID: "dedicated-3", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(3, 131),
		},
		{
			BlockID: "snb-0", Class: advisorBlockClassShared,
			NUMAID: 0, Quantity: 4, ComponentKey: "snb-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			OldPreferred: machine.NewCPUSet(0, 128),
		},
		{
			BlockID: "real-1", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 1, Quantity: 24, ComponentKey: "real-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(17, 18, 145, 146),
		},
		{
			BlockID: "snb-1", Class: advisorBlockClassShared,
			NUMAID: 1, Quantity: 4, ComponentKey: "snb-1",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(1),
			OldPreferred: machine.NewCPUSet(16, 144),
		},
		{
			BlockID: "real-2", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 2, Quantity: 20, ComponentKey: "real-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(33, 34, 161, 162),
		},
		{
			BlockID: "snb-2", Class: advisorBlockClassShared,
			NUMAID: 2, Quantity: 12, ComponentKey: "snb-2",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(2),
			OldPreferred: machine.NewCPUSet(32, 160),
		},
		{
			BlockID: "real-3", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 3, Quantity: 24, ComponentKey: "real-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(49, 50, 177, 178),
		},
		{
			BlockID: "snb-3", Class: advisorBlockClassShared,
			NUMAID: 3, Quantity: 4, ComponentKey: "snb-3",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(3),
			OldPreferred: machine.NewCPUSet(48, 176),
		},
		{
			BlockID: "global", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 102, ComponentKey: "global",
			Eligible:     all,
			OldPreferred: globalPreferred,
		},
		{
			BlockID: "seedpool-0", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: 4, ComponentKey: "seedpool-0",
			Eligible:     all,
			OldPreferred: machine.NewCPUSet(64),
		},
		{
			BlockID: "seedpool-1", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: 4, ComponentKey: "seedpool-1",
			Eligible:     all,
			OldPreferred: machine.NewCPUSet(65),
		},
		{
			BlockID: "seedpool-2", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: 4, ComponentKey: "seedpool-2",
			Eligible:     all,
			OldPreferred: machine.NewCPUSet(66),
		},
		{
			BlockID: "seedpool-3", Class: advisorBlockClassShared,
			NUMAID: commonstate.FakedNUMAID, Quantity: 4, ComponentKey: "seedpool-3",
			Eligible:     all,
			OldPreferred: machine.NewCPUSet(67),
		},
	}
	result := make(map[string]machine.CPUSet)
	setCommittedDescriptorOwnershipForTest(descriptors)

	remaining, err := p.solveAdvisorDescriptorPhase(descriptors, all, result, true, false)

	require.NoError(t, err)
	require.NotEmpty(t, result)
	require.True(t, remaining.IsSubsetOf(all))
}

func TestSolveSteadyFakeNUMAWholeCoreSupportsSMT1AndSMT4(t *testing.T) {
	t.Parallel()

	smt1, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	smt4 := &machine.CPUTopology{
		NumCPUs: 8, NumCores: 2, NumSockets: 1, NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			5: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			6: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			7: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		},
	}

	for _, tc := range []struct {
		name     string
		topology *machine.CPUTopology
		old      machine.CPUSet
	}{
		{name: "SMT1", topology: smt1, old: machine.NewCPUSet(0, 2, 3)},
		{name: "SMT4 fragmented repair", topology: smt4, old: machine.NewCPUSet(0, 1, 4, 5)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := tc.topology.CPUDetails.CPUs()
			got, solveErr := solveSteadyFakeNUMAWholeCore([]partitionDemand{
				{
					key: "fake", quantity: tc.old.Size(), eligible: all,
					preferred: tc.old, class: advisorBlockClassMandatoryReclaim,
				},
				{
					key: "share", quantity: all.Size() - tc.old.Size(), eligible: all,
					preferred: all.Difference(tc.old), class: advisorBlockClassShared,
				},
			}, []string{"fake"}, tc.topology)
			require.NoError(t, solveErr)
			require.NoError(t, assertCoreAligned(got["fake"], tc.topology))
			require.Equal(t, tc.old.Size(), got["fake"].Size())
		})
	}
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasRoutesAroundFragmentedNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0Cores := topology.CPUDetails.CoresInNUMANodes(0).ToSliceInt()
	fragmentedNUMA0 := machine.NewCPUSet(
		topology.CPUDetails.CPUsInCores(numa0Cores[0]).ToSliceInt()[0],
		topology.CPUDetails.CPUsInCores(numa0Cores[1]).ToSliceInt()[0],
	)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	eligible := fragmentedNUMA0.Union(numa1)

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		4, fragmentedNUMA0, eligible, topology, nil, map[int]int{1: 2})

	require.NoError(t, err)
	require.Equal(t, map[int]int{0: 0, 1: 4}, quotas)
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasRejectsFloorWithoutCompleteCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0Cores := topology.CPUDetails.CoresInNUMANodes(0).ToSliceInt()
	fragmentedNUMA0 := machine.NewCPUSet(
		topology.CPUDetails.CPUsInCores(numa0Cores[0]).ToSliceInt()[0],
		topology.CPUDetails.CPUsInCores(numa0Cores[1]).ToSliceInt()[0],
	)
	eligible := fragmentedNUMA0.Union(coresInNUMA(topology, 1, 0, 1))

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		4, machine.NewCPUSet(), eligible, topology, nil, map[int]int{0: 2, 1: 2})

	require.Nil(t, quotas)
	require.ErrorContains(t, err, "maximum 0 is smaller than minimum 2")
}

func TestPlanSteadyFakeNUMACoreCapacityQuotasAllowsCapacitySaturatedImbalance(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	eligible := coresInNUMA(topology, 0, 0, 1).
		Union(coresInNUMA(topology, 1, 0, 3))

	quotas, err := planSteadyFakeNUMACoreCapacityQuotas(
		8, eligible, eligible, topology, nil, map[int]int{0: 2, 1: 2})

	require.NoError(t, err)
	require.Equal(t, map[int]int{0: 2, 1: 6}, quotas)
}

func TestSolveSteadyFakeNUMAWholeCoreIsStableAcrossBlockIDChurn(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	old := coresInNUMA(topology, 0, 2, 6)
	solve := func(fakeKey string) machine.CPUSet {
		got, solveErr := solveSteadyFakeNUMAWholeCore([]partitionDemand{
			{
				key: fakeKey, quantity: old.Size(), eligible: all,
				preferred: old, class: advisorBlockClassMandatoryReclaim,
			},
			{
				key: "share", quantity: all.Size() - old.Size(), eligible: all,
				preferred: all.Difference(old), class: advisorBlockClassShared,
			},
		}, []string{fakeKey}, topology)
		require.NoError(t, solveErr)
		return got[fakeKey]
	}

	require.Equal(t, solve("fake-old-id"), solve("fake-new-id"))
}

func TestSolveSteadyFakeNUMAWholeCoreAcceptsZeroQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	got, err := solveSteadyFakeNUMAWholeCore([]partitionDemand{
		{key: "fake", quantity: 0, eligible: all, class: advisorBlockClassMandatoryReclaim},
		{key: "share", quantity: all.Size(), eligible: all, class: advisorBlockClassShared},
	}, []string{"fake"}, topology)

	require.NoError(t, err)
	require.True(t, got["fake"].IsEmpty())
	require.Equal(t, all, got["share"])
}

func TestSolveSteadyFakeNUMAWholeCoreRepairsAfterSiblingBecomesIneligible(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	old := coresInNUMA(topology, 0, 0, 1)
	oldThreads := old.ToSliceInt()
	available := all.Difference(machine.NewCPUSet(oldThreads[1]))
	demands := []partitionDemand{
		{
			key: "fake", quantity: 2, eligible: available,
			preferred: old, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "share", quantity: available.Size() - 2, eligible: available,
			preferred: available.Difference(old), class: advisorBlockClassShared,
		},
	}
	got, err := solveSteadyFakeNUMAWholeCore(demands, []string{"fake"}, topology)

	require.NoError(t, err)
	require.NoError(t, assertCoreAligned(got["fake"], topology))
	require.True(t, got["fake"].Intersection(machine.NewCPUSet(oldThreads[1])).IsEmpty())
	repeated, err := solveSteadyFakeNUMAWholeCore(demands, []string{"fake"}, topology)
	require.NoError(t, err)
	require.Equal(t, got["fake"], repeated["fake"])
}
