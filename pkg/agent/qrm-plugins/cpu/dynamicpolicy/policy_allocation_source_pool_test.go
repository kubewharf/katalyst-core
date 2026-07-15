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

package dynamicpolicy

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDynamicPolicy_takeByTieredPreferredCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_takeByTieredPreferredCPUs")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	t.Run("prefers the first tier before falling back to available", func(t *testing.T) {
		available := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		// first tier is fully within available; request fits entirely in tier 1
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(4, 5), machine.NewCPUSet(6, 7)}, 2)
		require.NoError(t, err)
		require.True(t, taken.Equals(machine.NewCPUSet(4, 5)), "taken=%s", taken.String())
		require.True(t, remaining.Equals(machine.NewCPUSet(0, 1, 2, 3, 6, 7)), "remaining=%s", remaining.String())
	})

	t.Run("spills from tier1 to tier2 then to remaining available", func(t *testing.T) {
		available := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		// tier1 has 2 cpus, tier2 has 1 cpu, need 5 -> 2 from tier1, 1 from tier2, 2 from remaining
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(4, 5), machine.NewCPUSet(6)}, 5)
		require.NoError(t, err)
		require.Equal(t, 5, taken.Size(), "taken=%s", taken.String())
		require.True(t, taken.Contains(4) && taken.Contains(5) && taken.Contains(6),
			"tiered cpus must be taken first, taken=%s", taken.String())
		require.Equal(t, 3, remaining.Size(), "remaining=%s", remaining.String())
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("ignores preferred cpus outside available", func(t *testing.T) {
		available := machine.NewCPUSet(0, 1, 2, 3)
		// preferred references cpus that are no longer available; must fall back gracefully
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(8, 9)}, 2)
		require.NoError(t, err)
		require.Equal(t, 2, taken.Size())
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("zero request returns empty taken", func(t *testing.T) {
		available := machine.NewCPUSet(0, 1, 2, 3)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available, nil, 0)
		require.NoError(t, err)
		require.True(t, taken.IsEmpty())
		require.True(t, remaining.Equals(available))
	})

	t.Run("insufficient available returns error", func(t *testing.T) {
		available := machine.NewCPUSet(0, 1)
		_, _, err := p.takeByTieredPreferredCPUs(available, []machine.CPUSet{machine.NewCPUSet(0)}, 5)
		require.Error(t, err)
	})
}

func TestBuildIsolationSourcePreferredCPUs(t *testing.T) {
	t.Parallel()

	makeIsolationEntry := func(podUID, cpuset string, ann map[string]string) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				ContainerName: "c",
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-" + podUID,
				Annotations:   ann,
			},
			AllocationResult: machine.MustParse(cpuset),
		}
	}

	entries := state.PodEntries{
		// two isolation pods sharing the same source share pool -> cpusets should union
		"pod1": state.ContainerEntries{"c": makeIsolationEntry("pod1", "8,9", map[string]string{})},
		"pod2": state.ContainerEntries{"c": makeIsolationEntry("pod2", "10", map[string]string{})},
		// a normal share pool entry -> must be ignored (not an isolation entry)
		commonstate.PoolNameShare: state.ContainerEntries{
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.AllocationMeta{OwnerPoolName: commonstate.PoolNameShare},
				AllocationResult: machine.MustParse("0-3"),
			},
		},
		// reclaim pool -> ignored
		commonstate.PoolNameReclaim: state.ContainerEntries{
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.AllocationMeta{OwnerPoolName: commonstate.PoolNameReclaim},
				AllocationResult: machine.MustParse("14,15"),
			},
		},
	}

	preferred := buildIsolationSourcePreferredCPUs(entries)
	require.Contains(t, preferred, commonstate.PoolNameShare)
	require.True(t, preferred[commonstate.PoolNameShare].Equals(machine.NewCPUSet(8, 9, 10)),
		"source share preferred should union both isolation cpusets, got %s",
		preferred[commonstate.PoolNameShare].String())
	require.NotContains(t, preferred, commonstate.PoolNameReclaim)
}

func TestBuildDedicatedSourcePreferredCPUs(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod1": state.ContainerEntries{
			"c": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "c",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					OwnerPoolName: commonstate.PoolNameDedicated,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9),
			},
		},
		"pod2": state.ContainerEntries{
			"c": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod2",
					ContainerName: "c",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					OwnerPoolName: commonstate.PoolNameDedicated,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
					},
				},
				AllocationResult: machine.NewCPUSet(10, 11),
			},
		},
	}

	poolPreferred, containerPreferred := buildDedicatedSourcePreferredCPUs(entries)

	require.Contains(t, poolPreferred, commonstate.PoolNameShare)
	require.True(t, poolPreferred[commonstate.PoolNameShare].Equals(machine.NewCPUSet(8, 9)),
		"source share preferred should include non-NUMA dedicated cpus only, got %s",
		poolPreferred[commonstate.PoolNameShare].String())
	require.Contains(t, containerPreferred, "pod1")
	require.True(t, containerPreferred["pod1"]["c"].Equals(machine.NewCPUSet(8, 9)))
	require.NotContains(t, containerPreferred, "pod2")
}

