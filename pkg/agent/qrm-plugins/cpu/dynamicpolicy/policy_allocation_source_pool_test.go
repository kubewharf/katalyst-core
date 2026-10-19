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

	// GenerateDummyCPUTopology(16,2,2) pairs siblings at +8, so a complete physical
	// core is {x, x+8}. NUMA0 cores: {0,8},{1,9},{2,10},{3,11}; NUMA1 cores:
	// {4,12},{5,13},{6,14},{7,15}. Core-aligned cases below use complete cores
	// so they can verify the prefer-whole behavior independently from the
	// per-CPU fallback exercised by the fragmented-input cases.

	t.Run("prefers the first tier before falling back to available", func(t *testing.T) {
		t.Parallel()

		// two whole cores per NUMA available; tier1 is one whole core.
		available := machine.NewCPUSet(0, 8, 1, 9, 4, 12, 5, 13)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(0, 8), machine.NewCPUSet(1, 9)}, 2)
		require.NoError(t, err)
		require.True(t, taken.Equals(machine.NewCPUSet(0, 8)), "taken=%s", taken.String())
		require.True(t, remaining.Equals(available.Difference(machine.NewCPUSet(0, 8))),
			"remaining=%s", remaining.String())
	})

	t.Run("spills from tier1 to tier2 then to remaining available", func(t *testing.T) {
		t.Parallel()

		// three whole cores per NUMA; need 6 CPUs (3 cores): one core from tier1,
		// one from tier2, one whole core spilled NUMA-balanced from remaining.
		available := machine.NewCPUSet(0, 8, 1, 9, 2, 10, 4, 12, 5, 13, 6, 14)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(0, 8), machine.NewCPUSet(1, 9)}, 6)
		require.NoError(t, err)
		require.Equal(t, 6, taken.Size(), "taken=%s", taken.String())
		// the two preferred whole cores are consumed first.
		require.True(t, taken.Contains(0) && taken.Contains(8) &&
			taken.Contains(1) && taken.Contains(9),
			"tiered whole cores must be taken first, taken=%s", taken.String())
		assertTieredTakenCoreAligned(t, cpuTopology, taken)
		require.Equal(t, 6, remaining.Size(), "remaining=%s", remaining.String())
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("ignores preferred cpus outside available", func(t *testing.T) {
		t.Parallel()

		// one whole core available; preferred references cpus no longer available,
		// so the fallback core-aligned take must still return that whole core.
		available := machine.NewCPUSet(0, 8, 1, 9)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(4, 12)}, 2)
		require.NoError(t, err)
		require.Equal(t, 2, taken.Size())
		assertTieredTakenCoreAligned(t, cpuTopology, taken)
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("zero request returns empty taken", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 1, 9)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available, nil, 0)
		require.NoError(t, err)
		require.True(t, taken.IsEmpty())
		require.True(t, remaining.Equals(available))
	})

	t.Run("prefers whole cores then fills the sub-core tail per-cpu", func(t *testing.T) {
		t.Parallel()

		// non-reclaim source pools prefer whole cores but must never starve: a
		// request of 3 CPUs (not a whole-core multiple on SMT2) takes the whole
		// preferred core {0,8} first, then fills the remaining 1 CPU per-cpu from
		// the leftover sibling. hard core-aligned cropping is reserved for the
		// reclaim pool (planHardReclaimPartition), not the tier layer.
		available := machine.NewCPUSet(0, 8, 1, 9)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available,
			[]machine.CPUSet{machine.NewCPUSet(0, 8)}, 3)
		require.NoError(t, err)
		require.Equal(t, 3, taken.Size(), "prefer-whole-then-fill, taken=%s", taken.String())
		require.True(t, taken.Contains(0) && taken.Contains(8),
			"preferred whole core must be taken first, taken=%s", taken.String())
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("fills orphan half cores only after whole cores are exhausted", func(t *testing.T) {
		t.Parallel()

		// only one complete core {0,8} exists; 1 and 4 are orphan half cores
		// (their siblings 9 and 12 are gone). A request of 4 takes the whole core
		// first, then falls back to filling the two orphans per-cpu — the tier
		// layer never starves a non-reclaim pool for lack of whole cores.
		available := machine.NewCPUSet(0, 8, 1, 4)
		taken, remaining, err := p.takeByTieredPreferredCPUs(available, nil, 4)
		require.NoError(t, err)
		require.Equal(t, 4, taken.Size(), "prefer-whole-then-fill, taken=%s", taken.String())
		require.True(t, taken.Contains(0) && taken.Contains(8),
			"whole core must be taken before orphans, taken=%s", taken.String())
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("does not take a preferred orphan before an available whole core", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 1, 9)
		taken, remaining, err := p.takeByTieredPreferredCPUs(
			available, []machine.CPUSet{machine.NewCPUSet(0)}, 2)
		require.NoError(t, err)
		require.Equal(t, 2, taken.Size(), "taken=%s", taken.String())
		assertTieredTakenCoreAligned(t, cpuTopology, taken)
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("completes a preferred orphan before taking an unrelated whole core", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 2, 10)
		taken, remaining, err := p.takeByTieredPreferredCPUs(
			available, []machine.CPUSet{machine.NewCPUSet(2)}, 2)
		require.NoError(t, err)
		require.True(t, taken.Equals(machine.NewCPUSet(2, 10)), "taken=%s", taken.String())
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("balances fallback against cores already taken from a preferred tier", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 1, 9, 4, 12)
		taken, remaining, err := p.takeByTieredPreferredCPUs(
			available, []machine.CPUSet{machine.NewCPUSet(0, 8)}, 4)
		require.NoError(t, err)
		require.True(t, taken.Contains(0) && taken.Contains(8), "taken=%s", taken.String())
		require.Equal(t, 2, taken.Intersection(cpuTopology.CPUDetails.CPUsInNUMANodes(0)).Size())
		require.Equal(t, 2, taken.Intersection(cpuTopology.CPUDetails.CPUsInNUMANodes(1)).Size())
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("prefers a fully reused core over a lower-id partial hit in the same tier", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 2, 10)
		taken, remaining, err := p.takeByTieredPreferredCPUs(
			available, []machine.CPUSet{machine.NewCPUSet(0, 2, 10)}, 2)
		require.NoError(t, err)
		require.True(t, taken.Equals(machine.NewCPUSet(2, 10)), "taken=%s", taken.String())
		require.True(t, taken.Union(remaining).Equals(available))
	})

	t.Run("does not borrow siblings when the preferred orphan domain is sufficient", func(t *testing.T) {
		t.Parallel()

		available := machine.NewCPUSet(0, 8, 1, 9, 2, 10)
		preferred := machine.NewCPUSet(0, 1, 2)
		taken, remaining, err := p.takeByTieredPreferredCPUs(
			available, []machine.CPUSet{preferred}, 2)
		require.NoError(t, err)
		require.True(t, taken.IsSubsetOf(preferred), "taken=%s preferred=%s", taken.String(), preferred.String())
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("keeps duplicate core ids on different sockets distinct", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(8, 2, 2)
		require.NoError(t, err)
		for cpu, info := range topology.CPUDetails {
			if info.SocketID == 1 {
				info.CoreID -= 2
				topology.CPUDetails[cpu] = info
			}
		}

		policy := &DynamicPolicy{machine: machineComponent{machineInfo: &machine.KatalystMachineInfo{CPUTopology: topology}}}
		available := topology.CPUDetails.CPUs()
		atomicDonor := machine.NewCPUSet(2, 6)

		taken, remaining, err := policy.takeByTieredPreferredCPUs(
			available, nil, topology.CPUsPerCore(), atomicDonor)
		require.NoError(t, err)
		assertTieredTakenCoreAligned(t, topology, taken)
		require.Equal(t, topology.CPUsPerCore(), taken.Size())
		require.True(t, taken.Union(remaining).Equals(available))
		require.True(t, taken.Intersection(remaining).IsEmpty())
	})

	t.Run("sub-core tail keeps protected own history excluded with atomic donor", func(t *testing.T) {
		t.Parallel()

		topology, err := machine.GenerateDummyCPUTopology(8, 2, 2)
		require.NoError(t, err)
		policy := &DynamicPolicy{machine: machineComponent{machineInfo: &machine.KatalystMachineInfo{CPUTopology: topology}}}
		available := machine.NewCPUSet(0, 4, 1, 5, 2, 3)
		protected := machine.NewCPUSet(2)
		donorClosure, err := policy.atomicDonorCoreClosure(machine.NewCPUSet(1, 5))
		require.NoError(t, err)

		taken, remaining, err := policy.takeByTieredPreferredCPUsWithProtected(
			available, []machine.CPUSet{protected}, 3, protected, donorClosure)
		require.NoError(t, err)
		require.True(t, taken.Intersection(protected).IsEmpty(),
			"sub-core tail must not re-admit protected history: taken=%s", taken.String())
		require.True(t, remaining.Intersection(protected).Equals(protected))
		require.True(t, taken.Union(remaining).Equals(available))
	})
}

// assertTieredTakenCoreAligned fails when a core-aligned test case returns a
// partial physical core.
func assertTieredTakenCoreAligned(t *testing.T, topology *machine.CPUTopology, taken machine.CPUSet) {
	t.Helper()
	cpusPerCore := topology.CPUsPerCore()
	type physicalCoreID struct {
		numaID   int
		socketID int
		coreID   int
	}
	byCore := make(map[physicalCoreID]int)
	for _, cpu := range taken.ToSliceInt() {
		info := topology.CPUDetails[cpu]
		byCore[physicalCoreID{
			numaID:   info.NUMANodeID,
			socketID: info.SocketID,
			coreID:   info.CoreID,
		}]++
	}
	for core, cnt := range byCore {
		if cnt != cpusPerCore {
			t.Fatalf("core %+v has %d/%d siblings in taken %s (half core)",
				core, cnt, cpusPerCore, taken.String())
		}
	}
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

	snapshot := buildPoolPlacementSnapshot(
		entries, nil,
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 1}},
		nil, machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11, 14, 15), false)
	preferred := snapshot.borrowedByPool
	require.Contains(t, preferred, commonstate.PoolNameShare)
	require.True(t, preferred[commonstate.PoolNameShare].Equals(machine.NewCPUSet(8, 9, 10)),
		"source share preferred should union both isolation cpusets, got %s",
		preferred[commonstate.PoolNameShare].String())
	require.NotContains(t, preferred, commonstate.PoolNameReclaim)
}

