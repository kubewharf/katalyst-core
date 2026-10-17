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
	"fmt"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type coreAlignedCandidate struct {
	key          physicalCoreKey
	coreID       int
	cpus         machine.CPUSet
	preferredHit int
}

type physicalCoreKey struct {
	numaID   int
	socketID int
	coreID   int
}

func physicalCoreKeyForCPU(info machine.CPUTopoInfo) physicalCoreKey {
	return physicalCoreKey{
		numaID:   info.NUMANodeID,
		socketID: info.SocketID,
		coreID:   info.CoreID,
	}
}

// physicalCoreIndex is the precomputed, topology-derived decomposition of all
// logical CPUs into physical cores. byKey groups each core's SMT siblings; byCPU
// maps every logical CPU back to its core key for O(1) membership lookup. The
// index is a pure function of a process-static CPU topology, so a planning loop
// that needs core decomposition more than once builds it once (newPhysicalCoreIndex)
// and reuses it instead of re-scanning every logical CPU per call.
type physicalCoreIndex struct {
	byKey map[physicalCoreKey]machine.CPUSet
	byCPU map[int]physicalCoreKey
}

func newPhysicalCoreIndex(topology *machine.CPUTopology) physicalCoreIndex {
	index := physicalCoreIndex{
		byKey: make(map[physicalCoreKey]machine.CPUSet),
		byCPU: make(map[int]physicalCoreKey),
	}
	if topology == nil {
		return index
	}
	for cpu, info := range topology.CPUDetails {
		key := physicalCoreKeyForCPU(info)
		index.byCPU[cpu] = key
		siblings := index.byKey[key]
		if !siblings.Initialed {
			siblings = machine.NewCPUSet()
		}
		siblings.Add(cpu)
		index.byKey[key] = siblings
	}
	return index
}

// takeCoreAlignedCPUSet selects cpus from candidates in complete physical cores
// only. It picks up to quantity cpus, but a core is chosen only when every one
// of its topology siblings is present in candidates, so the returned set is
// always core-aligned (invariant B). quantity is cropped DOWN to a whole-core
// multiple: a request that is not a whole-core amount never pulls a lone SMT
// sibling — Tasks 1-3 make the reclaim demands core-aligned, so this crop is a
// defensive net rather than an expected path.
//
// Cores are ordered by how many of their siblings live in the prefer set
// (descending) so stability (the currently pinned reclaim cpuset) is kept before
// fresh cpus are pulled in, then by ascending CoreID for a deterministic
// tie-break. The sibling set is derived per physical core rather than from the
// machine-wide CPUsPerCore average, which also supports non-uniform SMT.
func takeCoreAlignedCPUSet(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	prefer machine.CPUSet,
	quantity int,
) machine.CPUSet {
	if quantity <= 0 || candidates.IsEmpty() || topology == nil {
		return machine.NewCPUSet()
	}
	completeCores := coreAlignedCandidates(topology, candidates, prefer)
	selected := machine.NewCPUSet()
	for _, core := range completeCores {
		if selected.Size()+core.cpus.Size() > quantity {
			continue
		}
		selected = selected.Union(core.cpus)
	}
	return selected
}

// takeCoreAlignedCPUSetByTiers selects complete physical cores in tier order.
// A core belongs to a tier only when all of its SMT siblings are in that tier;
// a partial-core hit never raises the core's priority.
func takeCoreAlignedCPUSetByTiers(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	preferredTiers []machine.CPUSet,
	quantity int,
) machine.CPUSet {
	if quantity <= 0 || candidates.IsEmpty() || topology == nil {
		return machine.NewCPUSet()
	}
	type tieredCoreCandidate struct {
		key  physicalCoreKey
		cpus machine.CPUSet
		tier int
	}

	completeCores := coreAlignedCandidates(topology, candidates, machine.NewCPUSet())
	tieredCores := make([]tieredCoreCandidate, 0, len(completeCores))
	for _, core := range completeCores {
		tier := len(preferredTiers)
		for tierIndex, preferred := range preferredTiers {
			if core.cpus.IsSubsetOf(preferred) {
				tier = tierIndex
				break
			}
		}
		tieredCores = append(tieredCores, tieredCoreCandidate{
			key:  core.key,
			cpus: core.cpus,
			tier: tier,
		})
	}
	sort.Slice(tieredCores, func(i, j int) bool {
		if tieredCores[i].tier != tieredCores[j].tier {
			return tieredCores[i].tier < tieredCores[j].tier
		}
		return physicalCoreKeyLess(tieredCores[i].key, tieredCores[j].key)
	})

	selected := machine.NewCPUSet()
	for _, core := range tieredCores {
		if selected.Size()+core.cpus.Size() > quantity {
			continue
		}
		selected = selected.Union(core.cpus)
	}
	return selected
}

// coreAlignedCandidates returns complete physical cores in deterministic
// tiered-preference order: cores with more siblings in prefer come first, then
// lower core IDs. Callers can use the individual candidates when a full core
// must be expressed as a solver constraint instead of being selected eagerly.
//
// This rebuilds the full physical-core index on every call. A loop that needs
// the index more than once over the same static topology should build it once
// with newPhysicalCoreIndex and call coreAlignedCandidatesWithIndex.
func coreAlignedCandidates(
	topology *machine.CPUTopology,
	candidates machine.CPUSet,
	prefer machine.CPUSet,
) []coreAlignedCandidate {
	if topology == nil {
		return nil
	}
	return coreAlignedCandidatesWithIndex(newPhysicalCoreIndex(topology), candidates, prefer)
}

