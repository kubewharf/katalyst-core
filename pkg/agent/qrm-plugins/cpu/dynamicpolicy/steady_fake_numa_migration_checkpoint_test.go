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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSteadyFakeNUMAMigrationBudgetIsEightCPUIds(t *testing.T) {
	t.Parallel()

	require.Equal(t, 8, steadyFakeNUMAMaxMigratedCPUs)
}

func TestProjectSteadyFakeNUMAStageWithCheckpointKeepsFixedReplacementChurnBudget(t *testing.T) {
	t.Parallel()

	t.Run("large topology does not increase budget", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
		require.NoError(t, err)
		all := topology.CPUDetails.CPUs()
		committed := coresInNUMA(topology, 0, 0, 6)
		target := coresInNUMA(topology, 0, 6, 12)
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)

		next, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
			stagedMigrationDemands(all, committed, target.Size()),
			[]string{"fake"},
			newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
			map[string]machine.CPUSet{
				"fake":  target,
				"share": all.Difference(target),
			},
			nil,
		)

		require.NoError(t, err)
		require.LessOrEqual(t,
			steadyFakeNUMAMigrationChurn(committed, next["fake"]),
			steadyFakeNUMAMaxMigratedCPUs)
		require.NotNil(t, policy.steadyFakeNUMAMigration.current())
		require.Equal(t, target, policy.steadyFakeNUMAMigration.current().target)
	})

	t.Run("offsetting demand shrink and grow do not increase budget", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(20, 1, 1)
		require.NoError(t, err)
		all := topology.CPUDetails.CPUs()
		oldShrink := coresInNUMA(topology, 0, 0, 6)
		oldGrow := machine.NewCPUSet()
		committed := oldShrink.Union(oldGrow)
		targetShrink := coresInNUMA(topology, 0, 6, 7)
		targetGrow := coresInNUMA(topology, 0, 7, 12)
		target := targetShrink.Union(targetGrow)
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		demands := []partitionDemand{
			{
				key: "shrink", quantity: targetShrink.Size(), eligible: all,
				preferred: oldShrink, class: advisorBlockClassMandatoryReclaim,
			},
			{
				key: "grow", quantity: targetGrow.Size(), eligible: all,
				preferred: oldGrow, class: advisorBlockClassMandatoryReclaim,
			},
			{
				key: "share", quantity: all.Size() - target.Size(), eligible: all,
				preferred: all.Difference(committed), class: advisorBlockClassShared,
			},
		}

		next, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
			demands,
			[]string{"shrink", "grow"},
			newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
			map[string]machine.CPUSet{
				"shrink": targetShrink,
				"grow":   targetGrow,
				"share":  all.Difference(target),
			},
			nil,
		)

		require.NoError(t, err)
		nextReclaim := next["shrink"].Union(next["grow"])
		require.LessOrEqual(t,
			steadyFakeNUMAMigrationChurn(committed, nextReclaim),
			steadyFakeNUMAMaxMigratedCPUs)
		require.NotNil(t, policy.steadyFakeNUMAMigration.current())
		require.Equal(t, target, policy.steadyFakeNUMAMigration.current().target)
	})
}

func TestSteadyFakeNUMAMigrationTargetSurvivesRestart(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	dir := t.TempDir()
	first, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	want := &steadyFakeNUMAMigrationIntent{
		constraintDigest: "stable-constraints",
		target:           machine.NewCPUSet(0, 1, 4, 5),
	}
	require.NoError(t, first.steadyFakeNUMAMigration.storeIntent(want))

	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)

	require.Equal(t, want.constraintDigest, restarted.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, want.target, restarted.steadyFakeNUMAMigration.current().target)
}

func TestSteadyFakeNUMAMigrationTargetCorruptionFailsInitialization(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName),
		[]byte("{broken"),
		0o600,
	))

	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)

	require.Nil(t, policy)
	require.ErrorContains(t, err, "restore steady fake-NUMA migration target")
	require.FileExists(t, filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName))
}

