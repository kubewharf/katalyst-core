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

package noderesourcetopology

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
)

// TestScoreStrategyWeightSumZeroDoesNotPanic verifies that the most/least
// allocated score strategies return 0 instead of dividing by zero when no
// requested resource contributes to weightSum. This happens when a scored
// container has an empty resource request, or a request whose resources are
// all outside alignedResource (both reachable for a dedicated_cores +
// numa_binding pod under the dynamic resource plugin policy).
func TestScoreStrategyWeightSumZeroDoesNotPanic(t *testing.T) {
	t.Parallel()

	weightMap := resourceToWeightMap{}
	tests := []struct {
		name            string
		requested       v1.ResourceList
		allocatable     v1.ResourceList
		alignedResource sets.String
	}{
		{
			name:      "empty request",
			requested: v1.ResourceList{},
		},
		{
			name:            "request only has resources outside alignedResource",
			requested:       v1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
			allocatable:     v1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")},
			alignedResource: sets.NewString(v1.ResourceCPU.String(), v1.ResourceMemory.String()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var most, least int64
			assert.NotPanics(t, func() {
				most = mostAllocatedScoreStrategy(tt.requested, tt.allocatable, weightMap, tt.alignedResource)
			})
			assert.NotPanics(t, func() {
				least = leastAllocatedScoreStrategy(tt.requested, tt.allocatable, weightMap, tt.alignedResource)
			})
			assert.Equal(t, int64(0), most)
			assert.Equal(t, int64(0), least)
		})
	}
}
