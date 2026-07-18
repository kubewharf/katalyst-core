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

package utils

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type CPUSetPartitionView struct {
	Reserve                 machine.CPUSet
	Dedicated               machine.CPUSet
	ReclaimRaw              machine.CPUSet
	SharePool               machine.CPUSet
	SharePoolMap            map[string]machine.CPUSet
	Isolation               machine.CPUSet
	NonReclaimPool          machine.CPUSet
	ReclaimEffective        machine.CPUSet
	ReclaimEffectivePerNUMA map[int]machine.CPUSet
	ContainerCPUSetByPod    map[string]map[string]machine.CPUSet
}

type CPUSetPartitionViewOptions struct {
	NonReclaimPoolMinSize int64
	ReserveCPUReversely   bool
}

func BuildCPUSetPartitionView(state cpustate.ReadonlyState, topology *machine.CPUTopology, opts CPUSetPartitionViewOptions) *CPUSetPartitionView {
	view := &CPUSetPartitionView{
		Reserve:                 machine.NewCPUSet(),
		Dedicated:               machine.NewCPUSet(),
		ReclaimRaw:              machine.NewCPUSet(),
		SharePool:               machine.NewCPUSet(),
		SharePoolMap:            map[string]machine.CPUSet{},
		Isolation:               machine.NewCPUSet(),
		NonReclaimPool:          machine.NewCPUSet(),
		ReclaimEffective:        machine.NewCPUSet(),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{},
		ContainerCPUSetByPod:    map[string]map[string]machine.CPUSet{},
	}
	if state == nil || topology == nil {
		return view
	}

	allowOverlap := state.GetAllowSharedCoresOverlapReclaimedCores()
	podEntries := state.GetPodEntries()
	poolTypeOf := func(poolName string) string {
		return commonstate.GetPoolType(commonstate.OwnerPoolNameTranslator.Translate(poolName))
	}
	recordContainerCPUSet := func(podUID, containerName string, cpus machine.CPUSet) {
		if _, ok := view.ContainerCPUSetByPod[podUID]; !ok {
			view.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{}
		}
		view.ContainerCPUSetByPod[podUID][containerName] = cpus.Clone()
	}

	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation != nil {
				recordContainerCPUSet(allocation.PodUid, allocation.ContainerName, allocation.AllocationResult)
			}
		}
	}

	for poolName, containerEntries := range podEntries {
		if !containerEntries.IsPoolEntry() {
			continue
		}
		entry := containerEntries[commonstate.FakedContainerName]
		if entry == nil {
			continue
		}
		switch poolTypeOf(poolName) {
		case commonstate.PoolNameReserve:
			view.Reserve = view.Reserve.Union(entry.AllocationResult)
		case commonstate.PoolNameShare:
			view.SharePool = view.SharePool.Union(entry.AllocationResult)
			view.SharePoolMap[poolName] = entry.AllocationResult.Clone()
		case commonstate.PoolNamePrefixIsolation:
			view.Isolation = view.Isolation.Union(entry.AllocationResult)
		}
	}

	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation != nil && allocation.CheckDedicated() {
				view.Dedicated = view.Dedicated.Union(allocation.AllocationResult)
			}
		}
	}

	if reclaimEntries, ok := podEntries[commonstate.PoolNameReclaim]; ok {
		if entry := reclaimEntries[commonstate.FakedContainerName]; entry != nil {
			view.ReclaimRaw = entry.AllocationResult.Clone()
		}
	}
	if allowOverlap && !view.ReclaimRaw.IsEmpty() {
		view.SharePool = view.SharePool.Difference(view.ReclaimRaw)
		for poolName, cpus := range view.SharePoolMap {
			view.SharePoolMap[poolName] = cpus.Difference(view.ReclaimRaw)
		}
	}
	view.NonReclaimPool = view.SharePool.Union(view.Dedicated).Union(view.Isolation)
	if allowOverlap {
		view.ReclaimEffective = view.ReclaimRaw.Clone()
	} else {
		view.ReclaimEffective = topology.CPUDetails.CPUs().Difference(view.NonReclaimPool).Difference(view.Reserve)
		padNonReclaimPoolToMinSize(view, topology, opts)
	}
	rebuildReclaimEffectivePerNUMA(view, topology)
	return view
}

func padNonReclaimPoolToMinSize(view *CPUSetPartitionView, topology *machine.CPUTopology, opts CPUSetPartitionViewOptions) {
	if view == nil || topology == nil || opts.NonReclaimPoolMinSize <= 0 {
		return
	}
	currentSize := view.NonReclaimPool.Size()
	if currentSize >= int(opts.NonReclaimPoolMinSize) {
		return
	}
	deficit := int(opts.NonReclaimPoolMinSize) - currentSize
	candidates := view.ReclaimEffective.Clone()
	if candidates.IsEmpty() {
		return
	}
	if deficit > candidates.Size() {
		deficit = candidates.Size()
	}

	padding := takeCPUsByNUMABalanceWithSeed(topology, candidates, view.NonReclaimPool, deficit, opts.ReserveCPUReversely)
	view.NonReclaimPool = view.NonReclaimPool.Union(padding)
	view.ReclaimEffective = view.ReclaimEffective.Difference(padding)
}