// coreAlignedCandidatesWithIndex is coreAlignedCandidates but takes a precomputed
// physicalCoreIndex. The index is a pure function of the process-static CPU
// topology, so a caller iterating many NUMA domains builds it once and reuses it
// instead of re-scanning every logical CPU per call.
func coreAlignedCandidatesWithIndex(
	index physicalCoreIndex,
	candidates machine.CPUSet,
	prefer machine.CPUSet,
) []coreAlignedCandidate {
	if index.byKey == nil {
		return nil
	}
	cpusByCore := make(map[physicalCoreKey]machine.CPUSet)
	for _, cpu := range candidates.ToSliceInt() {
		key, ok := index.byCPU[cpu]
		if !ok {
			continue
		}
		set := cpusByCore[key]
		if !set.Initialed {
			set = machine.NewCPUSet()
		}
		set.Add(cpu)
		cpusByCore[key] = set
	}

	completeCores := make([]coreAlignedCandidate, 0, len(cpusByCore))
	for key, cpus := range cpusByCore {
		siblings, ok := index.byKey[key]
		if !ok || !cpus.Equals(siblings) {
			continue
		}
		completeCores = append(completeCores, coreAlignedCandidate{
			key:          key,
			coreID:       key.coreID,
			cpus:         cpus,
			preferredHit: cpus.Intersection(prefer).Size(),
		})
	}

	sort.Slice(completeCores, func(i, j int) bool {
		if completeCores[i].preferredHit != completeCores[j].preferredHit {
			return completeCores[i].preferredHit > completeCores[j].preferredHit
		}
		return physicalCoreKeyLess(completeCores[i].key, completeCores[j].key)
	})
	return completeCores
}

func physicalCoreCPUs(topology *machine.CPUTopology) map[physicalCoreKey]machine.CPUSet {
	return newPhysicalCoreIndex(topology).byKey
}

func physicalCoreKeyLess(left, right physicalCoreKey) bool {
	if left.numaID != right.numaID {
		return left.numaID < right.numaID
	}
	if left.socketID != right.socketID {
		return left.socketID < right.socketID
	}
	return left.coreID < right.coreID
}

func completeCoresForCPUSet(topology *machine.CPUTopology, cpus machine.CPUSet) (machine.CPUSet, error) {
	if topology == nil {
		return machine.NewCPUSet(), fmt.Errorf("cannot complete cores with nil cpu topology")
	}
	keys := make(map[physicalCoreKey]struct{}, cpus.Size())
	for _, cpu := range cpus.ToSliceInt() {
		info, ok := topology.CPUDetails[cpu]
		if !ok {
			return machine.NewCPUSet(), fmt.Errorf("cpu %d has no topology metadata", cpu)
		}
		keys[physicalCoreKeyForCPU(info)] = struct{}{}
	}
	completed := machine.NewCPUSet()
	for cpu, info := range topology.CPUDetails {
		if _, ok := keys[physicalCoreKeyForCPU(info)]; ok {
			completed.Add(cpu)
		}
	}
	return completed, nil
}

func completeEligibleCoresForPreferredCPUSet(
	topology *machine.CPUTopology,
	eligible machine.CPUSet,
	prefer machine.CPUSet,
) (machine.CPUSet, error) {
	if topology == nil {
		return machine.NewCPUSet(), fmt.Errorf("cannot select eligible preferred cores with nil cpu topology")
	}
	selected := machine.NewCPUSet()
	for _, core := range coreAlignedCandidates(topology, eligible, prefer) {
		if core.preferredHit == 0 {
			break
		}
		selected = selected.Union(core.cpus)
	}
	return selected, nil
}

// assertCoreAligned is a fail-loud safety net: it returns a lowercase error when
// reclaim holds a partial physical core. It never repairs silently; a violation signals an upstream
// invariant break (quantity/reserve/selection) that must be surfaced, not masked.
func assertCoreAligned(reclaim machine.CPUSet, topology *machine.CPUTopology) error {
	if topology == nil {
		return fmt.Errorf("cannot assert core alignment with nil cpu topology")
	}
	if reclaim.IsEmpty() {
		return nil
	}

	cpusByCore := make(map[physicalCoreKey]machine.CPUSet)
	orphanCPUs := machine.NewCPUSet()
	for _, cpu := range reclaim.ToSliceInt() {
		info, ok := topology.CPUDetails[cpu]
		if !ok {
			orphanCPUs.Add(cpu)
			continue
		}
		key := physicalCoreKeyForCPU(info)
		cpus := cpusByCore[key]
		if !cpus.Initialed {
			cpus = machine.NewCPUSet()
		}
		cpus.Add(cpu)
		cpusByCore[key] = cpus
	}
	if !orphanCPUs.IsEmpty() {
		return fmt.Errorf("reclaim set %s contains cpus without topology metadata: %s",
			reclaim.String(), orphanCPUs.String())
	}

	allCPUsByCore := physicalCoreCPUs(topology)
	coreKeys := make([]physicalCoreKey, 0, len(cpusByCore))
	for key := range cpusByCore {
		coreKeys = append(coreKeys, key)
	}
	sort.Slice(coreKeys, func(i, j int) bool {
		return physicalCoreKeyLess(coreKeys[i], coreKeys[j])
	})
	for _, key := range coreKeys {
		cpus := cpusByCore[key]
		siblings := allCPUsByCore[key]
		if !cpus.Equals(siblings) {
			return fmt.Errorf("reclaim set %s is not core-aligned: numa %d socket %d core %d has %d of %d siblings",
				reclaim.String(), key.numaID, key.socketID, key.coreID, cpus.Size(), siblings.Size())
		}
	}
	return nil
}
