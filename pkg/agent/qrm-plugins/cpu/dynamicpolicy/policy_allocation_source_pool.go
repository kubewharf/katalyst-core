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
	"sort"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// deriveIsolationSourceSharePool derives the source share pool that a shared_cores
// isolation allocation should be carved from (and recycled back to). It intentionally
// covers only shared_cores isolation in phase 1; dedicated_cores has no source share
// pool semantics in the current data model and is out of scope. When the source cannot
// be reliably derived, it returns ("", false) so the caller can fall back to the legacy
// path without risking an incorrect carve.
func deriveIsolationSourceSharePool(allocationInfo *state.AllocationInfo) (string, bool) {
	if allocationInfo == nil {
		return "", false
	}

	// phase 1 only handles shared_cores isolation
	if allocationInfo.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores {
		return "", false
	}

	// numa_binding shared_cores: source is the share-NUMA{id} pool derived from the
	// declared enhancement + NUMA hint, not the current (isolation-*) owner pool.
	if allocationInfo.CheckNUMABinding() {
		sourcePool, err := commonstate.GetSpecifiedNUMABindingPoolName(
			allocationInfo.QoSLevel, allocationInfo.Annotations)
		if err != nil {
			return "", false
		}
		return sourcePool, true
	}

	// non numa_binding shared_cores: honor cpuset_pool enhancement, defaulting to share.
	sourcePool := commonstate.GetSpecifiedPoolName(
		allocationInfo.QoSLevel, allocationInfo.Annotations[apiconsts.PodAnnotationCPUEnhancementCPUSet])
	if sourcePool == commonstate.EmptyOwnerPoolName {
		return "", false
	}
	return sourcePool, true
}

// takeByTieredPreferredCPUs allocates exactly cpuRequirement cpus from
// availableCPUs. It prefers complete physical cores before any sub-core tail;
// within each phase, ordered preferred tiers are consumed before the remaining
// available CPUs. The trailing protected set are other pools' legal exact own
// histories that a growth/fallback spill must not take; the current pool's own
// exact match is unaffected. It returns the taken set and the remaining
// available set. This is the shared building block that lets isolation cpusets
// be carved from their source share pool region and recycled back to it across
// recomputes.
func (p *DynamicPolicy) takeByTieredPreferredCPUs(
	availableCPUs machine.CPUSet,
	preferredTiers []machine.CPUSet,
	cpuRequirement int,
	atomicDonors ...machine.CPUSet,
) (machine.CPUSet, machine.CPUSet, error) {
	atomicDonorClosure, err := p.atomicDonorCoreClosure(atomicDonors...)
	if err != nil {
		return machine.NewCPUSet(), availableCPUs, err
	}
	return p.takeByTieredPreferredCPUsWithProtected(
		availableCPUs, preferredTiers, cpuRequirement, machine.NewCPUSet(), atomicDonorClosure)
}

func (p *DynamicPolicy) atomicDonorCoreClosure(atomicDonors ...machine.CPUSet) (machine.CPUSet, error) {
	closure := machine.NewCPUSet()
	for _, donor := range atomicDonors {
		donorClosure, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, donor)
		if err != nil {
			return machine.NewCPUSet(), err
		}
		closure = closure.Union(donorClosure)
	}
	return closure, nil
}