func TestBuildDedicatedSourcePreferredCPUs(t *testing.T) {
	t.Parallel()

	makeDedicatedEntry := func(podUID, containerName, cpuset string, ann map[string]string) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				ContainerName: containerName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				OwnerPoolName: commonstate.PoolNameDedicated,
				Annotations:   ann,
			},
			AllocationResult: machine.MustParse(cpuset),
		}
	}

	entries := state.PodEntries{
		"pod1": state.ContainerEntries{
			"c1": makeDedicatedEntry("pod1", "c1", "8,9", map[string]string{}),
			// NUMA-binding dedicated keeps the legacy path and must not feed the
			// non-binding dedicated source-share preference.
			"c2": makeDedicatedEntry("pod1", "c2", "10,11", map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			}),
		},
		"pod2": state.ContainerEntries{
			"c1": makeDedicatedEntry("pod2", "c1", "12", map[string]string{}),
			"c2": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod2",
					ContainerName: "c2",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					OwnerPoolName: commonstate.PoolNameDedicated,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(),
			},
		},
		commonstate.PoolNameDedicated: state.ContainerEntries{
			commonstate.FakedContainerName: makeDedicatedEntry(commonstate.PoolNameDedicated, commonstate.FakedContainerName, "0-3", map[string]string{}),
		},
	}

	snapshot := buildPoolPlacementSnapshot(
		entries, nil,
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 1}},
		map[string]map[string]int{"pod1": {"c1": 2}, "pod2": {"c1": 1}},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11, 12), false)
	preferredByPool, preferredByContainer := snapshot.borrowedByPool, snapshot.dedicatedByContainer

	require.Contains(t, preferredByPool, commonstate.PoolNameShare)
	require.True(t, preferredByPool[commonstate.PoolNameShare].Equals(machine.NewCPUSet(8, 9, 12)),
		"share pool should reclaim only non-NUMA-binding dedicated cpus, got %s",
		preferredByPool[commonstate.PoolNameShare].String())
	require.True(t, preferredByContainer["pod1"]["c1"].Equals(machine.NewCPUSet(8, 9)))
	require.True(t, preferredByContainer["pod2"]["c1"].Equals(machine.NewCPUSet(12)))
	require.NotContains(t, preferredByContainer["pod1"], "c2")
	require.NotContains(t, preferredByContainer["pod2"], "c2")
	require.NotContains(t, preferredByContainer, commonstate.PoolNameDedicated)
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
		"container should take preferred cpuset first, got %s",
		containersCPUSet["pod1"]["c"].String())
	require.True(t, remaining.Equals(machine.NewCPUSet(0, 1, 2, 3)))
}

