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

func TestCalculateAggregateRampUpTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		capacity    int
		ratio       float64
		cpusPerCore int
		want        int
		wantErr     string
	}{
		{
			// global domain: 120 logical CPUs, SMT2 -> 60 cores;
			// floor(60*0.2)=12 cores -> 24 logical CPUs.
			name:        "global 120 smt2 yields 24",
			capacity:    120,
			ratio:       0.2,
			cpusPerCore: 2,
			want:        24,
		},
		{
			// single SNB NUMA: 24 logical, SMT2 -> 12 cores;
			// floor(12*0.2)=2 cores -> 4.
			name:        "single numa 24 smt2 yields 4",
			capacity:    24,
			ratio:       0.2,
			cpusPerCore: 2,
			want:        4,
		},
		{
			// two-NUMA domain aggregated: 48 logical, SMT2 -> 24 cores;
			// floor(24*0.2)=4 cores -> 8. Aggregate rounding beats per-NUMA
			// rounding here (2x floor(12*0.2)=2x4=8 coincides).
			name:        "two numa 48 smt2 yields 8",
			capacity:    48,
			ratio:       0.2,
			cpusPerCore: 2,
			want:        8,
		},
		{
			// aggregate rounding must beat per-NUMA rounding: 24+28=52 logical,
			// SMT2 -> 26 cores; floor(26*0.2)=5 cores -> 10. Per-NUMA would give
			// floor(12*0.2)*2 + floor(14*0.2)*2 = 4+4 = 8 (wrong).
			name:        "uneven aggregate 52 smt2 yields 10 not 8",
			capacity:    52,
			ratio:       0.2,
			cpusPerCore: 2,
			want:        10,
		},
		{
			// SMT1: whole-core alignment is the identity; floor(120*0.2)=24.
			name:        "smt1 identity rounding",
			capacity:    120,
			ratio:       0.2,
			cpusPerCore: 1,
			want:        24,
		},
		{
			// SMT4: 120 logical -> 30 cores; floor(30*0.2)=6 cores -> 24.
			name:        "smt4 alignment",
			capacity:    120,
			ratio:       0.2,
			cpusPerCore: 4,
			want:        24,
		},
		{
			// ratio 0 -> no reservation.
			name:        "ratio zero",
			capacity:    120,
			ratio:       0,
			cpusPerCore: 2,
			want:        0,
		},
		{
			// negative ratio -> error.
			name:        "ratio negative",
			capacity:    120,
			ratio:       -0.1,
			cpusPerCore: 2,
			wantErr:     "ratio must be within [0,1], got -0.1",
		},
		{
			// ratio > 1 -> error.
			name:        "ratio above one",
			capacity:    120,
			ratio:       1.5,
			cpusPerCore: 2,
			wantErr:     "ratio must be within [0,1], got 1.5",
		},
		{
			// ratio exactly 1 -> core-aligned capacity (odd capacity clamps).
			name:        "ratio one clamps odd capacity",
			capacity:    121,
			ratio:       1,
			cpusPerCore: 2,
			want:        120,
		},
		{
			// non-core-aligned capacity: 121 logical, SMT2 -> 60 cores;
			// floor(60*0.2)=12 cores -> 24 (the stray 1 CPU is never donated).
			name:        "odd capacity rounds down",
			capacity:    121,
			ratio:       0.2,
			cpusPerCore: 2,
			want:        24,
		},
		{
			name:        "nan ratio invalid",
			capacity:    120,
			ratio:       math.NaN(),
			cpusPerCore: 2,
			wantErr:     "ratio must be within [0,1], got NaN",
		},
		{
			name:        "non positive cpus per core invalid",
			capacity:    120,
			ratio:       0.2,
			cpusPerCore: 0,
			wantErr:     "cpus per core must be positive, got 0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CalculateAggregateRampUpTarget(tt.capacity, tt.ratio, tt.cpusPerCore)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, got, tt.capacity)
		})
	}
}

func TestDistributeDomainTargetSumsExactlyAndIsWholeCore(t *testing.T) {
	t.Parallel()

	// 5 equal non-binding NUMAs of 24 logical CPUs, SMT2. Target 24 = 12 cores.
	capacity := map[int]int{1: 24, 2: 24, 4: 24, 5: 24, 6: 24}
	baseline := map[int]int{1: 2, 2: 2, 4: 2, 5: 2, 6: 2}

	got, err := DistributeDomainTarget(24, capacity, baseline, 2)
	require.NoError(t, err)

	sum := 0
	for numaID, share := range got {
		require.Equalf(t, 0, share%2, "NUMA %d share %d must be whole-core in SMT2", numaID, share)
		require.LessOrEqual(t, share, capacity[numaID], "NUMA %d share %d exceeds capacity %d", numaID, share, capacity[numaID])
		sum += share
	}
	require.Equal(t, 24, sum, "per-NUMA shares must sum exactly to the aggregate target")
}

func TestDistributeDomainTargetIsDeterministic(t *testing.T) {
	t.Parallel()

	capacity := map[int]int{0: 24, 1: 20, 3: 28}
	baseline := map[int]int{0: 2, 1: 2, 3: 2}

	first, err := DistributeDomainTarget(10, capacity, baseline, 2)
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		again, err := DistributeDomainTarget(10, capacity, baseline, 2)
		require.NoError(t, err)
		require.Equal(t, first, again, "distribution must be stable across 100 runs")
	}
	require.Equal(t, 10, first[0]+first[1]+first[3])
}

func TestDistributeDomainTargetRespectsUnevenCapacity(t *testing.T) {
	t.Parallel()

	// NUMA 0 has only 8 logical CPUs (4 cores); the target must not push it
	// beyond its core-aligned capacity even though the water-fill would prefer
	// to round-robin.
	capacity := map[int]int{0: 8, 1: 40, 2: 40}
	baseline := map[int]int{0: 2, 1: 2, 2: 2}

	got, err := DistributeDomainTarget(20, capacity, baseline, 2)
	require.NoError(t, err)
	require.LessOrEqual(t, got[0], 8)
	require.Equal(t, 20, got[0]+got[1]+got[2])
}

func TestDistributeDomainTargetRejectsInvalid(t *testing.T) {
	t.Parallel()

	if _, err := DistributeDomainTarget(-1, map[int]int{0: 8}, map[int]int{0: 0}, 2); err == nil {
		t.Fatal("negative target must error")
	}
	if _, err := DistributeDomainTarget(10, map[int]int{0: 8}, map[int]int{0: 0}, 0); err == nil {
		t.Fatal("non-positive cpusPerCore must error")
	}
}
