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

package planner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStableAdviceDomainSummaryValidate(t *testing.T) {
	t.Parallel()

	summary := NewStableAdviceDomainSummary(
		map[int]int{
			0: 2,
			1: 4,
		},
		map[string]int{
			"dedicated": 6,
			"reclaim":   3,
		},
		11,
		22,
		33,
	)

	tests := []struct {
		name        string
		local       LocalDomainSummary
		wantErrText string
	}{
		{
			name: "accepts matching summary",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				22,
				33,
			),
		},
		{
			name: "rejects per NUMA floor value mismatch",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 5,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				22,
				33,
			),
			wantErrText: "per NUMA floor mismatch for NUMA 1",
		},
		{
			name: "rejects missing per NUMA floor",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				22,
				33,
			),
			wantErrText: "per NUMA floor mismatch: missing local NUMA 1",
		},
		{
			name: "rejects unexpected per NUMA floor",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
					2: 1,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				22,
				33,
			),
			wantErrText: "per NUMA floor mismatch: unexpected local NUMA 2",
		},
		{
			name: "rejects per pool budget value mismatch",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   4,
				},
				11,
				22,
				33,
			),
			wantErrText: `per pool budget mismatch for pool "reclaim"`,
		},
		{
			name: "rejects missing per pool budget",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
				},
				11,
				22,
				33,
			),
			wantErrText: `per pool budget mismatch: missing local pool "reclaim"`,
		},
		{
			name: "rejects unexpected per pool budget",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"extra":     1,
					"reclaim":   3,
				},
				11,
				22,
				33,
			),
			wantErrText: `per pool budget mismatch: unexpected local pool "extra"`,
		},
		{
			name: "rejects package domain digest mismatch",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				12,
				22,
				33,
			),
			wantErrText: "package domain digest mismatch",
		},
		{
			name: "rejects block graph digest mismatch",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				23,
				33,
			),
			wantErrText: "block graph digest mismatch",
		},
		{
			name: "rejects overlap mode digest mismatch",
			local: NewLocalDomainSummary(
				map[int]int{
					0: 2,
					1: 4,
				},
				map[string]int{
					"dedicated": 6,
					"reclaim":   3,
				},
				11,
				22,
				34,
			),
			wantErrText: "overlap mode digest mismatch",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := summary.Validate(tt.local)

			if tt.wantErrText != "" {
				require.ErrorContains(t, err, tt.wantErrText)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestStableAdviceDomainSummaryValidateAcceptsNilAndEmptyMaps(t *testing.T) {
	t.Parallel()

	require.NoError(t, NewStableAdviceDomainSummary(nil, nil, 0, 0, 0).Validate(NewLocalDomainSummary(nil, nil, 0, 0, 0)))
	require.NoError(t, NewStableAdviceDomainSummary(map[int]int{}, map[string]int{}, 0, 0, 0).Validate(NewLocalDomainSummary(nil, nil, 0, 0, 0)))
}

func TestStableAdviceDomainSummaryClonesInputMaps(t *testing.T) {
	t.Parallel()

	perNUMAFloor := map[int]int{0: 2}
	perPoolBudget := map[string]int{"reclaim": 3}
	summary := NewStableAdviceDomainSummary(perNUMAFloor, perPoolBudget, 1, 2, 3)

	perNUMAFloor[0] = 4
	perPoolBudget["reclaim"] = 5

	require.Equal(t, map[int]int{0: 2}, summary.PerNUMAFloor())
	require.Equal(t, map[string]int{"reclaim": 3}, summary.PerPoolBudget())
	require.Equal(t, uint64(1), summary.PackageDomainDigest())
	require.Equal(t, uint64(2), summary.BlockGraphDigest())
	require.Equal(t, uint64(3), summary.OverlapModeDigest())
}

func TestLocalDomainSummaryClonesInputMaps(t *testing.T) {
	t.Parallel()

	perNUMAFloor := map[int]int{0: 2}
	perPoolBudget := map[string]int{"reclaim": 3}
	local := NewLocalDomainSummary(perNUMAFloor, perPoolBudget, 1, 2, 3)

	perNUMAFloor[0] = 4
	perPoolBudget["reclaim"] = 5

	require.Equal(t, map[int]int{0: 2}, local.PerNUMAFloor())
	require.Equal(t, map[string]int{"reclaim": 3}, local.PerPoolBudget())
	require.Equal(t, uint64(1), local.PackageDomainDigest())
	require.Equal(t, uint64(2), local.BlockGraphDigest())
	require.Equal(t, uint64(3), local.OverlapModeDigest())
}

func TestStableAdviceDomainSummaryGettersReturnClones(t *testing.T) {
	t.Parallel()

	summary := NewStableAdviceDomainSummary(map[int]int{0: 2}, map[string]int{"reclaim": 3}, 1, 2, 3)

	perNUMAFloor := summary.PerNUMAFloor()
	perPoolBudget := summary.PerPoolBudget()
	perNUMAFloor[0] = 4
	perPoolBudget["reclaim"] = 5

	require.Equal(t, map[int]int{0: 2}, summary.PerNUMAFloor())
	require.Equal(t, map[string]int{"reclaim": 3}, summary.PerPoolBudget())
}

func TestLocalDomainSummaryGettersReturnClones(t *testing.T) {
	t.Parallel()

	local := NewLocalDomainSummary(map[int]int{0: 2}, map[string]int{"reclaim": 3}, 1, 2, 3)

	perNUMAFloor := local.PerNUMAFloor()
	perPoolBudget := local.PerPoolBudget()
	perNUMAFloor[0] = 4
	perPoolBudget["reclaim"] = 5

	require.Equal(t, map[int]int{0: 2}, local.PerNUMAFloor())
	require.Equal(t, map[string]int{"reclaim": 3}, local.PerPoolBudget())
}
