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

package util

import (
	"fmt"
	"math"
)

func CalculateRampUpReclaimTarget(eligible, reserve, cap int, ratio float64, exclusive bool) (int, error) {
	if eligible <= 0 {
		return 0, fmt.Errorf("eligible CPU count must be positive, got %d", eligible)
	}
	if reserve < 0 {
		return 0, fmt.Errorf("reserve must be non-negative, got %d", reserve)
	}
	if cap < 0 {
		return 0, fmt.Errorf("cap must be non-negative, got %d", cap)
	}
	if ratio < 0 || ratio > 1 {
		return 0, fmt.Errorf("initial ramp-up reclaim ratio must be in [0,1], got %f", ratio)
	}

	target := reserve
	if ratio > 0 {
		ratioTarget := int(math.Floor(ratio * float64(eligible)))
		ratioTarget -= ratioTarget % 2
		target = int(math.Max(float64(target), float64(ratioTarget)))
	}
	if target <= 0 {
		return 0, fmt.Errorf("bootstrap target must be positive, got %d (reserve=%d, ratio=%f, eligible=%d)", target, reserve, ratio, eligible)
	}

	// Do not clamp bootstrap to cap: a clamp would make QRM and SysAdvisor
	// disagree about the hard partition that bulkhead must converge to.
	if target > cap {
		return 0, fmt.Errorf("bootstrap target exceeds reclaim cap: target %d, cap %d", target, cap)
	}
	if exclusive && target >= eligible {
		return 0, fmt.Errorf("exclusive ramp-up requires non-empty dedicated remainder: target %d, eligible %d", target, eligible)
	}
	return target, nil
}
