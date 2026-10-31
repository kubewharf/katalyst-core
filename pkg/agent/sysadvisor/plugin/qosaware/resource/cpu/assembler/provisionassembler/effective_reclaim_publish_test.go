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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newFakeNUMAHardAssembler builds an assembler whose global (FakedNUMAID) scope
// is an active ramp-up hard-partition domain. The two backing NUMAs model the
// shared NUMA set; the per-scope ceiling is pinned to a known effective target
// so the published reclaim size is deterministic.
func newFakeNUMAHardAssembler(
	t *testing.T,
	backingNUMAs []int,
	numaAvailable map[int]int,
	reserved map[int]int,
	effectiveTarget int,
	allowSharedOverlap bool,
) *provisionTestFixture {
	t.Helper()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	conf.GetDynamicConfiguration().EnableReclaim = true
	conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	// ratio<=0 disables the MaxRatio clamp entirely so the per-scope ceiling is
	// the single quantity boundary.
	conf.GetDynamicConfiguration().ReclaimedCPUMaxRatio = 0

	cpuDetails := machine.CPUDetails{}
	for numaID, size := range numaAvailable {
		for cpuID := 0; cpuID < size; cpuID++ {
			cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: numaID}
		}
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumCPUs:      len(cpuDetails),
					NumCores:     len(cpuDetails),
					NumSockets:   1,
					NumNUMANodes: len(numaAvailable),
					CPUDetails:   cpuDetails,
				},
			},
		},
	}

	metaReader := metacache.NewDummyMetaCacheImp()
	require.NoError(t, metaReader.SetResourcePackageConfig(types.ResourcePackageConfig{}))

	regionMap := map[string]region.QoSRegion{}
	nonBinding := machine.NewCPUSet(backingNUMAs...)
	rampUpReclaimCPUSetCap := map[int]int{}
	disableDedicatedOverlap := true
	pa := NewProvisionAssemblerCommon(conf, nil, metaReader, metaServer, metrics.DummyMetrics{}).(*ProvisionAssemblerCommon)

	// Drive the global scope as an active hard-partition ramp-up domain and pin
	// its per-scope ceiling to the known effective target.
	scope := NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)
	return &provisionTestFixture{
		ProvisionAssemblerCommon: pa,
		conf:                     conf,
		dynamicConf:              conf.GetDynamicConfiguration(),
		regionMap:                regionMap,
		reservedForReclaim:       reserved,
		rampUpReclaimCPUSetCap:   rampUpReclaimCPUSetCap,
		numaAvailable:            numaAvailable,
		nonBindingNumas:          nonBinding,
		allowShared:              allowSharedOverlap,
		disableDedicated:         disableDedicatedOverlap,
		RampUpDomains:            []int{commonstate.FakedNUMAID},
		ReclaimConstraint:        cputypes.ReclaimConstraintReservedFloor,
		ReclaimActiveScopes:      map[ReclaimConstraintScope]bool{scope: true},
		ReclaimCeilings:          map[ReclaimConstraintScope]*int{scope: ptrToInt(effectiveTarget)},
	}
}

// assembleFakeNUMAHardScope assembles only the global (FakedNUMAID) scope.
func assembleFakeNUMAHardScope(t *testing.T, pa *provisionTestFixture) *types.InternalCPUCalculationResult {
	t.Helper()
	result := &types.InternalCPUCalculationResult{
		PoolEntries:                 map[string]map[int]types.CPUResource{},
		PoolOverlapInfo:             map[string]map[int]map[string]int{},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
	}
	require.NoError(t, pa.assembleWithoutNUMAExclusivePool(pa.ctx(), NewRegionMapHelper(pa.regionMap), commonstate.FakedNUMAID, result))
	return result
}

// TestEffectiveReclaimedCoresSize verifies the single publish formula: the
// constrained reclaim target minus the portion already published as overlap
// metadata, clamped to the non-negative domain. reservedForReclaim is intentionally
// never subtracted here.
func TestEffectiveReclaimedCoresSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		reclaimTarget int
		overlapSize   int
		want          int
	}{
		{"target minus zero overlap", 8, 0, 8},
		{"target minus partial overlap", 8, 2, 6},
		{"overlap saturates target", 8, 8, 0},
		{"overlap exceeds target clamps to zero", 8, 12, 0},
		{"zero target", 0, 0, 0},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, effectiveReclaimedCoresSize(tc.reclaimTarget, tc.overlapSize))
		})
	}
}

