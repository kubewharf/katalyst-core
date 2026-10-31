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
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var errBoom = errors.New("boom")

// steadyFixture builds a real-NUMA mandatory-reclaim + dedicated pair on one NUMA.
func steadyFixture(
	topology *machine.CPUTopology,
	numaID, reclaimQty, dedicatedQty, reclaimCommittedCores, dedicatedCommittedCores int,
) []advisorBlockDescriptor {
	numa := topology.CPUDetails.CPUsInNUMANodes(numaID)
	reclaimCommitted := machine.NewCPUSet()
	for c := 0; c < reclaimCommittedCores; c++ {
		reclaimCommitted = reclaimCommitted.Union(coresInNUMA(topology, numaID, c, c+1))
	}
	dedicatedCommitted := machine.NewCPUSet()
	for c := reclaimCommittedCores; c < reclaimCommittedCores+dedicatedCommittedCores; c++ {
		dedicatedCommitted = dedicatedCommitted.Union(coresInNUMA(topology, numaID, c, c+1))
	}
	return []advisorBlockDescriptor{
		{
			BlockID: "reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: numaID,
			Quantity: reclaimQty, ComponentKey: "mand\x00" + string(rune(numaID)),
			Eligible: numa.Clone(), Committed: reclaimCommitted,
		},
		{
			BlockID: "dedicated", Class: advisorBlockClassDedicated, NUMAID: numaID,
			Quantity: dedicatedQty, ComponentKey: "ded\x00" + string(rune(numaID)),
			Eligible: numa.Clone(), Committed: dedicatedCommitted,
		},
	}
}

func recordByNUMA(report steadyReclaimNormalizeReport, numaID int) (steadyReclaimNormalizeRecord, bool) {
	for _, r := range report.Records {
		if r.NUMAID == numaID {
			return r, true
		}
	}
	return steadyReclaimNormalizeRecord{}, false
}

// smt4Topology builds a single-NUMA SMT4 topology: numCores physical cores each with
// 4 logical CPUs (w=4).
func smt4Topology(t *testing.T, numCores int) *machine.CPUTopology {
	t.Helper()
	cpuNum := numCores * 4
	topology := &machine.CPUTopology{
		NumCPUs: cpuNum, NumCores: numCores, NumSockets: 1, NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{},
	}
	for cpu := 0; cpu < cpuNum; cpu++ {
		topology.CPUDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: cpu / 4}
	}
	require.Equal(t, 4, topology.CPUsPerCore())
	return topology
}

// TestNormalizeSteadyRealNUMATargets_RoundDownOddAdviceToCommitted reproduces the node
// symptom: SMT2, NUMA2 advice mandatory-reclaim=3 / dedicated=29, committed reclaim=1
// core (2 CPUs), committed dedicated=15 cores (30 CPUs), free=0.
func TestNormalizeSteadyRealNUMATargets_RoundDownOddAdviceToCommitted(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 2, 4)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())

	descriptors := steadyFixture(topology, 2, 3, 29, 1, 15)
	available := topology.CPUDetails.CPUsInNUMANodes(2)

	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, available, topology, nil)
	require.NoError(t, err)
	require.True(t, report.HadOddAdvice)

	rec, ok := recordByNUMA(report, 2)
	require.True(t, ok)
	require.Equal(t, steadyReclaimDecisionRoundDown, rec.Decision)
	require.Equal(t, 3, rec.AdvisorReclaim)
	require.Equal(t, 2, rec.ReclaimAfter)
	require.Equal(t, 2, rec.CommittedReclaim)
	require.Equal(t, 0, rec.FreeWholeCoreCPUs)

	var reclaim, dedicated *advisorBlockDescriptor
	for i := range normalized {
		switch normalized[i].BlockID {
		case "reclaim":
			reclaim = &normalized[i]
		case "dedicated":
			dedicated = &normalized[i]
		}
	}
	require.NotNil(t, reclaim)
	require.NotNil(t, dedicated)
	require.Equal(t, 2, reclaim.Quantity)
	require.Equal(t, 30, dedicated.Quantity)
}

