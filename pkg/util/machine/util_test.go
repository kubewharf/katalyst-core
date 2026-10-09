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

package machine

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPUAssignmentFormat(t *testing.T) {
	t.Parallel()

	assignment := map[int]CPUSet{
		0: NewCPUSet(1, 2),
		1: NewCPUSet(3, 4),
	}
	assert.Equal(t, map[uint64]string{
		0: "1-2",
		1: "3-4",
	}, ParseCPUAssignmentFormat(assignment))
}

func TestDeepcopyCPUAssignment(t *testing.T) {
	t.Parallel()

	assignment := map[int]CPUSet{
		0: NewCPUSet(1, 2),
		1: NewCPUSet(3, 4),
	}
	assert.Equal(t, assignment, DeepcopyCPUAssignment(assignment))
}

func TestMaskToUInt64Array(t *testing.T) {
	t.Parallel()

	mask, err := NewBitMask(0, 1, 2, 3)
	assert.NoError(t, err)
	assert.Equal(t, []uint64{0, 1, 2, 3}, MaskToUInt64Array(mask))
}

func TestCalculateGlobalRampUpReclaimTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		total   int
		ratio   float64
		minimum int
		want    int
	}{
		{
			name:    "fractional target is floored then aligned down to even",
			total:   16,
			ratio:   0.5625,
			minimum: 4,
			want:    8,
		},
		{
			name:    "minimum wins after ratio alignment",
			total:   16,
			ratio:   0.1,
			minimum: 4,
			want:    4,
		},
		{
			name:    "configured reserve floor can exceed NUMA minimum",
			total:   16,
			ratio:   0.25,
			minimum: 6,
			want:    6,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, CalculateGlobalRampUpReclaimTarget(tt.total, tt.ratio, tt.minimum))
		})
	}
}

func TestDistributeNUMATarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		available map[int]int
		target    int
		min       int
		want      map[int]int
		wantErr   string
	}{
		{
			name:      "even target across uneven capacities",
			available: map[int]int{0: 4, 1: 12},
			target:    8,
			min:       2,
			want:      map[int]int{0: 4, 1: 4},
		},
		{
			name:      "remainder goes to capable NUMA deterministically",
			available: map[int]int{9: 4, 3: 12},
			target:    9,
			min:       2,
			want:      map[int]int{3: 5, 9: 4},
		},
		{
			name:      "stable tie break uses lower NUMA ID",
			available: map[int]int{9: 12, 3: 12},
			target:    9,
			min:       2,
			want:      map[int]int{3: 5, 9: 4},
		},
		{
			name:      "target below aggregate minimum",
			available: map[int]int{0: 4, 1: 12},
			target:    3,
			min:       2,
			wantErr:   "target 3 is below per-NUMA minimum total 4",
		},
		{
			name:      "NUMA below minimum capacity",
			available: map[int]int{0: 1, 1: 12},
			target:    4,
			min:       2,
			wantErr:   "NUMA 0 capacity 1 is below minimum 2",
		},
		{
			name:      "capacity cannot satisfy balanced target",
			available: map[int]int{0: 4, 1: 12},
			target:    12,
			min:       2,
			wantErr:   "cannot distribute target 12 within NUMA capacities while keeping counts balanced",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := DistributeNUMATarget(tt.available, tt.target, tt.min)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

type mockDirEntry struct {
	fs.DirEntry
	entryName string
	isDir     bool
	typ       fs.FileMode
}

// Name return the mock entry name
func (m *mockDirEntry) Name() string {
	return m.entryName
}

// IsDir return if the entry is a directory
func (m *mockDirEntry) IsDir() bool {
	return m.isDir
}

// Type return the mock entry type
func (m *mockDirEntry) Type() fs.FileMode {
	return m.typ
}

// Info return the mock entry info
func (m *mockDirEntry) Info() (fs.FileInfo, error) {
	return nil, nil
}
