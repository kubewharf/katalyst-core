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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Hold-current reasons for a topology-only shadow partition. They decide
// whether the admission round must short-circuit with zero writes instead of
// feeding an (often empty) reclaim target to the v1 planner.
const (
	// HoldReasonFullPending the pending set absorbs the whole reclaim budget,
	// so the normalized reclaim target would be empty on a non-empty current
	// reclaim cgroup. Hold-current (bounded replay) avoids the v1 empty guard.
	HoldReasonFullPending = "full_pending"
	// HoldReasonHardCollision pending overlaps a committed non-movable owner
	// (dedicated/isolation). Must not displace; hold-current.
	HoldReasonHardCollision = "hard_collision"
	// HoldReasonFloorExhausted after folding pending whole-cores, at least one
	// physical NUMA that carried a reclaim floor now has none. Building an empty
	// controlled NUMA bucket would trip the v1 guard; hold-current instead.
	HoldReasonFloorExhausted = "floor_exhausted"
	// HoldReasonInvariantViolation the derived partition failed its fail-closed
	// self-check (non-disjoint, out of availability, or per-NUMA projection
	// mismatch). Hold-current rather than emit an unsound plan.
	HoldReasonInvariantViolation = "invariant_violation"
)

// ErrShadowHoldCurrent is the typed sentinel returned by the plugin when a
// shadow partition decides to hold-current. Upper layers classify it via
// errors.Is into a bounded, zero-physical-write replan rather than a terminal
// admission rejection.
var ErrShadowHoldCurrent = errors.New("bulkhead topology shadow hold-current")

// Typed, fail-closed contradictions for the pending shared-non-reclaim
// projection. They are NOT bounded hold-current (Deferred) replays: when
// DeriveShadowPartition returns one on ShadowPartition.Err the caller must
// short-circuit with zero physical writes and surface the error rather than emit
// an empty/unsound reclaim target.
var (
	// ErrSharedProjectionHalfCoreDomain the shared non-reclaim domain splits a
	// physical core between primary and reclaim, so there is no whole-core domain
	// onto which an eligible pending allocation can be projected.
	ErrSharedProjectionHalfCoreDomain = errors.New("bulkhead shared projection domain is not whole-core clean")
	// ErrSharedProjectionDomainEmpty there is no shared non-reclaim domain to
	// project an eligible pending allocation onto.
	ErrSharedProjectionDomainEmpty = errors.New("bulkhead shared projection domain is empty")
	// ErrSharedProjectionOutsideDomain an eligible pending allocation projects to
	// nothing inside the shared non-reclaim domain (it lies entirely in reclaim
	// or outside availability), which contradicts the share-pool placement.
	ErrSharedProjectionOutsideDomain = errors.New("bulkhead eligible pending projects outside the shared non-reclaim domain")
	// ErrSharedProjectionTopologyPartial an eligible pending allocation references
	// a logical CPU absent from the machine topology, so the projection cannot be
	// trusted.
	ErrSharedProjectionTopologyPartial = errors.New("bulkhead eligible pending references a CPU outside the machine topology")
	// ErrSharedProjectionHardOverlap the projected set collides with a committed
	// hard owner (dedicated/isolation) or with the desired reclaim budget.
	ErrSharedProjectionHardOverlap = errors.New("bulkhead shared projection overlaps hard owners or reclaim")
	// ErrPoolOwnerHardNotSubset a hard pool owner's expected CPUSet is not a
	// subset of Dedicated ∪ Isolation.
	ErrPoolOwnerHardNotSubset = errors.New("bulkhead hard pool owner expected set is not a subset of dedicated∪isolation")
)

