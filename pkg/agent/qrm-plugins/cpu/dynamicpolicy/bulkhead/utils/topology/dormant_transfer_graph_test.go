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

// TestDormantOverlayNoFabricatedReclaimToPrimaryEdge drives the real production
// chain (live driver scan -> computeEffectiveTargets -> materializeTargets) and
// then BuildPhasePlan for the high-churn immediate-recreate shape that the
// overlap_churn E2E hits after the physical/semantic split:
//
//   - primary observed held back to 0-1 (churned), desired to grow back to 0-3,
//   - reclaim physical containment envelope still spans 0-7 (dormant v1 buckets
//     numa-0/numa-1), while semantic reclaim ownership is only the active 4-7.
//
// countTransferEdges/buildTransferGraph must read semantic ownership, not the
// physical DomainUnion. Reading physical fabricates a reclaim->primary edge for
// cores 2-3 that semantic ownership never actually leaves (the dormant envelope
// is reclaim containment evidence, not a reclaim-held CPU set). The drain batch
// (already semantic) then cannot execute that promised edge, so the projected
// fixed-point loop waits on a transfer that never drains and burns its rounds:
//
//	fixed-point compilation made no progress
//	convergence round budget exceeded: limit=32 used=32
func TestDormantOverlayNoFabricatedReclaimToPrimaryEdge(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1", Metadata: map[string]string{"numa": "1"}},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2", Metadata: map[string]string{"numa": "2"}},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3", Metadata: map[string]string{"numa": "3"}},
	})
	require.NoError(t, err)

	driver := newFakeHierarchyDriver()
	// Primary observed held back to 0-1 (churned), must grow back to 0-3.
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0-3")
	driver.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "4-7", "0-3")
	driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 3}, "0-7", "0")
	driver.add("reclaim/numa-1", CgroupIdentity{Device: 1, Inode: 4}, "0-7", "1")
	driver.add("reclaim/numa-2", CgroupIdentity{Device: 1, Inode: 5}, "4-5", "2")
	driver.add("reclaim/numa-3", CgroupIdentity{Device: 1, Inode: 6}, "6-7", "3")
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

	budget := NewBudgetTracker(ConvergenceBudget{})
	raw, err := newDormantCompleteSnapshotSource(driver, dag, budget)(context.Background())
	require.NoError(t, err)

	materialized := materializeTargets(dag, raw, false, semantic)
	require.NotEmpty(t, materialized.DormantRels, "numa-0/numa-1 must classify dormant")

	// Closed physical evidence: DomainUnion[reclaim] keeps the 0-7 envelope.
	require.Equal(t, "0-7", materialized.Snapshot.DomainUnion[DomainReclaim].String(),
		"physical evidence union must retain the dormant v1 containment envelope")
	// Semantic ownership: reclaim only owns the active 4-7.
	require.Equal(t, "4-7", materialized.Snapshot.OwnershipByRel["reclaim"].String(),
		"semantic reclaim ownership must be the active set, not the envelope")

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: materialized.Snapshot,
		DesiredByRel: materialized.PhysicalByRel, SemanticByRel: materialized.SemanticByRel,
		DormantRels: materialized.DormantRels, AllowedCPUs: machine.MustParse("0-7"),
		CPUDetails: cpuDetails,
		Budget:     NewBudgetTracker(ConvergenceBudget{}),
	})
	require.NoError(t, err)

	// The physical envelope must not fabricate a reclaim->primary edge. Semantic
	// reclaim ownership (4-7) does not leave cores 2-3, so there is nothing for
	// reclaim to transfer out to the growing primary.
	for destination, cpus := range plan.TransferGraph[DomainReclaim] {
		require.NotEqual(t, DomainPrimary, destination,
			"plan.TransferGraph[reclaim][primary] must be empty; got fabricated edge %s", cpus.String())
	}
	// No reclaim leaving at all, since semantic reclaim observed==desired.
	require.True(t, plan.DrainBatch[DomainReclaim].IsEmpty(),
		"semantic reclaim has nothing to drain; got %s", plan.DrainBatch[DomainReclaim].String())
}
