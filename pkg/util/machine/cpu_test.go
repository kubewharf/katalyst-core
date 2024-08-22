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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetCoreNumReservedForReclaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		numReservedCores int
		numNumaNodes     int
		want             map[int]int
	}{
		{
			name:             "reserve 4",
			numReservedCores: 4,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 2",
			numReservedCores: 2,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 0, 2: 1, 3: 0},
		},
		{
			name:             "reserve 3",
			numReservedCores: 3,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 0",
			numReservedCores: 0,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 0, 2: 0, 3: 0},
		},
		{
			name:             "reserve 1",
			numReservedCores: 1,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 0, 2: 0, 3: 0},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := GetCoreNumReservedForReclaim(tt.numReservedCores, tt.numNumaNodes)
			assert.Equal(t, tt.want, r)
		})
	}
}
