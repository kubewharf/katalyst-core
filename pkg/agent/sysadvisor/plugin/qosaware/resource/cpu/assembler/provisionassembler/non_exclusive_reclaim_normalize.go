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

// nonExclusiveJointDecision records how the non-exclusive reclaim target was
// jointly aligned to a whole-core boundary before publication.
type nonExclusiveJointDecision string

const (
	// nonExclusiveJointAligned means the reclaim target was already whole-core and
	// no adjustment was required.
	nonExclusiveJointAligned nonExclusiveJointDecision = "aligned"
	// nonExclusiveJointRoundedDown means reclaim shrank to a lower whole-core value
	// and the released CPUs were returned to the single dedicated pool.
	nonExclusiveJointRoundedDown nonExclusiveJointDecision = "rounded_down"
	// nonExclusiveJointRoundedUp means reclaim grew to a higher whole-core value and
	// the extra CPUs were taken from the single dedicated pool's headroom.
	nonExclusiveJointRoundedUp nonExclusiveJointDecision = "rounded_up"
	// nonExclusiveJointPassthrough means no safe joint whole-core value exists for
	// this NUMA (ambiguous multi-source, missing anchors, or a candidate that would
	// break a dedicated minimum / ceiling), so the original target is published and
	// QRM owns whole-core materialization.
	nonExclusiveJointPassthrough nonExclusiveJointDecision = "passthrough"
)

// nonExclusiveJointInput carries the final, already-clamped numbers for one real
// NUMA from the non-exclusive assembler path. share and isolation pools are not
// part of the conservation domain: they are workload pools and must not be moved.
type nonExclusiveJointInput struct {
	// CPUsPerCore is the physical SMT width of the machine; w<=1 means no whole-core
	// alignment is expressible and the caller should skip normalization.
	CPUsPerCore int
	// ReclaimTarget is the final non-overlap reclaim size after ratio/ramp-up/constraint.
	ReclaimTarget int
	// ReservedForReclaim is the committed reclaim anchor: the adjusted reclaim must
	// never shrink below it.
	ReservedForReclaim int
	// DedicatedPoolSizes maps pool name -> final dedicated pool size (the controlled
	// partition's dedicated side).
	DedicatedPoolSizes map[string]int
	// DedicatedMinimums maps pool name -> minimum dedicated size (requirements floor).
	DedicatedMinimums map[string]int
	// DedicatedCeilings maps pool name -> eligibility-domain ceiling (post-reserve
	// available capacity of that pool's pinned package, or the unpinned domain). It is
	// per-pool rather than NUMA-wide because a dedicated pool pinned to a resource
	// package may not grow into another package's CPUs.
	DedicatedCeilings map[string]int
}

// nonExclusiveJointAdjustment is the result of joint normalization. DedicatedSizes
// is populated only when the reclaim target changed and exactly one dedicated pool
// can absorb/release the delta unambiguously.
type nonExclusiveJointAdjustment struct {
	ReclaimSize    int
	DedicatedSizes map[string]int
	Changed        bool
	Decision       nonExclusiveJointDecision
}