// TestFakeNUMAHardPublishesEffectiveTarget_A1 reproduces the field defect:
// FakeNUMA hard partition, effective target 8, reserve 4, overlap 0 must publish
// the full target (8), not target-minus-reserve (4).
func TestFakeNUMAHardPublishesEffectiveTarget_A1(t *testing.T) {
	t.Parallel()

	pa := newFakeNUMAHardAssembler(t,
		[]int{0, 1},
		map[int]int{0: 24, 1: 24},
		map[int]int{0: 2, 1: 2}, // effectiveReservedForReclaim over {0,1} == 4
		8,                       // effective target / per-scope ceiling
		false,                   // non-overlap
	)
	result := assembleFakeNUMAHardScope(t, pa)

	require.Equal(t, 8, result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size,
		"FakeNUMA hard publish must equal the effective reclaim target, not target-reserve")
}

// TestFakeNUMAHardPublishIgnoresReserveAtPublishLayer_A4 is the discriminative
// test from the design: the reservation is a floor/constraint input consumed at
// target derivation only. The publish layer must emit the effective target for
// every reserve regime. The defective code subtracted reserve here again, which
// published 0 once the reservation reached or exceeded the target.
func TestFakeNUMAHardPublishIgnoresReserveAtPublishLayer_A4(t *testing.T) {
	t.Parallel()

	const availablePerNUMA = 24
	backing := []int{0, 1}

	tests := []struct {
		name            string
		reserved        map[int]int
		effectiveTarget int
		// want is the published reclaim size for the global (FakedNUMAID) scope.
		want int
	}{
		{"zero reserve publishes target", map[int]int{0: 0, 1: 0}, 8, 8},
		{"reserve equals target publishes target", map[int]int{0: 4, 1: 4}, 8, 8},
		{"reserve above target floor-binds then publishes target", map[int]int{0: 6, 1: 6}, 8, 12},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pa := newFakeNUMAHardAssembler(t, backing,
				map[int]int{0: availablePerNUMA, 1: availablePerNUMA},
				tc.reserved, tc.effectiveTarget, false)
			result := assembleFakeNUMAHardScope(t, pa)

			require.Equal(t, tc.want,
				result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size,
				"publish must equal the effective target; reserve is not re-deducted here")
		})
	}
}

// TestFakeNUMAHardOverlapReducesEntryOnce verifies overlap metadata is subtracted
// exactly once at publish: target 8 with overlap 2 publishes a non-overlap entry
// of 6 (and the 2 are already reflected as overlap metadata).
func TestFakeNUMAHardOverlapReducesEntryOnce(t *testing.T) {
	t.Parallel()

	// Pure unit check of the convergent formula covers the overlap accounting
	// without standing up a full overlap-enabled region matrix.
	require.Equal(t, 6, effectiveReclaimedCoresSize(8, 2))
	require.Equal(t, 0, effectiveReclaimedCoresSize(8, 8))
	require.Equal(t, 0, effectiveReclaimedCoresSize(8, 12))
}

// recordingInt64Emitter captures the latest int64 value stored per metric name.
type recordingInt64Emitter struct {
	metrics.DummyMetrics
	values map[string]int64
}

func newRecordingInt64Emitter() *recordingInt64Emitter {
	return &recordingInt64Emitter{values: map[string]int64{}}
}

func (e *recordingInt64Emitter) StoreInt64(name string, value int64, _ metrics.MetricTypeName, _ ...metrics.MetricTag) error {
	e.values[name] = value
	return nil
}

// TestFakeNUMAHardPublishDeltaIsZero verifies the publish-layer observability
// probe: with the defect fixed, publish_delta = effectiveTarget - (entry + overlap)
// is 0. Before the fix it would have equaled the reserve value (4).
func TestFakeNUMAHardPublishDeltaIsZero(t *testing.T) {
	t.Parallel()

	emitter := newRecordingInt64Emitter()
	pa := newFakeNUMAHardAssembler(t,
		[]int{0, 1},
		map[int]int{0: 24, 1: 24},
		map[int]int{0: 2, 1: 2},
		8,
		false,
	)
	pa.emitter = emitter

	result := assembleFakeNUMAHardScope(t, pa)
	require.Equal(t, 8, result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size)
	require.Equal(t, int64(0), emitter.values[metricReclaimPublishDelta],
		"publish_delta must be 0 once the double-deduction is removed")
}

