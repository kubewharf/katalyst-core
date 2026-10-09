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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestPartitionResidualSignatureIsOrderIndependentAndComplete(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	base := []partitionDemand{
		{
			key: "a", requestGroupKey: "pod-a/main", quantity: 1,
			requestQuantity: 1.5, eligible: machine.NewCPUSet(0, 1),
			preferred: machine.NewCPUSet(1), class: advisorBlockClassDedicated,
		},
		{
			key: "b", requestGroupKey: "pod-b/main", quantity: 1,
			requestQuantity: 1, eligible: machine.NewCPUSet(2, 3),
			preferred: machine.NewCPUSet(2), class: advisorBlockClassShared,
		},
	}

	first, err := newCanonicalPartitionResidual(base, topology)
	require.NoError(t, err)
	second, err := newCanonicalPartitionResidual(
		[]partitionDemand{base[1], base[0]}, topology)
	require.NoError(t, err)
	require.Equal(t, first.payload, second.payload)
	require.Equal(t, first.digest, second.digest)
	require.Contains(t, first.payload, `"key":"a"`)

	demandMutations := map[string]func([]partitionDemand){
		"key":               func(in []partitionDemand) { in[0].key = "changed" },
		"request-group-key": func(in []partitionDemand) { in[0].requestGroupKey = "changed/main" },
		"quantity":          func(in []partitionDemand) { in[0].quantity++ },
		"request-quantity":  func(in []partitionDemand) { in[0].requestQuantity = 2 },
		"eligible":          func(in []partitionDemand) { in[0].eligible = machine.NewCPUSet(0) },
		"preferred":         func(in []partitionDemand) { in[0].preferred = machine.NewCPUSet(0) },
		"class":             func(in []partitionDemand) { in[0].class = advisorBlockClassShared },
	}
	for name, mutate := range demandMutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := append([]partitionDemand(nil), base...)
			mutate(changed)
			canonical, signatureErr := newCanonicalPartitionResidual(changed, topology)
			require.NoError(t, signatureErr)
			require.NotEqual(t, first.payload, canonical.payload)
		})
	}
}

func TestPartitionResidualSolveCacheReturnsDeepCopies(t *testing.T) {
	t.Parallel()
	cache, err := newPartitionResidualSolveCache(partitionSolverFixtureTopology())
	require.NoError(t, err)
	demands := []partitionDemand{{
		key: "same-input", quantity: 1, eligible: machine.NewCPUSet(0),
		class: advisorBlockClassDedicated,
	}}
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{demands, demands}))
	searchBudget := defaultPartitionSearchBudget()
	first, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.False(t, hit)
	first["same-input"].Add(1)
	first["same-input"] = machine.NewCPUSet(7)

	second, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, machine.NewCPUSet(0), second["same-input"])
}

func TestSolvePartitionResidualCachedHitsEquivalentSignature(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{
		{key: "a", quantity: 1, eligible: machine.NewCPUSet(0, 1), class: advisorBlockClassDedicated},
		{key: "b", quantity: 1, eligible: machine.NewCPUSet(2, 3), class: advisorBlockClassShared},
	}
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
		demands,
		{demands[1], demands[0]},
		demands,
	}))
	searchBudget := defaultPartitionSearchBudget()

	first, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.False(t, hit)
	flowAfterMiss := searchBudget.flowOperations

	second, hit, _, err := solvePartitionResidualCached(
		[]partitionDemand{demands[1], demands[0]},
		cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, flowAfterMiss, searchBudget.flowOperations)
	require.Equal(t, first, second)

	first["a"] = machine.NewCPUSet(7)
	third, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, second, third)
}

func TestPartitionResidualCanonicalPayloadPreservesCPURankCost(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	firstDemands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: machine.NewCPUSet(0),
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: machine.NewCPUSet(1, 2, 3),
			class: advisorBlockClassShared,
		},
	}
	secondDemands := []partitionDemand{
		{
			key: "reclaim", quantity: 1, eligible: machine.NewCPUSet(1),
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "shared", quantity: 1, eligible: machine.NewCPUSet(0, 2, 3),
			class: advisorBlockClassShared,
		},
	}
	firstCanonical, err := newCanonicalPartitionResidual(firstDemands, topology)
	require.NoError(t, err)
	secondCanonical, err := newCanonicalPartitionResidual(secondDemands, topology)
	require.NoError(t, err)
	require.NotEqual(t, firstCanonical.payload, secondCanonical.payload,
		"cpuRank contributes to partitionEdgeCost, so a rank permutation is not equivalent")
	require.NotEqual(t, firstCanonical.digest, secondCanonical.digest)

	oldWeight, reclaimWeight, topologyWeight, err := partitionCostWeights(2, 4, 2)
	require.NoError(t, err)
	firstRankCost, err := partitionEdgeCost(
		0, 0, 0, firstDemands[0], machine.NewCPUSet(), topology,
		oldWeight, reclaimWeight, topologyWeight, 2, true)
	require.NoError(t, err)
	secondRankCost, err := partitionEdgeCost(
		1, 1, 0, secondDemands[0], machine.NewCPUSet(), topology,
		oldWeight, reclaimWeight, topologyWeight, 2, true)
	require.NoError(t, err)
	require.NotEqual(t, firstRankCost, secondRankCost)
}

