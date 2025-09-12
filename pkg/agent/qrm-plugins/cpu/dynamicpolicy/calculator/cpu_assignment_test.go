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

package calculator

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestTakeByTopology tests the TakeByTopology function with comprehensive scenarios
// covering various CPU allocation strategies including socket, NUMA node, core, and thread level allocations.
// It also tests L3 cache alignment and complex topology scenarios.
func TestTakeByTopology(t *testing.T) {
	t.Parallel()

	// Mock machine info for testing
	createMockMachineInfo := func(numCPUs, numCores, numSockets, numNUMANodes int, numL3Cache int) *machine.KatalystMachineInfo {
		cpuDetails := make(machine.CPUDetails)

		// Create CPU topology
		for i := 0; i < numCPUs; i++ {
			cpuDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i % numNUMANodes,
				SocketID:   i % numSockets,
				CoreID:     i % numCores,
				L3CacheID:  i % numL3Cache,
			}
		}

		return &machine.KatalystMachineInfo{
			CPUTopology: &machine.CPUTopology{
				NumCPUs:         numCPUs,
				NumCores:        numCores,
				NumSockets:      numSockets,
				NumNUMANodes:    numNUMANodes,
				CPUDetails:      cpuDetails,
				L3CacheTopology: machine.BuildL3CacheTopologyFromMachineInfo(cpuDetails),
			},
		}
	}

	t.Run("zero CPU requirement", func(t *testing.T) {
		t.Parallel()
		// Arrange
		info := createMockMachineInfo(8, 4, 2, 2, 1)
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		cpuRequirement := 0

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 0 {
			t.Errorf("Expected result size 0, got %d", result.Size())
		}
		if !result.IsEmpty() {
			t.Errorf("Expected result to be empty")
		}
	})

	t.Run("insufficient CPUs", func(t *testing.T) {
		t.Parallel()
		// Arrange
		info := createMockMachineInfo(4, 2, 1, 1, 1)
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3)
		cpuRequirement := 10

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)

		// Assert
		if err == nil {
			t.Error("Expected error for insufficient CPUs, got nil")
		}
		if err != nil && !containsSubstring(err.Error(), "insufficient CPUs") {
			t.Errorf("Expected error to contain 'insufficient CPUs', got %v", err)
		}
		if result.Size() != 0 {
			t.Errorf("Expected result size 0, got %d", result.Size())
		}
	})

	t.Run("exact CPU requirement", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation when CPU requirement exactly matches available CPUs
		// This scenario tests the optimal case where all available CPUs are allocated
		info := createMockMachineInfo(4, 2, 1, 1, 1)
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3)
		cpuRequirement := 4

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 4 {
			t.Errorf("Expected result size 4, got %d", result.Size())
		}
		if !result.Equals(availableCPUs) {
			t.Errorf("Expected result to equal availableCPUs")
		}
	})

	t.Run("socket level allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation at the socket level when requesting a full socket's worth of CPUs
		// This scenario tests allocation that should prefer to allocate all CPUs from a single socket
		info := createMockMachineInfo(16, 8, 2, 2, 1)
		// Socket 0: CPUs 0-7, Socket 1: CPUs 8-15
		for i := 0; i < 16; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 8,
				SocketID:   i / 8,
				CoreID:     i % 8,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
		cpuRequirement := 8 // Full socket

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 8 {
			t.Errorf("Expected result size 8, got %d", result.Size())
		}
		// Should allocate from one socket
		if !result.Contains(0) {
			t.Error("Expected result to contain CPU 0")
		}
		if !result.Contains(7) {
			t.Error("Expected result to contain CPU 7")
		}
	})

	t.Run("core level allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation at the core level when requesting multiple cores
		// This scenario tests allocation that should prefer to allocate complete CPU cores
		info := createMockMachineInfo(8, 4, 2, 2, 1)
		// 8 CPUs, 4 cores (2 CPUs per core), 2 sockets
		for i := 0; i < 8; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 4,
				SocketID:   i / 4,
				CoreID:     i / 2,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		cpuRequirement := 4 // 2 cores

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 4 {
			t.Errorf("Expected result size 4, got %d", result.Size())
		}
		// Should allocate complete cores
		if !result.Contains(0) {
			t.Error("Expected result to contain CPU 0")
		}
		if !result.Contains(1) {
			t.Error("Expected result to contain CPU 1")
		}
	})

	t.Run("thread level allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation at the thread level when requesting a single CPU
		// This scenario tests allocation of individual CPU threads when no larger units are needed
		info := createMockMachineInfo(4, 2, 1, 1, 1)
		// 4 CPUs, 2 cores (2 CPUs per core)
		for i := 0; i < 4; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: 0,
				SocketID:   0,
				CoreID:     i / 2,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3)
		cpuRequirement := 1 // Single thread

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 1 {
			t.Errorf("Expected result size 1, got %d", result.Size())
		}
	})

	t.Run("L3 cache alignment enabled", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test L3 cache alignment when enabled
		// This scenario tests allocation with L3 cache alignment enabled to optimize cache performance
		info := createMockMachineInfo(8, 4, 2, 2, 2)
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		cpuRequirement := 4

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 4 {
			t.Errorf("Expected result size 4, got %d", result.Size())
		}
	})

	t.Run("allocation failure - all strategies exhausted", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation failure when all allocation strategies are exhausted
		// This scenario tests error handling when there are insufficient CPUs to satisfy the request
		info := createMockMachineInfo(4, 2, 1, 1, 1)
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3)
		cpuRequirement := 5 // More than available

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)

		// Assert - should fail with insufficient CPUs error
		if err == nil {
			t.Error("Expected error for insufficient CPUs, got nil")
		}
		if err != nil && !containsSubstring(err.Error(), "insufficient CPUs") {
			t.Errorf("Expected error to contain 'insufficient CPUs', got %v", err)
		}
		if result.Size() != 0 {
			t.Errorf("Expected result size 0, got %d", result.Size())
		}
	})

	t.Run("empty available CPUs", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation when no CPUs are available
		// This scenario tests error handling when the available CPU set is empty
		info := createMockMachineInfo(0, 0, 0, 0, 1)
		availableCPUs := machine.NewCPUSet()
		cpuRequirement := 1

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)

		// Assert
		if err == nil {
			t.Error("Expected error for insufficient CPUs, got nil")
		}
		if err != nil && !containsSubstring(err.Error(), "insufficient CPUs") {
			t.Errorf("Expected error to contain 'insufficient CPUs', got %v", err)
		}
		if result.Size() != 0 {
			t.Errorf("Expected result size 0, got %d", result.Size())
		}
	})

	t.Run("complex topology with partial allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation in a complex topology with partial CPU availability
		// This scenario tests allocation across multiple sockets when only partial CPU sets are available
		// Arrange - 16 CPUs, 8 cores, 4 sockets, 2 NUMA nodes
		info := createMockMachineInfo(16, 8, 4, 2, 1)
		// Socket 0: CPUs 0-3 (2 cores)
		// Socket 1: CPUs 4-7 (2 cores)
		// Socket 2: CPUs 8-11 (2 cores)
		// Socket 3: CPUs 12-15 (2 cores)
		for i := 0; i < 16; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 8,
				SocketID:   i / 4,
				CoreID:     i / 2,
			}
		}

		// Only some CPUs available
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11)
		cpuRequirement := 6 // Should get from multiple sockets

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 6 {
			t.Errorf("Expected result size 6, got %d", result.Size())
		}
		if !result.IsSubsetOf(availableCPUs) {
			t.Error("Expected result to be subset of availableCPUs")
		}
	})

	t.Run("NUMA node level allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Create a topology where NUMA nodes map to sockets
		// This test verifies that when a full NUMA node's worth of CPUs is requested,
		// the allocator correctly identifies and allocates all CPUs from a single NUMA node.
		info := createMockMachineInfo(16, 8, 2, 2, 1)
		// NUMA Node 0: CPUs 0-7, NUMA Node 1: CPUs 8-15
		for i := 0; i < 16; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 8,
				SocketID:   i / 8,
				CoreID:     i % 8,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
		cpuRequirement := 8 // Full NUMA node

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 8 {
			t.Errorf("Expected result size 8, got %d", result.Size())
		}
		// Should allocate from one NUMA node
		if !result.Contains(0) {
			t.Error("Expected result to contain CPU 0")
		}
		if !result.Contains(7) {
			t.Error("Expected result to contain CPU 7")
		}
	})

	t.Run("cross-socket allocation with L3 cache alignment", func(t *testing.T) {
		t.Parallel()
		// Arrange: Complex topology with L3 cache alignment
		// This test verifies cross-socket allocation with L3 cache alignment enabled.
		// It ensures that when allocating CPUs across sockets, the allocator respects
		// L3 cache boundaries to optimize performance by minimizing cache contention.
		info := createMockMachineInfo(16, 8, 2, 2, 4)
		// Socket 0: CPUs 0-7, Socket 1: CPUs 8-15
		// L3 Cache 0: CPUs 0-3, L3 Cache 1: CPUs 4-7
		// L3 Cache 2: CPUs 8-11, L3 Cache 3: CPUs 12-15
		for i := 0; i < 16; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 8,
				SocketID:   i / 8,
				CoreID:     i / 2,
				L3CacheID:  i / 4,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
		cpuRequirement := 10 // Requires cross-socket allocation

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 10 {
			t.Errorf("Expected result size 10, got %d", result.Size())
		}
		// Should respect L3 cache alignment when possible
		if !result.Contains(0) || !result.Contains(3) {
			t.Error("Expected result to contain CPUs from first L3 cache (0-3)")
		}
	})

	t.Run("whole L3 cache allocation with L3 cache alignment", func(t *testing.T) {
		t.Parallel()
		// Arrange: Complex topology with L3 cache alignment
		// This test verifies cross-socket allocation with L3 cache alignment enabled.
		// It ensures that when allocating CPUs across sockets, the allocator respects
		// L3 cache boundaries to optimize performance by minimizing cache contention.
		info := createMockMachineInfo(16, 8, 2, 2, 4)
		// Socket 0: CPUs 0-7, Socket 1: CPUs 8-15
		// L3 Cache 0: CPUs 0-3, L3 Cache 1: CPUs 4-7
		// L3 Cache 2: CPUs 8-11, L3 Cache 3: CPUs 12-15
		for i := 0; i < 16; i++ {
			info.CPUTopology.CPUDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 8,
				SocketID:   i / 8,
				CoreID:     i / 2,
				L3CacheID:  i / 4,
			}
		}
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
		cpuRequirement := 4 // Requires on L3 cache

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 4 {
			t.Errorf("Expected result size 4, got %d", result.Size())
		}
		expected := "0-3"
		if result.String() != expected {
			t.Errorf("Expected result string %s, got %s", expected, result.String())
		}
	})
}