// ShadowPartition is a read-only, planning-time partition derived purely from
// (base DesiredView, machine topology, pending protections). It replaces the
// historical late mutation of the business DesiredView
// (ApplyTransientProtectedNonReclaim) followed by a second spec/DAG rebuild.
//
// The pending protection set is folded onto the non-reclaim side BEFORE the
// per-NUMA reclaim floor is projected, so non-reclaim and reclaim are derived
// as complementary sets in one step. The business *model.DesiredView is never
// mutated; callers consume AsPartitionView for spec construction.
type ShadowPartition struct {
	// Complementary pair: NonReclaimPool and ReclaimEffective are disjoint and
	// together are a subset of available (machine minus reserve).
	NonReclaimPool          machine.CPUSet
	ReclaimEffective        machine.CPUSet
	ReclaimEffectivePerNUMA map[int]machine.CPUSet

	// Diagnostic / audit fields (never written to cgroup or business state).
	ProtectedPending machine.CPUSet // pending folded onto whole physical cores.
	ExcludedHardSet  machine.CPUSet // dedicated/isolation/committed owners.
	HoldCurrent      bool           // true => admission must short-circuit, zero writes.
	HoldReason       string

	// Err is a typed fail-closed projection contradiction. When non-nil the
	// partition is intentionally unsound: the caller must stop with zero physical
	// writes and must NOT treat the round as a bounded Deferred replay. It carries
	// the projection diagnostics below.
	Err error
	// SharedDomain is the whole-core clean shared non-reclaim domain
	// (DesiredNonReclaimPool − Dedicated − Isolation, clipped to availability).
	SharedDomain machine.CPUSet
	// SharedProjected is the union of eligible pending whole-cores intersected
	// with SharedDomain. It is always a subset of DesiredNonReclaimPool, so it
	// never changes the reclaim partition.
	SharedProjected machine.CPUSet
	// ExactWhole is the union of non-eligible (exact) pending whole-cores folded
	// onto the non-reclaim side.
	ExactWhole machine.CPUSet
}

// hardOwnerCPUSets partitions committed, non-movable pool owners (dedicated and
// isolation) into two disjoint views of their ExpectedCPUSets. Share owners are
// movable and are excluded from both: folding pending must not be blocked by a
// share floor that the normal planner can rebalance.
//
//   - selfOwned  : a Dedicated/Isolation owner whose non-empty ProofPodUID matches a
//     pending protection PodUID. That owner is the pending pod re-affirming its own
//     committed allocation, not a collision with a different non-movable owner.
//   - otherOwned : every other hard owner — a blank ProofPodUID (cannot prove self)
//     or a ProofPodUID naming a different pod. These stay hard and force a
//     hold-current.
//
// The caller combines them as (Dedicated ∪ Isolation ∪ otherOwned) minus the
// self-exclusive subset (selfOwned − otherOwned). Subtracting only the
// self-exclusive subset — and never selfOwned wholesale — is what keeps a CPU that
// the self owner shares with another non-movable owner hard.
func hardOwnerCPUSets(base *model.DesiredView, pendingPodUIDs map[string]struct{}) (selfOwned, otherOwned machine.CPUSet) {
	selfOwned = machine.NewCPUSet()
	otherOwned = machine.NewCPUSet()
	if base == nil {
		return selfOwned, otherOwned
	}
	for id, owner := range base.PoolOwners {
		if id.Kind != model.CPUSetPoolKindDedicated && id.Kind != model.CPUSetPoolKindIsolation {
			continue
		}
		if owner.ProofPodUID != "" {
			if _, self := pendingPodUIDs[owner.ProofPodUID]; self {
				selfOwned = selfOwned.Union(owner.ExpectedCPUSet)
				continue
			}
		}
		otherOwned = otherOwned.Union(owner.ExpectedCPUSet)
	}
	return selfOwned, otherOwned
}

// expandToWholeCores folds a logical-CPU set onto every thread sharing each
// touched physical core. On SMT-off nodes this is the identity. It guarantees
// the pending set never splits a physical core between primary and reclaim.
func expandToWholeCores(p machine.CPUSet, details machine.CPUDetails) machine.CPUSet {
	cores := machine.NewCPUSet()
	for _, cpu := range p.ToSliceInt() {
		if info, ok := details[cpu]; ok {
			cores.Add(info.CoreID)
		}
	}
	out := machine.NewCPUSet()
	for _, core := range cores.ToSliceInt() {
		out = out.Union(details.CPUsInCores(core))
	}
	return out
}

// sharedWholeCoreDomain validates that `domain` splits no physical core among
// the available CPUs: for every physical core, its available threads are either
// all included in the domain or all excluded. Reserved (non-available) threads
// are ignored, so a reserved sibling does not make an otherwise whole-core
// domain "half". A half-core domain is a typed invariant failure with zero
// writes. It never expands the domain, so reclaim siblings are not reintroduced.
func sharedWholeCoreDomain(domain, available machine.CPUSet, details machine.CPUDetails) (machine.CPUSet, error) {
	threadsByCore := make(map[int]machine.CPUSet)
	for _, cpu := range available.ToSliceInt() {
		info, ok := details[cpu]
		if !ok {
			continue
		}
		set := threadsByCore[info.CoreID]
		if !set.Initialed {
			set = machine.NewCPUSet()
		}
		set = set.Union(machine.NewCPUSet(cpu))
		threadsByCore[info.CoreID] = set
	}
	for core, threads := range threadsByCore {
		inDomain := threads.Intersection(domain)
		if inDomain.IsEmpty() || inDomain.Equals(threads) {
			continue
		}
		return machine.NewCPUSet(), fmt.Errorf("%w: core=%d threads=%s in_domain=%s",
			ErrSharedProjectionHalfCoreDomain, core, threads.String(), inDomain.String())
	}
	return domain, nil
}

