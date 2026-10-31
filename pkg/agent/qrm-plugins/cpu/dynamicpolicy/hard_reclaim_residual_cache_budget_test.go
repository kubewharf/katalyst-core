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

package dynamicpolicy

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestPartitionResidualCacheRetainedBytesCountsRepeatedPayloadOnce proves the
// first-pass retained-byte accounting cannot over-count a repeated canonical
// payload and thereby falsely reject a batch that the authoritative second pass
// (which retains only payloads seen >=2 times) would admit.
//
// The digest and the canonical payload are the same sha256-serialization of the
// demands (writePartitionResidualCanonical), so every occurrence of a repeated
// digest produces byte-identical payload. The first pass must therefore charge
// the payload length exactly once, even though the digest repeats N times. We pin
// maxRetainedBytes to exactly that one-time length: if the first pass had charged
// it per-repeat, this would fail the retained-memory budget even though the
// second pass fits.
func TestPartitionResidualCacheRetainedBytesCountsRepeatedPayloadOnce(t *testing.T) {
	t.Parallel()
	topology := &machine.CPUTopology{
		NumCPUs:      6,
		NumCores:     3,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			5: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		},
	}

	demands := []partitionDemand{
		{
			key:       "dedicated-a",
			quantity:  1,
			eligible:  machine.NewCPUSet(0, 1, 3, 4),
			preferred: machine.NewCPUSet(0, 3),
			class:     advisorBlockClassDedicated,
		},
		{
			key:       "shared-a",
			quantity:  1,
			eligible:  machine.NewCPUSet(2, 5),
			preferred: machine.NewCPUSet(2),
			class:     advisorBlockClassShared,
		},
	}

	canonical, err := newCanonicalPartitionResidual(demands, topology)
	require.NoError(t, err)
	payloadLen := len(canonical.payload)
	require.Greater(t, payloadLen, 0, "fixture must produce a non-empty payload")

	// Four identical occurrences: the digest repeats, the payload must be retained
	// exactly once.
	batch := [][]partitionDemand{demands, demands, demands, demands}

	// Budget pinned to exactly the one-time payload cost. A first pass that
	// over-counted (e.g. charged the payload per repeat) would reject this even
	// though the second pass admits it.
	cache, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	cache.limits.maxRetainedBytes = payloadLen
	require.NoError(t, preparePartitionResidualCache(cache, batch),
		"repeated payload must be retained exactly once and fit the tight budget")
	require.Equal(t, payloadLen, cache.usage.retainedBytes,
		"retained bytes must equal the single payload, not the per-repeat sum")

	// Sanity: one byte less is a genuine rejection, proving the budget is real and
	// not a no-op.
	tooTight, err := newPartitionResidualSolveCache(topology)
	require.NoError(t, err)
	tooTight.limits.maxRetainedBytes = payloadLen - 1
	err = preparePartitionResidualCache(tooTight, batch)
	require.Error(t, err, "one byte below the true retained cost must be rejected")
	require.True(t, errors.Is(err, errPartitionResidualRetainedMemoryBudget),
		"expected the retained-memory budget error, got %v", err)
}