// TestTakeByTopologyEdgeCases tests edge cases and error conditions
// including negative CPU requirements and other invalid inputs.
func TestTakeByTopologyEdgeCases(t *testing.T) {
	t.Parallel()

	createMinimalMachineInfo := func() *machine.KatalystMachineInfo {
		cpuDetails := make(machine.CPUDetails)
		cpuDetails[0] = machine.CPUTopoInfo{
			NUMANodeID: 0,
			SocketID:   0,
			CoreID:     0,
			L3CacheID:  0,
		}

		return &machine.KatalystMachineInfo{
			CPUTopology: &machine.CPUTopology{
				NumCPUs:      1,
				NumCores:     1,
				NumSockets:   1,
				NumNUMANodes: 1,
				CPUDetails:   cpuDetails,
			},
		}
	}

	t.Run("negative CPU requirement", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test handling of negative CPU requirements
		// A negative CPU requirement should be treated as satisfied and return an empty CPU set
		info := createMinimalMachineInfo()
		availableCPUs := machine.NewCPUSet(0)
		cpuRequirement := -1

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, false)
		// Assert - negative requirement should be treated as satisfied (returns empty set)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 0 {
			t.Errorf("Expected result size 0, got %d", result.Size())
		}
	})
}

// TestTakeByTopologyWithL3CacheComplexity tests complex L3 cache scenarios
// including partial cache allocation and exact cache size allocation.
func TestTakeByTopologyWithL3CacheComplexity(t *testing.T) {
	t.Parallel()

	createComplexMachineInfo := func() *machine.KatalystMachineInfo {
		cpuDetails := make(machine.CPUDetails)

		// 24 CPUs, 12 cores, 2 sockets, 2 NUMA nodes, 4 L3 caches
		// L3 Cache Layout:
		// Cache 0: 0-5,
		// Cache 1: 12-17
		// Cache 2: 6-11,
		// Cache 3: 18-23
		for i := 0; i < 24; i++ {
			cpuDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 12,
				SocketID:   i / 12,
				CoreID:     i / 2,
				L3CacheID:  i / 6,
			}
		}

		info := &machine.KatalystMachineInfo{
			CPUTopology: &machine.CPUTopology{
				NumCPUs:         24,
				NumCores:        12,
				NumSockets:      2,
				NumNUMANodes:    2,
				CPUDetails:      cpuDetails,
				L3CacheTopology: machine.BuildL3CacheTopologyFromMachineInfo(cpuDetails),
			},
		}
		return info
	}

	t.Run("L3 cache alignment with partial cache", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test L3 cache alignment when requesting a partial cache size
		// This scenario tests allocation that spans multiple L3 caches but doesn't fully utilize any single cache
		info := createComplexMachineInfo()
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
		cpuRequirement := 8

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 8 {
			t.Errorf("Expected result size 8, got %d", result.Size())
		}
		expected := "0-5,12-13"
		if result.String() != expected {
			t.Errorf("Expected result string %s, got %s", expected, result.String())
		}
	})

	t.Run("L3 cache alignment with exact cache size", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test L3 cache alignment when requesting exactly one cache's worth of CPUs
		// This scenario tests allocation that perfectly matches an L3 cache boundary
		info := createComplexMachineInfo()
		availableCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
		cpuRequirement := 12 // Exact size of one L3 cache

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 12 {
			t.Errorf("Expected result size 12, got %d", result.Size())
		}
		expected := "0-11"
		if result.String() != expected {
			t.Errorf("Expected result string %s, got %s", expected, result.String())
		}
	})
}

