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

package dynamicpolicy

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type pendingCPUPartition struct {
	expectedRevision          uint64
	entries                   state.PodEntries
	baseMachineState          state.NUMANodeMap
	allowOverlap              bool
	disableDedicated          bool
	persist                   bool
	source                    string
	validate                  func(state.PodEntries, state.NUMANodeMap, bool, bool) error
	requireCoreAlignedReclaim bool
	enforceSteadyReclaim      bool
	residualFloor             machine.CPUSet
	dynamicConfig             *dynamicconfig.Configuration
}

type preparedCPUPartition struct {
	pending      pendingCPUPartition
	entries      state.PodEntries
	machineState state.NUMANodeMap
}

type advisorPostCommitPendingError struct {
	pendingRevision   uint64
	attemptedRevision uint64
	source            string
}

func (e *advisorPostCommitPendingError) Error() string {
	return fmt.Sprintf(
		"cpu state writer %q must retry while advisor post-commit revision %d is pending (attempted revision %d)",
		e.source, e.pendingRevision, e.attemptedRevision)
}

func (e *advisorPostCommitPendingError) Retryable() bool {
	return true
}

// preparePendingCPUPartition performs every fallible precommit step without
// mutating plugin state: clone, hooks, normalize, rebuild, and validation.
func (p *DynamicPolicy) preparePendingCPUPartition(
	pending pendingCPUPartition,
) (*preparedCPUPartition, error) {
	if p == nil || p.state == nil {
		return nil, fmt.Errorf("prepare pending cpu partition: policy is not initialized")
	}
	if pending.dynamicConfig == nil {
		pending.dynamicConfig = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	if pending.entries == nil {
		return nil, fmt.Errorf("prepare pending cpu partition: entries are nil")
	}

	// Earliest common boundary: reject any candidate that already carries a
	// reclaimed_cores container in ramp-up before running hooks, normalization,
	// or machine-state rebuild. Hooks run next and may reintroduce it, so an
	// explicit post-hooks gate below re-checks the candidate (the ownership
	// validator itself does not re-scan for ramp-up).
	if err := validateNoReclaimedRampUp(pending.entries); err != nil {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, pending.expectedRevision, "reclaimed ramp-up invariant", err)
	}

	currentEntries := p.state.GetPodEntries()
	candidate := pending.entries.Clone()
	if err := p.invokeAllocationHooksForPodEntries(currentEntries, candidate); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "run allocation hooks", err)
	}
	p.syncCandidateSidecarsFromMain(candidate)
	var topology *machine.CPUTopology
	if p.machine.machineInfo != nil {
		topology = p.machine.machineInfo.CPUTopology
	}
	replacementBaseline := pending.entries
	if !pending.dynamicConfig.EnableReclaim {
		replacementBaseline = pending.entries.Clone()
		if err := preserveCommittedReclaimPool(candidate, currentEntries, pending.allowOverlap); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, pending.expectedRevision, "preserve disabled reclaim identity", err)
		}
		if err := preserveCommittedReclaimPool(replacementBaseline, currentEntries, pending.allowOverlap); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, pending.expectedRevision, "preserve disabled reclaim baseline", err)
		}
		if topology != nil {
			if err := p.normalizePendingCPUPartition(replacementBaseline); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "normalize disabled reclaim baseline", err)
			}
			if err := p.resyncPoolOwnedAllocations(replacementBaseline); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "resync disabled reclaim baseline", err)
			}
		}
	}

	// Build the reclaim ownership view once from the post-hooks candidate: it is
	// the single source of truth for the active RNB target NUMAs and the ordinary
	// (non-RNB) reclaim-eligible NUMAs, reused by pool-member resync and ownership
	// validation below so neither rescans the same candidate.
	ownershipView, err := buildReclaimOwnershipView(candidate, topology)
	if err != nil {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, pending.expectedRevision, "build reclaim ownership view", err)
	}

	// Post-hooks defensive boundary: hooks (and any restored checkpoint carried
	// through them) may reintroduce a reclaimed_cores container in ramp-up. This
	// runs on the post-hooks candidate before the topology branch, so both the
	// normal path and the machineInfo==nil path are covered. The ownership
	// validator itself intentionally no longer re-scans for ramp-up; this is the
	// single post-hook gate.
	if err := validateNoReclaimedRampUp(candidate); err != nil {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, pending.expectedRevision, "post-hooks reclaimed ramp-up invariant", err)
	}

	var machineState state.NUMANodeMap
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		if pending.baseMachineState == nil {
			return nil, fmt.Errorf("prepare pending cpu partition: cpu topology is not initialized")
		}
		machineState = pending.baseMachineState.Clone()
	} else {
		if err := p.normalizePendingCPUPartition(candidate); err != nil {
			return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "normalize allocation entries", err)
		}
		// Reconcile each pool's scalar result with its (sparse) NUMA buckets
		// after normalization so the pool-owned reclaim/share allocations stay
		// self-consistent. This never materializes empty buckets, so the pool
		// bucket map stays sparse and existing hook shape / current-original /
		// topology invariants are preserved.
		if err := p.resyncPoolOwnedAllocationsWithView(candidate, ownershipView); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, pending.expectedRevision, "resync pool-owned allocations", err)
		}
		if err := validateAllocationShapeAfterHooks(
			replacementBaseline, candidate, p.machine.machineInfo.CPUTopology,
		); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, pending.expectedRevision, "revalidate allocation shape after hooks", err)
		}
		if pending.requireCoreAlignedReclaim {
			if err := assertCoreAligned(reclaimPoolCPUSet(candidate), p.machine.machineInfo.CPUTopology); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "revalidate reclaim core alignment after hooks", err)
			}
		}
		if pending.enforceSteadyReclaim {
			plannedReclaim := reclaimPoolCPUSet(replacementBaseline)
			candidateReclaim := reclaimPoolCPUSet(candidate)
			if err := validateSteadyReclaimPrecommitInvariant(
				plannedReclaim, candidateReclaim, p.machine.machineInfo.CPUTopology,
			); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "revalidate steady reclaim after hooks", err)
			}
		}
		baseMachineState := pending.baseMachineState
		if baseMachineState == nil {
			baseMachineState = p.state.GetMachineState()
		} else {
			baseMachineState = baseMachineState.Clone()
		}
		var err error
		machineState, err = generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, candidate, baseMachineState)
		if err != nil {
			return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "rebuild machine state", err)
		}
	}
	if err := p.validateResidualBackfillCandidateWithDynamicConfig(
		candidate, machineState, pending.residualFloor, pending.dynamicConfig); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "validate residual backfill candidate", err)
	}
	if err := validatePendingPoolOwnershipWithView(candidate, ownershipView); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "validate allocation ownership", err)
	}
	validate := pending.validate
	if validate == nil {
		validate = func(
			entries state.PodEntries,
			machineState state.NUMANodeMap,
			allowOverlap, disableDedicated bool,
		) error {
			return p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
				entries, machineState, allowOverlap, disableDedicated, pending.dynamicConfig)
		}
	}
	preValidationEntries := candidate.Clone()
	preValidationMachineState := machineState.Clone()
	if err := validate(candidate, machineState, pending.allowOverlap, pending.disableDedicated); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "validate hard floor and partition", err)
	}
	// Terminal non-empty reclaim guard: after the external validate callback may have
	// mutated the candidate (e.g. repair, resync), the actual candidate must still have
	// a non-empty reclaim pool. Shared with the applyPools commitPending closure.
	if err := validateRequiredReclaimPool(candidate); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, pending.expectedRevision, "reclaim empty required", err)
	}
	if !pending.dynamicConfig.EnableReclaim && !reclaimPoolCPUSet(candidate).Equals(reclaimPoolCPUSet(currentEntries)) {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, pending.expectedRevision, "preserve disabled reclaim identity",
			fmt.Errorf("candidate reclaim %s differs from committed reclaim %s",
				reclaimPoolCPUSet(candidate), reclaimPoolCPUSet(currentEntries)))
	}
	// Keep the frozen plan authoritative through the complete prepare phase.
	// A validator is expected to be read-only, but re-check the candidate here
	// so a late mutation cannot bypass the hook-time replacement invariants.
	if p.machine.machineInfo != nil && p.machine.machineInfo.CPUTopology != nil {
		if err := validateAllocationShapeAfterHooks(
			replacementBaseline, candidate, p.machine.machineInfo.CPUTopology,
		); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, pending.expectedRevision, "revalidate replacement baseline after partition validation", err)
		}
		if pending.requireCoreAlignedReclaim {
			if err := assertCoreAligned(reclaimPoolCPUSet(candidate), p.machine.machineInfo.CPUTopology); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "revalidate replacement baseline after partition validation",
					fmt.Errorf("reclaim is not core-aligned: %w", err))
			}
		}
		if pending.enforceSteadyReclaim {
			if err := validateSteadyReclaimPrecommitInvariant(
				reclaimPoolCPUSet(replacementBaseline),
				reclaimPoolCPUSet(candidate),
				p.machine.machineInfo.CPUTopology,
			); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, pending.expectedRevision, "revalidate replacement baseline after partition validation", err)
			}
		}
	}
	if !reflect.DeepEqual(preValidationEntries, candidate) ||
		!reflect.DeepEqual(preValidationMachineState, machineState) {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, pending.expectedRevision, "revalidate replacement baseline after partition validation",
			fmt.Errorf("partition validator mutated its candidate"))
	}
	p.syncCandidateSidecarsFromMain(candidate)
	return &preparedCPUPartition{
		pending:      pending,
		entries:      candidate,
		machineState: machineState,
	}, nil
}

