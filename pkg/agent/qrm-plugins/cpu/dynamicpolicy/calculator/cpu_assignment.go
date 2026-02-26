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

package calculator

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cpuAccumulator is used as a helper function to calculate cpu detailed
// allocation results according to machine topology and cpu requirements;
// it uses a stable allocation strategy, meaning that every time we need
// a fixed number of cpu cores based a fixed cpu topology, we will always
// get a fixed allocation results.
type cpuAccumulator struct {
	// numCPUsNeeded records the account of cpus that we needed
	numCPUsNeeded int

	cpuDetails  machine.CPUDetails
	cpuTopology *machine.CPUTopology

	result machine.CPUSet
}

func newCPUAccumulator(machineInfo *machine.KatalystMachineInfo, availableCPUs machine.CPUSet, numCPUs int) *cpuAccumulator {
	a := &cpuAccumulator{
		numCPUsNeeded: numCPUs,
		cpuTopology:   machineInfo.CPUTopology,
		cpuDetails:    machineInfo.CPUDetails.KeepOnly(availableCPUs),
		result:        machine.NewCPUSet(),
	}
	return a
}

func (a *cpuAccumulator) getDetails() machine.CPUDetails {
	return a.cpuDetails
}

func (a *cpuAccumulator) getTopology() *machine.CPUTopology {
	return a.cpuTopology
}

// freeCPUsInNUMANode returns free cpu ids in specified NUMA,
// and those returned cpu slices have already been sorted
func (a *cpuAccumulator) freeCPUsInNUMANode(numaID int) []int {
	return a.cpuDetails.CPUsInNUMANodes(numaID).ToSliceInt()
}

// freeCPUsInNUMANodeReversely returns free cpu ids in specified NUMA,
// and those returned cpu slices have already been sorted reversely
func (a *cpuAccumulator) freeCPUsInNUMANodeReversely(numaID int) []int {
	return a.cpuDetails.CPUsInNUMANodes(numaID).ToSliceIntReversely()
}

// freeCoresInNUMANode returns free core ids in specified NUMA,
// and those returned cpu slices have already been sorted
func (a *cpuAccumulator) freeCoresInNUMANode(numaID int) []int {
	return a.cpuDetails.CoresInNUMANodes(numaID).Filter(a.isCoreFree).ToSliceInt()
}

// freeCoresInNUMANode returns free core ids in specified NUMA,
// and those returned cpu slices have already been sorted reversely
func (a *cpuAccumulator) freeCoresInNUMANodeReversely(numaID int) []int {
	return a.cpuDetails.CoresInNUMANodes(numaID).Filter(a.isCoreFree).ToSliceIntReversely()
}

// isSocketFree returns true if the supplied socket is fully available
func (a *cpuAccumulator) isSocketFree(socketID int) bool {
	return a.cpuDetails.CPUsInSockets(socketID).Size() == a.getTopology().CPUsPerSocket()
}

// isCoreFree returns true if the supplied core is fully available
func (a *cpuAccumulator) isCoreFree(coreID int) bool {
	return a.cpuDetails.CPUsInCores(coreID).Size() == a.getTopology().CPUsPerCore()
}

// freeSockets returns free socket IDs as a slice sorted by sortAvailableSockets().
func (a *cpuAccumulator) freeSockets() []int {
	free := []int{}
	for _, socket := range a.sortAvailableSockets() {
		if a.isSocketFree(socket) {
			free = append(free, socket)
		}
	}
	return free
}

// freeCores returns free core IDs as a slice sorted by sortAvailableCores().
func (a *cpuAccumulator) freeCores() []int {
	free := []int{}
	for _, core := range a.sortAvailableCores() {
		if a.isCoreFree(core) {
			free = append(free, core)
		}
	}
	return free
}

// freeCPUs returns free CPU IDs as a slice sorted by sortAvailableCPUs().
func (a *cpuAccumulator) freeCPUs() []int {
	return a.sortAvailableCPUs()
}

// sort the provided list of sockets/cores/cpus referenced in 'ids' by the
// number of available CPUs contained within them (smallest to largest). The
// 'getCPU()' parameter defines the function that should be called to retrieve
// the list of available CPUs for the type of socket/core/cpu being referenced.
// If two sockets/cores/cpus have the same number of available CPUs, they are
// sorted in ascending order by their id.
func (a *cpuAccumulator) sort(ids []int, getCPUs func(ids ...int) machine.CPUSet) {
	sort.Slice(ids,
		func(i, j int) bool {
			iCPUs := getCPUs(ids[i])
			jCPUs := getCPUs(ids[j])
			if iCPUs.Size() < jCPUs.Size() {
				return true
			}
			if iCPUs.Size() > jCPUs.Size() {
				return false
			}
			return ids[i] < ids[j]
		})
}

