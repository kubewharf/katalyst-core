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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newReclaimFallback180Policy builds a policy on the real 180-CPU, 2-NUMA,
// adjacent-SMT fixture (NUMA0=0-89, NUMA1=90-179) via the without-init test
// harness. It deliberately does NOT run the production constructor (the bootstrap
// suite owns that); it only materializes the policy object so the real
// reviseReclaimPool fallback path can be exercised end to end.
//
//   - reservedCPUs: system/reserved CPUs (caller passes empty for the zero-reserve corner).
//   - seed: the cached default reclaim preference (default seed 0-1/90-91 in the corner).
//   - seedAssign: the per-NUMA slice of seed.
func newReclaimFallback180Policy(
	t *testing.T,
	reservedCPUs machine.CPUSet,
	seed machine.CPUSet,
	seedAssign map[int]machine.CPUSet,
) (*DynamicPolicy, func()) {
	t.Helper()
	topo := contiguous180CPUTopology(t)
	tmpDir, err := ioutil.TempDir("", "reclaim-fallback-180")
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topo, tmpDir)
	require.NoError(t, err)

	p.config.reservedCPUs = reservedCPUs
	p.reclaim.reservedReclaimedCPUSet = seed.Clone()
	p.reclaim.reservedReclaimedCPUsSize = seed.Size()
	p.reclaim.reservedReclaimedTopologyAwareAssignments = seedAssign

	return p, func() { _ = os.RemoveAll(tmpDir) }
}

// seed180Default is the default reclaim preference: one core on each NUMA.
func seed180Default() (machine.CPUSet, map[int]machine.CPUSet) {
	assign := map[int]machine.CPUSet{
		0: machine.MustParse("0-1"),
		1: machine.MustParse("90-91"),
	}
	return machine.MustParse("0-1,90-91"), assign
}

// seedCommittedPrior180 commits a reclaim identity distinct from the default seed
// (88-89 on NUMA0, 178-179 on NUMA1) into the currently published state so the
// fallback can prefer it.
func seedCommittedPrior180(t *testing.T, p *DynamicPolicy) machine.CPUSet {
	t.Helper()
	prior := machine.MustParse("88-89,178-179")
	priorAssign := map[int]machine.CPUSet{
		0: machine.MustParse("88-89"),
		1: machine.MustParse("178-179"),
	}
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:         prior.Clone(),
			OriginalAllocationResult: prior.Clone(),
			TopologyAwareAssignments: priorAssign,
		}, false))
	return prior
}

func emptyReclaimEntries() state.PodEntries {
	return make(state.PodEntries)
}

// TestReviseReclaimPoolFallbackPrefersCommittedPrior180 verifies that when the
// advisor omits its reclaim block, the bounded fallback keeps the already
// committed reclaim identity (88-89/178-179) rather than re-seeding the default
// preference (0-1/90-91), and stays bounded (4 CPUs, not the whole 180).
func TestReviseReclaimPoolFallbackPrefersCommittedPrior180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	prior := seedCommittedPrior180(t, p)

	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	// The advisor candidate is the local newEntries; the committed prior is read
	// from the published state inside reviseReclaimPool.
	entries := emptyReclaimEntries()
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))

	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(prior),
		"committed prior %s must be preferred over default seed %s, got %s", prior, seed, got)
	require.Equal(t, 4, got.Size(), "bounded to want(=seed size 4), not whole machine")
	require.True(t, got.IsSubsetOf(p.machine.machineInfo.CPUDetails.CPUs()), "never exceed all cpus")
}

// TestReviseReclaimPoolFallbackBoundedNotWholeMachine180 verifies that with no
// committed prior and a zero-reserved, full-capacity machine, the legacy
// fallback selects exactly the bounded default seed (4 CPUs), never the whole
// 180-CPU machine.
func TestReviseReclaimPoolFallbackBoundedNotWholeMachine180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	entries := emptyReclaimEntries()
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))

	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, seed.Size(), got.Size(), "bounded to want=%d, got %d", seed.Size(), got.Size())
	require.False(t, got.Equals(p.machine.machineInfo.CPUDetails.CPUs()), "must never take the whole machine")
	require.True(t, got.Equals(seed), "no committed prior -> default seed selected, got %s want %s", got, seed)
}