func (p *DynamicPolicy) syncCandidateSidecarsFromMain(entries state.PodEntries) {
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		main := containerEntries.GetMainContainerEntry()
		if main == nil {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation == nil || !allocation.CheckSideCar() {
				continue
			}
			// applySidecarAllocationInfoFromMainContainer preserves an already-recorded
			// sidecar "false" internally; the candidate entry already carries it.
			p.applySidecarAllocationInfoFromMainContainer(allocation, main)
		}
	}
}

func (p *DynamicPolicy) validateResidualBackfillCandidate(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	residualFloor machine.CPUSet,
) error {
	var dynamicConfig *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConfig = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.validateResidualBackfillCandidateWithDynamicConfig(
		entries, machineState, residualFloor, dynamicConfig)
}

func (p *DynamicPolicy) validateResidualBackfillCandidateWithDynamicConfig(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	residualFloor machine.CPUSet,
	dynamicConfig *dynamicconfig.Configuration,
) error {
	if dynamicConfig == nil || !dynamicConfig.FillDefaultSharePoolWithNonReclaimCPUs {
		return nil
	}
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return fmt.Errorf("cpu topology is not initialized")
	}

	share := machine.NewCPUSet()
	var shareEntry *state.AllocationInfo
	if poolEntries := entries[commonstate.PoolNameShare]; poolEntries != nil {
		shareEntry = poolEntries.GetPoolEntry()
		if shareEntry == nil {
			return fmt.Errorf("default share pool entry is invalid")
		}
		share = shareEntry.AllocationResult
	}

	fixedPools := machine.NewCPUSet()
	for poolName, containerEntries := range entries {
		if poolName == commonstate.PoolNameShare || !containerEntries.IsPoolEntry() {
			continue
		}
		poolEntry := containerEntries.GetPoolEntry()
		if poolEntry != nil {
			fixedPools = fixedPools.Union(poolEntry.AllocationResult)
		}
	}
	dedicated := unionIsolatedCPUSet(dedicatedCPUSetFromPodEntries(entries))

	if overlap := share.Intersection(fixedPools); !overlap.IsEmpty() {
		return fmt.Errorf("default share overlaps fixed pools: %s", overlap.String())
	}
	if overlap := share.Intersection(dedicated); !overlap.IsEmpty() {
		return fmt.Errorf("default share overlaps isolated or dedicated cpus: %s", overlap.String())
	}

	eligible := p.buildDefaultShareEligibleCPUSet(entries, machineState, residualFloor)
	expected := eligible.Difference(fixedPools).Difference(dedicated)
	activeRampUp := globalDomainActiveRampUpCPUSet(entries)
	extraShare := share.Difference(expected)
	uncoveredResidual := expected.Difference(share).Difference(activeRampUp)
	if !extraShare.IsEmpty() || !uncoveredResidual.IsEmpty() {
		return fmt.Errorf("default share cpuset %s with active ramp-up %s differs from eligible residual %s (uncovered=%s extra_share=%s eligible=%s fixed=%s dedicated=%s)",
			share.String(), activeRampUp.String(), expected.String(), uncoveredResidual.String(), extraShare.String(),
			eligible.String(), fixedPools.String(), dedicated.String())
	}
	if shareEntry == nil {
		return nil
	}

	expectedAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, share)
	if err != nil {
		return fmt.Errorf("calculate default share topology assignments: %w", err)
	}
	if !cpuAssignmentsEqual(shareEntry.TopologyAwareAssignments, expectedAssignments) {
		return fmt.Errorf("default share topology assignments are inconsistent with cpuset %s", share.String())
	}
	return nil
}