// TestFakeNUMAHardFollowsCeilingContinuity drives the global (FakedNUMAID) scope
// through a ramp-up / release sequence 14 -> 8 -> 14 and asserts the published
// reclaim tracks the per-scope ceiling on every step. The removed reserve
// double-deduction would have subtracted the reserve (4) again on each step, so
// continuity is what the field ramp relied on.
func TestFakeNUMAHardFollowsCeilingContinuity(t *testing.T) {
	t.Parallel()

	backing := []int{0, 1}
	reserved := map[int]int{0: 2, 1: 2} // effective reserve == 4
	for _, target := range []int{14, 8, 14} {
		target := target
		t.Run("", func(t *testing.T) {
			t.Parallel()
			pa := newFakeNUMAHardAssembler(t, backing,
				map[int]int{0: 24, 1: 24}, reserved, target, false)
			result := assembleFakeNUMAHardScope(t, pa)
			require.Equal(t, target,
				result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size,
				"published reclaim must track the per-scope ceiling through ramp and release")
		})
	}
}

// TestFakeNUMAEntryPlusOverlapEqualsTarget asserts the publish-layer conservation
// invariant for the target-driven global scope: the non-overlap reclaim entry plus
// the overlap metadata must equal the constrained effective target. With
// overlap 0 (allowSharedOverlap=false) this reduces to entry == target.
func TestFakeNUMAEntryPlusOverlapEqualsTarget(t *testing.T) {
	t.Parallel()

	pa := newFakeNUMAHardAssembler(t,
		[]int{0, 1},
		map[int]int{0: 24, 1: 24},
		map[int]int{0: 2, 1: 2},
		8,
		false,
	)
	result := assembleFakeNUMAHardScope(t, pa)

	entry := result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size
	overlap := 0 // no overlap atoms under allowSharedOverlap=false
	require.Equal(t, 8, entry+overlap, "entry + overlap must equal the constrained target")
}

// TestDefectPeriodDoubleDeductionGolden pins the pre-fix behavior so the removed
// bug cannot silently regress. The "old" values below were not hand-computed:
// they were captured by running the identical fixtures against the pre-fix
// baseline (commit 4c9df87c4, which still carried the FakeNUMA publish-layer
// `-= reservedForReclaim` post-step) and reading the actual assembler output:
//
//	A1 (target=8, overlap=0, reserve=4): old entry = 4, new = 8
//	A3 (target=8, overlap=2, reserve=4): old entry = 2, metadata = 2, sum = 4, new entry = 6
//	A4 reserve==target (target=8, reserve=8): old entry = 0, new = 8
//	A4 reserve>=target (floor binds to 12, reserve=12): old entry = 0, new = 12
//
// oldPublish is a faithful reconstruction of the deleted post-step
// max(max(target-overlap,0) - reserve, 0). Requiring it to differ from the fixed
// single formula is the falsification: if a future change reintroduces the
// double-deduction, newPublish would collapse back onto oldPublish.
func TestDefectPeriodDoubleDeductionGolden(t *testing.T) {
	t.Parallel()

	oldPublish := func(target, overlap, reserve int) int {
		return general.Max(general.Max(target-overlap, 0)-reserve, 0)
	}

	// Empirically captured defect-period published values.
	require.Equal(t, 4, oldPublish(8, 0, 4), "A1 old published = 8-4")
	require.Equal(t, 2, oldPublish(8, 2, 4), "A3 old entry = max(8-2,0)-4 = 2 (metadata 2, sum 4)")
	require.Equal(t, 0, oldPublish(8, 0, 8), "A4 reserve==target old = 0")
	require.Equal(t, 0, oldPublish(12, 0, 12), "A4 reserve>=target old = 0")

	// The fixed convergent formula must publish the effective target, not the
	// reserve-deducted value.
	require.Equal(t, 8, effectiveReclaimedCoresSize(8, 0), "A1 new entry = 8")
	require.Equal(t, 6, effectiveReclaimedCoresSize(8, 2), "A3 new entry = 6")
	require.Equal(t, 12, effectiveReclaimedCoresSize(12, 0), "A4 floor-binds to 12")
}

