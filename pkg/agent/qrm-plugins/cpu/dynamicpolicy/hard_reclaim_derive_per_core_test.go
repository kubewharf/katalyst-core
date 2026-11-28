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

// handBuiltMixedSMT builds a NON-UNIFORM topology: NUMA0 holds two 2-sibling
// cores (SMT), NUMA1 holds two 1-sibling cores (non-SMT). NumCPUs=6,
// NumCores=4, so the global average CPUsPerCore()==1 while the real complete-core
// size on NUMA0 is 2. This is the shape the old global-cpusPerCore distributor
// cropped to zero / rejected as capacity-insufficient.
func handBuiltMixedSMT(t *testing.T) *machine.CPUTopology {
	t.Helper()
	topo := &machine.CPUTopology{
		NumCPUs:              6,
		NumCores:             4,
		NumSockets:           1,
		NumNUMANodes:         2,
		CPUDetails:           map[int]machine.CPUTopoInfo{},
		NUMANodeIDToSocketID: map[int]int{0: 0, 1: 0},
	}
	topo.CPUDetails[0] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 0}
	topo.CPUDetails[1] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 0}
	topo.CPUDetails[2] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 1}
	topo.CPUDetails[3] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 1}
	topo.CPUDetails[4] = machine.CPUTopoInfo{NUMANodeID: 1, SocketID: 0, CoreID: 2}
	topo.CPUDetails[5] = machine.CPUTopoInfo{NUMANodeID: 1, SocketID: 0, CoreID: 3}
	topo.NUMAToCPUs = machine.NUMANodeInfo{
		0: topo.CPUDetails.CPUsInNUMANodes(0),
		1: topo.CPUDetails.CPUsInNUMANodes(1),
	}
	return topo
}

// size1OnlyTopology: NUMA0 has a size-2 core {0,1}; NUMA1 has size-1 cores {2},{3}.
func size1OnlyTopology(t *testing.T) *machine.CPUTopology {
	t.Helper()
	topo := &machine.CPUTopology{
		NumCPUs:              4,
		NumCores:             3,
		NumSockets:           1,
		NumNUMANodes:         2,
		CPUDetails:           map[int]machine.CPUTopoInfo{},
		NUMANodeIDToSocketID: map[int]int{0: 0, 1: 0},
	}
	topo.CPUDetails[0] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 0}
	topo.CPUDetails[1] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: 0}
	topo.CPUDetails[2] = machine.CPUTopoInfo{NUMANodeID: 1, SocketID: 0, CoreID: 1}
	topo.CPUDetails[3] = machine.CPUTopoInfo{NUMANodeID: 1, SocketID: 0, CoreID: 2}
	topo.NUMAToCPUs = machine.NUMANodeInfo{
		0: topo.CPUDetails.CPUsInNUMANodes(0),
		1: topo.CPUDetails.CPUsInNUMANodes(1),
	}
	return topo
}

// assertCoreAlignedSeed pins the derive contract: a successful seed is non-empty,
// meets the requested size, and is built only from complete physical cores (every
// sibling of every seeded CPU is also seeded).
func assertCoreAlignedSeed(t *testing.T, topo *machine.CPUTopology, seed machine.CPUSet, requested int) {
	t.Helper()
	require.False(t, seed.IsEmpty(), "seed must be non-empty")
	require.GreaterOrEqual(t, seed.Size(), requested, "seed total %d must meet requested %d", seed.Size(), requested)
	for _, cpu := range seed.ToSliceInt() {
		info, ok := topo.CPUDetails[cpu]
		require.True(t, ok, "cpu %d missing topology metadata", cpu)
		for sibCPU, sib := range topo.CPUDetails {
			if sib.CoreID == info.CoreID && sib.NUMANodeID == info.NUMANodeID {
				require.True(t, seed.Contains(sibCPU),
					"cpu %d sibling %d must also be in seed (core %d on numa %d)", cpu, sibCPU, info.CoreID, info.NUMANodeID)
			}
		}
	}
}