func cpuAssignmentsEqual(left, right map[int]machine.CPUSet) bool {
	if len(left) != len(right) {
		return false
	}
	for numaID, leftCPUSet := range left {
		rightCPUSet, ok := right[numaID]
		if !ok || !leftCPUSet.Equals(rightCPUSet) {
			return false
		}
	}
	return true
}

// sortedPoolNUMAList returns the NUMA keys present in a pool's (sparse)
// TopologyAwareAssignments as a sorted int slice, for stable diagnostics. It has
// no effect on metrics (error string only).
func sortedPoolNUMAList(assignments map[int]machine.CPUSet) []int {
	numaIDs := make([]int, 0, len(assignments))
	for numaID := range assignments {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)
	return numaIDs
}

// reclaimedRampUpCategory names the reclaimed sub-class of an allocation that
// violates the reclaimed ramp-up contract, so diagnostics can distinguish an
// actually-NUMA-bound RNB container from an ordinary (non-NUMA-binding)
// reclaimed container.
func reclaimedRampUpCategory(allocation *state.AllocationInfo) string {
	if allocation.CheckReclaimedActualNUMABinding() {
		return "reclaimed-numa-binding (RNB)"
	}
	return "reclaimed non-NUMA-binding"
}

// validateNoReclaimedRampUp enforces the product invariant that no
// reclaimed_cores allocation may ever be in ramp-up. Both RNB (actually
// NUMA-bound) and ordinary (non-NUMA-binding) reclaimed containers are covered,
// while shared_cores ramp-up stays legal. It scans every container, including
// sidecars and init containers, so an inconsistent RampUp flag mirrored onto a
// sidecar (or carried in from an old checkpoint / hook / advisor candidate)
// cannot bypass the contract. Pool entries are unaffected because they carry no
// reclaimed QoS.
func validateNoReclaimedRampUp(entries state.PodEntries) error {
	for podUID, containerEntries := range entries {
		for containerName, allocation := range containerEntries {
			if allocation == nil || !allocation.RampUp || !allocation.CheckReclaimed() {
				continue
			}
			return fmt.Errorf(
				"%s allocation %s/%s (%s) is illegally RampUp: reclaimed_cores must never ramp up",
				allocation.QoSLevel, podUID, containerName, reclaimedRampUpCategory(allocation))
		}
	}
	return nil
}