func TestSteadyFakeNUMAConstraintDigestChangesOnlyWithConstraints(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	demands := []partitionDemand{{
		key: "fake", quantity: 4,
		eligible:  machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		preferred: machine.NewCPUSet(0, 1, 2, 3),
		class:     advisorBlockClassMandatoryReclaim,
	}}

	first, err := steadyFakeNUMAConstraintDigest(demands, []string{"fake"}, nil, topology)
	require.NoError(t, err)
	demands[0].preferred = machine.NewCPUSet(2, 3, 4, 5)
	same, err := steadyFakeNUMAConstraintDigest(demands, []string{"fake"}, nil, topology)
	require.NoError(t, err)
	require.Equal(t, first, same, "committed placement is progress, not a target constraint")

	demands[0].eligible = machine.NewCPUSet(2, 3, 4, 5, 6, 7)
	changed, err := steadyFakeNUMAConstraintDigest(demands, []string{"fake"}, nil, topology)
	require.NoError(t, err)
	require.NotEqual(t, first, changed)
}

func TestSteadyFakeNUMAMigrationTargetContinuesAfterRestartAndIsReplacedOnConstraintChange(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	initial := coresInNUMA(topology, 0, 0, 6)
	targetA := coresInNUMA(topology, 0, 6, 12)
	targetB := coresInNUMA(topology, 0, 3, 9)
	dir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)

	demands := stagedMigrationDemands(all, initial, targetA.Size())
	first, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(initial, all),
		map[string]machine.CPUSet{"fake": targetA, "share": all.Difference(targetA)},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, targetA, policy.steadyFakeNUMAMigration.current().target)
	require.FileExists(t, filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName))

	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	committed := first["fake"]
	demands = stagedMigrationDemands(all, committed, targetA.Size())
	second, err := restarted.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		map[string]machine.CPUSet{"fake": targetB, "share": all.Difference(targetB)},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, targetA, restarted.steadyFakeNUMAMigration.current().target,
		"same constraints must continue toward the durable target, not a current-state-biased recomputation")
	require.Less(t,
		steadyFakeNUMAMigrationChurn(second["fake"], targetA),
		steadyFakeNUMAMigrationChurn(committed, targetA))

	changedDemands := stagedMigrationDemands(all, second["fake"], targetB.Size())
	changedDemands[0].requestGroupKey = "changed-constraint"
	_, err = restarted.projectSteadyFakeNUMAStageWithCheckpointForTest(
		changedDemands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(second["fake"], all),
		map[string]machine.CPUSet{"fake": targetB, "share": all.Difference(targetB)},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, targetB, restarted.steadyFakeNUMAMigration.current().target,
		"constraint changes must atomically replace the durable target")
}

func TestPlanSteadyFakeNUMAStageWithCheckpointIgnoresStaleTargetOwnedByFixedDemand(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committedFake := coresInNUMA(topology, 0, 0, 6)
	fixed := coresInNUMA(topology, 0, 6, 12)
	freshTarget := coresInNUMA(topology, 0, 12, 18)
	share := all.Difference(committedFake).Difference(fixed)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{
			constraintDigest: "stale-constraints",
			target:           fixed,
		}))

	demands := []partitionDemand{
		{
			key: "fake", quantity: freshTarget.Size(), eligible: all,
			preferred: committedFake, class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "fixed", quantity: fixed.Size(), eligible: fixed,
			preferred: fixed, class: advisorBlockClassDedicated,
		},
		{
			key: "share", quantity: share.Size(), eligible: all,
			preferred: share, class: advisorBlockClassShared,
		},
	}
	committed := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake", class: advisorBlockClassMandatoryReclaim,
				numaID: commonstate.FakedNUMAID, cpus: committedFake, eligible: all,
			},
			{
				blockID: "fixed", class: advisorBlockClassDedicated,
				numaID: 0, cpus: fixed, eligible: fixed,
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: commonstate.FakedNUMAID, cpus: share, eligible: all,
			},
		},
		rawReclaimAggregate: committedFake,
		reclaim:             committedFake,
	}
	freshDesired := map[string]machine.CPUSet{
		"fake":  freshTarget,
		"fixed": fixed,
		"share": all.Difference(freshTarget).Difference(fixed),
	}

	assignments, transition, err := policy.planSteadyFakeNUMAStageWithCheckpoint(
		demands, []string{"fake"}, committed, freshDesired, nil)

	require.NoError(t, err,
		"a stale target must not be validated against ownership under new constraints")
	require.Equal(t, fixed, assignments["fixed"])
	require.True(t, assignments["fake"].Intersection(fixed).IsEmpty())
	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace, transition.kind)
	require.NotNil(t, transition.target)
	require.Equal(t, freshTarget, transition.target.target)
	require.NotEqual(t, "stale-constraints", transition.target.constraintDigest)
}

