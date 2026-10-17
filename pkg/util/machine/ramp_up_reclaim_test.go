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

func TestCalculatePerNUMAHardReclaimTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		capacity          int
		ratio             float64
		minimumCores      int
		configuredReserve int
		cpusPerCore       int
		want              int
		wantErr           string
	}{
		{
			// SMT2: 192 CPUs => 96 cores; floor(96*0.2)=19 cores => 38 CPUs,
			// a whole-core multiple. No even-count rounding.
			name:         "smt2 core-aligned cores",
			capacity:     192,
			ratio:        0.2,
			minimumCores: 1,
			cpusPerCore:  2,
			want:         38,
		},
		{
			// non-SMT (cpusPerCore=1): whole-core alignment degrades to the raw
			// floor. floor(32*0.2)=6 => 6.
			name:         "non-smt whole core",
			capacity:     32,
			ratio:        0.2,
			minimumCores: 1,
			cpusPerCore:  1,
			want:         6,
		},
		{
			// cpusPerCore=4: 100 CPUs => 25 cores; floor(25*0.5)=12 cores => 48
			// CPUs, divisible by 4.
			name:         "cpusPerCore four divisible",
			capacity:     100,
			ratio:        0.5,
			minimumCores: 1,
			cpusPerCore:  4,
			want:         48,
		},
		{
			// odd capacity on SMT2 clamps to the core-aligned capacity at full
			// ratio: (97/2)*2 = 96, never the raw 97.
			name:         "odd capacity clamps to core aligned at full ratio",
			capacity:     97,
			ratio:        1,
			minimumCores: 1,
			cpusPerCore:  2,
			want:         96,
		},
		{
			// configured reserve rounds UP to a complete core: 3 CPUs on SMT2
			// rounds up to 2 cores => 4 CPUs.
			name:              "configured reserve rounds up to core",
			capacity:          64,
			ratio:             0,
			minimumCores:      1,
			configuredReserve: 3,
			cpusPerCore:       2,
			want:              4,
		},
		{
			// minimumCores lift is expressed in complete cores: 3 cores on SMT2
			// => 6 CPUs.
			name:         "minimum cores lift",
			capacity:     64,
			ratio:        0,
			minimumCores: 3,
			cpusPerCore:  2,
			want:         6,
		},
		{
			// minimumCores dominates at zero ratio on SMT2: 1 core => 2 CPUs.
			name:         "minimum core dominates at zero ratio",
			capacity:     8,
			ratio:        0,
			minimumCores: 1,
			cpusPerCore:  2,
			want:         2,
		},
		{
			name:         "maximum int capacity at full ratio non smt",
			capacity:     math.MaxInt,
			ratio:        1,
			minimumCores: 1,
			cpusPerCore:  1,
			want:         math.MaxInt,
		},
		{
			name:         "negative ratio is invalid",
			capacity:     32,
			ratio:        -0.1,
			minimumCores: 1,
			cpusPerCore:  2,
			wantErr:      "ratio must be within [0,1], got -0.1",
		},
		{
			name:         "ratio above one is invalid",
			capacity:     32,
			ratio:        1.1,
			minimumCores: 1,
			cpusPerCore:  2,
			wantErr:      "ratio must be within [0,1], got 1.1",
		},
		{
			name:         "NaN ratio is invalid",
			capacity:     32,
			ratio:        math.NaN(),
			minimumCores: 1,
			cpusPerCore:  2,
			wantErr:      "ratio must be within [0,1], got NaN",
		},
		{
			name:         "positive infinity ratio is invalid",
			capacity:     32,
			ratio:        math.Inf(1),
			minimumCores: 1,
			cpusPerCore:  2,
			wantErr:      "ratio must be within [0,1], got +Inf",
		},
		{
			name:         "negative infinity ratio is invalid",
			capacity:     32,
			ratio:        math.Inf(-1),
			minimumCores: 1,
			cpusPerCore:  2,
			wantErr:      "ratio must be within [0,1], got -Inf",
		},
		{
			name:         "non positive cpus per core is invalid",
			capacity:     32,
			ratio:        0.2,
			minimumCores: 1,
			cpusPerCore:  0,
			wantErr:      "cpus per core must be positive, got 0",
		},
		{
			name:              "target cannot exceed capacity",
			capacity:          4,
			ratio:             0.2,
			minimumCores:      1,
			configuredReserve: 6,
			cpusPerCore:       2,
			wantErr:           "hard reclaim target 6 exceeds NUMA capacity 4",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CalculatePerNUMAHardReclaimTarget(
				tt.capacity, tt.ratio, tt.minimumCores, tt.configuredReserve, tt.cpusPerCore)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDistributeConfiguredHardReclaimFloorRespectsNUMACapacity(t *testing.T) {
	t.Parallel()

	// cpusPerCore=2: baselines and every lifted target stay whole-core; the
	// floor 50 is reached without stranding a half core.
	got, err := DistributeConfiguredHardReclaimFloor(
		map[int]int{0: 24, 1: 32},
		map[int]int{0: 2, 1: 2},
		50,
		2,
	)
	require.NoError(t, err)
	require.Equal(t, 50, got[0]+got[1])
	require.LessOrEqual(t, got[0], 24)
	require.LessOrEqual(t, got[1], 32)
	require.Equal(t, map[int]int{0: 24, 1: 26}, got)
}

func TestDistributeConfiguredHardReclaimFloorLiftsByCompleteCores(t *testing.T) {
	t.Parallel()

	// cpusPerCore=2, floor 7 is not core-aligned: the water-filling step adds a
	// complete core (2 CPUs) at a time, rounding the floor UP to 8 so no NUMA is
	// left with an odd (half-core) target.
	got, err := DistributeConfiguredHardReclaimFloor(
		map[int]int{0: 8, 1: 8},
		map[int]int{0: 2, 1: 2},
		7,
		2,
	)
	require.NoError(t, err)
	for numaID, target := range got {
		require.Equalf(t, 0, target%2, "NUMA %d target %d must be core-aligned", numaID, target)
	}
	require.Equal(t, map[int]int{0: 4, 1: 4}, got)
}

func TestDistributeConfiguredHardReclaimFloorSkipsSaturatedNUMA(t *testing.T) {
	t.Parallel()

	// cpusPerCore=2, NUMA 0 has an odd capacity 7 => core-aligned capacity 6; the
	// lift must skip it once it reaches 6 (never add a half core to fill 7) and
	// place the remaining cores on NUMA 1.
	got, err := DistributeConfiguredHardReclaimFloor(
		map[int]int{0: 7, 1: 16},
		map[int]int{0: 2, 1: 2},
		18,
		2,
	)
	require.NoError(t, err)
	require.Equal(t, 18, got[0]+got[1])
	require.LessOrEqual(t, got[0], 6)
	for numaID, target := range got {
		require.Equalf(t, 0, target%2, "NUMA %d target %d must be core-aligned", numaID, target)
	}
}