// validateHardOwnerSubset proves every committed dedicated/isolation pool owner's
// expected CPUSet is a subset of Dedicated ∪ Isolation. A contradiction is a
// typed, zero-write failure. When the contract happens to prove equality
// (single owner == the whole Dedicated/Isolation set) this still holds; equality
// itself is not tightened beyond subset here.
func validateHardOwnerSubset(base *model.DesiredView) error {
	hardOwners := base.Dedicated.Clone().Union(base.Isolation)
	for id, owner := range base.PoolOwners {
		if id.Kind != model.CPUSetPoolKindDedicated && id.Kind != model.CPUSetPoolKindIsolation {
			continue
		}
		if owner.ExpectedCPUSet.IsEmpty() {
			continue
		}
		if outside := owner.ExpectedCPUSet.Difference(hardOwners); !outside.IsEmpty() {
			return fmt.Errorf("%w: kind=%s pod=%q expected=%s outside=%s",
				ErrPoolOwnerHardNotSubset, id.Kind, id.PodName,
				owner.ExpectedCPUSet.String(), outside.String())
		}
	}
	return nil
}

type sharedProjection struct {
	domain    machine.CPUSet
	projected machine.CPUSet
}

// projectSharedPending projects eligible non-SNB shared ramp-up pending
// protections onto the whole-core clean shared non-reclaim domain. It never
// mutates base. Any contradiction returns a typed, zero-write error carrying
// pod/container/pending/domain/dedicated/isolation/reclaim diagnostics; it is
// never nil, Deferred, or an empty target.
func projectSharedPending(
	base *model.DesiredView,
	topo *machine.CPUTopology,
	protections []topology.PendingProtection,
	available machine.CPUSet,
	hard machine.CPUSet,
) (sharedProjection, error) {
	var out sharedProjection
	// sharedDomainRaw = DesiredNonReclaimPool ∩ available − Dedicated − Isolation.
	// DesiredNonReclaimPool already includes dedicated/isolation, so they are
	// subtracted explicitly; hard owners are NOT subtracted again.
	raw := base.DesiredNonReclaimPool.Intersection(available).
		Difference(base.Dedicated).Difference(base.Isolation)
	domain, err := sharedWholeCoreDomain(raw, available, topo.CPUDetails)
	if err != nil {
		return out, err
	}
	out.domain = domain
	if domain.IsEmpty() {
		return out, fmt.Errorf("%w: dedicated=%s isolation=%s reclaim=%s",
			ErrSharedProjectionDomainEmpty,
			base.Dedicated.String(), base.Isolation.String(), base.DesiredReclaimEffective.String())
	}

	if err := validateHardOwnerSubset(base); err != nil {
		return out, err
	}

	all := topo.CPUDetails.CPUs()
	projected := machine.NewCPUSet()
	for _, p := range protections {
		// Only the per-container projected subset is projected onto the shared
		// domain; the protection's exact siblings stay exact and are never folded
		// here.
		pending := p.SharedProjectedCPUs
		if outside := pending.Difference(all); !outside.IsEmpty() {
			return out, fmt.Errorf("%w: pod=%q rel=%q pending=%s outside_topo=%s",
				ErrSharedProjectionTopologyPartial, p.PodUID, p.ScopeRel, pending.String(), outside.String())
		}
		pendingWhole := expandToWholeCores(pending, topo.CPUDetails)
		hit := pendingWhole.Intersection(domain)
		if hit.IsEmpty() {
			return out, fmt.Errorf("%w: pod=%q rel=%q pending=%s pending_whole=%s domain=%s dedicated=%s isolation=%s reclaim=%s",
				ErrSharedProjectionOutsideDomain, p.PodUID, p.ScopeRel,
				pending.String(), pendingWhole.String(), domain.String(),
				base.Dedicated.String(), base.Isolation.String(), base.DesiredReclaimEffective.String())
		}
		projected = projected.Union(hit)
	}

	// By construction projected ⊆ domain ⊆ DesiredNonReclaimPool. Assert the
	// disjointness invariants explicitly: projected must not leak outside the
	// domain and must be disjoint from hard owners and the desired reclaim budget.
	if leak := projected.Difference(domain); !leak.IsEmpty() {
		return out, fmt.Errorf("%w: projected=%s leaks outside domain=%s",
			ErrSharedProjectionHardOverlap, projected.String(), domain.String())
	}
	if overlap := projected.Intersection(hard); !overlap.IsEmpty() {
		return out, fmt.Errorf("%w: projected=%s overlaps hard=%s",
			ErrSharedProjectionHardOverlap, projected.String(), overlap.String())
	}
	if overlap := projected.Intersection(base.DesiredReclaimEffective); !overlap.IsEmpty() {
		return out, fmt.Errorf("%w: projected=%s overlaps reclaim=%s",
			ErrSharedProjectionHardOverlap, projected.String(), overlap.String())
	}
	out.projected = projected
	return out, nil
}