func TestSteadyFakeNUMAMigrationTargetIsRemovedAtConvergence(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	target := coresInNUMA(topology, 0, 6, 12)
	dir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	demands := stagedMigrationDemands(all, target, target.Size())
	digest, err := steadyFakeNUMAConstraintDigest(demands, []string{"fake"}, nil, topology)
	require.NoError(t, err)
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(&steadyFakeNUMAMigrationIntent{
		constraintDigest: digest,
		target:           target,
	}))

	got, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(target, all),
		map[string]machine.CPUSet{"fake": target, "share": all.Difference(target)},
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, target, got["fake"])
	require.Nil(t, policy.steadyFakeNUMAMigration.current())
	require.NoFileExists(t, filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName))
}

func TestSteadyFakeNUMAMigrationTargetIsNotStoredForAtomicCommittedRepair(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := coresInNUMA(topology, 0, 0, 8)
	target := coresInNUMA(topology, 0, 8, 16)
	dir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	demands := stagedMigrationDemands(all, committed, target.Size())
	snapshot := newSteadyFakeNUMACommittedSnapshotForTest(
		committed, all.Difference(machine.NewCPUSet(committed.ToSliceInt()[0])))

	got, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		snapshot,
		map[string]machine.CPUSet{"fake": target, "share": all.Difference(target)},
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, committed, got["fake"])
	require.Equal(t, all.Difference(committed), got["share"])
	require.Nil(t, policy.steadyFakeNUMAMigration.current())
	require.NoFileExists(t, filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName))
}

func TestPlanSteadyFakeNUMAStageWithCheckpointReturnsTypedReplaceWithoutSideEffects(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := coresInNUMA(topology, 0, 0, 6)
	oldTarget := coresInNUMA(topology, 0, 6, 12)
	freshTarget := coresInNUMA(topology, 0, 12, 18)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: "old", target: oldTarget}))
	beforeTarget := cloneSteadyFakeNUMAMigrationTargetForTest(
		policy.steadyFakeNUMAMigration.current())
	beforeBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)

	demands := stagedMigrationDemands(all, committed, freshTarget.Size())
	demands[0].requestGroupKey = "new-constraints"
	assignments, transition, err := policy.planSteadyFakeNUMAStageWithCheckpoint(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		map[string]machine.CPUSet{
			"fake":  freshTarget,
			"share": all.Difference(freshTarget),
		},
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, assignments)
	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace, transition.kind)
	require.NotNil(t, transition.target)
	require.Equal(t, freshTarget, transition.target.target)
	require.Equal(t, beforeTarget, policy.steadyFakeNUMAMigration.current())
	afterBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes)
}

