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

package provisionassembler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// defaultShareNUMABudget captures the per-NUMA canonical quantity budget used to
// compute the default share pool upper bound. All fields are expressed in
// unpinned CPU quantities (i.e. after excluding pinned resource-package CPUs).
type defaultShareNUMABudget struct {
	// UnpinnedAllocatableSize is numaAvailable[numaID] - pinnedCPUSizeInNUMA;
	// numaAvailable already excludes reserve and forbidden/system pools.
	UnpinnedAllocatableSize int
	// FinalUnpinnedReclaimSize is the post-clamp reclaim quantity that lives in
	// the unpinned eligibility domain of this NUMA. It is diagnostics-only for
	// the published upper bound because QRM's reclaim CPUSet snapshot may lag or
	// lead this quantity view.
	FinalUnpinnedReclaimSize int
	// FixedUnpinnedPoolSize is the sum of non-default, non-reclaim, non-reserve,
	// non-pinned, non-exclusive fixed pool quantities on this NUMA. It is
	// diagnostics-only: QRM materializes the exact fixed-pool CPUSet union from
	// its current allocation snapshot, which may lag or lead this quantity view.
	FixedUnpinnedPoolSize int
	// Exclusive marks a NUMA that is owned by a NUMA-exclusive region; such a
	// NUMA contributes zero default share capacity and its nested
	// reclaim/pinned/dedicated quantities are not deducted again.
	Exclusive bool
}

// defaultShareBudgetSummary carries classified quantities purely for diagnostics
// and metrics. It never participates in the residual computation.
type defaultShareBudgetSummary struct {
	AllocatableSize     int
	FixedCommonPoolSize int
	ReserveSize         int
	DedicatedSize       int
	IsolationSize       int
	CustomSharedSize    int
	SNBSize             int
	PinnedCPUSize       int
	ExclusiveNUMASize   int
}

// calculateDefaultShareTargetSize computes the allocatable upper bound that
// SysAdvisor publishes for the default share pool. Exclusive NUMAs contribute
// zero. Reclaim and fixed pool quantities intentionally do not lower this bound:
// QRM owns the exact CPUSet-level pool union and materializes the current
// residual. Reclaim exceeding allocatable capacity remains an invariant
// violation.
func calculateDefaultShareTargetSize(budgetByNUMA map[int]defaultShareNUMABudget) (int, error) {
	target := 0
	for numaID, budget := range budgetByNUMA {
		if budget.Exclusive {
			continue
		}
		if budget.FinalUnpinnedReclaimSize > budget.UnpinnedAllocatableSize {
			return 0, fmt.Errorf("default share reclaim exceeds unpinned allocatable in numa %d: unpinned=%d reclaim=%d",
				numaID, budget.UnpinnedAllocatableSize, budget.FinalUnpinnedReclaimSize)
		}
		target += budget.UnpinnedAllocatableSize
	}
	return target, nil
}

func hasEligibleDefaultShareDomain(budgetByNUMA map[int]defaultShareNUMABudget) bool {
	for _, budget := range budgetByNUMA {
		if budget.Exclusive {
			continue
		}
		if budget.UnpinnedAllocatableSize > 0 {
			return true
		}
	}
	return false
}

func canonicalizeDefaultShareEntries(result *types.InternalCPUCalculationResult) int {
	if result == nil {
		return 0
	}
	byNUMA := result.PoolEntries[commonstate.PoolNameShare]
	if byNUMA == nil {
		return 0
	}
	before := 0
	if existing, ok := byNUMA[commonstate.FakedNUMAID]; ok {
		before = existing.Size
	}
	for numaID := range byNUMA {
		if numaID != commonstate.FakedNUMAID {
			delete(byNUMA, numaID)
		}
	}
	if len(byNUMA) == 0 {
		delete(result.PoolEntries, commonstate.PoolNameShare)
	}
	return before
}

