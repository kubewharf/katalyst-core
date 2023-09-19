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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func leastAllocatedScoreStrategy(requested, allocatable v1.ResourceList, resourceToWeightMap resourceToWeightMap, alignedResource sets.String) int64 {
	var score int64 = 0
	var weightSum int64 = 0

	for resourceName := range requested {
		// resources not in alignedResource will not be calculated,
		// these resources may not be allocated to the same numas with alignedResource
		if alignedResource != nil && !alignedResource.Has(resourceName.String()) {
			continue
		}
		resourceScore := leastAllocatedScore(requested[resourceName], allocatable[resourceName])
		weight := resourceToWeightMap.weight(resourceName)
		score += resourceScore * weight
		weightSum += weight
	}

	return score / weightSum
}

// The used capacity is calculated on a scale of 0-MaxNodeScore (MaxNodeScore is
// constant with value set to 100).
// 0 being the lowest priority and 100 being the highest.
// The less allocated resources the node has, the higher the score is.
func leastAllocatedScore(requested, capacity resource.Quantity) int64 {
	if capacity.CmpInt64(0) == 0 {
		return 0
	}
	if requested.Cmp(capacity) > 0 {
		return 0
	}
	numaValue := capacity.Value()
	requestedValue := requested.Value()
	return (numaValue - requestedValue) * framework.MaxNodeScore / capacity.Value()
}
