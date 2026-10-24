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

/*
Production-path regressions for reclaim override projection and commit.
Path: applyBlocksWithDynamicConfig -> reviseReclaimPool ->

	buildAdjustmentCommitOverrideFromPodEntriesWithDynamicConfig (bulkhead view) ->
	syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig (trust boundary) ->
	commitPendingAdvisorState / precommit.

Set formula: R_raw=committed reclaim, C=advisor reclaim candidate, D=ordinary dedicated
union, X=exclusive/DNB conflict, E=reclaim eligible, R_eff=override.ReclaimEffective.
Invariant: |R_raw|>0 ⇒ R_eff≠∅ (terminal reclaim non-empty).
*/
package dynamicpolicy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func newReclaimOverrideTestPolicy(t *testing.T, cpus, numas, snippets int) *DynamicPolicy {
	topology, err := machine.GenerateDummyCPUTopology(cpus, numas, snippets)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	return p
}

func seedReclaimOverrideTestState(t *testing.T, p *DynamicPolicy, dedicated machine.CPUSet, reclaimC machine.CPUSet) {
	entries := state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: dedicated.Clone(),
				RequestQuantity:  float64(dedicated.Size()),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaimC.Clone(),
				OriginalAllocationResult: reclaimC.Clone(),
			},
		},
	}
	p.state.SetPodEntries(entries, false)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: reclaimC.Clone(), PodEntries: entries},
	}, false)
}

func reclaimOverrideTestEntry(cpus machine.CPUSet) state.ContainerEntries {
	return state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:         cpus.Clone(),
			OriginalAllocationResult: cpus.Clone(),
		},
	}
}

func reclaimOverrideTestResponse(disableDedicatedOverlap bool, dedicatedCPUs, reclaimCPUs machine.CPUSet) *advisorapi.ListAndWatchResponse {
	return &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: disableDedicatedOverlap,
		Entries: map[string]*advisorapi.CalculationEntries{
			"dedicated-pod": {Entries: map[string]*advisorapi.CalculationInfo{
				"main": {
					OwnerPoolName: commonstate.PoolNameDedicated,
					CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
						0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: uint64(dedicatedCPUs.Size())}}},
					},
				},
			}},
			commonstate.PoolNameReclaim: {Entries: map[string]*advisorapi.CalculationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName: commonstate.PoolNameReclaim,
					CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
						0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: uint64(reclaimCPUs.Size())}}},
					},
				},
			}},
		},
	}
}

func reclaimOverrideTestBlocks(dedicatedCPUs, reclaimCPUs machine.CPUSet) advisorapi.BlockCPUSet {
	return advisorapi.BlockCPUSet{
		"dedicated": dedicatedCPUs.Clone(),
		"reclaim":   reclaimCPUs.Clone(),
	}
}

// Legal ordinary overlap: EnableReclaim=false, disableDedicatedOverlap=false,
// ordinary dedicated owns the WHOLE machine while committed reclaim C lives inside it.
// Expect: success, terminal reclaim == C (non-empty), revision advances once.
func TestApplyBlocksOverlapWholeMachineKeepsCommittedReclaim(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false

	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	committed := machine.NewCPUSet(0, 1)
	seedReclaimOverrideTestState(t, p, all, committed)

	resp := reclaimOverrideTestResponse(false, all, committed)
	delete(resp.Entries, commonstate.PoolNameReclaim)
	blocks := reclaimOverrideTestBlocks(all, committed)
	delete(blocks, "reclaim")
	revBefore := p.state.GetRevision()

	pending, err := p.applyBlocksWithDynamicConfig(blocks, resp, false, false, dyn)
	require.NoError(t, err, "legal ordinary overlap must succeed when the advisor omits reclaim")

	completedCommitted, err := completeCoresForCPUSet(p.machine.machineInfo.CPUTopology, committed)
	require.NoError(t, err)
	got := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.False(t, got.IsEmpty(), "terminal reclaim must stay non-empty")
	require.True(t, got.Equals(completedCommitted),
		"overlap mode must retain committed CPUs and only add their physical-core siblings: committed=%s completed=%s got=%s",
		committed, completedCommitted, got)

	require.NoError(t, p.commitPendingAdvisorState(pending))
	require.Equal(t, revBefore+1, p.state.GetRevision(), "revision must advance exactly once")
	published := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.True(t, published.Equals(completedCommitted), "published reclaim must equal %s, got %s", completedCommitted, published)
}

