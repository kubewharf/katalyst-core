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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBatchPoolAllocatorReusesHistoricalThenTopUp(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()
	domain := machine.NewCPUSet(0, 1, 2, 3, 4, 5)
	quantity := map[string]int{"pool-b": 2, "pool-a": 3}
	historical := map[string]machine.CPUSet{
		"pool-a": machine.NewCPUSet(0, 1, 9),
		"pool-b": machine.NewCPUSet(2),
	}

	got, err := allocator.Allocate(domain, quantity, historical)
	require.NoError(t, err)

	require.Equal(t, machine.NewCPUSet(0, 1, 3), got["pool-a"])
	require.Equal(t, machine.NewCPUSet(2, 4), got["pool-b"])
	require.True(t, got["pool-a"].Intersection(got["pool-b"]).IsEmpty())
}

func TestBatchPoolAllocatorUsesDeterministicPoolNameOrderForOverlappingHistory(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()
	domain := machine.NewCPUSet(0, 1, 2, 3)
	quantity := map[string]int{"pool-b": 2, "pool-a": 2}
	historical := map[string]machine.CPUSet{
		"pool-a": machine.NewCPUSet(0, 1),
		"pool-b": machine.NewCPUSet(1, 2),
	}

	got, err := allocator.Allocate(domain, quantity, historical)
	require.NoError(t, err)

	require.Equal(t, machine.NewCPUSet(0, 1), got["pool-a"])
	require.Equal(t, machine.NewCPUSet(2, 3), got["pool-b"])
	require.True(t, got["pool-a"].Intersection(got["pool-b"]).IsEmpty())
}

func TestBatchPoolAllocatorErrorsWhenTotalQuantityExceedsDomain(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()

	got, err := allocator.Allocate(
		machine.NewCPUSet(0, 1),
		map[string]int{"pool-a": 2, "pool-b": 1},
		map[string]machine.CPUSet{"pool-a": machine.NewCPUSet(0)},
	)

	require.Error(t, err)
	require.ErrorContains(t, err, "total pool quantity 3 exceeds domain size 2")
	require.Nil(t, got)
}

func TestBatchPoolAllocatorHandlesNilQuantity(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()

	got, err := allocator.Allocate(
		machine.NewCPUSet(0, 1),
		nil,
		map[string]machine.CPUSet{"pool-a": machine.NewCPUSet(0)},
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestBatchPoolAllocatorHandlesNilHistorical(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()

	got, err := allocator.Allocate(
		machine.NewCPUSet(0, 1, 2),
		map[string]int{"pool-b": 1, "pool-a": 2},
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0, 1), got["pool-a"])
	require.Equal(t, machine.NewCPUSet(2), got["pool-b"])
	require.True(t, got["pool-a"].Intersection(got["pool-b"]).IsEmpty())
}

func TestBatchPoolAllocatorHandlesZeroQuantity(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()

	got, err := allocator.Allocate(
		machine.NewCPUSet(0, 1, 2),
		map[string]int{"pool-a": 0, "pool-b": 2},
		map[string]machine.CPUSet{"pool-a": machine.NewCPUSet(0), "pool-b": machine.NewCPUSet(1)},
	)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(), got["pool-a"])
	require.Equal(t, machine.NewCPUSet(1, 0), got["pool-b"])
	require.True(t, got["pool-a"].Intersection(got["pool-b"]).IsEmpty())
}

func TestBatchPoolAllocatorErrorsWhenQuantityIsNegative(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()

	got, err := allocator.Allocate(
		machine.NewCPUSet(0, 1),
		map[string]int{"pool-a": -1},
		nil,
	)

	require.Error(t, err)
	require.ErrorContains(t, err, `pool "pool-a" has negative quantity -1`)
	require.Nil(t, got)
}

func TestBatchPoolAllocatorDoesNotMutateInputsAndReturnsClones(t *testing.T) {
	t.Parallel()

	allocator := NewBatchPoolAllocator()
	domain := machine.NewCPUSet(0, 1, 2)
	quantity := map[string]int{"pool-a": 2, "pool-b": 1}
	historicalPoolA := machine.NewCPUSet(0, 1)
	historical := map[string]machine.CPUSet{
		"pool-a": historicalPoolA,
	}

	got, err := allocator.Allocate(domain, quantity, historical)
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0, 1, 2), domain)
	require.Equal(t, machine.NewCPUSet(0, 1), historical["pool-a"])

	got["pool-a"].Add(9)
	got["pool-b"].Add(10)
	require.Equal(t, machine.NewCPUSet(0, 1), historical["pool-a"])

	domain.Add(11)
	historicalPoolA.Add(12)
	historical["pool-a"].Add(13)

	require.Equal(t, machine.NewCPUSet(0, 1, 2, 11), domain)
	require.Equal(t, machine.NewCPUSet(0, 1, 12, 13), historical["pool-a"])
	require.Equal(t, machine.NewCPUSet(0, 1), got["pool-a"].Intersection(machine.NewCPUSet(0, 1, 2)))
	require.Equal(t, machine.NewCPUSet(2), got["pool-b"].Intersection(machine.NewCPUSet(0, 1, 2)))

	gotAgain, err := allocator.Allocate(machine.NewCPUSet(0, 1, 2), quantity, map[string]machine.CPUSet{
		"pool-a": machine.NewCPUSet(0, 1),
	})
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0, 1), gotAgain["pool-a"])
	require.Equal(t, machine.NewCPUSet(2), gotAgain["pool-b"])
}
