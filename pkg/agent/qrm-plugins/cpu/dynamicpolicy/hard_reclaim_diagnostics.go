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
	"fmt"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	errHardReclaimCandidateStateBudget = errors.New("hard reclaim candidate state budget exceeded")
	errHardReclaimTerminalSolveBudget  = errors.New("hard reclaim terminal solve budget exceeded")
)

type hardReclaimSearchBudgetKind string

const (
	hardReclaimBudgetCandidateStates      hardReclaimSearchBudgetKind = "candidate_states"
	hardReclaimBudgetTerminalSolves       hardReclaimSearchBudgetKind = "terminal_solves"
	hardReclaimBudgetGraphEdges           hardReclaimSearchBudgetKind = "graph_edges"
	hardReclaimBudgetFlowOperations       hardReclaimSearchBudgetKind = "flow_operations"
	hardReclaimBudgetCanonicalPreparation hardReclaimSearchBudgetKind = "canonical_preparation"
	hardReclaimBudgetCanonicalWork        hardReclaimSearchBudgetKind = "canonical_work"
	hardReclaimBudgetCanonicalRetained    hardReclaimSearchBudgetKind = "canonical_retained"
)

type hardReclaimSearchDiagnostics struct {
	Complete                    bool
	GeneratedCandidateStates    int
	DeduplicatedCandidateStates int
	TerminalStates              int
	ResidualCacheHits           int
	ResidualCacheMisses         int
	MaxAssignmentEdgesInGraph   int
	FlowOperations              int
	SelectedRetainedReclaimCPUs int
	SelectedTouchedDonorGroups  []string
}

type hardReclaimNoFeasibleReplacement struct {
	Diagnostics hardReclaimSearchDiagnostics
	Cause       error
}

func (e *hardReclaimNoFeasibleReplacement) Error() string {
	return fmt.Sprintf("no feasible hard reclaim replacement: %v", e.Cause)
}

func (e *hardReclaimNoFeasibleReplacement) Unwrap() error {
	return e.Cause
}

type hardReclaimSearchBudgetExceeded struct {
	Budget      hardReclaimSearchBudgetKind
	Diagnostics hardReclaimSearchDiagnostics
	Cause       error
}

func (e *hardReclaimSearchBudgetExceeded) Error() string {
	return fmt.Sprintf("hard reclaim %s budget exceeded: %v", e.Budget, e.Cause)
}

func (e *hardReclaimSearchBudgetExceeded) Unwrap() error {
	return e.Cause
}

type hardReclaimDiagnosedResult struct {
	assignments map[string]machine.CPUSet
	proof       *hardReclaimReplacementProof
	diagnostics hardReclaimSearchDiagnostics
}

func hardReclaimBudgetExceeded(
	kind hardReclaimSearchBudgetKind,
	diagnostics hardReclaimSearchDiagnostics,
	cause error,
) error {
	return &hardReclaimSearchBudgetExceeded{
		Budget:      kind,
		Diagnostics: cloneHardReclaimSearchDiagnostics(diagnostics),
		Cause:       cause,
	}
}

func hardReclaimNoFeasible(
	diagnostics hardReclaimSearchDiagnostics,
	cause error,
) error {
	return &hardReclaimNoFeasibleReplacement{
		Diagnostics: cloneHardReclaimSearchDiagnostics(diagnostics),
		Cause:       cause,
	}
}

func cloneHardReclaimSearchDiagnostics(
	diagnostics hardReclaimSearchDiagnostics,
) hardReclaimSearchDiagnostics {
	diagnostics.SelectedTouchedDonorGroups = append([]string(nil), diagnostics.SelectedTouchedDonorGroups...)
	return diagnostics
}

func hardReclaimDiagnosticsFromError(err error) (
	hardReclaimSearchDiagnostics,
	bool,
) {
	var noFeasible *hardReclaimNoFeasibleReplacement
	if errors.As(err, &noFeasible) {
		return cloneHardReclaimSearchDiagnostics(noFeasible.Diagnostics), true
	}
	var exhausted *hardReclaimSearchBudgetExceeded
	if errors.As(err, &exhausted) {
		return cloneHardReclaimSearchDiagnostics(exhausted.Diagnostics), true
	}
	return hardReclaimSearchDiagnostics{}, false
}

func hardReclaimBudgetKindFromError(err error) (
	hardReclaimSearchBudgetKind,
	bool,
) {
	switch {
	case errors.Is(err, errHardReclaimCandidateStateBudget):
		return hardReclaimBudgetCandidateStates, true
	case errors.Is(err, errHardReclaimTerminalSolveBudget):
		return hardReclaimBudgetTerminalSolves, true
	case errors.Is(err, errPartitionAssignmentEdgeBudget):
		return hardReclaimBudgetGraphEdges, true
	case errors.Is(err, errPartitionFlowOperationBudget):
		return hardReclaimBudgetFlowOperations, true
	case errors.Is(err, errPartitionResidualRetainedMemoryBudget):
		return hardReclaimBudgetCanonicalRetained, true
	case errors.Is(err, errPartitionResidualPreparationWorkBudget):
		var limitErr *partitionResidualCacheLimitError
		if errors.As(err, &limitErr) && limitErr.resource == "graphs" {
			return hardReclaimBudgetCanonicalPreparation, true
		}
		return hardReclaimBudgetCanonicalWork, true
	default:
		return "", false
	}
}

func hardReclaimSelectedDiagnostics(
	diagnostics hardReclaimSearchDiagnostics,
	proof *hardReclaimReplacementProof,
) hardReclaimSearchDiagnostics {
	if proof == nil {
		return diagnostics
	}
	diagnostics.SelectedRetainedReclaimCPUs = proof.reclaimAfter.Intersection(proof.reclaimBefore).Size()
	for groupKey, before := range proof.dedicatedBeforeByGroup {
		if !before.Equals(proof.dedicatedAfterByGroup[groupKey]) {
			diagnostics.SelectedTouchedDonorGroups = append(diagnostics.SelectedTouchedDonorGroups, groupKey)
		}
	}
	sort.Strings(diagnostics.SelectedTouchedDonorGroups)
	return diagnostics
}
