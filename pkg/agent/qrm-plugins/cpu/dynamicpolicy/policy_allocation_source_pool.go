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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
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

// deriveDedicatedSourceSharePool treats dedicated_cores without NUMA binding as sourced
// from the default share pool. dedicated_cores currently has no declared source share pool
// semantics, so phase 2 only covers this conservative non-NUMA-binding default source.
// NUMA-binding dedicated allocations keep the legacy path.
func deriveDedicatedSourceSharePool(allocationInfo *state.AllocationInfo) (string, bool) {
	if allocationInfo == nil {
		return "", false
	}
	if allocationInfo.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
		return "", false
	}
	if allocationInfo.CheckNUMABinding() {
		return "", false
	}
	return commonstate.PoolNameShare, true
}

// takeByTieredPreferredCPUs allocates cpuRequirement cpus from availableCPUs, preferring
// cpus from the ordered preferred tiers first (each intersected with availableCPUs), and
// only spilling to the remaining availableCPUs (via NUMA-balanced take) when the tiers are
// exhausted. It returns the taken set and the remaining available set. This is the shared
// building block that lets isolation cpusets be carved from their source share pool region
// and recycled back to it across recomputes.
func (p *DynamicPolicy) takeByTieredPreferredCPUs(
	availableCPUs machine.CPUSet,
	preferredTiers []machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, machine.CPUSet, error) {
	remaining := availableCPUs.Clone()
	taken := machine.NewCPUSet()

	if cpuRequirement <= 0 {
		return taken, remaining, nil
	}

	// consume the preferred tiers in order, only counting cpus still available.
	for _, tier := range preferredTiers {
		if taken.Size() >= cpuRequirement {
			break
		}
		candidate := tier.Intersection(remaining)
		if candidate.IsEmpty() {
			continue
		}

		need := cpuRequirement - taken.Size()
		var pick machine.CPUSet
		if candidate.Size() <= need {
			pick = candidate
		} else {
			var err error
			pick, _, err = calculator.TakeByNUMABalance(p.machineInfo, candidate, need)
			if err != nil {
				return machine.NewCPUSet(), availableCPUs, fmt.Errorf(
					"take preferred cpus failed with error: %v", err)
			}
		}

		taken = taken.Union(pick)
		remaining = remaining.Difference(pick)
	}

	// spill to the remaining available cpus if the preferred tiers were insufficient.
	if taken.Size() < cpuRequirement {
		need := cpuRequirement - taken.Size()
		pick, rest, err := calculator.TakeByNUMABalance(p.machineInfo, remaining, need)
		if err != nil {
			return machine.NewCPUSet(), availableCPUs, fmt.Errorf(
				"take fallback cpus of req: %d failed with error: %v", need, err)
		}
		taken = taken.Union(pick)
		remaining = rest
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

// buildDedicatedSourcePreferredCPUs collects historical cpusets for dedicated_cores without
// NUMA binding:
// 1. source-pool preferred cpus help the share pool reclaim CPUs first when dedicated shrinks,
//    disappears, or returns to share;
// 2. container preferred cpus let still-active dedicated containers reuse their own historical
//    cpuset and reduce churn.
func buildDedicatedSourcePreferredCPUs(entries state.PodEntries) (map[string]machine.CPUSet, map[string]map[string]machine.CPUSet) {
	poolPreferred := make(map[string]machine.CPUSet)
	containerPreferred := make(map[string]map[string]machine.CPUSet)

	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			sourcePool, ok := deriveDedicatedSourceSharePool(allocationInfo)
			if !ok {
				continue
			}

			poolPreferred[sourcePool] = poolPreferred[sourcePool].Union(allocationInfo.AllocationResult)
			if containerPreferred[podUID] == nil {
				containerPreferred[podUID] = make(map[string]machine.CPUSet)
			}
			containerPreferred[podUID][containerName] = allocationInfo.AllocationResult.Clone()
		}
	}

	return poolPreferred, containerPreferred
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
) (machine.CPUSet, error) {
	originalAvailableCPUSet := availableCPUs.Clone()

	sortedPoolNames := general.GetSortedMapKeys(poolsQuantityMap)
	sort.SliceStable(sortedPoolNames, func(i, j int) bool {
		leftPreferred := preferredCPUsByPool != nil && !preferredCPUsByPool[sortedPoolNames[i]].IsEmpty()
		rightPreferred := preferredCPUsByPool != nil && !preferredCPUsByPool[sortedPoolNames[j]].IsEmpty()
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		return sortedPoolNames[i] < sortedPoolNames[j]
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

		cset, remaining, err := p.takeByTieredPreferredCPUs(availableCPUs, preferredTiers, req)
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
		proportionalPoolsQuantityMap, poolsCPUSet, availableCPUs, preferredCPUsByPool)
}

// takeCPUsForContainersWithPreferred follows the same semantics as takeCPUsForContainers, but
// first tries to reuse the preferred cpuset for each pod/container and then fills the remaining
// request from available CPUs with NUMA balancing.
func (p *DynamicPolicy) takeCPUsForContainersWithPreferred(
	containersQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet,
	preferredCPUsByContainer map[string]map[string]machine.CPUSet,
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

			cset, remaining, err := p.takeByTieredPreferredCPUs(availableCPUs, preferredTiers, quantity)
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
