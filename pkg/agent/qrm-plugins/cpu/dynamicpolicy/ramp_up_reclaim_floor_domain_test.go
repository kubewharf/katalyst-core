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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGlobalRampUpDomainActive(t *testing.T) {
	t.Parallel()

	require.False(t, globalRampUpDomainActive(nil))
	require.False(t, globalRampUpDomainActive(sets.NewInt()))
	require.False(t, globalRampUpDomainActive(sets.NewInt(0, 1)))
	require.True(t, globalRampUpDomainActive(sets.NewInt(commonstate.FakedNUMAID)))
	require.True(t, globalRampUpDomainActive(sets.NewInt(commonstate.FakedNUMAID, 0)))
}

func newThreeNUMATopology(t *testing.T) *machine.CPUTopology {
	topology, err := machine.GenerateDummyCPUTopology(24, 1, 3)
	require.NoError(t, err)
	return topology
}

func TestAffectedRampUpNUMAs(t *testing.T) {
	t.Parallel()

	topology := newThreeNUMATopology(t)

	tests := []struct {
		name             string
		domains          sets.Int
		immutablePerNUMA bool
		expectedNUMAs    []int
	}{
		{
			name:          "nil domains affects nothing",
			domains:       nil,
			expectedNUMAs: []int{},
		},
		{
			name:          "empty domains affects nothing",
			domains:       sets.NewInt(),
			expectedNUMAs: []int{},
		},
		{
			name:             "global domain in immutable mode affects no real numa",
			domains:          sets.NewInt(commonstate.FakedNUMAID),
			immutablePerNUMA: true,
			expectedNUMAs:    []int{},
		},
		{
			name:             "global domain in overlap mode affects every real numa",
			domains:          sets.NewInt(commonstate.FakedNUMAID),
			immutablePerNUMA: false,
			expectedNUMAs:    []int{0, 1, 2},
		},
		{
			name:             "single real numa domain narrows onto itself in immutable mode",
			domains:          sets.NewInt(1),
			immutablePerNUMA: true,
			expectedNUMAs:    []int{1},
		},
		{
			name:             "single real numa domain narrows onto itself in overlap mode",
			domains:          sets.NewInt(1),
			immutablePerNUMA: false,
			expectedNUMAs:    []int{1},
		},
		{
			name:             "multiple real numa domains narrow onto themselves",
			domains:          sets.NewInt(0, 2),
			immutablePerNUMA: true,
			expectedNUMAs:    []int{0, 2},
		},
		{
			name:             "global plus real numa in overlap mode covers all",
			domains:          sets.NewInt(commonstate.FakedNUMAID, 0),
			immutablePerNUMA: false,
			expectedNUMAs:    []int{0, 1, 2},
		},
		{
			name:             "global plus real numa in immutable mode covers only the real numa",
			domains:          sets.NewInt(commonstate.FakedNUMAID, 0),
			immutablePerNUMA: true,
			expectedNUMAs:    []int{0},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := affectedRampUpNUMAs(tt.domains, topology, tt.immutablePerNUMA)
			require.ElementsMatch(t, tt.expectedNUMAs, got.List())
		})
	}
}

func TestAffectedRampUpNUMAsDeterministic(t *testing.T) {
	t.Parallel()

	topology := newThreeNUMATopology(t)
	domains := sets.NewInt(commonstate.FakedNUMAID, 1)

	first := affectedRampUpNUMAs(domains, topology, false)
	for i := 0; i < 50; i++ {
		again := affectedRampUpNUMAs(domains, topology, false)
		require.True(t, first.Equal(again), "affectedRampUpNUMAs not deterministic on iteration %d", i)
	}
	require.ElementsMatch(t, []int{0, 1, 2}, first.List())
}

func TestAffectedRampUpNUMAsNilTopology(t *testing.T) {
	t.Parallel()

	require.Empty(t, affectedRampUpNUMAs(sets.NewInt(0, commonstate.FakedNUMAID), nil, false).List())
	require.Empty(t, affectedRampUpNUMAs(nil, nil, false).List())
}
