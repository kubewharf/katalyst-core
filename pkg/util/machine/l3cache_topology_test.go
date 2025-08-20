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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildL3CacheTopologyFromMachineInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cpuDetails CPUDetails
		expected   L3CacheTopology
	}{
		{
			name:       "Empty CPU Details",
			cpuDetails: CPUDetails{},
			expected: L3CacheTopology{
				L3CacheToCPUs:      map[int]CPUSet{},
				NUMAToL3Caches:     map[int]CPUSet{},
				L3CacheToNUMANodes: map[int]CPUSet{},
			},
		},
		{
			name: "Single CPU Single NUMA Single L3",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0),
				},
			},
		},
		{
			name: "Multiple CPUs Single NUMA Single L3",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
				1: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     1,
					L3CacheID:  0,
				},
				2: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     2,
					L3CacheID:  0,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0, 1, 2),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0),
				},
			},
		},
		{
			name: "Multiple CPUs Multiple NUMA Multiple L3",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
				1: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     1,
					L3CacheID:  0,
				},
				2: {
					NUMANodeID: 1,
					SocketID:   1,
					CoreID:     2,
					L3CacheID:  1,
				},
				3: {
					NUMANodeID: 1,
					SocketID:   1,
					CoreID:     3,
					L3CacheID:  1,
				},
				4: {
					NUMANodeID: 2,
					SocketID:   1,
					CoreID:     4,
					L3CacheID:  2,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0, 1),
					1: NewCPUSet(2, 3),
					2: NewCPUSet(4),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
					1: NewCPUSet(1),
					2: NewCPUSet(2),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0),
					1: NewCPUSet(1),
					2: NewCPUSet(2),
				},
			},
		},
		{
			name: "Shared L3 Cache Across NUMA Nodes",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
				1: {
					NUMANodeID: 1,
					SocketID:   0,
					CoreID:     1,
					L3CacheID:  0,
				},
				2: {
					NUMANodeID: 2,
					SocketID:   1,
					CoreID:     2,
					L3CacheID:  0,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0, 1, 2),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
					1: NewCPUSet(0),
					2: NewCPUSet(0),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0, 1, 2),
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := BuildL3CacheTopologyFromMachineInfo(tt.cpuDetails)

			// Compare L3CacheToCPUs
			if !reflect.DeepEqual(got.L3CacheToCPUs, tt.expected.L3CacheToCPUs) {
				t.Errorf("BuildL3CacheTopologyFromMachineInfo() L3CacheToCPUs = %v, want %v", got.L3CacheToCPUs, tt.expected.L3CacheToCPUs)
			}

			// Compare NUMAToL3Caches
			if !reflect.DeepEqual(got.NUMAToL3Caches, tt.expected.NUMAToL3Caches) {
				t.Errorf("BuildL3CacheTopologyFromMachineInfo() NUMAToL3Caches = %v, want %v", got.NUMAToL3Caches, tt.expected.NUMAToL3Caches)
			}

			// Compare L3CacheToNUMANodes
			if !reflect.DeepEqual(got.L3CacheToNUMANodes, tt.expected.L3CacheToNUMANodes) {
				t.Errorf("BuildL3CacheTopologyFromMachineInfo() L3CacheToNUMANodes = %v, want %v", got.L3CacheToNUMANodes, tt.expected.L3CacheToNUMANodes)
			}
		})
	}
}