// TestTakeByTopologyPerformance tests performance-related scenarios
// including large scale allocations and fragmented CPU allocations.
func TestTakeByTopologyPerformance(t *testing.T) {
	t.Parallel()

	createLargeMachineInfo := func() *machine.KatalystMachineInfo {
		cpuDetails := make(machine.CPUDetails)

		// 128 CPUs, 64 cores, 4 sockets, 4 NUMA nodes, 16 L3 caches
		for i := 0; i < 128; i++ {
			cpuDetails[i] = machine.CPUTopoInfo{
				NUMANodeID: i / 32,
				SocketID:   i / 32,
				CoreID:     i / 2,
				L3CacheID:  i / 8,
			}
		}

		info := &machine.KatalystMachineInfo{
			CPUTopology: &machine.CPUTopology{
				NumCPUs:         128,
				NumCores:        64,
				NumSockets:      4,
				NumNUMANodes:    4,
				CPUDetails:      cpuDetails,
				L3CacheTopology: machine.BuildL3CacheTopologyFromMachineInfo(cpuDetails),
			},
		}
		return info
	}

	t.Run("large scale allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation performance with a large number of CPUs (128 total)
		// This scenario tests allocation of half the available CPUs in a large system
		info := createLargeMachineInfo()
		availableCPUs := machine.NewCPUSet()
		for i := 0; i < 128; i++ {
			availableCPUs.Add(i)
		}
		cpuRequirement := 64 // Half of available CPUs

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 64 {
			t.Errorf("Expected result size 64, got %d", result.Size())
		}
		expected := "0-63"
		if result.String() != expected {
			t.Errorf("Expected result string %s, got %s", expected, result.String())
		}
	})

	t.Run("fragmented CPU allocation", func(t *testing.T) {
		t.Parallel()
		// Arrange: Test allocation with fragmented CPU availability
		// This scenario simulates a system where CPUs are not contiguous due to prior allocations
		info := createLargeMachineInfo()
		availableCPUs := machine.NewCPUSet()
		// Create fragmented availability
		for i := 0; i < 128; i += 3 {
			availableCPUs.Add(i)
		}
		cpuRequirement := 10

		// Act
		result, err := TakeByTopology(info, availableCPUs, cpuRequirement, true)
		// Assert
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Size() != 10 {
			t.Errorf("Expected result size 10, got %d", result.Size())
		}
		expected := "0,3,6,9,12,15,18,24,27,30"
		if result.String() != expected {
			t.Errorf("Expected result string %s, got %s", expected, result.String())
		}
	})
}

// containsSubstring is a helper function to check if a string contains a substring
func containsSubstring(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