// TestReviseReclaimPoolFallbackStrictShortfallHardFailNoWrite180 verifies that in
// strict disjoint mode, when the eligible set cannot satisfy the requested size,
// the fallback hard-fails and writes nothing (no reclaim entry materialized).
func TestReviseReclaimPoolFallbackStrictShortfallHardFailNoWrite180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	// Reserve everything but the first core (0-1): only 2 eligible CPUs vs want 4.
	p, cleanup := newReclaimFallback180Policy(t, machine.MustParse("2-179"), seed, seedAssign)
	defer cleanup()

	strict := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
	entries := emptyReclaimEntries()
	err := p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), strict)
	require.ErrorIs(t, err, errReclaimFallbackCapacityInsufficient)
	require.True(t, entries.CheckPoolEmpty(commonstate.PoolNameReclaim),
		"strict shortfall must not materialize a reclaim entry")
}

// TestReviseReclaimPoolFallbackLegacyShortfallReturnsBounded180 verifies that
// outside strict mode the same shortfall returns a bounded partial selection
// instead of failing closed.
func TestReviseReclaimPoolFallbackLegacyShortfallReturnsBounded180(t *testing.T) {
	t.Parallel()
	// want stays 4 (seed size), but only NUMA0 carries a reserved preference so the
	// NUMA1 RNB backfill finds no target and safely leaves the bucket absent instead
	// of failing closed.
	seed := machine.MustParse("0-1,90-91")
	seedAssign := map[int]machine.CPUSet{0: machine.MustParse("0-1")}
	p, cleanup := newReclaimFallback180Policy(t, machine.MustParse("2-179"), seed, seedAssign)
	defer cleanup()

	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	entries := emptyReclaimEntries()
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))

	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, 2, got.Size(), "only the single eligible core (0-1) can be returned")
	require.True(t, got.Equals(machine.MustParse("0-1")))
}

// TestReviseReclaimPoolFallbackReclaimDemandTable180 drives the revise-only fallback
// path at reclaim-demand sizes 176/180/184/192/198 on a 180-CPU machine. This varies
// the RECLAIM demand (reservedReclaimedCPUsSize), not the share load; the real share
// full-capacity table is TestRealShareLoadTable180. Non-strict legacy must never return
// more than the 180 eligible CPUs; strict disjoint must hard-fail once want exceeds it.
func TestReviseReclaimPoolFallbackReclaimDemandTable180(t *testing.T) {
	t.Parallel()
	for _, want := range []int{176, 180, 184, 192, 198} {
		want := want
		t.Run(wantLabel(want), func(t *testing.T) {
			t.Parallel()
			seed, seedAssign := seed180Default()
			p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
			defer cleanup()

			p.reclaim.reservedReclaimedCPUsSize = want

			eligible := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs)

			legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
			entries := emptyReclaimEntries()
			require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))
			got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
			require.LessOrEqual(t, got.Size(), eligible.Size(),
				"non-strict must never exceed eligible %d, got %d", eligible.Size(), got.Size())
			require.True(t, got.IsSubsetOf(eligible), "selection must be a subset of eligible")

			strict := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
			strictEntries := emptyReclaimEntries()
			strictErr := p.reviseReclaimPool(strictEntries, machine.NewCPUSet(), machine.NewCPUSet(), strict)
			if want > eligible.Size() {
				require.ErrorIs(t, strictErr, errReclaimFallbackCapacityInsufficient,
					"strict must hard-fail when want %d exceeds eligible %d", want, eligible.Size())
				require.True(t, strictEntries.CheckPoolEmpty(commonstate.PoolNameReclaim))
			} else {
				require.NoError(t, strictErr)
				require.Equal(t, want, strictEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.Size())
			}
		})
	}
}