func TestBuildDummyL3CacheTopology(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cpuDetails CPUDetails
		expected   L3CacheTopology
	}{
		{
			name:       "Empty CPU Details",
			cpuDetails: CPUDetails{},
			expected: L3CacheTopology{
				L3CacheToCPUs:      map[int]CPUSet{},
				NUMAToL3Caches:     map[int]CPUSet{},
				L3CacheToNUMANodes: map[int]CPUSet{},
			},
		},
		{
			name: "Single NUMA Node",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
				1: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     1,
					L3CacheID:  0,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0, 1),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0),
				},
			},
		},
		{
			name: "Multiple NUMA Nodes",
			cpuDetails: CPUDetails{
				0: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     0,
					L3CacheID:  0,
				},
				1: {
					NUMANodeID: 0,
					SocketID:   0,
					CoreID:     1,
					L3CacheID:  0,
				},
				2: {
					NUMANodeID: 1,
					SocketID:   1,
					CoreID:     2,
					L3CacheID:  1,
				},
				3: {
					NUMANodeID: 1,
					SocketID:   1,
					CoreID:     3,
					L3CacheID:  1,
				},
				4: {
					NUMANodeID: 2,
					SocketID:   1,
					CoreID:     4,
					L3CacheID:  2,
				},
				5: {
					NUMANodeID: 2,
					SocketID:   1,
					CoreID:     5,
					L3CacheID:  2,
				},
			},
			expected: L3CacheTopology{
				L3CacheToCPUs: map[int]CPUSet{
					0: NewCPUSet(0, 1),
					1: NewCPUSet(2, 3),
					2: NewCPUSet(4, 5),
				},
				NUMAToL3Caches: map[int]CPUSet{
					0: NewCPUSet(0),
					1: NewCPUSet(1),
					2: NewCPUSet(2),
				},
				L3CacheToNUMANodes: map[int]CPUSet{
					0: NewCPUSet(0),
					1: NewCPUSet(1),
					2: NewCPUSet(2),
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := BuildDummyL3CacheTopology(tt.cpuDetails)

			// Compare L3CacheToCPUs
			if !reflect.DeepEqual(got.L3CacheToCPUs, tt.expected.L3CacheToCPUs) {
				t.Errorf("BuildDummyL3CacheTopology() L3CacheToCPUs = %v, want %v", got.L3CacheToCPUs, tt.expected.L3CacheToCPUs)
			}

			// Compare NUMAToL3Caches
			if !reflect.DeepEqual(got.NUMAToL3Caches, tt.expected.NUMAToL3Caches) {
				t.Errorf("BuildDummyL3CacheTopology() NUMAToL3Caches = %v, want %v", got.NUMAToL3Caches, tt.expected.NUMAToL3Caches)
			}

			// Compare L3CacheToNUMANodes
			if !reflect.DeepEqual(got.L3CacheToNUMANodes, tt.expected.L3CacheToNUMANodes) {
				t.Errorf("BuildDummyL3CacheTopology() L3CacheToNUMANodes = %v, want %v", got.L3CacheToNUMANodes, tt.expected.L3CacheToNUMANodes)
			}
		})
	}
}