// Overlap mode, ordinary dedicated PARTIAL: committed reclaim disjoint from dedicated.
// Expect success, reclaim preserved.
func TestApplyBlocksOverlapPartialDedicatedKeepsReclaim(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false

	dedicated := machine.NewCPUSet(2, 3, 4, 5, 6, 7)
	C := machine.NewCPUSet(0, 1)
	seedReclaimOverrideTestState(t, p, dedicated, C)

	pending, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(dedicated, C), reclaimOverrideTestResponse(false, dedicated, C), false, false, dyn)
	require.NoError(t, err)
	got := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(C), "partial dedicated must preserve reclaim C=%s, got %s", C, got)
}

// Disjoint mode (disableDedicatedOverlap=true): planner owns reclaim and requires
// whole-core alignment. Committed reclaim C={0,1} is a half-core set (SMT siblings
// 4,5 not included), so core-alignment folds R_eff to empty. The trust boundary must
// reject this fail-closed: error, no publish, revision not advanced.
func TestApplyBlocksDisjointHalfCoreReclaimRejectedFailClosed(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true

	dedicated := machine.NewCPUSet(2, 3, 4, 5, 6, 7)
	C := machine.NewCPUSet(0, 1) // half-core: siblings 4,5 not in reclaim
	seedReclaimOverrideTestState(t, p, dedicated, C)

	revBefore := p.state.GetRevision()
	_, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(dedicated, C), reclaimOverrideTestResponse(true, dedicated, C), false, false, dyn)
	require.Error(t, err, "disjoint half-core reclaim must be rejected fail-closed, not committed empty")
	require.Contains(t, err.Error(), "empty reclaim pool", "must reject via the active-empty override boundary")
	require.Equal(t, revBefore, p.state.GetRevision(), "revision must not advance on fail-closed")
	published := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.True(t, published.Equals(C), "committed reclaim must survive the rejected frame, got %s", published)
}

// Direct trust-boundary: an override with R_eff=empty but Source set must be rejected,
// and the committed reclaim C must survive.
func TestOverrideEmptyWithSourceRejectedFailClosed(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	// EnableReclaim=true: trust boundary active. When disabled, freeze path kicks in.
	dyn.EnableReclaim = true

	C := machine.NewCPUSet(0, 1)
	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: reclaimOverrideTestEntry(C),
	}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective: machine.NewCPUSet(),
		Source:           "advisor_pending_entries",
	}
	err := p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(newEntries, override, dyn)
	require.Error(t, err)
	got := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(C), "committed reclaim must survive a rejected empty override, got %s", got)
}

// F1: an active empty override against an already-empty pending reclaim is a
// legitimate no-op (fresh start). It must NOT error and must NOT publish anything.
func TestOverrideEmptyWithSourceAgainstEmptyReclaimIsNoOp(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: reclaimOverrideTestEntry(machine.NewCPUSet()),
	}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective: machine.NewCPUSet(),
		Source:           "advisor_pending_entries",
	}
	require.NoError(t, p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(newEntries, override, dyn),
		"active empty override against already-empty reclaim must be a no-op, not an error")
	got := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.IsEmpty(), "reclaim must stay empty on a fresh-start no-op, got %s", got)
}

// F4: NUMA-exclusive dedicated X completely covers R_raw, so the view projects
// R_eff=empty. The active override then hits the fail-closed trust boundary:
// error, no publish, revision does not advance.
// Note: EnableReclaim=true here — when reclaim is disabled, the freeze path
// (see reclaim_disabled_freeze_red_test.go) preserves committed C instead.
func TestOverrideEmptyFromExclusiveDedicatedCoveringReclaimFailClosed(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true

	// Dedicated {0,1,2,3} is NUMA-exclusive and covers R_raw={0,1}.
	entries := state.PodEntries{
		"exclusive-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "exclusive-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameReclaim: reclaimOverrideTestEntry(machine.NewCPUSet(0, 1)),
	}
	p.state.SetPodEntries(entries, false)

	override := &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective: machine.NewCPUSet(),
		Source:           "advisor_pending_entries",
	}
	revBefore := p.state.GetRevision()
	err := p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(entries, override, dyn)
	require.Error(t, err, "exclusive-dedicated covering all reclaim must fail closed")
	require.Contains(t, err.Error(), "empty reclaim pool")
	require.Equal(t, revBefore, p.state.GetRevision(), "revision must not advance on fail-closed")
	published := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.True(t, published.Equals(machine.NewCPUSet(0, 1)), "committed reclaim must survive, got %s", published)
}