func takeCPUsByNUMABalanceWithSeed(topology *machine.CPUTopology, candidates, seed machine.CPUSet, count int, reverse bool) machine.CPUSet {
	if topology == nil || count <= 0 || candidates.IsEmpty() {
		return machine.NewCPUSet()
	}

	candidateByNUMA := map[int][]int{}
	currentCountByNUMA := map[int]int{}
	numaIDs := topology.CPUDetails.NUMANodes().ToSliceInt()
	for _, numaID := range numaIDs {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		currentCountByNUMA[numaID] = seed.Intersection(numaCPUs).Size()
		numaCandidates := candidates.Intersection(numaCPUs)
		if reverse {
			candidateByNUMA[numaID] = numaCandidates.ToSliceIntReversely()
		} else {
			candidateByNUMA[numaID] = numaCandidates.ToSliceInt()
		}
	}

	result := machine.NewCPUSet()
	for result.Size() < count {
		selectedNUMA := -1
		for _, numaID := range numaIDs {
			if len(candidateByNUMA[numaID]) == 0 {
				continue
			}
			if selectedNUMA == -1 || currentCountByNUMA[numaID] < currentCountByNUMA[selectedNUMA] {
				selectedNUMA = numaID
			}
		}
		if selectedNUMA == -1 {
			break
		}
		cpu := candidateByNUMA[selectedNUMA][0]
		candidateByNUMA[selectedNUMA] = candidateByNUMA[selectedNUMA][1:]
		result.Add(cpu)
		currentCountByNUMA[selectedNUMA]++
	}
	return result
}

func rebuildReclaimEffectivePerNUMA(view *CPUSetPartitionView, topology *machine.CPUTopology) {
	if view == nil {
		return
	}
	view.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{}
	if topology == nil {
		return
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		intersection := view.ReclaimEffective.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		if !intersection.IsEmpty() {
			view.ReclaimEffectivePerNUMA[numaID] = intersection
		}
	}
}

func (v *CPUSetPartitionView) DeepCopy() *CPUSetPartitionView {
	if v == nil {
		return nil
	}
	out := &CPUSetPartitionView{
		Reserve:                 v.Reserve.Clone(),
		Dedicated:               v.Dedicated.Clone(),
		ReclaimRaw:              v.ReclaimRaw.Clone(),
		SharePool:               v.SharePool.Clone(),
		SharePoolMap:            map[string]machine.CPUSet{},
		Isolation:               v.Isolation.Clone(),
		NonReclaimPool:          v.NonReclaimPool.Clone(),
		ReclaimEffective:        v.ReclaimEffective.Clone(),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{},
		ContainerCPUSetByPod:    map[string]map[string]machine.CPUSet{},
	}
	for numaID, cpus := range v.ReclaimEffectivePerNUMA {
		out.ReclaimEffectivePerNUMA[numaID] = cpus.Clone()
	}
	for poolName, cpus := range v.SharePoolMap {
		out.SharePoolMap[poolName] = cpus.Clone()
	}
	for podUID, containers := range v.ContainerCPUSetByPod {
		out.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{}
		for containerName, cpus := range containers {
			out.ContainerCPUSetByPod[podUID][containerName] = cpus.Clone()
		}
	}
	return out
}

func EqualCPUSetPartitionView(a, b *CPUSetPartitionView) bool {
	if a == nil || b == nil {
		return a == b
	}
	// ContainerCPUSetByPod is intentionally excluded: leaf-only allocation
	// changes must not trigger the expensive cpuset topology reconciliation when
	// all aggregate partition CPU sets remain unchanged.
	return a.Reserve.Equals(b.Reserve) &&
		a.Dedicated.Equals(b.Dedicated) &&
		a.SharePool.Equals(b.SharePool) &&
		equalStringCPUSetMap(a.SharePoolMap, b.SharePoolMap) &&
		a.NonReclaimPool.Equals(b.NonReclaimPool) &&
		a.ReclaimEffective.Equals(b.ReclaimEffective) &&
		equalCPUSetMap(a.ReclaimEffectivePerNUMA, b.ReclaimEffectivePerNUMA)
}

func equalCPUSetMap(a, b map[int]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !av.Equals(bv) {
			return false
		}
	}
	return true
}

func equalStringCPUSetMap(a, b map[string]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !av.Equals(bv) {
			return false
		}
	}
	return true
}