func TestDynamicPolicy_takeCPUsForContainersWithPreferred(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_takeCPUsForContainersWithPreferred")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	available := machine.NewCPUSet(0, 1, 2, 3, 8, 9)
	containersQuantityMap := map[string]map[string]int{
		"pod1": {"c": 2},
	}
	preferred := map[string]map[string]machine.CPUSet{
		"pod1": {"c": machine.NewCPUSet(8, 9)},
	}

	containersCPUSet, remaining, err := p.takeCPUsForContainersWithPreferred(
		containersQuantityMap, available, preferred)
	require.NoError(t, err)
	require.True(t, containersCPUSet["pod1"]["c"].Equals(machine.NewCPUSet(8, 9)),
		"dedicated container should reuse historical cpuset first, got %s",
		containersCPUSet["pod1"]["c"].String())
	require.True(t, remaining.Equals(machine.NewCPUSet(0, 1, 2, 3)))
}

func TestDynamicPolicy_takeCPUsForPoolsInPlaceWithPreferred(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_takeCPUsForPoolsInPlaceWithPreferred")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	t.Run("source share pool reclaims its historical isolation cpus first", func(t *testing.T) {
		poolsCPUSet := make(map[string]machine.CPUSet)
		available := machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11)
		poolsQuantityMap := map[string]int{commonstate.PoolNameShare: 4}
		// historically isolation carved 8,9,10 from the share pool; on shrink they should come back
		preferred := map[string]machine.CPUSet{commonstate.PoolNameShare: machine.NewCPUSet(8, 9, 10)}

		remaining, err := p.takeCPUsForPoolsInPlaceWithPreferred(
			poolsQuantityMap, poolsCPUSet, available, preferred)
		require.NoError(t, err)

		share := poolsCPUSet[commonstate.PoolNameShare]
		require.Equal(t, 4, share.Size())
		require.True(t, share.Contains(8) && share.Contains(9) && share.Contains(10),
			"share pool should reclaim preferred cpus first, got %s", share.String())
		require.True(t, share.Union(remaining).Equals(available))
		require.True(t, share.Intersection(remaining).IsEmpty())
	})

	t.Run("pools without preferred behave like the legacy take", func(t *testing.T) {
		poolsCPUSet := make(map[string]machine.CPUSet)
		available := machine.NewCPUSet(0, 1, 2, 3)
		poolsQuantityMap := map[string]int{"batch": 2}

		remaining, err := p.takeCPUsForPoolsInPlaceWithPreferred(
			poolsQuantityMap, poolsCPUSet, available, nil)
		require.NoError(t, err)
		require.Equal(t, 2, poolsCPUSet["batch"].Size())
		require.Equal(t, 2, remaining.Size())
	})
}

func TestDynamicPolicy_generateProportionalPoolsCPUSetInPlaceWithPreferred(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generateProportionalPoolsCPUSetInPlaceWithPreferred")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	poolsCPUSet := make(map[string]machine.CPUSet)
	available := machine.NewCPUSet(0, 1, 2, 3, 8, 9)
	poolsQuantityMap := map[string]int{
		commonstate.PoolNameShare:                     4,
		commonstate.PoolNamePrefixIsolation + "-pod1": 2,
	}
	preferred := map[string]machine.CPUSet{
		commonstate.PoolNameShare: machine.NewCPUSet(8, 9),
	}

	remaining, err := p.generateProportionalPoolsCPUSetInPlaceWithPreferred(
		poolsQuantityMap, poolsCPUSet, available, preferred)
	require.NoError(t, err)
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Contains(8))
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Contains(9))
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Intersection(
		poolsCPUSet[commonstate.PoolNamePrefixIsolation+"-pod1"]).IsEmpty())
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].
		Union(poolsCPUSet[commonstate.PoolNamePrefixIsolation+"-pod1"]).
		Union(remaining).
		Equals(available))
}

func TestDeriveIsolationSourceSharePool(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		info       *state.AllocationInfo
		wantSource string
		wantOK     bool
	}{
		{
			name: "ordinary shared_cores isolation -> share",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					Annotations:   map[string]string{},
				},
			},
			wantSource: commonstate.PoolNameShare,
			wantOK:     true,
		},
		{
			name: "shared_cores isolation with cpuset_pool -> that pool",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod2",
					Annotations: map[string]string{
						apiconsts.PodAnnotationCPUEnhancementCPUSet: "batch",
					},
				},
			},
			wantSource: "batch",
			wantOK:     true,
		},
		{
			name: "shared_cores numa_binding isolation -> share-NUMA pool",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod3",
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
					},
				},
			},
			wantSource: commonstate.GetNUMAPoolName(commonstate.PoolNameShare, 1),
			wantOK:     true,
		},
		{
			name: "dedicated_cores is out of phase-1 scope",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					OwnerPoolName: commonstate.PoolNameDedicated,
					Annotations:   map[string]string{},
				},
			},
			wantOK: false,
		},
		{
			name: "numa_binding with invalid multi-numa hint falls back to false",
			info: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod4",
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0-1",
					},
				},
			},
			wantOK: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source, ok := deriveIsolationSourceSharePool(tc.info)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantSource, source)
			}
		})
	}
}

