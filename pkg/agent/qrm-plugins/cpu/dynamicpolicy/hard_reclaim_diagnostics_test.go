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
	"errors"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestHardReclaimDiagnosticsRecordCacheAndBudgetUsage(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)

	result, err := solveHardReclaimWithReplacementDiagnosed(
		demands,
		topology.CPUDetails.CPUs(),
		topology,
		defaultHardReclaimReplacementOptions(),
	)

	require.NoError(t, err)
	require.NotNil(t, result.proof)
	require.True(t, result.diagnostics.Complete)
	require.Positive(t, result.diagnostics.GeneratedCandidateStates)
	require.Positive(t, result.diagnostics.DeduplicatedCandidateStates)
	require.Positive(t, result.diagnostics.TerminalStates)
	require.Positive(t, result.diagnostics.ResidualCacheMisses)
	require.Equal(t, result.diagnostics.TerminalStates,
		result.diagnostics.ResidualCacheHits+result.diagnostics.ResidualCacheMisses)
	require.Positive(t, result.diagnostics.MaxAssignmentEdgesInGraph)
	require.Positive(t, result.diagnostics.FlowOperations)
	require.Equal(t, result.proof.reclaimAfter.Intersection(
		result.proof.reclaimBefore).Size(), result.diagnostics.SelectedRetainedReclaimCPUs)
	require.NotEmpty(t, result.diagnostics.SelectedTouchedDonorGroups)
}

func TestHardReclaimCompleteInfeasibilityIsNotBudgetExhaustion(t *testing.T) {
	t.Parallel()

	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{
		{
			key: "reclaim", quantity: 2, eligible: machine.NewCPUSet(0, 1),
			class: advisorBlockClassMandatoryReclaim,
		},
		{
			key: "dedicated", requestGroupKey: "pod/main", quantity: 2,
			requestQuantity: 2, eligible: machine.NewCPUSet(0, 1),
			class: advisorBlockClassDedicated,
		},
	}

	_, err := solveHardReclaimWithReplacementDiagnosed(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	var noFeasible *hardReclaimNoFeasibleReplacement
	require.ErrorAs(t, err, &noFeasible)
	var exhausted *hardReclaimSearchBudgetExceeded
	require.False(t, errors.As(err, &exhausted))
	require.True(t, noFeasible.Diagnostics.Complete)
}

func TestHardReclaimBudgetExhaustionCarriesIncompleteDiagnostics(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)
	options := defaultHardReclaimReplacementOptions()
	options.maxCandidateStates = 1

	_, err := solveHardReclaimWithReplacementDiagnosed(
		demands, topology.CPUDetails.CPUs(), topology, options)

	var exhausted *hardReclaimSearchBudgetExceeded
	require.ErrorAs(t, err, &exhausted)
	require.False(t, exhausted.Diagnostics.Complete)
	require.Equal(t, hardReclaimBudgetCandidateStates, exhausted.Budget)
}

func TestHardReclaimNonTypedFailuresReturnIncompleteDiagnostics(t *testing.T) {
	t.Parallel()

	topology := partitionSolverFixtureTopology()
	twoNUMATopology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 2)
	require.NoError(t, err)
	crossNUMAEligible := machine.NewCPUSet(
		twoNUMATopology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()[0],
		twoNUMATopology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()[0],
	)
	tests := []struct {
		name     string
		demands  []partitionDemand
		topology *machine.CPUTopology
	}{
		{
			name:     "topology",
			topology: nil,
		},
		{
			name: "input",
			demands: []partitionDemand{{
				key: "", quantity: 1, eligible: machine.NewCPUSet(0),
				class: advisorBlockClassShared,
			}},
			topology: topology,
		},
		{
			name: "internal",
			demands: []partitionDemand{{
				key: "reclaim", quantity: 1, eligible: crossNUMAEligible,
				class: advisorBlockClassMandatoryReclaim,
			}},
			topology: twoNUMATopology,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := solveHardReclaimWithReplacementDiagnosed(
				tc.demands, machine.NewCPUSet(0, 1, 2, 3), tc.topology,
				defaultHardReclaimReplacementOptions())

			require.Error(t, err)
			var noFeasible *hardReclaimNoFeasibleReplacement
			require.False(t, errors.As(err, &noFeasible))
			var exhausted *hardReclaimSearchBudgetExceeded
			require.False(t, errors.As(err, &exhausted))
			require.False(t, result.diagnostics.Complete)
		})
	}
}

func TestHardReclaimCompleteFailureResultPreservesAccumulatedDiagnostics(t *testing.T) {
	t.Parallel()

	topology := partitionSolverFixtureTopology()
	demands := []partitionDemand{{
		key:       "shared",
		quantity:  1,
		eligible:  machine.NewCPUSet(0, 1),
		preferred: machine.NewCPUSet(99),
		class:     advisorBlockClassShared,
	}}

	result, err := solveHardReclaimWithReplacementDiagnosed(
		demands, topology.CPUDetails.CPUs(), topology,
		defaultHardReclaimReplacementOptions())

	require.Error(t, err)
	var noFeasible *hardReclaimNoFeasibleReplacement
	require.ErrorAs(t, err, &noFeasible)
	var exhausted *hardReclaimSearchBudgetExceeded
	require.False(t, errors.As(err, &exhausted))
	require.True(t, result.diagnostics.Complete)
	require.Equal(t, 1, result.diagnostics.TerminalStates)
	require.Equal(t, 1, result.diagnostics.ResidualCacheMisses)
	require.Positive(t, result.diagnostics.MaxAssignmentEdgesInGraph)
	require.Positive(t, result.diagnostics.FlowOperations)
}

