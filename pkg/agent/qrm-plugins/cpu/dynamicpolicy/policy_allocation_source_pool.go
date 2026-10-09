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
// available CPUs. It returns the taken set and the remaining available set.
// This is the shared building block that lets isolation cpusets be carved from
// their source share pool region and recycled back to it across recomputes.
func (p *DynamicPolicy) takeByTieredPreferredCPUs(
	availableCPUs machine.CPUSet,
	preferredTiers []machine.CPUSet,
	cpuRequirement int,
	atomicDonors ...machine.CPUSet,
) (machine.CPUSet, machine.CPUSet, error) {
	remaining := availableCPUs.Clone()
	taken := machine.NewCPUSet()
	atomicDonorClosure := machine.NewCPUSet()
	for _, donor := range atomicDonors {
		closure, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, donor)
		if err != nil {
			return machine.NewCPUSet(), availableCPUs, err
		}
		atomicDonorClosure = atomicDonorClosure.Union(closure)
	}
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
	// combine or trim candidates.
	for _, tier := range preferredTiers {
		candidate := tier.Intersection(remaining)
		if candidate.Size() == cpuRequirement && respectsAtomicDonors(candidate) {
			return candidate, remaining.Difference(candidate), nil
		}
	}

	// Keep allocation inside the preferred source domain when that domain can
	// satisfy the request by itself. Only an insufficient preferred domain may
	// borrow sibling or tail CPUs from the rest of availableCPUs.
	preferredDomain := machine.NewCPUSet()
	for _, tier := range preferredTiers {
		preferredDomain = preferredDomain.Union(tier.Intersection(remaining))
	}
	selectable := remaining.Clone()
	if atomicDonorClosure.IsEmpty() && preferredDomain.Size() >= cpuRequirement {
		selectable = preferredDomain
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
		selectable = remaining.Difference(atomicDonorClosure)
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

// buildIsolationSourcePreferredCPUs scans the current pod entries and, for every
// shared_cores isolation container whose source share pool can be derived, records
// its current cpuset under that source pool. The resulting map lets the source share
// pool preferentially reclaim exactly those cpus when the isolation shrinks, is deleted,
// or the container returns to the share pool, keeping cpuset churn minimal. Entries whose
// source cannot be derived are skipped so the legacy behavior is preserved for them.
func buildIsolationSourcePreferredCPUs(entries state.PodEntries) map[string]machine.CPUSet {
	preferred := make(map[string]machine.CPUSet)

	for _, containerEntries := range entries {
		// pool entries themselves are not isolation containers; skip them.
		if containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			if !commonstate.IsIsolationPool(allocationInfo.GetOwnerPoolName()) {
				continue
			}

			sourcePool, ok := deriveIsolationSourceSharePool(allocationInfo)
			if !ok {
				continue
			}

			existing := preferred[sourcePool]
			preferred[sourcePool] = existing.Union(allocationInfo.AllocationResult)
		}
	}

	return preferred
}

// buildDedicatedSourcePreferredCPUs scans the current pod entries and records
// non-NUMA-binding dedicated_cores allocations as soft preferences for the next
// recompute. The container preference lets an unchanged dedicated container reuse
// its historical cpuset, while the share-pool preference lets those CPUs flow back
// to the default share pool first when the dedicated container shrinks or disappears.
//
// NUMA-binding dedicated allocations keep the legacy path for now because their
// source pool semantics are NUMA-scoped and need a separate design.
func buildDedicatedSourcePreferredCPUs(entries state.PodEntries) (
	map[string]machine.CPUSet,
	map[string]map[string]machine.CPUSet,
) {
	preferredByPool := make(map[string]machine.CPUSet)
	preferredByContainer := make(map[string]map[string]machine.CPUSet)

	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			if !allocationInfo.CheckDedicated() || allocationInfo.CheckNUMABinding() {
				continue
			}
			if allocationInfo.AllocationResult.IsEmpty() {
				continue
			}

			preferredByPool[commonstate.PoolNameShare] = preferredByPool[commonstate.PoolNameShare].Union(allocationInfo.AllocationResult)
			if preferredByContainer[podUID] == nil {
				preferredByContainer[podUID] = make(map[string]machine.CPUSet)
			}
			preferredByContainer[podUID][containerName] = allocationInfo.AllocationResult.Clone()
		}
	}

	return preferredByPool, preferredByContainer
}