// reclaimOwnershipView is the precomputed ownership relationship between
// reclaimed members and the reclaim pool for one candidate. It is built once from
// a PodEntries snapshot so that pool-member resync, ownership validation, and
// the advisor reclaim revision do not each rescan the same candidate for active
// RNB target NUMAs.
type reclaimOwnershipView struct {
	// activeRNBTargetNUMAs holds the NUMA each active (non-ramp-up)
	// reclaimed-numa-binding allocation is actually bound to.
	activeRNBTargetNUMAs map[int]struct{}
	// ordinaryReclaimNUMAs is the set of topology NUMAs NOT bound to an active
	// RNB allocation; ordinary (non-NUMA-binding) reclaimed members are
	// materialized from the union of the reclaim pool buckets on these NUMAs.
	ordinaryReclaimNUMAs machine.CPUSet
}

func (v reclaimOwnershipView) isActiveRNBNUMA(numaID int) bool {
	_, ok := v.activeRNBTargetNUMAs[numaID]
	return ok
}

// buildReclaimOwnershipView scans entries once and returns the active RNB target
// NUMAs plus (when topology is non-nil) the ordinary (non-RNB) reclaim-eligible
// NUMAs derived as the topology complement. It is the single source of truth for
// both. A reclaimed-numa-binding allocation with an unparseable NUMA hint fails
// closed; ramp-up allocations are already rejected by validateNoReclaimedRampUp
// upstream and are simply not active RNB targets here.
func buildReclaimOwnershipView(
	entries state.PodEntries, topology *machine.CPUTopology,
) (reclaimOwnershipView, error) {
	view := reclaimOwnershipView{
		activeRNBTargetNUMAs: make(map[int]struct{}),
		ordinaryReclaimNUMAs: machine.NewCPUSet(),
	}
	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || allocation.RampUp ||
				!allocation.CheckReclaimedActualNUMABinding() {
				continue
			}
			numaID, err := allocation.GetSpecifiedNUMABindingNUMAID()
			if err != nil {
				return view, fmt.Errorf("%s allocation %s/%s has invalid target NUMA: %w",
					allocation.QoSLevel, podUID, containerName, err)
			}
			view.activeRNBTargetNUMAs[numaID] = struct{}{}
		}
	}
	if topology != nil {
		for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
			if !view.isActiveRNBNUMA(numaID) {
				view.ordinaryReclaimNUMAs.Add(numaID)
			}
		}
	}
	return view, nil
}