func TestL3CacheTopology_CPUsInL3Caches(t *testing.T) {
	t.Parallel()

	// Setup test topology
	topology := L3CacheTopology{
		L3CacheToCPUs: map[int]CPUSet{
			0: NewCPUSet(0, 1, 2),
			1: NewCPUSet(3, 4, 5),
			2: NewCPUSet(6, 7),
			3: NewCPUSet(8, 9, 10, 11),
		},
		NUMAToL3Caches:     map[int]CPUSet{},
		L3CacheToNUMANodes: map[int]CPUSet{},
	}

	tests := []struct {
		name     string
		ids      []int
		expected CPUSet
	}{
		{
			name:     "Single L3 Cache",
			ids:      []int{0},
			expected: NewCPUSet(0, 1, 2),
		},
		{
			name:     "Multiple L3 Caches",
			ids:      []int{0, 1},
			expected: NewCPUSet(0, 1, 2, 3, 4, 5),
		},
		{
			name:     "Non-existent L3 Cache",
			ids:      []int{999},
			expected: NewCPUSet(),
		},
		{
			name:     "Mixed Existing and Non-existing",
			ids:      []int{0, 999, 2},
			expected: NewCPUSet(0, 1, 2, 6, 7),
		},
		{
			name:     "Empty IDs",
			ids:      []int{},
			expected: NewCPUSet(),
		},
		{
			name:     "All L3 Caches",
			ids:      []int{0, 1, 2, 3},
			expected: NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := topology.CPUsInL3Caches(tt.ids...)

			if !got.Equals(tt.expected) {
				t.Errorf("CPUsInL3Caches() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestL3CacheTopology_L3CachesInNUMANodes(t *testing.T) {
	t.Parallel()

	// Setup test topology
	topology := L3CacheTopology{
		L3CacheToCPUs: map[int]CPUSet{},
		NUMAToL3Caches: map[int]CPUSet{
			0: NewCPUSet(0, 1),
			1: NewCPUSet(2, 3),
			2: NewCPUSet(4),
			3: NewCPUSet(5, 6),
		},
		L3CacheToNUMANodes: map[int]CPUSet{},
	}

	tests := []struct {
		name     string
		ids      []int
		expected CPUSet
	}{
		{
			name:     "Single NUMA Node",
			ids:      []int{0},
			expected: NewCPUSet(0, 1),
		},
		{
			name:     "Multiple NUMA Nodes",
			ids:      []int{0, 1},
			expected: NewCPUSet(0, 1, 2, 3),
		},
		{
			name:     "Non-existent NUMA Node",
			ids:      []int{999},
			expected: NewCPUSet(),
		},
		{
			name:     "Mixed Existing and Non-existing",
			ids:      []int{0, 999, 2},
			expected: NewCPUSet(0, 1, 4),
		},
		{
			name:     "Empty IDs",
			ids:      []int{},
			expected: NewCPUSet(),
		},
		{
			name:     "All NUMA Nodes",
			ids:      []int{0, 1, 2, 3},
			expected: NewCPUSet(0, 1, 2, 3, 4, 5, 6),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := topology.L3CachesInNUMANodes(tt.ids...)

			if !got.Equals(tt.expected) {
				t.Errorf("L3CachesInNUMANodes() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestL3CacheTopology_NUMANodesInL3Caches(t *testing.T) {
	t.Parallel()

	// Setup test topology
	topology := L3CacheTopology{
		L3CacheToCPUs:  map[int]CPUSet{},
		NUMAToL3Caches: map[int]CPUSet{},
		L3CacheToNUMANodes: map[int]CPUSet{
			0: NewCPUSet(0, 1),
			1: NewCPUSet(2, 3),
			2: NewCPUSet(4),
			3: NewCPUSet(5, 6, 7),
		},
	}

	tests := []struct {
		name     string
		ids      []int
		expected CPUSet
	}{
		{
			name:     "Single L3 Cache",
			ids:      []int{0},
			expected: NewCPUSet(0, 1),
		},
		{
			name:     "Multiple L3 Caches",
			ids:      []int{0, 1},
			expected: NewCPUSet(0, 1, 2, 3),
		},
		{
			name:     "Non-existent L3 Cache",
			ids:      []int{999},
			expected: NewCPUSet(),
		},
		{
			name:     "Mixed Existing and Non-existing",
			ids:      []int{0, 999, 2},
			expected: NewCPUSet(0, 1, 4),
		},
		{
			name:     "Empty IDs",
			ids:      []int{},
			expected: NewCPUSet(),
		},
		{
			name:     "All L3 Caches",
			ids:      []int{0, 1, 2, 3},
			expected: NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := topology.NUMANodesInL3Caches(tt.ids...)

			if !got.Equals(tt.expected) {
				t.Errorf("NUMANodesInL3Caches() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestL3CacheTopologyIntegration(t *testing.T) {
	t.Parallel()

	// Integration test with a realistic topology
	cpuDetails := CPUDetails{
		// NUMA Node 0, L3 Cache 0
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0, L3CacheID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 1, L3CacheID: 0},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 2, L3CacheID: 0},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 3, L3CacheID: 0},

		// NUMA Node 1, L3 Cache 1
		4: {NUMANodeID: 1, SocketID: 0, CoreID: 4, L3CacheID: 1},
		5: {NUMANodeID: 1, SocketID: 0, CoreID: 5, L3CacheID: 1},
		6: {NUMANodeID: 1, SocketID: 0, CoreID: 6, L3CacheID: 1},
		7: {NUMANodeID: 1, SocketID: 0, CoreID: 7, L3CacheID: 1},

		// NUMA Node 2, L3 Cache 2 (shared across NUMA 2 and 3)
		8:  {NUMANodeID: 2, SocketID: 1, CoreID: 8, L3CacheID: 2},
		9:  {NUMANodeID: 2, SocketID: 1, CoreID: 9, L3CacheID: 2},
		10: {NUMANodeID: 3, SocketID: 1, CoreID: 10, L3CacheID: 2},
		11: {NUMANodeID: 3, SocketID: 1, CoreID: 11, L3CacheID: 2},
	}

	// Test both topology builders
	topologyFromMachine := BuildL3CacheTopologyFromMachineInfo(cpuDetails)
	topologyDummy := BuildDummyL3CacheTopology(cpuDetails)

	t.Run("Integration Test - BuildL3CacheTopologyFromMachineInfo", func(t *testing.T) {
		t.Parallel()

		// Verify L3CacheToCPUs
		assert.Equal(t, NewCPUSet(0, 1, 2, 3), topologyFromMachine.CPUsInL3Caches(0))
		assert.Equal(t, NewCPUSet(4, 5, 6, 7), topologyFromMachine.CPUsInL3Caches(1))
		assert.Equal(t, NewCPUSet(8, 9, 10, 11), topologyFromMachine.CPUsInL3Caches(2))

		// Verify NUMAToL3Caches
		assert.Equal(t, NewCPUSet(0), topologyFromMachine.L3CachesInNUMANodes(0))
		assert.Equal(t, NewCPUSet(1), topologyFromMachine.L3CachesInNUMANodes(1))
		assert.Equal(t, NewCPUSet(2), topologyFromMachine.L3CachesInNUMANodes(2))
		assert.Equal(t, NewCPUSet(2), topologyFromMachine.L3CachesInNUMANodes(3))

		// Verify L3CacheToNUMANodes
		assert.Equal(t, NewCPUSet(0), topologyFromMachine.NUMANodesInL3Caches(0))
		assert.Equal(t, NewCPUSet(1), topologyFromMachine.NUMANodesInL3Caches(1))
		assert.Equal(t, NewCPUSet(2, 3), topologyFromMachine.NUMANodesInL3Caches(2))
	})

	t.Run("Integration Test - BuildDummyL3CacheTopology", func(t *testing.T) {
		t.Parallel()

		// In dummy topology, each NUMA node has its own L3 cache
		assert.Equal(t, NewCPUSet(0, 1, 2, 3), topologyDummy.CPUsInL3Caches(0))
		assert.Equal(t, NewCPUSet(4, 5, 6, 7), topologyDummy.CPUsInL3Caches(1))
		assert.Equal(t, NewCPUSet(8, 9), topologyDummy.CPUsInL3Caches(2))
		assert.Equal(t, NewCPUSet(10, 11), topologyDummy.CPUsInL3Caches(3))

		// Verify NUMAToL3Caches - each NUMA node maps to its own L3 cache
		assert.Equal(t, NewCPUSet(0), topologyDummy.L3CachesInNUMANodes(0))
		assert.Equal(t, NewCPUSet(1), topologyDummy.L3CachesInNUMANodes(1))
		assert.Equal(t, NewCPUSet(2), topologyDummy.L3CachesInNUMANodes(2))
		assert.Equal(t, NewCPUSet(3), topologyDummy.L3CachesInNUMANodes(3))

		// Verify L3CacheToNUMANodes - each L3 cache maps to exactly one NUMA node
		assert.Equal(t, NewCPUSet(0), topologyDummy.NUMANodesInL3Caches(0))
		assert.Equal(t, NewCPUSet(1), topologyDummy.NUMANodesInL3Caches(1))
		assert.Equal(t, NewCPUSet(2), topologyDummy.NUMANodesInL3Caches(2))
		assert.Equal(t, NewCPUSet(3), topologyDummy.NUMANodesInL3Caches(3))
	})
}

func TestL3CacheTopologyEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("Negative L3 Cache IDs", func(t *testing.T) {
		t.Parallel()

		cpuDetails := CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0, L3CacheID: -1},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1, L3CacheID: -2},
		}

		topology := BuildL3CacheTopologyFromMachineInfo(cpuDetails)

		// Should handle negative cache IDs correctly
		assert.Equal(t, NewCPUSet(0), topology.CPUsInL3Caches(-1))
		assert.Equal(t, NewCPUSet(1), topology.CPUsInL3Caches(-2))
		assert.Equal(t, NewCPUSet(-1, -2), topology.L3CachesInNUMANodes(0))
		assert.Equal(t, NewCPUSet(0), topology.NUMANodesInL3Caches(-1))
		assert.Equal(t, NewCPUSet(0), topology.NUMANodesInL3Caches(-2))
	})

	t.Run("Large NUMA and Cache IDs", func(t *testing.T) {
		t.Parallel()

		cpuDetails := CPUDetails{
			0: {NUMANodeID: 1024, SocketID: 0, CoreID: 0, L3CacheID: 2048},
			1: {NUMANodeID: 1024, SocketID: 0, CoreID: 1, L3CacheID: 2048},
			2: {NUMANodeID: 2048, SocketID: 1, CoreID: 2, L3CacheID: 4096},
		}

		topology := BuildL3CacheTopologyFromMachineInfo(cpuDetails)

		// Should handle large IDs correctly
		assert.Equal(t, NewCPUSet(0, 1), topology.CPUsInL3Caches(2048))
		assert.Equal(t, NewCPUSet(2), topology.CPUsInL3Caches(4096))
		assert.Equal(t, NewCPUSet(2048), topology.L3CachesInNUMANodes(1024))
		assert.Equal(t, NewCPUSet(4096), topology.L3CachesInNUMANodes(2048))
		assert.Equal(t, NewCPUSet(1024), topology.NUMANodesInL3Caches(2048))
		assert.Equal(t, NewCPUSet(2048), topology.NUMANodesInL3Caches(4096))
	})

	t.Run("Empty Topology Queries", func(t *testing.T) {
		t.Parallel()

		emptyTopology := L3CacheTopology{
			L3CacheToCPUs:      map[int]CPUSet{},
			NUMAToL3Caches:     map[int]CPUSet{},
			L3CacheToNUMANodes: map[int]CPUSet{},
		}

		// All queries on empty topology should return empty CPUSet
		assert.Equal(t, NewCPUSet(), emptyTopology.CPUsInL3Caches(0))
		assert.Equal(t, NewCPUSet(), emptyTopology.CPUsInL3Caches(0, 1, 2))
		assert.Equal(t, NewCPUSet(), emptyTopology.L3CachesInNUMANodes(0))
		assert.Equal(t, NewCPUSet(), emptyTopology.L3CachesInNUMANodes(0, 1, 2))
		assert.Equal(t, NewCPUSet(), emptyTopology.NUMANodesInL3Caches(0))
		assert.Equal(t, NewCPUSet(), emptyTopology.NUMANodesInL3Caches(0, 1, 2))
	})
}