func buildHistoricalPoolEntryPreferredCPUs(entries state.PodEntries, poolNames ...string) map[string]machine.CPUSet {
	preferred := make(map[string]machine.CPUSet)
	for _, poolName := range poolNames {
		if entries[poolName] == nil {
			continue
		}
		allocationInfo := entries[poolName][commonstate.FakedContainerName]
		if allocationInfo == nil || allocationInfo.AllocationResult.IsEmpty() {
			continue
		}
		preferred[poolName] = allocationInfo.AllocationResult.Clone()
	}
	return preferred
}

// takeCPUsForPoolsInPlaceWithPreferred behaves like takeCPUsForPoolsInPlace, but for pools
// that carry a preferred cpuset (their historically carved isolation cpus) it takes from that
// preferred set first and only spills to the remaining available cpus afterwards. Pools without
// a preferred set fall back to the plain NUMA-balanced take, so behavior is unchanged for them.
func (p *DynamicPolicy) takeCPUsForPoolsInPlaceWithPreferred(
	poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
	preferredCPUsByPool map[string]machine.CPUSet,
	atomicDonors ...machine.CPUSet,
) (machine.CPUSet, error) {
	originalAvailableCPUSet := availableCPUs.Clone()

	sortedPoolNames := general.GetSortedMapKeys(poolsQuantityMap)
	sort.SliceStable(sortedPoolNames, func(i, j int) bool {
		leftPoolName := sortedPoolNames[i]
		rightPoolName := sortedPoolNames[j]
		leftPreferred := preferredCPUsByPool != nil && !preferredCPUsByPool[leftPoolName].IsEmpty()
		rightPreferred := preferredCPUsByPool != nil && !preferredCPUsByPool[rightPoolName].IsEmpty()
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
	for _, poolName := range sortedPoolNames {
		if _, found := poolsCPUSet[poolName]; found {
			return originalAvailableCPUSet, fmt.Errorf("duplicated pool: %s", poolName)
		}

		req := poolsQuantityMap[poolName]

		var preferredTiers []machine.CPUSet
		if preferred, ok := preferredCPUsByPool[poolName]; ok && !preferred.IsEmpty() {
			preferredTiers = []machine.CPUSet{preferred}
		}

		cset, remaining, err := p.takeByTieredPreferredCPUs(
			availableCPUs, preferredTiers, req, atomicDonors...)
		if err != nil {
			return originalAvailableCPUSet, fmt.Errorf("take cpu for pool: %s of req: %d failed with error: %v",
				poolName, req, err)
		}

		poolsCPUSet[poolName] = cset
		availableCPUs = remaining
	}

	return availableCPUs, nil
}

// generateProportionalPoolsCPUSetInPlaceWithPreferred mirrors
// generateProportionalPoolsCPUSetInPlace but uses preferred-aware taking when the
// proportional requests can be satisfied disjointly. If available CPUs cannot satisfy
// even one CPU per pool, it preserves the legacy shared-available behavior.
func (p *DynamicPolicy) generateProportionalPoolsCPUSetInPlaceWithPreferred(
	poolsQuantityMap map[string]int,
	poolsCPUSet map[string]machine.CPUSet,
	availableCPUs machine.CPUSet,
	preferredCPUsByPool map[string]machine.CPUSet,
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

	return p.takeCPUsForPoolsInPlaceWithPreferred(
		proportionalPoolsQuantityMap, poolsCPUSet, availableCPUs, preferredCPUsByPool, atomicDonors...)
}

// takeCPUsForContainersWithPreferred follows the same semantics as takeCPUsForContainers, but
// first tries to reuse the preferred cpuset for each pod/container and then fills the remaining
// request from available CPUs with NUMA balancing.
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