func TestCommitPendingAdvisorStateAppliesCheckpointTransitionOnlyAfterSuccessfulCAS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		expectedRevision func(uint64) uint64
		entries          func(*machine.CPUTopology) state.PodEntries
		wantError        string
	}{
		{
			name:             "precommit failure",
			expectedRevision: func(revision uint64) uint64 { return revision },
			entries: func(topology *machine.CPUTopology) state.PodEntries {
				core := coresInNUMA(topology, 0, 0, 1)
				fragmented := machine.NewCPUSet(core.ToSliceInt()[0])
				return precommitPartitionEntries(
					fragmented, coresInNUMA(topology, 0, 1, 2))
			},
			wantError: "is not core-aligned",
		},
		{
			name:             "revision CAS failure",
			expectedRevision: func(revision uint64) uint64 { return revision + 1 },
			entries: func(topology *machine.CPUTopology) state.PodEntries {
				return precommitPartitionEntries(
					coresInNUMA(topology, 0, 0, 1),
					coresInNUMA(topology, 0, 1, 2))
			},
			wantError: "revision",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			topology := policy.machine.machineInfo.CPUTopology
			oldTarget := coresInNUMA(topology, 0, 2, 3)
			newTarget := coresInNUMA(topology, 0, 3, 4)
			require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(
				&steadyFakeNUMAMigrationIntent{constraintDigest: "old", target: oldTarget}))
			beforeTarget := cloneSteadyFakeNUMAMigrationTargetForTest(
				policy.steadyFakeNUMAMigration.current())
			beforeBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
			require.NoError(t, err)
			revision := policy.state.GetRevision()

			err = policy.commitPendingAdvisorState(&pendingAdvisorState{
				preCommitRevision: tc.expectedRevision(revision),
				entries:           tc.entries(topology),
				disableDedicated:  true,
				migrationCheckpointTransition: steadyFakeNUMAMigrationCheckpointTransition{
					kind: steadyFakeNUMAMigrationCheckpointReplace,
					target: &steadyFakeNUMAMigrationIntent{
						constraintDigest: "new",
						target:           newTarget,
					},
				},
			})

			require.ErrorContains(t, err, tc.wantError)
			require.Equal(t, revision, policy.state.GetRevision())
			require.Equal(t, beforeTarget, policy.steadyFakeNUMAMigration.current())
			afterBytes, readErr := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
			require.NoError(t, readErr)
			require.Equal(t, beforeBytes, afterBytes)
		})
	}
}

func TestCommitPendingAdvisorStateDoesNotApplyCheckpointTransitionAfterSuccessfulCAS(t *testing.T) {
	t.Parallel()

	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	topology := policy.machine.machineInfo.CPUTopology
	oldTarget := coresInNUMA(topology, 0, 2, 3)
	newTarget := coresInNUMA(topology, 0, 3, 4)
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: "old", target: oldTarget}))
	revision := policy.state.GetRevision()

	err := policy.commitPendingAdvisorState(&pendingAdvisorState{
		preCommitRevision: revision,
		entries: precommitPartitionEntries(
			coresInNUMA(topology, 0, 0, 1),
			coresInNUMA(topology, 0, 1, 2)),
		disableDedicated: true,
		migrationCheckpointTransition: steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointReplace,
			target: &steadyFakeNUMAMigrationIntent{
				constraintDigest: "new",
				target:           newTarget,
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, revision+1, policy.state.GetRevision())
	require.Equal(t, "old", policy.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, oldTarget, policy.steadyFakeNUMAMigration.current().target)
	require.FileExists(t, steadyFakeNUMACheckpointFile(policy))
}

func TestSteadyFakeNUMAMigrationTargetIgnoresPreferenceOnlyChanges(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	committed := coresInNUMA(topology, 0, 0, 6)
	target := coresInNUMA(topology, 0, 6, 12)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	demands := stagedMigrationDemands(all, committed, target.Size())
	digest, err := steadyFakeNUMAConstraintDigest(demands, []string{"fake"}, nil, topology)
	require.NoError(t, err)
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: digest, target: target}))
	beforeBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)

	demands[0].preferred = coresInNUMA(topology, 0, 12, 18)
	snapshot := steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{
			{
				blockID: "fake", class: advisorBlockClassMandatoryReclaim,
				numaID: 0, cpus: committed, eligible: all,
			},
			{
				blockID: "share", class: advisorBlockClassShared,
				numaID: 0, cpus: all.Difference(committed), eligible: all,
			},
		},
		rawReclaimAggregate: committed,
		reclaim:             committed,
	}

	_, err = policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		snapshot,
		map[string]machine.CPUSet{
			"fake":  target,
			"share": all.Difference(target),
		},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, target, policy.steadyFakeNUMAMigration.current().target)
	afterBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes,
		"preference-only changes must not replace the durable target")
}

