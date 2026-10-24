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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJointlyNormalizeNonExclusiveTargets_OddAdviceAnchoredToCommitted is the core
// on-site case: SMT2 odd reclaim 3 with a single dedicated pool at 29 and a
// committed anchor of 2 must choose reclaim 2 / dedicated 30 (round down), never
// reclaim 4 which would drop dedicated below its minimum.
func TestJointlyNormalizeNonExclusiveTargets_OddAdviceAnchoredToCommitted(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu": 29},
		DedicatedMinimums:  map[string]int{"dpu": 29},
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointRoundedDown, adj.Decision)
	require.True(t, adj.Changed)
	require.Equal(t, 2, adj.ReclaimSize)
	require.Equal(t, 30, adj.DedicatedSizes["dpu"])
	// conservation: reclaim + dedicated unchanged.
	require.Equal(t, 3+29, adj.ReclaimSize+adj.DedicatedSizes["dpu"])
}

func TestJointlyNormalizeNonExclusiveTargets_AlignedPassthrough(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      2,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu": 30},
		DedicatedMinimums:  map[string]int{"dpu": 29},
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointAligned, adj.Decision)
	require.False(t, adj.Changed)
	require.Equal(t, 2, adj.ReclaimSize)
}

func TestJointlyNormalizeNonExclusiveTargets_SMT1Passthrough(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        1,
		ReclaimTarget:      3,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu": 29},
		DedicatedMinimums:  map[string]int{"dpu": 29},
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointAligned, adj.Decision)
	require.False(t, adj.Changed)
	require.Equal(t, 3, adj.ReclaimSize)
}

// TestJointlyNormalizeNonExclusiveTargets_UpperWhenLowerBelowFloor: when the lower
// candidate would shrink reclaim below the committed anchor, the upper candidate is
// chosen and dedicated gives up the headroom.
func TestJointlyNormalizeNonExclusiveTargets_UpperWhenLowerBelowFloor(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 4, // lower=2 violates the anchor
		DedicatedPoolSizes: map[string]int{"dpu": 29},
		DedicatedMinimums:  map[string]int{"dpu": 28}, // allows shrinking to 28
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointRoundedUp, adj.Decision)
	require.Equal(t, 4, adj.ReclaimSize)
	require.Equal(t, 28, adj.DedicatedSizes["dpu"])
	require.Equal(t, 3+29, adj.ReclaimSize+adj.DedicatedSizes["dpu"])
}

// TestJointlyNormalizeNonExclusiveTargets_MultiSourcePassthrough: more than one
// dedicated pool means no per-pool provenance to assign the core; pass through.
func TestJointlyNormalizeNonExclusiveTargets_MultiSourcePassthrough(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu-a": 15, "dpu-b": 14},
		DedicatedMinimums:  map[string]int{"dpu-a": 15, "dpu-b": 14},
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointPassthrough, adj.Decision)
	require.False(t, adj.Changed)
	require.Equal(t, 3, adj.ReclaimSize)
}

// TestJointlyNormalizeNonExclusiveTargets_NoLegalCandidatePassthrough: both
// candidates break a floor; publish the original and let QRM own materialization.
func TestJointlyNormalizeNonExclusiveTargets_NoLegalCandidatePassthrough(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 4, // lower=2 invalid
		DedicatedPoolSizes: map[string]int{"dpu": 28},
		DedicatedMinimums:  map[string]int{"dpu": 28}, // upper would need dedicated 27 < 28
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	require.Equal(t, nonExclusiveJointPassthrough, adj.Decision)
	require.False(t, adj.Changed)
	require.Equal(t, 3, adj.ReclaimSize)
}

func TestJointlyNormalizeNonExclusiveTargets_SMT4(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        4,
		ReclaimTarget:      13,
		ReservedForReclaim: 12,
		DedicatedPoolSizes: map[string]int{"dpu": 19},
		DedicatedMinimums:  map[string]int{"dpu": 19},
		DedicatedCeilings:  map[string]int{"dpu": 64},
	})
	// lower=12 (anchor), dedicated 19+1=20; upper=16 needs dedicated 16 < min 19.
	require.Equal(t, nonExclusiveJointRoundedDown, adj.Decision)
	require.Equal(t, 12, adj.ReclaimSize)
	require.Equal(t, 20, adj.DedicatedSizes["dpu"])
	require.Equal(t, 13+19, adj.ReclaimSize+adj.DedicatedSizes["dpu"])
}

// TestJointlyNormalizeNonExclusiveTargets_PinnedPackageCeilingCapsGrowth: a pinned
// dedicated pool can only grow within its package domain. When round-down would push
// dedicated past the package ceiling, that candidate is illegal and the target passes
// through (rather than overflowing the package).
func TestJointlyNormalizeNonExclusiveTargets_PinnedPackageCeilingCapsGrowth(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu": 29},
		DedicatedMinimums:  map[string]int{"dpu": 29},
		// package domain only holds 29; NUMA-wide available is larger and must not be used.
		DedicatedCeilings: map[string]int{"dpu": 29},
	})
	// lower=2 would grow dedicated 29->30 > package ceiling 29; upper=4 shrinks 29->28 < min 29.
	require.Equal(t, nonExclusiveJointPassthrough, adj.Decision)
	require.False(t, adj.Changed)
	require.Equal(t, 3, adj.ReclaimSize)
}

// TestJointlyNormalizeNonExclusiveTargets_UnknownCeilingPassthrough: when no
// eligibility-domain ceiling is recorded for the pool, do not guess.
func TestJointlyNormalizeNonExclusiveTargets_UnknownCeilingPassthrough(t *testing.T) {
	t.Parallel()
	adj := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 2,
		DedicatedPoolSizes: map[string]int{"dpu": 29},
		DedicatedMinimums:  map[string]int{"dpu": 29},
		DedicatedCeilings:  map[string]int{}, // not recorded
	})
	require.Equal(t, nonExclusiveJointPassthrough, adj.Decision)
	require.Equal(t, 3, adj.ReclaimSize)
}