func TestSolvePartitionResidualCachedCachesOnlySemanticInfeasibility(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()

	t.Run("semantic-infeasibility", func(t *testing.T) {
		t.Parallel()
		demands := []partitionDemand{
			{key: "a", quantity: 1, eligible: machine.NewCPUSet(0), class: advisorBlockClassDedicated},
			{key: "b", quantity: 1, eligible: machine.NewCPUSet(0), class: advisorBlockClassShared},
			{key: "c", quantity: 1, eligible: machine.NewCPUSet(1, 2), class: advisorBlockClassShared},
		}
		cache, cacheErr := newPartitionResidualSolveCache(topology)
		require.NoError(t, cacheErr)
		require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
			demands,
			{demands[2], demands[1], demands[0]},
		}))
		searchBudget := defaultPartitionSearchBudget()

		_, hit, _, err := solvePartitionResidualCached(
			demands, cache, defaultPartitionGraphBudget(), &searchBudget)
		require.ErrorIs(t, err, errPartitionNoFeasibleAssignment)
		require.False(t, hit)
		flowAfterMiss := searchBudget.flowOperations
		require.Positive(t, flowAfterMiss)

		_, hit, _, err = solvePartitionResidualCached(
			[]partitionDemand{demands[2], demands[1], demands[0]},
			cache, defaultPartitionGraphBudget(), &searchBudget)
		require.ErrorIs(t, err, errPartitionNoFeasibleAssignment)
		require.True(t, hit)
		require.Equal(t, flowAfterMiss, searchBudget.flowOperations,
			"repeated semantic infeasibility must consume flow only once")
		require.Len(t, cache.entries, 1)
	})

	t.Run("budget-error", func(t *testing.T) {
		t.Parallel()
		demands := []partitionDemand{{
			key: "a", quantity: 1, eligible: machine.NewCPUSet(0, 1),
			class: advisorBlockClassDedicated,
		}}
		cache, cacheErr := newPartitionResidualSolveCache(topology)
		require.NoError(t, cacheErr)
		require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
			demands, demands,
		}))
		searchBudget := defaultPartitionSearchBudget()

		for i := 0; i < 2; i++ {
			_, hit, _, err := solvePartitionResidualCached(
				demands, cache,
				partitionGraphBudget{maxAssignmentEdges: 1}, &searchBudget)
			require.ErrorIs(t, err, errPartitionAssignmentEdgeBudget)
			require.False(t, hit)
		}
		require.Empty(t, cache.entries)
	})

	t.Run("input-error", func(t *testing.T) {
		t.Parallel()
		demands := []partitionDemand{{
			key: "a", quantity: -1, eligible: machine.NewCPUSet(0),
			class: advisorBlockClassDedicated,
		}}
		cache, cacheErr := newPartitionResidualSolveCache(topology)
		require.NoError(t, cacheErr)
		require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
			demands, demands,
		}))
		searchBudget := defaultPartitionSearchBudget()

		for i := 0; i < 2; i++ {
			_, hit, _, err := solvePartitionResidualCached(
				demands, cache, defaultPartitionGraphBudget(), &searchBudget)
			require.ErrorContains(t, err, "negative quantity")
			require.False(t, hit)
		}
		require.Empty(t, cache.entries)
	})

	t.Run("internal-error", func(t *testing.T) {
		t.Parallel()
		demands := []partitionDemand{{
			key: "a", quantity: 1, eligible: machine.NewCPUSet(99),
			class: advisorBlockClassDedicated,
		}}
		cache, cacheErr := newPartitionResidualSolveCache(topology)
		require.NoError(t, cacheErr)
		searchBudget := defaultPartitionSearchBudget()

		for i := 0; i < 2; i++ {
			_, hit, _, err := solvePartitionResidualCached(
				demands, cache, defaultPartitionGraphBudget(), &searchBudget)
			require.ErrorContains(t, err, "missing from topology")
			require.False(t, hit)
		}
		require.Empty(t, cache.entries)
	})
}

