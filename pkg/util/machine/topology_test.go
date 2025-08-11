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
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

var mockLock sync.Mutex

func TestMemoryDetailsEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		detail MemoryDetails
		want   MemoryDetails
		equal  bool
	}{
		{
			name:   "Equal Maps",
			detail: MemoryDetails{1: 100, 2: 200},
			want:   MemoryDetails{1: 100, 2: 200},
			equal:  true,
		},
		{
			name:   "Different Lengths",
			detail: MemoryDetails{1: 100},
			want:   MemoryDetails{1: 100, 2: 200},
			equal:  false,
		},
		{
			name:   "Different Values",
			detail: MemoryDetails{1: 100, 2: 200},
			want:   MemoryDetails{1: 100, 2: 300},
			equal:  false,
		},
		{
			name:   "Different Keys",
			detail: MemoryDetails{1: 100, 3: 300},
			want:   MemoryDetails{1: 100, 2: 300},
			equal:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.detail.Equal(tt.want); got != tt.equal {
				t.Errorf("MemoryDetails.Equal() = %v, want %v", got, tt.equal)
			}
		})
	}
}

func TestMemoryDetailsClone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		detail MemoryDetails
	}{
		{
			name:   "Empty Map",
			detail: MemoryDetails{},
		},
		{
			name:   "Single Element",
			detail: MemoryDetails{1: 100},
		},
		{
			name:   "Multiple Elements",
			detail: MemoryDetails{1: 100, 2: 200, 3: 300},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cloned := tt.detail.Clone()

			if !reflect.DeepEqual(cloned, tt.detail) {
				t.Errorf("Clone() = %v, want %v", cloned, tt.detail)
			}

			// Ensure that the clone is a different instance
			if &cloned == &tt.detail {
				t.Errorf("Clone() returned the same instance, want different instances")
			}
		})
	}
}