// takeByTieredPreferredCPUsWithProtected is the tiered taker with an explicit
// protected set and a precomputed atomic-donor core closure.
func (p *DynamicPolicy) takeByTieredPreferredCPUsWithProtected(
	availableCPUs machine.CPUSet,
	preferredTiers []machine.CPUSet,
	cpuRequirement int,
	protected machine.CPUSet,
	atomicDonorClosure machine.CPUSet,
) (machine.CPUSet, machine.CPUSet, error) {
	remaining := availableCPUs.Clone()
	taken := machine.NewCPUSet()
	respectsAtomicDonors := func(candidate machine.CPUSet) bool {
		touched := candidate.Intersection(atomicDonorClosure)
		if touched.IsEmpty() {
			return true
		}
		closure, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, touched)
		return err == nil && closure.IsSubsetOf(candidate)
	}

	if cpuRequirement <= 0 {
		return taken, remaining, nil
	}

	topology := p.machine.machineInfo.CPUTopology
	cpusPerCore := topology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return machine.NewCPUSet(), availableCPUs, fmt.Errorf(
			"take tiered preferred cpus failed: invalid cpus per core %d", cpusPerCore)
	}

	// Preserve an exact historical placement byte-for-byte. This keeps the
	// non-hard-partition reclaim path stable when its previous cpuset already
	// satisfies the complete request; prefer-whole applies when allocation must
	// combine or trim candidates. A borrowed/return tier's exact hit must never
	// take another pool's reserved own history; only the current pool's own tier
	// may match against protected-free candidates.
	for _, tier := range preferredTiers {
		candidate := tier.Intersection(remaining)
		if candidate.Size() != cpuRequirement || !respectsAtomicDonors(candidate) {
			continue
		}
		// Any exact hit overlapping another pool's reserved own history is
		// forbidden, including a borrowed/return tier that coincides with it.
		if !candidate.Intersection(protected).IsEmpty() {
			continue
		}
		return candidate, remaining.Difference(candidate), nil
	}

	// Keep allocation inside the preferred source domain when that domain can
	// satisfy the request by itself. Only an insufficient preferred domain may
	// borrow sibling or tail CPUs from the rest of availableCPUs. Reserved own
	// history of other pools is always excluded from selectable, whether the
	// preferred domain satisfies the request or not.
	preferredDomain := machine.NewCPUSet()
	for _, tier := range preferredTiers {
		preferredDomain = preferredDomain.Union(tier.Intersection(remaining))
	}
	selectable := remaining.Difference(protected)
	domainSet := preferredDomain.Difference(protected)
	// Only stay inside the preferred domain when the unprotected part of that
	// domain can itself satisfy the request; otherwise spill from the remaining
	// available set minus protected. With no protected set this is identical to
	// the legacy preferred-domain behavior.
	if atomicDonorClosure.IsEmpty() && domainSet.Size() >= cpuRequirement {
		selectable = domainSet
	}

	preference := func(candidate machine.CPUSet) (int, int) {
		for rank, tier := range preferredTiers {
			if hits := candidate.Intersection(tier).Size(); hits > 0 {
				return rank, hits
			}
		}
		return len(preferredTiers), 0
	}

	type rankedCandidate struct {
		cpus    machine.CPUSet
		numaID  int
		rank    int
		hits    int
		id      int
		coreKey physicalCoreKey
	}

	// Build every complete core once. Preferred orphan siblings contribute their
	// tier rank and are completed from selectable when the source domain allows it.
	cpusByCore := make(map[physicalCoreKey]machine.CPUSet)
	for _, cpu := range selectable.ToSliceInt() {
		info, ok := topology.CPUDetails[cpu]
		if !ok {
			continue
		}
		key := physicalCoreKeyForCPU(info)
		coreCPUs := cpusByCore[key]
		if !coreCPUs.Initialed {
			coreCPUs = machine.NewCPUSet()
		}
		coreCPUs.Add(cpu)
		cpusByCore[key] = coreCPUs
	}

	coreCandidates := make([]rankedCandidate, 0, len(cpusByCore))
	for coreKey, coreCPUs := range cpusByCore {
		if coreCPUs.Size() != cpusPerCore {
			continue
		}
		firstCPU := coreCPUs.ToSliceInt()[0]
		rank, hits := preference(coreCPUs)
		coreCandidates = append(coreCandidates, rankedCandidate{
			cpus: coreCPUs, numaID: topology.CPUDetails[firstCPU].NUMANodeID,
			rank: rank, hits: hits, id: coreKey.coreID, coreKey: coreKey,
		})
	}

	numaLoad := make(map[int]int)
	better := func(left, right rankedCandidate) bool {
		if numaLoad[left.numaID] != numaLoad[right.numaID] {
			return numaLoad[left.numaID] < numaLoad[right.numaID]
		}
		if left.rank != right.rank {
			return left.rank < right.rank
		}
		if left.hits != right.hits {
			return left.hits > right.hits
		}
		if left.coreKey != right.coreKey {
			return physicalCoreKeyLess(left.coreKey, right.coreKey)
		}
		return left.id < right.id
	}

	coresWanted := cpuRequirement / cpusPerCore
	for coresWanted > 0 && len(coreCandidates) > 0 {
		best := 0
		for i := 1; i < len(coreCandidates); i++ {
			if better(coreCandidates[i], coreCandidates[best]) {
				best = i
			}
		}
		selected := coreCandidates[best]
		taken = taken.Union(selected.cpus)
		remaining = remaining.Difference(selected.cpus)
		selectable = selectable.Difference(selected.cpus)
		numaLoad[selected.numaID] += selected.cpus.Size()
		coreCandidates = append(coreCandidates[:best], coreCandidates[best+1:]...)
		coresWanted--
	}

	// Fill the exact sub-core tail with the same cumulative NUMA load. NUMA
	// balance is primary; tier order and logical CPU ID break equal-load ties.
	// A tail may not take a sibling from an atomic donor core: donor CPUs can
	// move only through the complete-core loop above.
	if !atomicDonorClosure.IsEmpty() {
		selectable = remaining.Difference(protected).Difference(atomicDonorClosure)
	}
	for taken.Size() < cpuRequirement && !selectable.IsEmpty() {
		cpuCandidates := make([]rankedCandidate, 0, selectable.Size())
		for _, cpu := range selectable.ToSliceInt() {
			info, ok := topology.CPUDetails[cpu]
			if !ok {
				continue
			}
			cpuSet := machine.NewCPUSet(cpu)
			rank, hits := preference(cpuSet)
			cpuCandidates = append(cpuCandidates, rankedCandidate{
				cpus: cpuSet, numaID: info.NUMANodeID, rank: rank, hits: hits, id: cpu,
			})
		}
		if len(cpuCandidates) == 0 {
			break
		}

		best := 0
		for i := 1; i < len(cpuCandidates); i++ {
			if better(cpuCandidates[i], cpuCandidates[best]) {
				best = i
			}
		}
		selected := cpuCandidates[best]
		taken = taken.Union(selected.cpus)
		remaining = remaining.Difference(selected.cpus)
		selectable = selectable.Difference(selected.cpus)
		numaLoad[selected.numaID]++
	}

	if taken.Size() < cpuRequirement {
		return machine.NewCPUSet(), availableCPUs, fmt.Errorf(
			"take tiered preferred cpus failed: not enough cpus available to satisfy request: "+
				"available=%s taken=%s atomic-donor-closure=%s",
			availableCPUs.String(), taken.String(), atomicDonorClosure.String())
	}

	return taken, remaining, nil
}