// dedicatedTopology captures the dedicated-cores topology needed by the default
// share budget accounting: the set of NUMAs owned by NUMA-exclusive dedicated
// regions, and a lookup from dedicated pod uid to its owner pool name.
//
// Dedicated pool entries in result.PoolEntries are keyed by pod uid (a single
// dedicated region owns exactly one pod uid), so the accounting loop only needs
// to test membership by pod uid rather than reverse-mapping pool entries back
// to region objects. A numa-binding dedicated pod spanning multiple NUMAs is
// split into one sibling region per NUMA, but every sibling shares the same pod
// uid and owner pool name, so folding them into a single per-uid entry is
// lossless.
type dedicatedTopology struct {
	exclusiveNUMAs        sets.Int
	ownerPoolNameByPodUID map[string]string
}

// collectDedicatedTopology walks every dedicated region (both the real-NUMA
// binding regions and the FakedNUMAID non-binding regions) and records the
// exclusive NUMAs plus the per-pod owner pool name. All sibling regions of the
// same pod uid share an identical owner pool name, so no representative choice
// is required.
func collectDedicatedTopology(regionHelper *RegionMapHelper, numaAvailable map[int]int) (dedicatedTopology, error) {
	topo := dedicatedTopology{
		exclusiveNUMAs:        sets.NewInt(),
		ownerPoolNameByPodUID: make(map[string]string),
	}

	record := func(r region.QoSRegion) error {
		if r.IsNumaBinding() && r.IsNumaExclusive() {
			for _, bindingNUMA := range r.GetBindingNumas().ToSliceInt() {
				topo.exclusiveNUMAs.Insert(bindingNUMA)
			}
		}
		for podUID := range r.GetPods() {
			// sibling regions of the same pod share the owner pool name; a
			// mismatch would mean two unrelated dedicated pods collided on one
			// pod uid, which must never happen.
			if existing, ok := topo.ownerPoolNameByPodUID[podUID]; ok && existing != r.OwnerPoolName() {
				return fmt.Errorf("pod uid %q maps to dedicated regions with conflicting owner pools %q and %q",
					podUID, existing, r.OwnerPoolName())
			}
			topo.ownerPoolNameByPodUID[podUID] = r.OwnerPoolName()
		}
		return nil
	}

	for numaID := range numaAvailable {
		for _, r := range regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicated) {
			if err := record(r); err != nil {
				return dedicatedTopology{}, err
			}
		}
	}
	// non-binding dedicated regions live at FakedNUMAID scope.
	for _, r := range regionHelper.GetRegions(commonstate.FakedNUMAID, configapi.QoSRegionTypeDedicated) {
		if err := record(r); err != nil {
			return dedicatedTopology{}, err
		}
	}
	return topo, nil
}