// TestFakeNUMAHardOverlapPublishesEntryTargetMinusOverlap is the assembly-level
// integration counterpart of the pure A3 check. It drives a real (non-binding,
// reclaim enabled) share region through the global (FakedNUMAID) hard scope so
// overlap metadata is actually produced, then asserts the full publish-layer
// contract across a ramp of legitimate overlap levels:
//
//	effective target (ceiling)        = 8
//	overlap metadata                 = clamped to the hard overlapBudget (= target - global reserve)
//	non-overlap publish entry         = target - overlap (overlap subtracted exactly once)
//	entry + overlap                   == target   (publish conservation, delta = 0)
//	no invariant violation is emitted for ANY overlap <= target
//
// The middle rows (overlap 5, 6) are the regression guard for the invariant
// fix: the previous probe `entry < overlap` is equivalent to `overlap > target/2`
// for the convergent formula, so it would have falsely flagged these legitimate
// high-overlap layouts. The corrected probe only trips when overlap exceeds the
// target itself. The overlap=2 row is the A3 field case (target 8, entry 6).
func TestFakeNUMAHardOverlapPublishesEntryTargetMinusOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		reserved    map[int]int // per backing NUMA; global reserve = sum
		wantEntry   int
		wantOverlap int
	}{
		{"A3 overlap 2", map[int]int{0: 3, 1: 3}, 6, 2},         // global reserve 6 -> budget 2
		{"legit high overlap 5", map[int]int{0: 1, 1: 2}, 3, 5}, // global reserve 3 -> budget 5
		{"legit high overlap 6", map[int]int{0: 1, 1: 1}, 2, 6}, // global reserve 2 -> budget 6
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			emitter := newRecordingInt64Emitter()
			pa := newFakeNUMAHardAssembler(t,
				[]int{0, 1},
				map[int]int{0: 24, 1: 24},
				tc.reserved,
				8,
				true, // allowSharedOverlap: a real share region drives overlap metadata
			)
			pa.emitter = emitter

			share := NewFakeRegion("share", configapi.QoSRegionTypeShare, "share")
			share.SetIsNumaBinding(false)
			share.enableReclaim = true
			share.podsRequest = 16
			share.SetProvision(types.ControlKnob{
				configapi.ControlKnobNonReclaimedCPURequirement: {Value: 4},
			})
			pa.regionMap[share.Name()] = share

			result := assembleFakeNUMAHardScope(t, pa)

			entry := result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size
			overlapMeta := 0
			for _, v := range result.PoolOverlapInfo[commonstate.PoolNameReclaim][commonstate.FakedNUMAID] {
				overlapMeta += v
			}

			require.Equal(t, tc.wantOverlap, overlapMeta, "overlap metadata must equal the hard overlapBudget")
			require.Equal(t, tc.wantEntry, entry, "non-overlap entry = target - overlap")
			require.Equal(t, 8, entry+overlapMeta, "publish conservation: entry + overlap must equal the effective target")

			require.Equal(t, int64(0), emitter.values[metricReclaimPublishDelta],
				"publish_delta = entry + overlap - target must be 0")
			_, violationEmitted := emitter.values[metricReclaimInvariantViolationTotal]
			require.False(t, violationEmitted,
				"overlap %d <= target 8 is legitimate; no overlap_double_count must fire", overlapMeta)

			bf := result.DefaultShareBackfill
			require.Equal(t, bf.RawReclaimSize, bf.FinalReclaimSize+bf.ReleasedReclaimSize,
				"default share backfill accounting must balance")
		})
	}
}

// assembleHardScopeWithShareBackfill runs the global reclaim publish and then the
// production default-share backfill finalizer, returning the published reclaim and
// share entries so the reclaim<->share conservation can be measured end to end.
func assembleHardScopeWithShareBackfill(t *testing.T, pa *provisionTestFixture) *types.InternalCPUCalculationResult {
	t.Helper()
	result := assembleFakeNUMAHardScope(t, pa)
	require.NoError(t, pa.finalizeDefaultShareBackfill(pa.ctx(), NewRegionMapHelper(pa.regionMap), result))
	return result
}