func TestSolveAdvisorDescriptorPhaseValidationFailureHasNoSideEffects(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	target := &steadyFakeNUMAMigrationIntent{
		constraintDigest: "existing-target",
		target:           machine.NewCPUSet(68, 196),
	}
	require.NoError(t, policy.steadyFakeNUMAMigration.storeIntent(target))
	beforeBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)
	beforeTarget := cloneSteadyFakeNUMAMigrationTargetForTest(
		policy.steadyFakeNUMAMigration.current())
	result := map[string]machine.CPUSet{
		"existing": machine.NewCPUSet(0, 128),
	}
	beforeResult := cloneBlockCPUSetForTest(result)
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
	descriptors[0].Committed.Add(999)
	descriptors[1].Committed.Add(999)

	// This is a planner-only call boundary. Apply, cgroup, and Bulkhead writers
	// are invoked only after a successful planner result and are not reachable
	// from solveAdvisorDescriptorPhase itself.
	_, err = policy.solveAdvisorDescriptorPhase(
		descriptors, all, result, true, false)

	require.ErrorContains(t, err, "outside machine topology")
	require.Equal(t, beforeResult, result)
	require.Equal(t, beforeTarget, policy.steadyFakeNUMAMigration.current())
	afterBytes, readErr := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, readErr)
	require.Equal(t, beforeBytes, afterBytes)
}

func cloneBlockCPUSetForTest(
	source map[string]machine.CPUSet,
) map[string]machine.CPUSet {
	cloned := make(map[string]machine.CPUSet, len(source))
	for blockID, cpus := range source {
		cloned[blockID] = cpus.Clone()
	}
	return cloned
}

func cloneSteadyFakeNUMAMigrationTargetForTest(
	source *steadyFakeNUMAMigrationIntent,
) *steadyFakeNUMAMigrationIntent {
	if source == nil {
		return nil
	}
	return &steadyFakeNUMAMigrationIntent{
		constraintDigest: source.constraintDigest,
		target:           source.target.Clone(),
	}
}

// TestSteadyFakeNUMAMigrationCheckpointBackwardCompat pins the on-disk contract
// after migrating the hand-rolled writer to the shared FileCheckpointStore: the
// new writer must emit the legacy JSON schema, the new reader must decode a
// hand-written legacy file, and corruption/unknown-field guards must survive.
func TestSteadyFakeNUMAMigrationCheckpointBackwardCompat(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	dir := t.TempDir()

	const digest = "legacy-constraints"
	cpus := []int{0, 1, 4, 5}
	legacyRecord := steadyFakeNUMAMigrationCheckpoint{
		Version:          steadyFakeNUMAMigrationCheckpointVersion,
		ConstraintDigest: digest,
		TargetCPUs:       cpus,
	}
	legacyRecord.Checksum = steadyFakeNUMAMigrationCheckpointChecksum(
		legacyRecord.Version, legacyRecord.ConstraintDigest, legacyRecord.TargetCPUs)
	legacyBytes, err := json.Marshal(legacyRecord)
	require.NoError(t, err)

	// New writer emits the legacy JSON schema.
	writer, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.NoError(t, writer.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: digest, target: machine.NewCPUSet(cpus...)}))
	written, err := os.ReadFile(filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName))
	require.NoError(t, err)
	require.JSONEq(t, string(legacyBytes), string(written),
		"new CheckpointStore codec must write the legacy JSON schema")

	// New reader decodes a hand-written legacy file.
	restored, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.Equal(t, digest, restored.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, machine.NewCPUSet(cpus...), restored.steadyFakeNUMAMigration.current().target)

	// Corrupted checksum fails closed.
	badRecord := legacyRecord
	badRecord.Checksum = "0000000000000000000000000000000000000000000000000000000000000000"
	badBytes, err := json.Marshal(badRecord)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName), badBytes, 0o600))
	_, err = getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.Error(t, err)

	// Unknown fields are rejected (DisallowUnknownFields preserved).
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, steadyFakeNUMAMigrationCheckpointName),
		[]byte(`{"version":1,"constraint_digest":"x","target_cpus":[0,1,4,5],"checksum":"x","future_field":true}`),
		0o600))
	_, err = getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.Error(t, err)
}

