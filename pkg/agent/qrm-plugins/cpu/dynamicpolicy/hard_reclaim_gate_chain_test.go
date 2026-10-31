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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func cpus(n int) machine.CPUSet {
	s := machine.NewCPUSet()
	for i := 0; i < n; i++ {
		s = s.Union(machine.NewCPUSet(i))
	}
	return s
}

// TestGatePropagatesQuotaToDonor calls the production gate and asserts the
// enabled branch derives TargetDriven/SourceTarget/ReclaimQuota correctly and skips
// FakeNUMA sources.
func TestGatePropagatesQuotaToDonor(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "a", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 28, OldPreferred: cpus(30)},
		{BlockID: "b", Class: advisorBlockClassDedicated, NUMAID: 1, Quantity: 25, OldPreferred: cpus(20)},
		{BlockID: "c", Class: advisorBlockClassDedicated, NUMAID: commonstate.FakedNUMAID, Quantity: 28, OldPreferred: cpus(30)},
	}
	out := applyTargetDrivenReclaimGate(descs, true)
	require.True(t, out[0].TargetDriven)
	require.Equal(t, 28, out[0].SourceTarget)
	require.Equal(t, 2, out[0].ReclaimQuota)
	require.True(t, out[1].TargetDriven)
	require.Equal(t, 25, out[1].SourceTarget)
	require.Equal(t, 0, out[1].ReclaimQuota) // old<target: no donation
	require.False(t, out[2].TargetDriven)    // FakeNUMA skipped
	require.Equal(t, 0, out[2].ReclaimQuota)

	// Disabled: everything stays legacy (fresh input).
	off := applyTargetDrivenReclaimGate([]advisorBlockDescriptor{
		{BlockID: "a", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 28, OldPreferred: cpus(30)},
	}, false)
	require.False(t, off[0].TargetDriven)
	require.Equal(t, 0, off[0].SourceTarget)
	require.Equal(t, 0, off[0].ReclaimQuota)
}

// TestFakeNUMANotCountedInMultiSourceAmbiguity proves a FakeNUMA dedicated source
// stays legacy and does not trip the multi-source fail-closed check.
func TestFakeNUMANotCountedInMultiSourceAmbiguity(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "real", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 28, OldPreferred: cpus(30)},
		{BlockID: "fake", Class: advisorBlockClassDedicated, NUMAID: commonstate.FakedNUMAID, Quantity: 24, OldPreferred: cpus(24)},
	}
	require.Empty(t, identifyAmbiguousTargetDrivenNUMAs(descs))
	out := applyTargetDrivenReclaimGate(descs, true)
	require.True(t, out[0].TargetDriven)
	require.False(t, out[1].TargetDriven)
}

// TestGateQuotaPropagatesThroughDemandToDonor proves the production flow end to
// end instead of re-asserting struct copies: the gate derives
// TargetDriven/SourceTarget/ReclaimQuota on the descriptor; mapping that gated
// descriptor into a partitionDemand fixture and running the production
// pinHardReclaimPartitionDemands must shrink the dedicated donor to the frozen
// sourceTarget (lending exactly the reclaimQuota) to satisfy the mandatory
// reclaim target. The demand->donor field copy and the whole reclaim solve run
// inside the tested function; only the descriptor->demand mapping below is a
// hand-built fixture.
func TestGateQuotaPropagatesThroughDemandToDonor(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)

	// Gate: dedicated source with a 12-cpu old footprint and a frozen target of
	// 10, so it may donate exactly 2 cpus.
	descs := []advisorBlockDescriptor{
		{BlockID: "d1", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
	}
	out := applyTargetDrivenReclaimGate(descs, true)
	require.True(t, out[0].TargetDriven)
	require.Equal(t, 10, out[0].SourceTarget)
	require.Equal(t, 2, out[0].ReclaimQuota)

	// Map the gated descriptor onto the production demand shape. The donor owns
	// the first 6 whole cores (12 cpus) on NUMA0; the mandatory reclaim demand
	// asks for a 2-cpu (one core) target on the same NUMA.
	donorCPUs := coresInNUMA(topology, 0, 0, 6)
	require.Equal(t, 12, donorCPUs.Size())
	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			eligible: numa, preferred: machine.NewCPUSet(),
		},
		{
			key: "d1", requestGroupKey: "d1", class: advisorBlockClassDedicated,
			quantity: out[0].SourceTarget, requestQuantity: float64(donorCPUs.Size()),
			eligible: numa, preferred: donorCPUs,
			targetDriven: out[0].TargetDriven, sourceTarget: out[0].SourceTarget,
			reclaimQuota: out[0].ReclaimQuota, numaID: out[0].NUMAID,
		},
	}

	pinned, err := pinHardReclaimPartitionDemands(demands, donorCPUs, topology, false)
	require.NoError(t, err)

	// The donor retained exactly the gate-derived frozen target (10 cpus = five
	// whole cores), having lent the 2-cpu reclaimQuota to the reclaim target.
	var afterDonor machine.CPUSet
	for _, d := range pinned {
		if d.key == "d1" {
			afterDonor = d.preferred
		}
	}
	require.Equal(t, 10, afterDonor.Size(), "donor must shrink to the gate-derived sourceTarget")
	requireCoreAligned(t, topology, afterDonor)
}
