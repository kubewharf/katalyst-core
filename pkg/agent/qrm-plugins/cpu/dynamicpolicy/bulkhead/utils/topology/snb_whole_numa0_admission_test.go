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

// e2eWholeNUMA0CPUDetails mirrors the production E2E memory layout:
//
//	NUMA0={0-15,64-79}, NUMA1={16-31,80-95}, NUMA2={32-47,96-111}, NUMA3={48-63,112-127}.
//
// CPU c and c+64 share a physical core (SMT pair); coreID = numa*16 + cpu%16.
// This is the exact 128-CPU / 4-NUMA production shape.
func e2eWholeNUMA0CPUDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	numaByBlock := []int{0, 1, 2, 3, 0, 1, 2, 3}
	for cpu := 0; cpu < 128; cpu++ {
		block := cpu / 16
		numa := numaByBlock[block]
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: numa*16 + cpu%16}
	}
	return details
}

// e2eWholeNUMA0Reclaim is the steady-state reclaim set: 2 whole cores per NUMA,
// confined entirely inside one NUMA.
//
//	NUMA0 bucket {4,68}, NUMA1 {20,84}, NUMA2 {36,100}, NUMA3 {52,116}.
func e2eWholeNUMA0Reclaim() machine.CPUSet {
	return machine.MustParse("4,20,36,52,68,84,100,116")
}

// e2eWholeNUMA0Specs builds the controlled-DAG skeleton of the production E2E
// admission: one DomainPrimary root (kubepods), one reclaim root, and four reclaim
// NUMA buckets (one per NUMA node). The seed pod leaf is a dynamic descendant of
// kubepods, never a controlled DAG node, exactly as in production.
func e2eWholeNUMA0Specs(details machine.CPUDetails) []NodeSpec {
	reclaim := e2eWholeNUMA0Reclaim()
	all := machine.MustParse("0-127")
	nonReclaim := all.Difference(reclaim)
	return []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: nonReclaim.Clone(), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaim.Clone(), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{
			Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4,68"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: details.CPUsInNUMANodes(0), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "0"},
		},
		{
			Rel: "reclaimed/reclaimed-1", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("20,84"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: details.CPUsInNUMANodes(1), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "1"},
		},
		{
			Rel: "reclaimed/reclaimed-2", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("36,100"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: details.CPUsInNUMANodes(2), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "2"},
		},
		{
			Rel: "reclaimed/reclaimed-3", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("52,116"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: details.CPUsInNUMANodes(3), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "3"},
		},
	}
}

