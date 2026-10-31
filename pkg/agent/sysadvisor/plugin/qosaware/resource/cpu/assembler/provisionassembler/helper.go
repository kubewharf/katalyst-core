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
	"sort"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
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

// allocatePoolSizesByPriority allocates physical capacity without mixing
// guarantees from different classes in one proportional normalization. The
// order is dedicated requirement, isolation lower bound, shared minimum, then
// all optional expansion.
func allocatePoolSizesByPriority(
	available int,
	dedicatedRequirements,
	isolationLowerSizes,
	shareMinimums,
	expansionTargets map[string]int,
) (map[string]int, bool) {
	result := make(map[string]int)
	remaining := general.Max(available, 0)
	for _, tier := range []map[string]int{
		dedicatedRequirements,
		isolationLowerSizes,
		shareMinimums,
		expansionTargets,
	} {
		increments := make(map[string]int)
		for name, target := range tier {
			if delta := general.Max(target, 0) - result[name]; delta > 0 {
				increments[name] = delta
			}
		}
		allocated := allocateProportionally(increments, remaining)
		for name, size := range allocated {
			result[name] += size
			remaining -= size
		}
		if remaining == 0 {
			break
		}
	}

	throttled := false
	for name, target := range expansionTargets {
		if result[name] < general.Max(target, 0) {
			throttled = true
			break
		}
	}
	return result, throttled
}

func allocatePoolSizesByWorkloadPriority(
	available, dedicatedAvailable, sharedAvailable int,
	dedicatedRequirements,
	isolationLowerSizes,
	shareMinimums,
	expansionTargets map[string]int,
) (map[string]int, bool) {
	result := make(map[string]int)
	remaining := general.Max(available, 0)

	allocateTier := func(targets map[string]int, limit int) {
		increments := make(map[string]int)
		for name, target := range targets {
			if delta := general.Max(target, 0) - result[name]; delta > 0 {
				increments[name] = delta
			}
		}
		budget := general.Min(remaining, general.Max(limit, 0))
		allocated := allocateProportionally(increments, budget)
		for name, size := range allocated {
			result[name] += size
			remaining -= size
		}
	}

	allocateTier(dedicatedRequirements, dedicatedAvailable)
	allocateTier(isolationLowerSizes, remaining)
	allocateTier(shareMinimums, sharedAvailable)

	dedicatedExpansion := make(map[string]int)
	isolationExpansion := make(map[string]int)
	sharedExpansion := make(map[string]int)
	for name, target := range expansionTargets {
		switch {
		case dedicatedRequirements[name] > 0:
			dedicatedExpansion[name] = target
		case isolationLowerSizes[name] > 0:
			isolationExpansion[name] = target
		default:
			sharedExpansion[name] = target
		}
	}
	allocateTier(dedicatedExpansion,
		general.Max(dedicatedAvailable-general.SumUpMapValues(filterPoolSizes(result, dedicatedRequirements)), 0))
	allocateTier(isolationExpansion, remaining)
	allocateTier(sharedExpansion,
		general.Max(sharedAvailable-general.SumUpMapValues(filterPoolSizes(result, shareMinimums)), 0))

	throttled := false
	for name, target := range expansionTargets {
		if result[name] < general.Max(target, 0) {
			throttled = true
			break
		}
	}
	return result, throttled
}

func filterPoolSizes(poolSizes, names map[string]int) map[string]int {
	result := make(map[string]int)
	for name := range names {
		result[name] = poolSizes[name]
	}
	return result
}

func expandSharePoolsToCapacity(poolSizes, shareWeights map[string]int, available int) {
	remaining := general.Max(available-general.SumUpMapValues(poolSizes), 0)
	if remaining == 0 || len(shareWeights) == 0 {
		return
	}

	shareTarget := remaining
	active := general.DeepCopyIntMap(shareWeights)
	for name := range shareWeights {
		shareTarget += poolSizes[name]
	}
	fixed := make(map[string]int)
	for len(active) > 0 {
		fixedSum := general.SumUpMapValues(fixed)
		desired := allocateByWeight(active, general.Max(shareTarget-fixedSum, 0))
		changed := false
		for name := range active {
			if desired[name] < poolSizes[name] {
				fixed[name] = poolSizes[name]
				delete(active, name)
				changed = true
			}
		}
		if changed {
			continue
		}
		for name, size := range desired {
			poolSizes[name] = size
		}
		break
	}
}

func allocateProportionally(requirements map[string]int, available int) map[string]int {
	result := make(map[string]int)
	total := general.SumUpMapValues(requirements)
	if available <= 0 || total <= 0 {
		return result
	}
	if total <= available {
		return general.DeepCopyIntMap(requirements)
	}
	return allocateByWeight(requirements, available)
}

func allocateByWeight(weights map[string]int, amount int) map[string]int {
	result := make(map[string]int)
	total := general.SumUpMapValues(weights)
	if amount <= 0 || total <= 0 {
		return result
	}
	type fractionalRemainder struct {
		name      string
		remainder int
	}
	remainders := make([]fractionalRemainder, 0, len(weights))
	allocated := 0
	for name, weight := range weights {
		if weight <= 0 {
			continue
		}
		numerator := weight * amount
		result[name] = numerator / total
		allocated += result[name]
		remainders = append(remainders, fractionalRemainder{name: name, remainder: numerator % total})
	}
	sort.Slice(remainders, func(i, j int) bool {
		if remainders[i].remainder != remainders[j].remainder {
			return remainders[i].remainder > remainders[j].remainder
		}
		return remainders[i].name < remainders[j].name
	})
	for i := 0; allocated < amount && i < len(remainders); i++ {
		result[remainders[i].name]++
		allocated++
	}
	return result
}

type RegionMapHelper struct {
	regions map[int]map[configapi.QoSRegionType][]region.QoSRegion
}

func NewRegionMapHelper(regions map[string]region.QoSRegion) *RegionMapHelper {
	helper := &RegionMapHelper{
		regions: map[int]map[configapi.QoSRegionType][]region.QoSRegion{},
	}

	helper.preProcessRegions(regions)

	return helper
}

func (rm *RegionMapHelper) GetRegions(numaID int, regionType configapi.QoSRegionType) []region.QoSRegion {
	numaRecords, ok := rm.regions[numaID]
	if !ok {
		return nil
	}

	return numaRecords[regionType]
}

func (rm *RegionMapHelper) preProcessRegions(regions map[string]region.QoSRegion) {
	for _, r := range regions {
		if r.IsNumaBinding() {
			for _, numaID := range r.GetBindingNumas().ToSliceInt() {
				numaRecords, ok := rm.regions[numaID]
				if !ok {
					numaRecords = map[configapi.QoSRegionType][]region.QoSRegion{}
				}
				numaRegions := numaRecords[r.Type()]
				numaRegions = append(numaRegions, r)
				numaRecords[r.Type()] = numaRegions
				rm.regions[numaID] = numaRecords
			}
		} else {
			numaRecords, ok := rm.regions[commonstate.FakedNUMAID]
			if !ok {
				numaRecords = map[configapi.QoSRegionType][]region.QoSRegion{}
			}
			numaRegions := numaRecords[r.Type()]
			numaRegions = append(numaRegions, r)
			numaRecords[r.Type()] = numaRegions
			rm.regions[commonstate.FakedNUMAID] = numaRecords
		}
	}
}