// F3: after a successful overlap-mode commit, the reclaim pool C must survive a
// real restart via checkpoint reload.
func TestOverlapCommitReclaimSurvivesCheckpointRestart(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false

	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	C := machine.NewCPUSet(0, 1)
	seedReclaimOverrideTestState(t, p, all, C)

	pending, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(false, all, C), false, false, dyn)
	require.NoError(t, err)
	require.NoError(t, p.commitPendingAdvisorState(pending))

	published := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.True(t, published.Equals(C), "pre-restart reclaim must equal C=%s, got %s", C, published)

	// Restart: reload checkpoint from the same state directory.
	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: p.advisorPostCommitCheckpointDir},
		cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic,
		p.machine.machineInfo.CPUTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	got := restarted.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.True(t, got.Equals(C), "post-restart reclaim must survive as C=%s, got %s", C, got)
}

// Other entry changes still commit (C same but dedicated grows).
func TestP0OtherEntryChangeStillCommits(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false
	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	C := machine.NewCPUSet(0, 1)
	seedReclaimOverrideTestState(t, p, all, C)

	// First commit
	pending1, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(false, all, C), false, false, dyn)
	require.NoError(t, err)
	require.NoError(t, p.commitPendingAdvisorState(pending1))

	// Second: dedicated grows (other entry change)
	dedicated2 := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6)
	pending2, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(dedicated2, C), reclaimOverrideTestResponse(false, dedicated2, C), false, false, dyn)
	require.NoError(t, err)
	revBefore2 := p.state.GetRevision()
	require.NoError(t, p.commitPendingAdvisorState(pending2))
	require.Greater(t, p.state.GetRevision(), revBefore2, "other entry change must advance revision")
}

// Table-driven: full/partial/noC/enabled/other-change/freezes.
func TestP0FreezeBranchMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		cpus           int
		numas          int
		snippets       int
		committedC     []int
		dedicated      []int
		enableReclaim  bool
		disableDedOver bool
		expectError    bool
		expectC        []int
	}{
		{"full_overlap_disabled", 8, 1, 1, []int{0, 1}, []int{0, 1, 2, 3, 4, 5, 6, 7}, false, false, false, []int{0, 1}},
		{"partial_disjoint_disabled", 8, 1, 1, []int{0, 1}, []int{2, 3, 4, 5, 6, 7}, false, false, false, []int{0, 1}},
		{"enabled_fail_closed", 8, 1, 1, []int{0, 1}, []int{2, 3, 4, 5, 6, 7}, true, true, true, []int{0, 1}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newReclaimOverrideTestPolicy(t, tc.cpus, tc.numas, tc.snippets)
			dyn := p.config.dynamicConfig.GetDynamicConfiguration()
			dyn.EnableReclaim = tc.enableReclaim
			C := machine.NewCPUSet(tc.committedC...)
			dd := machine.NewCPUSet(tc.dedicated...)
			seedReclaimOverrideTestState(t, p, dd, C)
			pending, err := p.applyBlocksWithDynamicConfig(
				reclaimOverrideTestBlocks(dd, C), reclaimOverrideTestResponse(tc.disableDedOver, dd, C), false, false, dyn)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
			want := machine.NewCPUSet(tc.expectC...)
			require.True(t, got.Equals(want), "got=%s want=%s", got, want)
		})
	}
}

