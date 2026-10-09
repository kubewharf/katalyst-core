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

type numaCPUShare struct {
	numaID    int
	weight    int64
	limit     int64
	allocated int64
	remainder int64
}

// ApportionNUMACPU distributes total logical CPUs among NUMA nodes in
// physical-core quanta. The returned effective total is the sum of the final
// NUMA allocations and may be lower than total because of alignment or limits.
func ApportionNUMACPU(
	total int64,
	weights map[int]int64,
	limits map[int]int64,
	cpusPerCore int,
) (map[int]int64, int64, error) {
	if cpusPerCore <= 0 {
		return nil, 0, fmt.Errorf("cpus per core must be positive")
	}
	if total < 0 {
		return nil, 0, fmt.Errorf("total cpu must not be negative")
	}

	quantum := int64(cpusPerCore)
	shares, err := newNUMACPUShares(weights, limits, quantum)
	if err != nil {
		return nil, 0, err
	}

	target := total / quantum
	var limitSum int64
	for i := range shares {
		remainingTarget := target - limitSum
		if shares[i].limit >= remainingTarget {
			limitSum = target
			break
		}
		limitSum += shares[i].limit
	}
	if target > limitSum {
		target = limitSum
	}

	if err := apportionPhysicalCores(shares, target); err != nil {
		return nil, 0, err
	}

	allocations := make(map[int]int64, len(shares))
	var effective int64
	for _, share := range shares {
		value := share.allocated * quantum
		allocations[share.numaID] = value
		effective += value
	}
	return allocations, effective, nil
}

func newNUMACPUShares(
	weights map[int]int64,
	limits map[int]int64,
	quantum int64,
) ([]numaCPUShare, error) {
	if len(weights) != len(limits) {
		return nil, fmt.Errorf("numa weight and limit keys must match")
	}

	numaIDs := make([]int, 0, len(weights))
	for numaID := range weights {
		if _, ok := limits[numaID]; !ok {
			return nil, fmt.Errorf("numa weight and limit keys must match")
		}
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)

	shares := make([]numaCPUShare, 0, len(numaIDs))
	for _, numaID := range numaIDs {
		weight := weights[numaID]
		limit := limits[numaID]
		if weight < 0 {
			return nil, fmt.Errorf("numa weight must not be negative")
		}
		if limit < 0 {
			return nil, fmt.Errorf("numa limit must not be negative")
		}

		alignedLimit := limit / quantum
		if alignedLimit > 0 && weight == 0 {
			return nil, fmt.Errorf("numa weight must be positive when aligned limit is positive")
		}
		shares = append(shares, numaCPUShare{
			numaID: numaID,
			weight: weight,
			limit:  alignedLimit,
		})
	}
	return shares, nil
}

func apportionPhysicalCores(shares []numaCPUShare, target int64) error {
	remaining := target
	for remaining > 0 {
		active := make([]int, 0, len(shares))
		var totalWeight int64
		for i := range shares {
			if shares[i].allocated >= shares[i].limit {
				continue
			}
			if shares[i].weight > math.MaxInt64-totalWeight {
				return fmt.Errorf("cpu apportionment overflow")
			}
			totalWeight += shares[i].weight
			active = append(active, i)
		}
		if len(active) == 0 || totalWeight == 0 {
			return fmt.Errorf("cpu apportionment made no progress")
		}

		progress := false
		floorCapped := false
		roundTarget := remaining
		for _, i := range active {
			product, ok := checkedPositiveMultiply(roundTarget, shares[i].weight)
			if !ok {
				return fmt.Errorf("cpu apportionment overflow")
			}

			floorShare := product / totalWeight
			shares[i].remainder = product % totalWeight
			capacity := shares[i].limit - shares[i].allocated
			if floorShare > capacity {
				floorShare = capacity
			}
			if floorShare > 0 {
				shares[i].allocated += floorShare
				remaining -= floorShare
				progress = true
				if shares[i].allocated >= shares[i].limit {
					floorCapped = true
				}
			}
		}

		if floorCapped {
			continue
		}

		sort.Slice(active, func(i, j int) bool {
			left, right := &shares[active[i]], &shares[active[j]]
			if left.remainder != right.remainder {
				return left.remainder > right.remainder
			}
			return left.numaID < right.numaID
		})
		for _, i := range active {
			if remaining == 0 {
				break
			}
			if shares[i].allocated >= shares[i].limit {
				continue
			}
			shares[i].allocated++
			remaining--
			progress = true
		}

		if !progress {
			return fmt.Errorf("cpu apportionment made no progress")
		}
	}
	return nil
}

func checkedPositiveMultiply(left, right int64) (int64, bool) {
	if left != 0 && right > math.MaxInt64/left {
		return 0, false
	}
	return left * right, true
}