// TestApplyPoolsRejectsEmptyEligibleNoWrite180 drives the real applyPoolsAndIsolatedInfo
// path (buildPoolEntries → reviseReclaimPool → 3730 fallback) on a 180-CPU machine where
// every CPU is reserved, so the hard-eligible set is empty. With want=4 the fallback must
// return errReclaimFallbackEmptyRequired and must not mutate committed state (revision,
// entries, machine state). This is the B no-write negative case exercised through the real
// apply entry point, not a standalone validator call.
func TestApplyPoolsRejectsEmptyEligibleNoWrite180(t *testing.T) {
	t.Parallel()
	topo := contiguous180CPUTopology(t)
	tmpDir, err := ioutil.TempDir("", "reclaim-fallback-empty-180")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	p, err := getTestDynamicPolicyWithoutInitialization(topo, tmpDir)
	require.NoError(t, err)

	// Reserve every CPU so the hard-eligible set (all - reserved - forbidden/system) is empty.
	p.config.reservedCPUs = p.machine.machineInfo.CPUDetails.CPUs().Clone()
	p.reclaim.reservedReclaimedCPUSet = machine.MustParse("0-1,90-91")
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.MustParse("0-1"), 1: machine.MustParse("90-91"),
	}
	require.NoError(t, p.state.StoreState())

	beforeRevision := p.state.GetRevision()
	beforeEntries := p.state.GetPodEntries()
	beforeMachineState := p.state.GetMachineState()

	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: machine.NewCPUSet(),
		commonstate.PoolNameShare:   machine.NewCPUSet(),
	}
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true

	applyErr := p.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		sets.NewInt(),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		dynamicConf,
	)
	require.ErrorIs(t, applyErr, errReclaimFallbackEmptyRequired)

	// No committed state mutation.
	require.Equal(t, beforeRevision, p.state.GetRevision(), "revision must not advance on empty-eligible reject")
	require.Equal(t, beforeEntries, p.state.GetPodEntries(), "pod entries must not be written")
	require.Equal(t, beforeMachineState, p.state.GetMachineState(), "machine state must not be written")
}

func wantLabel(want int) string {
	switch want {
	case 176:
		return "share176"
	case 180:
		return "share180"
	case 184:
		return "share184"
	case 192:
		return "share192"
	case 198:
		return "share198"
	default:
		return ""
	}
}

