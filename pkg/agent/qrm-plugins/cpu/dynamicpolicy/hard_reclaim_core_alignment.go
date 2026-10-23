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

// allocateCompleteCores selects a set of complete physical cores from eligible
// whose total logical-CPU count meets requested, using each core's ACTUAL sibling
// size — never a machine-wide cpus-per-core average.
//
// Objective (lexicographic, deterministic):
//  1. Hard constraint: only eligible complete cores (coreAlignedCandidates).
//  2. Primary: minimise the selected total that is still >= requested (minimum
//     overshoot). This is a 0/1 subset choice over complete cores; it is solved with
//     a bounded reachability DP, not by round-robin, so a request of 3 over
//     cores [2,3] picks the size-3 core (total 3) rather than 2+3=5.
//  3. Tie (same minimal total): prefer cores with more siblings in prefer, then
//     spread the pick across ascending NUMAs (round-robin), then ascending core key.
//
// Complexity: the optimal total T* satisfies requested <= T* < requested+maxCoreSize
// (removing any core from a minimal-feasible subset that overshot by >= maxCoreSize
// would still be feasible, a contradiction), so the DP window W = requested+maxSize
// is bounded. The reachability DP and the suffix-reachability reconstruction are both
// O(n*W); this runs once per derive, never on a hot path.
//
// If the sum of every eligible complete core is below requested it returns an empty
// set alongside errReclaimFallbackCapacityInsufficient so the caller fails closed
// instead of silently under-seeding.
func allocateCompleteCores(
	topology *machine.CPUTopology,
	eligible machine.CPUSet,
	prefer machine.CPUSet,
	requested int,
) (machine.CPUSet, error) {
	result := machine.NewCPUSet()
	if topology == nil || eligible.IsEmpty() {
		return result, errReclaimFallbackCapacityInsufficient
	}
	cores := coreAlignedCandidates(topology, eligible, prefer)
	if len(cores) == 0 {
		return result, errReclaimFallbackCapacityInsufficient
	}

	// Existence-only: one whole core, no balancing needed.
	if requested <= 0 {
		return cores[0].cpus.Clone(), nil
	}

	totalCapacity := 0
	maxSize := 0
	for _, c := range cores {
		totalCapacity += c.cpus.Size()
		if c.cpus.Size() > maxSize {
			maxSize = c.cpus.Size()
		}
	}
	if totalCapacity < requested {
		return result, errReclaimFallbackCapacityInsufficient
	}

	// Bounded 0/1 reachability DP over complete-core sizes. reachable[s] is true
	// iff some subset sums to exactly s. We only need sums up to requested+maxSize
	// because the optimal total T* is provably inside that window.
	limit := requested + maxSize
	reachable := make([]bool, limit+1)
	reachable[0] = true
	for _, c := range cores {
		sz := c.cpus.Size()
		for s := limit; s >= sz; s-- {
			if reachable[s-sz] {
				reachable[s] = true
			}
		}
	}

	target := -1
	for s := requested; s <= limit; s++ {
		if reachable[s] {
			target = s
			break
		}
	}
	if target < 0 {
		// totalCapacity >= requested guarantees a feasible subset (all cores); this
		// is defensive and should be unreachable, but fail closed rather than guess.
		return result, errReclaimFallbackCapacityInsufficient
	}

	// Deterministic pick order: higher preference tier first, and within a tier
	// round-robin across ascending NUMAs to spread the seed.
	order := completeCoresPickOrder(cores)

	// suffix[i][s] is reachable using order[i:] (the not-yet-decided cores).
	// Reconstruct greedily: take a core only if the remaining sum is still reachable
	// without it, which guarantees we land exactly on target.
	suffix := make([][]bool, len(order)+1)
	suffix[len(order)] = make([]bool, target+1)
	suffix[len(order)][0] = true
	for i := len(order) - 1; i >= 0; i-- {
		suffix[i] = append([]bool(nil), suffix[i+1]...)
		sz := order[i].cpus.Size()
		for s := target; s >= sz; s-- {
			if suffix[i+1][s-sz] {
				suffix[i][s] = true
			}
		}
	}

	selected := machine.NewCPUSet()
	remaining := target
	for i := range order {
		sz := order[i].cpus.Size()
		if sz <= remaining && suffix[i+1][remaining-sz] {
			selected = selected.Union(order[i].cpus)
			remaining -= sz
		}
	}
	if remaining != 0 {
		// Should be impossible given target is reachable; fail closed.
		return machine.NewCPUSet(), errReclaimFallbackCapacityInsufficient
	}
	return selected, nil
}

// completeCoresPickOrder flattens already-preference-sorted cores into a
// deterministic reconstruction order: preference tier (preferredHit desc) first —
// every core of a higher tier precedes any core of a lower tier — and within each
// tier round-robin across ascending NUMAs so equal-total picks are spread rather
// than stacked on a single NUMA.
func completeCoresPickOrder(cores []coreAlignedCandidate) []coreAlignedCandidate {
	// cores arrive sorted preferredHit desc then key asc; split into contiguous tiers.
	tiers := make([][]coreAlignedCandidate, 0)
	for _, c := range cores {
		n := len(tiers)
		if n == 0 || tiers[n-1][0].preferredHit != c.preferredHit {
			tiers = append(tiers, []coreAlignedCandidate{c})
		} else {
			tiers[n-1] = append(tiers[n-1], c)
		}
	}

	order := make([]coreAlignedCandidate, 0, len(cores))
	for _, tier := range tiers {
		byNuma := make(map[int][]coreAlignedCandidate)
		numaSet := map[int]struct{}{}
		for _, c := range tier {
			byNuma[c.key.numaID] = append(byNuma[c.key.numaID], c)
			numaSet[c.key.numaID] = struct{}{}
		}
		numaIDs := make([]int, 0, len(numaSet))
		for id := range numaSet {
			numaIDs = append(numaIDs, id)
		}
		sort.Ints(numaIDs)

		for {
			progressed := false
			for _, id := range numaIDs {
				if len(byNuma[id]) == 0 {
					continue
				}
				order = append(order, byNuma[id][0])
				byNuma[id] = byNuma[id][1:]
				progressed = true
			}
			if !progressed {
				break
			}
		}
	}
	return order
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
