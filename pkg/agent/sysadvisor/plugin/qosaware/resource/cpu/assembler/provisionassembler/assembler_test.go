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
		expectedPoolSizes                     map[string]int
	}{
		{
			name:              "test1",
			available:         12,
			enableReclaim:     false,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 2, "batch": 4, "flink": 6},
		},
		{
			name:              "test2",
			available:         12,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 3},
		},
		{
			name:              "test3",
			available:         6,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 3},
		},
		{
			name:              "test4",
			available:         5,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 2, "flink": 2},
		},
		{
			name:              "test5",
			available:         4,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 1, "flink": 2},
		},
		{
			name:              "test6",
			available:         3,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 1, "batch": 1, "flink": 1},
		},
		{
			name:              "test7",
			available:         2,
			enableReclaim:     true,
			poolSizes:         map[string]int{"share": 1, "batch": 2, "flink": 3},
			expectedPoolSizes: map[string]int{"share": 2, "batch": 2, "flink": 2},
		},
		{
			name:                                  "test8",
			available:                             24,
			enableReclaim:                         true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolSizes:                             map[string]int{"share": 1, "batch": 2, "flink": 3},
			isolatedPoolSizes:                     map[string]int{"i1": 2, "i2": 2},
			expectedPoolSizes:                     map[string]int{"share": 3, "batch": 7, "flink": 10, "i1": 2, "i2": 2},
		},
		{
			name:                                  "test9",
			available:                             8,
			enableReclaim:                         true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolSizes:                             map[string]int{"share": 2, "batch": 4, "flink": 6},
			isolatedPoolSizes:                     map[string]int{"i1": 2, "i2": 2},
			expectedPoolSizes:                     map[string]int{"share": 1, "batch": 2, "flink": 3, "i1": 1, "i2": 1},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			poolSizes, _ := regulatePoolSizes(tt.poolSizes, tt.isolatedPoolSizes, tt.available, !tt.enableReclaim || tt.allowSharedCoresOverlapReclaimedCores)
			assert.Equal(t, tt.expectedPoolSizes, poolSizes)
		})
	}
}
