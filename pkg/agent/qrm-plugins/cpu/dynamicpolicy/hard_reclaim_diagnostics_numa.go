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
	"errors"
)

// Hard reclaim solve outcome strings, emitted as structured diagnostics.
const (
	hardReclaimOutcomeSelected            = "selected"
	hardReclaimOutcomeNoFeasible          = "no_feasible"
	hardReclaimOutcomeGraphBudget         = "graph_budget"
	hardReclaimOutcomeSearchBudget        = "search_budget"
	hardReclaimOutcomeWholeCoreInfeasible = "whole_core_infeasible"
)

// classifyHardReclaimSolveOutcome maps a solve error to a stable outcome token for
// structured logs. A successful solve is "selected". Whole-core infeasibility and
// search/graph budget exhaustion are classified from the typed error tree
// (errors.As / errors.Is) rather than from substrings of Error() text, which drift
// whenever the wrapped message changes. Distinguishing a graph-edge budget from a
// search-state budget, and whole-core infeasibility from a generic infeasible
// result, lets operators tell a budget/config issue apart from a genuine capacity
// shortfall.
func classifyHardReclaimSolveOutcome(err error) string {
	if err == nil {
		return hardReclaimOutcomeSelected
	}
	var infeasible *wholeCoreInfeasibleError
	if errors.As(err, &infeasible) {
		return hardReclaimOutcomeWholeCoreInfeasible
	}
	var sel *hardReclaimSelectionError
	if errors.As(err, &sel) {
		switch sel.reason {
		case hardReclaimFailureSearchBudget:
			// Distinguish graph-edge budget from search-state budget via cause.
			if isPartitionSolverBudgetError(err) {
				return hardReclaimOutcomeGraphBudget
			}
			return hardReclaimOutcomeSearchBudget
		case hardReclaimFailureInsufficientWholeCore, hardReclaimFailureDonorFloor:
			return hardReclaimOutcomeNoFeasible
		}
	}
	if isPartitionSolverBudgetError(err) {
		return hardReclaimOutcomeGraphBudget
	}
	return hardReclaimOutcomeNoFeasible
}