// steadyFakeNUMACheckpointFile joins the advisor post-commit checkpoint dir and
// the steady fake-NUMA migration checkpoint name. Tests use this local helper
// instead of a production accessor so the on-disk path stays a pure test concern.
func steadyFakeNUMACheckpointFile(p *DynamicPolicy) string {
	return filepath.Join(p.advisorPostCommitCheckpointDir, steadyFakeNUMAMigrationCheckpointName)
}

// projectSteadyFakeNUMAStageWithCheckpointForTest runs the side-effect-free
// planner and then explicitly applies the resulting checkpoint transition,
// mirroring what the production advisor path does only after a successful state
// transaction. Production no longer bundles plan+apply; tests use this local
// helper so behavior stays characterized at the pure-planner + explicit-apply
// boundary.
func (p *DynamicPolicy) projectSteadyFakeNUMAStageWithCheckpointForTest(
	demands []partitionDemand,
	fakeKeys []string,
	committed steadyFakeNUMACommittedSnapshot,
	freshDesired map[string]machine.CPUSet,
	floors []partitionCoreFloorConstraint,
) (map[string]machine.CPUSet, error) {
	assignments, transition, err := p.planSteadyFakeNUMAStageWithCheckpoint(
		demands, fakeKeys, committed, freshDesired, floors)
	if err != nil {
		return nil, err
	}
	if err := p.steadyFakeNUMAMigration.applyTransition(transition); err != nil {
		return nil, err
	}
	return assignments, nil
}

// TestSteadyFakeNUMAMigrationTransitionCloneIsDeepIndependent pins the
// transition boundary copy discipline: a cloned checkpoint transition must own
// an independent CPUSet. CPUSet.elems is a shared map, so without a deep clone a
// later mutation of the source intent would leak into a queued WAL / stuck
// post-commit transition.
func TestSteadyFakeNUMAMigrationTransitionCloneIsDeepIndependent(t *testing.T) {
	t.Parallel()

	original := &steadyFakeNUMAMigrationIntent{
		constraintDigest: "digest-a",
		target:           machine.NewCPUSet(0, 1, 4, 5),
	}
	transition := steadyFakeNUMAMigrationCheckpointTransition{
		kind:   steadyFakeNUMAMigrationCheckpointReplace,
		target: original,
	}

	cloned := cloneSteadyFakeNUMAMigrationCheckpointTransition(transition)

	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace, cloned.kind)
	require.NotNil(t, cloned.target)
	require.Equal(t, original.constraintDigest, cloned.target.constraintDigest)
	require.Equal(t, original.target, cloned.target.target)

	original.target.Add(999)
	require.True(t, original.target.Contains(999))
	require.False(t, cloned.target.target.Contains(999),
		"cloned transition CPUSet must be deep independent of the source intent")
}

