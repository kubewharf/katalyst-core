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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newSafetyNetAssembler builds a ProvisionAssemblerCommon wired only with the
// minimum dependencies the reclaim-pool calculators read: a dummy metaReader
// (so IsQuotaCtrlKnobEnabled reports the quota knob disabled) and no
// metaServer (so cpuCountInNUMAs-style callers degrade to 0). It is meant for
// direct, hermetic unit tests of the pool-calculation algorithms.
func newSafetyNetAssembler() *ProvisionAssemblerCommon {
	return &ProvisionAssemblerCommon{
		metaReader: metacache.NewDummyMetaCacheImp(),
	}
}

func newEmptyResult() *types.InternalCPUCalculationResult {
	return &types.InternalCPUCalculationResult{
		PoolEntries:                 map[string]map[int]types.CPUResource{},
		PoolOverlapInfo:             map[string]map[int]map[string]int{},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
	}
}

// -----------------------------------------------------------------------------
// calculateNonOverlapReclaimPool golden tests (previously 41.7% covered).
// -----------------------------------------------------------------------------

func TestCalculateNonOverlapReclaimPool_Golden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		data           func() *reclaimPoolCalculationData
		policy         reclaimPoolCalculationPolicy
		wantReclaimed  int
		wantOverlap    int
		wantPodOverlap map[string]map[string]int // podUID -> container -> size (numa 0)
	}{
		{
			name: "reclaim disabled -> only reserved survives",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					dedicatedReclaimCoresSize:              8,
					reservedForReclaim:                     3,
					nodeEnableReclaim:                      false,
					numaID:                                 0,
				}
			},
			policy:        reclaimPoolCalculationPolicy{},
			wantReclaimed: 3,
		},
		{
			name: "reclaim enabled, no dedicated overlap -> standalone residual only",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40, "isolation": 10},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					totalUnusedNonReclaimablePinnedCPUSize: 5,
					dedicatedReclaimCoresSize:              8,
					reservedForReclaim:                     3,
					nodeEnableReclaim:                      true,
					numaID:                                 0,
				}
			},
			// 100 - (40+10) - 5 + 8 + 3 = 56
			policy:        reclaimPoolCalculationPolicy{allowDedicatedOverlap: false},
			wantReclaimed: 56,
		},
		{
			name: "dedicated overlap accumulates when reclaimable and podSet present",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					dedicatedInfo: regionInfo{
						requests:      map[string]int{"d1": 20},
						requirements:  map[string]int{"d1": 12},
						reclaimEnable: map[string]bool{"d1": true},
						podSet:        map[string]types.PodSet{"d1": {"pod-1": sets.NewString("c1")}},
					},
					nodeEnableReclaim: true,
					numaID:            0,
				}
			},
			policy: reclaimPoolCalculationPolicy{allowDedicatedOverlap: true},
			// overlap = 20 - 12 = 8; standalone = 100 - 40 = 60
			wantReclaimed: 60,
			wantOverlap:   8,
			wantPodOverlap: map[string]map[string]int{
				"pod-1": {"c1": 8},
			},
		},
		{
			name: "dedicated overlap skipped when reclaimSize non-positive",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					dedicatedInfo: regionInfo{
						requests:      map[string]int{"d1": 10},
						requirements:  map[string]int{"d1": 12},
						reclaimEnable: map[string]bool{"d1": true},
						podSet:        map[string]types.PodSet{"d1": {"pod-1": sets.NewString("c1")}},
					},
					nodeEnableReclaim: true,
					numaID:            0,
				}
			},
			policy:        reclaimPoolCalculationPolicy{allowDedicatedOverlap: true},
			wantReclaimed: 60,
			wantOverlap:   0,
		},
		{
			name: "dedicated overlap skipped when pool not reclaim-enabled",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					dedicatedInfo: regionInfo{
						requests:      map[string]int{"d1": 30},
						requirements:  map[string]int{"d1": 12},
						reclaimEnable: map[string]bool{"d1": false},
						podSet:        map[string]types.PodSet{"d1": {"pod-1": sets.NewString("c1")}},
					},
					nodeEnableReclaim: true,
					numaID:            0,
				}
			},
			policy:        reclaimPoolCalculationPolicy{allowDedicatedOverlap: true},
			wantReclaimed: 60,
			wantOverlap:   0,
		},
		{
			name: "dedicated overlap with headroom but no podSet records nothing",
			data: func() *reclaimPoolCalculationData {
				return &reclaimPoolCalculationData{
					shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 40},
					shareAndIsolatedDedicatedPoolAvailable: 100,
					dedicatedInfo: regionInfo{
						requests:      map[string]int{"d1": 30},
						requirements:  map[string]int{"d1": 12},
						reclaimEnable: map[string]bool{"d1": true},
						podSet:        map[string]types.PodSet{},
					},
					nodeEnableReclaim: true,
					numaID:            0,
				}
			},
			policy:        reclaimPoolCalculationPolicy{allowDedicatedOverlap: true},
			wantReclaimed: 60,
			wantOverlap:   0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pa := newSafetyNetAssembler()
			result := newEmptyResult()
			data := tt.data()
			got, overlap, quota, err := pa.calculateNonOverlapReclaimPool(data, tt.policy)
			require.NoError(t, err)
			require.Equal(t, tt.wantReclaimed, got, "reclaimed cores mismatch")
			require.Equal(t, tt.wantOverlap, overlap, "overlap cores mismatch")
			require.Equal(t, float64(-1), quota, "quota must stay -1 on the non-overlap path")
			// The calculator records dedicated overlap only via atoms; the production
			// path then clamps. Replay that clamp here so the golden asserts the final,
			// atom-derived map. budget==overlap means no shrinking on these rows.
			clampReclaimOverlapMetadata(result, 0, overlap, data.overlapAtoms...)
			if tt.wantPodOverlap != nil {
				require.Equal(t, tt.wantPodOverlap, result.GetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, 0))
			} else {
				require.Empty(t, result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim])
			}
		})
	}
}

