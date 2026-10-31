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

package dynamicpolicy

import (
	"math"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type numaBindingAllocationPreference struct {
	currentRequestAllocation  machine.CPUSet
	reclaim                   machine.CPUSet
	snbAllocated              machine.CPUSet
	boundPreemptionCandidates []boundPreemptionCandidate
}

type boundPreemptionCandidate struct {
	podUID        string
	containerName string
	cpus          machine.CPUSet
}

func shouldUseNumaBindingAllocationPreference(allocationInfo *state.AllocationInfo) bool {
	return allocationInfo != nil && allocationInfo.CheckDedicatedNUMABindingNUMAExclusive()
}

func existingAllocationSatisfiesRequest(
	allocationInfo *state.AllocationInfo,
	reqInt int,
	allowShortNUMABindingAllocation bool,
) bool {
	if allocationInfo == nil {
		return false
	}
	if allocationInfo.OriginalAllocationResult.Size() >= reqInt {
		return true
	}
	if !allowShortNUMABindingAllocation {
		return false
	}
	if !allocationInfo.CheckDedicatedNUMABinding() || allocationInfo.OriginalAllocationResult.IsEmpty() {
		return false
	}
	return int(math.Ceil(allocationInfo.RequestQuantity)) == reqInt
}

func removePodOwnerEntriesForReallocation(podEntries state.PodEntries, podUID, containerName string) {
	containerEntries := podEntries[podUID]
	if containerEntries == nil {
		return
	}
	if len(containerEntries) <= 1 {
		delete(podEntries, podUID)
		return
	}

	current := containerEntries[containerName]
	if current == nil {
		delete(containerEntries, containerName)
		if len(containerEntries) == 0 {
			delete(podEntries, podUID)
		}
		return
	}
	currentCPUs := allocationCurrentCPUSet(current)
	if currentCPUs.IsEmpty() {
		delete(containerEntries, containerName)
		if len(containerEntries) == 0 {
			delete(podEntries, podUID)
		}
		return
	}
	for siblingName, sibling := range containerEntries {
		if siblingName == containerName {
			delete(containerEntries, siblingName)
			continue
		}
		if allocationCurrentCPUSet(sibling).Equals(currentCPUs) {
			delete(containerEntries, siblingName)
		}
	}
	if len(containerEntries) == 0 {
		delete(podEntries, podUID)
	}
}

func numaBindingAllocationPreferenceFromEntries(
	podEntries state.PodEntries, podUID, containerName string,
) *numaBindingAllocationPreference {
	preference := &numaBindingAllocationPreference{
		currentRequestAllocation: machine.NewCPUSet(),
		reclaim:                  machine.NewCPUSet(),
		snbAllocated:             machine.NewCPUSet(),
	}

	if podEntries[podUID] != nil {
		preference.currentRequestAllocation = allocationCurrentCPUSet(podEntries[podUID][containerName])
	}
	if reclaimEntries := podEntries[commonstate.PoolNameReclaim]; reclaimEntries != nil {
		preference.reclaim = allocationCurrentCPUSet(reclaimEntries[commonstate.FakedContainerName])
	}
	preference.snbAllocated = unionSharedNUMABindingAllocationCPUSet(podEntries)
	preference.boundPreemptionCandidates = boundPreemptionCandidatesBySize(podEntries, podUID, containerName)

	if preference.currentRequestAllocation.IsEmpty() &&
		preference.reclaim.IsEmpty() &&
		preference.snbAllocated.IsEmpty() &&
		len(preference.boundPreemptionCandidates) == 0 {
		return nil
	}
	return preference
}

func allocationCurrentCPUSet(allocationInfo *state.AllocationInfo) machine.CPUSet {
	if allocationInfo == nil {
		return machine.NewCPUSet()
	}
	if !allocationInfo.AllocationResult.IsEmpty() {
		return allocationInfo.AllocationResult.Clone()
	}
	return allocationInfo.OriginalAllocationResult.Clone()
}

func unionSharedNUMABindingAllocationCPUSet(podEntries state.PodEntries) machine.CPUSet {
	result := machine.NewCPUSet()
	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckSharedNUMABinding() {
				continue
			}
			result = result.Union(allocationCurrentCPUSet(allocationInfo))
		}
	}
	return result
}