// validatedNonReclaimAuthority returns the manager-owned validated non-reclaim
// partition used to clip pending protections on a soft, non-overlap admission.
//
// The clipping authority is deliberately NOT the business DesiredNonReclaimPool.
// DesiredNonReclaimPool is the target the planner wants to grow into, not a
// validated ceiling: on the admission round it can already have self-expanded to
// absorb the whole NUMA the pending pod wants (it then equals the whole NUMA the
// pending scope already covers), so intersecting the pending scope with it clips
// nothing and lets the primary widen onto a reclaim bucket, dropping that bucket's
// required floor and stalling at a bare fixed-point no-progress.
//
// The clipping authority is instead the non-reclaim partition already validated on
// the node: the pre-admission AppliedView's NonReclaimPool (the manager-owned
// prior partition), falling back to the equivalent materialized validated
// partition folded into the DesiredView. Request quantities, AQC/KCC sizing, and
// a machine-wide fallback are deliberately not consulted. A nil/initial applied
// view with no materialized fallback yields an empty set; the caller treats that
// as fail-closed (preserving protections verbatim as exact).
func validatedNonReclaimAuthority(base *model.DesiredView, applied *model.AppliedView) machine.CPUSet {
	if applied != nil && !applied.NonReclaimPool.IsEmpty() {
		return applied.NonReclaimPool
	}
	if base != nil {
		return base.NonReclaimPool
	}
	return machine.NewCPUSet()
}