func TestDynamicPolicy_takeCPUsForPoolsInPlaceWithTiersSinglePreference(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_takeCPUsForPoolsInPlaceWithPreferred")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	t.Run("source share pool prefers cores containing historical isolation cpus", func(t *testing.T) {
		t.Parallel()

		poolsCPUSet := make(map[string]machine.CPUSet)
		available := machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11)
		poolsQuantityMap := map[string]int{commonstate.PoolNameShare: 4}
		// Historical isolation carved 8,9,10 from three different physical cores.
		// A 4-CPU pool can reuse at most two of them while staying core-aligned.
		preferred := map[string]machine.CPUSet{commonstate.PoolNameShare: machine.NewCPUSet(8, 9, 10)}

		remaining, err := p.takeCPUsForPoolsInPlaceWithTiers(
			poolsQuantityMap, poolsCPUSet, available,
			map[string][]machine.CPUSet{commonstate.PoolNameShare: {preferred[commonstate.PoolNameShare]}}, nil)
		require.NoError(t, err)

		share := poolsCPUSet[commonstate.PoolNameShare]
		require.Equal(t, 4, share.Size())
		assertTieredTakenCoreAligned(t, cpuTopology, share)
		require.Equal(t, 2, share.Intersection(preferred[commonstate.PoolNameShare]).Size(),
			"share pool should maximize historical CPU reuse within complete cores, got %s", share.String())
		require.True(t, share.Union(remaining).Equals(available))
		require.True(t, share.Intersection(remaining).IsEmpty())
	})

	t.Run("pools without preferred behave like the legacy take", func(t *testing.T) {
		t.Parallel()

		poolsCPUSet := make(map[string]machine.CPUSet)
		available := machine.NewCPUSet(0, 1, 2, 3)
		poolsQuantityMap := map[string]int{"batch": 2}

		remaining, err := p.takeCPUsForPoolsInPlaceWithTiers(
			poolsQuantityMap, poolsCPUSet, available, nil, nil)
		require.NoError(t, err)
		require.Equal(t, 2, poolsCPUSet["batch"].Size())
		require.Equal(t, 2, remaining.Size())
	})

	t.Run("reclaim keeps historical cpus before share preferred cpus", func(t *testing.T) {
		t.Parallel()

		poolsCPUSet := make(map[string]machine.CPUSet)
		available := machine.NewCPUSet(0, 1, 8, 9)
		poolsQuantityMap := map[string]int{
			commonstate.PoolNameShare:   2,
			commonstate.PoolNameReclaim: 2,
		}
		preferred := map[string]machine.CPUSet{
			commonstate.PoolNameShare:   machine.NewCPUSet(0, 8),
			commonstate.PoolNameReclaim: machine.NewCPUSet(0, 8),
		}

		remaining, err := p.takeCPUsForPoolsInPlaceWithTiers(
			poolsQuantityMap, poolsCPUSet, available,
			map[string][]machine.CPUSet{
				commonstate.PoolNameShare:   {preferred[commonstate.PoolNameShare]},
				commonstate.PoolNameReclaim: {preferred[commonstate.PoolNameReclaim]},
			}, nil)
		require.NoError(t, err)

		require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Equals(machine.NewCPUSet(0, 8)),
			"reclaim pool should keep its historical cpuset before share consumes preferred cpus, got %s",
			poolsCPUSet[commonstate.PoolNameReclaim].String())
		require.True(t, poolsCPUSet[commonstate.PoolNameShare].Equals(machine.NewCPUSet(1, 9)),
			"share pool should fall back after reclaim keeps historical cpus, got %s",
			poolsCPUSet[commonstate.PoolNameShare].String())
		require.True(t, remaining.IsEmpty())
	})
}

