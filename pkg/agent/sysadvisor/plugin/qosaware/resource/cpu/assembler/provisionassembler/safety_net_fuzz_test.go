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
	"testing"
)

// Fuzz smoke tests. They are run as a seed-corpus smoke under
// `go test -run=Fuzz...` (no fuzzer) and can be extended with
// `-fuzz=<name> -fuzztime=...` when hunting for edge cases.
//
// The invariants asserted here are the contracts a refactor of the
// reclaim/quantize algorithms must never violate.

// FuzzQuantizeDisjointReclaimTargetToWholeCore feeds random (target, reserved,
// cpusPerCore) triples and asserts the quantizer's safety contract:
//   - aligned/passthrough decisions return the target unchanged;
//   - a snapped (rounded) value is a whole-core multiple, within one core of
//     the target, and never below the committed reserved anchor.
func FuzzQuantizeDisjointReclaimTargetToWholeCore(f *testing.F) {
	seed := [][3]int{
		{3, 2, 2},
		{13, 4, 4},
		{1, 8, 4},
		{0, 0, 2},
		{7, 0, 2},
		{100, 50, 4},
		{5, 8, 4},
		{9, 1, 1},
	}
	for _, s := range seed {
		f.Add(s[0], s[1], s[2])
	}

	f.Fuzz(func(t *testing.T, target, reserved, cpusPerCore int) {
		// Guard against absurd inputs that make the algorithm unusable anyway.
		if target < 0 || reserved < 0 || cpusPerCore < 1 || target > 1<<20 || reserved > 1<<20 {
			return
		}
		got, decision := quantizeDisjointReclaimTargetToWholeCore(target, reserved, cpusPerCore)

		switch decision {
		case disjointReclaimQuantizeAligned, disjointReclaimQuantizePassthrough:
			if got != target {
				t.Fatalf("aligned/passthrough must be unchanged: target=%d reserved=%d w=%d got=%d",
					target, reserved, cpusPerCore, got)
			}
		case disjointReclaimQuantizeRoundedUp, disjointReclaimQuantizeRoundedDown:
			if got%cpusPerCore != 0 {
				t.Fatalf("snapped value must be whole-core: got=%d w=%d", got, cpusPerCore)
			}
			if got < reserved {
				t.Fatalf("snapped value below committed reserve: got=%d reserved=%d", got, reserved)
			}
			if d := absInt(got - target); d > cpusPerCore {
				t.Fatalf("snap distance too large: got=%d target=%d w=%d", got, target, cpusPerCore)
			}
		default:
			t.Fatalf("unknown decision %q", decision)
		}
	})
}

// FuzzJointlyNormalizeNonExclusiveTargets feeds random reclaim/dedicated splits
// and asserts the conservation and floor/ceiling contracts:
//   - the controlled partition (reclaim + the single dedicated pool) stays
//     size-conserved;
//   - a changed reclaim target is whole-core aligned;
//   - reclaim never drops below the committed anchor and the dedicated pool
//     stays within its [minimum, ceiling] box.
func FuzzJointlyNormalizeNonExclusiveTargets(f *testing.F) {
	// reclaimTarget, reserved, cpusPerCore, dedicatedSize, dedicatedMin, dedicatedCeiling
	seed := [][6]int{
		{3, 2, 2, 10, 2, 18},
		{13, 4, 4, 20, 4, 30},
		{1, 0, 2, 5, 1, 8},
		{7, 6, 2, 8, 1, 16},
	}
	for _, s := range seed {
		f.Add(s[0], s[1], s[2], s[3], s[4], s[5])
	}

	f.Fuzz(func(t *testing.T, reclaimTarget, reserved, cpusPerCore, dedicatedSize, dedicatedMin, dedicatedCeiling int) {
		if reclaimTarget < 0 || reserved < 0 || cpusPerCore < 1 ||
			dedicatedSize < 0 || dedicatedMin < 0 || dedicatedCeiling < 0 ||
			reclaimTarget > 1<<18 || dedicatedSize > 1<<18 {
			return
		}
		// Skip internally contradictory inputs: a minimum above the ceiling (or an
		// initial dedicated size already outside the box) cannot form a valid
		// partition, so the algorithm correctly passes through unchanged and the
		// floor/ceiling box is not meaningful for those.
		if dedicatedMin > dedicatedCeiling || dedicatedSize < dedicatedMin || dedicatedSize > dedicatedCeiling {
			return
		}
		in := nonExclusiveJointInput{
			CPUsPerCore:        cpusPerCore,
			ReclaimTarget:      reclaimTarget,
			ReservedForReclaim: reserved,
			DedicatedPoolSizes: map[string]int{"dpu": dedicatedSize},
			DedicatedMinimums:  map[string]int{"dpu": dedicatedMin},
			DedicatedCeilings:  map[string]int{"dpu": dedicatedCeiling},
		}
		out := jointlyNormalizeNonExclusiveTargets(in)

		// Conservation holds on every path, passthrough included.
		before := reclaimTarget + dedicatedSize
		after := out.ReclaimSize + out.DedicatedSizes["dpu"]
		if before != after {
			t.Fatalf("partition not conserved: before=%d after=%d input=%+v", before, after, in)
		}
		// The committed anchor and the floor/ceiling box are only commitments
		// when the algorithm actually re-assigns the partition (Changed). On
		// passthrough/aligned it returns the caller's numbers untouched, even if
		// they sit below the anchor -- QRM owns whole-core reconciliation then.
		if out.Changed {
			if out.ReclaimSize < reserved {
				t.Fatalf("changed reclaim below anchor: %d < %d", out.ReclaimSize, reserved)
			}
			if d := out.DedicatedSizes["dpu"]; d < dedicatedMin || d > dedicatedCeiling {
				t.Fatalf("changed dedicated outside [min,ceiling]: %d not in [%d,%d]", d, dedicatedMin, dedicatedCeiling)
			}
			if out.ReclaimSize%cpusPerCore != 0 {
				t.Fatalf("changed reclaim not whole-core: %d w=%d", out.ReclaimSize, cpusPerCore)
			}
		}
	})
}
