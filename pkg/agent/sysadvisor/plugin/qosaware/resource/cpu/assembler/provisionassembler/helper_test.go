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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type regulateOverlapReclaimPoolSizeCase struct {
	name                           string
	sharePoolSizes                 map[string]int
	overlapReclaimPoolSizeRequired int
	expectError                    error
	expectOverlapReclaimPoolSize   map[string]int
}

func Test_regulateOverlapReclaimPoolSize(t *testing.T) {
	t.Parallel()

	testCases := []regulateOverlapReclaimPoolSizeCase{
		{
			name: "case1",
			sharePoolSizes: map[string]int{
				"share": 9,
				"bmq":   8,
				"flink": 7,
			},
			overlapReclaimPoolSizeRequired: 30,
			expectError:                    fmt.Errorf("invalid sharedOverlapReclaimSize"),
			expectOverlapReclaimPoolSize:   nil,
		},
		{
			name: "case2",
			sharePoolSizes: map[string]int{
				"share": 9,
				"bmq":   8,
				"flink": 7,
			},
			overlapReclaimPoolSizeRequired: 2,
			expectError:                    nil,
			expectOverlapReclaimPoolSize: map[string]int{
				"share": 1,
				"bmq":   1,
			},
		},
		{
			name: "case3",
			sharePoolSizes: map[string]int{
				"share": 9,
				"bmq":   8,
				"flink": 7,
			},
			overlapReclaimPoolSizeRequired: 4,
			expectError:                    nil,
			expectOverlapReclaimPoolSize: map[string]int{
				"share": 2,
				"bmq":   2,
			},
		},
		{
			name: "case4",
			sharePoolSizes: map[string]int{
				"share": 9,
				"bmq":   8,
				"flink": 7,
			},
			overlapReclaimPoolSizeRequired: 8,
			expectError:                    nil,
			expectOverlapReclaimPoolSize: map[string]int{
				"share": 3,
				"bmq":   3,
				"flink": 2,
			},
		},
	}

	for _, testCase := range testCases {
		func(testCase regulateOverlapReclaimPoolSizeCase) {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				result, err := regulateOverlapReclaimPoolSize(testCase.sharePoolSizes, testCase.overlapReclaimPoolSizeRequired)
				if testCase.expectError == nil {
					require.NoError(t, err, "failed to regulate overlap reclaim pool size")
				} else {
					require.EqualError(t, err, testCase.expectError.Error(), "invalid error return")
				}
				require.Equal(t, testCase.expectOverlapReclaimPoolSize, result, "invalid result")

			})
		}(testCase)
	}
}