// Mixed-SMT configured<=0 must bootstrap one whole core and succeed instead of
// cropping the sub-core pick to zero under the global-average cpusPerCore unit.
func TestDeriveReclaimMixedSMTZeroConfigured(t *testing.T) {
	t.Parallel()
	topo := handBuiltMixedSMT(t)
	require.Equal(t, 1, topo.CPUsPerCore(), "fixture premise: global average is 1")

	seed, size, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), 0)
	require.NoError(t, err, "mixed-SMT node with complete cores must bootstrap reclaim seed, not fail closed")
	assertCoreAlignedSeed(t, topo, seed, 0)
	require.Equal(t, seed.Size(), size)
}

// Mixed-SMT configured negative must be treated as the zero/non-expansion case
// (seed one whole core), not propagated as a negative target.
func TestDeriveReclaimMixedSMTNegativeConfigured(t *testing.T) {
	t.Parallel()
	topo := handBuiltMixedSMT(t)

	seed, _, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), -4)
	require.NoError(t, err)
	assertCoreAlignedSeed(t, topo, seed, 0)
}

// A positive configured size smaller than the smallest complete core on a
// mixed/sparse topology must succeed, returning exactly one whole core (no
// rounding error, no false capacity-insufficient).
func TestDeriveReclaimMixedSMTPositiveSubCore(t *testing.T) {
	t.Parallel()
	topo := handBuiltMixedSMT(t)

	for _, cfg := range []int{1, 2, 3} {
		seed, _, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), cfg)
		require.NoError(t, err, "cross-NUMA mixed cfg=%d must succeed", cfg)
		assertCoreAlignedSeed(t, topo, seed, cfg)
	}
}

// When only a size-1 complete core survives (the size-2 core and one size-1 core
// are reserved), derive must seed that size-1 core and succeed rather than
// treating it as non-aligned capacity under a global-max unit.
func TestDeriveReclaimOnlySize1CoreAvailable(t *testing.T) {
	t.Parallel()
	topo := size1OnlyTopology(t)
	reserved := machine.MustParse("0,1,3") // leave only core {2}

	seed, _, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, reserved, 0)
	require.NoError(t, err, "one complete size-1 core exists; must seed it, not false-insufficient")
	assertCoreAlignedSeed(t, topo, seed, 0)
	require.Equal(t, "2", seed.String(), "the only surviving complete core is {2}, got %s", seed.String())
}

// A uniform non-SMT node, configured=0 must seed exactly one cpu (its whole core).
func TestDeriveReclaimNonSMTZero(t *testing.T) {
	t.Parallel()
	topo, err := machine.GenerateDummyCPUTopologyWithoutSMT(32, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, topo.CPUsPerCore())

	seed, size, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, seed.Size(), "non-SMT whole core is one cpu, got %s", seed.String())
	require.Equal(t, 1, size)
}

// A positive configured size below one SMT core on a uniform SMT node rounds up
// to one whole core (2 cpus).
func TestDeriveReclaimUniformSMTLessThanCore(t *testing.T) {
	t.Parallel()
	topo, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topo.CPUsPerCore())

	seed, size, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), 1)
	require.NoError(t, err)
	require.Equal(t, 2, seed.Size(), "less-than-core positive must round up to one whole core, got %s", seed.String())
	require.Equal(t, 2, size)
}

// Sparse topology: reserve a whole NUMA so its only available CPUs are half-cores;
// derive must seed entirely from the surviving NUMA and succeed.
func TestDeriveReclaimSparseHalfCoreNUMATolerated(t *testing.T) {
	t.Parallel()
	topo, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	// NUMA0 = 0-7,16-23; NUMA1 = 8-15,24-31. Reserve all of NUMA0.
	reserved := machine.MustParse("0-7,16-23")

	seed, _, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, reserved, 0)
	require.NoError(t, err)
	require.False(t, seed.IsEmpty())
	require.True(t, seed.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)),
		"seed must live entirely on surviving NUMA1: %s", seed.String())
}

// Requesting more complete-core capacity than exists must fail closed, never
// silently under-seed.
func TestDeriveReclaimOverCapacityFailsClosed(t *testing.T) {
	t.Parallel()
	topo := handBuiltMixedSMT(t) // total eligible = 6 cpus

	_, _, _, err := deriveDefaultReclaimPreference(topo, topo.CPUDetails, machine.NewCPUSet(), 99)
	require.Error(t, err, "request 99 > total eligible 6 must fail closed")
}