func TestNormalizeSteadyRealNUMATargets_AlignedAdviceUnchanged(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 2, 4)
	require.NoError(t, err)

	descriptors := steadyFixture(topology, 0, 8, 24, 4, 12)
	available := topology.CPUDetails.CPUsInNUMANodes(0)

	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, available, topology, nil)
	require.NoError(t, err)
	require.False(t, report.HadOddAdvice)
	rec, ok := recordByNUMA(report, 0)
	require.True(t, ok)
	require.Equal(t, steadyReclaimDecisionAligned, rec.Decision)
	require.Equal(t, 8, normalized[0].Quantity)
	require.Equal(t, 24, normalized[1].Quantity)
}

func TestNormalizeSteadyRealNUMATargets_SMT1PassThrough(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(32, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 1, topology.CPUsPerCore())

	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 3, ComponentKey: "mand", Eligible: numa.Clone(), Committed: numa.Clone(),
		},
		{
			BlockID: "dedicated", Class: advisorBlockClassDedicated, NUMAID: 0,
			Quantity: 5, ComponentKey: "ded", Eligible: numa.Clone(),
		},
	}
	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, numa, topology, nil)
	require.NoError(t, err)
	require.False(t, report.HadOddAdvice)
	require.Empty(t, report.Records)
	require.Equal(t, 3, normalized[0].Quantity)
	require.Equal(t, 5, normalized[1].Quantity)
}

func TestNormalizeSteadyRealNUMATargets_SMT4WholeCoreGrid(t *testing.T) {
	t.Parallel()

	topology := smt4Topology(t, 16)

	// committed reclaim=1 core(4), dedicated=14 cores(56), leaving 1 free core(4).
	// advisor reclaim=13. lower=12 (growth 8 > free 4), upper=16 (growth 12 > free 4)
	// -> infeasible passthrough.
	descriptors := steadyFixture(topology, 0, 13, 51, 1, 14)
	available := topology.CPUDetails.CPUsInNUMANodes(0)
	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, available, topology, nil)
	require.NoError(t, err)
	rec, _ := recordByNUMA(report, 0)
	require.Equal(t, steadyReclaimDecisionInfeasible, rec.Decision)
	require.Equal(t, 13, normalized[0].Quantity)

	// Enough free cores to cover growth to lower=12 (growth 8 <= 16) -> round down.
	descriptors2 := steadyFixture(topology, 0, 13, 35, 1, 11)
	normalized2, report2, err2 := normalizeSteadyRealNUMATargets(descriptors2, available, topology, nil)
	require.NoError(t, err2)
	rec2, _ := recordByNUMA(report2, 0)
	require.Equal(t, steadyReclaimDecisionRoundDown, rec2.Decision)
	require.Equal(t, 12, normalized2[0].Quantity)
}

func TestNormalizeSteadyRealNUMATargets_FakeNUMAUntouched(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	fake := advisorBlockDescriptor{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: -1,
		Quantity: 3, ComponentKey: "fake", Eligible: topology.CPUDetails.CPUs().Clone(),
	}
	normalized, report, err := normalizeSteadyRealNUMATargets([]advisorBlockDescriptor{fake},
		topology.CPUDetails.CPUs(), topology, nil)
	require.NoError(t, err)
	require.False(t, report.HadOddAdvice)
	require.Empty(t, report.Records)
	require.Equal(t, 3, normalized[0].Quantity)
}

func TestNormalizeSteadyRealNUMATargets_SkippedNUMAUntouched(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	descriptors := steadyFixture(topology, 0, 3, 29, 1, 15)
	available := topology.CPUDetails.CPUsInNUMANodes(0)
	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, available, topology, sets.NewInt(0))
	require.NoError(t, err)
	require.False(t, report.HadOddAdvice)
	require.Empty(t, report.Records)
	require.Equal(t, 3, normalized[0].Quantity)
}