// TestSteadyFakeNUMAMigrationIntentResumesAcrossFourNUMANodes characterizes the
// durable frozen intent on a multi-NUMA machine: after a restart, an unchanged
// constraint digest must resume toward the frozen target even when a fresh desire
// points the fake pool at an entirely different NUMA. freshDesired must not
// override the frozen durable target.
func TestSteadyFakeNUMAMigrationIntentResumesAcrossFourNUMANodes(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 4)
	require.NoError(t, err)
	require.Equal(t, 4, topology.NumNUMANodes)
	all := topology.CPUDetails.CPUs()

	// Five CPUs per fake pool make a disjoint NUMA move cost ten CPU changes,
	// which remains above the production per-stage limit of eight while keeping
	// the characterization test's search space small under race+coverage builds.
	committed := coresInNUMA(topology, 0, 0, 5)
	frozen := coresInNUMA(topology, 1, 0, 5)
	rogue := coresInNUMA(topology, 3, 0, 5)
	require.True(t, committed.Intersection(frozen).IsEmpty())
	require.True(t, frozen.Intersection(rogue).IsEmpty())

	dir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)

	// First stage: committed(NUMA0) -> frozen(NUMA1) is a large jump, so a frozen
	// durable intent is recorded toward NUMA1.
	demands := stagedMigrationDemands(all, committed, frozen.Size())
	first, err := policy.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(committed, all),
		map[string]machine.CPUSet{"fake": frozen, "share": all.Difference(frozen)},
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, policy.steadyFakeNUMAMigration.current())
	require.Equal(t, frozen, policy.steadyFakeNUMAMigration.current().target,
		"first large stage must freeze the durable target at NUMA1")

	// Restart. A fresh desire would rather move the fake pool to NUMA3, but the
	// constraint digest is unchanged: the frozen NUMA1 intent must win.
	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	committedNow := first["fake"]
	demands = stagedMigrationDemands(all, committedNow, frozen.Size())
	second, err := restarted.projectSteadyFakeNUMAStageWithCheckpointForTest(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(committedNow, all),
		map[string]machine.CPUSet{"fake": rogue, "share": all.Difference(rogue)},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, frozen, restarted.steadyFakeNUMAMigration.current().target,
		"unchanged constraints must resume the frozen NUMA1 intent, not the fresh NUMA3 desire")
	require.Less(t,
		steadyFakeNUMAMigrationChurn(second["fake"], frozen),
		steadyFakeNUMAMigrationChurn(committedNow, frozen),
		"resumed stage must step toward the frozen target")
}

// TestSteadyFakeNUMAMigrationTransitionReplayIsStable characterizes the physical
// apply / stuck-replay boundary: replaying an already-applied replace transition
// must keep the durable intent and on-disk checkpoint stable, and a following
// remove must clear both.
func TestSteadyFakeNUMAMigrationTransitionReplayIsStable(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(24, 1, 1)
	require.NoError(t, err)
	target := coresInNUMA(topology, 0, 6, 12)
	dir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)

	replace := steadyFakeNUMAMigrationCheckpointTransition{
		kind: steadyFakeNUMAMigrationCheckpointReplace,
		target: &steadyFakeNUMAMigrationIntent{
			constraintDigest: "replay",
			target:           target,
		},
	}
	require.NoError(t, policy.steadyFakeNUMAMigration.applyTransition(replace))
	firstBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)

	// Stuck replay: the same transition is applied again after recovery.
	require.NoError(t, policy.steadyFakeNUMAMigration.applyTransition(replace))
	afterBytes, err := os.ReadFile(steadyFakeNUMACheckpointFile(policy))
	require.NoError(t, err)
	require.Equal(t, firstBytes, afterBytes,
		"replaying an already-applied replace must not flip the durable checkpoint")
	require.NotNil(t, policy.steadyFakeNUMAMigration.current())
	require.Equal(t, target, policy.steadyFakeNUMAMigration.current().target)

	require.NoError(t, policy.steadyFakeNUMAMigration.applyTransition(
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointRemove,
		}))
	require.Nil(t, policy.steadyFakeNUMAMigration.current())
	require.NoFileExists(t, steadyFakeNUMACheckpointFile(policy))
}