// ProjectPendingProtectionsToDesiredNonReclaim normalizes admission-time
// protections for the soft, non-overlap mode. The business allocation may still
// carry a full-machine shared RampUp placeholder while the partition view has
// already assigned reclaim and hard owners. Protecting that placeholder exactly
// would reclassify reclaim as non-reclaim and produce an empty system target.
//
// The pending scope is clipped to the manager-owned validated non-reclaim
// partition (the pre-admission AppliedView NonReclaimPool, or the equivalent
// materialized validated partition folded into the DesiredView). The business
// DesiredNonReclaimPool remains business truth and is deliberately NOT the clip
// authority: on this round it can already have self-expanded to the whole NUMA the
// pending pod wants, which would clip nothing and widen the primary onto a reclaim
// bucket. Dedicated and isolation CPUs are excluded because their ownership is
// already represented by the base view.
//
// Two classes of protection are handled distinctly:
//
//   - Eligible shared ramp-up protections overlap the shared domain. They are
//     clipped to the validated non-reclaim domain − Dedicated − Isolation and
//     flagged as shared-projected, so they never take CPUs away from reclaim.
//   - Fully-exact protections (SNB / steady / dedicated / an admission-target
//     seed leaf) whose CPUs have no overlap with the shared domain are retained
//     verbatim as exact. In particular, the admission seed leaf's required CPU
//     has not yet been folded into the validated non-reclaim partition; dropping
//     it would starve the leaf's widen-able expand target while ParentSafety still
//     owes it the CPU, producing the bare fixed-point no-progress deadlock. Such a
//     retained protection is NOT bypassed: DeriveShadowPartition still folds its
//     whole cores and fail-closes with a HardCollision hold when it lands on a
//     dedicated/isolation hard owner.
//
// Request quantities and business allocation state are deliberately not
// consulted or modified.
func ProjectPendingProtectionsToDesiredNonReclaim(
	base *model.DesiredView,
	topo *machine.CPUTopology,
	protections []topology.PendingProtection,
	applied *model.AppliedView,
) []topology.PendingProtection {
	if base == nil || topo == nil || len(topo.CPUDetails) == 0 || len(protections) == 0 {
		return nil
	}

	available := topo.CPUDetails.CPUs().Difference(base.Reserve)
	// Clip to the manager-owned validated non-reclaim partition, never to the
	// self-expanded business DesiredNonReclaimPool (see validatedNonReclaimAuthority).
	// This only ever intersects the pending set with CPUs already proven non-reclaim,
	// so it can release reclaim CPUs but never claim new ones. When there is no
	// validated non-reclaim domain at all the domain below is empty and every
	// protection takes the verbatim exact-seed retention branch (fail-closed).
	nonReclaimAuthority := validatedNonReclaimAuthority(base, applied)
	sharedDomain := nonReclaimAuthority.Intersection(available).
		Difference(base.Dedicated).Difference(base.Isolation)
	out := make([]topology.PendingProtection, 0, len(protections))
	for _, protection := range protections {
		// The exact remainder is every pending CPU NOT classified as a shared
		// ramp-up container. It is folded whole-core by the shadow and must survive
		// the clip verbatim; only the already-classified eligible subset is narrowed
		// to the validated non-reclaim domain.
		exactRemainder := protection.CPUs.Difference(protection.SharedProjectedCPUs)
		if protection.SharedProjectedCPUs.IsEmpty() {
			// No container classified as shared-eligible: legacy overlap clip. The
			// protection's requested CPUs have no overlap with the desired non-reclaim
			// shared domain. This is the admission-target SNB/seed leaf (or another
			// fully-exact scope) whose required CPU has not yet been folded into
			// DesiredNonReclaimPool. Dropping it would starve the leaf's widen-able
			// expand target while ParentSafety still owes it the required CPU,
			// producing the bare fixed-point no-progress deadlock. Preserve the
			// protection verbatim as exact (SharedProjectedCPUs untouched) so
			// DeriveShadowPartition folds its whole cores onto the non-reclaim side.
			projected := protection.CPUs.Intersection(sharedDomain)
			if projected.IsEmpty() {
				out = append(out, protection)
				continue
			}
			protection.CPUs = projected.Clone()
			protection.SharedProjectedCPUs = projected.Clone()
			out = append(out, protection)
			continue
		}
		// A classified shared-eligible protection (possibly mixed with an exact
		// sibling in the same pod): narrow only the eligible subset to the validated
		// non-reclaim domain. The exact remainder stays verbatim in CPUs so the
		// shadow folds its whole cores; it is never clipped onto the domain.
		sharedEligible := protection.SharedProjectedCPUs.Intersection(sharedDomain)
		protection.CPUs = exactRemainder.Union(sharedEligible)
		if !sharedEligible.IsEmpty() {
			protection.SharedProjectedCPUs = sharedEligible
		}
		// When the eligible subset projects entirely outside the validated domain,
		// leave the raw eligible subset in SharedProjectedCPUs rather than blanking
		// it to the (empty) domain intersection. Blanking it would drop the
		// protection out of the shared-projection set, silently reclassify it as
		// exact, and swallow the downstream typed zero-write
		// ErrSharedProjectionOutsideDomain. Keeping it classified lets
		// projectSharedPending raise that contract; the exact remainder stays exact.
		out = append(out, protection)
	}
	return out
}