// reclaimBucketClass names the four distinguishable states of an owner-pool
// NUMA bucket. It collapses what used to be three parallel booleans
// (bucket_present / bucket_initialed / bucket_empty) into one value so the
// producer, resync, and validator share a single, fail-closed ownership meaning.
type reclaimBucketClass int

const (
	reclaimBucketAbsent        reclaimBucketClass = iota // key not present in the bucket map
	reclaimBucketUninitialized                           // key present, CPUSet zero value (Initialed=false)
	reclaimBucketEmpty                                   // key present, initialized, zero CPUs
	reclaimBucketReady                                   // key present, initialized, at least one CPU
)

func (c reclaimBucketClass) String() string {
	switch c {
	case reclaimBucketAbsent:
		return "missing"
	case reclaimBucketUninitialized:
		return "uninitialized"
	case reclaimBucketEmpty:
		return "explicit-empty"
	default:
		return "ready"
	}
}

// resolvedReclaimBucket is the value-object result of resolving one owner-pool
// NUMA bucket. cpus is only meaningful when class == reclaimBucketReady.
type resolvedReclaimBucket struct {
	class reclaimBucketClass
	cpus  machine.CPUSet
}

// failClosedError renders the provisioning-gap error for an RNB member whose
// owner-pool bucket is not ready. missing, uninitialized, and explicit-empty stay
// textually distinguishable; a ready-but-wrong bucket is reported by the caller
// as a content mismatch instead.
func (b resolvedReclaimBucket) failClosedError(pool *state.AllocationInfo, numaID int) error {
	size := -1
	if b.class != reclaimBucketAbsent {
		size = b.cpus.Size()
	}
	return fmt.Errorf(
		"active reclaimed-numa-binding on NUMA %d is not backed by owner pool %q: "+
			"bucket_state=%s bucket_size=%d (pool_total=%s, pool_numas=%v)",
		numaID, pool.OwnerPoolName, b.class, size,
		pool.AllocationResult.String(), sortedPoolNUMAList(pool.TopologyAwareAssignments))
}

// lookupReclaimBucketIn resolves one NUMA bucket of a (possibly deep-copied) pool
// assignment map into its class. It never returns an error: callers decide which
// classes are acceptable, so missing/uninitialized/empty stay distinguishable.
func lookupReclaimBucketIn(assignments map[int]machine.CPUSet, numaID int) resolvedReclaimBucket {
	bucket, ok := assignments[numaID]
	if !ok {
		return resolvedReclaimBucket{class: reclaimBucketAbsent}
	}
	if !bucket.Initialed {
		return resolvedReclaimBucket{class: reclaimBucketUninitialized, cpus: bucket}
	}
	if bucket.IsEmpty() {
		return resolvedReclaimBucket{class: reclaimBucketEmpty, cpus: bucket}
	}
	return resolvedReclaimBucket{class: reclaimBucketReady, cpus: bucket}
}

func lookupReclaimBucket(pool *state.AllocationInfo, numaID int) resolvedReclaimBucket {
	return lookupReclaimBucketIn(pool.TopologyAwareAssignments, numaID)
}

// expectedReclaimMemberCPUSet resolves the owner-pool cpuset a reclaimed member
// must own, used by the validator. For an active RNB member it requires a ready
// bucket on its target NUMA and shares the exact bucket classifier
// (lookupReclaimBucket) that the producer (updateReclaimAllocationResultByPoolEntry)
// uses, so both fail closed identically on missing/uninitialized/empty. For an
// ordinary (non-actual-binding) reclaimed member it unions every pool bucket whose
// NUMA is not an active RNB target; this is an equivalent complement predicate to
// the producer's nonReclaimActualBindingNUMAs filter, not a call into the producer.
func expectedReclaimMemberCPUSet(
	pool *state.AllocationInfo, alloc *state.AllocationInfo, view reclaimOwnershipView,
) (machine.CPUSet, error) {
	if alloc.CheckReclaimedActualNUMABinding() {
		numaID, err := alloc.GetSpecifiedNUMABindingNUMAID()
		if err != nil {
			return machine.CPUSet{}, fmt.Errorf("invalid target NUMA: %w", err)
		}
		rb := lookupReclaimBucket(pool, numaID)
		if rb.class != reclaimBucketReady {
			return machine.CPUSet{}, rb.failClosedError(pool, numaID)
		}
		return rb.cpus.Clone(), nil
	}
	expected := machine.NewCPUSet()
	for numaID, numaBucket := range pool.TopologyAwareAssignments {
		if view.isActiveRNBNUMA(numaID) {
			continue
		}
		expected = expected.Union(numaBucket)
	}
	return expected, nil
}