// TestNormalizeSteadyRealNUMATargets_SMT4RollsBackPartialDedicatedAbsorb proves that
// when an odd reclaim target rounds down but the released CPUs cannot be fully
// absorbed, the normalization rolls back both the mandatory delta and every
// partially-taken dedicated block.
func TestNormalizeSteadyRealNUMATargets_SMT4RollsBackPartialDedicatedAbsorb(t *testing.T) {
	t.Parallel()

	topology := smt4Topology(t, 16)
	all := topology.CPUDetails.CPUsInNUMANodes(0)

	reclaimCommitted := coresInNUMA(topology, 0, 0, 1)
	dedA := coresInNUMA(topology, 0, 1, 2)
	dedB := coresInNUMA(topology, 0, 2, 3)

	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 14, ComponentKey: "mand", Eligible: all.Clone(), Committed: reclaimCommitted,
		},
		{
			BlockID: "ded-a", Class: advisorBlockClassDedicated, NUMAID: 0,
			Quantity: 3, ComponentKey: "ded-a", Eligible: dedA.Clone(), Committed: dedA,
		},
		{
			BlockID: "ded-b", Class: advisorBlockClassDedicated, NUMAID: 0,
			Quantity: 4, ComponentKey: "ded-b", Eligible: dedB.Clone(), Committed: dedB,
		},
	}
	beforeTotal := 14 + 3 + 4

	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, all, topology, nil)
	require.NoError(t, err)
	require.True(t, report.HadOddAdvice)
	rec, ok := recordByNUMA(report, 0)
	require.True(t, ok)
	require.Equal(t, steadyReclaimDecisionInfeasible, rec.Decision)

	var reclaim, dedAOut, dedBOut *advisorBlockDescriptor
	for i := range normalized {
		switch normalized[i].BlockID {
		case "reclaim":
			reclaim = &normalized[i]
		case "ded-a":
			dedAOut = &normalized[i]
		case "ded-b":
			dedBOut = &normalized[i]
		}
	}
	require.NotNil(t, reclaim)
	require.NotNil(t, dedAOut)
	require.NotNil(t, dedBOut)
	require.Equal(t, 14, reclaim.Quantity)
	require.Equal(t, 3, dedAOut.Quantity, "partially-absorbed dedicated block must be rolled back")
	require.Equal(t, 4, dedBOut.Quantity)
	require.Equal(t, beforeTotal, reclaim.Quantity+dedAOut.Quantity+dedBOut.Quantity)
}

// TestPNHCommittedFallback_AnchorsOnCommittedWholeCore proves the committed fallback
// retries anchored on the committed whole core when the fast path cannot grow.
func TestPNHCommittedFallback_AnchorsOnCommittedWholeCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	committed := coresInNUMA(topology, 0, 0, 1) // one whole core = 2 cpus

	input := hardReclaimPartitionInput{
		topology:        topology,
		targetByNUMA:    map[int]int{0: 4},
		currentReclaim:  committed,
		free:            machine.NewCPUSet(),
		reclaimEligible: topology.CPUDetails.CPUsInNUMANodes(0),
		donors:          nil,
	}

	_, planErr := planHardReclaimPartition(input)
	require.Error(t, planErr)
	var selErr *hardReclaimSelectionError
	require.True(t, stderrors.As(planErr, &selErr))
	require.Equal(t, hardReclaimFailureInsufficientWholeCore, selErr.reason)

	plan, _, err := pnhCommittedFallback(input, planErr)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.True(t, plan.reclaim.Equals(committed),
		"fallback must keep the committed whole core: got %s want %s", plan.reclaim.String(), committed.String())
}

