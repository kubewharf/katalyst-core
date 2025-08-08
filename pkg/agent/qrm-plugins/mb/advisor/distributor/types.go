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

package distributor

import "math"

type Distributor interface {
	Distribute(total int, weights map[int]int) map[int]int
}

func New(min, max int) Distributor {
	return &logarithmicBoundedDistributor{
		inner: &linearBoundedDistributor{
			min: min,
			max: max,
		},
	}
}

type linearBoundedDistributor struct {
	min int
	max int
}

func (l *linearBoundedDistributor) Distribute(total int, weights map[int]int) map[int]int {
	totalWeight := 0
	for _, weight := range weights {
		if weight <= 0 {
			continue
		}
		totalWeight += weight
	}

	results := make(map[int]int)
	for ccd, weight := range weights {
		portion := getPortion(total, weight, totalWeight)
		results[ccd] = l.adjust(portion)
	}

	return results
}

func (l *linearBoundedDistributor) adjust(value int) int {
	if l.max != 0 && value > l.max {
		return l.max
	}
	if l.min != 0 && value < l.min {
		return l.min
	}
	return value
}

func getPortion(total int, weight int, totalWeight int) int {
	if weight <= 0 || totalWeight <= 0 {
		return 0
	}

	return total * weight / totalWeight
}

type logarithmicBoundedDistributor struct {
	inner *linearBoundedDistributor
}

func (l logarithmicBoundedDistributor) Distribute(total int, weights map[int]int) map[int]int {
	logarithmicWeights := make(map[int]int)
	for k, v := range weights {
		if v <= 1 {
			logarithmicWeights[k] = 0
			continue
		}
		logarithmicWeights[k] = int(math.Log2(float64(v)))
	}
	return l.inner.Distribute(total, logarithmicWeights)
}
