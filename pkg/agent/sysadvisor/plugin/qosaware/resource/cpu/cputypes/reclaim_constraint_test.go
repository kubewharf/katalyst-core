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

package cputypes

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReclaimConstraintScopeFormats pins the stable on-the-wire string contract.
// The scope string is recorded into InternalCPUCalculationResult.ReclaimConstraintTargets
// and emitted as a metric label, so any change here is a contract break and must be
// deliberate.
func TestReclaimConstraintScopeFormats(t *testing.T) {
	t.Parallel()

	require.Equal(t, ReclaimConstraintScope("non-exclusive/0"), NewNonExclusiveReclaimConstraintScope(0))
	require.Equal(t, ReclaimConstraintScope("non-exclusive/1"), NewNonExclusiveReclaimConstraintScope(1))
	// The faked (global/non-binding) NUMA id must be preserved verbatim.
	require.Equal(t, ReclaimConstraintScope("non-exclusive/-1"), NewNonExclusiveReclaimConstraintScope(-1))

	require.Equal(t, ReclaimConstraintScope("exclusive/region-a"), NewExclusiveReclaimConstraintScope("region-a"))
	require.Equal(t, ReclaimConstraintScope("legacy-exclusive/region-b"), NewLegacyExclusiveReclaimConstraintScope("region-b"))
}

// TestReclaimConstraintScopeDeterminism ensures the constructors are pure: the same
// input always yields the same scope key (so map lookups across cycles line up).
func TestReclaimConstraintScopeDeterminism(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		NewNonExclusiveReclaimConstraintScope(3),
		NewNonExclusiveReclaimConstraintScope(3),
	)
	require.Equal(t,
		NewExclusiveReclaimConstraintScope("dedicated-x"),
		NewExclusiveReclaimConstraintScope("dedicated-x"),
	)
	require.Equal(t,
		NewLegacyExclusiveReclaimConstraintScope("dedicated-x"),
		NewLegacyExclusiveReclaimConstraintScope("dedicated-x"),
	)
}

// TestReclaimConstraintScopeDisjoint ensures the three prefix namespaces never
// collide: a NUMA id, an exclusive region and a legacy-exclusive region that
// share a display name must still produce distinct map keys.
func TestReclaimConstraintScopeDisjoint(t *testing.T) {
	t.Parallel()

	nonExclusive := NewNonExclusiveReclaimConstraintScope(0)
	exclusive := NewExclusiveReclaimConstraintScope("0")
	legacy := NewLegacyExclusiveReclaimConstraintScope("0")

	require.NotEqual(t, nonExclusive, exclusive)
	require.NotEqual(t, nonExclusive, legacy)
	require.NotEqual(t, exclusive, legacy)

	// Distinct NUMAs map to distinct scopes.
	require.NotEqual(t,
		NewNonExclusiveReclaimConstraintScope(0),
		NewNonExclusiveReclaimConstraintScope(1),
	)
}

// TestReclaimConstraintConstants pins the iota ordering. These values are part of
// the per-cycle decision contract (and surfaced in logs/metrics), so reordering
// them would silently flip behavior and must be a conscious change.
func TestReclaimConstraintConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, ReclaimConstraint(0), ReclaimConstraintNone)
	require.Equal(t, ReclaimConstraint(1), ReclaimConstraintReservedFloor)
	require.NotEqual(t, ReclaimConstraintNone, ReclaimConstraintReservedFloor)
}
