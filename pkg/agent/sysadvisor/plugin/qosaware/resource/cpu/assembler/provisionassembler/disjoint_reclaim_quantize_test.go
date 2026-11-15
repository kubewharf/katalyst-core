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

	"github.com/stretchr/testify/require"
)

func TestQuantizeDisjointReclaimTargetToWholeCore_OddRoundDownToCommitted(t *testing.T) {
	t.Parallel()
	got, decision := quantizeDisjointReclaimTargetToWholeCore(3, 2, 2)
	require.Equal(t, 2, got)
	require.Equal(t, disjointReclaimQuantizeRoundedDown, decision)
}

func TestQuantizeDisjointReclaimTargetToWholeCore_AlignedUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ reclaim, reserved, w int }{
		{8, 2, 2}, {4, 4, 2}, {12, 4, 4}, {0, 0, 2},
	} {
		tc := tc
		got, decision := quantizeDisjointReclaimTargetToWholeCore(tc.reclaim, tc.reserved, tc.w)
		require.Equal(t, tc.reclaim, got)
		require.Equal(t, disjointReclaimQuantizeAligned, decision)
	}
}

func TestQuantizeDisjointReclaimTargetToWholeCore_SMT1Passthrough(t *testing.T) {
	t.Parallel()
	got, decision := quantizeDisjointReclaimTargetToWholeCore(3, 1, 1)
	require.Equal(t, 3, got)
	require.Equal(t, disjointReclaimQuantizeAligned, decision)
}

func TestQuantizeDisjointReclaimTargetToWholeCore_NeverShrinksReserve(t *testing.T) {
	t.Parallel()
	got, decision := quantizeDisjointReclaimTargetToWholeCore(3, 4, 2)
	require.Equal(t, 4, got)
	require.Equal(t, disjointReclaimQuantizeRoundedUp, decision)
}

func TestQuantizeDisjointReclaimTargetToWholeCore_PassthroughWhenNoLegalCandidate(t *testing.T) {
	t.Parallel()
	got, decision := quantizeDisjointReclaimTargetToWholeCore(5, 8, 4)
	require.Equal(t, 8, got)
	require.Equal(t, disjointReclaimQuantizeRoundedUp, decision)

	got2, decision2 := quantizeDisjointReclaimTargetToWholeCore(1, 8, 4)
	require.Equal(t, 1, got2)
	require.Equal(t, disjointReclaimQuantizePassthrough, decision2)
}

func TestQuantizeDisjointReclaimTargetToWholeCore_SMT4Grid(t *testing.T) {
	t.Parallel()
	got, decision := quantizeDisjointReclaimTargetToWholeCore(13, 4, 4)
	require.Equal(t, 12, got)
	require.Equal(t, disjointReclaimQuantizeRoundedDown, decision)
}
