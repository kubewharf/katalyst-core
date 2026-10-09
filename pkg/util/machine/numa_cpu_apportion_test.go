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

package machine

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApportionNUMACPU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		total         int64
		weights       map[int]int64
		limits        map[int]int64
		cpusPerCore   int
		want          map[int]int64
		wantEffective int64
		wantErr       string
	}{
		{
			name:          "smt2 preserves 76 cpus with unequal limits",
			total:         76,
			weights:       map[int]int64{0: 32, 1: 32, 2: 32, 3: 32, 4: 32, 5: 32, 6: 32, 7: 32},
			limits:        map[int]int64{0: 10, 1: 10, 2: 10, 3: 10, 4: 10, 5: 10, 6: 8, 7: 8},
			cpusPerCore:   2,
			want:          map[int]int64{0: 10, 1: 10, 2: 10, 3: 10, 4: 10, 5: 10, 6: 8, 7: 8},
			wantEffective: 76,
		},
		{
			name:          "smt2 distributes post reserve total",
			total:         64,
			weights:       map[int]int64{0: 10, 1: 10, 2: 10, 3: 10, 4: 10, 5: 10, 6: 8, 7: 8},
			limits:        map[int]int64{0: 10, 1: 10, 2: 10, 3: 10, 4: 10, 5: 10, 6: 8, 7: 8},
			cpusPerCore:   2,
			want:          map[int]int64{0: 8, 1: 8, 2: 8, 3: 8, 4: 8, 5: 8, 6: 8, 7: 8},
			wantEffective: 64,
		},
		{
			name:          "smt2 aligns one global remainder",
			total:         37,
			weights:       map[int]int64{0: 1, 1: 1},
			limits:        map[int]int64{0: 40, 1: 40},
			cpusPerCore:   2,
			want:          map[int]int64{0: 18, 1: 18},
			wantEffective: 36,
		},
		{
			name:          "limits clamp effective total",
			total:         20,
			weights:       map[int]int64{0: 1, 1: 1},
			limits:        map[int]int64{0: 4, 1: 6},
			cpusPerCore:   2,
			want:          map[int]int64{0: 4, 1: 6},
			wantEffective: 10,
		},
		{
			name:          "renormalizes after a floor allocation reaches its limit",
			total:         10,
			weights:       map[int]int64{0: 100, 1: 100, 2: 1},
			limits:        map[int]int64{0: 1, 1: 100, 2: 100},
			cpusPerCore:   1,
			want:          map[int]int64{0: 1, 1: 9, 2: 0},
			wantEffective: 10,
		},
		{
			name:          "sub-core total returns zero allocations",
			total:         1,
			weights:       map[int]int64{0: 100, 1: 100, 2: 1},
			limits:        map[int]int64{0: 1, 1: 100, 2: 100},
			cpusPerCore:   2,
			want:          map[int]int64{0: 0, 1: 0, 2: 0},
			wantEffective: 0,
		},
		{
			name:          "smt1 breaks equal remainders by numa id",
			total:         5,
			weights:       map[int]int64{0: 1, 1: 1},
			limits:        map[int]int64{0: 5, 1: 5},
			cpusPerCore:   1,
			want:          map[int]int64{0: 3, 1: 2},
			wantEffective: 5,
		},
		{
			name:          "smt4 allocates physical core quanta",
			total:         15,
			weights:       map[int]int64{0: 1, 1: 2},
			limits:        map[int]int64{0: 8, 1: 8},
			cpusPerCore:   4,
			want:          map[int]int64{0: 4, 1: 8},
			wantEffective: 12,
		},
		{
			name:          "zero total returns zero allocations",
			total:         0,
			weights:       map[int]int64{0: 1, 1: 1},
			limits:        map[int]int64{0: 8, 1: 8},
			cpusPerCore:   2,
			want:          map[int]int64{0: 0, 1: 0},
			wantEffective: 0,
		},
		{
			name:          "empty maps clamp effective total to zero",
			total:         8,
			weights:       map[int]int64{},
			limits:        map[int]int64{},
			cpusPerCore:   2,
			want:          map[int]int64{},
			wantEffective: 0,
		},
		{
			name:          "unaligned limits are rounded down",
			total:         8,
			weights:       map[int]int64{0: 1, 1: 1},
			limits:        map[int]int64{0: 3, 1: 5},
			cpusPerCore:   2,
			want:          map[int]int64{0: 2, 1: 4},
			wantEffective: 6,
		},
		{
			name:        "reject invalid quantum",
			total:       8,
			weights:     map[int]int64{0: 1},
			limits:      map[int]int64{0: 8},
			cpusPerCore: 0,
			wantErr:     "cpus per core must be positive",
		},
		{
			name:        "reject negative total",
			total:       -1,
			weights:     map[int]int64{0: 1},
			limits:      map[int]int64{0: 8},
			cpusPerCore: 2,
			wantErr:     "total cpu must not be negative",
		},
		{
			name:        "reject negative weight",
			total:       8,
			weights:     map[int]int64{0: -1},
			limits:      map[int]int64{0: 8},
			cpusPerCore: 2,
			wantErr:     "numa weight must not be negative",
		},
		{
			name:        "reject negative limit",
			total:       8,
			weights:     map[int]int64{0: 1},
			limits:      map[int]int64{0: -1},
			cpusPerCore: 2,
			wantErr:     "numa limit must not be negative",
		},
		{
			name:        "reject key mismatch",
			total:       8,
			weights:     map[int]int64{0: 1},
			limits:      map[int]int64{1: 8},
			cpusPerCore: 2,
			wantErr:     "numa weight and limit keys must match",
		},
		{
			name:        "reject zero weight with positive aligned limit",
			total:       8,
			weights:     map[int]int64{0: 0},
			limits:      map[int]int64{0: 8},
			cpusPerCore: 2,
			wantErr:     "numa weight must be positive when aligned limit is positive",
		},
		{
			name:          "allow zero weight with sub-quantum limit",
			total:         8,
			weights:       map[int]int64{0: 0},
			limits:        map[int]int64{0: 1},
			cpusPerCore:   2,
			want:          map[int]int64{0: 0},
			wantEffective: 0,
		},
		{
			name:        "reject overflowing weighted product",
			total:       math.MaxInt64,
			weights:     map[int]int64{0: math.MaxInt64},
			limits:      map[int]int64{0: math.MaxInt64},
			cpusPerCore: 1,
			wantErr:     "cpu apportionment overflow",
		},
		{
			name:        "reject overflowing total weight",
			total:       math.MaxInt64,
			weights:     map[int]int64{0: math.MaxInt64, 1: 1},
			limits:      map[int]int64{0: math.MaxInt64, 1: math.MaxInt64},
			cpusPerCore: 1,
			wantErr:     "cpu apportionment overflow",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, effective, err := ApportionNUMACPU(
				tt.total, tt.weights, tt.limits, tt.cpusPerCore)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantEffective, effective)
			assert.Equal(t, effective, sumNUMACPU(got))
			for numaID, value := range got {
				assert.Zero(t, value%int64(tt.cpusPerCore))
				assert.LessOrEqual(t, value, tt.limits[numaID])
			}
		})
	}
}