// TestSNBWholeNUMA0BorrowedReclaimDrainConverges is the red/green proof for the
// production E2E stall on a 128-CPU / 4-NUMA node.
//
// Scenario (reconstructed from the E2E evidence):
//
//   - A pending SNB pod (kubepods/pod840b) currently owns the whole NUMA0 slice
//     0-15,64-79 (32 CPUs), which overlaps the NUMA0 reclaim bucket {4,68}.
//   - The production projection (ProjectPendingProtectionsToDesiredNonReclaim /
//     DeriveShadowPartition) classifies the whole-NUMA0 protection as SHARED 30 =
//     0-3,5-15,64-67,69-79: it is clipped to the non-reclaim pool, the shadow holds
//     no current CPU, and the {4,68} bucket is retained.
//   - The pod leaf's bulkhead expected cpuset is the shared 30; its observed cgroup
//     still holds 32. The leaf therefore borrows reclaim core {4,68} that its
//     primary parent kubepods (120, excluding {4,68}) does not contain.
//
// Before the fix, the RAW drain projection emits exactly one op — shrink the leaf
// 32 -> 30, releasing {4,68} — but SplitPlanForAdmission deferred it as
// OperationDeferredCleanup. walkTransferEdges only credits a shrink as an outgoing
// transfer when a destination domain is net *entering* those CPUs; here the reclaim
// bucket already enforces {4,68}, so no transfer edge exists and removedPending is
// empty (protectedPending is the shared 30, which excludes {4,68}). The deferred
// shrink leaves the leaf observed=32 not contained by its parent 120, ParentSafety
// reports parent_not_containing_child, and the fixed-point loop stalls as a bare
// ErrNoProgress with zero writes.
//
// The fix classifies a primary WriteShrink that releases CPUs already in the
// reclaim union as a mandatory OperationAdmissionSourceDrain. The leaf drains
// {4,68} into the already-enforced bucket, the overlap / parent-containment
// violation clears, and admission converges. ParentSafe, the v1 empty-target
// guard, no-overlap, and retry/budget are all untouched.
func TestSNBWholeNUMA0BorrowedReclaimDrainConverges(t *testing.T) {
	t.Parallel()

	details := e2eWholeNUMA0CPUDetails()
	reclaim := e2eWholeNUMA0Reclaim()
	sharedNUMA0 := machine.MustParse("0-3,5-15,64-67,69-79")
	wholeNUMA0 := machine.MustParse("0-15,64-79")

	fixture := newAdmissionTraceFixture(t)
	fixture.cpuDetails = details
	fixture.round.allowEmptyTarget = false // cgroup v1

	fixture.addPrimary("kubepods", machine.MustParse("0-127").Difference(reclaim).String(), "0")
	fixture.addReclaim("reclaimed", reclaim.String(), "0")
	fixture.addReclaim("reclaimed/reclaimed-0", "4,68", "0")
	fixture.addReclaim("reclaimed/reclaimed-1", "20,84", "0")
	fixture.addReclaim("reclaimed/reclaimed-2", "36,100", "0")
	fixture.addReclaim("reclaimed/reclaimed-3", "52,116", "0")

	// Seed pod leaf: a REAL cgroup entry (business whole NUMA0=32) but NOT a
	// controlled DAG node — the seed leaf is a pure dynamic descendant.
	fixture.driver.add("kubepods/pod840b", CgroupIdentity{Device: 1, Inode: fixture.allocInode()}, wholeNUMA0.String(), "0")

	// Freeze bucket required floors at the canonical per-NUMA reclaim budget.
	fixture.requireCPUSet("reclaimed/reclaimed-0", "4,68")
	fixture.requireCPUSet("reclaimed/reclaimed-1", "20,84")
	fixture.requireCPUSet("reclaimed/reclaimed-2", "36,100")
	fixture.requireCPUSet("reclaimed/reclaimed-3", "52,116")

	pendingRequiredByRel := map[string]machine.CPUSet{"kubepods": sharedNUMA0.Clone()}

	dag, err := BuildDAG(fixture.specs)
	require.NoError(t, err)
	eff, err := computeEffectiveTargets(dag, false, details, pendingRequiredByRel, fixture.requiredByRel)
	require.NoError(t, err)
	for i := range fixture.specs {
		fixture.specs[i].CPUs = eff[fixture.specs[i].Rel].Clone()
	}
	fixture.targetByRel = eff

	// Guard: production projection keeps the NUMA0 bucket; it is not emptied.
	require.Equal(t, machine.MustParse("4,68"), eff["reclaimed/reclaimed-0"],
		"NUMA0 bucket must retain {4,68} (shared projection, not exact)")
	require.Equal(t, reclaim, eff["reclaimed"], "reclaim root must stay at 8 CPUs")

	fixture.round.protectedPending = sharedNUMA0.Clone()
	fixture.round.pendingRequiredByRel = pendingRequiredByRel
	// Pod leaf bulkhead expected = shared-projected NUMA0 (30).
	fixture.dynamicByRel = map[string]machine.CPUSet{"kubepods/pod840b": sharedNUMA0.Clone()}
	fixture.round.dynamicByRel = fixture.dynamicByRel

	base := fixture.snapshot()

	// The converged production chain: drain + expand through the fixed point.
	fixture.round.deferredCleanupRels = map[string]struct{}{}
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err, "admission must converge; on 018 this returns bare ErrNoProgress")

	ops := flattenTraceOperations(trace)
	require.Len(t, ops, 1, "exactly one required op: drain the borrowed reclaim core out of the leaf")
	require.Equal(t, "kubepods/pod840b", ops[0].Rel)
	require.Equal(t, WriteShrink, ops[0].Direction, "the leaf must shrink, not grow")
	require.True(t, wholeNUMA0.IsSubsetOf(ops[0].ExpectedCurrent.CPUs),
		"the leaf observed must still hold whole NUMA0 before the drain")
	require.Equal(t, sharedNUMA0, ops[0].Target.CPUs, "the drain target must be the shared-projected 30")

	ps := trace.FinalEvaluation.ParentSafety
	require.True(t, ps.Safe, "ParentSafety must converge; ParentSafety=%+v", ps)
	require.Empty(t, ps.UnsafeRequiredRels, "no remaining parent-containment violation; %+v", ps.UnsafeRequiredRels)
	require.Empty(t, ps.DeferredLeafMismatches, "no deferred leaf mismatch; %+v", ps.DeferredLeafMismatches)
	require.True(t, ps.PrimaryReclaimOverlap.IsEmpty(), "no primary/reclaim overlap; %s", ps.PrimaryReclaimOverlap)
	require.True(t, trace.FinalEvaluation.Report.FullyConverged, "report must be fully converged; %+v", trace.FinalEvaluation.Report)
}