// getBestMatchCPUsNeededL3Cache returns the L3 cache ID that best matches the number of CPUs needed.
// It directly selects the L3 cache with the closest match to the required number of CPUs,
// preferring caches with CPU count equal to or slightly greater than the requirement.
func (a *cpuAccumulator) getBestMatchCPUsNeededL3Cache() (int, bool) {
	l3Caches := a.cpuDetails.L3Caches().ToSliceInt()
	if len(l3Caches) == 0 {
		return 0, false
	}

	var bestL3CacheID int
	bestMatchFound := false
	var bestMatchDiff int = -1 // -1 indicates no match found yet

	for _, l3CacheID := range l3Caches {
		cpusInL3Cache := a.cpuDetails.CPUsInL3Caches(l3CacheID)
		cpuCount := cpusInL3Cache.Size()

		// Exact match - return immediately
		if cpuCount == a.numCPUsNeeded {
			return l3CacheID, true
		}

		// For caches with more CPUs than needed, prefer the one with the smallest excess
		if cpuCount > a.numCPUsNeeded {
			diff := cpuCount - a.numCPUsNeeded
			if !bestMatchFound || diff < bestMatchDiff {
				bestL3CacheID = l3CacheID
				bestMatchDiff = diff
				bestMatchFound = true
			}
		}
	}

	// If we found a cache with more CPUs than needed, return it
	if bestMatchFound {
		return bestL3CacheID, true
	}

	// If no cache with more CPUs was found, find the one with the most CPUs
	// (closest match when all caches have fewer CPUs than needed)
	for _, l3CacheID := range l3Caches {
		cpusInL3Cache := a.cpuDetails.CPUsInL3Caches(l3CacheID)
		cpuCount := cpusInL3Cache.Size()

		if !bestMatchFound || cpuCount > bestMatchDiff {
			bestL3CacheID = l3CacheID
			bestMatchDiff = cpuCount
			bestMatchFound = true
		}
	}

	return bestL3CacheID, bestMatchFound
}

// tryAlignL3Caches handles remaining CPU allocation with L3 cache topology awareness.
//
// This method implements fine-grained CPU allocation based on L3 cache topology.
// When the requested CPU count doesn't align with complete L3 cache sizes,
// it intelligently selects the most suitable L3 cache to minimize cache contention
// and maximize memory locality for the workload.
//
// Algorithm:
// 1. Directly selects the L3 cache that best matches the remaining CPU requirement
// 2. If remaining need >= cache size: allocate entire cache and recurse
// 3. If remaining need < cache size: restrict allocation to this cache only
func (a *cpuAccumulator) tryAlignL3Caches() {
	l3Cache, found := a.getBestMatchCPUsNeededL3Cache()
	if !found {
		return
	}

	cpusInL3Cache := a.cpuDetails.CPUsInL3Caches(l3Cache)
	if a.numCPUsNeeded >= cpusInL3Cache.Size() {
		// Cache is smaller than remaining need - take entire cache for efficiency
		klog.V(4).InfoS("tryAlignL3Caches: claiming entire L3 cache (partial)", "l3Cache", l3Cache, "cacheSize", cpusInL3Cache.Size(), "remainingNeed", a.numCPUsNeeded)
		a.take(cpusInL3Cache)
		if a.isSatisfied() {
			return
		}
		// Continue with remaining allocation from other caches
		a.tryAlignL3Caches()
	} else {
		// Cache is larger than remaining need - restrict to this cache for optimal locality
		// This ensures all allocated CPUs share the same L3 cache, minimizing memory latency
		klog.V(4).InfoS("tryAlignL3Caches: restricting allocation to L3 cache", "l3Cache", l3Cache, "cacheSize", cpusInL3Cache.Size(), "remainingNeed", a.numCPUsNeeded)
		a.cpuDetails = a.cpuDetails.KeepOnly(cpusInL3Cache)
	}
}

// Sort all sockets with free CPUs using the sort() algorithm defined above.
func (a *cpuAccumulator) sortAvailableSockets() []int {
	sockets := a.cpuDetails.Sockets().ToSliceNoSortInt()
	a.sort(sockets, a.cpuDetails.CPUsInSockets)
	return sockets
}

// Sort all cores with free CPUs:
// - First by socket using sortAvailableSockets().
// - Then within each socket, using the sort() algorithm defined above.
func (a *cpuAccumulator) sortAvailableCores() []int {
	var result []int
	for _, socket := range a.sortAvailableSockets() {
		cores := a.cpuDetails.CoresInSockets(socket).ToSliceNoSortInt()
		a.sort(cores, a.cpuDetails.CPUsInCores)
		result = append(result, cores...)
	}
	return result
}