type poolPlacementSnapshot struct {
	borrowedByPool       map[string]machine.CPUSet
	dedicatedByContainer map[string]map[string]machine.CPUSet
	ownByPool            map[string]machine.CPUSet
	historicalReclaim    machine.CPUSet
}

func buildPoolPlacementSnapshot(
	entries state.PodEntries,
	numaResourcePackagePinnedCPUSet map[int]map[string]machine.CPUSet,
	requestedPools map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet,
	includeReclaim bool,
) *poolPlacementSnapshot {
	snapshot := &poolPlacementSnapshot{
		borrowedByPool:       make(map[string]machine.CPUSet),
		dedicatedByContainer: make(map[string]map[string]machine.CPUSet),
		ownByPool:            make(map[string]machine.CPUSet),
		historicalReclaim:    machine.NewCPUSet(),
	}
	for podUID, containerEntries := range entries {
		if allocationInfo := containerEntries[commonstate.FakedContainerName]; allocationInfo != nil {
			if podUID == commonstate.PoolNameReclaim && includeReclaim {
				snapshot.historicalReclaim = allocationInfo.AllocationResult.Intersection(availableCPUs)
				if !snapshot.historicalReclaim.IsEmpty() {
					snapshot.borrowedByPool[commonstate.PoolNameReclaim] = snapshot.historicalReclaim.Clone()
				}
			} else if podUID != commonstate.PoolNameShare {
				if _, requested := requestedPools[podUID]; requested {
					if cset := allocationInfo.AllocationResult.Intersection(availableCPUs); !cset.IsEmpty() {
						snapshot.ownByPool[podUID] = cset
					}
				}
			}
		}
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || allocationInfo.AllocationResult.IsEmpty() {
				continue
			}
			if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelSharedCores &&
				commonstate.IsIsolationPool(allocationInfo.GetOwnerPoolName()) {
				var sourcePool string
				if allocationInfo.CheckSharedNUMABinding() {
					sourceInfo := allocationInfo.Clone()
					sourceInfo.OwnerPoolName = commonstate.EmptyOwnerPoolName
					canonical, _, err := state.GetCanonicalSharedNUMABindingPoolKey(
						numaResourcePackagePinnedCPUSet, sourceInfo)
					if err == nil {
						sourcePool = canonical
					}
				} else if derived, ok := deriveIsolationSourceSharePool(allocationInfo); ok {
					sourcePool = derived
				}
				if _, requested := requestedPools[sourcePool]; requested {
					if cset := allocationInfo.AllocationResult.Intersection(availableCPUs); !cset.IsEmpty() {
						snapshot.borrowedByPool[sourcePool] = snapshot.borrowedByPool[sourcePool].Union(cset)
					}
				}
			}
			if allocationInfo.CheckDedicated() && !allocationInfo.CheckNUMABinding() {
				cset := allocationInfo.AllocationResult.Intersection(availableCPUs)
				if cset.IsEmpty() {
					continue
				}
				if _, requested := requestedPools[commonstate.PoolNameShare]; requested {
					snapshot.borrowedByPool[commonstate.PoolNameShare] = snapshot.borrowedByPool[commonstate.PoolNameShare].Union(cset)
				}
				if containers := isolatedQuantityMap[podUID]; containers != nil {
					if _, requested := containers[containerName]; requested {
						if snapshot.dedicatedByContainer[podUID] == nil {
							snapshot.dedicatedByContainer[podUID] = make(map[string]machine.CPUSet)
						}
						snapshot.dedicatedByContainer[podUID][containerName] = cset
					}
				}
			}
		}
	}
	return snapshot
}