// TestPNHCommittedFallback_DoesNotSwallowUnrelatedError proves a non-insufficient
// error is returned untouched.
func TestPNHCommittedFallback_DoesNotSwallowUnrelatedError(t *testing.T) {
	t.Parallel()

	_, _, err := pnhCommittedFallback(hardReclaimPartitionInput{}, errBoom)
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

// TestNormalizeSteadyRealNUMATargets_NonExclusiveNoDedicatedPassthrough proves the
// coverage question raised by assembleWithoutNUMAExclusivePool: a real NUMA whose
// only odd mandatory-reclaim block comes from the non-exclusive reclaim pool (no
// disjoint dedicated block on that NUMA) cannot round-down into a dedicated block.
// The normalization must roll back and pass through unchanged rather than invent a
// dedicated absorb; the non-exclusive normal reclaim solver owns partial reclaim.
func TestNormalizeSteadyRealNUMATargets_NonExclusiveNoDedicatedPassthrough(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())

	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	committed := coresInNUMA(topology, 0, 0, 1)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 3, ComponentKey: "mand", Eligible: numa.Clone(), Committed: committed,
		},
		{
			BlockID: "share", Class: advisorBlockClassShared, NUMAID: 0,
			Quantity: 20, ComponentKey: "share", Eligible: numa.Clone(),
		},
	}

	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, numa, topology, nil)
	require.NoError(t, err)
	require.True(t, report.HadOddAdvice)
	rec, ok := recordByNUMA(report, 0)
	require.True(t, ok)
	require.Equal(t, steadyReclaimDecisionInfeasible, rec.Decision)
	// reclaim must be untouched (rolled back); the shared block must be untouched.
	require.Equal(t, 3, normalized[0].Quantity)
	require.Equal(t, 20, normalized[1].Quantity)
}

// TestNormalizeSteadyRealNUMATargets_NonExclusiveWithDedicatedAbsorbs proves that
// when a non-exclusive NUMA does carry a dedicated block, the committed-anchored
// round-down absorbs the released CPU into that dedicated block, conserving the
// reclaim+dedicated split.
func TestNormalizeSteadyRealNUMATargets_NonExclusiveWithDedicatedAbsorbs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	numa := topology.CPUDetails.CPUsInNUMANodes(0)
	reclaimCommitted := coresInNUMA(topology, 0, 0, 1)
	dedicatedCommitted := coresInNUMA(topology, 0, 1, 14)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 3, ComponentKey: "mand", Eligible: numa.Clone(), Committed: reclaimCommitted,
		},
		{
			BlockID: "dedicated", Class: advisorBlockClassDedicated, NUMAID: 0,
			Quantity: 25, ComponentKey: "ded", Eligible: numa.Clone(), Committed: dedicatedCommitted,
		},
	}

	normalized, report, err := normalizeSteadyRealNUMATargets(descriptors, numa, topology, nil)
	require.NoError(t, err)
	rec, ok := recordByNUMA(report, 0)
	require.True(t, ok)
	require.Equal(t, steadyReclaimDecisionRoundDown, rec.Decision)
	require.Equal(t, 2, normalized[0].Quantity)
	require.Equal(t, 26, normalized[1].Quantity)
}

// TestNormalizeSteadyRealNUMATargets_NonExclusiveFakeNUMAReclaimUntouched proves the
// FakedNUMAID reclaim block published by assembleWithoutNUMABinding never enters the
// real-NUMA normalization.
func TestNormalizeSteadyRealNUMATargets_NonExclusiveFakeNUMAReclaimUntouched(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	fake := advisorBlockDescriptor{
		BlockID: "fake-reclaim", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 3, ComponentKey: "fake", Eligible: topology.CPUDetails.CPUs().Clone(),
	}
	normalized, report, err := normalizeSteadyRealNUMATargets([]advisorBlockDescriptor{fake},
		topology.CPUDetails.CPUs(), topology, nil)
	require.NoError(t, err)
	require.False(t, report.HadOddAdvice)
	require.Empty(t, report.Records)
	require.Equal(t, 3, normalized[0].Quantity)
}