func TestPartitionResidualSolveCacheBindsImmutableTopologySnapshot(t *testing.T) {
	t.Parallel()
	_, err := newPartitionResidualSolveCache(nil)
	require.EqualError(t, err, "partition topology is nil")

	topology := partitionSolverFixtureTopology()
	topology.CPUInfo = &machine.CPUInfo{}
	topology.NUMAToCPUs = machine.NUMANodeInfo{
		0: topology.CPUDetails.CPUsInNUMANodes(0),
	}
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)

	info := topology.CPUDetails[0]
	info.NUMANodeID++
	topology.CPUDetails[0] = info
	topology.NUMAToCPUs[0].Add(99)
	topology.NumCPUs++

	require.Equal(t, partitionSolverFixtureTopology().CPUDetails, cache.topology.CPUDetails)
	require.False(t, cache.topology.NUMAToCPUs[0].Contains(99))
	cache.topology.NUMAToCPUs[0].Add(98)
	require.False(t, topology.NUMAToCPUs[0].Contains(98))
	require.Nil(t, cache.topology.CPUInfo,
		"CPUInfo is not consumed by the partition solver and must not remain aliased")
}

func TestPartitionResidualSolveCacheUsesDigestOnlyAsBucket(t *testing.T) {
	t.Parallel()
	cache, err := newPartitionResidualSolveCache(partitionSolverFixtureTopology())
	require.NoError(t, err)
	first, err := newCanonicalPartitionResidual([]partitionDemand{{
		key: "first", quantity: 1, eligible: machine.NewCPUSet(0, 1),
		class: advisorBlockClassDedicated,
	}}, cache.topology)
	require.NoError(t, err)
	second, err := newCanonicalPartitionResidual([]partitionDemand{{
		key: "second", quantity: 1, eligible: machine.NewCPUSet(0, 1),
		class: advisorBlockClassDedicated,
	}}, cache.topology)
	require.NoError(t, err)
	second.digest = first.digest // deterministic collision injection
	cache.repeatedDigests[first.digest] = struct{}{}

	require.NoError(t, cache.storeCanonicalSuccess(first, map[string]machine.CPUSet{
		"first": machine.NewCPUSet(0),
	}))
	_, found, err := cache.lookupCanonical(second)
	require.NoError(t, err)
	require.False(t, found, "digest collisions require exact canonical payload equality")
}

func TestPartitionResidualSolveCacheRetainsOnlyTrueRepeatsWithoutEviction(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)

	const uniqueCount = 4097
	repeated := []partitionDemand{{
		key: "repeated", quantity: 1, eligible: machine.NewCPUSet(0, 1),
		class: advisorBlockClassDedicated,
	}}
	all := make([][]partitionDemand, 0, uniqueCount+1)
	all = append(all, repeated)
	for i := 0; i < uniqueCount; i++ {
		all = append(all, []partitionDemand{{
			key: fmt.Sprintf("demand-%d", i), quantity: 1,
			eligible: machine.NewCPUSet(0, 1), class: advisorBlockClassDedicated,
		}})
	}
	all = append(all, repeated)
	require.NoError(t, preparePartitionResidualCache(cache, all))

	oracleBudget := defaultPartitionSearchBudget()
	for _, demands := range all[:len(all)-1] {
		graphBudget := defaultPartitionGraphBudget()
		_, solveErr := solveDisjointPartitionsWithBudgets(
			demands, topology, &graphBudget, &oracleBudget)
		require.NoError(t, solveErr)
	}

	cachedBudget := defaultPartitionSearchBudget()
	var last map[string]machine.CPUSet
	for i, demands := range all {
		var hit bool
		last, hit, _, err = solvePartitionResidualCached(
			demands, cache, defaultPartitionGraphBudget(), &cachedBudget)
		require.NoError(t, err)
		require.Equal(t, i == len(all)-1, hit)
	}
	require.Equal(t, oracleBudget.flowOperations, cachedBudget.flowOperations,
		"a hit after more than 4096 unique graphs must not alter flow-budget order")
	require.Equal(t, machine.NewCPUSet(0), last["repeated"])
	require.Len(t, cache.entries, 1,
		"unique graphs retain only fixed-size digests, not full payloads or results")
}