func TestMemoryDetailsFillNUMANodesWithZero(t *testing.T) {
	t.Parallel()

	// Define test cases for FillNUMANodesWithZero method
	tests := []struct {
		name     string
		detail   MemoryDetails
		allNUMAs CPUSet
		expected MemoryDetails
	}{
		{
			name:   "Existing NUMA Nodes",
			detail: MemoryDetails{0: 100, 1: 200},
			allNUMAs: CPUSet{
				Initialed: true,
				elems:     map[int]struct{}{0: {}, 1: {}, 2: {}},
			},
			expected: MemoryDetails{0: 100, 1: 200, 2: 0},
		},
		{
			name:   "Empty NUMA Nodes",
			detail: MemoryDetails{},
			allNUMAs: CPUSet{
				Initialed: true,
				elems:     map[int]struct{}{0: {}},
			},
			expected: MemoryDetails{0: 0},
		},
		{
			name:   "No Additional NUMA Nodes",
			detail: MemoryDetails{0: 100, 1: 200},
			allNUMAs: CPUSet{
				Initialed: true,
				elems:     map[int]struct{}{0: {}, 1: {}},
			},
			expected: MemoryDetails{0: 100, 1: 200},
		},
	}

	// Run the test cases
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Call the FillNUMANodesWithZero method and compare the result with expected outcome
			if got := tt.detail.FillNUMANodesWithZero(tt.allNUMAs); !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("MemoryDetails.FillNUMANodesWithZero() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetSiblingNumaInfo(t *testing.T) {
	t.Parallel()

	type args struct {
		conf            *global.MachineInfoConfiguration
		numaDistanceMap map[int][]NumaDistanceInfo
	}
	tests := []struct {
		name string
		args args
		want *SiblingNumaInfo
	}{
		{
			name: "test for without sibling",
			args: args{
				conf: &global.MachineInfoConfiguration{
					SiblingNumaMemoryBandwidthAllocatableRate: 0.5,
					SiblingNumaMemoryBandwidthCapacity:        10,
				},
				numaDistanceMap: map[int][]NumaDistanceInfo{
					0: {
						{
							NumaID:   0,
							Distance: 10,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
					},
					1: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 10,
						},
					},
				},
			},
			want: &SiblingNumaInfo{
				SiblingNumaMap: map[int]sets.Int{
					0: sets.NewInt(),
					1: sets.NewInt(),
				},
				SiblingNumaAvgMBWCapacityMap: map[int]int64{
					0: 10,
					1: 10,
				},
				SiblingNumaAvgMBWAllocatableMap: map[int]int64{
					0: 5,
					1: 5,
				},
			},
		},
		{
			name: "test for with sibling",
			args: args{
				conf: &global.MachineInfoConfiguration{
					SiblingNumaMemoryBandwidthAllocatableRate: 0.8,
					SiblingNumaMemoryBandwidthCapacity:        10,
				},
				numaDistanceMap: map[int][]NumaDistanceInfo{
					0: {
						{
							NumaID:   0,
							Distance: 10,
						},
						{
							NumaID:   1,
							Distance: 10,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
					},
					1: {
						{
							NumaID:   0,
							Distance: 10,
						},
						{
							NumaID:   1,
							Distance: 10,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
					},
					2: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 10,
						},
						{
							NumaID:   3,
							Distance: 10,
						},
					},
					3: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 10,
						},
						{
							NumaID:   3,
							Distance: 10,
						},
					},
				},
			},
			want: &SiblingNumaInfo{
				SiblingNumaMap: map[int]sets.Int{
					0: sets.NewInt(1),
					1: sets.NewInt(0),
					2: sets.NewInt(3),
					3: sets.NewInt(2),
				},
				SiblingNumaAvgMBWCapacityMap: map[int]int64{
					0: 5,
					1: 5,
					2: 5,
					3: 5,
				},
				SiblingNumaAvgMBWAllocatableMap: map[int]int64{
					0: 4,
					1: 4,
					2: 4,
					3: 4,
				},
			},
		},
		{
			name: "test for with sibling with 3 level distance larger than 10",
			args: args{
				conf: &global.MachineInfoConfiguration{
					SiblingNumaMemoryBandwidthAllocatableRate: 0.8,
					SiblingNumaMaxDistance:                    11,
					SiblingNumaMemoryBandwidthCapacity:        10,
				},
				numaDistanceMap: map[int][]NumaDistanceInfo{
					0: {
						{
							NumaID:   0,
							Distance: 10,
						},
						{
							NumaID:   1,
							Distance: 11,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
					},
					1: {
						{
							NumaID:   0,
							Distance: 11,
						},
						{
							NumaID:   1,
							Distance: 10,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
					},
					2: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 10,
						},
						{
							NumaID:   3,
							Distance: 11,
						},
					},
					3: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 11,
						},
						{
							NumaID:   3,
							Distance: 10,
						},
					},
				},
			},
			want: &SiblingNumaInfo{
				SiblingNumaMap: map[int]sets.Int{
					0: sets.NewInt(1),
					1: sets.NewInt(0),
					2: sets.NewInt(3),
					3: sets.NewInt(2),
				},
				SiblingNumaAvgMBWCapacityMap: map[int]int64{
					0: 5,
					1: 5,
					2: 5,
					3: 5,
				},
				SiblingNumaAvgMBWAllocatableMap: map[int]int64{
					0: 4,
					1: 4,
					2: 4,
					3: 4,
				},
			},
		},
		{
			name: "test for with sibling with 4 level distance larger than 10",
			args: args{
				conf: &global.MachineInfoConfiguration{
					SiblingNumaMemoryBandwidthAllocatableRate: 0.8,
					SiblingNumaMaxDistance:                    12,
					SiblingNumaMemoryBandwidthCapacity:        10,
				},
				numaDistanceMap: map[int][]NumaDistanceInfo{
					0: {
						{
							NumaID:   0,
							Distance: 10,
						},
						{
							NumaID:   1,
							Distance: 11,
						},
						{
							NumaID:   2,
							Distance: 12,
						},
						{
							NumaID:   3,
							Distance: 12,
						},
						{
							NumaID:   4,
							Distance: 32,
						},
						{
							NumaID:   5,
							Distance: 32,
						},
						{
							NumaID:   6,
							Distance: 32,
						},
						{
							NumaID:   7,
							Distance: 32,
						},
					},
					1: {
						{
							NumaID:   0,
							Distance: 11,
						},
						{
							NumaID:   1,
							Distance: 10,
						},
						{
							NumaID:   2,
							Distance: 12,
						},
						{
							NumaID:   3,
							Distance: 12,
						},
						{
							NumaID:   4,
							Distance: 32,
						},
						{
							NumaID:   5,
							Distance: 32,
						},
						{
							NumaID:   6,
							Distance: 32,
						},
						{
							NumaID:   7,
							Distance: 32,
						},
					},
					2: {
						{
							NumaID:   0,
							Distance: 12,
						},
						{
							NumaID:   1,
							Distance: 12,
						},
						{
							NumaID:   2,
							Distance: 10,
						},
						{
							NumaID:   3,
							Distance: 11,
						},
						{
							NumaID:   4,
							Distance: 32,
						},
						{
							NumaID:   5,
							Distance: 32,
						},
						{
							NumaID:   6,
							Distance: 32,
						},
						{
							NumaID:   7,
							Distance: 32,
						},
					},
					3: {
						{
							NumaID:   0,
							Distance: 12,
						},
						{
							NumaID:   1,
							Distance: 12,
						},
						{
							NumaID:   2,
							Distance: 11,
						},
						{
							NumaID:   3,
							Distance: 10,
						},
						{
							NumaID:   4,
							Distance: 32,
						},
						{
							NumaID:   5,
							Distance: 32,
						},
						{
							NumaID:   6,
							Distance: 32,
						},
						{
							NumaID:   7,
							Distance: 32,
						},
					},
					4: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
						{
							NumaID:   4,
							Distance: 10,
						},
						{
							NumaID:   5,
							Distance: 11,
						},
						{
							NumaID:   6,
							Distance: 12,
						},
						{
							NumaID:   7,
							Distance: 12,
						},
					},
					5: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
						{
							NumaID:   4,
							Distance: 11,
						},
						{
							NumaID:   5,
							Distance: 10,
						},
						{
							NumaID:   6,
							Distance: 12,
						},
						{
							NumaID:   7,
							Distance: 12,
						},
					},
					6: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
						{
							NumaID:   4,
							Distance: 12,
						},
						{
							NumaID:   5,
							Distance: 12,
						},
						{
							NumaID:   6,
							Distance: 10,
						},
						{
							NumaID:   7,
							Distance: 11,
						},
					},
					7: {
						{
							NumaID:   0,
							Distance: 32,
						},
						{
							NumaID:   1,
							Distance: 32,
						},
						{
							NumaID:   2,
							Distance: 32,
						},
						{
							NumaID:   3,
							Distance: 32,
						},
						{
							NumaID:   4,
							Distance: 12,
						},
						{
							NumaID:   5,
							Distance: 12,
						},
						{
							NumaID:   6,
							Distance: 11,
						},
						{
							NumaID:   7,
							Distance: 11,
						},
					},
				},
			},
			want: &SiblingNumaInfo{
				SiblingNumaMap: map[int]sets.Int{
					0: sets.NewInt(1),
					1: sets.NewInt(0),
					2: sets.NewInt(3),
					3: sets.NewInt(2),
					4: sets.NewInt(5),
					5: sets.NewInt(4),
					6: sets.NewInt(7),
					7: sets.NewInt(6),
				},
				SiblingNumaAvgMBWCapacityMap: map[int]int64{
					0: 5,
					1: 5,
					2: 5,
					3: 5,
					4: 5,
					5: 5,
					6: 5,
					7: 5,
				},
				SiblingNumaAvgMBWAllocatableMap: map[int]int64{
					0: 4,
					1: 4,
					2: 4,
					3: 4,
					4: 4,
					5: 4,
					6: 4,
					7: 4,
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, GetSiblingNumaInfo(tt.args.conf, tt.args.numaDistanceMap), "GetSiblingNumaInfo(%v, %v)", tt.args.conf, tt.args.numaDistanceMap)
		})
	}
}

