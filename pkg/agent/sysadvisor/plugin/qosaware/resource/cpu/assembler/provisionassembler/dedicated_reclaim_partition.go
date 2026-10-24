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

package provisionassembler

import (
	"fmt"
	"math"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type exclusivePartitionInput struct {
	PartitionCapacity int
	DedicatedCapacity int
	ReclaimCapacity   int
	Reserved          int
	NonReclaimed      int
	EnableReclaim     bool
	RatioPhysicalCap  int
}

func calculateExclusiveDisjointTargets(
	in exclusivePartitionInput,
) (dedicated, reclaim int, err error) {
	if in.PartitionCapacity < 0 || in.DedicatedCapacity < 0 || in.ReclaimCapacity < 0 {
		return 0, 0, fmt.Errorf("exclusive partition capacities must be non-negative")
	}

	capacity := general.Max(in.PartitionCapacity, 0)
	dedicatedCapacity := general.Max(in.DedicatedCapacity, 0)
	reclaimCapacity := general.Max(in.ReclaimCapacity, 0)
	reserved := general.Min(general.Max(in.Reserved, 0), reclaimCapacity)
	nonReclaimed := general.Min(general.Max(in.NonReclaimed, 0), dedicatedCapacity)

	candidate := reserved
	if in.EnableReclaim {
		candidate = general.Max(reserved, capacity-nonReclaimed)
	}
	if in.RatioPhysicalCap > 0 {
		candidate = general.Min(candidate, in.RatioPhysicalCap)
	}

	lower := general.Max(reserved, capacity-dedicatedCapacity)
	upper := general.Min(reclaimCapacity, capacity-1)
	if candidate < lower || candidate > upper {
		return 0, 0, fmt.Errorf(
			"exclusive partition target %d outside reclaim bounds [%d,%d]",
			candidate,
			lower,
			upper,
		)
	}

	return capacity - candidate, candidate, nil
}

func calculateReclaimQuotaLimit(
	physicalTarget int,
	quotaKnob float64,
	ratioCap int,
) float64 {
	if math.IsNaN(quotaKnob) || quotaKnob < 0 {
		return -1
	}

	limit := general.MinFloat64(quotaKnob, float64(general.Max(physicalTarget, 0)))
	if ratioCap > 0 {
		limit = general.MinFloat64(limit, float64(ratioCap))
	}
	return limit
}

func desiredDedicatedPhysical(
	rawRequest, rawRequirement int,
	enableReclaim, disableOverlap bool,
) int {
	request := general.Max(rawRequest, 0)
	if !disableOverlap || !enableReclaim {
		return request
	}
	return general.Min(request, general.Max(rawRequirement, 0))
}
