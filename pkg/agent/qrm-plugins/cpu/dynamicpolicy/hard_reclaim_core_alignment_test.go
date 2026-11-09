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
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCoreAlignmentUsesActualSiblingsOnNonUniformSMT(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs:    3,
		NumCores:   2,
		NumSockets: 2,
		CPUDetails: machine.CPUDetails{
			0: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
			1: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
			2: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		},
	}
	smt2Core := machine.NewCPUSet(1, 2)

	t.Run("candidate selection", func(t *testing.T) {
		t.Parallel()
		selected := takeCoreAlignedCPUSet(topology, smt2Core, machine.NewCPUSet(), 2)
		require.True(t, selected.Equals(smt2Core),
			"selected %s, want complete SMT2 core %s", selected.String(), smt2Core.String())
	})

	t.Run("alignment assertion", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, assertCoreAligned(smt2Core, topology))
		require.ErrorContains(t, assertCoreAligned(machine.NewCPUSet(1), topology), "1 of 2 siblings")
	})
}

func TestIndexedCoreAlignmentPreservesSiblingAndOrphanChecks(t *testing.T) {
	t.Parallel()
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
		1: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
		2: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		3: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		4: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
	}}
	index := newPhysicalCoreIndex(topology)
	for mask := 0; mask < 64; mask++ {
		cpus := machine.NewCPUSet()
		for cpu := 0; cpu < 6; cpu++ {
			if mask&(1<<cpu) != 0 {
				cpus.Add(cpu)
			}
		}
		// CPU 5 is an orphan; equal core IDs across sockets/NUMAs are distinct.
		want := mask&32 == 0 && (mask&3 == 0 || mask&3 == 3) && (mask&28 == 0 || mask&28 == 28)
		require.Equal(t, want, assertCoreAlignedWithIndex(cpus, index) == nil, "mask=%d", mask)
	}
	require.ErrorContains(t, assertCoreAlignedWithIndex(machine.NewCPUSet(5), index), "without topology metadata")
	require.ErrorContains(t, assertCoreAlignedWithIndex(machine.NewCPUSet(2), index), "1 of 3 siblings")
}

func TestIndexedCandidatesPreserveOrderAndDoNotAliasTopology(t *testing.T) {
	t.Parallel()
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
		1: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
		2: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		3: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		4: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
	}}
	index := newPhysicalCoreIndex(topology)
	before := machine.DeepcopyCPUAssignment(map[int]machine.CPUSet{0: index.byKey[physicalCoreKey{}], 1: index.byKey[physicalCoreKey{numaID: 1, socketID: 1}]})
	for mask := 0; mask < 64; mask++ {
		candidates := machine.NewCPUSet()
		for cpu := 0; cpu < 6; cpu++ {
			if mask&(1<<cpu) != 0 {
				candidates.Add(cpu)
			}
		}
		for preference := 0; preference < 64; preference++ {
			prefer := machine.NewCPUSet()
			for cpu := 0; cpu < 6; cpu++ {
				if preference&(1<<cpu) != 0 {
					prefer.Add(cpu)
				}
			}
			want := []coreAlignedCandidate{}
			for _, core := range []struct {
				key  physicalCoreKey
				cpus machine.CPUSet
			}{{physicalCoreKey{}, machine.NewCPUSet(0, 1)}, {physicalCoreKey{numaID: 1, socketID: 1}, machine.NewCPUSet(2, 3, 4)}} {
				if core.cpus.IsSubsetOf(candidates) {
					want = append(want, coreAlignedCandidate{key: core.key, coreID: core.key.coreID, cpus: core.cpus, preferredHit: core.cpus.Intersection(prefer).Size()})
				}
			}
			sort.Slice(want, func(i, j int) bool {
				if want[i].preferredHit != want[j].preferredHit {
					return want[i].preferredHit > want[j].preferredHit
				}
				return physicalCoreKeyLess(want[i].key, want[j].key)
			})
			got := coreAlignedCandidatesWithIndex(index, candidates, prefer)
			require.Equal(t, want, got, "mask=%d preference=%d", mask, preference)
			for _, core := range got {
				core.cpus.Add(99)
			}
		}
	}
	require.Equal(t, before[0], index.byKey[physicalCoreKey{}])
	require.Equal(t, before[1], index.byKey[physicalCoreKey{numaID: 1, socketID: 1}])
}

func BenchmarkIndexedCoreCandidates(b *testing.B) {
	for _, cpus := range []int{96, 1024} {
		b.Run(fmt.Sprintf("cpus=%d", cpus), func(b *testing.B) {
			topology, err := machine.GenerateDummyCPUTopology(cpus, 2, 2)
			if err != nil {
				b.Fatal(err)
			}
			index := newPhysicalCoreIndex(topology)
			candidates := topology.CPUDetails.CPUs()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(coreAlignedCandidatesWithIndex(index, candidates, candidates)) != cpus/2 {
					b.Fatal("incomplete core candidates")
				}
			}
		})
	}
}

func TestSteadyNormalizationRetainsElasticOwnerAndConfiguredFloor(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 2, 2)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	numa := topology.CPUDetails.CPUsInNUMANodes(1)
	committed := coresInNUMA(topology, 1, 0, 2)
	allocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			QoSLevel:    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
		},
		RampUp: true, RequestQuantity: 13,
		OriginalAllocationResult: machine.NewCPUSet(numa.ToSliceInt()[:4]...),
	}
	entries := state.PodEntries{"pod": state.ContainerEntries{"main": allocation}}
	descriptors := []advisorBlockDescriptor{
		{Class: advisorBlockClassMandatoryReclaim, NUMAID: 0, Quantity: 4, Eligible: all},
		{Class: advisorBlockClassMandatoryReclaim, NUMAID: 1, Quantity: 3, Eligible: numa, Committed: committed},
		{
			Class: advisorBlockClassDedicated, NUMAID: 1, Quantity: 13, Eligible: numa,
			Owners: []string{canonicalAdvisorBlockOwner(commonstate.PoolNameDedicated, "pod", "main", "")},
		},
	}
	for _, tc := range []struct {
		name    string
		elastic bool
		floor   string
		want    int
	}{
		{"request bounds permit only lower", false, "0", 2},
		{"elastic actual permits upper", true, "0", 4},
		{"configured floor overrides lower", false, "4", 3},
		{"configured floor and elastic upper", true, "4", 4},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conf := dynamicconfig.NewConfiguration()
			conf.EnableReclaim = true
			// NUMA0 is aligned; the floor must still be resolved for NUMA1.
			conf.MinReclaimedResourceForAllocate = v1.ResourceList{v1.ResourceCPU: resource.MustParse(tc.floor)}
			if tc.floor == "4" {
				conf.MinReclaimedResourceForAllocate[v1.ResourceCPU] = resource.MustParse("8")
			}
			got, _, err := normalizeSteadyRealNUMATargets(descriptors, all, topology, nil, conf, entries, tc.elastic)
			require.NoError(t, err)
			require.Equal(t, 4, got[0].Quantity)
			require.Equal(t, tc.want, got[1].Quantity)
			require.Equal(t, 16, got[1].Quantity+got[2].Quantity)
			require.Equal(t, 3, descriptors[1].Quantity)
			require.Equal(t, float64(13), allocation.RequestQuantity)
		})
	}
}