// assemblePoolPreferredTiers orders each pool's OWN frozen history as tier 0 and
// appends borrowed isolation / dedicated-return preferences as later tiers. The
// own tier stays a separate entry so the exact-match phase can reuse the
// historical cpuset byte-for-byte instead of matching a diluted union.
func assemblePoolPreferredTiers(
	ownPreferredCPUsByPool, borrowedPreferredCPUsByPool map[string]machine.CPUSet,
) map[string][]machine.CPUSet {
	tiers := make(map[string][]machine.CPUSet)
	for poolName, cset := range ownPreferredCPUsByPool {
		if cset.IsEmpty() {
			continue
		}
		tiers[poolName] = append(tiers[poolName], cset.Clone())
	}
	for poolName, cset := range borrowedPreferredCPUsByPool {
		if cset.IsEmpty() {
			continue
		}
		tiers[poolName] = append(tiers[poolName], cset)
	}
	return tiers
}

// takeCPUsForPoolsInPlaceWithTiers behaves like takeCPUsForPoolsInPlace, but for
// pools that carry ordered preference tiers it takes from tier 0 (the pool's OWN
// frozen historical placement) first and only spills to later borrowed tiers
// and the remaining available cpus afterwards. Pools without a tier fall back to
// the plain NUMA-balanced take, so behavior is unchanged for them.
func (p *DynamicPolicy) takeCPUsForPoolsInPlaceWithTiers(
	poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
	preferredTiersByPool map[string][]machine.CPUSet,
	ownHistoryByPool map[string]machine.CPUSet,
	atomicDonors ...machine.CPUSet,
) (machine.CPUSet, error) {
	originalAvailableCPUSet := availableCPUs.Clone()

	sortedPoolNames := general.GetSortedMapKeys(poolsQuantityMap)
	sort.SliceStable(sortedPoolNames, func(i, j int) bool {
		leftPoolName := sortedPoolNames[i]
		rightPoolName := sortedPoolNames[j]
		leftPreferred := len(preferredTiersByPool[leftPoolName]) > 0
		rightPreferred := len(preferredTiersByPool[rightPoolName]) > 0
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if leftPreferred && rightPreferred {
			if leftPoolName == commonstate.PoolNameReclaim && rightPoolName != commonstate.PoolNameReclaim {
				return true
			}
			if rightPoolName == commonstate.PoolNameReclaim && leftPoolName != commonstate.PoolNameReclaim {
				return false
			}
		}
		return leftPoolName < rightPoolName
	})

	atomicDonorClosure, err := p.atomicDonorCoreClosure(atomicDonors...)
	if err != nil {
		return originalAvailableCPUSet, err
	}

	// Per-pool own-history reservation. Only a pool's own committed history may be
	// reserved: borrowed/return tiers (default share, reclaim) are never reserved,
	// so their legacy strategy is untouched. A history reserves only when it is an
	// exact complete request, respects the atomic donor closure, and stays disjoint
	// from an already-reserved winner. Overlapping own histories are mutually
	// exclusive: the first sorted winner reserves, the overlapping loser does not
	// reserve and falls back to soft spill rather than blindly double-reserving.
	ownReservation := make(map[string]machine.CPUSet)
	reservedTaken := machine.NewCPUSet()
	for _, name := range sortedPoolNames {
		own := ownHistoryByPool[name]
		if own.IsEmpty() {
			continue
		}
		ownExact := own.Intersection(availableCPUs)
		if ownExact.Size() != poolsQuantityMap[name] {
			continue
		}
		if !p.validReservedOwnExact(ownExact, atomicDonorClosure) {
			continue
		}
		if !ownExact.Intersection(reservedTaken).IsEmpty() {
			continue
		}
		ownReservation[name] = ownExact
		reservedTaken = reservedTaken.Union(ownExact)
	}

	for _, poolName := range sortedPoolNames {
		if _, found := poolsCPUSet[poolName]; found {
			return originalAvailableCPUSet, fmt.Errorf("duplicated pool: %s", poolName)
		}

		req := poolsQuantityMap[poolName]

		// Protected = other winners' reserved own history only. reservedTaken is
		// the disjoint union of winners, so subtracting this pool's own reservation
		// avoids a per-pool O(P²) union scan; the current pool's own candidate is
		// never protected against itself.
		protected := reservedTaken.Difference(ownReservation[poolName])

		cset, remaining, err := p.takeByTieredPreferredCPUsWithProtected(
			availableCPUs, preferredTiersByPool[poolName], req, protected, atomicDonorClosure)
		if err != nil {
			return originalAvailableCPUSet, fmt.Errorf("take cpu for pool: %s of req: %d failed with error: %v",
				poolName, req, err)
		}

		poolsCPUSet[poolName] = cset
		availableCPUs = remaining
	}

	return availableCPUs, nil
}

