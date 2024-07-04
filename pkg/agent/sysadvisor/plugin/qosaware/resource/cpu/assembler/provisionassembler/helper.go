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

func getNUMAsResource(resources map[int]int, numas machine.CPUSet) int {
	res := 0
	for _, numaID := range numas.ToSliceInt() {
		res += resources[numaID]
	}
	return res
}

func regulateOverlapReclaimPoolSize(sharePoolSizes map[string]int, overlapReclaimPoolSizeRequired int) (map[string]int, error) {
	sharePoolSum := general.SumUpMapValues(sharePoolSizes)
	if overlapReclaimPoolSizeRequired > sharePoolSum {
		return nil, fmt.Errorf("invalid sharedOverlapReclaimSize")
	}

	overlapReclaimPoolSizeRequiredLeft := overlapReclaimPoolSizeRequired
	sharedOverlapReclaimSize := make(map[string]int) // sharedPoolName -> reclaimedSize
	ps := general.SortedByValue(sharePoolSizes)
	for i := 0; i < len(ps); i++ {
		index := len(ps) - 1 - i
		sharePoolSize := ps[index].Value
		sharePoolName := ps[index].Key

		size := int(math.Ceil(float64(overlapReclaimPoolSizeRequired*sharePoolSize) / float64(sharePoolSum)))
		if size > sharePoolSize {
			size = sharePoolSize
		}
		if size > overlapReclaimPoolSizeRequiredLeft {
			size = overlapReclaimPoolSizeRequiredLeft
		}
		sharedOverlapReclaimSize[sharePoolName] = size
		overlapReclaimPoolSizeRequiredLeft -= size
		if overlapReclaimPoolSizeRequiredLeft == 0 {
			break
		}
	}

	return sharedOverlapReclaimSize, nil
}

// regulatePoolSizes modifies pool size map to legal values, taking total available
// resource and config such as enable reclaim into account. should be compatible with
// any case and not return error. return true if reach resource upper bound.
func regulatePoolSizes(expandableRequirements, unexpandableRequirements map[string]int, available int, enableReclaim bool, allowSharedCoresOverlapReclaimedCores bool) (map[string]int, bool) {
	expandableRequirementsSum := general.SumUpMapValues(expandableRequirements)
	unexpandableRequirementsSum := general.SumUpMapValues(unexpandableRequirements)

	requirementSum := expandableRequirementsSum + unexpandableRequirementsSum
	if requirementSum > available {
		requirements := general.MergeMapInt(expandableRequirements, unexpandableRequirements)
		poolSizes, err := normalizePoolSizes(requirements, available)
		if err != nil {
			// all pools share available resource as fallback if normalization failed
			for k := range requirements {
				poolSizes[k] = available
			}
		}
		return poolSizes, true
	} else if !enableReclaim || allowSharedCoresOverlapReclaimedCores {
		expandableRequirementsSum = available - unexpandableRequirementsSum
	}

	poolSizes, err := normalizePoolSizes(expandableRequirements, expandableRequirementsSum)
	if err != nil {
		for k := range expandableRequirements {
			poolSizes[k] = available
		}
	}
	for name, size := range unexpandableRequirements {
		poolSizes[name] = size
	}

	return poolSizes, false
}

func normalizePoolSizes(poolSizes map[string]int, targetSum int) (map[string]int, error) {
	sum := general.SumUpMapValues(poolSizes)
	if sum == targetSum {
		return general.DeepCopyIntMap(poolSizes), nil
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
			return poolSizesNormalized, fmt.Errorf("no enough resource")
		}
		poolSizesNormalized[poolName] -= 1
		normalizedSum -= 1
	}
	return poolSizesNormalized, nil
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