func TestDeriveDedicatedSourceSharePool(t *testing.T) {
	t.Parallel()

	t.Run("dedicated_cores without numa_binding -> share", func(t *testing.T) {
		t.Parallel()
		source, ok := deriveDedicatedSourceSharePool(&state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				OwnerPoolName: commonstate.PoolNameDedicated,
				Annotations:   map[string]string{},
			},
		})
		require.True(t, ok)
		require.Equal(t, commonstate.PoolNameShare, source)
	})

	t.Run("dedicated_cores with numa_binding is out of phase-2 scope", func(t *testing.T) {
		t.Parallel()
		_, ok := deriveDedicatedSourceSharePool(&state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				OwnerPoolName: commonstate.PoolNameDedicated,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
				},
			},
		})
		require.False(t, ok)
	})
}

// TestDynamicPolicy_generatePoolsAndIsolation_reclaimsIsolationCPUs verifies the end-to-end
// behavior: when a shared_cores isolation container already exists in state and its source
// share pool is regenerated, the share pool preferentially reclaims exactly the cpus that
// the isolation historically borrowed, keeping cpuset churn minimal.
func TestDynamicPolicy_generatePoolsAndIsolation_reclaimsIsolationCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_reclaim")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)

	// seed an existing shared_cores isolation container that historically borrowed 8,9,10
	// from the "share" source pool.
	entries := state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9, 10),
			},
		},
	}
	p.state.SetPodEntries(entries, false)

	availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11)
	poolsQuantityMap := map[string]map[int]int{
		commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
	}
	isolatedQuantityMap := map[string]map[string]int{}

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		poolsQuantityMap, isolatedQuantityMap, availableCPUs, map[string]float64{})
	require.NoError(t, err)

	share := poolsCPUSet[commonstate.PoolNameShare]
	require.Equal(t, 4, share.Size(), "share=%s", share.String())
	require.True(t, share.Contains(8) && share.Contains(9) && share.Contains(10),
		"share pool should reclaim the historical isolation cpus 8,9,10 first, got %s", share.String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_overlapReclaimsIsolationCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_overlap_reclaim_isolation")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9),
			},
		},
	}, false)

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNameShare:                     {commonstate.FakedNUMAID: 4},
			commonstate.PoolNamePrefixIsolation + "-pod1": {commonstate.FakedNUMAID: 2},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9),
		map[string]float64{commonstate.PoolNameShare: 0.5})
	require.NoError(t, err)

	share := poolsCPUSet[commonstate.PoolNameShare]
	isolation := poolsCPUSet[commonstate.PoolNamePrefixIsolation+"-pod1"]
	reclaim := poolsCPUSet[commonstate.PoolNameReclaim]
	require.True(t, share.Contains(8) && share.Contains(9),
		"overlap mode should still let source share reclaim historical isolation cpus first, share=%s",
		share.String())
	require.True(t, share.Intersection(isolation).IsEmpty())
	require.True(t, reclaim.Intersection(isolation).IsEmpty(),
		"reclaim overlap should come from share only, reclaim=%s isolation=%s",
		reclaim.String(), isolation.String())
	require.False(t, reclaim.Intersection(share).IsEmpty(),
		"reclaim should still overlap source share according to ratio, reclaim=%s share=%s",
		reclaim.String(), share.String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_reusesDedicatedCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_reuse_dedicated")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9),
			},
		},
	}, false)

	poolsCPUSet, isolatedCPUSet, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4}},
		map[string]map[string]int{"pod1": {"container1": 2}},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11),
		map[string]float64{})
	require.NoError(t, err)

	require.True(t, isolatedCPUSet["pod1"]["container1"].Equals(machine.NewCPUSet(8, 9)),
		"dedicated container should reuse historical cpuset first, got %s",
		isolatedCPUSet["pod1"]["container1"].String())
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Intersection(machine.NewCPUSet(8, 9)).IsEmpty(),
		"source share pool must not overlap with still-active dedicated cpuset, share=%s",
		poolsCPUSet[commonstate.PoolNameShare].String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_overlapReusesDedicatedCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_overlap_reuse_dedicated")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9),
			},
		},
	}, false)

	_, isolatedCPUSet, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4}},
		map[string]map[string]int{"pod1": {"container1": 2}},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9),
		map[string]float64{commonstate.PoolNameShare: 0.5})
	require.NoError(t, err)

	require.True(t, isolatedCPUSet["pod1"]["container1"].Equals(machine.NewCPUSet(8, 9)),
		"overlap mode should let dedicated container reuse historical cpuset first, got %s",
		isolatedCPUSet["pod1"]["container1"].String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_reclaimsDedicatedCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_reclaim_dedicated")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9),
			},
		},
	}, false)

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4}},
		map[string]map[string]int{},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11),
		map[string]float64{})
	require.NoError(t, err)

	share := poolsCPUSet[commonstate.PoolNameShare]
	require.True(t, share.Contains(8) && share.Contains(9),
		"source share pool should reclaim historical dedicated cpus first, got %s", share.String())
}