func TestPartitionResidualCacheCapacityDoesNotRejectSolvableDenseGraph(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(2000, 1, 1)
	require.NoError(t, err)
	demands := make([]partitionDemand, 100)
	for i := range demands {
		eligible := machine.NewCPUSet()
		for cpu := i * 20; cpu < (i+1)*20; cpu++ {
			eligible.Add(cpu)
		}
		demands[i] = partitionDemand{
			key:      fmt.Sprintf("demand-%03d", i),
			quantity: 1,
			eligible: eligible,
			class:    advisorBlockClassShared,
		}
	}
	equivalent := append([]partitionDemand(nil), demands...)
	for left, right := 0, len(equivalent)-1; left < right; left, right = left+1, right-1 {
		equivalent[left], equivalent[right] = equivalent[right], equivalent[left]
	}

	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
		demands, equivalent,
	}), "cache capacity must not turn a solvable graph into an error")
	require.Greater(t, cache.usage.retainedBytes, 4<<20,
		"regression fixture must exceed the removed arbitrary per-payload limit")

	searchBudget := defaultPartitionSearchBudget()
	first, hit, graphBudget, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.False(t, hit)
	require.Len(t, first, len(demands))
	require.LessOrEqual(t, graphBudget.assignmentEdges, graphBudget.maxAssignmentEdges)
	require.LessOrEqual(t, searchBudget.flowOperations, searchBudget.maxFlowOperations)
	flowAfterMiss := searchBudget.flowOperations

	second, hit, graphBudget, err := solvePartitionResidualCached(
		equivalent, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, first, second)
	require.LessOrEqual(t, graphBudget.assignmentEdges, graphBudget.maxAssignmentEdges)
	require.Equal(t, flowAfterMiss, searchBudget.flowOperations,
		"equivalent fallback must not solve the same flow graph twice")
}

func TestPartitionResidualCachePreparationHasDeterministicInvocationBounds(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{{
		key: "repeat", quantity: 1, eligible: machine.NewCPUSet(0, 1),
		class: advisorBlockClassDedicated,
	}}

	t.Run("work", func(t *testing.T) {
		t.Parallel()
		var messages []string
		for i := 0; i < 2; i++ {
			cache, err := newPartitionResidualSolveCache(topology)
			require.NoError(t, err)
			cache.limits.maxPreparationGraphs = 1
			err = preparePartitionResidualCache(cache, [][]partitionDemand{demands, demands})
			require.ErrorIs(t, err, errPartitionResidualPreparationWorkBudget)
			messages = append(messages, err.Error())
			require.Empty(t, cache.entries)
			require.Empty(t, cache.repeatedPayloads)
		}
		require.Equal(t, messages[0], messages[1])
		require.Contains(t, messages[0], "graphs: used 2, limit 1")
	})

	t.Run("canonical-work", func(t *testing.T) {
		t.Parallel()
		cache, err := newPartitionResidualSolveCache(topology)
		require.NoError(t, err)
		cache.limits.maxCanonicalWork = 1
		err = preparePartitionResidualCache(cache, [][]partitionDemand{demands})
		require.ErrorIs(t, err, errPartitionResidualPreparationWorkBudget)
		require.Contains(t, err.Error(), "canonical work units")
		require.Equal(t, 1, cache.usage.preparationGraphs)
		require.Empty(t, cache.entries)
		require.Empty(t, cache.repeatedPayloads)
	})

	t.Run("retained-memory", func(t *testing.T) {
		t.Parallel()
		cache, err := newPartitionResidualSolveCache(topology)
		require.NoError(t, err)
		cache.limits.maxRetainedBytes = 1
		err = preparePartitionResidualCache(cache, [][]partitionDemand{demands, demands})
		require.ErrorIs(t, err, errPartitionResidualRetainedMemoryBudget)
		require.Contains(t, err.Error(), "canonical payload bytes")
		require.Empty(t, cache.entries)
		require.Empty(t, cache.repeatedPayloads)
	})
}

func TestPartitionResidualCacheBoundsRetainedResults(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{
		{key: "a", quantity: 1, eligible: machine.NewCPUSet(0, 1), class: advisorBlockClassDedicated},
		{key: "b", quantity: 1, eligible: machine.NewCPUSet(2, 3), class: advisorBlockClassShared},
	}
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{demands, demands}))
	cache.limits.maxResultUnits = 1
	searchBudget := defaultPartitionSearchBudget()

	_, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.ErrorIs(t, err, errPartitionResidualRetainedMemoryBudget)
	require.False(t, hit)
	require.Empty(t, cache.entries)
}