func validatePendingPoolOwnership(entries state.PodEntries) error {
	if err := validateNoReclaimedRampUp(entries); err != nil {
		return err
	}
	view, err := buildReclaimOwnershipView(entries, nil)
	if err != nil {
		return err
	}
	return validatePendingPoolOwnershipWithView(entries, view)
}

// validatePendingPoolOwnershipWithView is a near-linear orchestration: for every
// pool-owned member it resolves the owner pool, computes the single expected
// cpuset (shared with the producer via expectedReclaimMemberCPUSet), and compares.
// Ramp-up rejections are enforced by the caller's post-hooks gate (see
// preparePendingCPUPartition), not re-scanned here.
func validatePendingPoolOwnershipWithView(entries state.PodEntries, view reclaimOwnershipView) error {
	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || allocation.RampUp ||
				(!allocation.CheckShared() && !allocation.CheckReclaimed()) {
				continue
			}
			if isZeroCPUDefaultShareWithoutOwnership(allocation) {
				continue
			}
			ownerPool := allocation.GetOwnerPoolName()
			if ownerPool == commonstate.EmptyOwnerPoolName {
				return fmt.Errorf("%s allocation %s/%s has empty owner pool",
					allocation.QoSLevel, podUID, containerName)
			}
			pool := entries[ownerPool].GetPoolEntry()
			if pool == nil {
				return fmt.Errorf("%s allocation %s/%s owns missing pool %q",
					allocation.QoSLevel, podUID, containerName, ownerPool)
			}
			var expected machine.CPUSet
			if allocation.CheckReclaimed() {
				exp, err := expectedReclaimMemberCPUSet(pool, allocation, view)
				if err != nil {
					return fmt.Errorf("%s allocation %s/%s: %w",
						allocation.QoSLevel, podUID, containerName, err)
				}
				expected = exp
			} else {
				// Shared members inherit the pool scalar.
				expected = pool.AllocationResult
			}
			if !allocation.AllocationResult.ElementsEqual(expected) {
				return fmt.Errorf("%s allocation %s/%s cpuset %s (size=%d) differs from owner pool %q expected %s (size=%d, pool_numas=%v)",
					allocation.QoSLevel, podUID, containerName,
					allocation.AllocationResult.String(), allocation.AllocationResult.Size(),
					ownerPool, expected.String(), expected.Size(),
					sortedPoolNUMAList(pool.TopologyAwareAssignments))
			}
		}
	}
	return nil
}

// commitPreparedCPUPartition is the only state mutation for a prepared CPU
// partition. No fallible response or finalize work may follow this revision-CAS.
func (p *DynamicPolicy) commitPreparedCPUPartition(prepared *preparedCPUPartition) error {
	return p.commitPreparedCPUPartitionForAdvisorTarget(prepared, nil)
}

func (p *DynamicPolicy) commitPreparedCPUPartitionForAdvisorTarget(
	prepared *preparedCPUPartition,
	reconcileTarget *advisorPostCommitTarget,
) error {
	if prepared == nil {
		return fmt.Errorf("commit prepared cpu partition: candidate is nil")
	}
	var permits []*state.WritePermit
	if reconcileTarget != nil {
		permits = append(permits, p.newAdvisorStateWritePermit(reconcileTarget))
	}
	if err := p.state.CommitAdvisorStateIfRevision(
		prepared.pending.expectedRevision,
		prepared.entries,
		prepared.machineState,
		prepared.pending.allowOverlap,
		prepared.pending.disableDedicated,
		prepared.pending.persist,
		permits...,
	); err != nil {
		return err
	}
	p.emitFinalPoolSizeMetrics(prepared.entries)
	return nil
}

func (p *DynamicPolicy) commitPendingCPUPartition(
	pending pendingCPUPartition,
) (state.PodEntries, state.NUMANodeMap, error) {
	return p.commitPendingCPUPartitionForAdvisorTarget(pending, nil)
}

// commitPendingCPUPartitionForAdvisorTarget is the canonical mutation boundary
// for a planned CPU partition. It validates writer ownership, prepares the
// complete candidate, and publishes it only through the pending revision CAS;
// an advisor target supplies the narrow fence permit for that exact commit.
func (p *DynamicPolicy) commitPendingCPUPartitionForAdvisorTarget(
	pending pendingCPUPartition,
	reconcileTarget *advisorPostCommitTarget,
) (state.PodEntries, state.NUMANodeMap, error) {
	if err := p.ensureCPUStateWriterAllowed(
		pending.expectedRevision, pending.source, reconcileTarget); err != nil {
		return nil, nil, err
	}
	prepared, err := p.preparePendingCPUPartition(pending)
	if err != nil {
		return nil, nil, err
	}
	if err := p.commitPreparedCPUPartitionForAdvisorTarget(prepared, reconcileTarget); err != nil {
		return nil, nil, err
	}
	return prepared.entries, prepared.machineState, nil
}

