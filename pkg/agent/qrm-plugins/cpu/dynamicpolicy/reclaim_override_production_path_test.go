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
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
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
	dyn.EnableReclaim = false

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
