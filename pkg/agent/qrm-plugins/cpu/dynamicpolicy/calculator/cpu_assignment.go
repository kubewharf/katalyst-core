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

// TakeByTopology tries to allocate those required cpus in the same socket or cores
func TakeByTopology(info *machine.KatalystMachineInfo, availableCPUs machine.CPUSet,
	cpuRequirement int,
) (machine.CPUSet, error) {
	acc := newCPUAccumulator(info, availableCPUs, cpuRequirement)
	if acc.isSatisfied() {
		return acc.result.Clone(), nil
	}
	if acc.isFailed() {
		return machine.NewCPUSet(), fmt.Errorf("not enough cpus available to satisfy request")
	}

	// Algorithm: topology-aware best-fit
	// 1. Acquire whole sockets, if available and the container requires at
	//    least a socket's-worth of CPUs.
	acc.takeFullSockets()
	if acc.isSatisfied() {
		return acc.result.Clone(), nil
	}

	// 2. Acquire whole cores, if available and the container requires at least
	//    a core's-worth of CPUs.
	acc.takeFullCores()
	if acc.isSatisfied() {
		return acc.result.Clone(), nil
	}

	// 3. Acquire single threads, preferring to fill partially-allocated cores
	//    on the same sockets as the whole cores we have already taken in this
	//    allocation.
	acc.takeRemainingCPUs()
	if acc.isSatisfied() {
		return acc.result.Clone(), nil
	}

	return machine.NewCPUSet(), fmt.Errorf("failed to allocate cpus")
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