// Sort all available CPUs:
// - First by core using sortAvailableCores().
// - Then within each core, using the sort() algorithm defined above.
func (a *cpuAccumulator) sortAvailableCPUs() []int {
	var result []int
	for _, core := range a.sortAvailableCores() {
		cpus := a.cpuDetails.CPUsInCores(core).ToSliceNoSortInt()
		sort.Ints(cpus)
		result = append(result, cpus...)
	}
	return result
}

func (a *cpuAccumulator) take(cpus machine.CPUSet) {
	a.result = a.result.Union(cpus)
	a.cpuDetails = a.cpuDetails.KeepOnly(a.cpuDetails.CPUs().Difference(a.result))
	a.numCPUsNeeded -= cpus.Size()
}

func (a *cpuAccumulator) takeFullSockets() {
	for _, socket := range a.freeSockets() {
		cpusInSocket := a.getTopology().CPUDetails.CPUsInSockets(socket)
		if !a.needs(cpusInSocket.Size()) {
			continue
		}
		klog.V(4).InfoS("takeFullSockets: claiming socket", "socket", socket)
		a.take(cpusInSocket)
	}
}

func (a *cpuAccumulator) takeFullCores() {
	for _, core := range a.freeCores() {
		cpusInCore := a.getTopology().CPUDetails.CPUsInCores(core)
		if !a.needs(cpusInCore.Size()) {
			continue
		}
		klog.V(4).InfoS("takeFullCores: claiming core", "core", core)
		a.take(cpusInCore)
	}
}

func (a *cpuAccumulator) takeRemainingCPUs() {
	for _, cpu := range a.sortAvailableCPUs() {
		klog.V(4).InfoS("takeRemainingCPUs: claiming CPU", "cpu", cpu)
		a.take(machine.NewCPUSet(cpu))
		if a.isSatisfied() {
			return
		}
	}
}

func (a *cpuAccumulator) needs(n int) bool {
	return a.numCPUsNeeded >= n
}

func (a *cpuAccumulator) isSatisfied() bool {
	return a.numCPUsNeeded < 1
}

func (a *cpuAccumulator) isFailed() bool {
	return a.numCPUsNeeded > a.cpuDetails.CPUs().Size()
}

// TakeByTopology implements a topology-aware CPU allocation strategy that prioritizes
// hardware locality and cache efficiency for optimal workload performance.
//
// This function implements a multi-tier allocation strategy designed to minimize
// cross-socket communication and maximize cache utilization. The allocation follows
// a hierarchical approach from largest to smallest topology units.
//
// Parameters:
//   - info: Machine topology information including NUMA, socket, core, and cache hierarchy
//   - availableCPUs: Set of CPUs available for allocation
//   - cpuRequirement: Number of CPUs needed for the workload
//   - alignByL3Caches: Whether to consider L3 cache topology in allocation decisions
//
// Returns:
//   - CPUSet: The allocated set of CPUs with optimal topology placement
//   - error: Error if allocation fails due to insufficient resources
//
// Allocation Strategy (Topology-Aware Best-Fit):
//
// Phase 1: Socket-Level Allocation (Highest Locality)
//   - Attempts to allocate entire CPU sockets when the requirement matches or exceeds socket size
//   - Provides maximum memory bandwidth and minimal cross-socket latency
//
// Phase 2: L3 Cache-Aware Allocation (Conditional)
//   - Activated when alignByL3Caches is true
//   - Prioritizes allocation within shared L3 cache domains to minimize cache contention
//   - Uses tryAlignL3Caches() for intelligent cache-aligned distribution
//
// Phase 3: Core-Level Allocation (Medium Locality)
//   - Allocates complete CPU cores to avoid hyperthreading contention
//   - Preferred for workloads sensitive to thread interference
//
// Phase 4: Thread-Level Allocation (Fine-Grained)
//   - Allocates individual hyperthreads from partially utilized cores
//   - Prefers cores on sockets already allocated to maintain NUMA affinity
func TakeByTopology(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int, alignByL3Caches bool,
) (machine.CPUSet, error) {
	// Initialize accumulator with topology-aware state
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)

	// Fast-path: Handle edge cases immediately
	if acc.isSatisfied() {
		// Zero CPU requirement - return empty set immediately
		return acc.result.Clone(), nil
	}
	if acc.isFailed() {
		// Insufficient resources - fail fast with descriptive error
		return machine.NewCPUSet(), fmt.Errorf("insufficient CPUs: requested %d, available %d",
			cpuRequirement, availableCPUs.Size())
	}

	// Phase 1: Socket-level allocation for maximum locality
	// This phase attempts to allocate entire CPU sockets when beneficial
	acc.takeFullSockets()
	if acc.isSatisfied() {
		klog.V(4).InfoS("TakeByTopology: allocated at socket level", "allocated", acc.result.Size())
		return acc.result.Clone(), nil
	}

	// Phase 2: L3 cache topology optimization (if enabled)
	// This phase considers cache topology to minimize memory latency
	if alignByL3Caches {
		acc.tryAlignL3Caches()
		if acc.isSatisfied() {
			klog.V(4).InfoS("TakeByTopology: allocated with L3 cache alignment", "allocated", acc.result.Size())
			return acc.result.Clone(), nil
		}
	}

	// Phase 3: Core-level allocation to avoid HT contention
	// Allocates complete cores for workloads sensitive to thread interference
	acc.takeFullCores()
	if acc.isSatisfied() {
		klog.V(4).InfoS("TakeByTopology: allocated at core level", "allocated", acc.result.Size())
		return acc.result.Clone(), nil
	}

	// Phase 4: Thread-level allocation for remaining needs
	// Allocates individual threads from partially utilized cores
	acc.takeRemainingCPUs()
	if acc.isSatisfied() {
		klog.V(4).InfoS("TakeByTopology: allocated at thread level", "allocated", acc.result.Size())
		return acc.result.Clone(), nil
	}

	// Exhaustive allocation failed - no combination satisfies requirement
	return machine.NewCPUSet(), fmt.Errorf("topology-aware allocation failed: requested %d CPUs, exhausted all allocation strategies", cpuRequirement)
}