func TestHardReclaimAllSolverAndCanonicalBudgetsHaveTypedIncompleteKinds(t *testing.T) {
	t.Parallel()

	topology, demands, _, _ := productionNUMA2ReplacementFixture(t)
	tests := []struct {
		name     string
		kind     hardReclaimSearchBudgetKind
		sentinel error
		mutate   func(*hardReclaimReplacementOptions)
	}{
		{
			name: "terminal-solves", kind: hardReclaimBudgetTerminalSolves,
			mutate: func(options *hardReclaimReplacementOptions) {
				options.maxTerminalSolves = 1
			},
		},
		{
			name: "graph-edges", kind: hardReclaimBudgetGraphEdges,
			sentinel: errPartitionAssignmentEdgeBudget,
			mutate: func(options *hardReclaimReplacementOptions) {
				options.maxPartitionAssignmentEdges = 1
			},
		},
		{
			name: "flow-operations", kind: hardReclaimBudgetFlowOperations,
			sentinel: errPartitionFlowOperationBudget,
			mutate: func(options *hardReclaimReplacementOptions) {
				options.maxPartitionFlowOperations = 1
			},
		},
		{
			name: "canonical-preparation", kind: hardReclaimBudgetCanonicalPreparation,
			sentinel: errPartitionResidualPreparationWorkBudget,
			mutate: func(options *hardReclaimReplacementOptions) {
				options.residualCacheLimits.maxPreparationGraphs = 1
			},
		},
		{
			name: "canonical-work", kind: hardReclaimBudgetCanonicalWork,
			sentinel: errPartitionResidualPreparationWorkBudget,
			mutate: func(options *hardReclaimReplacementOptions) {
				options.residualCacheLimits.maxCanonicalWork = 1
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := defaultHardReclaimReplacementOptions()
			tc.mutate(&options)

			_, err := solveHardReclaimWithReplacementDiagnosed(
				demands, topology.CPUDetails.CPUs(), topology, options)

			var exhausted *hardReclaimSearchBudgetExceeded
			require.ErrorAs(t, err, &exhausted)
			require.Equal(t, tc.kind, exhausted.Budget)
			require.False(t, exhausted.Diagnostics.Complete)
			if tc.sentinel != nil {
				require.ErrorIs(t, err, tc.sentinel)
			}
		})
	}
}

func TestHardReclaimCanonicalRetainedBudgetPathsMapToTypedIncompleteKind(t *testing.T) {
	t.Parallel()

	for _, resource := range []string{
		"canonical payload bytes",
		"canonical entries",
		"result entries",
		"result units",
		"payload/result bytes",
	} {
		cause := partitionResidualLimitError(
			errPartitionResidualRetainedMemoryBudget, resource, 2, 1)
		kind, ok := hardReclaimBudgetKindFromError(cause)
		require.True(t, ok, "resource=%s", resource)
		require.Equal(t, hardReclaimBudgetCanonicalRetained, kind, "resource=%s", resource)

		err := hardReclaimBudgetExceeded(
			kind, hardReclaimSearchDiagnostics{Complete: false}, cause)
		var exhausted *hardReclaimSearchBudgetExceeded
		require.ErrorAs(t, err, &exhausted)
		require.False(t, exhausted.Diagnostics.Complete)
		require.ErrorIs(t, err, errPartitionResidualRetainedMemoryBudget)
	}
}

func TestPartitionResidualCacheFixedSeedDifferentialAgainstUncachedSolver(t *testing.T) {
	t.Parallel()

	const seed int64 = 20260923
	random := rand.New(rand.NewSource(seed))
	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(8, 1, 2)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()

	for iteration := 0; iteration < 100; iteration++ {
		demands := []partitionDemand{
			{
				key: "a", quantity: 1, eligible: all,
				preferred: machine.NewCPUSet(random.Intn(8)), class: advisorBlockClassDedicated,
			},
			{
				key: "b", quantity: 1, eligible: all,
				preferred: machine.NewCPUSet(random.Intn(8)), class: advisorBlockClassShared,
			},
			{
				key: "c", quantity: 1, eligible: all,
				preferred: machine.NewCPUSet(random.Intn(8)), class: advisorBlockClassShared,
			},
		}
		permuted := append([]partitionDemand(nil), demands...)
		random.Shuffle(len(permuted), func(i, j int) {
			permuted[i], permuted[j] = permuted[j], permuted[i]
		})

		uncached, err := solveDisjointPartitions(permuted, topology)
		require.NoError(t, err, "seed=%d iteration=%d", seed, iteration)

		cache, err := newPartitionResidualSolveCache(topology)
		require.NoError(t, err)
		require.NoError(t, preparePartitionResidualCache(
			cache, [][]partitionDemand{demands, permuted}))
		searchBudget := defaultPartitionSearchBudget()
		_, hit, _, err := solvePartitionResidualCached(
			demands, cache, defaultPartitionGraphBudget(), &searchBudget)
		require.NoError(t, err)
		require.False(t, hit)
		cached, hit, _, err := solvePartitionResidualCached(
			permuted, cache, defaultPartitionGraphBudget(), &searchBudget)
		require.NoError(t, err)
		require.True(t, hit)
		require.Equal(t, uncached, cached, "seed=%d iteration=%d", seed, iteration)
	}
}
