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
	Isolation               machine.CPUSet
	NonReclaimPool          machine.CPUSet
	ReclaimEffective        machine.CPUSet
	ReclaimEffectivePerNUMA map[int]machine.CPUSet
	ContainerCPUSetByPod    map[string]map[string]machine.CPUSet
}

func BuildCPUSetPartitionView(state cpustate.ReadonlyState, topology *machine.CPUTopology) *CPUSetPartitionView {
	view := &CPUSetPartitionView{
		Reserve:                 machine.NewCPUSet(),
		Dedicated:               machine.NewCPUSet(),
		ReclaimRaw:              machine.NewCPUSet(),
		SharePool:               machine.NewCPUSet(),
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
	}
	view.NonReclaimPool = view.SharePool.Union(view.Dedicated).Union(view.Isolation)
	if allowOverlap {
		view.ReclaimEffective = view.ReclaimRaw.Clone()
	} else {
		view.ReclaimEffective = topology.CPUDetails.CPUs().Difference(view.NonReclaimPool).Difference(view.Reserve)
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		intersection := view.ReclaimEffective.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		if !intersection.IsEmpty() {
			view.ReclaimEffectivePerNUMA[numaID] = intersection
		}
	}
	return view
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
		Isolation:               v.Isolation.Clone(),
		NonReclaimPool:          v.NonReclaimPool.Clone(),
		ReclaimEffective:        v.ReclaimEffective.Clone(),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{},
		ContainerCPUSetByPod:    map[string]map[string]machine.CPUSet{},
	}
	for numaID, cpus := range v.ReclaimEffectivePerNUMA {
		out.ReclaimEffectivePerNUMA[numaID] = cpus.Clone()
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
	return a.Reserve.Equals(b.Reserve) &&
		a.Dedicated.Equals(b.Dedicated) &&
		a.ReclaimRaw.Equals(b.ReclaimRaw) &&
		a.SharePool.Equals(b.SharePool) &&
		a.Isolation.Equals(b.Isolation) &&
		a.NonReclaimPool.Equals(b.NonReclaimPool) &&
		a.ReclaimEffective.Equals(b.ReclaimEffective) &&
		equalCPUSetMap(a.ReclaimEffectivePerNUMA, b.ReclaimEffectivePerNUMA) &&
		equalNestedCPUSetMap(a.ContainerCPUSetByPod, b.ContainerCPUSetByPod)
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

func equalNestedCPUSetMap(a, b map[string]map[string]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for podUID, aContainers := range a {
		bContainers, ok := b[podUID]
		if !ok || len(aContainers) != len(bContainers) {
			return false
		}
		for containerName, av := range aContainers {
			bv, ok := bContainers[containerName]
			if !ok || !av.Equals(bv) {
				return false
			}
		}
	}
	return true
}