// TestRealShareLoadTable180 drives the REAL production pipeline
// (generatePoolsAndIsolation -> applyPoolsAndIsolatedInfo -> commit) on the 180-CPU
// fixture at share-load points 176/178/180/184/192/198. The reclaim target is FIXED at
// want=4 (never varied with share), and the committed reclaim prior is 88-89/178-179.
// enableReclaim=false, allowSharedOverlap=false, disableDedicatedOverlap=false. It
// asserts the reclaim pool stays bounded (never the whole machine), the revision
// advances on commit, and the raw committed reclaim entry does not balloon into the
// share residual. This is a real share-capacity load, not a reclaim-demand table.
func TestRealShareLoadTable180(t *testing.T) {
	t.Parallel()
	// Per-point contract: share materializes min(request, 180) (the existing all-
	// occupancy capacity floor; it must not silently shrink to 176). Reclaim is fixed
	// want=4: wherever the leftover lets the prior identity through, committed reclaim
	// is exactly 88-89,178-179. At share=178 the machine only leaves 2 free CPUs after
	// share takes 178, so the legacy enable=false leftover path yields a partial 2-CPU
	// reclaim (178-179) — explicitly expected, not a silent green.
	type expect struct {
		shareSize   int
		reclaimSize int
	}
	table := map[int]expect{
		176: {shareSize: 176, reclaimSize: 4},
		178: {shareSize: 178, reclaimSize: 2}, // legacy leftover partial, documented
		180: {shareSize: 180, reclaimSize: 4},
		184: {shareSize: 180, reclaimSize: 4},
		192: {shareSize: 180, reclaimSize: 4},
		198: {shareSize: 180, reclaimSize: 4},
	}
	prior := machine.MustParse("88-89,178-179")
	for _, shareQty := range []int{176, 178, 180, 184, 192, 198} {
		shareQty := shareQty
		exp := table[shareQty]
		t.Run(wantLabel(shareQty), func(t *testing.T) {
			t.Parallel()
			seed, seedAssign := seed180Default()
			p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
			defer cleanup()

			seedCommittedPrior180(t, p)
			p.reclaim.reservedReclaimedCPUsSize = 4

			dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = false
			require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
			require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, true))

			beforeRevision := p.state.GetRevision()
			allCPUs := p.machine.machineInfo.CPUDetails.CPUs()

			poolsCPUSet, isolatedCPUSet, err := p.generatePoolsAndIsolation(
				map[string]map[int]int{
					commonstate.PoolNameShare: {commonstate.FakedNUMAID: shareQty},
				},
				map[string]map[string]int{},
				allCPUs.Clone(),
				map[string]float64{},
				generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dynamicConf},
			)
			require.NoError(t, err)

			generatedReclaim := poolsCPUSet[commonstate.PoolNameReclaim]
			t.Logf("shareQty=%d generatedReclaim=%s size=%d", shareQty, generatedReclaim.String(), generatedReclaim.Size())
			require.LessOrEqual(t, generatedReclaim.Size(), 4,
				"reclaim must stay bounded at want=4, shareQty=%d got %d", shareQty, generatedReclaim.Size())
			require.False(t, generatedReclaim.Equals(allCPUs), "reclaim must never expand to the whole machine")

			applyErr := p.applyPoolsAndIsolatedInfo(
				poolsCPUSet, isolatedCPUSet, p.state.GetPodEntries(), p.state.GetMachineState(),
				sets.NewInt(), false, machine.NewCPUSet(), defaultShareMaterializationPlan{},
				p.state.GetRevision(), dynamicConf)
			require.NoError(t, applyErr)

			committed, cErr := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameReclaim)
			require.NoError(t, cErr)
			committedShare, _ := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameShare)
			t.Logf("shareQty=%d committedReclaim=%s size=%d committedShareSize=%d",
				shareQty, committed.String(), committed.Size(), committedShare.Size())

			require.Equal(t, exp.shareSize, committedShare.Size(),
				"share actual must take min(request,180) without silent shrink, shareQty=%d", shareQty)
			require.Equal(t, exp.reclaimSize, committed.Size(),
				"committed reclaim must match the expected contract size, shareQty=%d", shareQty)
			require.False(t, committed.Equals(allCPUs), "committed reclaim must never be the whole machine")
			if exp.reclaimSize == prior.Size() {
				require.True(t, prior.IsSubsetOf(committed),
					"legal committed prior %s must be preferred, got %s", prior, committed)
			}
			require.Greater(t, p.state.GetRevision(), beforeRevision, "revision must advance on commit")
		})
	}
}

// TestReviseReclaimPoolStrictExcludesAllSharePools180 verifies that in strict disjoint
// mode the fallback subtracts EVERY share-type pool in the candidate — the default
// share pool AND a custom share-NUMA-binding pool — not only the default share entry.
func TestReviseReclaimPoolStrictExcludesAllSharePools180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()
	prior := seedCommittedPrior180(t, p)

	strict := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
	// The candidate already allocates most of the machine to the default share pool and
	// to a custom share-NUMA-binding pool. Strict reclaim must not borrow from either.
	entries := state.PodEntries{
		commonstate.PoolNameShare: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.MustParse("4-87"),
			},
		},
		"custom-NUMA1": state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("custom-NUMA1"),
				AllocationResult: machine.MustParse("92-177"),
			},
		},
	}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), strict))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, prior.IsSubsetOf(got), "prior identity must be kept: got %s", got)
	shareOwned := machine.MustParse("4-87").Union(machine.MustParse("92-177"))
	require.True(t, got.Intersection(shareOwned).IsEmpty(),
		"strict reclaim must not borrow any share-type pool, got %s", got)
	require.Equal(t, 4, got.Size(), "strict reclaim must fill want=4, got %d", got.Size())
}

// TestReviseReclaimPoolDedicatedExcludedIndependentOfStrict180 pins the C gap: with
// allowSharedOverlap=true AND disableDedicatedOverlap=true, strict() is false, so the
// old strict-only subtraction left dedicated borrowable. Dedicated overlap must
// STILL be forbidden while shared overlap stays permitted.
func TestReviseReclaimPoolDedicatedExcludedIndependentOfStrict180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	// share overlap permitted, dedicated overlap disabled; strict() == false.
	mode := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: true}
	dedicated := machine.MustParse("8-177")
	entries := state.PodEntries{
		commonstate.PoolNameDedicated: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameDedicated),
				AllocationResult: dedicated.Clone(),
			},
		},
		commonstate.PoolNameShare: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.MustParse("4-5"),
			},
		},
	}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), mode))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Intersection(dedicated).IsEmpty(),
		"dd=true must exclude dedicated even when allowShare=true/strict=false, got %s", got)
	require.Equal(t, 4, got.Size(), "reclaim must still fill want=4 from the allowed leftover, got %d", got.Size())
}

