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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// coreSpec describes one physical core: which NUMA, its core id, and how many
// online SMT siblings it has (its complete-core size).
type coreSpec struct {
	numaID int
	coreID int
	size   int
}

// topologyWithCores builds a topology whose physical cores have the given
// sibling counts. Logical CPUs are allocated consecutively so a core of size s
// owns s consecutive CPUs on its NUMA.
func topologyWithCores(t *testing.T, specs []coreSpec) *machine.CPUTopology {
	t.Helper()
	topo := &machine.CPUTopology{
		CPUDetails:           map[int]machine.CPUTopoInfo{},
		NUMANodeIDToSocketID: map[int]int{},
		NUMAToCPUs:           machine.NUMANodeInfo{},
	}
	numaCPUs := map[int][]int{}
	cpu := 0
	for _, sp := range specs {
		seen := map[int]struct{}{}
		for i := 0; i < sp.size; i++ {
			topo.CPUDetails[cpu] = machine.CPUTopoInfo{
				NUMANodeID: sp.numaID,
				SocketID:   0,
				CoreID:     sp.coreID,
			}
			numaCPUs[sp.numaID] = append(numaCPUs[sp.numaID], cpu)
			seen[sp.numaID] = struct{}{}
			cpu++
		}
		_ = seen
	}
	topo.NumCPUs = cpu
	topo.NumCores = len(specs)
	topo.NumSockets = 1
	for _, n := range topo.CPUDetails {
		topo.NUMANodeIDToSocketID[n.NUMANodeID] = 0
	}
	numNUMAs := 0
	for n := range numaCPUs {
		numNUMAs++
		topo.NUMANodeIDToSocketID[n] = 0
	}
	topo.NumNUMANodes = numNUMAs
	for n, cpus := range numaCPUs {
		set := machine.NewCPUSet()
		for _, c := range cpus {
			set.Add(c)
		}
		topo.NUMAToCPUs[n] = set
	}
	return topo
}

func eligibleAll(topo *machine.CPUTopology) machine.CPUSet {
	return topo.CPUDetails.CPUs()
}

// [2,3] req=3: must pick the size-3 core exactly (total 3), NOT size-2 + size-3.
func TestAllocateCompleteCoresTwoThreePickThree(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 2},
		{numaID: 0, coreID: 1, size: 3},
	})
	got, err := allocateCompleteCores(topo, eligibleAll(topo), machine.NewCPUSet(), 3)
	require.NoError(t, err)
	require.Equal(t, 3, got.Size(), "must pick the size-3 core for total 3, got %s", got.String())
	require.True(t, got.Equals(machine.MustParse("2-4")), "expected the size-3 core {2,3,4}, got %s", got.String())
}

// [3,2,2] req=4: must pick the two size-2 cores (total 4), not the size-3 first.
func TestAllocateCompleteCoresThreeTwoTwoPickTwoTwo(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 3},
		{numaID: 0, coreID: 1, size: 2},
		{numaID: 0, coreID: 2, size: 2},
	})
	got, err := allocateCompleteCores(topo, eligibleAll(topo), machine.NewCPUSet(), 4)
	require.NoError(t, err)
	require.Equal(t, 4, got.Size(), "must pick two size-2 cores for total 4, got %s", got.String())
	require.True(t, got.Equals(machine.MustParse("3-6")), "expected the two size-2 cores {3,4,5,6}, got %s", got.String())
}

// [4,1,2] req=3: must pick size-1 + size-2 (total 3), not the size-4 core.
func TestAllocateCompleteCoresFourOneTwoPickOneTwo(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 4},
		{numaID: 0, coreID: 1, size: 1},
		{numaID: 0, coreID: 2, size: 2},
	})
	got, err := allocateCompleteCores(topo, eligibleAll(topo), machine.NewCPUSet(), 3)
	require.NoError(t, err)
	require.Equal(t, 3, got.Size(), "must pick size-1 + size-2 for total 3, got %s", got.String())
	require.True(t, got.Equals(machine.MustParse("4-6")), "expected {4}(size1)+{5,6}(size2), got %s", got.String())
}

// Same-total tie-break: three size-2 cores on three NUMAs, req=4 must spread the
// two picked cores across two distinct NUMAs rather than stacking them on one.
func TestAllocateCompleteCoresSameTotalSpreadsNUMAs(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 2},
		{numaID: 1, coreID: 0, size: 2},
		{numaID: 2, coreID: 0, size: 2},
	})
	got, err := allocateCompleteCores(topo, eligibleAll(topo), machine.NewCPUSet(), 4)
	require.NoError(t, err)
	require.Equal(t, 4, got.Size())
	numaSet := map[int]int{}
	for _, cpu := range got.ToSliceInt() {
		info := topo.CPUDetails[cpu]
		numaSet[info.NUMANodeID]++
	}
	require.Len(t, numaSet, 2, "two size-2 cores should land on 2 distinct NUMAs, per-NUMA counts %v", numaSet)
}

// Preference tie-break: among same-size cores summing to the target, the core
// whose siblings hit the preference set is preferred.
func TestAllocateCompleteCoresPrefersPreferredCore(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 2},
		{numaID: 0, coreID: 1, size: 2},
	})
	eligible := eligibleAll(topo)
	// prefer the second core {2,3}.
	prefer := machine.MustParse("2-3")
	got, err := allocateCompleteCores(topo, eligible, prefer, 2)
	require.NoError(t, err)
	require.Equal(t, 2, got.Size())
	require.True(t, got.Equals(machine.MustParse("2-3")), "preferred core {2,3} must be chosen, got %s", got.String())
}

// Insufficient total capacity still fails closed.
func TestAllocateCompleteCoresInsufficientFailsClosed(t *testing.T) {
	t.Parallel()
	topo := topologyWithCores(t, []coreSpec{
		{numaID: 0, coreID: 0, size: 2},
		{numaID: 0, coreID: 1, size: 2},
	})
	_, err := allocateCompleteCores(topo, eligibleAll(topo), machine.NewCPUSet(), 9)
	require.ErrorIs(t, err, errReclaimFallbackCapacityInsufficient)
}