// jointlyNormalizeNonExclusiveTargets aligns the non-overlap reclaim target to a
// whole-core boundary while keeping the controlled NUMA partition (non-overlap
// reclaim + dedicated pools) size-conserved. share and isolation pools are never
// touched.
//
// Candidates are lower=floor(R/w)*w and upper=ceil(R/w)*w. For each candidate c the
// delta = R - c is applied to the dedicated side: a smaller reclaim returns the
// released CPUs to dedicated, a larger reclaim takes them from dedicated headroom.
// A candidate is legal only when reclaim stays at/above the committed anchor, the
// dedicated pool stays at/above its minimum, and (when growing) within its ceiling.
//
// Multi-source safety: the delta can only be assigned when exactly one dedicated
// pool exists, because with multiple pools there is no per-pool provenance here to
// decide which pool owns the physical core. In that case the input is passed through
// unchanged and QRM's steady normalization owns materialization.
func jointlyNormalizeNonExclusiveTargets(in nonExclusiveJointInput) nonExclusiveJointAdjustment {
	w := in.CPUsPerCore
	// The no-adjustment result aliases the caller's DedicatedPoolSizes: in every
	// path that returns this base the caller only consumes Decision/ReclaimSize
	// and never reads or mutates DedicatedSizes (the adjusted result below builds
	// a fresh map). A defensive copy would therefore be dead weight.
	base := nonExclusiveJointAdjustment{
		ReclaimSize:    in.ReclaimTarget,
		DedicatedSizes: in.DedicatedPoolSizes,
		Decision:       nonExclusiveJointAligned,
	}
	if w <= 1 {
		return base
	}
	if in.ReclaimTarget%w == 0 {
		return base
	}

	// A single dedicated pool is required for unambiguous delta assignment.
	if len(in.DedicatedPoolSizes) != 1 {
		base.Decision = nonExclusiveJointPassthrough
		return base
	}

	var poolName string
	for name := range in.DedicatedPoolSizes {
		poolName = name
	}
	dedicatedSize := in.DedicatedPoolSizes[poolName]
	dedicatedMin := in.DedicatedMinimums[poolName]
	dedicatedCeiling, knownCeiling := in.DedicatedCeilings[poolName]
	if !knownCeiling {
		// No eligibility-domain ceiling recorded for this pool: do not guess it.
		base.Decision = nonExclusiveJointPassthrough
		return base
	}

	lower := (in.ReclaimTarget / w) * w
	upper := lower + w
	candidates := []int{lower, upper}

	type cand struct {
		reclaim   int
		dedicated int
		legal     bool
	}
	evaluated := make([]cand, 0, len(candidates))
	for _, c := range candidates {
		delta := in.ReclaimTarget - c // reclaim change; dedicated moves by -delta
		newDedicated := dedicatedSize + delta
		legal := true
		// Reclaim floor: never below the committed anchor.
		if c < in.ReservedForReclaim {
			legal = false
		}
		if c < 0 {
			legal = false
		}
		// Dedicated floor: never below its requirement minimum.
		if newDedicated < dedicatedMin {
			legal = false
		}
		// Dedicated ceiling: never above its eligibility-domain available capacity.
		if newDedicated > dedicatedCeiling {
			legal = false
		}
		evaluated = append(evaluated, cand{reclaim: c, dedicated: newDedicated, legal: legal})
	}

	best := -1
	bestCommitted, bestAdvisor, bestReclaim := 0, 0, 0
	for i, ev := range evaluated {
		if !ev.legal {
			continue
		}
		// Score: distance to committed anchor first (prefer anchoring on reserved
		// reclaim), then distance to the advisor target, then smaller reclaim. Stable
		// pool ordering is irrelevant here (single pool).
		committedDist := absInt(ev.reclaim - in.ReservedForReclaim)
		advisorDist := absInt(ev.reclaim - in.ReclaimTarget)
		reclaim := ev.reclaim
		if best == -1 ||
			committedDist < bestCommitted ||
			(committedDist == bestCommitted && advisorDist < bestAdvisor) ||
			(committedDist == bestCommitted && advisorDist == bestAdvisor && reclaim < bestReclaim) {
			best = i
			bestCommitted, bestAdvisor, bestReclaim = committedDist, advisorDist, reclaim
		}
	}

	if best == -1 {
		base.Decision = nonExclusiveJointPassthrough
		return base
	}

	chosen := evaluated[best]
	adj := nonExclusiveJointAdjustment{
		ReclaimSize: chosen.reclaim,
		DedicatedSizes: map[string]int{
			poolName: chosen.dedicated,
		},
		Changed: true,
	}
	switch {
	case chosen.reclaim < in.ReclaimTarget:
		adj.Decision = nonExclusiveJointRoundedDown
	case chosen.reclaim > in.ReclaimTarget:
		adj.Decision = nonExclusiveJointRoundedUp
	default:
		adj.Decision = nonExclusiveJointAligned
		adj.Changed = false
	}
	return adj
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