func (p *DynamicPolicy) normalizePendingCPUPartition(entries state.PodEntries) error {
	for podUID, containerEntries := range entries {
		for containerName, allocation := range containerEntries {
			if allocation == nil {
				continue
			}
			assignments, err := machine.GetNumaAwareAssignments(
				p.machine.machineInfo.CPUTopology, allocation.AllocationResult)
			if err != nil {
				return fmt.Errorf("%s/%s allocation result: %w", podUID, containerName, err)
			}
			originalAssignments, err := machine.GetNumaAwareAssignments(
				p.machine.machineInfo.CPUTopology, allocation.OriginalAllocationResult)
			if err != nil {
				return fmt.Errorf("%s/%s original allocation result: %w", podUID, containerName, err)
			}
			allocation.TopologyAwareAssignments = assignments
			allocation.OriginalTopologyAwareAssignments = originalAssignments
		}
	}
	return nil
}

func validateAllocationShapeAfterHooks(
	planned, candidate state.PodEntries,
	topology *machine.CPUTopology,
) error {
	for podUID, plannedContainers := range planned {
		for containerName, plannedAllocation := range plannedContainers {
			if plannedAllocation == nil {
				continue
			}
			// Sidecars are intentionally normalized to their main container
			// after hooks, so their previous quantity and NUMA shape are not an
			// independent allocation contract.
			if plannedAllocation.CheckSideCar() {
				continue
			}
			candidateAllocation := candidate[podUID][containerName]
			if candidateAllocation == nil {
				return fmt.Errorf("allocation %s/%s was removed after hooks", podUID, containerName)
			}
			if plannedAllocation.AllocationResult.Size() != candidateAllocation.AllocationResult.Size() {
				return fmt.Errorf(
					"allocation quantity changed after hooks for %s/%s: planned=%d candidate=%d",
					podUID, containerName,
					plannedAllocation.AllocationResult.Size(),
					candidateAllocation.AllocationResult.Size())
			}
			for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
				numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
				plannedQuantity := plannedAllocation.AllocationResult.Intersection(numaCPUs).Size()
				candidateQuantity := candidateAllocation.AllocationResult.Intersection(numaCPUs).Size()
				if plannedQuantity != candidateQuantity {
					return fmt.Errorf(
						"allocation NUMA distribution changed after hooks for %s/%s NUMA %d: planned=%d candidate=%d",
						podUID, containerName, numaID, plannedQuantity, candidateQuantity)
				}
			}
		}
	}
	return nil
}

func reclaimPoolCPUSet(entries state.PodEntries) machine.CPUSet {
	if reclaimEntries := entries[commonstate.PoolNameReclaim]; reclaimEntries != nil {
		if reclaim := reclaimEntries[commonstate.FakedContainerName]; reclaim != nil {
			return reclaim.AllocationResult.Clone()
		}
	}
	return machine.NewCPUSet()
}

// resyncPoolOwnedAllocations re-derives each pool's scalar AllocationResult from
// the union of its (sparse) NUMA buckets, then rematerializes every pool-owned
// member container from the (possibly re-normalized) pool entry. normalize derives
// buckets from each entry's own scalar; after hooks the pool buckets may have
// changed (e.g. a backfilled RNB NUMA), so the member containers must be rebuilt
// from the pool entry before ownership validation runs. It reuses the canonical
// pool-entry update semantics (updateReclaimAllocationResultByPoolEntry for
// reclaimed members, pool-bucket copy for shared members), updates current and
// original topology consistently, never materializes empty pool buckets, and
// finally re-syncs each sidecar from its rematerialized main container.
func (p *DynamicPolicy) resyncPoolOwnedAllocations(entries state.PodEntries) error {
	// Rematerializing pool-owned members must never resurrect or silently carry a
	// reclaimed container in ramp-up: reject it here too so this path cannot be a
	// bypass even when it is reached on its own.
	if err := validateNoReclaimedRampUp(entries); err != nil {
		return err
	}
	var topology *machine.CPUTopology
	if p.machine.machineInfo != nil {
		topology = p.machine.machineInfo.CPUTopology
	}
	view, err := buildReclaimOwnershipView(entries, topology)
	if err != nil {
		return err
	}
	return p.resyncPoolOwnedAllocationsWithView(entries, view)
}

