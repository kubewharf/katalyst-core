/*
Copyright 2026 The Katalyst Authors.

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
	"fmt"
	"math"
	"sort"
)

// CalculatePerNUMAHardReclaimTarget derives a stable reclaim target from the
// immutable CPU capacity of one NUMA node. The target is always a whole-core
// multiple: the ratio is applied on the core count (not the raw CPU count) and
// aligned down to a complete number of donated cores, and every floor
// (minimumCores and configuredReserve) is expressed in / rounded up to complete
// cores using cpusPerCore. This guarantees the reclaim quantity can be satisfied
// by complete physical cores, so a NUMA reclaim pool never has to strand a half
// core.
//
// minimumCores is a hard floor in complete cores; configuredReserve is a raw CPU
// count that is rounded UP to a complete core before it can lift the target.
func CalculatePerNUMAHardReclaimTarget(capacity int, ratio float64, minimumCores, configuredReserve, cpusPerCore int) (int, error) {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
		return 0, fmt.Errorf("ratio must be within [0,1], got %v", ratio)
	}
	if cpusPerCore <= 0 {
		return 0, fmt.Errorf("cpus per core must be positive, got %d", cpusPerCore)
	}

	totalCores := capacity / cpusPerCore
	coreAlignedCapacity := totalCores * cpusPerCore

	target := coreAlignedCapacity
	if ratio < 1 {
		cores := int(math.Floor(float64(totalCores) * ratio))
		target = cores * cpusPerCore
	}

	floorCores := maxInt(minimumCores, ceilDiv(configuredReserve, cpusPerCore))
	if floor := floorCores * cpusPerCore; target < floor {
		target = floor
	}
	if target > capacity {
		return 0, fmt.Errorf("hard reclaim target %d exceeds NUMA capacity %d", target, capacity)
	}
	return target, nil
}

// ceilDiv returns the smallest integer >= a/b for positive b, i.e. a rounded up
// to the next multiple of b divided by b. b must be positive.
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// roundUpToCoreAligned rounds a raw CPU count UP to the next complete physical
// core, i.e. the smallest multiple of cpusPerCore that is >= cpus. It is the one
// shared primitive every reserve floor uses so a non-core-aligned reserve can
// never seed a half core. cpusPerCore <= 0 is treated as a no-op (returns cpus).
func roundUpToCoreAligned(cpus, cpusPerCore int) int {
	if cpusPerCore <= 0 {
		return cpus
	}
	return ceilDiv(cpus, cpusPerCore) * cpusPerCore
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// DistributeConfiguredHardReclaimFloor raises per-NUMA baseline targets until
// the configured global floor is met, without exceeding any NUMA capacity. The
// lift is applied one complete physical core (cpusPerCore CPUs) at a time so a
// balanced result never leaves a half core; a NUMA is skipped once its next
// complete core would exceed its core-aligned capacity. When the floor is not a
// whole-core multiple it is effectively rounded UP to the next complete core.
func DistributeConfiguredHardReclaimFloor(
	capacityByNUMA, baselineByNUMA map[int]int,
	configuredFloor int,
	cpusPerCore int,
) (map[int]int, error) {
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf("cpus per core must be positive, got %d", cpusPerCore)
	}

	numaIDs := make([]int, 0, len(capacityByNUMA))
	targets := make(map[int]int, len(capacityByNUMA))
	coreAlignedCapacity := make(map[int]int, len(capacityByNUMA))
	totalTarget, totalCoreAlignedCapacity := 0, 0
	for numaID, capacity := range capacityByNUMA {
		if capacity < 0 {
			return nil, fmt.Errorf("NUMA %d has negative capacity %d", numaID, capacity)
		}
		baseline := baselineByNUMA[numaID]
		if baseline < 0 || baseline > capacity {
			return nil, fmt.Errorf("NUMA %d hard reclaim baseline %d exceeds capacity %d", numaID, baseline, capacity)
		}
		aligned := (capacity / cpusPerCore) * cpusPerCore
		numaIDs = append(numaIDs, numaID)
		targets[numaID] = baseline
		coreAlignedCapacity[numaID] = aligned
		totalTarget += baseline
		totalCoreAlignedCapacity += aligned
	}
	sort.Ints(numaIDs)
	required := configuredFloor
	if required < totalTarget {
		required = totalTarget
	}
	if required > totalCoreAlignedCapacity {
		return nil, fmt.Errorf("configured hard reclaim floor %d exceeds total core-aligned NUMA capacity %d", required, totalCoreAlignedCapacity)
	}
	// water-fill complete cores round-robin; stop when the floor is met or a full
	// pass makes no progress (every NUMA is at its core-aligned capacity).
	for remaining := required - totalTarget; remaining > 0; {
		progressed := false
		for _, numaID := range numaIDs {
			if remaining <= 0 {
				break
			}
			if targets[numaID]+cpusPerCore > coreAlignedCapacity[numaID] {
				continue
			}
			targets[numaID] += cpusPerCore
			remaining -= cpusPerCore
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return targets, nil
}
