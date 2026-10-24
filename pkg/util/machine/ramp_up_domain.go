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

// RampUpDomainDescriptor is the internal, name-neutral view of one ramp-up
// reclaim domain. It aggregates the member NUMA logical capacities first, so
// the whole-core rounding that derives the target happens on the aggregate
// rather than per NUMA (per-NUMA rounding then summing strands cores and
// under-provisions the domain).
//
// The struct intentionally carries only the four quantities the sysadvisor
// assembler consumes. Bookkeeping fields that were computed but never read
// (the domain kind, aggregate capacity, SMT width, and per-NUMA distribution)
// have been dropped so this surface is the consumed contract rather than an
// internal scratchpad.
type RampUpDomainDescriptor struct {
	// MemberNUMAs are the NUMA ids the domain spans, sorted for deterministic
	// consumption.
	MemberNUMAs []int
	// DesiredTarget is the whole-core-floor ramp-up reclaim size for the whole
	// domain, derived from the aggregate capacity * InitialRampUpReclaimCPUSetRatio.
	// It is the ramp-up midpoint the per-scope ceiling tracks; it is NOT the
	// enforced pool size (the per-scope rate-limited ceiling is) and it must
	// never lift the reservation floor.
	DesiredTarget int
	// SteadyCap is the reclaim pool upper bound the domain returns to once
	// ramp-up exits. MaxRatio only caps the pool; it never raises the floor.
	SteadyCap int
	// ReserveFloor is the pure lower bound for the domain: the sum of the member
	// NUMA steady reserves (NumaMinReclaimedResourceForAllocate). It is never
	// raised by DesiredTarget.
	ReserveFloor int
}

// CalculateAggregateRampUpTarget derives the whole-core ramp-up reclaim target
// for one partition domain from its AGGREGATE logical capacity. The ratio is
// applied to the core count of the aggregate capacity and rounded down to a
// complete number of physical cores, so a domain spanning several NUMAs is
// never under-provisioned by rounding each NUMA first and summing afterwards.
//
// Boundary handling:
//   - ratio <= 0            -> target 0 (no ramp-up reclaim reservation);
//   - ratio >= 1           -> the core-aligned aggregate capacity (never a
//     stray half core on an odd-capacity aggregate);
//   - NaN / +/-Inf / <0    -> error, treated as a configuration fault by the
//     caller which fails closed;
//   - cpusPerCore <= 0     -> error;
//   - the returned target is always <= the core-aligned aggregate capacity.
//
// Global and per-NUMA partition domains share this single algorithm so their
// rounding cannot drift apart.
func CalculateAggregateRampUpTarget(aggregateCapacity int, ratio float64, cpusPerCore int) (int, error) {
	if cpusPerCore <= 0 {
		return 0, fmt.Errorf("cpus per core must be positive, got %d", cpusPerCore)
	}
	if aggregateCapacity < 0 {
		return 0, fmt.Errorf("aggregate capacity must be non-negative, got %d", aggregateCapacity)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
		return 0, fmt.Errorf("ratio must be within [0,1], got %v", ratio)
	}
	if ratio <= 0 {
		return 0, nil
	}

	totalCores := aggregateCapacity / cpusPerCore
	coreAlignedCapacity := totalCores * cpusPerCore
	if ratio >= 1 {
		return coreAlignedCapacity, nil
	}
	cores := int(math.Floor(float64(totalCores) * ratio))
	target := cores * cpusPerCore
	if target > coreAlignedCapacity {
		target = coreAlignedCapacity
	}
	return target, nil
}

// DistributeDomainTarget splits a domain's aggregate DesiredTarget back across
// its member NUMAs as complete physical cores, so the sum of the per-NUMA
// shares is exactly the target, every share is core-aligned, and no NUMA
// exceeds its core-aligned capacity. Per-NUMA baselines (the steady reserved
// minimum each NUMA keeps) seed the distribution and are lifted up together;
// the remainder is water-filled round-robin over NUMA ids in ascending order,
// which makes the split deterministic across cycles.
//
// capacityByNUMA and baselineByNUMA must cover the same member set; baselines
// are rounded up to a complete core first so a half-core reserve can never seed
// a half-core share. A target that cannot be met within core-aligned capacity
// is an error (fail closed rather than over-provisioning a member NUMA).
func DistributeDomainTarget(
	target int,
	capacityByNUMA map[int]int,
	baselineByNUMA map[int]int,
	cpusPerCore int,
) (map[int]int, error) {
	if cpusPerCore <= 0 {
		return nil, fmt.Errorf("cpus per core must be positive, got %d", cpusPerCore)
	}
	if target < 0 {
		return nil, fmt.Errorf("domain target must be non-negative, got %d", target)
	}

	numaIDs := make([]int, 0, len(capacityByNUMA))
	for numaID := range capacityByNUMA {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)

	alignedBaseline := make(map[int]int, len(numaIDs))
	for _, numaID := range numaIDs {
		baseline := baselineByNUMA[numaID]
		if baseline < 0 {
			return nil, fmt.Errorf("NUMA %d negative baseline %d", numaID, baseline)
		}
		alignedBaseline[numaID] = roundUpToCoreAligned(baseline, cpusPerCore)
	}

	return DistributeConfiguredHardReclaimFloor(capacityByNUMA, alignedBaseline, target, cpusPerCore)
}