// TestReviseReclaimPoolStrictShareOverCapacityNoWrite180 verifies that in strict mode
// when the share pool leaves fewer than a whole core-aligned reclaim can form, the
// fallback hard-fails and writes nothing.
func TestReviseReclaimPoolStrictShareOverCapacityNoWrite180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()
	seedCommittedPrior180(t, p)

	strict := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
	// Share owns 2-179; only CPUs 0-1 (one core) remain, below the want=4 requirement.
	entries := state.PodEntries{
		commonstate.PoolNameShare: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.MustParse("2-179"),
			},
		},
	}
	err := p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), strict)
	require.ErrorIs(t, err, errReclaimFallbackCapacityInsufficient)
	require.True(t, entries.CheckPoolEmpty(commonstate.PoolNameReclaim), "strict shortfall must write no reclaim entry")
}

// TestReviseReclaimPoolImbalancedPriorNoInflation180 verifies E: a committed prior that
// piled all 4 reclaim CPUs onto NUMA0 must not, after the global pick, union the NUMA1
// default bucket on top (4 -> 6) when there is no active RNB pinned to NUMA1.
func TestReviseReclaimPoolImbalancedPriorNoInflation180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	prior := machine.MustParse("0-3")
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:         prior.Clone(),
			OriginalAllocationResult: prior.Clone(),
			TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.MustParse("0-3")},
		}, false))

	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	entries := emptyReclaimEntries()
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, 4, got.Size(), "imbalanced prior must not inflate past want=4, got %d", got.Size())
	require.True(t, prior.IsSubsetOf(got))
}

// TestReviseReclaimPoolGiantOldBucketClipped180 verifies E: an oversized committed
// reclaim bucket (8 CPUs) is clipped to the configured want=4 and the per-NUMA loop
// does not re-inflate it.
func TestReviseReclaimPoolGiantOldBucketClipped180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	giant := machine.MustParse("0-7")
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:         giant.Clone(),
			OriginalAllocationResult: giant.Clone(),
			TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.MustParse("0-7")},
		}, false))

	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	entries := emptyReclaimEntries()
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, 4, got.Size(), "giant committed bucket must be clipped to want=4, got %d", got.Size())
}

// nonEmptyReclaimOnNUMA0 builds a candidate whose reclaim pool is already materialized on
// NUMA0 only, so revise enters the per-NUMA backfill loop (not the global empty branch).
func nonEmptyReclaimOnNUMA0(t *testing.T, result string, numa0 string) state.PodEntries {
	t.Helper()
	assign := map[int]machine.CPUSet{0: machine.MustParse(numa0)}
	return state.PodEntries{
		commonstate.PoolNameReclaim: state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.MustParse(result),
				OriginalAllocationResult:         machine.MustParse(result),
				TopologyAwareAssignments:         assign,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assign),
			},
		},
	}
}

// TestRevisePerNUMADedicatedNotBorrowedWithDDSelf180 pins the per-NUMA closure bypass:
// reclaim already has NUMA0=0-1; NUMA1's reserved preference is 90-91 which is now owned
// by the dedicated pool. With allowSharedOverlap=true AND disableDedicatedOverlap=true
// (strict()==false) the old per-NUMA closure borrowed dedicated 90-91 anyway. The new
// closure excludes dedicated on every backfill and tops up from an alternative core.
func TestRevisePerNUMADedicatedNotBorrowedWithDDSelf180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	entries := nonEmptyReclaimOnNUMA0(t, "0-1", "0-1")
	entries[commonstate.PoolNameDedicated] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameDedicated),
			AllocationResult: machine.MustParse("90-91"),
		},
	}
	mode := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: true}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), mode))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Intersection(machine.MustParse("90-91")).IsEmpty(),
		"per-NUMA backfill must not borrow dedicated 90-91 even when allowShare=true/dd=true, got %s", got)
	require.Equal(t, 4, got.Size(), "must top up to want=4 from an alternative legal core, got %s", got)
}