func TestGetInterfaceSocketInfo(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		nics        []InterfaceInfo
		cpuTopology *CPUTopology
		expectErr   bool
	}{
		{
			name:        "No sockets available",
			nics:        []InterfaceInfo{{IfIndex: 1, NumaNode: 0}},
			cpuTopology: &CPUTopology{NumSockets: 0},
			expectErr:   true,
		},
		{
			name: "Single NIC, valid socket binding",
			nics: []InterfaceInfo{{IfIndex: 1, NumaNode: 0}},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 1},
			},
			expectErr: false,
		},
		{
			name: "Multiple NICs, evenly distributed",
			nics: []InterfaceInfo{
				{IfIndex: 1, NumaNode: 0},
				{IfIndex: 2, NumaNode: 1},
			},
			cpuTopology: &CPUTopology{
				NumSockets:           2,
				CPUDetails:           CPUDetails{0: CPUTopoInfo{NUMANodeID: 0, SocketID: 0}, 1: CPUTopoInfo{NUMANodeID: 1, SocketID: 1}},
				NUMANodeIDToSocketID: map[int]int{0: 0, 1: 1},
			},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockLock.Lock()
			defer mockLock.Unlock()
			result, err := GetInterfaceSocketInfo(tc.nics, tc.cpuTopology.CPUDetails.Sockets().ToSliceInt())
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}