// TestSNBWholeNUMA0MultiNUMAContainmentAndBounds guards that the borrowed-reclaim
// drain fix does not break the structural invariants across all four NUMA nodes:
// each bucket stays inside its NUMA upper bound, contained by its reclaim parent,
// sibling buckets stay disjoint, and no primary/reclaim overlap appears.
func TestSNBWholeNUMA0MultiNUMAContainmentAndBounds(t *testing.T) {
	t.Parallel()

	details := e2eWholeNUMA0CPUDetails()
	specs := e2eWholeNUMA0Specs(details)

	// Reconcile each bucket floor with its effective target; floors must never
	// demand a CPU the bucket no longer retains.
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(specs)

	sharedNUMA0 := machine.MustParse("0-3,5-15,64-67,69-79")
	pendingRequiredByRel := map[string]machine.CPUSet{"kubepods": sharedNUMA0.Clone()}

	eff, err := computeEffectiveTargets(dag, false, details, pendingRequiredByRel, requiredByRel)
	require.NoError(t, err)
	reconcileReclaimNUMABucketRequiredFloors(dag, eff, requiredByRel)

	buckets := []string{"reclaimed/reclaimed-0", "reclaimed/reclaimed-1", "reclaimed/reclaimed-2", "reclaimed/reclaimed-3"}
	expected := []string{"4,68", "20,84", "36,100", "52,116"}
	numas := []int{0, 1, 2, 3}
	for i, rel := range buckets {
		got := eff[rel]
		require.Equal(t, machine.MustParse(expected[i]), got, "bucket %s effective must be unchanged", rel)
		// NUMA upper-bound guard.
		require.True(t, got.IsSubsetOf(details.CPUsInNUMANodes(numas[i])),
			"bucket %s must stay inside NUMA%d upper bound; got=%s", rel, numas[i], got)
		// Parent-containment guard.
		require.True(t, got.IsSubsetOf(eff["reclaimed"]),
			"bucket %s must be contained by its reclaim parent; got=%s", rel, got)
		// Frozen floor must be a subset of the effective target.
		require.True(t, requiredByRel[rel].IsSubsetOf(got),
			"bucket %s required floor must be subset of effective target; floor=%s eff=%s",
			rel, requiredByRel[rel], got)
	}

	// Sibling-disjoint / hard-overlap guard: all four buckets are pairwise disjoint
	// and together cover exactly the reclaim root.
	union := machine.NewCPUSet()
	for _, rel := range buckets {
		union = union.Union(eff[rel])
	}
	require.Equal(t, eff["reclaimed"], union, "bucket union must equal the reclaim root")
	for i := range buckets {
		for j := i + 1; j < len(buckets); j++ {
			require.True(t, eff[buckets[i]].Intersection(eff[buckets[j]]).IsEmpty(),
				"buckets %s and %s must be disjoint", buckets[i], buckets[j])
		}
	}

	// No primary/reclaim overlap guard.
	require.True(t, eff["kubepods"].Intersection(eff["reclaimed"]).IsEmpty(),
		"primary and reclaim must not overlap; overlap=%s", eff["kubepods"].Intersection(eff["reclaimed"]))
}
