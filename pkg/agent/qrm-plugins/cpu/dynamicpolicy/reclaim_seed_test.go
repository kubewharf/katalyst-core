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
package dynamicpolicy

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSelectReclaimSeedMinimalCompleteSubset(t *testing.T) {
	t.Parallel()
	for _, sizes := range [][]int{{2, 3}, {3, 2, 2}, {4, 1, 2}, {1, 2, 3, 4}} {
		sizes := sizes
		t.Run(fmt.Sprint(sizes), func(t *testing.T) {
			t.Parallel()
			topology := &machine.CPUTopology{CPUDetails: make(machine.CPUDetails)}
			cpu := 0
			for core, size := range sizes {
				for sibling := 0; sibling < size; sibling++ {
					topology.CPUDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: core % 2, CoreID: core}
					cpu++
				}
			}
			eligible := topology.CPUDetails.CPUs()
			for requested := 1; requested <= cpu; requested++ {
				best := cpu
				for mask := 1; mask < 1<<len(sizes); mask++ {
					sum := 0
					for i, size := range sizes {
						if mask&(1<<i) != 0 {
							sum += size
						}
					}
					if sum >= requested && sum < best {
						best = sum
					}
				}
				got, err := selectReclaimSeed(topology, eligible, machine.NewCPUSet(), requested)
				require.NoError(t, err)
				require.Equal(t, best, got.Size(), "request=%d sizes=%v", requested, sizes)
				require.NoError(t, assertCoreAligned(got, topology))
				for repeat := 0; repeat < 3; repeat++ {
					again, err := selectReclaimSeed(topology, eligible, machine.NewCPUSet(), requested)
					require.NoError(t, err)
					require.True(t, got.Equals(again))
				}
			}
		})
	}
}

func BenchmarkSelectReclaimSeed(b *testing.B) {
	for _, n := range []int{128, 1024} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			topology, err := machine.GenerateDummyCPUTopology(n, 2, 2)
			if err != nil {
				b.Fatal(err)
			}
			eligible := topology.CPUDetails.CPUs()
			prefer := machine.NewCPUSet()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := selectReclaimSeed(topology, eligible, prefer, 4); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