// -----------------------------------------------------------------------------
// calculateEnabledReclaimPool golden tests (previously 84.1% covered).
// -----------------------------------------------------------------------------

func TestCalculateEnabledReclaimPool_Golden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          *reclaimPoolCalculationData
		policy        reclaimPoolCalculationPolicy
		wantReclaimed int
		wantOverlap   int
	}{
		{
			name: "shared overlap folds headroom into reclaim",
			data: &reclaimPoolCalculationData{
				shareInfo: regionInfo{
					requirements:  map[string]int{"s1": 10},
					reclaimEnable: map[string]bool{"s1": true},
				},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 25},
				shareAndIsolatedDedicatedPoolAvailable: 60,
				totalUnusedNonReclaimablePinnedCPUSize: 5,
			},
			// freeStandalone = max(60 - 25 - 5, 0) = 30; overlap = 25 - 10 = 15
			policy:        reclaimPoolCalculationPolicy{allowSharedOverlap: true},
			wantReclaimed: 45,
			wantOverlap:   15,
		},
		{
			name: "shared overlap disabled drops the overlap component",
			data: &reclaimPoolCalculationData{
				shareInfo: regionInfo{
					requirements:  map[string]int{"s1": 10},
					reclaimEnable: map[string]bool{"s1": true},
				},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 25},
				shareAndIsolatedDedicatedPoolAvailable: 60,
				totalUnusedNonReclaimablePinnedCPUSize: 5,
			},
			policy:        reclaimPoolCalculationPolicy{allowSharedOverlap: false},
			wantReclaimed: 30,
			wantOverlap:   0,
		},
		{
			name: "held-outside-pools reserve adds back",
			data: &reclaimPoolCalculationData{
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 25},
				shareAndIsolatedDedicatedPoolAvailable: 60,
				reservedForReclaim:                     4,
				reserveHeldOutsidePools:                true,
			},
			// freeStandalone = 60 - 25 = 35; + reserved 4
			policy:        reclaimPoolCalculationPolicy{},
			wantReclaimed: 39,
			wantOverlap:   0,
		},
		{
			name: "dedicated overlap folds reclaimable dedicated headroom",
			data: &reclaimPoolCalculationData{
				dedicatedInfo: regionInfo{
					requirements:  map[string]int{"d1": 10},
					reclaimEnable: map[string]bool{"d1": true},
					podSet:        map[string]types.PodSet{"d1": {"pod-1": sets.NewString("c1")}},
				},
				dedicatedPoolSizes:                     map[string]int{"d1": 18},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"d1": 18},
				shareAndIsolatedDedicatedPoolAvailable: 40,
			},
			// freeStandalone = max(40 - 18, 0) = 22; overlap = 18 - 10 = 8
			policy:        reclaimPoolCalculationPolicy{allowDedicatedOverlap: true},
			wantReclaimed: 30,
			wantOverlap:   8,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pa := newSafetyNetAssembler()
			got, overlap, quota, err := pa.calculateEnabledReclaimPool(tt.data, tt.policy)
			require.NoError(t, err)
			require.Equal(t, tt.wantReclaimed, got)
			require.Equal(t, tt.wantOverlap, overlap)
			// dummy metaReader negotiates no feature gates -> quota knob off.
			require.Equal(t, float64(-1), quota)
		})
	}
}

