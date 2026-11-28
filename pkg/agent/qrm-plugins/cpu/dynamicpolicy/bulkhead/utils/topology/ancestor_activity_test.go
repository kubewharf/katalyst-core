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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func ancestorActivityFixture(t *testing.T) (*TopoDAG, *fakeHierarchyDriver) {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("1-3"), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "reclaim/idle", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("1-3")}},
		{Rel: "reclaim/active", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0)},
	})
	require.NoError(t, err)
	d := newFakeHierarchyDriver()
	d.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "1-2", "0")
	d.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "0,3", "0")
	d.add("reclaim/idle", CgroupIdentity{Device: 1, Inode: 3}, "3", "0")
	d.add("reclaim/active", CgroupIdentity{Device: 1, Inode: 4}, "0", "0")
	d.markInactive("reclaim/idle")
	return dag, d
}

// The same identity and masks must produce distinguishable evidence when only
// direct ancestor occupancy changes. Neither observation grants a CPU release.
func TestDormantSnapshotObservesAncestorDirectOccupancy(t *testing.T) {
	t.Parallel()

	dag, driver := ancestorActivityFixture(t)
	capture := newDormantCompleteSnapshotSource(driver, dag, NewBudgetTracker(ConvergenceBudget{}))
	active, err := capture(context.Background())
	require.NoError(t, err)
	require.False(t, active.Entries["reclaim"].Activity.TasksEmpty)
	driver.markInactive("reclaim")
	empty, err := capture(context.Background())
	require.NoError(t, err)
	require.True(t, empty.Entries["reclaim"].Activity.TasksEmpty, "direct ancestor tasks must be observed")
	require.True(t, empty.Entries["reclaim"].Activity.CgroupProcsEmpty)
	require.False(t, empty.Entries["reclaim"].Activity.Childless, "a root with children is not a dormant leaf")
	require.NotEqual(t, active.ID, empty.ID, "activity must be included in evidence identity")
	for _, snapshot := range []*CompleteSnapshot{active, empty} {
		materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))
		gate, err := NewDomainGate("ancestor-observation", materialized.Snapshot,
			desiredDomainUnions(dag, materialized.SemanticByRel), machine.MustParse("0-3"), nil)
		require.NoError(t, err)
		require.False(t, gate.AllowedEntering(DomainPrimary).Contains(3), "observed emptiness must not imply physical release")
		require.Equal(t, machine.MustParse("0,3"), snapshot.DomainUnion[DomainReclaim])
	}
	require.Empty(t, driver.writes)
}

type ancestorActivityFailureDriver struct {
	*fakeHierarchyDriver
	err   error
	reads []string
}

func (d *ancestorActivityFailureDriver) ReadEntryWithActivity(ctx context.Context, rel string, expected CgroupIdentity) (EntryState, error) {
	d.reads = append(d.reads, rel)
	if rel == "reclaim" {
		return EntryState{
			Rel: rel, Identity: expected, CPUs: machine.MustParse("0-3"),
			Activity: CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true},
		}, d.err
	}
	return d.fakeHierarchyDriver.ReadEntryWithActivity(ctx, rel, expected)
}

// Failed activity reads can carry plausible dirty evidence, which must never
// escape as a valid or partial snapshot.
func TestDormantSnapshotRejectsFailedAncestorActivity(t *testing.T) {
	t.Parallel()

	dag, fake := ancestorActivityFixture(t)
	sentinel := errors.New("ancestor membership unavailable")
	driver := &ancestorActivityFailureDriver{fakeHierarchyDriver: fake, err: sentinel}
	snapshot, err := newDormantCompleteSnapshotSource(driver, dag, NewBudgetTracker(ConvergenceBudget{}))(context.Background())
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, snapshot)
	require.Contains(t, driver.reads, "reclaim")
	require.Empty(t, fake.writes)
}

// A path replaced while reading the ancestor must not survive as a partial
// snapshot, even when the replacement carries plausible empty activity.
func TestDormantSnapshotRejectsAncestorIdentityChange(t *testing.T) {
	t.Parallel()

	dag, driver := ancestorActivityFixture(t)
	driver.markInactive("reclaim")
	replaced := false
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationRead && rel == "reclaim" && !replaced {
			replaced = true
			driver.bumpIdentity(rel)
		}
		return nil
	}
	snapshot, err := newDormantCompleteSnapshotSource(driver, dag, NewBudgetTracker(ConvergenceBudget{}))(context.Background())
	require.True(t, replaced)
	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
	require.Nil(t, snapshot)
	require.Empty(t, driver.writes)
}

// Additional reads remain scoped to v1 dormant-aware snapshots. A failed
// ancestor activity reader must be irrelevant outside that scope.
func TestAncestorActivityCollectionKeepsExistingScope(t *testing.T) {
	t.Parallel()

	for _, v2 := range []bool{false, true} {
		dag, fake := ancestorActivityFixture(t)
		if v2 {
			fake.capabilities = cgroupV2Policy.capabilities(true)
		}
		driver := &ancestorActivityFailureDriver{fakeHierarchyDriver: fake, err: errors.New("must not be read")}
		var snapshot *CompleteSnapshot
		var err error
		if v2 {
			snapshot, err = newDormantCompleteSnapshotSource(driver, dag, NewBudgetTracker(ConvergenceBudget{}))(context.Background())
		} else {
			snapshot, err = BuildCompleteSnapshot(context.Background(), driver, dag,
				SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary", "reclaim", "reclaim/idle", "reclaim/active"}}, NewBudgetTracker(ConvergenceBudget{}))
		}
		require.NoError(t, err)
		require.NotNil(t, snapshot)
		require.Empty(t, driver.reads)
		require.Empty(t, fake.writes)
	}
}