// TestRevisePerNUMAIsolatedDedicatedNotBorrowedWithDDSelf180 mirrors the dd pool case but
// pins the isolated-dedicated CPUs as pooledUnionDedicatedCPUSet (not a pool entry): with
// allowSharedOverlap=true and disableDedicatedOverlap=true, dd must still subtract them on
// every per-NUMA backfill even though share overlap remains allowed.
func TestRevisePerNUMAIsolatedDedicatedNotBorrowedWithDDSelf180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	entries := nonEmptyReclaimOnNUMA0(t, "0-1", "0-1")
	isolatedDedicated := machine.MustParse("90-91")
	mode := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: true}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), isolatedDedicated.Clone(), mode))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Intersection(isolatedDedicated).IsEmpty(),
		"per-NUMA backfill must not borrow isolated dedicated 90-91 when dd=true/allowShare=true, got %s", got)
	require.Equal(t, 4, got.Size(), "must top up to want=4 from an alternative legal core, got %s", got)
}

// TestRevisePerNUMAStrictShareRejectedLeavesFloor180: in strict mode NUMA1 is entirely
// owned by the share pool, so the optional NUMA1 backfill cannot form a core-aligned
// bucket. It must be left absent (reclaim keeps the NUMA0 floor of 2), not borrow 90-91
// and not hard-error for an optional backfill.
func TestRevisePerNUMAStrictShareRejectedLeavesFloor180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	entries := nonEmptyReclaimOnNUMA0(t, "0-1", "0-1")
	entries[commonstate.PoolNameShare] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
			AllocationResult: machine.MustParse("90-179"),
		},
	}
	strict := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: true}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), strict))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Intersection(machine.MustParse("90-91")).IsEmpty(),
		"strict per-NUMA must not borrow share-owned 90-91, got %s", got)
	require.Equal(t, 2, got.Size(), "optional backfill that cannot be formed leaves the NUMA0 floor, got %s", got)
}

// TestRevisePerNUMAReservedCPUNotReadded180: 90-91 is node-reserved. Even in overlap
// mode (allowSharedOverlap=true) the per-NUMA backfill subtracts node reservation from the
// eligible set, so the reserved preference 90-91 is never re-added; it tops up from a
// non-reserved core instead. Hard reservation has no overlap exception.
func TestRevisePerNUMAReservedCPUNotReadded180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.MustParse("90-91"), seed, seedAssign)
	defer cleanup()

	entries := nonEmptyReclaimOnNUMA0(t, "0-1", "0-1")
	mode := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: true, disableDedicatedOverlap: false}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), mode))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Intersection(machine.MustParse("90-91")).IsEmpty(),
		"overlap backfill must not re-add hard node-reserved 90-91, got %s", got)
	require.Equal(t, 4, got.Size(), "must backfill to want=4 from a non-reserved core, got %s", got)
}

// TestRevisePerNUBudgetZeroSkipsWithoutError180: the global reclaim budget is already
// spent (reclaim NUMA0=0-3 == want=4). An optional NUMA1 bucket whose reserved preference
// would want 2 must be skipped up-front (budget clamped to 0) — not selected first and
// then either faked as a capacity error or materialized out of thin air.
func TestRevisePerNUBudgetZeroSkipsWithoutError180(t *testing.T) {
	t.Parallel()
	seed, seedAssign := seed180Default()
	p, cleanup := newReclaimFallback180Policy(t, machine.NewCPUSet(), seed, seedAssign)
	defer cleanup()

	entries := nonEmptyReclaimOnNUMA0(t, "0-3", "0-3")
	legacy := reclaimFallbackMode{enableReclaim: true, allowSharedOverlap: false, disableDedicatedOverlap: false}
	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(), legacy))
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, 4, got.Size(), "spent global budget must not inflate reclaim past want=4, got %s", got)
	require.True(t, got.IsSubsetOf(machine.MustParse("0-3")),
		"NUMA1 must remain absent when global budget is spent, got %s", got)
}