// DeriveShadowPartition is the pure-function projection described in the
// topology-only shadow design. It never mutates base. It derives the shadow
// partition from the business DesiredView (no enforcement baseline). Admission
// paths that hold a validated pre-admission AppliedView must use
// DeriveShadowPartitionWithAppliedEnforcement so the enforced reclaim floors are
// built from that validated prior partition rather than the self-expanded business
// desired pool.
//
// Pending protections are split into two classes:
//
//   - Exact (default): the allocation is materialized/resolved, steady shared,
//     SNB, dedicated, isolation, reclaimed, system, or otherwise not eligible for
//     projection. Its whole cores are folded wholesale onto the non-reclaim side,
//     exactly as before; a collision with a hard owner holds-current.
//   - Shared projected (eligible non-SNB shared ramp-up, observed under the
//     HardPartition=false && overlap=false prepare gate): its whole cores are
//     intersected with the whole-core clean shared non-reclaim domain
//     (DesiredNonReclaimPool − Dedicated − Isolation) rather than folded wholesale.
//     Because the projection is a subset of DesiredNonReclaimPool it never takes
//     CPUs away from reclaim.
//
// Order (the invariants):
//  1. available = all CPUs - reserve.
//  2. hard = dedicated + isolation + committed dedicated/isolation owners.
//  3. exact pending folded onto whole cores; if it overlaps hard => hold-current.
//  4. shared pending projected onto the whole-core shared non-reclaim domain; any
//     contradiction (half-core domain, empty domain, pending outside the domain,
//     partial topology, hard/reclaim overlap, owner-subset violation) is a typed,
//     zero-write failure carried on ShadowPartition.Err (never Deferred).
//  5. shadow non-reclaim = desired non-reclaim pool ∪ exactWhole ∪ sharedProjected.
//  6. shadow reclaim = (available - shadow non-reclaim) ∩ desired reclaim budget.
//  7. per-NUMA floor projected from shadow reclaim against real NUMA IDs; any
//     NUMA that carried a budget floor but now has none => hold-current.
//  8. self-check disjointness / subset / projection completeness.
func DeriveShadowPartition(base *model.DesiredView, topo *machine.CPUTopology, protections []topology.PendingProtection) ShadowPartition {
	return DeriveShadowPartitionWithAppliedEnforcement(base, topo, protections, nil)
}

