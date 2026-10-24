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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func TestPoolPlacementSnapshotScopesSharedIsolationHistoryToPinnedPackage(t *testing.T) {
	t.Parallel()

	const packageName = "pkg-a"
	wrappedPool := rputil.WrapOwnerPoolName("share-NUMA0", packageName)
	history := machine.NewCPUSet(2, 10)
	entries := state.PodEntries{
		"pod-a": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod-a",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationResourcePackageKey:           packageName,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
					},
				},
				AllocationResult: history.Clone(),
			},
		},
	}
	pinned := map[int]map[string]machine.CPUSet{
		0: {packageName: machine.NewCPUSet(0, 2, 8, 10)},
	}
	packageScope := buildPoolPlacementSnapshot(
		entries, pinned, map[string]map[int]int{wrappedPool: {0: 2}}, nil,
		machine.NewCPUSet(0, 2, 8, 10), false)
	require.True(t, packageScope.borrowedByPool[wrappedPool].Equals(history))
	require.NotContains(t, packageScope.borrowedByPool, "share-NUMA0")

	commonScope := buildPoolPlacementSnapshot(
		entries, pinned, map[string]map[int]int{"share-NUMA0": {0: 2}}, nil,
		machine.NewCPUSet(1, 3, 9, 11), false)
	require.NotContains(t, commonScope.borrowedByPool, wrappedPool)
	require.NotContains(t, commonScope.borrowedByPool, "share-NUMA0")
}

func TestGroupAndAllocatePoolsKeepsPackagesDisjointWithinSameNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()

	poolA := rputil.WrapOwnerPoolName("share-NUMA0", "pkg-a")
	poolB := rputil.WrapOwnerPoolName("share-NUMA0", "pkg-b")
	pinnedA := machine.NewCPUSet(0, 8)
	pinnedB := machine.NewCPUSet(1, 9)
	pools, _, err := p.groupAndAllocatePoolsWithPlacement(
		map[string]map[int]int{poolA: {0: 2}, poolB: {0: 2}}, nil,
		pinnedA.Union(pinnedB),
		map[string]machine.CPUSet{"pkg-a": pinnedA, "pkg-b": pinnedB},
		map[string]float64{}, p.config.dynamicConfig.GetDynamicConfiguration(),
		state.PodEntries{}, nil)
	require.NoError(t, err)
	require.True(t, pools[poolA].Equals(pinnedA), "pool-a=%s", pools[poolA])
	require.True(t, pools[poolB].Equals(pinnedB), "pool-b=%s", pools[poolB])
	require.True(t, pools[poolA].Intersection(pools[poolB]).IsEmpty())
}

func TestGroupAndAllocatePoolsKeepsLegacyPackageNonBindingQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))

	poolName := rputil.WrapOwnerPoolName("pool", "pkg")
	pinned := machine.NewCPUSet(0, 1, 2, 3)
	pools, _, err := p.groupAndAllocatePoolsWithPlacement(
		map[string]map[int]int{poolName: {commonstate.FakedNUMAID: 2}}, nil,
		pinned, map[string]machine.CPUSet{"pkg": pinned}, map[string]float64{},
		p.config.dynamicConfig.GetDynamicConfiguration(), state.PodEntries{}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, pools[poolName].Size(), "pool=%s", pools[poolName])
	require.True(t, pools[poolName].IsSubsetOf(pinned))
}

func TestGroupAndAllocatePoolsRejectsWrappedPoolWithoutPinnedPackage(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()

	poolName := rputil.WrapOwnerPoolName("share-NUMA0", "missing")
	_, _, err = p.groupAndAllocatePoolsWithPlacement(
		map[string]map[int]int{poolName: {0: 2}}, nil,
		machine.NewCPUSet(0, 1, 2, 3), nil, map[string]float64{},
		p.config.dynamicConfig.GetDynamicConfiguration(), state.PodEntries{}, nil)
	require.ErrorContains(t, err, "has no pinned cpuset")
}

func TestPoolPlacementSnapshotScopesOwnHistoryByRequestedPoolAndAvailability(t *testing.T) {
	t.Parallel()

	const packageName = "pkg-a"
	wrappedPool := rputil.WrapOwnerPoolName("share-NUMA0", packageName)
	wrappedHistory := machine.NewCPUSet(0, 8)
	commonHistory := machine.NewCPUSet(1, 9)
	entries := state.PodEntries{
		wrappedPool: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(wrappedPool),
				AllocationResult: wrappedHistory.Clone(),
			},
		},
		"share-NUMA0": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("share-NUMA0"),
				AllocationResult: commonHistory.Clone(),
			},
		},
	}
	packageScope := buildPoolPlacementSnapshot(
		entries, nil, map[string]map[int]int{wrappedPool: {0: 2}}, nil,
		machine.NewCPUSet(0, 8), false)
	require.True(t, packageScope.ownByPool[wrappedPool].Equals(wrappedHistory))
	require.NotContains(t, packageScope.ownByPool, "share-NUMA0")

	commonScope := buildPoolPlacementSnapshot(
		entries, nil, map[string]map[int]int{"share-NUMA0": {0: 2}}, nil,
		machine.NewCPUSet(1, 9), false)
	require.True(t, commonScope.ownByPool["share-NUMA0"].Equals(commonHistory))
	require.NotContains(t, commonScope.ownByPool, wrappedPool)
}