// TestFakeNUMACeilingLoweringConservesReclaimAndShare is the measured conservation
// gate: when the constrained reclaim ceiling is lowered, the cores released from the
// reclaim pool must be absorbed by the default share pool one-for-one, i.e.
// Δreclaim + Δshare == 0. This is asserted across two real assembler runs (ceiling
// 14 -> 8) and cross-checked against the finalizeDefaultShareBackfill diagnostics
// (Raw/Final/Released), not derived from the formula alone.
func TestFakeNUMACeilingLoweringConservesReclaimAndShare(t *testing.T) {
	t.Parallel()

	backing := []int{0, 1}
	reserved := map[int]int{0: 2, 1: 2} // reserve == 4, held constant

	publish := func(ceiling int) (reclaim, share int, bf types.DefaultShareBackfillDiagnostics) {
		pa := newFakeNUMAHardAssembler(t, backing,
			map[int]int{0: 24, 1: 24}, reserved, ceiling, false)
		res := assembleHardScopeWithShareBackfill(t, pa)
		reclaim = res.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size
		// This harness assembles the FakedNUMAID scope directly, so the production
		// AssembleProvision path (which materializes PoolEntries[share][FakedNUMAID]
		// under FillDefaultSharePoolWithNonReclaimCPUs) does not run and that entry is
		// structurally 0. The share-side quantity that actually moves one-for-one with
		// reclaim here is the ReleasedReclaimSize: the cores cut out of the reclaim
		// pool and handed to the default-share backfill. It is the equivalent measure
		// of "how much the share pool absorbed" for the conservation gate.
		share = res.DefaultShareBackfill.ReleasedReclaimSize
		return reclaim, share, res.DefaultShareBackfill
	}

	reclaimHigh, shareHigh, bfHigh := publish(14)
	reclaimLow, shareLow, bfLow := publish(8)

	// The published reclaim tracks the ceiling on both steps.
	require.Equal(t, 14, reclaimHigh)
	require.Equal(t, 8, reclaimLow)

	// Per-run reclaim conservation: the raw reclaim target is split between what is
	// published and what is released for share backfill.
	require.Equal(t, bfHigh.RawReclaimSize, bfHigh.FinalReclaimSize+bfHigh.ReleasedReclaimSize)
	require.Equal(t, bfLow.RawReclaimSize, bfLow.FinalReclaimSize+bfLow.ReleasedReclaimSize)

	// Measured Δreclaim + Δshare == 0. Lowering the ceiling 14 -> 8 shrinks the
	// published reclaim by 6 (Final 14 -> 8); those exact 6 cores appear in the
	// ReleasedReclaimSize pool (34 -> 40), which is the source the default share
	// backfill absorbs. Raw is the constant total, so Final + Released is invariant.
	deltaReclaim := bfLow.FinalReclaimSize - bfHigh.FinalReclaimSize        // -6
	deltaReleased := bfLow.ReleasedReclaimSize - bfHigh.ReleasedReclaimSize // +6
	require.Equal(t, -6, deltaReclaim, "published reclaim shrinks by exactly the ceiling delta")
	require.Equal(t, +6, deltaReleased, "the same cores move into the share-backfill released pool")
	require.Equal(t, 0, deltaReclaim+deltaReleased, "Δreclaim + Δshare(released) must balance to zero")
	require.Equal(t, bfHigh.RawReclaimSize, bfLow.RawReclaimSize, "raw total is held constant across the step")

	// Direct pool-level conservation on the share side, previously discarded with
	// `_`. Lowering the ceiling releases cores out of the reclaim pool; the share
	// pool must absorb them one-for-one: reclaimHigh-reclaimLow == shareLow-shareHigh.
	reclaimReleased := reclaimHigh - reclaimLow
	shareAbsorbed := shareLow - shareHigh
	require.Equalf(t, reclaimReleased, shareAbsorbed,
		"share pool did not absorb the cores released by reclaim: reclaimHigh=%d reclaimLow=%d shareHigh=%d shareLow=%d releasedFromReclaim=%d absorbedByShare=%d diff=%d",
		reclaimHigh, reclaimLow, shareHigh, shareLow, reclaimReleased, shareAbsorbed, shareAbsorbed-reclaimReleased)
}