func TestDynamicPolicy_generateProportionalPoolsCPUSetInPlaceWithTiersSinglePreference(t *testing.T) {
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

	remaining, err := p.generateProportionalPoolsCPUSetInPlaceWithTiers(
		poolsQuantityMap, poolsCPUSet, available,
		map[string][]machine.CPUSet{commonstate.PoolNameShare: {preferred[commonstate.PoolNameShare]}}, nil)
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

// TestDynamicPolicy_generatePoolsAndIsolation_reclaimsIsolationCPUs verifies the
// end-to-end behavior: when a shared_cores isolation container already exists in
// state and its source share pool is regenerated, the share pool prefers complete
// cores containing CPUs that the isolation historically borrowed.
func TestDynamicPolicy_generatePoolsAndIsolation_reclaimsIsolationCPUs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_reclaim")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
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
		poolsQuantityMap, isolatedQuantityMap, availableCPUs, map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()})
	require.NoError(t, err)

	share := poolsCPUSet[commonstate.PoolNameShare]
	require.Equal(t, 4, share.Size(), "share=%s", share.String())
	assertTieredTakenCoreAligned(t, cpuTopology, share)
	require.Equal(t, 2, share.Intersection(machine.NewCPUSet(8, 9, 10)).Size(),
		"two complete cores can preserve at most two historical orphan CPUs, got %s", share.String())
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

	p.config.reservedCPUs = machine.NewCPUSet()
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
		map[string]float64{commonstate.PoolNameShare: 0.5},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()})
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
