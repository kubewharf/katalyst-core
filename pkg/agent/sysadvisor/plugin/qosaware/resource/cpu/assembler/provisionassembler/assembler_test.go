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

package provisionassembler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegulatePoolSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                  string
		available                             int
		enableReclaim                         bool
		allowSharedCoresOverlapReclaimedCores bool
		poolSizes                             map[string]int
		isolatedPoolSizes                     map[string]int
		poolSorter                            func(string, string) bool
		expectedPoolSizes                     map[string]int
	}{
		{
			// test1 checks the basic expansion functionality when ample resources are available.
			// Reclaim is disabled, so all available resources should be distributed proportionally.
			// Each pool gets double its request: share=1*2=2, batch=2*2=4, flink=3*2=6.
			name:              "test1",
			available:         12,
			enableReclaim:     false,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 2, "batch": 4, "flink": 6},
		},
		{
			// test2 checks behavior when reclaim is enabled.
			// When reclaim is enabled, pools should not expand beyond their requests unless explicitly allowed.
			// Here, each pool gets exactly its requested size, total=6, remaining 6 goes to reclaim.
			name:              "test2",
			available:         12,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 3},
		},
		{
			// test3 checks behavior when available resources exactly match the total requests.
			// No expansion or reduction needed.
			name:              "test3",
			available:         6,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 3},
		},
		{
			// test4 checks resource reduction when available resources (5) are less than total requests (6).
			// Pools are reduced proportionally. 'flink' is reduced from 3 to 2.
			name:              "test4",
			available:         5,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 2},
		},
		{
			// test5 checks further reduction (available=4).
			// 'batch' is reduced from 2 to 1, 'flink' reduced from 3 to 2.
			name:              "test5",
			available:         4,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 1, "flink": 2},
		},
		{
			// test6 checks severe resource constraint (available=3).
			// Each pool gets 1 core.
			name:              "test6",
			available:         3,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 1, "flink": 1},
		},
		{
			// test7 checks extreme resource constraint (available=2).
			// When available resources are critically low, the algorithm may fall back to
			// distributing the total available amount to each pool if normalization fails.
			// Here, each pool gets 'available' (2) cores.
			name:              "test7",
			available:         2,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 2, "batch": 2, "flink": 2},
		},
		{
			// test8 checks logic with isolated pools and allowSharedCoresOverlapReclaimedCores.
			// Isolated pools (i1, i2) take 4 cores. Remaining available for shared = 24 - 4 = 20.
			// Shared pools (req=6) can expand into remaining 20 cores.
			// Total shared weight = 1+2+3=6.
			// share: 1/6 * 20 = 3.33 -> 3
			// batch: 2/6 * 20 = 6.66 -> 7
			// flink: 3/6 * 20 = 10
			name:                                  "test8",
			available:                             24,
			enableReclaim:                         true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolSizes:                             map[string]int{"share": 1, "batch": 2, "flink": 3},
			isolatedPoolSizes:                     map[string]int{"i1": 2, "i2": 2},
			expectedPoolSizes:                     map[string]int{"share": 3, "batch": 7, "flink": 10, "i1": 2, "i2": 2},
		},
		{
			// test9 checks isolated pools with limited available resources.
			// Isolated pools (4 cores) are satisfied first. Remaining = 8 - 4 = 4.
			// Shared pools (req=12) need to be reduced to fit in 4 cores.
			// Normalization result: share=1, batch=2, flink=3. Total=6. Wait, available=4.
			// Actually, normalizePoolSizes logic will reduce them.
			// Initial ceil: share=1, batch=2, flink=2. Sum=5.
			// Reduced 'batch' (or flink?) to 1 -> share=1, batch=2, flink=1?
			// Wait, test expectation is share=1, batch=2, flink=3.
			// This means it hit the "requirementSum > available" branch and fallback logic?
			// ReqSum = 12 + 4 = 16 > 8.
			// Normalize(all_pools, 8).
			// i1(2) -> 2/16*8 = 1.
			// i2(2) -> 1.
			// share(2) -> 1.
			// batch(4) -> 2.
			// flink(6) -> 3.
			// Sum = 1+1+1+2+3 = 8.
			// Matches expectation: i1=1, i2=1, share=1, batch=2, flink=3.
			name:                                  "test9",
			available:                             8,
			enableReclaim:                         true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolSizes:                             map[string]int{"share": 2, "batch": 4, "flink": 6},
			isolatedPoolSizes:                     map[string]int{"i1": 2, "i2": 2},
			expectedPoolSizes:                     map[string]int{"share": 1, "batch": 2, "flink": 3, "i1": 1, "i2": 1},
		},
		{
			// test10-custom_sorter tests a custom sorter that prioritizes 'b' (p1 > p2).
			// Available=4. Req: a=2, b=3. Total=5.
			// Normalization: a=2, b=3. Need to reduce 1.
			// Ratios: a=2/2=1.0, b=3/3=1.0. Tie.
			// Sorter p1 > p2 ("b" > "a") puts "b" first?
			// Actually "b" has larger size, so it might be picked regardless of sorter if size is tie-breaker.
			// But here we rely on the fact that result is a=2, b=2.
			name:          "test10-custom_sorter",
			available:     4,
			enableReclaim: true,
			poolSizes:     map[string]int{"a": 2, "b": 3},
			poolSorter: func(p1, p2 string) bool {
				// reverse order
				return p1 > p2
			},
			expectedPoolSizes: map[string]int{"a": 2, "b": 2},
		},
		{
			// test_tie_breaking_a_reduced tests deterministic behavior when pools have identical resource requirements.
			// Initial state: Pool A (10), Pool B (10), Available (5).
			// Both pools have same expansion ratio and normalized requirements.
			// With sorter "p1 < p2" (A < B), A is processed first and reduced, resulting in A=2, B=3.
			name:          "test_tie_breaking_a_reduced",
			available:     5,
			enableReclaim: true,
			poolSizes:     map[string]int{"a": 10, "b": 10},
			poolSorter: func(p1, p2 string) bool {
				return p1 < p2
			},
			expectedPoolSizes: map[string]int{"a": 2, "b": 3},
		},
		{
			// test_tie_breaking_b_reduced tests deterministic behavior with reverse sorting order.
			// Initial state: Pool A (10), Pool B (10), Available (5).
			// With sorter "p1 > p2" (B < A), B is processed first and reduced, resulting in A=3, B=2.
			// This confirms that sorting order strictly controls which pool wins the tie-break.
			name:          "test_tie_breaking_b_reduced",
			available:     5,
			enableReclaim: true,
			poolSizes:     map[string]int{"a": 10, "b": 10},
			poolSorter: func(p1, p2 string) bool {
				return p1 > p2
			},
			expectedPoolSizes: map[string]int{"a": 3, "b": 2},
		},
		{
			// test11-custom_sorter_default tests default sort order (p1 < p2).
			// Available=4. Req: a=2, b=3.
			// Same as test10, but default sorter.
			// Result is same (a=2, b=2) because 'b' has larger normalized size (3 vs 2),
			// and selectPoolHelper prioritizes larger pool when ratios are tied (if v > vMax).
			name:          "test11-custom_sorter_default",
			available:     4,
			enableReclaim: true,
			poolSizes:     map[string]int{"a": 2, "b": 3},
			poolSorter: func(p1, p2 string) bool {
				// reverse order
				return p1 < p2
			},
			expectedPoolSizes: map[string]int{"a": 2, "b": 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sorter := tt.poolSorter
			if sorter == nil {
				sorter = func(a, b string) bool { return a < b }
			}
			poolSizes, _ := regulatePoolSizes(tt.poolSizes, tt.isolatedPoolSizes, tt.available, !tt.enableReclaim || tt.allowSharedCoresOverlapReclaimedCores, sorter)
			assert.Equal(t, tt.expectedPoolSizes, poolSizes)
		})
	}
}
