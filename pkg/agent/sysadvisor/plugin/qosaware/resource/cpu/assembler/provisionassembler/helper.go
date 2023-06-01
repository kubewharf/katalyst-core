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
	"math"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func getNumasAvailableResource(numaAvailable map[int]int, numas machine.CPUSet) int {
	res := 0
	for _, numaID := range numas.ToSliceInt() {
		res += numaAvailable[numaID]
	}
	return res
}

// regulatePoolSizes modifies pool size map to legal values, taking total available resource
// and enable reclaim config into account. should hold any cases and not return error.
func regulatePoolSizes(poolSizes map[string]int, available int, enableReclaim bool) {
	sum := general.SumUpMapValues(poolSizes)
	targetSum := sum

	if !enableReclaim || sum > available {
		// use up all available resource for pools in this case
		targetSum = available
	}

	if err := normalizePoolSizes(poolSizes, targetSum); err != nil {
		// all pools share available resource as fallback if normalization failed
		for k := range poolSizes {
			poolSizes[k] = available
		}
	}
}

func normalizePoolSizes(poolSizes map[string]int, targetSum int) error {
	sum := general.SumUpMapValues(poolSizes)
	if sum == targetSum {
		return nil
	}

	sorted := general.TraverseMapByValueDescending(poolSizes)
	normalizedSum := 0

	for _, v := range sorted {
		v.Value = int(math.Ceil(float64(v.Value*targetSum) / float64(sum)))
		normalizedSum += v.Value
	}

	for i := 0; i < normalizedSum-targetSum; i++ {
		if sorted[i].Value <= 1 {
			return fmt.Errorf("no enough resource")
		}
		sorted[i].Value -= 1
	}

	for _, v := range sorted {
		poolSizes[v.Key] = v.Value
	}
	return nil
}
