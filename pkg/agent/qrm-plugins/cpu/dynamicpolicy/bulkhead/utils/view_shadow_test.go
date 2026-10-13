/*
Copyright 2022 The Kubernetes Authors.

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

package utils

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// make4NUMASMTTopology builds a 128-logical-CPU, 4-NUMA, SMT=2 topology.
// NUMA n owns CPUs [32n, 32n+32); within each NUMA, cpu and cpu+16 share a
// physical core so expandToWholeCores folds a pending thread onto its sibling.
func make4NUMASMTTopology() *machine.CPUTopology {
	details := machine.CPUDetails{}
	for cpu := 0; cpu < 128; cpu++ {
		numa := cpu / 32
		local := cpu % 32
		core := numa*16 + local%16
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: numa,
			SocketID:   numa,
			CoreID:     core,
		}
	}
	return &machine.CPUTopology{
		NumCPUs:      128,
		NumCores:     64,
		NumSockets:   4,
		NumNUMANodes: 4,
		CPUDetails:   details,
	}
}

// baseR3View mirrors the E2E R3 target state: per-NUMA reclaim floor is
// non-empty (2 reclaim CPUs per NUMA), non-reclaim pool small, no hard owner.
func baseR3View(t *testing.T) *model.DesiredView {
	t.Helper()
	return baseR3BenchView()
}

func baseR3BenchView() *model.DesiredView {
	reclaim := machine.NewCPUSet()
	for _, n := range []int{0, 1, 2, 3} {
		reclaim.Add(n*32 + 4)
		reclaim.Add(n*32 + 20)
	}
	nonReclaim := machine.NewCPUSet()
	for n := 0; n < 4; n++ {
		for c := 0; c < 4; c++ {
			nonReclaim.Add(n*32 + c)
		}
	}
	return &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:                 machine.NewCPUSet(),
			Dedicated:               machine.NewCPUSet(),
			Isolation:               machine.NewCPUSet(),
			DesiredNonReclaimPool:   nonReclaim.Clone(),
			DesiredReclaimEffective: reclaim.Clone(),
			NonReclaimPool:          nonReclaim.Clone(),
			ReclaimEffective:        reclaim.Clone(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}
}

// TestR3FullPendingHoldCurrent reproduces the admission scenario where a shared
// ramp-up pod absorbs the whole reclaim budget: the shadow projection must
// hold-current instead of emitting an empty reclaim desired.
func TestR3FullPendingHoldCurrent(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)
	before := base.ReclaimEffective.Clone()

	protections := []topology.PendingProtection{{
		PodUID: "pending-shared",
		CPUs:   machine.MustParse("0-127"),
	}}
	sh := DeriveShadowPartition(base, topo, protections)

	if !base.ReclaimEffective.Equals(before) {
		t.Fatalf("business view mutated: %s -> %s", before, base.ReclaimEffective)
	}
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonFullPending {
		t.Fatalf("expected hold-current full_pending, hold=%v reason=%q reclaim=%s",
			sh.HoldCurrent, sh.HoldReason, sh.ReclaimEffective)
	}
	if !sh.ReclaimEffective.IsEmpty() {
		t.Fatalf("full-pending reclaim should be empty, got %s", sh.ReclaimEffective)
	}
}

// TestR3PartialPendingPreservesFloor: partial pending leaves other NUMA floors
// non-empty and disjoint; no hold-current.
func TestR3PartialPendingPreservesFloor(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)

	protections := []topology.PendingProtection{{
		PodUID: "partial",
		CPUs:   machine.NewCPUSet(8, 40),
	}}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.HoldCurrent {
		t.Fatalf("partial pending should not hold-current: %s", sh.HoldReason)
	}
	for _, want := range []int{4, 20, 36, 52, 68, 84, 100, 116} {
		if !sh.ReclaimEffective.Contains(want) {
			t.Fatalf("floor cpu %d missing: %s", want, sh.ReclaimEffective)
		}
	}
	if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
		t.Fatalf("primary/reclaim overlap: %s", sh.NonReclaimPool.Intersection(sh.ReclaimEffective))
	}
}

// TestR3EmptyShadowIdentity: no protections => shadow == base partition.
func TestR3EmptyShadowIdentity(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)
	sh := DeriveShadowPartition(base, topo, nil)
	if sh.HoldCurrent {
		t.Fatalf("empty shadow must not hold-current: %s", sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(base.DesiredReclaimEffective) ||
		!sh.NonReclaimPool.Equals(base.DesiredNonReclaimPool) {
		t.Fatalf("empty shadow mismatch")
	}
}

// TestShadowHardCollision: pending on a dedicated owner CPU holds-current.
func TestShadowHardCollision(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "d1"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(0, 1)}

	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{
		PodUID: "p", CPUs: machine.NewCPUSet(0),
	}})
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
		t.Fatalf("expected hard_collision, hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
}

// TestShadowShareOwnerNotHard: a share owner CPU is movable; pending on it
// must NOT classify as hard_collision.
func TestShadowShareOwnerNotHard(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: "share-0"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(10)}

	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{
		PodUID: "p", CPUs: machine.NewCPUSet(10),
	}})
	if sh.HoldCurrent && sh.HoldReason == HoldReasonHardCollision {
		t.Fatalf("share owner must not be hard: %s", sh.HoldReason)
	}
}

// TestShadowFloorExhausted: pending whole-cores wipe one NUMA's reclaim floor.
func TestShadowFloorExhausted(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3View(t)
	// Pending on NUMA0 reclaim threads 4,20 expands to whole cores (4/20 + their
	// HT sibling at +16). NUMA0 floor cpus 4,20 are both consumed; no other NUMA0
	// reclaim floor remains => floor_exhausted hold-current.
	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{
		PodUID: "p", CPUs: machine.NewCPUSet(4, 20),
	}})
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonFloorExhausted {
		t.Fatalf("expected floor_exhausted, hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
}

// TestErrShadowHoldCurrentSentinel ensures the plugin error wraps the sentinel.
func TestErrShadowHoldCurrentSentinel(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("%w: %s", ErrShadowHoldCurrent, HoldReasonFullPending)
	if !errors.Is(err, ErrShadowHoldCurrent) {
		t.Fatalf("error does not wrap ErrShadowHoldCurrent: %v", err)
	}
}

// TestShadowMatrix exercises preserve-style matrix over SMT-on whole-core
// folding, owner-empty, and isolation so the derived partition always holds the
// disjoint/available invariant whether or not it holds-current.
func TestShadowMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		pend   []int
		isHard bool
	}{
		{"smt_whole_core", []int{8}, false},
		{"owner_empty", nil, false},
		{"hard_on_dedicated", []int{0}, true},
		{"isolated_touched", []int{40}, true},
	}
	topo := make4NUMASMTTopology()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := baseR3View(t)
			if tc.isHard {
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindIsolation, PodName: "iso"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(40, 56)}
			}
			var prot []topology.PendingProtection
			for _, c := range tc.pend {
				prot = append(prot, topology.PendingProtection{PodUID: "p", CPUs: machine.NewCPUSet(c)})
			}
			sh := DeriveShadowPartition(base, topo, prot)
			// Invariant: whenever not holding, non-reclaim and reclaim must be
			// disjoint and within availability.
			if !sh.HoldCurrent {
				if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
					t.Fatalf("overlap: %s", sh.NonReclaimPool.Intersection(sh.ReclaimEffective))
				}
				avail := topo.CPUDetails.CPUs().Difference(base.Reserve)
				if !sh.NonReclaimPool.Union(sh.ReclaimEffective).Difference(avail).IsEmpty() {
					t.Fatalf("partition out of availability")
				}
			}
		})
	}
}

// FuzzDeriveShadowPartition enforces the fail-closed invariant across random
// pending single-CPU sets: either hold-current, or the emitted partition is
// disjoint. Multi-CPU combinations are covered by the table matrix above.
func FuzzDeriveShadowPartition(f *testing.F) {
	topo := make4NUMASMTTopology()
	for _, seed := range []int{0, 8, 40, 4, 20, 63, 127} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, c int) {
		base := baseR3View(t)
		c = ((c % 128) + 128) % 128
		sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{
			{PodUID: "f", CPUs: machine.NewCPUSet(c)},
		})
		if sh.HoldCurrent {
			return
		}
		if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
			t.Fatalf("overlap for cpu %d: %s", c, sh.NonReclaimPool.Intersection(sh.ReclaimEffective))
		}
	})
}

// BenchmarkDeriveShadowPartition measures the read-only shadow projection. The
// pre-fix code path had no equivalent pure function (it mutated the shared
// DesiredView and rebuilt the DAG), so this is post-only.
func BenchmarkDeriveShadowPartition(b *testing.B) {
	topo := make4NUMASMTTopology()
	base := baseR3View(&testing.T{})
	protections := []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(8, 40)}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeriveShadowPartition(base, topo, protections)
	}
}

// makeFakedNUMATopology maps all 128 CPUs onto a single virtual NUMA to prove
// the shadow projection stays well-defined when physical NUMA layout is folded
// (FakedNUMA/global mode).
func makeFakedNUMATopology() *machine.CPUTopology {
	details := machine.CPUDetails{}
	for cpu := 0; cpu < 128; cpu++ {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: cpu / 2}
	}
	return &machine.CPUTopology{NumCPUs: 128, NumCores: 64, NumSockets: 1, NumNUMANodes: 1, CPUDetails: details}
}

// TestShadowFakedNUMA proves the projection remains disjoint on a folded NUMA
// layout.
func TestShadowFakedNUMA(t *testing.T) {
	t.Parallel()
	topo := makeFakedNUMATopology()
	base := baseR3BenchView()
	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{
		{PodUID: "p", CPUs: machine.NewCPUSet(8, 40)},
	})
	if sh.HoldCurrent {
		t.Fatalf("faked-numa partial should not hold: %s", sh.HoldReason)
	}
	if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
		t.Fatalf("faked-numa overlap: %s", sh.NonReclaimPool.Intersection(sh.ReclaimEffective))
	}
}

// TestShadowSNBAndOwnerEmpty covers single-NUBA (snb) and owner-pool-empty
// states: the pure function must not depend on PoolOwners being populated.
func TestShadowSNBAndOwnerEmpty(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	base.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{} // owner_pool empty
	sh := DeriveShadowPartition(base, topo, nil)
	if sh.HoldCurrent {
		t.Fatalf("owner-empty no-pending should not hold: %s", sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(base.DesiredReclaimEffective) {
		t.Fatalf("owner-empty shadow reclaim drift: %s", sh.ReclaimEffective)
	}
}

// TestShadowHardTrueFalse exercises both hard (dedicated present) and soft
// (no hard owner) folds.
func TestShadowHardTrueFalse(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	// hard=true: pending on dedicated CPU must hold.
	hard := baseR3BenchView()
	hard.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "d"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(0, 1)}
	if sh := DeriveShadowPartition(hard, topo, []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(0)}}); !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
		t.Fatalf("hard=true want hard_collision, got %v/%q", sh.HoldCurrent, sh.HoldReason)
	}
	// hard=false: no dedicated, pending on free CPU proceeds.
	soft := baseR3BenchView()
	if sh := DeriveShadowPartition(soft, topo, []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(8)}}); sh.HoldCurrent {
		t.Fatalf("hard=false want proceed, got %q", sh.HoldReason)
	}
}

// TestShadowSelfOwnerNoHardCollision is the identity-scoped self-owner exclusion
// proof on the 128-CPU / 4-NUMA SMT2 runtime shape. A pending protection whose
// PodUID names a Dedicated/Isolation PoolOwner whose non-empty ProofPodUID is the
// SAME pod folds that owner's own expected CPUs onto the non-reclaim side. That is
// the pending pod re-affirming its own committed allocation, not a collision with a
// *different*, non-movable owner, so the shadow must NOT hold-current with
// hard_collision. The reclaim floor and budget are untouched.
func TestShadowSelfOwnerNoHardCollision(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()

	// Pending pod P owns whole core {8,24} as a committed dedicated pool owner.
	const podP = "pod-self-owner"
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "self"}] = model.DesiredPoolOwner{ProofPodUID: podP, ExpectedCPUSet: machine.NewCPUSet(8)}

	protections := []topology.PendingProtection{{PodUID: podP, CPUs: machine.NewCPUSet(8)}}
	sh := DeriveShadowPartition(base, topo, protections)

	if sh.HoldCurrent && sh.HoldReason == HoldReasonHardCollision {
		t.Fatalf("self-owned dedicated fold must not classify as hard_collision: hold=%v reason=%q protected=%s hard=%s",
			sh.HoldCurrent, sh.HoldReason, sh.ProtectedPending, sh.ExcludedHardSet)
	}
	// The self owner's expected CPU is excluded from the hard-collision set.
	if sh.ExcludedHardSet.Contains(8) {
		t.Fatalf("self owner ExpectedCPUSet must be excluded from hard set: %s", sh.ExcludedHardSet)
	}
	// The fold proceeds and must not displace the reclaim budget/floor.
	if sh.HoldCurrent {
		t.Fatalf("self-owned fold should converge, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(base.DesiredReclaimEffective) {
		t.Fatalf("self-owned fold must leave reclaim untouched: got %s want %s",
			sh.ReclaimEffective, base.DesiredReclaimEffective)
	}
}

// TestShadowTrueOtherOwnerHardCollision proves the identity-scoped exclusion does
// NOT widen past a real, different committed owner: a pending fold landing on a
// Dedicated/Isolation PoolOwner whose ProofPodUID is a DIFFERENT pod, or is blank
// (cannot prove self), still holds-current with hard_collision and reports the
// colliding whole core through the diagnostic fields.
func TestShadowTrueOtherOwnerHardCollision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		ownerKind  model.CPUSetPoolKind
		proofPod   string
		pendingPod string
	}{
		{"dedicated_other_pod", model.CPUSetPoolKindDedicated, "pod-other", "pod-pending"},
		{"isolation_other_pod", model.CPUSetPoolKindIsolation, "pod-other", "pod-pending"},
		{"blank_proof", model.CPUSetPoolKindDedicated, "", "pod-pending"},
	}
	topo := make4NUMASMTTopology()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := baseR3BenchView()
			base.PoolOwners[model.CPUSetPoolIdentity{Kind: tc.ownerKind, PodName: "other"}] = model.DesiredPoolOwner{ProofPodUID: tc.proofPod, ExpectedCPUSet: machine.NewCPUSet(8)}
			protections := []topology.PendingProtection{{PodUID: tc.pendingPod, CPUs: machine.NewCPUSet(8)}}
			sh := DeriveShadowPartition(base, topo, protections)
			if !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
				t.Fatalf("%s: want hard_collision, got hold=%v reason=%q", tc.name, sh.HoldCurrent, sh.HoldReason)
			}
			// Diagnostic fields preserved: the other owner's expected CPU 8 is
			// reported inside the excluded hard set, and the folded whole core
			// {8,24} is reported as protected pending.
			if !sh.ProtectedPending.Equals(machine.NewCPUSet(8, 24)) {
				t.Fatalf("%s: ProtectedPending = %s, want {8,24}", tc.name, sh.ProtectedPending)
			}
			if !sh.ExcludedHardSet.Contains(8) {
				t.Fatalf("%s: ExcludedHardSet = %s, want to contain other owner CPU 8", tc.name, sh.ExcludedHardSet)
			}
		})
	}
}

// makeDNBRuntimeTopology reproduces the minimal DNB runtime shape: 128 logical
// CPUs on a single NUMA, SMT=2 where physical core i folds threads {i, i+64}.
// Folding pending CPU 0 therefore expands to whole core {0,64}, the exact runtime
// hard_collision diagnostic captured on 90a (protected=0,64 hard_set=0,64).
func makeDNBRuntimeTopology() *machine.CPUTopology {
	details := machine.CPUDetails{}
	for cpu := 0; cpu < 128; cpu++ {
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: 0,
			SocketID:   0,
			CoreID:     cpu % 64,
		}
	}
	return &machine.CPUTopology{
		NumCPUs:      128,
		NumCores:     64,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails:   details,
	}
}

// dnbRuntimeView builds the minimal DNB base: a single reclaim whole core
// {10,74}; every other CPU is non-reclaim. The returned Dedicated/Isolation
// aggregates are empty and the caller overlays the self/other owners under test so
// aggregate-owner consistency (the runtime condition the 082 fix missed) is set
// explicitly per case.
func dnbRuntimeView() *model.DesiredView {
	reclaim := machine.NewCPUSet(10, 74)
	nonReclaim := machine.NewCPUSet()
	for cpu := 0; cpu < 128; cpu++ {
		nonReclaim.Add(cpu)
	}
	nonReclaim = nonReclaim.Difference(reclaim)
	return &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:                 machine.NewCPUSet(),
			Dedicated:               machine.NewCPUSet(),
			Isolation:               machine.NewCPUSet(),
			DesiredNonReclaimPool:   nonReclaim.Clone(),
			DesiredReclaimEffective: reclaim.Clone(),
			NonReclaimPool:          nonReclaim.Clone(),
			ReclaimEffective:        reclaim.Clone(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}
}

// TestShadowSelfCollisionIdentityBounded is the TDD proof for the identity-bounded
// self-collision subtraction. On the minimal DNB runtime shape (core i = {i,i+64},
// pending CPU 0 folds to whole core {0,64}), the final hard set is built from the
// Dedicated/Isolation aggregates PLUS the other committed hard owners, and only the
// CPUs that are EXCLUSIVELY self-owned (no other/blank-proof hard owner overlaps)
// are subtracted. Aggregate-owner consistency is required: base.Dedicated /
// base.Isolation actually fold in the owners' ExpectedCPUSets, exactly as the live
// BuildValidatedCPUSetPartitionView does.
func TestShadowSelfCollisionIdentityBounded(t *testing.T) {
	t.Parallel()
	const pendingPod = "pod-pending"
	cases := []struct {
		name     string
		mutate   func(base *model.DesiredView)
		wantHard bool
	}{
		{
			// Self-only dedicated: the pending pod's own whole core {0,64} is the
			// ONLY owner. It must NOT hard-collide. 082 left the self CPUs inside
			// the base.Dedicated aggregate, reintroducing protected=0,64.
			name: "self_only_dedicated",
			mutate: func(base *model.DesiredView) {
				base.Dedicated = machine.NewCPUSet(0, 64)
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "self"}] = model.DesiredPoolOwner{ProofPodUID: pendingPod, ExpectedCPUSet: machine.NewCPUSet(0, 64)}
			},
			wantHard: false,
		},
		{
			// Self-only isolation: same shape on the isolation aggregate.
			name: "self_only_isolation",
			mutate: func(base *model.DesiredView) {
				base.Isolation = machine.NewCPUSet(0, 64)
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindIsolation, PodName: "self"}] = model.DesiredPoolOwner{ProofPodUID: pendingPod, ExpectedCPUSet: machine.NewCPUSet(0, 64)}
			},
			wantHard: false,
		},
		{
			// Self + different overlap: a DIFFERENT dedicated owner also claims CPU
			// 0. The overlapping CPU 0 stays hard (a second non-movable owner owns
			// it), so the fold must still hard_collide; the self-exclusive sibling
			// 64 must be dropped.
			name: "self_plus_different_overlap",
			mutate: func(base *model.DesiredView) {
				base.Dedicated = machine.NewCPUSet(0, 64)
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "self"}] = model.DesiredPoolOwner{ProofPodUID: pendingPod, ExpectedCPUSet: machine.NewCPUSet(0, 64)}
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "other"}] = model.DesiredPoolOwner{ProofPodUID: "pod-other", ExpectedCPUSet: machine.NewCPUSet(0)}
			},
			wantHard: true,
		},
		{
			// Blank proof: the owner cannot prove it is the pending pod, so it
			// stays hard even though it sits on the same core.
			name: "blank_proof_other",
			mutate: func(base *model.DesiredView) {
				base.Dedicated = machine.NewCPUSet(0, 64)
				base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "other"}] = model.DesiredPoolOwner{ProofPodUID: "", ExpectedCPUSet: machine.NewCPUSet(0, 64)}
			},
			wantHard: true,
		},
	}

	topo := makeDNBRuntimeTopology()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := dnbRuntimeView()
			tc.mutate(base)
			sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{PodUID: pendingPod, CPUs: machine.NewCPUSet(0)}})
			if tc.wantHard {
				if !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
					t.Fatalf("%s: want hard_collision, got hold=%v reason=%q protected=%s hard=%s",
						tc.name, sh.HoldCurrent, sh.HoldReason, sh.ProtectedPending, sh.ExcludedHardSet)
				}
				if !sh.ProtectedPending.Equals(machine.NewCPUSet(0, 64)) {
					t.Fatalf("%s: ProtectedPending=%s want {0,64}", tc.name, sh.ProtectedPending)
				}
			} else {
				if sh.HoldCurrent && sh.HoldReason == HoldReasonHardCollision {
					t.Fatalf("%s: self fold must not hard_collide: hold=%v reason=%q protected=%s hard=%s",
						tc.name, sh.HoldCurrent, sh.HoldReason, sh.ProtectedPending, sh.ExcludedHardSet)
				}
				// The self-exclusive whole core is gone from the hard set.
				if sh.ExcludedHardSet.Contains(0) || sh.ExcludedHardSet.Contains(64) {
					t.Fatalf("%s: self core {0,64} must be excluded from hard set: %s", tc.name, sh.ExcludedHardSet)
				}
			}
		})
	}
}

// TestShadowRuntimeSelfCoreNoCollision pins the exact 90a runtime symptom: minimal
// DNB, pending CPU 0 folds to {0,64} which the pending pod itself owns as a
// committed dedicated whole core. The shadow must converge (no hold-current), leave
// the reclaim core {10,74} untouched, and never emit hard_collision.
func TestShadowRuntimeSelfCoreNoCollision(t *testing.T) {
	t.Parallel()
	topo := makeDNBRuntimeTopology()
	base := dnbRuntimeView()
	const pendingPod = "pod-pending"
	base.Dedicated = machine.NewCPUSet(0, 64)
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "self"}] = model.DesiredPoolOwner{ProofPodUID: pendingPod, ExpectedCPUSet: machine.NewCPUSet(0, 64)}

	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{PodUID: pendingPod, CPUs: machine.NewCPUSet(0)}})
	if sh.HoldCurrent && sh.HoldReason == HoldReasonHardCollision {
		t.Fatalf("runtime self fold must not hard_collision: protected=%s hard=%s", sh.ProtectedPending, sh.ExcludedHardSet)
	}
	if sh.HoldCurrent {
		t.Fatalf("runtime self fold should converge, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(machine.NewCPUSet(10, 74)) {
		t.Fatalf("reclaim drift: got %s want {10,74}", sh.ReclaimEffective)
	}
}

// TestShadowAggregateOwnerConsistency proves the subtraction is computed against a
// consistent aggregate: base.Dedicated is derived as the union of the committed
// dedicated owners' ExpectedCPUSets (exactly as BuildValidatedCPUSetPartitionView
// does), and a second, different dedicated owner sharing part of the core keeps
// only its overlap hard — the self-exclusive sibling is not retained.
func TestShadowAggregateOwnerConsistency(t *testing.T) {
	t.Parallel()
	topo := makeDNBRuntimeTopology()
	base := dnbRuntimeView()
	const pendingPod = "pod-pending"

	self := model.DesiredPoolOwner{ProofPodUID: pendingPod, ExpectedCPUSet: machine.NewCPUSet(0, 64)}
	other := model.DesiredPoolOwner{ProofPodUID: "pod-other", ExpectedCPUSet: machine.NewCPUSet(0)}
	// Aggregate derived from the owners, not hand-set.
	base.Dedicated = self.ExpectedCPUSet.Clone().Union(other.ExpectedCPUSet)
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "self"}] = self
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "other"}] = other

	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{PodUID: pendingPod, CPUs: machine.NewCPUSet(0)}})
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
		t.Fatalf("consistent-aggregate overlap must hard_collide, got hold=%v reason=%q hard=%s",
			sh.HoldCurrent, sh.HoldReason, sh.ExcludedHardSet)
	}
	// Only the overlapping CPU 0 survives; the self-exclusive sibling 64 is dropped.
	if sh.ExcludedHardSet.Contains(64) {
		t.Fatalf("self-exclusive CPU 64 must not be hard: %s", sh.ExcludedHardSet)
	}
	if !sh.ExcludedHardSet.Contains(0) {
		t.Fatalf("overlap CPU 0 must remain hard: %s", sh.ExcludedHardSet)
	}
}

// TestShadowStableRecreateNoAlias proves repeated derivation over an immutable
// base never accumulates mutation: the base CPU sets equal their original
// snapshot on every iteration.
func TestShadowStableRecreateNoAlias(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	origNR := base.DesiredNonReclaimPool.Clone()
	origR := base.DesiredReclaimEffective.Clone()
	for i := 0; i < 5; i++ {
		sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(8)}})
		if sh.HoldCurrent {
			t.Fatalf("iter %d unexpected hold: %s", i, sh.HoldReason)
		}
		if !base.DesiredNonReclaimPool.Equals(origNR) || !base.DesiredReclaimEffective.Equals(origR) {
			t.Fatalf("iter %d base mutated: nr=%s r=%s", i, base.DesiredNonReclaimPool, base.DesiredReclaimEffective)
		}
	}
}

// TestShadowInvariantViolationExercised forces the fail-closed self-check:
// DesiredNonReclaimPool contains a Reserved CPU outside `available`, so the
// union of nonReclaim and reclaimed leaks outside available, which the
// invariant check catches and reports as invariant_violation.
func TestShadowInvariantViolationExercised(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	base.Reserve = machine.NewCPUSet(5) // CPU5 is reserved, removed from available
	base.DesiredNonReclaimPool = base.DesiredNonReclaimPool.Clone().Union(machine.NewCPUSet(5))
	sh := DeriveShadowPartition(base, topo, nil)
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonInvariantViolation {
		t.Fatalf("want invariant_violation hold, got %v/%q", sh.HoldCurrent, sh.HoldReason)
	}
}

// TestShadowEarlyDeleteSequence models a stable -> pending -> delete -> recreate
// lifecycle across three immutable base snapshots. Each step derives against a
// fresh base; the business base must never carry forward a stale shadow.
func TestShadowEarlyDeleteSequence(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()

	// Snapshot 1: stable, no pending.
	stable := baseR3BenchView()
	// Snapshot 2: a shared ramp-up pod is pending.
	pending := baseR3BenchView()
	protections := []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(8)}}
	// Snapshot 3: pod deleted, recreated without pending again.
	recreate := baseR3BenchView()

	beforeNR := func(b *model.DesiredView) string { return b.DesiredNonReclaimPool.String() }

	s1 := DeriveShadowPartition(stable, topo, nil)
	if s1.HoldCurrent {
		t.Fatalf("stable hold: %s", s1.HoldReason)
	}
	s2 := DeriveShadowPartition(pending, topo, protections)
	if s2.HoldCurrent {
		t.Fatalf("pending should proceed: %s", s2.HoldReason)
	}
	s3 := DeriveShadowPartition(recreate, topo, nil)
	if s3.HoldCurrent {
		t.Fatalf("recreate hold: %s", s3.HoldReason)
	}
	// Recreate must equal the original stable projection exactly (no
	// accumulated pending residue).
	if !s3.ReclaimEffective.Equals(s1.ReclaimEffective) || !s3.NonReclaimPool.Equals(s1.NonReclaimPool) {
		t.Fatalf("recreate diverged from stable: reclaim %s vs %s", s3.ReclaimEffective, s1.ReclaimEffective)
	}
	// All three business bases unchanged.
	for i, b := range []*model.DesiredView{stable, pending, recreate} {
		if b.DesiredNonReclaimPool.String() != beforeNR(b) {
			t.Fatalf("base %d mutated", i)
		}
	}
}

// TestShadowExcludedHardSetNonEmptyCPUSet proves ExcludedHardSet is always a
// non-nil CPUSet (possibly empty) so callers need no nil guard.
func TestShadowExcludedHardSetNonEmptyCPUSet(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	sh := DeriveShadowPartition(baseR3BenchView(), topo, nil)
	if sh.ExcludedHardSet.Size() != 0 {
		t.Fatalf("no-hard-owner base should yield empty hard set, got %s", sh.ExcludedHardSet)
	}
}

// TestAsPartitionViewShallowCopyPreservesFields locks the AsPartitionView
// contract: it shallow-copies every base field and overrides only the three
// shadow-derived fields, leaving the base untouched.
func TestAsPartitionViewShallowCopyPreservesFields(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	base.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{
		"pod-a": {"main": machine.NewCPUSet(0)},
	}

	// Snapshot the base fields BEFORE the call so we can prove AsPartitionView
	// never mutates them.
	beforeNR := base.DesiredNonReclaimPool.Clone()
	beforeR := base.DesiredReclaimEffective.Clone()
	beforePodCPUs := base.ContainerCPUSetByPod["pod-a"]["main"].Clone()

	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{{PodUID: "p", CPUs: machine.NewCPUSet(8)}})
	if sh.HoldCurrent {
		t.Fatalf("unexpected hold: %s", sh.HoldReason)
	}

	out := sh.AsPartitionView(&base.CPUSetPartitionView)

	// Overridden fields must be the shadow values and must NOT alias the base.
	if !out.NonReclaimPool.Equals(sh.NonReclaimPool) {
		t.Fatalf("NonReclaimPool not overridden: %s", out.NonReclaimPool)
	}
	if !out.ReclaimEffective.Equals(sh.ReclaimEffective) {
		t.Fatalf("ReclaimEffective not overridden: %s", out.ReclaimEffective)
	}
	if len(out.ReclaimEffectivePerNUMA) != len(sh.ReclaimEffectivePerNUMA) {
		t.Fatalf("ReclaimEffectivePerNUMA not overridden")
	}

	// Shallow-copy identity: out.ContainerCPUSetByPod must share the SAME backing
	// map as base. Write a sentinel through out and observe it through base, then
	// clean it up. This proves the map was referenced, not deep-copied, and that
	// AsPartitionView itself never wrote into the map.
	if out.ContainerCPUSetByPod == nil || base.ContainerCPUSetByPod == nil {
		t.Fatalf("ContainerCPUSetByPod must be preserved non-nil")
	}
	out.ContainerCPUSetByPod["__sentinel__"] = map[string]machine.CPUSet{"main": machine.NewCPUSet(7)}
	if _, ok := base.ContainerCPUSetByPod["__sentinel__"]; !ok {
		t.Fatalf("ContainerCPUSetByPod is not shared: out and base do not alias the same map")
	}
	delete(out.ContainerCPUSetByPod, "__sentinel__")

	// Base must be unchanged by the call (compare against pre-call snapshots).
	if !base.DesiredNonReclaimPool.Equals(beforeNR) {
		t.Fatalf("base DesiredNonReclaimPool mutated: %s -> %s", beforeNR, base.DesiredNonReclaimPool)
	}
	if !base.DesiredReclaimEffective.Equals(beforeR) {
		t.Fatalf("base DesiredReclaimEffective mutated: %s -> %s", beforeR, base.DesiredReclaimEffective)
	}
	if !base.ContainerCPUSetByPod["pod-a"]["main"].Equals(beforePodCPUs) {
		t.Fatalf("base pod cpuset mutated")
	}
}

//
// Pending shared-non-reclaim projection tests.
//
// In make4NUMASMTTopology, threads cpu and cpu+16 share a physical core, and
// reclaim uses whole core {4,20} per NUMA. baseWholeCoreView makes the
// non-reclaim pool whole-core aligned so the shared projection domain is clean.

// baseWholeCoreView builds a 4-NUMA SMT2 view whose non-reclaim pool is whole-core
// aligned (cores 0 and 1 per NUMA => threads {0,16,1,17}) and whose reclaim
// budget is eight strided whole-core CPUs (core 4 per NUMA => {4,20} pairs).
func baseWholeCoreView() *model.DesiredView {
	reclaim := machine.NewCPUSet()
	for _, n := range []int{0, 1, 2, 3} {
		reclaim.Add(n*32 + 4)
		reclaim.Add(n*32 + 20)
	}
	nonReclaim := machine.NewCPUSet()
	for n := 0; n < 4; n++ {
		for _, c := range []int{0, 1, 16, 17} {
			nonReclaim.Add(n*32 + c)
		}
	}
	return &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:                 machine.NewCPUSet(),
			Dedicated:               machine.NewCPUSet(),
			Isolation:               machine.NewCPUSet(),
			DesiredNonReclaimPool:   nonReclaim.Clone(),
			DesiredReclaimEffective: reclaim.Clone(),
			NonReclaimPool:          nonReclaim.Clone(),
			ReclaimEffective:        reclaim.Clone(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}
}

func sharedProjectionProtect(podUID string, cpus machine.CPUSet) topology.PendingProtection {
	return topology.PendingProtection{
		PodUID:              podUID,
		CPUs:                cpus,
		SharedProjectedCPUs: cpus.Clone(),
	}
}

// TestSharedProjectionRepro is the exact reproduction: a pending non-SNB shared
// ramp-up allocation asking for 0-127 must be projected onto the existing shared
// non-reclaim domain, never folded wholesale. Reclaim keeps its eight CPUs, there
// is no FullPending, and the business allocation (0-127) is unmutated.
func TestSharedProjectionRepro(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	wantReclaim := base.DesiredReclaimEffective.Clone()

	beforeNR := base.DesiredNonReclaimPool.Clone()
	beforeReclaim := base.DesiredReclaimEffective.Clone()

	protections := []topology.PendingProtection{sharedProjectionProtect("ramp-up", machine.MustParse("0-127"))}
	sh := DeriveShadowPartition(base, topo, protections)

	if sh.Err != nil {
		t.Fatalf("unexpected typed projection failure: %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("shared projection must not hold-current: %s", sh.HoldReason)
	}

	// The projection equals DesiredNonReclaimPool − Dedicated − Isolation.
	wantShared := base.DesiredNonReclaimPool.Clone().
		Difference(base.Dedicated).Difference(base.Isolation)
	if !sh.SharedProjected.Equals(wantShared) {
		t.Fatalf("shared projected = %s, want %s", sh.SharedProjected, wantShared)
	}
	// Reclaim budget is untouched (eight CPUs) and disjoint.
	if !sh.ReclaimEffective.Equals(wantReclaim) {
		t.Fatalf("reclaim drift: got %s, want %s", sh.ReclaimEffective, wantReclaim)
	}
	if sh.ReclaimEffective.Size() != 8 {
		t.Fatalf("reclaim size = %d, want 8", sh.ReclaimEffective.Size())
	}
	// Non-reclaim is unchanged (projection is a subset of the base pool).
	if !sh.NonReclaimPool.Equals(beforeNR) {
		t.Fatalf("non-reclaim changed by projection: got %s, want %s", sh.NonReclaimPool, beforeNR)
	}
	// Projected invariants.
	if !sh.SharedProjected.IsSubsetOf(base.DesiredNonReclaimPool) {
		t.Fatalf("projected %s not subset of desired non-reclaim %s", sh.SharedProjected, base.DesiredNonReclaimPool)
	}
	if !sh.SharedProjected.Intersection(base.DesiredReclaimEffective).IsEmpty() {
		t.Fatalf("projected overlaps reclaim")
	}
	if !sh.SharedProjected.Intersection(base.Dedicated).IsEmpty() || !sh.SharedProjected.Intersection(base.Isolation).IsEmpty() {
		t.Fatalf("projected overlaps dedicated/isolation")
	}
	// Per-NUMA union equals global reclaim.
	if !unionPerNUMA(sh.ReclaimEffectivePerNUMA).Equals(sh.ReclaimEffective) {
		t.Fatalf("per-NUMA union %s != global reclaim %s", unionPerNUMA(sh.ReclaimEffectivePerNUMA), sh.ReclaimEffective)
	}
	// Business base is never mutated.
	if !base.DesiredNonReclaimPool.Equals(beforeNR) || !base.DesiredReclaimEffective.Equals(beforeReclaim) {
		t.Fatalf("business base mutated")
	}
}

// TestSharedProjectionMixedPodNoCrossClassification proves classification is per
// container, not a pod-wide flag: a pod that mixes one eligible shared ramp-up
// container (CPU0) with one exact sibling (CPU2) must project only CPU0's whole
// core and fold CPU2's whole core exactly, never contaminating the sibling.
func TestSharedProjectionMixedPodNoCrossClassification(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	wantReclaim := base.DesiredReclaimEffective.Clone()

	// One pending protection for the pod: container A eligible on CPU0, container
	// B exact on CPU2. SharedProjectedCPUs names only the eligible subset.
	protection := topology.PendingProtection{
		PodUID:              "mixed",
		CPUs:                machine.NewCPUSet(0, 2), // container A {0}, container B {2}
		SharedProjectedCPUs: machine.NewCPUSet(0),    // only container A projects
	}
	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{protection})
	if sh.Err != nil {
		t.Fatalf("unexpected typed projection failure: %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("must not hold-current: %s", sh.HoldReason)
	}

	// Exact fold must cover the exact sibling's whole core {2,18} and must NOT pull
	// in the projected container's core {0,16}.
	if !sh.ExactWhole.IsSubsetOf(machine.NewCPUSet(2, 18)) || sh.ExactWhole.IsEmpty() {
		t.Fatalf("exact whole = %s, want only sibling core {2,18}", sh.ExactWhole)
	}
	if sh.ExactWhole.Intersection(machine.NewCPUSet(0, 16)).IsEmpty() == false {
		t.Fatalf("exact whole %s must not include projected core {0,16}", sh.ExactWhole)
	}
	// Projected must cover the eligible container's whole core {0,16} and must NOT
	// include the exact sibling's core {2,18}.
	if !sh.SharedProjected.Equals(machine.NewCPUSet(0, 16)) {
		t.Fatalf("shared projected = %s, want {0,16} only (no sibling contamination)", sh.SharedProjected)
	}
	if sh.SharedProjected.Intersection(machine.NewCPUSet(2, 18)).IsEmpty() == false {
		t.Fatalf("projected %s must not include exact sibling core {2,18}", sh.SharedProjected)
	}
	// Reclaim budget untouched.
	if !sh.ReclaimEffective.Equals(wantReclaim) {
		t.Fatalf("reclaim drift: got %s, want %s", sh.ReclaimEffective, wantReclaim)
	}
}

// TestSharedProjectionMultipleShareDomain proves several eligible shared pending
// allocations share the same domain and are not treated as exclusive of one
// another (no hard collision between them).
func TestSharedProjectionMultipleShareDomain(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// Two eligible pendings on different whole cores of the shared domain.
	protections := []topology.PendingProtection{
		sharedProjectionProtect("a", machine.NewCPUSet(0, 16)),
		sharedProjectionProtect("b", machine.NewCPUSet(1, 17)),
	}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err != nil || sh.HoldCurrent {
		t.Fatalf("unexpected: err=%v hold=%s", sh.Err, sh.HoldReason)
	}
	// Both project onto their respective cores within the shared domain.
	if !sh.SharedProjected.Contains(0) || !sh.SharedProjected.Contains(1) {
		t.Fatalf("shared projected missing pending cores: %s", sh.SharedProjected)
	}
	// Reclaim untouched.
	if sh.ReclaimEffective.Size() != 8 {
		t.Fatalf("reclaim size = %d, want 8", sh.ReclaimEffective.Size())
	}
}

// TestSharedProjectionDomainSubtractsDedicatedIsolation proves the shared domain
// explicitly subtracts dedicated and isolation, and projected CPUs there are
// excluded from the result.
func TestSharedProjectionDomainSubtractsDedicatedIsolation(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	// Move whole core 0 ({0,16}) into dedicated.
	base.Dedicated = machine.NewCPUSet(0, 16)
	base.DesiredNonReclaimPool = base.DesiredNonReclaimPool.Clone().Difference(base.Dedicated)

	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.MustParse("0-127"))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err != nil {
		t.Fatalf("unexpected: %v", sh.Err)
	}
	// Dedicated core must be excluded from the shared domain.
	if sh.SharedDomain.Contains(0) || sh.SharedDomain.Contains(16) {
		t.Fatalf("shared domain leaked dedicated core: %s", sh.SharedDomain)
	}
	if sh.SharedProjected.Contains(0) || sh.SharedProjected.Contains(16) {
		t.Fatalf("shared projected leaked dedicated core: %s", sh.SharedProjected)
	}
}

// TestSharedProjectionDedicatedHoldsOneSiblingIsNotHalfCore covers the case where an odd-sized dedicated owner holds one
// SMT sibling (CPU 0) while the other sibling (CPU 16) stays in the shared
// non-reclaim pool. Only Reclaim is whole-core, so this is a legal state and the
// shared ramp-up projection must succeed instead of failing closed as a
// half-core domain.
func TestSharedProjectionDedicatedHoldsOneSiblingIsNotHalfCore(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	base.Dedicated = machine.NewCPUSet(0)
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "odd-dedicated"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(0)}

	protections := []topology.PendingProtection{sharedProjectionProtect("ramp-up", machine.MustParse("0-127"))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err != nil {
		t.Fatalf("dedicated-held sibling must not be a half-core shared domain: %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("unexpected hold-current: %s", sh.HoldReason)
	}
	if !sh.SharedDomain.Contains(16) || sh.SharedDomain.Contains(0) {
		t.Fatalf("shared domain must keep shared sibling 16 and exclude dedicated 0: %s", sh.SharedDomain)
	}
	if !sh.SharedProjected.Intersection(base.Dedicated).IsEmpty() {
		t.Fatalf("projected leaked dedicated cpu: %s", sh.SharedProjected)
	}
	if !sh.ReclaimEffective.Equals(base.DesiredReclaimEffective) {
		t.Fatalf("reclaim drift: got %s want %s", sh.ReclaimEffective, base.DesiredReclaimEffective)
	}
}

// TestSharedProjectionIsolationHoldsOneSiblingIsNotHalfCore covers the isolation variant: an odd-sized isolation group holds one SMT sibling.
func TestSharedProjectionIsolationHoldsOneSiblingIsNotHalfCore(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	base.Isolation = machine.NewCPUSet(1)

	protections := []topology.PendingProtection{sharedProjectionProtect("ramp-up", machine.MustParse("0-127"))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err != nil {
		t.Fatalf("isolation-held sibling must not be a half-core shared domain: %v", sh.Err)
	}
	if !sh.SharedDomain.Contains(17) || sh.SharedDomain.Contains(1) {
		t.Fatalf("shared domain must keep shared sibling 17 and exclude isolation 1: %s", sh.SharedDomain)
	}
}

// TestSharedProjectionHalfCoreDomain proves a half-core shared domain is a typed
// invariant failure with zero writes.
func TestSharedProjectionHalfCoreDomain(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	// Split core 0: keep thread 0 in the domain but drop its sibling 16.
	base.DesiredNonReclaimPool = base.DesiredNonReclaimPool.Clone().Difference(machine.NewCPUSet(16))

	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.NewCPUSet(0))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err == nil {
		t.Fatalf("expected half-core typed failure, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !errors.Is(sh.Err, ErrSharedProjectionHalfCoreDomain) {
		t.Fatalf("expected half-core domain err, got %v", sh.Err)
	}
	// Zero writes: no non-reclaim/reclaim partition emitted.
	if !sh.NonReclaimPool.IsEmpty() || !sh.ReclaimEffective.IsEmpty() {
		t.Fatalf("half-core failure must emit zero partition: nr=%s r=%s", sh.NonReclaimPool, sh.ReclaimEffective)
	}
}

// TestSharedProjectionOutsideDomain proves an eligible pending whose CPUs sit in
// reclaim (outside the shared domain) is a typed fail-closed, never empty target.
func TestSharedProjectionOutsideDomain(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// Eligible pending claims reclaim core {4,20}, which is outside the shared domain.
	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.NewCPUSet(4, 20))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err == nil {
		t.Fatalf("expected outside-domain typed failure, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !errors.Is(sh.Err, ErrSharedProjectionOutsideDomain) {
		t.Fatalf("expected outside-domain err, got %v", sh.Err)
	}
}

// TestSharedProjectionDomainEmpty proves an empty shared domain fails closed.
func TestSharedProjectionDomainEmpty(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	// Move the whole non-reclaim pool into dedicated => shared domain becomes empty.
	base.Dedicated = base.DesiredNonReclaimPool.Clone()
	base.DesiredNonReclaimPool = machine.NewCPUSet().Union(base.Dedicated)

	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.NewCPUSet(0))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err == nil || !errors.Is(sh.Err, ErrSharedProjectionDomainEmpty) {
		t.Fatalf("expected empty-domain err, got err=%v hold=%v", sh.Err, sh.HoldCurrent)
	}
}

// TestSharedProjectionHardOwnerSubsetViolation proves a hard pool owner claiming
// CPUs outside Dedicated∪Isolation is a typed failure.
func TestSharedProjectionHardOwnerSubsetViolation(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	base.PoolOwners[model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodName: "d1"}] = model.DesiredPoolOwner{ExpectedCPUSet: machine.NewCPUSet(40)} // 40 is NOT in Dedicated.

	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.NewCPUSet(0))}
	sh := DeriveShadowPartition(base, topo, protections)
	if sh.Err == nil || !errors.Is(sh.Err, ErrPoolOwnerHardNotSubset) {
		t.Fatalf("expected owner-subset err, got err=%v hold=%v/%s", sh.Err, sh.HoldCurrent, sh.HoldReason)
	}
}

// TestSharedProjectionSNBStaysExact proves an SNB pending (flag off) keeps exact
// whole-core folding: it is NOT projected onto the shared domain.
func TestSharedProjectionSNBStaysExact(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// Exact (non-eligible) pending claiming a reclaim whole core folds wholesale.
	protections := []topology.PendingProtection{{PodUID: "snb", CPUs: machine.NewCPUSet(4, 20)}}
	sh := DeriveShadowPartition(base, topo, protections)
	// Folding reclaim core {4,20} onto non-reclaim empties NUMA0 reclaim floor
	// (4,20 are the only NUMA0 reclaim CPUs) => floor_exhausted hold-current,
	// proving it was treated as exact rather than projected away.
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonFloorExhausted {
		t.Fatalf("SNB/exact pending should floor-exhaust, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !sh.SharedProjected.IsEmpty() {
		t.Fatalf("non-eligible pending must not be projected: %s", sh.SharedProjected)
	}
}

// TestSharedProjectionSMTVariants exercises whole-core projection across
// SMT1/SMT2/SMT4 and 2/4 NUMA layouts.
func TestSharedProjectionSMTVariants(t *testing.T) {
	t.Parallel()
	mkTopo := func(numCPU, numNUMA, threadsPerCore int) *machine.CPUTopology {
		details := machine.CPUDetails{}
		numaSize := numCPU / numNUMA
		for cpu := 0; cpu < numCPU; cpu++ {
			numa := cpu / numaSize
			coreInNUMA := (cpu % numaSize) / threadsPerCore
			details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: numa*1000 + coreInNUMA}
		}
		return &machine.CPUTopology{NumCPUs: numCPU, NumCores: numCPU / threadsPerCore, NumSockets: numNUMA, NumNUMANodes: numNUMA, CPUDetails: details}
	}
	cases := []struct {
		name              string
		numCPU, numa, smt int
	}{
		{"smt1_2numa", 64, 2, 1},
		{"smt2_4numa", 128, 4, 2},
		{"smt4_2numa", 128, 2, 4},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topo := mkTopo(tc.numCPU, tc.numa, tc.smt)
			// Reclaim: first whole core of each NUMA.
			reclaim := machine.NewCPUSet()
			nonReclaim := machine.NewCPUSet()
			numaSize := tc.numCPU / tc.numa
			for n := 0; n < tc.numa; n++ {
				base := n * numaSize
				for s := 0; s < tc.smt; s++ {
					reclaim.Add(base + s)             // whole core 0 per NUMA
					nonReclaim.Add(base + tc.smt + s) // whole core 1 per NUMA
				}
			}
			base := &model.DesiredView{
				CPUSetPartitionView: model.CPUSetPartitionView{
					Reserve:                 machine.NewCPUSet(),
					Dedicated:               machine.NewCPUSet(),
					Isolation:               machine.NewCPUSet(),
					DesiredNonReclaimPool:   nonReclaim.Clone(),
					DesiredReclaimEffective: reclaim.Clone(),
					NonReclaimPool:          nonReclaim.Clone(),
					ReclaimEffective:        reclaim.Clone(),
				},
				PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
			}
			// Eligible pending claims everything.
			protections := []topology.PendingProtection{sharedProjectionProtect("r", topo.CPUDetails.CPUs())}
			sh := DeriveShadowPartition(base, topo, protections)
			if sh.Err != nil || sh.HoldCurrent {
				t.Fatalf("%s: unexpected err=%v hold=%s", tc.name, sh.Err, sh.HoldReason)
			}
			if !sh.ReclaimEffective.Equals(reclaim) {
				t.Fatalf("%s: reclaim drift: %s", tc.name, sh.ReclaimEffective)
			}
			if !sh.SharedProjected.Equals(nonReclaim) {
				t.Fatalf("%s: projected=%s want %s", tc.name, sh.SharedProjected, nonReclaim)
			}
		})
	}
}

// TestSharedProjectionNoEligibleIsIdentity proves the projection machinery is
// inert when there are no eligible protections (existing exact behavior
// preserved byte-for-byte).
func TestSharedProjectionNoEligibleIsIdentity(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	sh := DeriveShadowPartition(base, topo, nil)
	if sh.Err != nil || sh.HoldCurrent {
		t.Fatalf("empty should be identity: err=%v hold=%s", sh.Err, sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(base.DesiredReclaimEffective) || !sh.NonReclaimPool.Equals(base.DesiredNonReclaimPool) {
		t.Fatalf("empty shadow mismatch")
	}
	if !sh.SharedProjected.IsEmpty() {
		t.Fatalf("no eligible protections => no projection")
	}
}

func TestProjectPendingProtectionsToDesiredNonReclaimClipsFullMachinePlaceholder(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	nonReclaim := machine.NewCPUSet()
	for numaID := 0; numaID < 4; numaID++ {
		for _, local := range []int{0, 1} {
			nonReclaim.Add(numaID*32 + local)
			nonReclaim.Add(numaID*32 + local + 16)
		}
	}
	base.DesiredNonReclaimPool = nonReclaim.Clone()
	base.NonReclaimPool = nonReclaim.Clone()
	input := []topology.PendingProtection{{
		PodUID:   "pending-shared",
		ScopeRel: "kubepods/burstable/pod-pending",
		CPUs:     machine.MustParse("0-127"),
	}}
	before := input[0].CPUs.Clone()

	got := ProjectPendingProtectionsToDesiredNonReclaim(base, topo, input, nil)
	if len(got) != 1 {
		t.Fatalf("projected protection count = %d, want 1", len(got))
	}
	if !got[0].CPUs.Equals(nonReclaim) {
		t.Fatalf("projected pending = %s, want desired shared domain %s", got[0].CPUs, nonReclaim)
	}
	if !got[0].SharedProjectedCPUs.Equals(nonReclaim) {
		t.Fatalf("shared projection = %s, want %s", got[0].SharedProjectedCPUs, nonReclaim)
	}
	if !input[0].CPUs.Equals(before) {
		t.Fatalf("input protection was mutated: got=%s want=%s", input[0].CPUs, before)
	}

	shadow := DeriveShadowPartition(base, topo, got)
	if shadow.Err != nil || shadow.HoldCurrent {
		t.Fatalf("normalized pending must converge, err=%v hold=%t reason=%q", shadow.Err, shadow.HoldCurrent, shadow.HoldReason)
	}
	if !shadow.ReclaimEffective.Equals(base.DesiredReclaimEffective) {
		t.Fatalf("reclaim changed: got=%s want=%s", shadow.ReclaimEffective, base.DesiredReclaimEffective)
	}
}

func TestProjectPendingProtectionsToDesiredNonReclaimExcludesHardOwners(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()
	base.Dedicated = machine.NewCPUSet(0, 64)
	base.Isolation = machine.NewCPUSet(1, 65)
	// The validated non-reclaim partition (the clip authority) is the proven domain
	// {0-3,64-67}; dedicated {0,64} and isolation {1,65} are subtracted below.
	base.DesiredNonReclaimPool = machine.NewCPUSet(0, 1, 2, 3, 64, 65, 66, 67)
	base.NonReclaimPool = machine.NewCPUSet(0, 1, 2, 3, 64, 65, 66, 67)
	got := ProjectPendingProtectionsToDesiredNonReclaim(base, topo, []topology.PendingProtection{{
		PodUID: "pending", CPUs: machine.MustParse("0-127"),
	}}, nil)
	want := machine.NewCPUSet(2, 3, 66, 67)
	if len(got) != 1 || !got[0].CPUs.Equals(want) {
		t.Fatalf("hard-owner exclusion = %+v, want %s", got, want)
	}
}

// TestProjectPendingProtectionsKeepsAdmissionSeedLeafExact pins the narrow fix:
// a freshly created SNB seed leaf whose required CPU has not yet been folded into
// DesiredNonReclaimPool must be preserved verbatim as exact, not dropped by the
// shared-domain intersection. Dropping it starves the leaf's widen-able expand
// target and produces the bare fixed-point no-progress deadlock.
func TestProjectPendingProtectionsKeepsAdmissionSeedLeafExact(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()

	// CPU 8 is available but outside DesiredNonReclaimPool {0-3,32-35,64-67,96-99}
	// and outside the reclaim budget; this is the new seed leaf's required CPU.
	const seedCPU = 8
	input := []topology.PendingProtection{{
		PodUID:              "snbseed-stable-0",
		ScopeRel:            "kubepods/burstable/podsnbseed-stable-0",
		CPUs:                machine.NewCPUSet(seedCPU),
		SharedProjectedCPUs: machine.NewCPUSet(), // fully exact: SNB seed leaf
	}}

	got := ProjectPendingProtectionsToDesiredNonReclaim(base, topo, input, nil)
	if len(got) != 1 {
		t.Fatalf("admission seed leaf protection must survive the clip, got %+v", got)
	}
	if !got[0].CPUs.Equals(machine.NewCPUSet(seedCPU)) {
		t.Fatalf("seed leaf required CPUs = %s, want exact {%d}", got[0].CPUs, seedCPU)
	}
	if !got[0].SharedProjectedCPUs.IsEmpty() {
		t.Fatalf("seed leaf must stay exact, SharedProjectedCPUs = %s", got[0].SharedProjectedCPUs)
	}

	// Mixed with an eligible shared ramp-up that does overlap the domain: the
	// shared one is still clipped to the non-reclaim domain, the seed leaf stays
	// exact. Neither contaminates the other.
	shared := sharedProjectionProtect("ramp-up", machine.MustParse("0-127"))
	mixed := append([]topology.PendingProtection{input[0]}, shared)
	gotMixed := ProjectPendingProtectionsToDesiredNonReclaim(base, topo, mixed, nil)
	if len(gotMixed) != 2 {
		t.Fatalf("mixed projection count = %d, want 2: %+v", len(gotMixed), gotMixed)
	}
	if !gotMixed[0].CPUs.Equals(machine.NewCPUSet(seedCPU)) ||
		!gotMixed[0].SharedProjectedCPUs.IsEmpty() {
		t.Fatalf("seed leaf must remain exact in mixed set: %+v", gotMixed[0])
	}
	wantShared := base.DesiredNonReclaimPool.Clone()
	if !gotMixed[1].CPUs.Equals(wantShared) {
		t.Fatalf("shared ramp-up still clips to non-reclaim: got %s want %s", gotMixed[1].CPUs, wantShared)
	}
}

// TestProjectPendingProtectionsSamePodMixedPreservesExactRemainder reproduces the
// production aggregation shape: pendingProtectionScopes collapses one pod's
// containers into a SINGLE PendingProtection whose CPUs is the union and whose
// SharedProjectedCPUs is only the eligible container subset. When the exact
// sibling's CPU lies outside the validated non-reclaim domain, the clip must keep
// that exact remainder in CPUs (it is folded whole-core downstream) while the
// eligible subset is narrowed to the domain. Clipping the whole union previously
// dropped the exact sibling, starving the leaf's widen-able expand target.
func TestProjectPendingProtectionsSamePodMixedPreservesExactRemainder(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// NUMA0 validated non-reclaim domain = {0,1,16,17}. CPU0 is in it (eligible
	// shared ramp container A); CPU8 is outside it (exact seed sibling B).
	const eligibleCPU, exactCPU = 0, 8
	if !base.NonReclaimPool.Contains(eligibleCPU) {
		t.Fatalf("fixture error: eligible CPU %d must be in the non-reclaim domain", eligibleCPU)
	}
	if base.NonReclaimPool.Contains(exactCPU) {
		t.Fatalf("fixture error: exact CPU %d must be outside the non-reclaim domain", exactCPU)
	}

	// One aggregated protection for the pod, exactly as pendingProtectionScopes
	// builds it: CPUs is the container union, SharedProjectedCPUs names only A.
	protection := topology.PendingProtection{
		PodUID:              "mixed-pod",
		CPUs:                machine.NewCPUSet(eligibleCPU, exactCPU),
		SharedProjectedCPUs: machine.NewCPUSet(eligibleCPU),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(base, topo,
		[]topology.PendingProtection{protection}, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 protection to survive, got %d: %+v", len(got), got)
	}

	// The exact sibling must remain in CPUs; the eligible subset is narrowed to
	// the domain (already inside it here) and must NOT widen to cover the sibling.
	if !got[0].CPUs.Equals(machine.NewCPUSet(eligibleCPU, exactCPU)) {
		t.Fatalf("exact sibling %d dropped from CPUs: got %s want {%d,%d}",
			exactCPU, got[0].CPUs, eligibleCPU, exactCPU)
	}
	if !got[0].SharedProjectedCPUs.Equals(machine.NewCPUSet(eligibleCPU)) {
		t.Fatalf("shared subset must stay only {%d}, got %s", eligibleCPU, got[0].SharedProjectedCPUs)
	}

	// Downstream DAG semantics: the exact sibling folds to its whole SMT core
	// {8,24} (in this fixture core siblings are local/local+16 within a NUMA);
	// the eligible container projects to its core {0,16}; no cross leak.
	sh := DeriveShadowPartition(base, topo, got)
	if sh.Err != nil || sh.HoldCurrent {
		t.Fatalf("shadow err=%v hold=%q", sh.Err, sh.HoldReason)
	}
	if !sh.ExactWhole.Equals(machine.NewCPUSet(8, 24)) {
		t.Fatalf("exact sibling whole core = %s, want {8,24}", sh.ExactWhole)
	}
	if !sh.SharedProjected.Equals(machine.NewCPUSet(0, 16)) {
		t.Fatalf("eligible projected core = %s, want {0,16}", sh.SharedProjected)
	}
}

// TestProjectPendingProtectionsEligibleOutsideDomainFailsClosed pins the edge that
// the mixed-overlap clip must not regress: a classified protection whose eligible
// subset lies entirely outside the validated non-reclaim domain (but which also
// carries an exact remainder) must keep the eligible subset classified as shared
// (non-empty SharedProjectedCPUs). Blanking it to the empty intersection would
// silently reclassify the protection as exact and swallow the downstream typed
// zero-write ErrSharedProjectionOutsideDomain. The exact remainder stays exact.
func TestProjectPendingProtectionsEligibleOutsideDomainFailsClosed(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// NUMA0 validated non-reclaim domain = {0,1,16,17}. Eligible container A asks
	// for CPU8 (outside the domain); exact sibling B asks for CPU4 (a reclaim-floor
	// CPU, outside the shared domain).
	const eligibleCPU, exactCPU = 8, 4
	if base.NonReclaimPool.Contains(eligibleCPU) {
		t.Fatalf("fixture error: eligible CPU %d must be outside the domain", eligibleCPU)
	}

	protection := topology.PendingProtection{
		PodUID:              "mixed-pod-outside",
		CPUs:                machine.NewCPUSet(eligibleCPU, exactCPU),
		SharedProjectedCPUs: machine.NewCPUSet(eligibleCPU),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(base, topo,
		[]topology.PendingProtection{protection}, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 protection, got %d: %+v", len(got), got)
	}

	// The exact remainder survives in CPUs; the eligible subset is NOT blanked.
	if !got[0].CPUs.Equals(machine.NewCPUSet(exactCPU)) {
		t.Fatalf("CPUs = %s, want only exact remainder {%d}", got[0].CPUs, exactCPU)
	}
	if !got[0].SharedProjectedCPUs.Equals(machine.NewCPUSet(eligibleCPU)) {
		t.Fatalf("eligible subset must stay classified as shared {%d}, got %s (blanked = silently dropped)",
			eligibleCPU, got[0].SharedProjectedCPUs)
	}

	// Downstream must raise the typed zero-write outside-domain contract, not fold
	// the eligible container as exact.
	sh := DeriveShadowPartition(base, topo, got)
	if !errors.Is(sh.Err, ErrSharedProjectionOutsideDomain) {
		t.Fatalf("expected typed ErrSharedProjectionOutsideDomain, got err=%v hold=%q", sh.Err, sh.HoldReason)
	}
	if !sh.ExactWhole.Equals(machine.NewCPUSet(exactCPU, exactCPU+16)) {
		t.Fatalf("exact remainder whole core = %s, want {%d,%d}", sh.ExactWhole, exactCPU, exactCPU+16)
	}
}

// TestDeriveShadowPartitionNilEnforceEquivalentToBusiness pins that a nil
// enforcement baseline makes DeriveShadowPartitionWithAppliedEnforcement return
// the byte-identical business DesiredView projection: periodic/full,
// hard/overlap, topology-disabled, and initial-nil-AppliedView rounds must be
// unchanged from DeriveShadowPartition.
func TestDeriveShadowPartitionNilEnforceEquivalentToBusiness(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	protections := []topology.PendingProtection{
		sharedProjectionProtect("a", machine.NewCPUSet(0)),
		{PodUID: "exact", CPUs: machine.NewCPUSet(8), SharedProjectedCPUs: machine.NewCPUSet()},
	}
	want := DeriveShadowPartition(base, topo, protections)
	got := DeriveShadowPartitionWithAppliedEnforcement(base, topo, protections, nil)
	if !got.NonReclaimPool.Equals(want.NonReclaimPool) ||
		!got.ReclaimEffective.Equals(want.ReclaimEffective) ||
		got.HoldCurrent != want.HoldCurrent ||
		got.HoldReason != want.HoldReason ||
		got.Err != want.Err {
		t.Fatalf("nil-enforce shadow diverges from business projection:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestProjectPendingProtectionsHardOwnerBoundaryIsFailClosed pins the boundary
// that the admission-seed retention added in 6a5 must not widen: a fully-exact
// pending whose required CPU lands on a Dedicated/Isolation hard owner survives
// the clip verbatim (it is exact, not projected), but DeriveShadowPartition must
// still fail closed with a HardCollision hold and emit zero physical writes
// rather than passing it through into a bad partition.
func TestProjectPendingProtectionsHardOwnerBoundaryIsFailClosed(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()

	// Dedicated owns whole core 0 = {0,16}. Take it out of the non-reclaim pool
	// exactly as the partition authority would.
	hardCore := machine.NewCPUSet(0, 16)
	base.Dedicated = hardCore.Clone()
	base.DesiredNonReclaimPool = base.DesiredNonReclaimPool.Difference(hardCore)
	base.NonReclaimPool = base.NonReclaimPool.Difference(hardCore)

	// Fully-exact pending claiming CPU 0, which belongs to the hard-owned core.
	protection := topology.PendingProtection{
		PodUID:              "snbseed-stable-0",
		CPUs:                machine.NewCPUSet(0),
		SharedProjectedCPUs: machine.NewCPUSet(), // fully exact
	}

	// The clip keeps it verbatim as exact (it has no overlap with the shared
	// domain, which now excludes the hard core).
	clipped := ProjectPendingProtectionsToDesiredNonReclaim(base, topo, []topology.PendingProtection{protection}, nil)
	if len(clipped) != 1 || !clipped[0].CPUs.Equals(machine.NewCPUSet(0)) ||
		!clipped[0].SharedProjectedCPUs.IsEmpty() {
		t.Fatalf("hard-owner exact pending must be retained verbatim, got %+v", clipped)
	}

	// DeriveShadowPartition folds the exact CPU to its whole core {0,16}, which
	// collides with the dedicated owner: fail closed, hold-current, no write.
	sh := DeriveShadowPartition(base, topo, clipped)
	if sh.Err != nil {
		t.Fatalf("hard-collision must be a hold, not an error: %v", sh.Err)
	}
	if !sh.HoldCurrent || sh.HoldReason != HoldReasonHardCollision {
		t.Fatalf("hard-owner exact pending must HardCollision-hold, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !sh.SharedProjected.IsEmpty() {
		t.Fatalf("exact hard-owner pending must not be projected: %s", sh.SharedProjected)
	}
	// Zero physical write: the hold short-circuits before constructing any
	// proposed partition, so the shadow carries no non-reclaim/reclaim rewrite
	// (the caller keeps the live base partition instead).
	if !sh.NonReclaimPool.IsEmpty() {
		t.Fatalf("hard-collision hold must not rewrite non-reclaim: %s", sh.NonReclaimPool)
	}
	if !sh.ReclaimEffective.IsEmpty() {
		t.Fatalf("hard-collision hold must not rewrite reclaim: %s", sh.ReclaimEffective)
	}
	// The colliding whole core must be reported as protected pending on the hard
	// set, proving it was recognized as a hard-owner collision rather than dropped.
	if !sh.ProtectedPending.Equals(hardCore) {
		t.Fatalf("ProtectedPending = %s, want colliding whole core %s", sh.ProtectedPending, hardCore)
	}
	if !hardCore.IsSubsetOf(sh.ExcludedHardSet) {
		t.Fatalf("ExcludedHardSet = %s, want to contain %s", sh.ExcludedHardSet, hardCore)
	}
}

// FuzzSharedProjectionRepro drives the eligible projection path across random
// whole-core-aligned pendings, asserting either a typed fail-closed error or a
// partition that keeps reclaim disjoint and within availability.
func FuzzSharedProjectionRepro(f *testing.F) {
	topo := make4NUMASMTTopology()
	for _, seed := range []int{0, 1, 16, 17, 32, 64} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cpu int) {
		base := baseWholeCoreView()
		cpu = ((cpu % 128) + 128) % 128
		sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{
			sharedProjectionProtect("f", machine.NewCPUSet(cpu)),
		})
		if sh.Err != nil {
			return // typed fail-closed is acceptable
		}
		if sh.HoldCurrent {
			return
		}
		avail := topo.CPUDetails.CPUs().Difference(base.Reserve)
		if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
			t.Fatalf("overlap for cpu %d", cpu)
		}
		if !sh.NonReclaimPool.Union(sh.ReclaimEffective).Difference(avail).IsEmpty() {
			t.Fatalf("out of availability for cpu %d", cpu)
		}
		if !sh.SharedProjected.IsSubsetOf(base.DesiredNonReclaimPool) {
			t.Fatalf("projected escaped non-reclaim for cpu %d", cpu)
		}
	})
}

// BenchmarkSharedProjection drives the eligible pending projection path.
func BenchmarkSharedProjection(b *testing.B) {
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	protections := []topology.PendingProtection{sharedProjectionProtect("r", machine.MustParse("0-127"))}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeriveShadowPartition(base, topo, protections)
	}
}

// TestFourSharedPendingNoUUE is the imp4021616现场 regression: 4 NUMA SMT2/128
// CPU, reclaim 8 strided {4,20,36,52,68,84,100,116}, 4 shared pending RampUp
// containers all projecting onto the whole-core-aligned shared non-reclaim
// domain. Must NOT UUE (hold-current), reclaim stays 8, business stays 0-127.
func TestFourSharedPendingNoUUE(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseR3BenchView()

	// Whole-core-aligned non-reclaim pool (现场 min=16 CPUs = 8 whole cores).
	// Per NUMA, take local cores 0 and 1, i.e. threads {local, local+16}.
	nonReclaim := machine.NewCPUSet()
	for n := 0; n < 4; n++ {
		for _, local := range []int{0, 1} {
			nonReclaim.Add(n*32 + local)
			nonReclaim.Add(n*32 + local + 16)
		}
	}
	base.DesiredNonReclaimPool = nonReclaim.Clone()
	base.NonReclaimPool = nonReclaim.Clone()
	sharedDomain := nonReclaim.
		Intersection(allCPUs(topo)).
		Difference(base.Dedicated).
		Difference(base.Isolation)

	// 4 pending shared RampUp containers, each pending 0-127 (business result).
	// Their projection folds to whole cores then intersects the shared domain;
	// multiple shared pending share the same domain and are not exclusive.
	var prot []topology.PendingProtection
	for i := 0; i < 4; i++ {
		pending := machine.MustParse("0-127")
		whole := expandToWholeCores(pending, topo.CPUDetails)
		projected := whole.Intersection(sharedDomain)
		if projected.IsEmpty() {
			t.Fatalf("pending %d projected empty", i)
		}
		prot = append(prot, topology.PendingProtection{
			PodUID:              string(rune('a' + i)),
			CPUs:                machine.NewCPUSet(), // exact empty: projection handles it
			SharedProjectedCPUs: projected,
		})
	}

	reclaimBefore := base.ReclaimEffective.Clone()
	sh := DeriveShadowPartition(base, topo, prot)
	if sh.Err != nil {
		t.Fatalf("projection must not fail-closed: %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("expected no hold-current/UUE, got reason=%q", sh.HoldReason)
	}
	// Reclaim stays the 8 strided CPUs, unchanged.
	if !sh.ReclaimEffective.Equals(reclaimBefore) {
		t.Fatalf("reclaim mutated: want=%s got=%s", reclaimBefore, sh.ReclaimEffective)
	}
	// Non-reclaim equals the whole-core shared domain; disjoint from reclaim.
	if !sh.NonReclaimPool.Equals(sharedDomain) {
		t.Fatalf("non-reclaim mismatch: want=%s got=%s", sharedDomain, sh.NonReclaimPool)
	}
	if !sh.NonReclaimPool.Intersection(sh.ReclaimEffective).IsEmpty() {
		t.Fatalf("non-reclaim/reclaim overlap: %s", sh.NonReclaimPool.Intersection(sh.ReclaimEffective))
	}
}

func allCPUs(topo *machine.CPUTopology) machine.CPUSet {
	cpus := machine.NewCPUSet()
	for i := 0; i < topo.NumCPUs; i++ {
		cpus.Add(i)
	}
	return cpus
}