func TestPartitionResidualCacheEntryReusesPreparedPayloadOwnership(t *testing.T) {
	t.Parallel()
	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{{
		key: "same-input", quantity: 1, eligible: machine.NewCPUSet(0, 1),
		class: advisorBlockClassDedicated,
	}}
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{
		demands, demands,
	}))
	canonical, err := newCanonicalPartitionResidual(demands, topology)
	require.NoError(t, err)
	prepared := cache.repeatedPayloads[canonical.digest][canonical.payload]
	require.NotNil(t, prepared)
	require.Equal(t, len(prepared.payload), cache.usage.retainedBytes)
	preparedBytes := cache.usage.retainedBytes

	searchBudget := defaultPartitionSearchBudget()
	_, hit, _, err := solvePartitionResidualCached(
		demands, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.False(t, hit)
	require.Len(t, cache.entries[canonical.digest], 1)
	entry := cache.entries[canonical.digest][0]
	require.Same(t, prepared, entry.prepared,
		"prepared exact key and cached result must share one payload owner")

	resultBytes := 0
	for demandKey, counts := range entry.result.canonicalAssignments {
		resultBytes += len(demandKey)
		for classKey := range counts {
			resultBytes += len(classKey)
		}
	}
	require.Equal(t, preparedBytes+resultBytes, cache.usage.retainedBytes,
		"retained bytes must count the shared payload once and every retained result key")
}

func TestPartitionResidualCacheDifferentialOracleAcrossCPUIdentityNUMASMTAndTie(t *testing.T) {
	t.Parallel()
	topology := &machine.CPUTopology{
		NumCPUs:      12,
		NumCores:     6,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0:  {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1:  {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2:  {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3:  {NUMANodeID: 1, SocketID: 1, CoreID: 3},
			4:  {NUMANodeID: 1, SocketID: 1, CoreID: 4},
			5:  {NUMANodeID: 1, SocketID: 1, CoreID: 5},
			6:  {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			7:  {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			8:  {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			9:  {NUMANodeID: 1, SocketID: 1, CoreID: 3},
			10: {NUMANodeID: 1, SocketID: 1, CoreID: 4},
			11: {NUMANodeID: 1, SocketID: 1, CoreID: 5},
		},
	}
	first := []partitionDemand{
		{
			key: "smt", quantity: 1, eligible: machine.NewCPUSet(0, 6),
			preferred: machine.NewCPUSet(0), class: advisorBlockClassDedicated,
		},
		{
			key: "numa", quantity: 1, eligible: machine.NewCPUSet(3, 4),
			preferred: machine.NewCPUSet(3), class: advisorBlockClassShared,
		},
	}
	second := []partitionDemand{
		{
			key: "numa", quantity: 1, eligible: machine.NewCPUSet(4, 5),
			preferred: machine.NewCPUSet(4), class: advisorBlockClassShared,
		},
		{
			key: "smt", quantity: 1, eligible: machine.NewCPUSet(1, 7),
			preferred: machine.NewCPUSet(1), class: advisorBlockClassDedicated,
		},
	}
	firstCanonical, err := newCanonicalPartitionResidual(first, topology)
	require.NoError(t, err)
	secondCanonical, err := newCanonicalPartitionResidual(second, topology)
	require.NoError(t, err)
	require.Equal(t, firstCanonical.payload, secondCanonical.payload,
		"equivalent topology features must cross concrete CPU identity")

	uncachedFirst, err := solveDisjointPartitions(first, topology)
	require.NoError(t, err)
	uncachedSecond, err := solveDisjointPartitions(second, topology)
	require.NoError(t, err)

	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	require.NoError(t, preparePartitionResidualCache(cache, [][]partitionDemand{first, second}))
	searchBudget := defaultPartitionSearchBudget()
	cachedFirst, hit, _, err := solvePartitionResidualCached(
		first, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, uncachedFirst, cachedFirst)
	flowAfterMiss := searchBudget.flowOperations

	cachedSecond, hit, _, err := solvePartitionResidualCached(
		second, cache, defaultPartitionGraphBudget(), &searchBudget)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, uncachedSecond, cachedSecond)
	require.Equal(t, flowAfterMiss, searchBudget.flowOperations)
	require.Equal(t, machine.NewCPUSet(1), cachedSecond["smt"],
		"SMT distance-zero and cpu-rank tie must match the uncached oracle")
	require.Equal(t, machine.NewCPUSet(4), cachedSecond["numa"],
		"NUMA-local distance and tie ordering must match the uncached oracle")
}