// validReservedOwnExact checks a candidate own history that we intend to reserve:
// it must respect the atomic donor closure (a donor may only be touched through a
// complete core) and decompose into complete hardware cores. An invalid partial
// history is not reserved and simply falls back to soft spill.
func (p *DynamicPolicy) validReservedOwnExact(candidate, atomicDonorClosure machine.CPUSet) bool {
	touched := candidate.Intersection(atomicDonorClosure)
	if !touched.IsEmpty() {
		touchedClosure, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, touched)
		if err != nil || !touchedClosure.IsSubsetOf(candidate) {
			return false
		}
	}
	complete, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, candidate)
	return err == nil && complete.Equals(candidate)
}

// generateProportionalPoolsCPUSetInPlaceWithTiers mirrors
// generateProportionalPoolsCPUSetInPlace but consumes ordered preference tiers
// (own history first) when the proportional requests can be satisfied disjointly.
// If available CPUs cannot satisfy even one CPU per pool, it preserves the legacy
// shared-available behavior.
func (p *DynamicPolicy) generateProportionalPoolsCPUSetInPlaceWithTiers(
	poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
	preferredTiersByPool map[string][]machine.CPUSet,
	ownHistoryByPool map[string]machine.CPUSet,
	atomicDonors ...machine.CPUSet,
) (machine.CPUSet, error) {
	availableSize := availableCPUs.Size()

	proportionalPoolsQuantityMap, totalProportionalPoolsQuantity := getProportionalPoolsQuantityMap(poolsQuantityMap, availableSize)

	general.Infof("poolsQuantityMap: %v, proportionalPoolsQuantityMap: %v", poolsQuantityMap, proportionalPoolsQuantityMap)

	if totalProportionalPoolsQuantity > availableSize {
		for poolName := range poolsQuantityMap {
			if _, found := poolsCPUSet[poolName]; found {
				return availableCPUs.Clone(), fmt.Errorf("duplicated pool: %s", poolName)
			}

			poolsCPUSet[poolName] = availableCPUs.Clone()
		}

		return machine.NewCPUSet(), nil
	}

	return p.takeCPUsForPoolsInPlaceWithTiers(
		proportionalPoolsQuantityMap, poolsCPUSet, availableCPUs, preferredTiersByPool, ownHistoryByPool, atomicDonors...)
}