// TakeByTopologyWithSpreading implements a topology-aware CPU allocation strategy that
// spreads the allocated CPUs across NUMA nodes.
func TakeByTopologyWithSpreading(info *machine.KatalystMachineInfo,
	availableCPUs map[int]machine.CPUSet,
	cpuRequirement int, alignByL3Caches bool,
) (machine.CPUSet, error) {
	alignedAvailableCPUs := machine.CPUSet{}
	for _, availableCPUsInNuma := range availableCPUs {
		// allocate cpu for numa affinity pod, prefer to allocate cpus spread across NUMA nodes,
		// if cpu requirement cannot be divided evenly among numa nodes,
		// round up to ensure pod request can be satisfied
		requestCPUsInNuma := math.Ceil(float64(cpuRequirement) / float64(len(availableCPUs)))
		result, err := TakeByTopology(info, availableCPUsInNuma, int(requestCPUsInNuma), alignByL3Caches)
		if err != nil {
			return machine.NewCPUSet(), err
		}
		alignedAvailableCPUs.Union(result)
	}
	return alignedAvailableCPUs, nil
}

// TakeByNUMABalance tries to make the allocated cpu spread on different
// sockets, and it uses cpu Cores as the basic allocation unit
func TakeByNUMABalance(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, machine.CPUSet, error) {
	var err error
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)

	if acc.isSatisfied() {
		goto successful
	}

	if takeFreeCoresByNumaBalance(acc) {
		goto successful
	}

	for {
		if acc.isFailed() {
			err = fmt.Errorf("not enough cpus available to satisfy request")
			goto failed
		}

		for _, s := range info.CPUDetails.NUMANodes().ToSliceInt() {
			for _, c := range acc.freeCPUsInNUMANode(s) {
				if acc.needs(1) {
					acc.take(machine.NewCPUSet(c))
				}
				if acc.isSatisfied() {
					goto successful
				} else {
					break
				}
			}
		}
	}
failed:
	if err == nil {
		err = errors.New("failed to allocate cpus")
	}
	return availableCPUs, availableCPUs, err
successful:
	return acc.result.Clone(), availableCPUs.Difference(acc.result), nil
}

// TakeHTByNUMABalance tries to make the allocated cpu spread on different
// NUMAs, and it uses cpu HT as the basic allocation unit
func TakeHTByNUMABalance(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, machine.CPUSet, error) {
	var err error
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)
	if acc.isSatisfied() {
		goto successful
	}

	for {
		if acc.isFailed() {
			err = fmt.Errorf("not enough cpus available to satisfy request")
			goto failed
		}

		for _, s := range info.CPUDetails.NUMANodes().ToSliceInt64() {
			for _, c := range acc.freeCPUsInNUMANode(int(s)) {
				if acc.needs(1) {
					acc.take(machine.NewCPUSet(c))
				}
				if acc.isSatisfied() {
					goto successful
				} else {
					break
				}
			}
		}
	}
