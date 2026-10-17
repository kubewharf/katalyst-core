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

// disjointReclaimQuantizeDecision records how the final per-NUMA reclaim target was
// adjusted to a whole-core representable value before being published as a block.
type disjointReclaimQuantizeDecision string

const (
	disjointReclaimQuantizeAligned     disjointReclaimQuantizeDecision = "aligned"
	disjointReclaimQuantizeRoundedDown disjointReclaimQuantizeDecision = "rounded_down"
	disjointReclaimQuantizeRoundedUp   disjointReclaimQuantizeDecision = "rounded_up"
	disjointReclaimQuantizePassthrough disjointReclaimQuantizeDecision = "passthrough"
)

// quantizeDisjointReclaimTargetToWholeCore rewrites a per-NUMA dedicated/reclaim
// split whose reclaim target is not a whole-core multiple into the closest
// whole-core representable value. It is conservative by construction:
//
//   - SMT1 (cpusPerCore<=1) and already-aligned targets pass through unchanged.
//   - The rounded value must never drop below reservedForReclaim, the committed
//     reclaim anchor, so quantization never asks QRM to migrate a core out of the
//     already-committed reclaim pool.
//   - When both floor(R/w)*w and ceil(R/w)*w are legal it prefers the one that does
//     not move away from the committed reserve (no dedicated migration), then the
//     one closest to the original target.
//   - When no legal whole-core value exists it returns the original target unchanged
//     so QRM owns the reconcile rather than guessing.
func quantizeDisjointReclaimTargetToWholeCore(
	reclaimTarget, reservedForReclaim, cpusPerCore int,
) (int, disjointReclaimQuantizeDecision) {
	if cpusPerCore <= 1 || reclaimTarget%cpusPerCore == 0 {
		return reclaimTarget, disjointReclaimQuantizeAligned
	}

	lower := (reclaimTarget / cpusPerCore) * cpusPerCore
	upper := ((reclaimTarget + cpusPerCore - 1) / cpusPerCore) * cpusPerCore

	legal := make([]int, 0, 2)
	for _, t := range []int{lower, upper} {
		if t < reservedForReclaim {
			continue
		}
		legal = append(legal, t)
	}
	if len(legal) == 0 {
		return reclaimTarget, disjointReclaimQuantizePassthrough
	}

	best := legal[0]
	for _, t := range legal[1:] {
		best = pickWholeCoreReclaimAnchor(t, best, reservedForReclaim, reclaimTarget)
	}

	switch {
	case best < reclaimTarget:
		return best, disjointReclaimQuantizeRoundedDown
	case best > reclaimTarget:
		return best, disjointReclaimQuantizeRoundedUp
	default:
		return best, disjointReclaimQuantizeAligned
	}
}

// pickWholeCoreReclaimAnchor chooses between two legal whole-core reclaim values:
// prefer the one anchored closest to the committed reserve (no migration), then the
// one closest to the original advisor target.
func pickWholeCoreReclaimAnchor(a, b, reservedForReclaim, original int) int {
	aDist := absInt(a - reservedForReclaim)
	bDist := absInt(b - reservedForReclaim)
	if aDist != bDist {
		if aDist < bDist {
			return a
		}
		return b
	}
	aAdvisor := absInt(a - original)
	bAdvisor := absInt(b - original)
	if aAdvisor != bAdvisor {
		if aAdvisor < bAdvisor {
			return a
		}
		return b
	}
	if a < b {
		return a
	}
	return b
}
