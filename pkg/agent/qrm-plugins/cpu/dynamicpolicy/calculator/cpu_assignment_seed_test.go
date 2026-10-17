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

package calculator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestTakeHTByNUMABalanceWithSeedPinsBehavior locks the seed-aware padding
// policy that used to live as a private helper in bulkhead/utils/view.go. It
// was moved here (additively) so all NUMA-balance CPU selection sits in one
// package; the exact cpu selections are pinned from the original back-to-back
// duel against the seed-blind TakeHTByNUMABalance.
//
// Two facts matter and are pinned below:
//  1. With an EMPTY seed, this function is numerically identical to
//     TakeHTByNUMABalance (round-robin by NUMA id, ascending within NUMA).
//  2. With a NON-EMPTY seed, it deliberately diverges: it pads the NUMA that is
//     currently under-represented (seed + already-taken counts) so the final
//     per-NUMA distribution stays balanced, instead of round-robin-ing by id.
func TestTakeHTByNUMABalanceWithSeedPinsBehavior(t *testing.T) {
	t.Parallel()

	// SMT: 16 cpus / 2 sockets / 2 NUMAs, CPUsPerCore()==2.
	//   NUMA0 = {0,1,2,3,8,9,10,11}  (core i == {i, i+8})
	//   NUMA1 = {4,5,6,7,12,13,14,15}
	smt, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	// Non-SMT: CPUsPerCore()==1. NUMA0={0..7}, NUMA1={8..15}.
	noSMT, err := machine.GenerateDummyCPUTopologyWithoutSMT(16, 2, 2)
	require.NoError(t, err)
	// Single NUMA.
	single, err := machine.GenerateDummyCPUTopologyWithoutSMT(8, 1, 1)
	require.NoError(t, err)

	empty := machine.NewCPUSet()

	tests := []struct {
		name       string
		topology   *machine.CPUTopology
		candidates machine.CPUSet
		seed       machine.CPUSet
		count      int
		reverse    bool
		want       string
	}{
		{
			name:     "SMT_empty_seed_forward_coincides_with_HT_roundrobin",
			topology: smt, candidates: smt.CPUDetails.CPUs(), seed: empty,
			count: 4, reverse: false, want: "0-1,4-5",
		},
		{
			name:     "SMT_empty_seed_reverse",
			topology: smt, candidates: smt.CPUDetails.CPUs(), seed: empty,
			count: 4, reverse: true, want: "10-11,14-15",
		},
		{
			name:     "NoSMT_empty_seed_forward",
			topology: noSMT, candidates: noSMT.CPUDetails.CPUs(), seed: empty,
			count: 4, reverse: false, want: "0-1,8-9",
		},
		{
			// seed already owns 4 cpus on NUMA0; pad on NUMA1 to balance the
			// final distribution. This is the case the seed-blind HT variant
			// gets WRONG (it returns 2-5 instead).
			name:       "SMT_nonempty_seed_pads_underrepresented_numa",
			topology:   smt,
			candidates: machine.NewCPUSet(2, 3, 10, 11, 4, 5, 12, 13, 14, 15),
			seed:       machine.NewCPUSet(0, 1, 8, 9),
			count:      4, reverse: false, want: "4-5,12-13",
		},
		{
			name:       "NoSMT_uneven_candidates_empty_seed",
			topology:   noSMT,
			candidates: machine.NewCPUSet(0, 1, 8, 9, 10, 11, 12, 13),
			seed:       empty, count: 5, reverse: false, want: "0-1,8-10",
		},
		{
			// over-request clamps to every candidate; never errors.
			name:       "clamps_when_request_exceeds_candidates",
			topology:   noSMT,
			candidates: machine.NewCPUSet(0, 1, 2),
			seed:       empty, count: 10, reverse: false, want: "0-2",
		},
		{
			name:       "single_numa",
			topology:   single,
			candidates: single.CPUDetails.CPUs(), seed: empty,
			count: 3, reverse: false, want: "0-2",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := TakeHTByNUMABalanceWithSeed(tc.topology, tc.candidates, tc.seed, tc.count, tc.reverse)
			require.Equal(t, tc.want, got.String(), "seed-aware take mismatch")
		})
	}
}

// TestTakeHTByNUMABalanceWithSeedDiffersFromSeedBlindHT pins the deliberate
// divergence: with a non-empty seed, the seed-aware padding must NOT equal the
// seed-blind TakeHTByNUMABalance output. If this ever becomes equal again, the
// seed weighting has silently regressed and the bulkhead padding behavior has
// changed.
func TestTakeHTByNUMABalanceWithSeedDiffersFromSeedBlindHT(t *testing.T) {
	t.Parallel()

	smt, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	info := &machine.KatalystMachineInfo{CPUTopology: smt}

	candidates := machine.NewCPUSet(2, 3, 10, 11, 4, 5, 12, 13, 14, 15)
	seed := machine.NewCPUSet(0, 1, 8, 9)

	seedAware := TakeHTByNUMABalanceWithSeed(smt, candidates, seed, 4, false)
	require.Equal(t, "4-5,12-13", seedAware.String())

	blind, _, err := TakeHTByNUMABalance(info, candidates, 4)
	require.NoError(t, err)
	require.Equal(t, "2-5", blind.String(), "seed-blind round-robin should be 2+2 across NUMAs")

	require.NotEqual(t, seedAware.String(), blind.String(),
		"seed-aware padding must diverge from seed-blind HT round-robin when seed is non-empty")
}

// TestTakeHTByNUMABalanceWithSeedEmptySeedEqualsHT confirms the empty-seed
// equivalence: without a seed the function must be byte-identical to the
// seed-blind TakeHTByNUMABalance.
func TestTakeHTByNUMABalanceWithSeedEmptySeedEqualsHT(t *testing.T) {
	t.Parallel()

	smt, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	info := &machine.KatalystMachineInfo{CPUTopology: smt}
	empty := machine.NewCPUSet()

	for _, count := range []int{1, 2, 3, 4, 5, 6, 8} {
		seedAware := TakeHTByNUMABalanceWithSeed(smt, smt.CPUDetails.CPUs(), empty, count, false)
		blind, _, err := TakeHTByNUMABalance(info, smt.CPUDetails.CPUs(), count)
		require.NoError(t, err)
		require.Equal(t, blind.String(), seedAware.String(),
			"empty-seed take must equal seed-blind HT take for count=%d", count)
	}
}