// -----------------------------------------------------------------------------
// calculateOverlapReclaimPool golden tests (previously 66.7% covered).
// -----------------------------------------------------------------------------

func TestCalculateOverlapReclaimPool_Golden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		data            *reclaimPoolCalculationData
		policy          reclaimPoolCalculationPolicy
		wantReclaimed   int
		wantOverlap     int
		wantOverlapInfo map[string]int // overlapPoolName -> size on numa 0
	}{
		{
			name: "reclaim disabled clamps to reserved and regulates share pools",
			data: &reclaimPoolCalculationData{
				shareInfo: regionInfo{
					requirements:  map[string]int{"s1": 4},
					requests:      map[string]int{"s1": 10},
					reclaimEnable: map[string]bool{"s1": true},
				},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 10},
				shareAndIsolatedDedicatedPoolAvailable: 30,
				reservedForReclaim:                     5,
				nodeEnableReclaim:                      false,
				numaID:                                 0,
			},
			// reclaimed = reserved(5) <= sum(sharePoolSizes=10) -> regulate to 5.
			policy:          reclaimPoolCalculationPolicy{allowSharedOverlap: true},
			wantReclaimed:   5,
			wantOverlap:     5,
			wantOverlapInfo: map[string]int{"s1": 5},
		},
		{
			name: "reclaim enabled folds pool headroom into overlap",
			data: &reclaimPoolCalculationData{
				shareInfo: regionInfo{
					requirements:  map[string]int{"s1": 4},
					requests:      map[string]int{"s1": 10},
					reclaimEnable: map[string]bool{"s1": true},
				},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 10},
				shareAndIsolatedDedicatedPoolAvailable: 30,
				dedicatedPoolSizes:                     map[string]int{},
				dedicatedReclaimCoresSize:              0,
				reservedForReclaim:                     2,
				nodeEnableReclaim:                      true,
				numaID:                                 0,
			},
			// shareReclaim = 30 - 4 = 26; overlap = 10 - 4 = 6.
			policy:          reclaimPoolCalculationPolicy{allowSharedOverlap: true},
			wantReclaimed:   26,
			wantOverlap:     6,
			wantOverlapInfo: map[string]int{"s1": 6},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pa := newSafetyNetAssembler()
			result := newEmptyResult()
			got, overlap, quota, err := pa.calculateOverlapReclaimPool(tt.data, tt.policy)
			require.NoError(t, err)
			require.Equal(t, tt.wantReclaimed, got)
			require.Equal(t, tt.wantOverlap, overlap)
			require.Equal(t, float64(-1), quota)
			// The calculator records share overlap only via atoms; the production path
			// then clamps. Replay that clamp here so the golden asserts the final,
			// atom-derived PoolOverlapInfo. budget==overlap means no shrinking here.
			clampReclaimOverlapMetadata(result, 0, overlap, tt.data.overlapAtoms...)
			require.Equal(t, tt.wantOverlapInfo, result.GetPoolOverlapInfo(commonstate.PoolNameReclaim, 0))
		})
	}
}

// -----------------------------------------------------------------------------
// Reclaim-overlap output contract: calculators emit only atoms, clamp rebuilds
// the final overlap map, and the clamped total never exceeds the overlap budget.
// -----------------------------------------------------------------------------