// takeCPUsForContainersWithPreferred first tries to reuse the preferred cpuset for
// each pod/container and then fills the remaining request from available CPUs with
// NUMA balancing.
func (p *DynamicPolicy) takeCPUsForContainersWithPreferred(
	containersQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet,
	preferredCPUsByContainer map[string]map[string]machine.CPUSet,
	atomicDonors ...machine.CPUSet,
) (map[string]map[string]machine.CPUSet, machine.CPUSet, error) {
	containersCPUSet := make(map[string]map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	sortedPodUIDs := make([]string, 0, len(containersQuantityMap))
	for podUID := range containersQuantityMap {
		sortedPodUIDs = append(sortedPodUIDs, podUID)
	}
	sort.Strings(sortedPodUIDs)
	for _, podUID := range sortedPodUIDs {
		containerQuantities := containersQuantityMap[podUID]
		if len(containerQuantities) > 0 {
			containersCPUSet[podUID] = make(map[string]machine.CPUSet)
		}

		sortedContainerNames := general.GetSortedMapKeys(containerQuantities)
		for _, containerName := range sortedContainerNames {
			quantity := containerQuantities[containerName]
			general.Infof("allocated for pod: %s container: %s with req: %d", podUID, containerName, quantity)

			var preferredTiers []machine.CPUSet
			if preferredCPUsByContainer != nil && preferredCPUsByContainer[podUID] != nil {
				if preferred := preferredCPUsByContainer[podUID][containerName]; !preferred.IsEmpty() {
					preferredTiers = []machine.CPUSet{preferred}
				}
			}

			cset, remaining, err := p.takeByTieredPreferredCPUs(
				availableCPUs, preferredTiers, quantity, atomicDonors...)
			if err != nil {
				return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pod: %s container: %s of req: %d failed with error: %v",
					podUID, containerName, quantity, err)
			}
			containersCPUSet[podUID][containerName] = cset
			availableCPUs = remaining
		}
	}

	return containersCPUSet, availableCPUs, nil
}