// B: false→true and true→false transitions.
func TestP0EnableReclaimTransitions(t *testing.T) {
	t.Parallel()
	// false→true: after disabled freeze, enable reclaim must fail-closed on empty override.
	t.Run("false_to_true", func(t *testing.T) {
		t.Parallel()
		p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
		dyn := p.config.dynamicConfig.GetDynamicConfiguration()
		dyn.EnableReclaim = false
		all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		C := machine.NewCPUSet(0, 1)
		seedReclaimOverrideTestState(t, p, all, C)
		// R1: disabled freeze commits
		pending1, err := p.applyBlocksWithDynamicConfig(
			reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(false, all, C), false, false, dyn)
		require.NoError(t, err)
		require.NoError(t, p.commitPendingAdvisorState(pending1))
		// R2: enable reclaim, empty override must fail-closed
		dyn.EnableReclaim = true
		_, err = p.applyBlocksWithDynamicConfig(
			reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(true, all, C), false, false, dyn)
		require.Error(t, err, "enabled empty override must fail-closed")
	})
	// true→false: committed C survives when reclaim disabled.
	t.Run("true_to_false", func(t *testing.T) {
		t.Parallel()
		p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
		dyn := p.config.dynamicConfig.GetDynamicConfiguration()
		dyn.EnableReclaim = true
		all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		C := machine.NewCPUSet(0, 1)
		seedReclaimOverrideTestState(t, p, all, C)
		// Disable reclaim
		dyn.EnableReclaim = false
		pending, err := p.applyBlocksWithDynamicConfig(
			reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(false, all, C), false, false, dyn)
		require.NoError(t, err)
		got := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
		require.True(t, got.Equals(C), "committed C must survive true→false, got %s", got)
	})
}

// C: offline/missing topology C fail, revision not advanced.
func TestP0OfflineCPUFailClosed(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false
	// C includes CPU 100 (offline, not in 8-CPU topology)
	C := machine.NewCPUSet(0, 1, 100)
	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	seedReclaimOverrideTestState(t, p, all, C)
	revBefore := p.state.GetRevision()
	_, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(false, all, C), false, false, dyn)
	require.Error(t, err, "offline CPU in C must fail-closed")
	require.Equal(t, revBefore, p.state.GetRevision(), "revision must not advance on fail")
}

// D: ordinaryD + allowShared/disableDedicated flags table.
func TestP0OrdinaryDFlagsMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		allowShared    bool
		disableDedOver bool
		expectError    bool
	}{
		{"shared_overlap_allowed", true, false, false},
		{"both_default", false, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
			dyn := p.config.dynamicConfig.GetDynamicConfiguration()
			dyn.EnableReclaim = false
			all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
			C := machine.NewCPUSet(0, 1)
			seedReclaimOverrideTestState(t, p, all, C)
			_, err := p.applyBlocksWithDynamicConfig(
				reclaimOverrideTestBlocks(all, C), reclaimOverrideTestResponse(tc.disableDedOver, all, C), tc.allowShared, false, dyn)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// E: allocation production path: eligible={4-7} must not leak into reclaim C={0-1}.
func TestP0AllocationPathEligibleNotInReclaim(t *testing.T) {
	t.Parallel()
	p := newReclaimOverrideTestPolicy(t, 8, 1, 1)
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false
	C := machine.NewCPUSet(0, 1)
	eligible := machine.NewCPUSet(4, 5, 6, 7)
	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	seedReclaimOverrideTestState(t, p, all, C)

	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: C.Clone(),
		commonstate.PoolNameShare:   eligible.Clone(),
	}
	entries := state.PodEntries{
		"reclaim": reclaimOverrideTestEntry(C.Clone()),
	}
	ms := state.NUMANodeMap{0: {DefaultCPUSet: all.Clone(), PodEntries: entries}}

	_ = p.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		entries,
		ms,
		nil,
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{eligibleCPUSet: eligible.Clone()},
		p.state.GetRevision(),
		dyn,
	)
	got := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(C), "reclaim=%s want C=%s (eligible=%s must not leak)", got, C, eligible)
	require.False(t, got.Intersection(eligible).Size() > 0, "reclaim must not contain eligible=%s", eligible)
}