// buildDefaultShareBudget collects the canonical per-NUMA quantity budget that
// drives the default share pool upper-bound backfill. It reads only the already
// assembled result.PoolEntries plus the region topology; it never mutates the
// result.
//
// Accounting model (see rule set in the design doc):
//   - Each real NUMA yields UnpinnedAllocatableSize = numaAvailable[numaID] -
//     pinnedCPUSizeInNUMA. numaAvailable already excludes reserve and
//     forbidden/system pools, so only resource-package pinned CPUs are removed
//     here.
//   - A NUMA owned by a NUMA-exclusive dedicated region is marked Exclusive and
//     contributes zero residual; its nested reclaim/pinned/fixed quantities are
//     NOT deducted again to avoid double counting.
//   - Non-binding NUMAs are folded into a single combined bucket keyed by
//     FakedNUMAID, because the default share pool and its sibling non-binding
//     pools live at FakedNUMAID and collectively span the non-binding NUMAs.
//   - FinalUnpinnedReclaimSize only counts the unpinned reclaim entries (the
//     reclaim entry written per scope); resource-package reclaim is already
//     excluded through the pinned budget.
//   - FixedUnpinnedPoolSize only counts non-default, non-reclaim, non-reserve,
//     non-pinned, non-exclusive pool quantities.
//
// Assumption: dedicated pool entries are keyed by pod UID (not by a name that
// GetPoolType can classify), so we identify them authoritatively via the
// dedicated pod uid set collected by collectDedicatedTopology. Pinned
// (resource-package) pools are identified by the package prefix in their
// wrapped owner pool name.
func (pa *ProvisionAssemblerCommon) buildDefaultShareBudget(
	ctx ProvisionContext,
	regionHelper *RegionMapHelper,
	result *types.InternalCPUCalculationResult,
) (map[int]defaultShareNUMABudget, defaultShareBudgetSummary, error) {
	var summary defaultShareBudgetSummary

	numaAvailable := ctx.NUMAAvailable
	nonBinding := ctx.NonBindingNumas
	cfg := pa.metaReader.GetResourcePackageConfig()

	// dedicated pool entries are keyed by pod uid; collect the dedicated topology
	// (exclusive NUMAs and the per-pod owner pool name) up front so the
	// accounting loop can classify a pool as dedicated by pod uid directly,
	// instead of reverse-mapping pool entries back to region objects.
	topo, err := collectDedicatedTopology(regionHelper, numaAvailable)
	if err != nil {
		return nil, summary, err
	}

	// effectiveBucket folds non-binding real NUMAs into the FakedNUMAID bucket.
	effectiveBucket := func(numaID int) int {
		if numaID == commonstate.FakedNUMAID || nonBinding.Contains(numaID) {
			return commonstate.FakedNUMAID
		}
		return numaID
	}

	// seed per-NUMA allocatable budgets.
	budgetByNUMA := make(map[int]defaultShareNUMABudget)
	pinnedCPUSizeByPackageByBucket := make(map[int]map[string]int)
	combined := defaultShareNUMABudget{}
	hasCombined := false
	for numaID := range numaAvailable {
		pinnedCPUSizeByPackage := pa.getPinnedCPUSizeByPackage(machine.NewCPUSet(numaID), cfg)
		bucket := effectiveBucket(numaID)
		if pinnedCPUSizeByPackageByBucket[bucket] == nil {
			pinnedCPUSizeByPackageByBucket[bucket] = make(map[string]int)
		}
		for pkgName, size := range pinnedCPUSizeByPackage {
			pinnedCPUSizeByPackageByBucket[bucket][pkgName] += size
		}
		pinned := general.SumUpMapValues(pinnedCPUSizeByPackage)
		alloc := numaAvailable[numaID] - pinned
		summary.PinnedCPUSize += pinned
		summary.AllocatableSize += alloc

		if topo.exclusiveNUMAs.Has(numaID) {
			budgetByNUMA[numaID] = defaultShareNUMABudget{UnpinnedAllocatableSize: alloc, Exclusive: true}
			summary.ExclusiveNUMASize += alloc
			continue
		}
		if nonBinding.Contains(numaID) {
			combined.UnpinnedAllocatableSize += alloc
			hasCombined = true
		} else {
			budgetByNUMA[numaID] = defaultShareNUMABudget{UnpinnedAllocatableSize: alloc}
		}
	}

	// accumulate reclaim and fixed pool quantities per effective bucket.
	fixedByBucket := make(map[int]int)
	reclaimByBucket := make(map[int]int)
	for poolName, byNUMA := range result.PoolEntries {
		if poolName == commonstate.PoolNameReserve {
			for _, res := range byNUMA {
				summary.ReserveSize += res.Size
			}
			continue
		}
		// dedicated pool entries are keyed by pod uid, so classify a pool as
		// dedicated by looking the pool name up in the dedicated pod uid set
		// directly. This avoids reverse-mapping the pool entry back to a region
		// object, which is ambiguous because a numa-binding dedicated pod that
		// spans several NUMAs is split into one sibling region per NUMA.
		ownerPoolName := poolName
		isDedicated := false
		if opn, ok := topo.ownerPoolNameByPodUID[poolName]; ok {
			isDedicated = true
			ownerPoolName = opn
		}
		_, pkgName := resourcepackage.UnwrapOwnerPoolName(ownerPoolName)
		for numaID, res := range byNUMA {
			// nested quantities inside exclusive NUMAs are ignored.
			if topo.exclusiveNUMAs.Has(numaID) {
				continue
			}
			bucket := effectiveBucket(numaID)
			if poolName == commonstate.PoolNameReclaim {
				reclaimByBucket[bucket] += res.Size
				if bucket == commonstate.FakedNUMAID {
					hasCombined = true
				}
				continue
			}
			// the default share pool is exactly what we are computing; skip it.
			//
			// Precondition: the default share pool only ever exists at
			// FakedNUMAID, while share NUMA-binding (SNB) pools carry a
			// "-NUMA" suffix and live on real numaIDs. If upstream ever emits a
			// plain PoolNameShare entry on a real numaID, it would fall through
			// to the fixed-pool branch below and be counted into
			// FixedUnpinnedPoolSize, corrupting fixed-pool diagnostics. This is
			// a known precondition/constraint rather than a case handled here.
			if numaID == commonstate.FakedNUMAID && poolName == commonstate.PoolNameShare {
				continue
			}
			// Each pool entry is keyed by (pool name, numaID) and visited once,
			// so no dedup is required: a numa-binding dedicated pod split into
			// per-NUMA siblings writes one entry per real NUMA, and every entry
			// carries an independent dedicatedTarget that must all be counted.
			fixedSize := res.Size
			if pkgName != "" {
				pinnedSizeInBucket := pinnedCPUSizeByPackageByBucket[bucket][pkgName]
				if isDedicated {
					fixedSize = general.Max(res.Size-pinnedSizeInBucket, 0)
					if fixedSize == 0 {
						continue
					}
				} else if pinnedSizeInBucket > 0 {
					// Non-dedicated resource-package pools are already excluded
					// via the pinned budget deducted from UnpinnedAllocatableSize.
					continue
				}
			}
			// classify the fixed unpinned pool for diagnostics only.
			//
			// This classification (in particular the IsShareNUMABindingPool /
			// "-NUMA" suffix check for SNB) feeds only summary/metrics and does
			// not participate in the target computation. A custom shared pool
			// whose name happens to contain "-NUMA" may be misclassified as SNB
			// here; that is an accepted metrics-bucketing approximation and has
			// no effect on the residual result.
			switch {
			case commonstate.IsIsolationPool(poolName):
				summary.IsolationSize += fixedSize
			case isDedicated:
				summary.DedicatedSize += fixedSize
			case commonstate.IsShareNUMABindingPool(poolName):
				summary.SNBSize += fixedSize
			default:
				summary.CustomSharedSize += fixedSize
			}
			fixedByBucket[bucket] += fixedSize
			if bucket == commonstate.FakedNUMAID {
				hasCombined = true
			}
		}
	}

	// attach the combined non-binding bucket.
	//
	// Precondition on upstream assemble behavior: when there are no non-binding
	// NUMAs, the FakedNUMAID scope is expected to carry neither available
	// capacity (combined.UnpinnedAllocatableSize stays 0) nor reclaim/fixed
	// entries. If that assumption is violated (e.g. a reclaim entry exists at
	// FakedNUMAID while nonBinding is empty), the combined bucket ends up with
	// alloc=0 but reclaim>0, so calculateDefaultShareTargetSize reports reclaim
	// exceeding allocatable and the whole provision fails. This fail-closed outcome
	// is intentional: it surfaces the upstream inconsistency instead of silently
	// producing an incorrect default share target.
	if hasCombined {
		combined.FixedUnpinnedPoolSize = fixedByBucket[commonstate.FakedNUMAID]
		combined.FinalUnpinnedReclaimSize = reclaimByBucket[commonstate.FakedNUMAID]
		budgetByNUMA[commonstate.FakedNUMAID] = combined
	}
	// fill fixed/reclaim into the binding real-NUMA budgets.
	for numaID, budget := range budgetByNUMA {
		if numaID == commonstate.FakedNUMAID || budget.Exclusive {
			continue
		}
		budget.FixedUnpinnedPoolSize = fixedByBucket[numaID]
		budget.FinalUnpinnedReclaimSize = reclaimByBucket[numaID]
		budgetByNUMA[numaID] = budget
	}

	summary.FixedCommonPoolSize = 0
	for _, size := range fixedByBucket {
		summary.FixedCommonPoolSize += size
	}

	return budgetByNUMA, summary, nil
}