func TestReclaimOverlapAtomsThenClampBudgetBounded(t *testing.T) {
	t.Parallel()
	pa := newSafetyNetAssembler()

	// Two share pools, each reclaimable by 6 CPUs (raw overlap = 12).
	data := &reclaimPoolCalculationData{
		shareInfo: regionInfo{
			requirements:  map[string]int{"s1": 4, "s2": 4},
			requests:      map[string]int{"s1": 10, "s2": 10},
			reclaimEnable: map[string]bool{"s1": true, "s2": true},
		},
		shareAndIsolateDedicatedPoolSizes:      map[string]int{"s1": 10, "s2": 10},
		shareAndIsolatedDedicatedPoolAvailable: 30,
		nodeEnableReclaim:                      true,
		numaID:                                 0,
	}
	result := newEmptyResult()

	_, overlap, _, err := pa.calculateOverlapReclaimPool(data, reclaimPoolCalculationPolicy{allowSharedOverlap: true})
	require.NoError(t, err)
	require.Equal(t, 12, overlap)

	// Direct writes are gone: nothing reaches the overlap map until clamp runs.
	require.Empty(t, result.PoolOverlapInfo[commonstate.PoolNameReclaim][0])

	// Clamp to a 4-CPU budget: the first atom (by sorted key) absorbs up to 4,
	// and the emitted total must never exceed the budget.
	const budget = 4
	actual := clampReclaimOverlapMetadata(result, 0, budget, data.overlapAtoms...)
	require.LessOrEqual(t, actual, budget)

	info := result.GetPoolOverlapInfo(commonstate.PoolNameReclaim, 0)
	sum := 0
	for _, v := range info {
		sum += v
		require.LessOrEqual(t, v, overlap)
	}
	require.LessOrEqual(t, sum, budget)
	require.Equal(t, budget, sum) // budget is fully consumed by the sorted-first atom
}

// -----------------------------------------------------------------------------
// pickWholeCoreReclaimAnchor exhaustive tie-break grid (previously 33.3%).
// -----------------------------------------------------------------------------

func TestPickWholeCoreReclaimAnchor_TieBreakGrid(t *testing.T) {
	t.Parallel()

	// (a, b, reserved, original) -> expected winner.
	tests := []struct {
		name     string
		a, b     int
		reserved int
		original int
		want     int
	}{
		{"a closer to reserve wins", 8, 12, 4, 10, 8},
		{"b closer to reserve wins", 8, 12, 11, 9, 12},
		{"equal reserve distance -> closer to advisor target", 8, 12, 10, 11, 12},
		{"equal reserve+advisor distance -> smaller value", 8, 12, 10, 10, 8},
		{"exact anchor equal distance picks smaller on tie", 4, 8, 6, 6, 4},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, pickWholeCoreReclaimAnchor(tt.a, tt.b, tt.reserved, tt.original))
		})
	}
}