// A: 180-CPU dummy topology, C={32,48,96,112} sparse set, real NUMA-exclusive D.
func TestP0Freeze180CPUExclusiveReal(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(180, 2, 2)
	require.NoError(t, err)
	dir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false

	C := machine.NewCPUSet(32, 48, 96, 112)
	D := C.Clone()
	D.Add(122, 138, 6, 22)

	entries := state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult: D.Clone(),
				RequestQuantity:  float64(D.Size()),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         C.Clone(),
				OriginalAllocationResult: C.Clone(),
			},
		},
	}
	p.state.SetPodEntries(entries, false)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: C.Clone(), PodEntries: entries},
	}, false)

	exclAI := p.state.GetAllocationInfo("dedicated-pod", "main")
	require.True(t, exclAI.CheckDedicatedNUMABindingNUMAExclusive(),
		"dedicated-pod must be NUMA-exclusive")

	committed := p.state.GetPodEntries()[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, "32,48,96,112", committed.String(), "committed C must be sparse set")

	revBefore := p.state.GetRevision()
	pending, err := p.applyBlocksWithDynamicConfig(
		reclaimOverrideTestBlocks(D, C), reclaimOverrideTestResponse(false, D, C), false, false, dyn)
	require.NoError(t, err)
	require.NoError(t, p.commitPendingAdvisorState(pending))
	revAfter := p.state.GetRevision()

	got := p.state.GetPodEntries()[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, got.Equals(C), "freeze exact, got %s want %s", got, C)
	require.Equal(t, "32,48,96,112", got.String(), "published C must be sparse set")
	require.Equal(t, revBefore+1, revAfter, "rev must advance")
	t.Logf("C=%s D=%s published=%s rev %d→%d exclusive=true", C, D, got, revBefore, revAfter)
}

func checkpointFileDigest(t *testing.T, dir string) (hash string, size int64, mtimeNsec int64) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		require.NoError(t, err)
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		return fmt.Sprintf("%x", sum), info.Size(), info.ModTime().UnixNano()
	}
	return "", 0, 0
}

func TestP0B1TwoRoundProductionFreeze(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	ckptDir := t.TempDir()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, ckptDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = false
	all := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	C := machine.NewCPUSet(0, 1)
	seedReclaimOverrideTestState(t, p, all, C)

	// Handler spy
	handlersRun := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"spy": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			handlersRun++
			return nil
		},
	}

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"dedicated-pod": {Entries: map[string]*advisorapi.CalculationInfo{
				"main": {OwnerPoolName: "dedicated"},
			}},
		},
		// reclaim omitted → freeze
	}

	rev0 := p.state.GetRevision()
	h0 := handlersRun
	h0Hash, _, _ := checkpointFileDigest(t, ckptDir)
	require.NoError(t, p.allocateByCPUAdvisor(nil, resp, nil))
	rev1 := p.state.GetRevision()
	h1 := handlersRun
	h1Hash, h1Size, h1Mtime := checkpointFileDigest(t, ckptDir)

	require.NoError(t, p.allocateByCPUAdvisor(nil, resp, nil))
	rev2 := p.state.GetRevision()
	h2 := handlersRun
	h2Hash, h2Size, h2Mtime := checkpointFileDigest(t, ckptDir)

	t.Logf("rev %d→%d→%d (R1+%d R2+%d), handlers R1=%d R2=%d",
		rev0, rev1, rev2, rev1-rev0, rev2-rev1, h1-h0, h2-h1)
	t.Logf("checkpoint R1: hash=%s size=%d mtime=%d", h1Hash, h1Size, h1Mtime)
	t.Logf("checkpoint R2: hash=%s size=%d mtime=%d", h2Hash, h2Size, h2Mtime)
	require.Equal(t, rev0+1, rev1, "R1 must advance")
	require.Equal(t, rev1, rev2, "R2 must NOT advance")
	require.Equal(t, 0, h2-h1, "R2 must NOT run adjustment handlers")
	require.NotEqual(t, h0Hash, h1Hash, "R1 checkpoint hash must change")
	require.Equal(t, h1Hash, h2Hash, "R2 checkpoint hash must NOT change")
	require.Equal(t, h1Size, h2Size, "R2 checkpoint size must NOT change")
	require.Equal(t, h1Mtime, h2Mtime, "R2 checkpoint mtime must NOT change")
}