// finalizeDefaultShareBackfill overrides the default share pool quantity with
// the canonical allocatable upper bound once every scope has been assembled.
// QRM then materializes the exact residual from its current CPUSet allocation
// snapshot. It is a no-op when the backfill feature is disabled. On success it
// also records the structured diagnostics for metrics.
func (pa *ProvisionAssemblerCommon) finalizeDefaultShareBackfill(
	ctx ProvisionContext,
	regionHelper *RegionMapHelper,
	result *types.InternalCPUCalculationResult,
) error {
	if !result.DefaultShareBackfill.Enabled {
		return nil
	}
	before := canonicalizeDefaultShareEntries(result)
	budgetByNUMA, summary, err := pa.buildDefaultShareBudget(ctx, regionHelper, result)
	if err != nil {
		return err
	}
	target, err := calculateDefaultShareTargetSize(budgetByNUMA)
	if err != nil {
		return err
	}
	if target == 0 && hasEligibleDefaultShareDomain(budgetByNUMA) {
		return fmt.Errorf("default share target is zero before sysadvisor publish")
	}
	result.SetPoolEntry(commonstate.PoolNameShare, commonstate.FakedNUMAID, target, -1)
	result.DefaultShareBackfill.AllocatableBudget = summary.AllocatableSize
	result.DefaultShareBackfill.FixedPoolSize = summary.FixedCommonPoolSize
	result.DefaultShareBackfill.ReserveSize = summary.ReserveSize
	result.DefaultShareBackfill.DedicatedSize = summary.DedicatedSize
	result.DefaultShareBackfill.IsolationSize = summary.IsolationSize
	result.DefaultShareBackfill.CustomSharedSize = summary.CustomSharedSize
	result.DefaultShareBackfill.SNBSize = summary.SNBSize
	result.DefaultShareBackfill.PinnedCPUSize = summary.PinnedCPUSize
	result.DefaultShareBackfill.ExclusiveNUMASize = summary.ExclusiveNUMASize
	result.DefaultShareBackfill.DefaultShareBeforeBackfill = before
	result.DefaultShareBackfill.DefaultShareBackfilled = target - before
	result.DefaultShareBackfill.DefaultShareFinal = target
	general.InfoS("default share upper-bound backfill",
		"allocatableBudget", result.DefaultShareBackfill.AllocatableBudget,
		"reserveSize", result.DefaultShareBackfill.ReserveSize,
		"rawReclaimSize", result.DefaultShareBackfill.RawReclaimSize,
		"finalReclaimSize", result.DefaultShareBackfill.FinalReclaimSize,
		"releasedReclaimSize", result.DefaultShareBackfill.ReleasedReclaimSize,
		"fixedPoolSize", result.DefaultShareBackfill.FixedPoolSize,
		"pinnedCPUSize", result.DefaultShareBackfill.PinnedCPUSize,
		"exclusiveNUMASize", result.DefaultShareBackfill.ExclusiveNUMASize,
		"defaultShareBefore", result.DefaultShareBackfill.DefaultShareBeforeBackfill,
		"defaultShareAfter", result.DefaultShareBackfill.DefaultShareFinal,
		"unassignedNonReclaimSize", result.DefaultShareBackfill.UnassignedNonReclaimSize,
	)
	return nil
}
