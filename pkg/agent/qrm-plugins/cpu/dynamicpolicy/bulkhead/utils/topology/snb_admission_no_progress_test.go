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