func TestApportionNUMACPUDeterministicAcrossMapInsertionOrder(t *testing.T) {
	t.Parallel()

	weightsAscending := make(map[int]int64)
	limitsAscending := make(map[int]int64)
	weightsDescending := make(map[int]int64)
	limitsDescending := make(map[int]int64)
	for numaID := 0; numaID < 8; numaID++ {
		weightsAscending[numaID] = 1
		limitsAscending[numaID] = 8
	}
	for numaID := 7; numaID >= 0; numaID-- {
		weightsDescending[numaID] = 1
		limitsDescending[numaID] = 8
	}

	want := map[int]int64{0: 6, 1: 6, 2: 6, 3: 6, 4: 4, 5: 4, 6: 4, 7: 4}
	for i := 0; i < 100; i++ {
		gotAscending, effectiveAscending, err := ApportionNUMACPU(
			40, weightsAscending, limitsAscending, 2)
		require.NoError(t, err)
		gotDescending, effectiveDescending, err := ApportionNUMACPU(
			40, weightsDescending, limitsDescending, 2)
		require.NoError(t, err)

		assert.Equal(t, want, gotAscending)
		assert.Equal(t, gotAscending, gotDescending)
		assert.Equal(t, effectiveAscending, effectiveDescending)
	}
}

func sumNUMACPU(values map[int]int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}