failed:
	if err == nil {
		err = errors.New("failed to allocate cpus")
	}
	return availableCPUs, availableCPUs, err
successful:
	return acc.result.Clone(), availableCPUs.Difference(acc.result), nil
}

// TakeHTByNUMABalanceReversely sames as TakeHTByNUMABalance, but it takes cpus reversely
func TakeHTByNUMABalanceReversely(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, machine.CPUSet, error) {
	var err error
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)
	if acc.isSatisfied() {
		goto successful
	}

	for {
		if acc.isFailed() {
			err = fmt.Errorf("not enough cpus available to satisfy request")
			goto failed
		}

		for _, s := range info.CPUDetails.NUMANodes().ToSliceInt64() {
			for _, c := range acc.freeCPUsInNUMANodeReversely(int(s)) {
				if acc.needs(1) {
					acc.take(machine.NewCPUSet(c))
				}
				if acc.isSatisfied() {
					goto successful
				} else {
					break
				}
			}
		}
	}
failed:
	if err == nil {
		err = errors.New("failed to allocate cpus")
	}
	return availableCPUs, availableCPUs, err
successful:
	return acc.result.Clone(), availableCPUs.Difference(acc.result), nil
}

// TakeByNUMABalanceReversely tries to make the allocated cpu resersely spread on different
// NUMAs, and it uses cpu Cores as the basic allocation unit
func TakeByNUMABalanceReversely(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, machine.CPUSet, error) {
	var err error
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)

	if acc.isSatisfied() {
		goto successful
	}

	if takeFreeCoresByNumaBalanceReversely(acc) {
		goto successful
	}

	for {
		if acc.isFailed() {
			err = fmt.Errorf("not enough cpus available to satisfy request")
			goto failed
		}

		for _, s := range info.CPUDetails.NUMANodes().ToSliceInt() {
			for _, c := range acc.freeCPUsInNUMANodeReversely(s) {
				if acc.needs(1) {
					acc.take(machine.NewCPUSet(c))
				}
				if acc.isSatisfied() {
					goto successful
				} else {
					break
				}
			}
		}
	}
failed:
	if err == nil {
		err = errors.New("failed to allocate cpus")
	}
	return availableCPUs, availableCPUs, err
successful:
	return acc.result.Clone(), availableCPUs.Difference(acc.result), nil
}

func takeFreeCoresByNumaBalance(acc *cpuAccumulator) bool {
	info := acc.getTopology()
	if !acc.needs(info.CPUsPerCore()) {
		return false
	}

	numaSlice := info.CPUDetails.NUMANodes().ToSliceInt()
	freeCoresMap := make(map[int][]int)
	maxFreeCoresCountInNuma := 0
	for _, s := range numaSlice {
		freeCores := acc.freeCoresInNUMANode(s)
		freeCoresMap[s] = freeCores
		if len(freeCores) > maxFreeCoresCountInNuma {
			maxFreeCoresCountInNuma = len(freeCores)
		}
	}

	for i := 0; i < maxFreeCoresCountInNuma; i++ {
		for _, s := range numaSlice {
			if len(freeCoresMap[s]) > i {
				c := freeCoresMap[s][i]
				if acc.needs(info.CPUsPerCore()) {
					acc.take(acc.getDetails().CPUsInCores(c))
					if acc.isSatisfied() {
						return true
					}
				}
			}
		}
	}
	return false
}

func takeFreeCoresByNumaBalanceReversely(acc *cpuAccumulator) bool {
	info := acc.getTopology()
	if !acc.needs(info.CPUsPerCore()) {
		return false
	}

	numaSlice := info.CPUDetails.NUMANodes().ToSliceInt()
	reverselyFreeCoresMap := make(map[int][]int)
	maxFreeCoresCountInNuma := 0
	for _, s := range numaSlice {
		freeCores := acc.freeCoresInNUMANodeReversely(s)
		reverselyFreeCoresMap[s] = freeCores
		if len(freeCores) > maxFreeCoresCountInNuma {
			maxFreeCoresCountInNuma = len(freeCores)
		}
	}
	for i := 0; i < maxFreeCoresCountInNuma; i++ {
		for _, s := range numaSlice {
			if len(reverselyFreeCoresMap[s]) > i {
				c := reverselyFreeCoresMap[s][i]
				if acc.needs(info.CPUsPerCore()) {
					acc.take(acc.getDetails().CPUsInCores(c))
					if acc.isSatisfied() {
						return true
					}
				}
			}
		}
	}
	return false
}
