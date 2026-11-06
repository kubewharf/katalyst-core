/*
Copyright 2022 The Katalyst Authors.

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

// snbAdmissionCPUDetails describes CPUs 0-3 all on NUMA node 0.
func snbAdmissionCPUDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	for c := 0; c < 4; c++ {
		details[c] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: c}
	}
	return details
}

// snbAdmissionNodeSpecs is the controlled-DAG skeleton of a 128c/4NUMA SNB
// seed-leaf admission, reduced to the single NUMA0 slice that owns the pending
// CPU. The production DAG carries exactly one DomainPrimary root (kubepods) and
// one reclaim NUMA bucket (reclaimed/reclaimed-0). The seed leaf itself is a
// dynamic descendant of kubepods, never a controlled DAG node.
func snbAdmissionNodeSpecs() []NodeSpec {
	return []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0"), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("1-3"), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{
			Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("1-3"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-3"), Scope: TopologyScopeNUMANode},
			Metadata:   map[string]string{"numa": "0"},
		},
	}
}

// TestSNBRequiredFloorIsReclaimBucketOnly proves the production required-floor
// source. RequiredCPUSetByRelFromNodeSpecs freezes ONLY reclaim NUMA buckets; the
// primary kubepods root has no required floor. This is why commit 937's union of
// requiredByRel into DomainPrimary nodes is a no-op in production: requiredByRel
// never carries a primary rel, so the union is always empty.
func TestSNBRequiredFloorIsReclaimBucketOnly(t *testing.T) {
	t.Parallel()

	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(snbAdmissionNodeSpecs())
	require.Len(t, requiredByRel, 1, "production requiredByRel must freeze only reclaim NUMA buckets")
	require.Equal(t, machine.MustParse("1-3"), requiredByRel["reclaimed/reclaimed-0"])
	_, hasPrimaryFloor := requiredByRel["kubepods"]
	require.False(t, hasPrimaryFloor,
		"production requiredByRel has NO primary rel: 937's primary-union widen is a dead no-op")
}

// TestSNBSeedAdmissionReclaimsPendingCPUNoProgressRegression is the red/green
// proof for the production-real stall.
//
// A pending admission protects CPU 1 that still lives inside the reclaim NUMA
// bucket. normalizeReclaimTargetsByPrimary folds CPU 1 onto the primary kubepods
// root and deducts it from the bucket, so the effective bucket target is {2,3}.
// Before the fix, the frozen bucket floor stayed at the canonical budget
// {1,2,3}. ParentSafety's RequiredFloorDeficit = required{1,2,3} - observed{2,3} =
// {1} permanently: the expand envelope can never claw the pending CPU back out
// of primary, so the fixed-point loop stalls as a bare ErrNoProgress.
//
// reconcileReclaimNUMABucketRequiredFloors intersects the frozen bucket floor
// with the post-deduction effective target, collapsing it to {2,3}; the deficit
// clears and admission converges without inventing a primary leaf.
func TestSNBSeedAdmissionReclaimsPendingCPUNoProgressRegression(t *testing.T) {
	t.Parallel()

	specs := snbAdmissionNodeSpecs()
	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(specs)

	dag, err := BuildDAG(specs)
	require.NoError(t, err)

	// Pending admission folds the seed's protected CPU 1 onto the controlled
	// primary ancestor (kubepods). No primary-leaf DAG node is invented.
	pendingRequiredByRel := map[string]machine.CPUSet{
		"kubepods": machine.MustParse("1"),
	}

	effective, err := computeEffectiveTargets(dag, false, snbAdmissionCPUDetails(), pendingRequiredByRel, requiredByRel)
	require.NoError(t, err)

	// Primary absorbs the pending CPU; the bucket sheds it.
	require.Equal(t, machine.MustParse("0-1"), effective["kubepods"],
		"controlled primary must grow onto the pending CPU")
	require.Equal(t, machine.MustParse("2-3"), effective["reclaimed/reclaimed-0"],
		"reclaim bucket must shed the pending CPU")
	require.True(t, effective["reclaimed/reclaimed-0"].IsSubsetOf(effective["reclaimed"]),
		"reclaim bucket must remain contained by its reclaim parent")

	// The production-real green invariant: the reconciled bucket required floor
	// must never demand a CPU the effective target has shed. ParentSafety owes
	// the bucket floor minus the (already enforced) effective target.
	bucketFloor := requiredByRel["reclaimed/reclaimed-0"]
	require.True(t, bucketFloor.IsSubsetOf(effective["reclaimed/reclaimed-0"]),
		"reclaim bucket required floor must be a subset of its effective target; "+
			"floor=%s effective=%s otherwise ParentSafety owes the pending CPU back and stalls",
		bucketFloor, effective["reclaimed/reclaimed-0"])
	require.False(t, bucketFloor.Contains(1),
		"reconciled floor must not still demand the pending CPU 1, floor=%s", bucketFloor)
}

// TestSNBReconcileShrinksOnlyBucketFloors pins the narrowness of the fix: the
// reconcile intersects ONLY reclaim NUMA-bucket floors with their effective
// targets. It never widens a floor, never touches a primary floor, and never
// widens a non-bucket reclaim floor.
func TestSNBReconcileShrinksOnlyBucketFloors(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG(snbAdmissionNodeSpecs())
	require.NoError(t, err)

	effective := map[string]machine.CPUSet{
		"kubepods":              machine.MustParse("0-1"),
		"reclaimed":             machine.MustParse("2-3"),
		"reclaimed/reclaimed-0": machine.MustParse("2-3"),
	}
	requiredByRel := map[string]machine.CPUSet{
		"reclaimed/reclaimed-0": machine.MustParse("1-3"),
	}
	reconcileReclaimNUMABucketRequiredFloors(dag, effective, requiredByRel)

	require.Equal(t, machine.MustParse("2-3"), requiredByRel["reclaimed/reclaimed-0"],
		"bucket floor must shrink to what the effective target retains")
}

// TestSNBReconcileNoOpWhenBucketKeepsAllCPUs ensures the reconcile is a no-op
// when the bucket retains its whole canonical budget (no pending deduction).
func TestSNBReconcileNoOpWhenBucketKeepsAllCPUs(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG(snbAdmissionNodeSpecs())
	require.NoError(t, err)

	effective := map[string]machine.CPUSet{
		"kubepods":              machine.MustParse("0"),
		"reclaimed":             machine.MustParse("1-3"),
		"reclaimed/reclaimed-0": machine.MustParse("1-3"),
	}
	requiredByRel := map[string]machine.CPUSet{
		"reclaimed/reclaimed-0": machine.MustParse("1-3"),
	}
	reconcileReclaimNUMABucketRequiredFloors(dag, effective, requiredByRel)
	require.Equal(t, machine.MustParse("1-3"), requiredByRel["reclaimed/reclaimed-0"],
		"un-deducted bucket keeps its canonical floor")
}

// TestSNBMultiNUMAContainmentAndAllowedBound proves the normalize+reconcile path
// preserves the structural invariants the fix must not break: each reclaim NUMA
// bucket stays inside its NUMA upper bound, contained by its reclaim parent, and
// sibling NUMA buckets stay disjoint. A pending CPU on NUMA0 is folded onto the
// primary and deducted from the NUMA0 bucket; the NUMA1 bucket is untouched.
func TestSNBMultiNUMAContainmentAndAllowedBound(t *testing.T) {
	t.Parallel()

	// 8 CPUs: NUMA0 = 0-3, NUMA1 = 4-7.
	cpuDetails := machine.CPUDetails{}
	for c := 0; c < 8; c++ {
		numa := 0
		if c >= 4 {
			numa = 1
		}
		cpuDetails[c] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: 0, CoreID: c}
	}

	specs := []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0"), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("1-7"), ControlledRoot: true, TrustAnchor: true, Mems: "0"},
		{
			Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("1-3"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-3"), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "0"},
		},
		{
			Rel: "reclaimed/reclaimed-1", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), TrustAnchor: true, Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("4-7"), Scope: TopologyScopeNUMANode}, Metadata: map[string]string{"numa": "1"},
		},
	}
	requiredByRel := RequiredCPUSetByRelFromNodeSpecs(specs)

	dag, err := BuildDAG(specs)
	require.NoError(t, err)

	// Pending admission folds NUMA0 CPU 1 onto the primary.
	pendingRequiredByRel := map[string]machine.CPUSet{"kubepods": machine.MustParse("1")}

	effective, err := computeEffectiveTargets(dag, false, cpuDetails, pendingRequiredByRel, requiredByRel)
	require.NoError(t, err)

	b0 := effective["reclaimed/reclaimed-0"]
	b1 := effective["reclaimed/reclaimed-1"]

	// NUMA0 bucket sheds the pending CPU; NUMA1 bucket is untouched.
	require.Equal(t, machine.MustParse("2-3"), b0, "NUMA0 bucket sheds the pending CPU")
	require.Equal(t, machine.MustParse("4-7"), b1, "NUMA1 bucket is untouched")

	// Allowed bound: each bucket stays inside its NUMA upper bound.
	require.True(t, b0.IsSubsetOf(machine.MustParse("0-3")), "b0 must stay in NUMA0 bound, got=%s", b0)
	require.True(t, b1.IsSubsetOf(machine.MustParse("4-7")), "b1 must stay in NUMA1 bound, got=%s", b1)

	// Parent containment: each bucket stays inside the reclaim parent.
	require.True(t, b0.IsSubsetOf(effective["reclaimed"]), "b0 must be contained by reclaimed")
	require.True(t, b1.IsSubsetOf(effective["reclaimed"]), "b1 must be contained by reclaimed")

	// Sibling buckets disjoint.
	require.True(t, b0.IsSubsetOf(machine.MustParse("0-3")) && b1.IsSubsetOf(machine.MustParse("4-7")))
	require.True(t, b0.Intersection(b1).IsEmpty(), "sibling NUMA buckets must be disjoint")

	// Reconciled floors respect the same containment: floors are subsets of the
	// retained effective targets and stay inside the NUMA bounds.
	require.True(t, requiredByRel["reclaimed/reclaimed-0"].IsSubsetOf(b0),
		"reconciled NUMA0 floor must be a subset of the retained target")
	require.True(t, requiredByRel["reclaimed/reclaimed-1"].IsSubsetOf(b1),
		"reconciled NUMA1 floor must be a subset of the retained target")
	require.True(t, requiredByRel["reclaimed/reclaimed-0"].IsSubsetOf(machine.MustParse("0-3")))
	require.True(t, requiredByRel["reclaimed/reclaimed-1"].IsSubsetOf(machine.MustParse("4-7")))
}

// sharedRampUpDeferralFixture wires the controlled-DAG skeleton of a shared
// ramp-up admission in which a reclaim NUMA bucket still wants a CPU the primary
// side currently legitimately holds.
//
//	primary root desired {0}, observed superset {0,2};
//	shared leaf observed {0,2}, projected expectation {0};
//	reclaim root/bucket canonical desired {1,2,3}, currently enforced {1,3};
//	protected pending {0} sits entirely on the primary side.
//
// The CPU 2 is a desired reclaim addition still held by primary and unrelated to
// the protected pending set.
func sharedRampUpDeferralFixture(t *testing.T) *admissionTraceFixture {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	for c := 0; c < 4; c++ {
		fixture.cpuDetails[c] = machine.CPUTopoInfo{NUMANodeID: 0, CoreID: c}
	}
	fixture.round.allowEmptyTarget = false

	fixture.addPrimary("kubepods", "0", "0")
	// Controlled root observed is the domain superset {0,2}.
	fixture.driver.add("kubepods", CgroupIdentity{Device: 1, Inode: fixture.allocInode()}, "0,2", "0")
	// Shared leaf currently holds the primary-held addition CPU 2; the shared
	// projection expects only CPU 0.
	fixture.driver.add("kubepods/podShared", CgroupIdentity{Device: 1, Inode: fixture.allocInode()}, "0,2", "0")
	fixture.dynamicByRel = map[string]machine.CPUSet{"kubepods/podShared": machine.MustParse("0")}

	fixture.addReclaim("reclaimed", "1,3", "0")
	fixture.addReclaim("reclaimed/reclaimed-0", "1,3", "0")
	for i := range fixture.specs {
		switch fixture.specs[i].Rel {
		case "reclaimed":
			fixture.specs[i].CPUs = machine.MustParse("1,2,3")
		case "reclaimed/reclaimed-0":
			fixture.specs[i].CPUs = machine.MustParse("1,2,3")
			fixture.specs[i].Role = TopoNodeRoleReclaimNUMABucket
			fixture.specs[i].Constraint = TopologyConstraint{CPUUpperBound: machine.MustParse("0-3"), Scope: TopologyScopeNUMANode}
			fixture.specs[i].Metadata = map[string]string{"numa": "0"}
		}
	}
	fixture.targetByRel["reclaimed"] = machine.MustParse("1,2,3")
	fixture.targetByRel["reclaimed/reclaimed-0"] = machine.MustParse("1,2,3")
	fixture.requireCPUSet("reclaimed/reclaimed-0", "1,2,3")
	return fixture
}

// TestSNBSharedRampUpReclaimAdditionDeferredConverges is the regression proof: a
// desired reclaim addition (CPU 2) that is still legitimately held by the
// primary side, and that is unrelated to the protected pending set (CPU 0), is
// allowed to ride out as deferred cleanup together with the primary release.
//
// Before the fix the primary cleanup was deferred while ParentSafety still owed
// the CPU: the reclaim required floor stayed at the canonical {1,2,3}, so the
// bucket reported a permanent RequiredFloorDeficit={2} and the fixed-point loop
// stalled as a bare ErrNoProgress with an empty required plan.
//
// The fix derives deferSet = floor ∩ observed-primary − protected-pending,
// shrinks both the reclaim required floor and the reclaim desired targets by
// exactly that set, and routes the matching primary shrink and reclaim grow into
// the existing OperationDeferredCleanup. The round then converges ParentSafe
// without a single required write, and the bucket floor stays non-empty.
func TestSNBSharedRampUpReclaimAdditionDeferredConverges(t *testing.T) {
	t.Parallel()

	fixture := sharedRampUpDeferralFixture(t)
	pending := machine.MustParse("0")
	pend := map[string]machine.CPUSet{"kubepods": pending.Clone()}

	dag, err := BuildDAG(fixture.specs)
	require.NoError(t, err)
	eff, err := computeEffectiveTargets(dag, false, fixture.cpuDetails, pend, fixture.requiredByRel)
	require.NoError(t, err)
	for i := range fixture.specs {
		fixture.specs[i].CPUs = eff[fixture.specs[i].Rel].Clone()
	}
	fixture.targetByRel = eff

	fixture.round.protectedPending = pending.Clone()
	fixture.round.pendingRequiredByRel = pend
	fixture.round.dynamicByRel = fixture.dynamicByRel

	// Canonical inputs must never be mutated by the frozen compilation: the
	// adjustment derives an immutable projection and leaves the round's own
	// canonical maps intact for a fresh round to re-clone.
	canonicalFloor := machine.MustParse("1,2,3")
	canonicalTarget := machine.MustParse("1,2,3")

	base := fixture.snapshot()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err, "want ParentSafe convergence, err=%v", err)
	require.False(t, errors.Is(err, ErrNoProgress), "must not stall as a bare ErrNoProgress")

	// Single frozen projection: exactly one immutable adjusted floor/target for
	// the reclaim NUMA bucket, derived once and shared by planning, split
	// classification and the frozen evaluation. CPU 2 (primary-held overshoot)
	// is trimmed from both; the floor stays non-empty.
	require.Equal(t, canonicalFloor, fixture.requiredByRel["reclaimed/reclaimed-0"],
		"canonical required floor must not be mutated by compilation")
	require.Equal(t, canonicalTarget, fixture.targetByRel["reclaimed/reclaimed-0"],
		"canonical desired target must not be mutated by compilation")
	require.Equal(t, machine.MustParse("1,3"), trace.RequiredCPUSetByRel["reclaimed/reclaimed-0"],
		"frozen round required floor must be the single adjusted projection")
	require.Equal(t, machine.MustParse("1,3"), trace.EvaluationInput.RequiredByRel["reclaimed/reclaimed-0"],
		"frozen evaluation must read the same adjusted floor")
	require.Equal(t, machine.MustParse("1,3"), trace.EvaluationInput.TargetByRel["reclaimed/reclaimed-0"],
		"frozen evaluation must read the same adjusted desired target")

	// Operation contract: no required handoff write is executed while the primary
	// still overshoots CPU 2. The only planned operations are the primary drains,
	// both classified deferred_cleanup; the reclaim grow onto CPU 2 is never even
	// planned because the adjusted desired already excludes it.
	var executed []PlanOperation
	for _, phase := range trace.Phases {
		executed = append(executed, phase.Operations...)
	}
	require.Empty(t, executed, "no required handoff write may execute; ops=%+v", executed)
	require.NotContains(t, trace.EvaluationInput.DeferredCleanupRels, "reclaimed/reclaimed-0",
		"the reclaim bucket must not be a deferred cleanup rel; deferred=%v", trace.EvaluationInput.DeferredCleanupRels)
	require.Contains(t, trace.EvaluationInput.DeferredCleanupRels, "kubepods")
	require.Contains(t, trace.EvaluationInput.DeferredCleanupRels, "kubepods/podShared")

	// Stall contract: ParentSafe converges with the primary overshoot still held,
	// so the round is ParentSafe but NOT fully converged (the deferred cleanup has
	// not run yet).
	ps := trace.FinalEvaluation.ParentSafety
	require.True(t, ps.Safe, "ParentSafety must converge; %+v", ps)
	require.Empty(t, ps.RequiredFloorDeficit, "no floor deficit; %+v", ps.RequiredFloorDeficit)
	require.Empty(t, ps.UnsafeRequiredRels, "no unsafe required grow rels; %+v", ps.UnsafeRequiredRels)
	require.False(t, trace.FinalEvaluation.Report.FullyConverged,
		"round must be ParentSafe-deferred, not fully converged, while CPU 2 is held by primary")

	// Structural invariants the deferral must preserve.
	primaryUnion := trace.EvaluationInput.DesiredByDomain[DomainPrimary]
	reclaimUnion := trace.EvaluationInput.DesiredByDomain[DomainReclaim]
	require.True(t, pending.IsSubsetOf(primaryUnion),
		"protected pending must stay inside the primary side; pending=%s primary=%s", pending, primaryUnion)
	require.True(t, pending.Intersection(reclaimUnion).IsEmpty(),
		"protected pending must never enter reclaim; pending=%s reclaim=%s", pending, reclaimUnion)
	require.True(t, ps.PrimaryReclaimOverlap.IsEmpty(), "primary and reclaim must not overlap")
	require.True(t, primaryUnion.Intersection(reclaimUnion).IsEmpty(),
		"desired primary/reclaim domains must be disjoint")
}

// deferralProjectionEnv wires the minimal controlled DAG and observed snapshot
// the single adjustedReclaimProjection derivation inspects: observed primary
// domain {0,2} and reclaim domain {1,3}, with one reclaim NUMA bucket.
func deferralProjectionEnv(t *testing.T, pending machine.CPUSet, required, target map[string]machine.CPUSet) (*coordinatorRound, *CompleteSnapshot) {
	t.Helper()
	specs := []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("1,3"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("1,3")},
	}
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	snap := &CompleteSnapshot{
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0,2"),
			DomainReclaim: machine.MustParse("1,3"),
		},
		DomainByRel: map[string]DomainID{
			"kubepods": DomainPrimary, "reclaimed": DomainReclaim, "reclaimed/reclaimed-0": DomainReclaim,
		},
	}
	round := &coordinatorRound{
		dag:              dag,
		requiredByRel:    required,
		targetByRel:      target,
		protectedPending: pending,
		objective:        ConvergenceObjectiveParentSafe,
	}
	return round, snap
}

// TestSNBSharedRampUpDeferralSkipsProtectedPending proves the overshoot set never
// sweeps a CPU that is protected pending. The primary-held addition CPU 2 sits
// inside protected pending, so overshoot is empty and the bucket floor stays put.
func TestSNBSharedRampUpDeferralSkipsProtectedPending(t *testing.T) {
	t.Parallel()
	required := map[string]machine.CPUSet{"reclaimed/reclaimed-0": machine.MustParse("1,2,3")}
	target := map[string]machine.CPUSet{"kubepods": machine.MustParse("0"), "reclaimed/reclaimed-0": machine.MustParse("1,2,3")}
	round, snap := deferralProjectionEnv(t, machine.MustParse("0,2"), required, target) // CPU 2 is pending.

	floor, desired := round.adjustedReclaimProjection(snap)
	require.Equal(t, machine.MustParse("1,2,3"), floor["reclaimed/reclaimed-0"],
		"floor must remain unchanged when the primary-held addition is protected pending")
	require.Equal(t, machine.MustParse("1,2,3"), desired["reclaimed/reclaimed-0"],
		"desired must remain unchanged when overshoot is empty")
	require.Equal(t, machine.MustParse("1,2,3"), required["reclaimed/reclaimed-0"],
		"projection must not mutate the round's canonical floor")
}

// TestSNBSharedRampUpDeferralFailsClosedOnHollowFloor proves the reclaim NUMA
// bucket floor is never hollowed out to empty. When the overshoot set would
// remove the last CPU of a bucket floor, that floor is left untouched (fail
// closed) so ParentSafety never owes a CPU the round then refuses to enforce.
func TestSNBSharedRampUpDeferralFailsClosedOnHollowFloor(t *testing.T) {
	t.Parallel()
	required := map[string]machine.CPUSet{"reclaimed/reclaimed-0": machine.MustParse("2")}
	target := map[string]machine.CPUSet{"kubepods": machine.MustParse("0"), "reclaimed/reclaimed-0": machine.MustParse("2")}
	round, snap := deferralProjectionEnv(t, machine.MustParse("0"), required, target)

	floor, desired := round.adjustedReclaimProjection(snap)
	require.False(t, floor["reclaimed/reclaimed-0"].IsEmpty(), "floor must stay non-empty")
	require.Equal(t, machine.MustParse("2"), floor["reclaimed/reclaimed-0"],
		"hollow floor must be left untouched, never shrunk to empty")
	require.Equal(t, machine.MustParse("2"), desired["reclaimed/reclaimed-0"],
		"desired must be left untouched alongside its fail-closed floor")
}

// TestSNBSharedRampUpDeferralNonBucketFloorUntouched pins the narrowness of the
// derivation: only TopoNodeRoleReclaimNUMABucket floors are trimmed. The reclaim
// root floor (a plain reclaim node) is never adjusted, even when it holds the
// same primary-overshoot CPU.
func TestSNBSharedRampUpDeferralNonBucketFloorUntouched(t *testing.T) {
	t.Parallel()
	required := map[string]machine.CPUSet{
		"reclaimed":             machine.MustParse("1,2,3"),
		"reclaimed/reclaimed-0": machine.MustParse("1,2,3"),
	}
	target := map[string]machine.CPUSet{
		"kubepods": machine.MustParse("0"), "reclaimed": machine.MustParse("1,2,3"), "reclaimed/reclaimed-0": machine.MustParse("1,2,3"),
	}
	round, snap := deferralProjectionEnv(t, machine.MustParse("0"), required, target)

	floor, desired := round.adjustedReclaimProjection(snap)
	require.Equal(t, machine.MustParse("1,2,3"), floor["reclaimed"],
		"non-bucket reclaim floor must never be trimmed (floors are trimmed only for NUMA buckets)")
	require.Equal(t, machine.MustParse("1,3"), floor["reclaimed/reclaimed-0"],
		"bucket floor must shed the primary-held overshoot CPU")
	require.Equal(t, machine.MustParse("1,3"), desired["reclaimed"],
		"reclaim root desired must shrink with its bucket to preserve containment")
	require.Equal(t, machine.MustParse("1,3"), desired["reclaimed/reclaimed-0"],
		"bucket desired must shrink by the same overshoot set")
}

// TestSNBSharedRampUpDeferralHollowCoverKeepsRequiredGrow pins the fail-closed
// linkage: when a bucket floor is entirely covered by the overshoot set, the
// floor is preserved (non-empty) and the desired targets must NOT be trimmed by
// that set. Otherwise the planner would drop the grow, leaving ParentSafety with
// a permanent RequiredFloorDeficit. Bucket {2,3} floor vs observed reclaim {4}:
// overshoot {2,3} would hollow it, so neither the bucket nor its root desired is
// trimmed and the grow onto {2,3} stays required.
func TestSNBSharedRampUpDeferralHollowCoverKeepsRequiredGrow(t *testing.T) {
	t.Parallel()
	specs := []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("2,3,4")},
	}
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	snap := &CompleteSnapshot{
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0,2,3"),
			DomainReclaim: machine.MustParse("4"),
		},
		DomainByRel: map[string]DomainID{
			"kubepods": DomainPrimary, "reclaimed": DomainReclaim, "reclaimed/reclaimed-0": DomainReclaim,
		},
	}
	round := &coordinatorRound{
		dag:              dag,
		requiredByRel:    map[string]machine.CPUSet{"reclaimed/reclaimed-0": machine.MustParse("2,3")},
		targetByRel:      map[string]machine.CPUSet{"kubepods": machine.MustParse("0"), "reclaimed": machine.MustParse("2,3,4"), "reclaimed/reclaimed-0": machine.MustParse("2,3,4")},
		protectedPending: machine.MustParse("0"),
		objective:        ConvergenceObjectiveParentSafe,
	}

	floor, desired := round.adjustedReclaimProjection(snap)
	require.Equal(t, machine.MustParse("2,3"), floor["reclaimed/reclaimed-0"],
		"hollow bucket floor must be preserved, never shrunk to empty")
	require.Equal(t, machine.MustParse("2,3,4"), desired["reclaimed/reclaimed-0"],
		"hollow bucket desired must keep demanding the shed CPUs so the grow stays required")
	require.Equal(t, machine.MustParse("2,3,4"), desired["reclaimed"],
		"ancestor reclaim root desired must keep demanding the hollow bucket's CPUs")
}

// TestSNBSharedRampUpDeferralMixedHollowAndShedRootContainment proves the desired
// trim follows exactly the union of CPUs bucket floors actually shed: a bucket
// that sheds CPU 3 trims its desired (and the root), while a hollow bucket that
// kept CPU 2 leaves CPU 2 demanded on both itself and the root.
func TestSNBSharedRampUpDeferralMixedHollowAndShedRootContainment(t *testing.T) {
	t.Parallel()
	specs := []NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4,5"), ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaimed/reclaimed-0", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("2,4")},
		{Rel: "reclaimed/reclaimed-1", ParentRel: "reclaimed", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("3,5")},
	}
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	snap := &CompleteSnapshot{
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0,2,3"),
			DomainReclaim: machine.MustParse("4,5"),
		},
		DomainByRel: map[string]DomainID{
			"kubepods": DomainPrimary, "reclaimed": DomainReclaim,
			"reclaimed/reclaimed-0": DomainReclaim, "reclaimed/reclaimed-1": DomainReclaim,
		},
	}
	round := &coordinatorRound{
		dag: dag,
		requiredByRel: map[string]machine.CPUSet{
			"reclaimed/reclaimed-0": machine.MustParse("2"),
			"reclaimed/reclaimed-1": machine.MustParse("3,5"),
		},
		targetByRel: map[string]machine.CPUSet{
			"kubepods": machine.MustParse("0"), "reclaimed": machine.MustParse("2,3,4,5"),
			"reclaimed/reclaimed-0": machine.MustParse("2,4"), "reclaimed/reclaimed-1": machine.MustParse("3,5"),
		},
		protectedPending: machine.MustParse("0"),
		objective:        ConvergenceObjectiveParentSafe,
	}

	floor, desired := round.adjustedReclaimProjection(snap)
	// bucket0 floor {2} is entirely overshoot {2} → fail-closed preserved.
	require.Equal(t, machine.MustParse("2"), floor["reclaimed/reclaimed-0"], "hollow bucket0 floor preserved")
	// bucket1 floor {3,5} − reclaim{4,5} = {3}; overshoot {3} sheds to {5}.
	require.Equal(t, machine.MustParse("5"), floor["reclaimed/reclaimed-1"], "shedding bucket1 floor trims CPU 3")
	// Only CPU 3 was shed; desired trims exactly {3}, keeps {2} (hollow).
	require.Equal(t, machine.MustParse("2,4"), desired["reclaimed/reclaimed-0"], "hollow bucket0 desired unchanged")
	require.Equal(t, machine.MustParse("5"), desired["reclaimed/reclaimed-1"], "shedding bucket1 desired trims CPU 3")
	require.Equal(t, machine.MustParse("2,4,5"), desired["reclaimed"], "root desired keeps hollow CPU 2, sheds CPU 3")
}

// TestSNBSharedRampUpDeferralRebaseStable proves the rebase (second buildPlan on
// the already-adjusted round maps) consumes the same projection and never drifts:
// re-running the derivation is idempotent, so the drain and expand/split
// classification agree.
func TestSNBSharedRampUpDeferralRebaseStable(t *testing.T) {
	t.Parallel()
	required := map[string]machine.CPUSet{"reclaimed/reclaimed-0": machine.MustParse("1,2,3")}
	target := map[string]machine.CPUSet{"kubepods": machine.MustParse("0"), "reclaimed/reclaimed-0": machine.MustParse("1,2,3")}
	round, snap := deferralProjectionEnv(t, machine.MustParse("0"), required, target)

	floor1, desired1 := round.adjustedReclaimProjection(snap)
	round.requiredByRel = floor1
	round.targetByRel = desired1
	floor2, desired2 := round.adjustedReclaimProjection(snap)

	require.Equal(t, floor1["reclaimed/reclaimed-0"], floor2["reclaimed/reclaimed-0"],
		"rebase must reproduce the same adjusted floor")
	require.Equal(t, desired1["reclaimed/reclaimed-0"], desired2["reclaimed/reclaimed-0"],
		"rebase must reproduce the same adjusted desired target")
	require.Equal(t, machine.MustParse("1,3"), floor2["reclaimed/reclaimed-0"])
	require.Equal(t, machine.MustParse("1,3"), desired2["reclaimed/reclaimed-0"])
}

// TestSNBSharedRampUpDeferralSplitDoesNotMutateInputs proves SplitPlanForAdmission
// never mutates the caller's floor map: it only classifies against the already
// adjusted projection passed in, so no floor/target change can leak into an
// outer round.
func TestSNBSharedRampUpDeferralSplitDoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	base := &CompleteSnapshot{
		DomainUnion: map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0,2"), DomainReclaim: machine.MustParse("1,3")},
		DomainByRel: map[string]DomainID{"reclaimed/bucket": DomainReclaim},
	}
	plan := &PhasePlan{
		Base: base,
		Operations: []PlanOperation{{
			Rel:             "reclaimed/bucket",
			Direction:       WriteGrow,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("1,3")},
			Target:          CPUSetTarget{CPUs: machine.MustParse("1,2,3")},
		}},
	}
	floor := map[string]machine.CPUSet{"reclaimed/bucket": machine.MustParse("1,2,3")}

	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{
		PendingCPUSet:       machine.MustParse("0"),
		RequiredCPUSetByRel: floor,
	})
	require.NoError(t, err)
	require.Equal(t, machine.MustParse("1,2,3"), floor["reclaimed/bucket"],
		"split must not mutate the caller's requiredByRel map")
	require.NotEmpty(t, required.Operations,
		"a planned grow onto a floor CPU is required, not silently deferred")
	require.Empty(t, deferred.Operations, "no extra deferred ops")
}
