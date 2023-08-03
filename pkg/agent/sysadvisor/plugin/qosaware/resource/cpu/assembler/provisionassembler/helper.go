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

// regulatePoolSizes modifies pool size map to legal values, taking total available
// resource and config such as enable reclaim into account. should be compatible with
// any case and not return error. return true if reach resource upper bound.
func regulatePoolSizes(poolSizes map[string]int, available int, enableReclaim bool) bool {
	targetSum := general.SumUpMapValues(poolSizes)
	boundUpper := false

	// use all available resource for pools when reclaim is disabled
	if !enableReclaim || targetSum > available {
		targetSum = available
	}

	// use all available resource when reaching resource upper bound
	if targetSum > available {
		targetSum = available
		boundUpper = true
	}

	if err := normalizePoolSizes(poolSizes, targetSum); err != nil {
		// all pools share available resource as fallback if normalization failed
		for k := range poolSizes {
			poolSizes[k] = available
		}
	}

	return boundUpper
}

func normalizePoolSizes(poolSizes map[string]int, targetSum int) error {
	sum := general.SumUpMapValues(poolSizes)
	if sum == targetSum {
		return nil
	}

	poolSizesNormalized := make(map[string]int)
	normalizedSum := 0

	for k, v := range poolSizes {
		value := int(math.Ceil(float64(v*targetSum) / float64(sum)))
		poolSizesNormalized[k] = value
		normalizedSum += value
	}

	for {
		if normalizedSum <= targetSum {
			break
		}
		poolName := selectPoolHelper(poolSizes, poolSizesNormalized)
		if poolName == "" {
			return fmt.Errorf("no enough resource")
		}
		poolSizesNormalized[poolName] -= 1
		normalizedSum -= 1
	}

	for k, v := range poolSizesNormalized {
		poolSizes[k] = v
	}
	return nil
}

func selectPoolHelper(poolSizesOriginal, poolSizesNormalized map[string]int) string {
	candidates := []string{}
	rMax := 0.0
	for k, v := range poolSizesNormalized {
		if v <= 1 {
			continue
		}
		r := float64(v) / float64(poolSizesOriginal[k])
		if r > rMax {
			candidates = []string{k}
			rMax = r
		} else if r == rMax {
			candidates = append(candidates, k)
		}
	}

	if len(candidates) <= 0 {
		return ""
	} else if len(candidates) == 1 {
		return candidates[0]
	}

	selected := ""
	vMax := 0
	for _, pool := range candidates {
		if v := poolSizesNormalized[pool]; v > vMax {
			selected = pool
			vMax = v
		}
	}
	return selected
}