// TestReconcileCommittedFallbackQuantities_ShrinksMandatoryAndReturnsToDedicated proves
// the F1 fix end to end: after the committed fallback anchors reclaim down to the
// committed whole core, the mandatory demand quantity is shrunk to its pinned eligible
// and the released CPUs are returned to the dedicated demand so quantity == eligible.
func TestReconcileCommittedFallbackQuantities_ShrinksMandatoryAndReturnsToDedicated(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	committed := coresInNUMA(topology, 0, 0, 1)
	dedicatedEligible := numa0.Difference(committed)

	// Simulate post-pinning state: mandatory eligible is the committed core (size 2),
	// dedicated eligible is the rest (size 30).
	pinned := []partitionDemand{
		{
			key: "reclaim", class: advisorBlockClassMandatoryReclaim, numaID: 0,
			quantity: 4, eligible: committed.Clone(), preferred: committed.Clone(),
		},
		{
			key: "dedicated", class: advisorBlockClassDedicated, numaID: 0,
			quantity: 26, eligible: dedicatedEligible.Clone(), preferred: dedicatedEligible.Clone(),
		},
	}
	originalTargetByNUMA := map[int]int{0: 4}
	committedByNUMA := map[int]int{0: 2}

	require.NoError(t, reconcileCommittedFallbackQuantities(pinned, originalTargetByNUMA, committedByNUMA, topology))
	// The downstream solver requires eligible.Size() >= quantity; the per-NUMA
	// mandatory+dedicated total is conserved (reclaim shrink = dedicated growth).
	require.LessOrEqual(t, pinned[0].quantity, pinned[0].eligible.Size())
	require.LessOrEqual(t, pinned[1].quantity, pinned[1].eligible.Size())
	require.Equal(t, 4+26, pinned[0].quantity+pinned[1].quantity)
	// Mandatory shrank from its original target to the committed anchor.
	require.Less(t, pinned[0].quantity, 4)
}

// TestReconcileCommittedFallbackQuantities_AbsorptionFailure proves that when no
// dedicated demand can absorb the released CPUs, the reconcile returns an error so
// the caller defers to the general replacement search.
func TestReconcileCommittedFallbackQuantities_AbsorptionFailure(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	committed := coresInNUMA(topology, 0, 0, 1)
	pinned := []partitionDemand{
		{
			key: "reclaim", class: advisorBlockClassMandatoryReclaim, numaID: 0,
			quantity: 4, eligible: committed.Clone(), preferred: committed.Clone(),
		},
	}
	err = reconcileCommittedFallbackQuantities(pinned, map[int]int{0: 4}, map[int]int{0: 2}, topology)
	require.Error(t, err)
}

// TestPinHardReclaimPartitionDemands_SteadyFallbackWiresReconcile proves the pin-level
// wiring: with no dedicated donor (whole-core capacity below target) the steady path
// takes the committed fallback and, when there is nowhere to absorb the released CPUs,
// defers to replacement rather than returning a half-reconciled success.
func TestPinHardReclaimPartitionDemands_SteadyFallbackWiresReconcile(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	committed := coresInNUMA(topology, 0, 0, 1)
	// eligible spans the whole NUMA so the solver reaches the frontier selection (and
	// returns insufficient_whole_core rather than a pre-search eligibility error);
	// available is only the committed core so free is empty and the target cannot be
	// grown; preferred is the committed anchor the fallback will re-anchor on.
	demands := []partitionDemand{
		{
			key: "reclaim", class: advisorBlockClassMandatoryReclaim, numaID: 0,
			quantity: 4, eligible: numa0.Clone(), preferred: committed.Clone(),
		},
	}
	_, err = pinHardReclaimPartitionDemands(demands, committed.Clone(), topology, true)
	require.Error(t, err)
	// The error must come from the committed fallback reconciliation deferring to the
	// replacement search, not from a pre-search eligibility rejection: "needs N more
	// reclaim CPUs" is the fast-path insufficient_whole_core cause, and the trailing
	// "replacement failed" proves the fallback ran and handed off to the search.
	require.Contains(t, err.Error(), "needs 2 more reclaim CPUs")
	require.Contains(t, err.Error(), "replacement failed")
}
