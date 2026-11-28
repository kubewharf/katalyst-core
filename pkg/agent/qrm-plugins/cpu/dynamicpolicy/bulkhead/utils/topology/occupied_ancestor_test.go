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

package topology

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Non-dormant descendants still own their observed CPUs, even when a dormant
// bucket has an overlapping containment mask and the ancestor has no tasks.
func TestDormantOverlayPreservesOtherObservedOccupancy(t *testing.T) {
	t.Parallel()

	for _, observed := range []string{"24,72", "26,74"} {
		observed := observed
		t.Run(observed, func(t *testing.T) {
			t.Parallel()
			f, _ := dormantTransferFixture(t)
			f.driver.markInactive("reclaim")
			f.driver.add("reclaim/workload", CgroupIdentity{Device: 1, Inode: f.allocInode()}, observed, "1")
			root := f.driver.nodes["reclaim"]
			root.cpus = root.cpus.Union(machine.MustParse(observed))
			root.configuredCPUs = root.cpus.Clone()
			raw, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
			require.NoError(t, err)
			m := materializeTargets(f.round.dag, raw, false, f.round.semanticTargetByRel)
			owned, found := m.Snapshot.TargetProofCPUs("reclaim", f.round.semanticTargetByRel["reclaim"])
			require.True(t, found)
			require.True(t, machine.MustParse(observed).IsSubsetOf(owned), "observed descendant cannot be suppressed by a dormant sibling")
			require.False(t, owned.Contains(2), "unobserved desired CPU must not be preclaimed")
			require.Empty(t, f.driver.writes)
		})
	}
}

// A pre-set envelope can satisfy the planner's Full report but must not hide
// direct tasks on a physically overlapping ancestor. This is a reduced
// counterfactual fixture, not an automatic relocation or node verification.
func TestFullReportDoesNotAcceptOccupiedDormantAncestorEnvelope(t *testing.T) {
	t.Parallel()

	f, _ := dormantTransferFixture(t)
	envelope := machine.NewCPUSet(25, 73)
	f.driver.nodes["reclaim/bucket-1"].cpus = envelope.Clone()
	f.driver.nodes["reclaim/bucket-1"].configuredCPUs = envelope.Clone()
	for _, rel := range []string{"reclaim", "system"} {
		f.driver.nodes[rel].cpus = machine.NewCPUSet(0, 48).Union(envelope)
		f.driver.nodes[rel].configuredCPUs = f.driver.nodes[rel].cpus.Clone()
	}
	raw, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
	require.NoError(t, err)
	require.False(t, raw.Entries["reclaim"].Activity.TasksEmpty)
	prepared, err := f.round.preparePlanningSnapshot(context.Background(), raw)
	require.NoError(t, err)
	f.round.objective = ConvergenceObjectiveFull
	trace, err := f.round.compileFixedPointTrace(context.Background(), prepared)
	if err == nil {
		t.Logf("full=%v primary=%s reclaim=%s leaf=%s direct_ancestor_tasks_empty=%v",
			trace.FinalEvaluation.Report.FullyConverged,
			trace.FinalSnapshot.Entries["primary"].CPUs,
			trace.FinalSnapshot.Entries["reclaim"].CPUs,
			trace.FinalSnapshot.Entries["primary/pod/leaf"].CPUs,
			raw.Entries["reclaim"].Activity.TasksEmpty)
	}
	require.Error(t, err, "nonempty direct ancestor occupancy cannot be hidden behind a dormant child")
	require.Empty(t, f.driver.writes)
}

// Direct ancestor occupancy is different from an inactive child's containment.
// Observe both through the real builder; a dormant overlay cannot erase the
// ancestor's CPUs while its own membership remains nonempty.
func TestDormantOverlayPreservesOccupiedAncestor(t *testing.T) {
	t.Parallel()

	for _, directTasksEmpty := range []bool{false, true} {
		name := "direct-tasks-live"
		if directTasksEmpty {
			name = "direct-tasks-empty"
		}
		directTasksEmpty := directTasksEmpty
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, _ := dormantTransferFixture(t)
			if directTasksEmpty {
				f.driver.markInactive("reclaim")
			}
			capture := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)
			raw, err := capture(context.Background())
			require.NoError(t, err)
			m := materializeTargets(f.round.dag, raw, false, f.round.semanticTargetByRel)
			owned, found := m.Snapshot.TargetProofCPUs("reclaim", f.round.semanticTargetByRel["reclaim"])
			require.True(t, found)
			if !directTasksEmpty {
				require.True(t, raw.Entries["reclaim"].CPUs.IsSubsetOf(owned),
					"nonempty direct ancestor tasks must retain all observed ownership")
			} else {
				require.Equal(t, machine.MustParse("0,48"), owned,
					"observed-empty ancestor may suppress only proven dormant containment")
			}
			require.Empty(t, f.driver.writes)
		})
	}
}