func boundPreemptionCandidatesBySize(
	podEntries state.PodEntries,
	excludePodUID, excludeContainerName string,
) []boundPreemptionCandidate {
	candidates := make([]boundPreemptionCandidate, 0)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil ||
				(podUID == excludePodUID && containerName == excludeContainerName) ||
				(!allocationInfo.CheckDedicatedNUMABinding() && !allocationInfo.CheckSharedNUMABinding()) {
				continue
			}
			cpus := allocationCurrentCPUSet(allocationInfo)
			if cpus.IsEmpty() {
				continue
			}
			candidates = append(candidates, boundPreemptionCandidate{
				podUID:        podUID,
				containerName: containerName,
				cpus:          cpus,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].cpus.Size() != candidates[j].cpus.Size() {
			return candidates[i].cpus.Size() > candidates[j].cpus.Size()
		}
		if candidates[i].podUID != candidates[j].podUID {
			return candidates[i].podUID < candidates[j].podUID
		}
		return candidates[i].containerName < candidates[j].containerName
	})
	return candidates
}

func takeReclaimSupplementWithPreference(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	quantity int,
	preference *numaBindingAllocationPreference,
) machine.CPUSet {
	tiers := make([]machine.CPUSet, 0, 6)
	if preference != nil {
		tiers = appendNonEmptyCPUSet(tiers, preference.reclaim.Intersection(candidates))
		tiers = appendNonEmptyCPUSet(tiers, candidates.
			Difference(preference.snbAllocated).
			Difference(preference.currentRequestAllocation))
		for _, candidate := range orderBoundPreemptionCandidatesForCPUSet(
			topology, candidates, preference.boundPreemptionCandidates,
		) {
			tiers = appendNonEmptyCPUSet(tiers, candidate.cpus.Intersection(candidates))
		}
		tiers = appendNonEmptyCPUSet(tiers, candidates.Difference(preference.currentRequestAllocation))
		tiers = appendNonEmptyCPUSet(tiers, preference.currentRequestAllocation.Intersection(candidates))
	}
	tiers = appendNonEmptyCPUSet(tiers, candidates)
	return takeCoreAlignedCPUSetByTiers(topology, candidates, tiers, quantity)
}

func orderBoundPreemptionCandidatesForCPUSet(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	boundCandidates []boundPreemptionCandidate,
) []boundPreemptionCandidate {
	ordered := append([]boundPreemptionCandidate(nil), boundCandidates...)
	completeSize := func(candidate boundPreemptionCandidate) int {
		eligible := candidate.cpus.Intersection(candidates)
		size := 0
		for _, core := range coreAlignedCandidates(topology, eligible, machine.NewCPUSet()) {
			size += core.cpus.Size()
		}
		return size
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		leftComplete := completeSize(ordered[i])
		rightComplete := completeSize(ordered[j])
		if leftComplete != rightComplete {
			return leftComplete > rightComplete
		}
		if ordered[i].cpus.Size() != ordered[j].cpus.Size() {
			return ordered[i].cpus.Size() > ordered[j].cpus.Size()
		}
		if ordered[i].podUID != ordered[j].podUID {
			return ordered[i].podUID < ordered[j].podUID
		}
		return ordered[i].containerName < ordered[j].containerName
	})
	return ordered
}

func appendNonEmptyCPUSet(tiers []machine.CPUSet, cpus machine.CPUSet) []machine.CPUSet {
	if cpus.IsEmpty() {
		return tiers
	}
	return append(tiers, cpus)
}