// DeriveShadowPartitionWithAppliedEnforcement is DeriveShadowPartition with an
// optional enforcement baseline: the pre-admission validated AppliedView
// partition. When non-nil and carrying a non-empty ReclaimEffective fully inside
// availability, the shadow
// non-reclaim / reclaim budgets are built from that validated prior partition
// instead of from the self-expanded business DesiredNonReclaimPool /
// DesiredReclaimEffective, and the projected pending protection is then folded
// onto the non-reclaim side exactly as before.
//
// Why: on a soft, non-overlap admission the business DesiredView has often
// self-expanded DesiredNonReclaimPool to the whole NUMA the pending pod wants,
// which already absorbed a previously-validated reclaim bucket (e.g. the NUMA0
// pair {4,68}) into the primary. Deriving the shadow from that self-expanded pool
// drops the reclaim bucket out of the NodeSpec / required floor and lets the
// primary widen onto it, stalling convergence at a bare no-progress. Building the
// enforcement baseline from the pre-admission AppliedView keeps those validated
// reclaim CPUs reclaimed while the projected pending protection (already clipped
// to the validated non-reclaim authority by ProjectPendingProtectionsToDesiredNonReclaim)
// folds on top without touching the business desired allocation.
//
// A nil/empty enforcement baseline preserves the historical business-desired
// projection verbatim: periodic/full, hard/overlap, topology-disabled, and the
// initial nil-AppliedView admission round are unchanged.
func DeriveShadowPartitionWithAppliedEnforcement(
	base *model.DesiredView,
	topo *machine.CPUTopology,
	protections []topology.PendingProtection,
	enforce *model.CPUSetPartitionView,
) ShadowPartition {
	var sh ShadowPartition
	if base == nil || topo == nil || len(topo.CPUDetails) == 0 {
		return sh
	}

	all := topo.CPUDetails.CPUs()
	available := all.Difference(base.Reserve)

	// Split protections: for each pending protection, the projected CPU subset
	// (eligible non-SNB shared ramp-up containers) is projected onto the shared
	// domain, while the remainder (exact siblings / steady / SNB / dedicated /
	// isolation / reclaimed / system) is folded whole-core. Classification is per
	// container: SharedProjectedCPUs is a subset of CPUs, never a pod-wide flag.
	var exactCPUs machine.CPUSet
	sharedProtectionsList := make([]topology.PendingProtection, 0, len(protections))
	for _, protection := range protections {
		exactCPUs = exactCPUs.Union(protection.CPUs.Difference(protection.SharedProjectedCPUs))
		if !protection.SharedProjectedCPUs.IsEmpty() {
			sharedProtectionsList = append(sharedProtectionsList, protection)
		}
	}

	exactWhole := expandToWholeCores(exactCPUs, topo.CPUDetails)
	sh.ExactWhole = exactWhole

	// pendingPodUIDs names every pod on the pending protection side. It scopes the
	// identity-scoped self-owner exclusion: a committed dedicated/isolation owner
	// whose non-empty ProofPodUID is one of these is the pending pod's own
	// allocation, not a different non-movable owner.
	pendingPodUIDs := make(map[string]struct{}, len(protections))
	for _, protection := range protections {
		if protection.PodUID != "" {
			pendingPodUIDs[protection.PodUID] = struct{}{}
		}
	}

	// Partition committed hard owners into the pending pods' own allocations
	// (selfOwned) and every other non-movable owner (otherOwned). The aggregates
	// base.Dedicated / base.Isolation already fold in the self owner's ExpectedCPUSet,
	// so the exclusion must be a subtraction from the FINAL union, not a drop from
	// the per-owner list: otherwise the self owner's CPUs come back through the
	// aggregate and re-trip a false hard_collision.
	//
	// selfExclusive is the subset of selfOwned that NO other/blank-proof hard owner
	// also claims. Only those CPUs may leave the hard set. A CPU the self owner
	// shares with another non-movable owner (the selfOwned ∩ otherOwned overlap) is
	// retained hard, because the overlap is a second, non-movable owner rather than
	// the pending pod re-affirming its own allocation.
	selfOwned, otherOwned := hardOwnerCPUSets(base, pendingPodUIDs)
	selfExclusive := selfOwned.Difference(otherOwned)

	// hard = dedicated + isolation + other committed hard owners, minus the
	// self-exclusive committed CPUs. Built whenever it can matter: an exact
	// collision, or the shared disjointness and owner-subset checks. Always a
	// non-nil (possibly empty) CPUSet.
	hard := machine.NewCPUSet()
	needsHard := !exactWhole.IsEmpty() || len(sharedProtectionsList) > 0
	if needsHard {
		hard = base.Dedicated.Clone().Union(base.Isolation).Union(otherOwned).Difference(selfExclusive)
	}

	// HardCollision applies to exact protection vs hard owners only. A shared
	// projected protection must never reach here overlapping hard owners; that is
	// asserted separately as a typed overlap failure.
	if !exactWhole.Intersection(hard).IsEmpty() {
		sh.HoldCurrent = true
		sh.HoldReason = HoldReasonHardCollision
		sh.ProtectedPending = exactWhole
		sh.ExcludedHardSet = hard
		return sh
	}
	sh.ExcludedHardSet = hard

	sharedProjectedWhole := machine.NewCPUSet()
	if len(sharedProtectionsList) > 0 {
		shared, err := projectSharedPending(base, topo, sharedProtectionsList, available, hard)
		if err != nil {
			sh.Err = err
			sh.SharedDomain = shared.domain
			sh.ProtectedPending = exactWhole
			sh.ExcludedHardSet = hard
			return sh
		}
		sh.SharedDomain = shared.domain
		sharedProjectedWhole = shared.projected
		sh.SharedProjected = sharedProjectedWhole
	}
	sh.ProtectedPending = exactWhole.Clone().Union(sharedProjectedWhole)

	// Enforcement partition baseline. The business DesiredReclaimEffective /
	// DesiredNonReclaimPool are the target the planner grows into. The shadow view
	// that builds the controlled DAG and required floors, however, must bridge from
	// the manager-owned, already-validated prior partition (the pre-admission
	// AppliedView). On the first soft/non-overlap admission for a whole-NUMA SNB pod
	// the business target has self-expanded DesiredNonReclaimPool to absorb the whole
	// NUMA (incl the NUMA0 reclaim bucket {4,68}) and shrunk DesiredReclaimEffective
	// to drop that bucket; deriving the shadow reclaim from the business target then
	// omits reclaimed-0, RequiredCPUSetByRel has no NUMA0 floor, and the leaf's
	// borrowed {4,68} can never be drained — a bare fixed-point no-progress.
	//
	// When a valid pre-admission AppliedView exists (non-empty ReclaimEffective fully
	// inside available), the enforcement reclaim is that validated partition and the
	// enforcement non-reclaim = available − enforcement reclaim; the projected pending
	// then folds onto non-reclaim as usual. A nil/initial AppliedView (empty reclaim)
	// fails closed to the business target, preserving periodic/full/hard/overlap/
	// topology-disabled behavior byte-for-byte.
	reclaimBudget := base.DesiredReclaimEffective
	nonReclaimBase := base.DesiredNonReclaimPool
	if enforce != nil &&
		!enforce.ReclaimEffective.IsEmpty() &&
		enforce.ReclaimEffective.Difference(available).IsEmpty() {
		reclaimBudget = enforce.ReclaimEffective
		nonReclaimBase = available.Difference(enforce.ReclaimEffective)
	}
	nonReclaim := nonReclaimBase.Clone().Union(exactWhole).Union(sharedProjectedWhole)
	// reclaim = available − nonReclaim, then clipped to the reclaim budget. With the
	// business fallback the budget is DesiredReclaimEffective; with a validated
	// AppliedView the budget is that partition itself, so the clip is a no-op and the
	// prior reclaim bucket (incl reclaimed-0) survives into the per-NUMA floor.
	reclaim := available.Difference(nonReclaim).Intersection(reclaimBudget)

	// Project reclaim onto real NUMA IDs in a single pass, reusing nodeCPUs for
	// both the per-NUMA reclaim slice and the budget-floor detection.
	perNUMA := make(map[int]machine.CPUSet)
	budgetPerNUMA := make(map[int]bool)
	for _, n := range topo.CPUDetails.NUMANodes().ToSliceInt() {
		nodeCPUs := topo.CPUDetails.CPUsInNUMANodes(n)
		perNUMA[n] = reclaim.Intersection(nodeCPUs)
		if !reclaimBudget.Intersection(nodeCPUs).IsEmpty() {
			budgetPerNUMA[n] = true
		}
	}

	// Fail-closed self-check. Disjointness of nonReclaim and reclaim is
	// guaranteed by construction (reclaim := available − nonReclaim clipped to
	// budget), so the meaningful checks are: (a) neither side leaks outside the
	// available set (guards against a DesiredNonReclaimPool that references a
	// reserved CPU), (b) the per-NUMA projection reconstructs the global reclaim
	// set exactly, and (c) the shared projection stays inside the desired
	// non-reclaim pool and disjoint from dedicated/isolation/reclaim — proving it
	// cannot change the partition.
	sharedStaysInNonReclaim := sharedProjectedWhole.Difference(base.DesiredNonReclaimPool).IsEmpty() &&
		sharedProjectedWhole.Intersection(base.Dedicated).IsEmpty() &&
		sharedProjectedWhole.Intersection(base.Isolation).IsEmpty() &&
		sharedProjectedWhole.Intersection(base.DesiredReclaimEffective).IsEmpty()
	if !sharedStaysInNonReclaim ||
		!nonReclaim.Union(reclaim).Difference(available).IsEmpty() ||
		!unionPerNUMA(perNUMA).Equals(reclaim) {
		sh.HoldCurrent = true
		sh.HoldReason = HoldReasonInvariantViolation
		sh.NonReclaimPool = nonReclaim
		sh.ReclaimEffective = reclaim
		sh.ReclaimEffectivePerNUMA = perNUMA
		return sh
	}

	sh.NonReclaimPool = nonReclaim
	sh.ReclaimEffective = reclaim
	sh.ReclaimEffectivePerNUMA = perNUMA

	// Full-pending: the reclaim budget has been entirely absorbed. Emitting an
	// empty reclaim desired onto a non-empty current reclaim cgroup is exactly
	// the v1 guard we must avoid; hold-current. This is checked before the
	// per-NUMA floor rule so a globally-empty reclaim reports full_pending.
	if reclaim.IsEmpty() {
		sh.HoldCurrent = true
		sh.HoldReason = HoldReasonFullPending
		return sh
	}

	// Any physical NUMA that carried a reclaim budget floor but was emptied by
	// the pending fold must not become an empty controlled bucket: hold-current.
	for n, hadFloor := range budgetPerNUMA {
		if hadFloor && perNUMA[n].IsEmpty() {
			sh.HoldCurrent = true
			sh.HoldReason = HoldReasonFloorExhausted
			return sh
		}
	}
	return sh
}

func unionPerNUMA(perNUMA map[int]machine.CPUSet) machine.CPUSet {
	out := machine.NewCPUSet()
	for _, cpus := range perNUMA {
		out = out.Union(cpus)
	}
	return out
}

// AsPartitionView shallow-copies the base partition view and overrides only the
// three shadow-derived fields, so BuildTopologyNodeSpecsFromView keeps reading
// all other business fields (SharePool/Dedicated/Isolation/Reserve/etc.)
// byte-identical to the base.
func (s ShadowPartition) AsPartitionView(base *model.CPUSetPartitionView) *model.CPUSetPartitionView {
	out := *base
	out.NonReclaimPool = s.NonReclaimPool
	out.ReclaimEffective = s.ReclaimEffective
	out.ReclaimEffectivePerNUMA = s.ReclaimEffectivePerNUMA
	return &out
}
