/*
Copyright 2026 The Kubernetes Authors.

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

// TestDormantOverlayReclaimUnionMatchesPhysicalEntries drives the real
// production chain (live driver scan -> materializeTargets -> frozen-trace
// evidence validator) for a cgroup v1 shape that the overlap_churn E2E hits:
//
//   - a primary domain,
//   - a reclaim parent whose aggregate collapsed,
//   - per-NUMA reclaim buckets whose physical v1 containment envelopes were
//     drained/churned so they still span cores that are no longer reclaim
//     ownership (here numa-0/numa-1 keep the 0-7 envelope while semantically
//     owning nothing),
//   - a top-level ReclaimSibling (the controlled system cgroup) that the
//     normalized reclaim-union rescue widens to the active reclaim set.
//
// The dormant ownership overlay must keep the closed evidence snapshot
// self-consistent: DomainUnion[reclaim] must equal the union of the physical
// reclaim entries exactly as validateCompleteSnapshotEvidence recomputes it,
// while semantic ownership of the dormant ancestors stays in OwnershipByRel.
// Before the fix the overlay rebuilt DomainUnion from the semantic (rescued)
// reclaim set, so the physical envelope cores leaked out of DomainUnion and
// the frozen trace failed to compile on every ParentSafe admission with:
//
//	frozen phase trace initial snapshot evidence is inconsistent: domain union
//	"reclaim" does not match entries
func TestDormantOverlayReclaimUnionMatchesPhysicalEntries(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1", Metadata: map[string]string{"numa": "1"}},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2", Metadata: map[string]string{"numa": "2"}},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3", Metadata: map[string]string{"numa": "3"}},
		// Reclaim sibling (controlled system cgroup): aggregate collapsed, rescued
		// by the normalized reclaim-union widening.
		{Rel: "system", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0-3", TrustAnchor: true, ControlledRoot: true},
	})
	require.NoError(t, err)

	driver := newFakeHierarchyDriver()
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0-3")
	driver.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "4-7", "0-3")
	driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 3}, "0-7", "0")
	driver.add("reclaim/numa-1", CgroupIdentity{Device: 1, Inode: 4}, "0-7", "1")
	driver.add("reclaim/numa-2", CgroupIdentity{Device: 1, Inode: 5}, "4-5", "2")
	driver.add("reclaim/numa-3", CgroupIdentity{Device: 1, Inode: 6}, "6-7", "3")
	driver.add("system", CgroupIdentity{Device: 1, Inode: 7}, "4-7", "0-3")
	driver.markInactive("reclaim/numa-0")
	driver.markInactive("reclaim/numa-1")

	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
		4: {NUMANodeID: 2}, 5: {NUMANodeID: 2},
		6: {NUMANodeID: 3}, 7: {NUMANodeID: 3},
	}
	semantic, err := computeEffectiveTargets(dag, false, cpuDetails, nil, nil, nil)
	require.NoError(t, err)

	// The normalized reclaim union must have rescued the collapsed sibling.
	require.True(t, machine.MustParse("4-7").IsSubsetOf(semantic["system"]),
		"reclaim sibling %q must follow the normalized reclaim union", semantic["system"])

	budget := NewBudgetTracker(ConvergenceBudget{})
	raw, err := newDormantCompleteSnapshotSource(driver, dag, budget)(context.Background())
	require.NoError(t, err)

	materialized := materializeTargets(dag, raw, false, semantic)
	require.NotEmpty(t, materialized.DormantRels, "numa-0/numa-1 must classify dormant")

	// This is the exact evidence gate the frozen ParentSafe trace compiles
	// (validateCompleteSnapshotEvidence on the materialized InitialSnapshot).
	require.NoError(t, validateCompleteSnapshotEvidence(materialized.Snapshot),
		"frozen phase trace initial snapshot evidence must stay self-consistent")

	// Semantic ownership of the dormant ancestors must still be protected: the
	// drained envelope must not be promoted into AppliedView / target proof.
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		owned, ok := materialized.Snapshot.TargetProofCPUs(rel, machine.NewCPUSet())
		require.True(t, ok, "dormant rel %q must carry ownership evidence", rel)
		require.True(t, owned.IsEmpty(),
			"dormant rel %q semantic ownership = %s, want empty envelope-suppressed proof", rel, owned)
	}
	// The reclaim parent semantic ownership is the rescued active reclaim set,
	// not the physical envelope union.
	parentOwned, ok := materialized.Snapshot.TargetProofCPUs("reclaim", machine.NewCPUSet())
	require.True(t, ok)
	require.Equal(t, "4-7", parentOwned.String(),
		"reclaim parent semantic ownership must stay the rescued active set")
	// Physical envelopes are still observed (v1 containment unchanged).
	require.Equal(t, "0-7", materialized.Snapshot.Entries["reclaim/numa-0"].CPUs.String())
}