func (p *DynamicPolicy) resyncPoolOwnedAllocationsWithView(
	entries state.PodEntries, view reclaimOwnershipView,
) error {
	poolEntries := map[string]*state.AllocationInfo{}
	for _, poolName := range []string{commonstate.PoolNameReclaim, commonstate.PoolNameShare} {
		containerEntries, ok := entries[poolName]
		if !ok {
			continue
		}
		poolEntry := containerEntries.GetPoolEntry()
		if poolEntry == nil {
			continue
		}
		poolEntries[poolName] = poolEntry
		if len(poolEntry.TopologyAwareAssignments) > 0 {
			union := machine.NewCPUSet()
			for _, bucket := range poolEntry.TopologyAwareAssignments {
				union = union.Union(bucket)
			}
			poolEntry.AllocationResult = union
		}
		if len(poolEntry.OriginalTopologyAwareAssignments) > 0 {
			union := machine.NewCPUSet()
			for _, bucket := range poolEntry.OriginalTopologyAwareAssignments {
				union = union.Union(bucket)
			}
			poolEntry.OriginalAllocationResult = union
		}
	}
	if len(poolEntries) == 0 || p.machine.machineInfo == nil ||
		p.machine.machineInfo.CPUTopology == nil {
		return nil
	}

	// ordinaryReclaimNUMAs (from the shared view) is every topology NUMA except
	// those an active RNB member is actually bound to.
	nonReclaimActualBindingNUMAs := view.ordinaryReclaimNUMAs

	for podUID, containerEntries := range entries {
		if _, isPool := poolEntries[podUID]; isPool {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil ||
				(allocation.CheckSideCar() && containerEntries.GetMainContainerEntry() != nil) {
				continue
			}
			poolEntry, ok := poolEntries[allocation.OwnerPoolName]
			if !ok {
				continue
			}
			if allocation.CheckReclaimed() {
				// Reuse the canonical RNB/ordinary-reclaimed rematerialization and
				// propagate its error (e.g. an active RNB with no owner bucket).
				if err := p.updateReclaimAllocationResultByPoolEntry(
					allocation, poolEntry, nonReclaimActualBindingNUMAs); err != nil {
					return fmt.Errorf("rematerialize reclaimed member %s/%s from pool %q: %w",
						podUID, containerName, allocation.OwnerPoolName, err)
				}
				continue
			}
			// Shared members inherit the pool's current and original buckets.
			allocation.AllocationResult = poolEntry.AllocationResult.Clone()
			allocation.OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
			allocation.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
			allocation.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.OriginalTopologyAwareAssignments)
		}
	}
	// Main containers are rematerialized above while sidecars are deliberately
	// skipped. Sync again only after every main is final: the earlier post-hook
	// sync precedes pool resync, so without this pass a sidecar could retain the
	// stale pre-resync cpuset and fail ownership validation (or diverge if this
	// helper is called directly).
	p.syncCandidateSidecarsFromMain(entries)
	return nil
}

func validateSteadyReclaimPrecommitInvariant(
	planned, candidate machine.CPUSet,
	topology *machine.CPUTopology,
) error {
	if topology == nil {
		return fmt.Errorf("steady reclaim precommit topology is nil")
	}
	if err := assertCoreAligned(candidate, topology); err != nil {
		return fmt.Errorf("steady reclaim is not core-aligned: %w", err)
	}
	if candidate.Size() != planned.Size() {
		return fmt.Errorf(
			"steady reclaim quantity changed after hooks: planned=%d candidate=%d",
			planned.Size(), candidate.Size())
	}
	if churn := steadyFakeNUMAMigrationChurn(planned, candidate); churn >
		steadyFakeNUMAMaxMigratedCPUs {
		return fmt.Errorf(
			"steady reclaim hook migration churn %d exceeds limit %d",
			churn, steadyFakeNUMAMaxMigratedCPUs)
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		plannedQuantity := planned.Intersection(numaCPUs).Size()
		candidateQuantity := candidate.Intersection(numaCPUs).Size()
		if plannedQuantity != candidateQuantity {
			return fmt.Errorf(
				"steady reclaim NUMA distribution changed after hooks for NUMA %d: planned=%d candidate=%d",
				numaID, plannedQuantity, candidateQuantity)
		}
	}
	return nil
}

func (p *DynamicPolicy) wrapPartitionPrecommitError(source string, revision uint64, stage string, err error) error {
	if source == "" {
		source = "cpu partition"
	}
	return fmt.Errorf("%s precommit (expected_revision=%d) %s: %w", source, revision, stage, err)
}
