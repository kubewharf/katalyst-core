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

package cpu

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestAdvisorObservedReclaimByScope covers how the advisor aggregates the current
// (QRM-committed) reclaim pool cpuset into each constraint scope. The observed
// value is the ACK baseline: a scope is ACK-eligible only when its pool entry is
// readable, and its size is the sum of the per-NUMA cpuset sizes of its member
// NUMAs. The global (FakedNUMAID) scope aggregates over its real backing NUMAs.
func TestAdvisorObservedReclaimByScope(t *testing.T) {
	t.Parallel()

	real0 := provisionassembler.NewNonExclusiveReclaimConstraintScope(0)
	real1 := provisionassembler.NewNonExclusiveReclaimConstraintScope(1)
	global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

	scopeNumas := map[provisionassembler.ReclaimConstraintScope][]int{
		real0:  {0},
		real1:  {1},
		global: {2, 3}, // backing real NUMAs; the faked id itself carries no cpuset
	}

	t.Run("nil advisor and nil metaCache yield no ACK-eligible scope", func(t *testing.T) {
		t.Parallel()

		var nilAdvisor *cpuResourceAdvisor
		got, ok := nilAdvisor.observedReclaimByScope(scopeNumas)
		require.Empty(t, got)
		require.Empty(t, ok)

		got, ok = (&cpuResourceAdvisor{}).observedReclaimByScope(scopeNumas)
		require.Empty(t, got)
		require.Empty(t, ok)
	})

	t.Run("missing pool is not ACK evidence", func(t *testing.T) {
		t.Parallel()

		advisor := &cpuResourceAdvisor{metaCache: metacache.NewDummyMetaCacheImp()}
		got, ok := advisor.observedReclaimByScope(scopeNumas)
		require.Empty(t, got)
		require.Empty(t, ok)
	})

	t.Run("nil pool is treated like a missing pool", func(t *testing.T) {
		t.Parallel()

		metaCache := metacache.NewDummyMetaCacheImp()
		require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReclaim, nil))
		advisor := &cpuResourceAdvisor{metaCache: metaCache}
		got, ok := advisor.observedReclaimByScope(scopeNumas)
		require.Empty(t, got)
		require.Empty(t, ok)
	})

	t.Run("per-NUMA sizes sum into their scope, global sums backing NUMAs", func(t *testing.T) {
		t.Parallel()

		metaCache := metacache.NewDummyMetaCacheImp()
		require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReclaim, &types.PoolInfo{
			TopologyAwareAssignments: types.TopologyAwareAssignment{
				0: machine.MustParse("0-2"), // 3 CPUs -> real0
				1: machine.MustParse("8-9"), // 2 CPUs -> real1
				2: machine.MustParse("4"),   // 1 CPU  -> global
				3: machine.MustParse("6-7"), // 2 CPUs -> global
			},
		}))
		advisor := &cpuResourceAdvisor{metaCache: metaCache}

		got, ok := advisor.observedReclaimByScope(scopeNumas)
		require.True(t, ok[real0])
		require.True(t, ok[real1])
		require.True(t, ok[global])
		require.Equal(t, 3, got[real0])
		require.Equal(t, 2, got[real1])
		require.Equal(t, 1+2, got[global])
	})
}

// TestAdvisorPublishedReclaimByScope covers how the advisor aggregates the
// published reclaim PoolEntries back into scopes for the next cycle's ACK
// comparison. The load-bearing case is the global (FakedNUMAID) scope: its member
// list holds the real backing NUMAs, but the global pool is published as a single
// aggregate entry keyed by FakedNUMAID, so that id must be explicitly mapped onto
// the global scope -- otherwise the global scope never acknowledges and its ceiling
// cannot advance.
func TestAdvisorPublishedReclaimByScope(t *testing.T) {
	t.Parallel()

	real0 := provisionassembler.NewNonExclusiveReclaimConstraintScope(0)
	global := provisionassembler.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)

	// The global scope's members are its real backing NUMAs; FakedNUMAID is NOT in
	// the member list, which is exactly why the explicit faked->global mapping in
	// publishedReclaimByScope matters.
	scopeNumas := map[provisionassembler.ReclaimConstraintScope][]int{
		real0:  {0},
		global: {2, 3},
	}

	t.Run("nil and empty result aggregate to nothing", func(t *testing.T) {
		t.Parallel()

		advisor := &cpuResourceAdvisor{}
		require.Empty(t, advisor.publishedReclaimByScope(nil, scopeNumas))
		require.Empty(t, advisor.publishedReclaimByScope(&types.InternalCPUCalculationResult{}, scopeNumas))
	})

	t.Run("per-NUMA entries aggregate and FakedNUMAID maps onto global scope", func(t *testing.T) {
		t.Parallel()

		result := &types.InternalCPUCalculationResult{
			PoolEntries: map[string]map[int]types.CPUResource{
				commonstate.PoolNameReclaim: {
					0:                       {Size: 7}, // real scope
					commonstate.FakedNUMAID: {Size: 5}, // single global aggregate publication
				},
			},
		}
		got := (&cpuResourceAdvisor{}).publishedReclaimByScope(result, scopeNumas)
		require.Equal(t, 7, got[real0])
		require.Equal(t, 5, got[global])
		// The backing real NUMAs 2/3 carry no separate reclaim entry; the global
		// aggregate lives only on the FakedNUMAID key.
		require.Len(t, got, 2)
	})

	t.Run("published reclaim on a NUMA outside the mapping is skipped, not mis-attributed", func(t *testing.T) {
		t.Parallel()

		result := &types.InternalCPUCalculationResult{
			PoolEntries: map[string]map[int]types.CPUResource{
				commonstate.PoolNameReclaim: {
					0:                       {Size: 7},
					commonstate.FakedNUMAID: {Size: 5},
					99:                      {Size: 42}, // no scope covers NUMA 99
				},
			},
		}
		got := (&cpuResourceAdvisor{}).publishedReclaimByScope(result, scopeNumas)
		require.Equal(t, 7, got[real0])
		require.Equal(t, 5, got[global])
		require.Len(t, got, 2)
	})
}
