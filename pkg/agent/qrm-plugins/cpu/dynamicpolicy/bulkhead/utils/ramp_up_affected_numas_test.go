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

package utils

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func newRampUpAffectedNUMAsTopology(t *testing.T) *machine.CPUTopology {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(24, 1, 3)
	require.NoError(t, err)
	return topology
}

func TestRampUpAffectedNUMAs(t *testing.T) {
	t.Parallel()

	topology := newRampUpAffectedNUMAsTopology(t)

	tests := []struct {
		name             string
		domains          sets.Int
		immutablePerNUMA bool
		expected         []int
	}{
		{
			name:     "nil domains affects nothing",
			domains:  nil,
			expected: []int{},
		},
		{
			name:     "empty domains affects nothing",
			domains:  sets.NewInt(),
			expected: []int{},
		},
		{
			name:     "global domain in overlap mode affects all real numas",
			domains:  sets.NewInt(commonstate.FakedNUMAID),
			expected: []int{0, 1, 2},
		},
		{
			name:             "global domain in immutable mode affects no real numas",
			domains:          sets.NewInt(commonstate.FakedNUMAID),
			immutablePerNUMA: true,
			expected:         []int{},
		},
		{
			name:     "single real numa domain affects only that numa",
			domains:  sets.NewInt(1),
			expected: []int{1},
		},
		{
			name:             "single real numa domain affects only that numa in immutable mode",
			domains:          sets.NewInt(1),
			immutablePerNUMA: true,
			expected:         []int{1},
		},
		{
			name:     "multiple real numa domains",
			domains:  sets.NewInt(0, 2),
			expected: []int{0, 2},
		},
		{
			name:     "global plus real numa domains in overlap mode",
			domains:  sets.NewInt(commonstate.FakedNUMAID, 0),
			expected: []int{0, 1, 2},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RampUpAffectedNUMAs(tc.domains, topology, tc.immutablePerNUMA)
			require.ElementsMatch(t, tc.expected, got.List())
		})
	}
}

func TestRampUpAffectedNUMAsNilTopology(t *testing.T) {
	t.Parallel()
	got := RampUpAffectedNUMAs(sets.NewInt(commonstate.FakedNUMAID, 0, 1), nil, false)
	require.True(t, got.Len() == 0)
}

func TestScopeHardPartitionReclaimTargets(t *testing.T) {
	t.Parallel()

	topology := newRampUpAffectedNUMAsTopology(t)
	targets := map[int]int{0: 6, 1: 6, 2: 6}

	t.Run("nil targets returned as-is", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, scopeHardPartitionReclaimTargets(nil, sets.NewInt(0), topology))
	})

	t.Run("all numas affected keeps all targets", func(t *testing.T) {
		t.Parallel()
		scoped := scopeHardPartitionReclaimTargets(targets, sets.NewInt(0, 1, 2), topology)
		require.Equal(t, 6, scoped[0])
		require.Equal(t, 6, scoped[1])
		require.Equal(t, 6, scoped[2])
	})

	t.Run("non-affected numas zeroed but keys retained", func(t *testing.T) {
		t.Parallel()
		scoped := scopeHardPartitionReclaimTargets(targets, sets.NewInt(0), topology)
		require.Equal(t, 6, scoped[0])
		require.Equal(t, 0, scoped[1])
		require.Equal(t, 0, scoped[2])
		require.Len(t, scoped, 3)
	})

	t.Run("empty affected set zeroes every target", func(t *testing.T) {
		t.Parallel()
		scoped := scopeHardPartitionReclaimTargets(targets, sets.NewInt(), topology)
		require.Equal(t, 0, scoped[0])
		require.Equal(t, 0, scoped[1])
		require.Equal(t, 0, scoped[2])
	})
}

func TestGlobalDomainOnlyRampUpZeroesRealNUMATargetsInImmutableMode(t *testing.T) {
	t.Parallel()

	topology := newRampUpAffectedNUMAsTopology(t)
	// In immutable-per-NUMA mode, a global-domain-only ramp-up must not touch
	// any real NUMA reclaim target: the floor stays off for every real NUMA.
	affected := RampUpAffectedNUMAs(sets.NewInt(commonstate.FakedNUMAID), topology, true)
	require.True(t, affected.Len() == 0)

	targets := map[int]int{0: 6, 1: 6, 2: 6}
	scoped := scopeHardPartitionReclaimTargets(targets, affected, topology)
	require.Equal(t, 0, scoped[0])
	require.Equal(t, 0, scoped[1])
	require.Equal(t, 0, scoped[2])
}