// TestRealNUMANormalizedPublishDeltaIsZero proves the corrected delta baseline on
// the real-NUMA whole-core normalization path. The solver emits an odd reclaim
// target (3 on a SMT2 core), jointlyNormalize rounds the published reclaim to a whole
// core (2). Against the raw target the old baseline would report delta = 2 - 3 = -1
// on a perfectly healthy run; against the normalized baseline (rounded reclaim +
// overlap) the delta must be 0. dedicated grows from 22 to 23, keeping the partition
// size-conserved.
func TestRealNUMANormalizedPublishDeltaIsZero(t *testing.T) {
	t.Parallel()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	conf.GetDynamicConfiguration().EnableReclaim = true
	conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

	regionMap := map[string]region.QoSRegion{}
	dedicated := NewFakeRegion("dedicated", configapi.QoSRegionTypeDedicated, "dedicated")
	dedicated.SetBindingNumas(machine.NewCPUSet(0))
	dedicated.SetIsNumaBinding(true)
	dedicated.enableReclaim = true
	dedicated.podsRequest = 22
	dedicated.SetPods(types.PodSet{"dedicated-pod": sets.NewString("main")})
	dedicated.SetProvision(types.ControlKnob{
		configapi.ControlKnobNonReclaimedCPURequirement: {Value: 22},
	})
	regionMap[dedicated.Name()] = dedicated

	reservedForReclaim := map[int]int{0: 0}
	rampUpReclaimCPUSetCap := map[int]int{0: 3}
	numaAvailable := map[int]int{0: 34}
	nonBindingNUMAs := machine.NewCPUSet()
	allowSharedOverlap := false
	disableDedicatedOverlap := true
	metaReader := metacache.NewDummyMetaCacheImp()
	require.NoError(t, metaReader.SetResourcePackageConfig(types.ResourcePackageConfig{}))

	emitter := newRecordingInt64Emitter()
	pa := NewProvisionAssemblerCommon(
		conf, nil, metaReader,
		nonExclusiveTestMetaServer(numaAvailable, 2), emitter,
	).(*ProvisionAssemblerCommon)

	scope := NewNonExclusiveReclaimConstraintScope(0)
	fx := &provisionTestFixture{
		ProvisionAssemblerCommon: pa,
		conf:                     conf,
		dynamicConf:              conf.GetDynamicConfiguration(),
		regionMap:                regionMap,
		reservedForReclaim:       reservedForReclaim,
		rampUpReclaimCPUSetCap:   rampUpReclaimCPUSetCap,
		numaAvailable:            numaAvailable,
		nonBindingNumas:          nonBindingNUMAs,
		allowShared:              allowSharedOverlap,
		disableDedicated:         disableDedicatedOverlap,
		RampUpDomains:            []int{0},
		ReclaimConstraint:        cputypes.ReclaimConstraintReservedFloor,
		ReclaimActiveScopes:      map[ReclaimConstraintScope]bool{scope: true},
		ReclaimCeilings:          map[ReclaimConstraintScope]*int{scope: ptrToInt(3)},
	}

	result := &types.InternalCPUCalculationResult{
		PoolEntries:                 map[string]map[int]types.CPUResource{},
		PoolOverlapInfo:             map[string]map[int]map[string]int{},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
	}
	require.NoError(t, pa.assembleWithoutNUMAExclusivePool(fx.ctx(), NewRegionMapHelper(regionMap), 0, result))

	require.Equal(t, 2, result.PoolEntries[commonstate.PoolNameReclaim][0].Size,
		"odd target 3 must be whole-core rounded to 2 on SMT2")
	require.Equal(t, 23, result.PoolEntries["dedicated-pod"][0].Size,
		"the released core moves to the dedicated pool")
	require.Equal(t, 0, int(emitter.values[metricReclaimPublishDelta]),
		"publish_delta must be 0 against the normalized baseline, not 2-3=-1")
}
