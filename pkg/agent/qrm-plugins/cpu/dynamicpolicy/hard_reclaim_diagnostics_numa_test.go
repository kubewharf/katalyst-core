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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHardReclaimSolveOutcomeClassifiesErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"selected", nil, "selected"},
		{"whole core infeasible", &wholeCoreInfeasibleError{Target: 3, CPUsPerCore: 2}, "whole_core_infeasible"},
		{
			"search budget without cause",
			&hardReclaimSelectionError{reason: hardReclaimFailureSearchBudget, numaID: 0, deficit: 2},
			"search_budget",
		},
		{
			"graph edge budget wrapped as search budget",
			hardReclaimBudgetError(errPartitionAssignmentEdgeBudget),
			"graph_budget",
		},
		{
			"flow operation budget wrapped as search budget",
			hardReclaimBudgetError(errPartitionFlowOperationBudget),
			"graph_budget",
		},
		{
			"insufficient whole core",
			&hardReclaimSelectionError{reason: hardReclaimFailureInsufficientWholeCore, numaID: 0, deficit: 2},
			"no_feasible",
		},
		{
			"donor floor",
			&hardReclaimSelectionError{reason: hardReclaimFailureDonorFloor, numaID: 0, deficit: 2},
			"no_feasible",
		},
		{"generic no-feasible", errors.New("NUMA 0 needs 2 more reclaim CPUs"), "no_feasible"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, classifyHardReclaimSolveOutcome(tc.err))
		})
	}
}