// Property test: the quantizer never asks QRM to shrink the committed reclaim
// anchor and only emits whole-core (or passthrough) decisions.
func TestQuantizeDisjointReclaimTargetToWholeCore_PropertyInvariants(t *testing.T) {
	t.Parallel()

	for target := 0; target < 40; target++ {
		for reserved := 0; reserved <= target+6; reserved++ {
			for _, w := range []int{1, 2, 4} {
				got, decision := quantizeDisjointReclaimTargetToWholeCore(target, reserved, w)

				switch decision {
				case disjointReclaimQuantizeAligned:
					// Already-aligned or SMT1 targets pass through unchanged: no floor
					// is applied because QRM owns the steady-state reconciliation.
					require.Equalf(t, target, got,
						"aligned decision must return the original target: target=%d reserved=%d", target, reserved)
				case disjointReclaimQuantizePassthrough:
					require.Equalf(t, target, got,
						"passthrough must return the original target: target=%d got=%d", target, got)
				case disjointReclaimQuantizeRoundedUp, disjointReclaimQuantizeRoundedDown:
					// A snapped whole-core value must respect the committed anchor.
					require.GreaterOrEqualf(t, got, reserved,
						"snapped value must not drop below committed reserve: target=%d reserved=%d got=%d",
						target, reserved, got)
					require.Equalf(t, 0, got%w,
						"snapped value must be whole-core multiple: got=%d w=%d", got, w)
					require.LessOrEqualf(t, absInt(got-target), w,
						"snap must be within one core: target=%d got=%d w=%d", target, got, w)
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// jointlyNormalizeNonExclusiveTargets conservation property.
// -----------------------------------------------------------------------------

func TestJointlyNormalizeNonExclusiveTargets_ConservationProperty(t *testing.T) {
	t.Parallel()

	for _, w := range []int{2, 4} {
		for target := 0; target < 20; target++ {
			for reserved := 0; reserved <= target; reserved++ {
				dedicated := 10
				min := 2
				ceiling := 18
				in := nonExclusiveJointInput{
					CPUsPerCore:        w,
					ReclaimTarget:      target,
					ReservedForReclaim: reserved,
					DedicatedPoolSizes: map[string]int{"d1": dedicated},
					DedicatedMinimums:  map[string]int{"d1": min},
					DedicatedCeilings:  map[string]int{"d1": ceiling},
				}
				out := jointlyNormalizeNonExclusiveTargets(in)

				controlledBefore := target + dedicated
				controlledAfter := out.ReclaimSize + out.DedicatedSizes["d1"]
				require.Equalf(t, controlledBefore, controlledAfter,
					"partition must stay size-conserved: w=%d target=%d reserved=%d", w, target, reserved)
				require.GreaterOrEqualf(t, out.ReclaimSize, reserved,
					"reclaim must not drop below committed anchor")
				require.GreaterOrEqualf(t, out.DedicatedSizes["d1"], min,
					"dedicated must respect its minimum")
				require.LessOrEqualf(t, out.DedicatedSizes["d1"], ceiling,
					"dedicated must respect its ceiling")
				if out.Changed {
					require.Equalf(t, 0, out.ReclaimSize%w,
						"a changed reclaim target must be whole-core aligned")
				}
			}
		}
	}
}

// A missing eligibility-domain ceiling must not be guessed: pass through and let
// QRM own whole-core materialization (covers the unknown-ceiling branch).
func TestJointlyNormalizeNonExclusiveTargets_MissingCeilingPassthrough(t *testing.T) {
	t.Parallel()

	in := nonExclusiveJointInput{
		CPUsPerCore:        2,
		ReclaimTarget:      3,
		ReservedForReclaim: 0,
		DedicatedPoolSizes: map[string]int{"d1": 5},
		DedicatedMinimums:  map[string]int{"d1": 1},
		// DedicatedCeilings intentionally absent.
	}
	out := jointlyNormalizeNonExclusiveTargets(in)
	require.False(t, out.Changed)
	require.Equal(t, 3, out.ReclaimSize)
	require.Equal(t, nonExclusiveJointPassthrough, out.Decision)
}

// -----------------------------------------------------------------------------
// Boundary coverage: cpuCountInNUMAs / reclaimRatioCPUsPerCore / registry.
// -----------------------------------------------------------------------------

func TestCPUCountInNUMAs_NoTopologyIsZero(t *testing.T) {
	t.Parallel()
	pa := &ProvisionAssemblerCommon{}
	require.Equal(t, 0, pa.cpuCountInNUMAs(machine.NewCPUSet(0, 1)))
}

func TestReclaimRatioCPUsPerCore_RampInactiveIsOne(t *testing.T) {
	t.Parallel()
	pa := &ProvisionAssemblerCommon{}
	got, err := pa.reclaimRatioCPUsPerCore(ProvisionContext{}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

func TestReclaimRatioCPUsPerCore_RampActiveMissingTopologyErrors(t *testing.T) {
	t.Parallel()
	capMap := map[int]int{0: 4}
	pa := &ProvisionAssemblerCommon{}
	ctx := ProvisionContext{
		Snapshot: Snapshot{RampUpReclaimCPUSetCap: capMap, RampUpDomains: []int{0}},
	}
	_, err := pa.reclaimRatioCPUsPerCore(ctx, 0)
	require.Error(t, err)
}

func TestReclaimRatioCPUsPerCore_RampActiveNoCapEntryFallsBack(t *testing.T) {
	t.Parallel()
	capMap := map[int]int{1: 4} // numa 0 has no cap entry
	pa := &ProvisionAssemblerCommon{}
	ctx := ProvisionContext{
		Snapshot: Snapshot{RampUpReclaimCPUSetCap: capMap, RampUpDomains: []int{1}},
	}
	got, err := pa.reclaimRatioCPUsPerCore(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// The registry helpers are trivial but were at 0% coverage; exercise the real
// RegisterInitializer/GetRegisteredInitializers surface directly.
func TestRegisteredInitializersRoundTrip(t *testing.T) {
	t.Parallel()

	name := types.CPUProvisionAssemblerName("safety-net-registry-probe")
	RegisterInitializer(name, func(*config.Configuration, interface{}, metacache.MetaReader, *metaserver.MetaServer, metrics.MetricEmitter) ProvisionAssembler {
		return nil
	})

	all := GetRegisteredInitializers()
	_, ok := all[name]
	require.True(t, ok, "registered initializer must be retrievable")
}
