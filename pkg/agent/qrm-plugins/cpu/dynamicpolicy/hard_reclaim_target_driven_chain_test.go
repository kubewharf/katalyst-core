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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestPerNUMADegradationIsolatesAmbiguousNUMAs proves the gate degrades only the
// ambiguous NUMA: NUMA0 with two distinct dedicated BlockIDs stays legacy while
// NUMA1 with a single dedicated source is still upgraded to target-driven.
func TestPerNUMADegradationIsolatesAmbiguousNUMAs(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "a", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
		{BlockID: "b", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
		{BlockID: "c", Class: advisorBlockClassDedicated, NUMAID: 1, Quantity: 10, OldPreferred: cpus(12)},
	}
	out := applyTargetDrivenReclaimGate(descs, true)
	// NUMA0 is ambiguous: both descriptors stay legacy, no guessed quota.
	require.False(t, out[0].TargetDriven)
	require.False(t, out[1].TargetDriven)
	require.Equal(t, 0, out[0].SourceTarget)
	require.Equal(t, 0, out[0].ReclaimQuota)
	require.Equal(t, 0, out[1].SourceTarget)
	require.Equal(t, 0, out[1].ReclaimQuota)
	// NUMA1 single source: upgraded.
	require.True(t, out[2].TargetDriven)
	require.Equal(t, 10, out[2].SourceTarget)
	require.Equal(t, 2, out[2].ReclaimQuota)
}

// TestDistinctBlockIDsOnSameNUMAStayLegacy covers the multi non-exclusive Pod
// safety policy: two different BlockIDs on one NUMA => that NUMA is legacy.
func TestDistinctBlockIDsOnSameNUMAStayLegacy(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "a", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
		{BlockID: "b", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
	}
	ambiguous := identifyAmbiguousTargetDrivenNUMAs(descs)
	require.Contains(t, ambiguous, 0)
	out := applyTargetDrivenReclaimGate(descs, true)
	require.False(t, out[0].TargetDriven)
	require.False(t, out[1].TargetDriven)
}

// TestSameBlockIDSidecarCountsAsSingleSource proves a pod and its sidecar sharing
// one BlockID are treated as a single source, so the NUMA is still target-driven.
func TestSameBlockIDSidecarCountsAsSingleSource(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "shared", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
		{BlockID: "shared", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
	}
	require.Empty(t, identifyAmbiguousTargetDrivenNUMAs(descs))
	out := applyTargetDrivenReclaimGate(descs, true)
	require.True(t, out[0].TargetDriven)
	require.True(t, out[1].TargetDriven)
	require.Equal(t, 10, out[0].SourceTarget)
	require.Equal(t, 2, out[0].ReclaimQuota)
}

// TestPinDerivesTargetDrivenReclaimNUMAFromDedicatedDonor exercises the real chain:
// pinHardReclaimPartitionDemands must mark a reclaim NUMA as target-driven when it
// hosts a target-driven dedicated donor, and then refuse to draw on a legacy donor's
// spare (G2 isolation). Without the fix, targetDrivenReclaimNUMAs was derived from
// the mandatory demand's (always empty) targetDriven field, so a legacy donor would
// have been borrowed.
func TestPinDerivesTargetDrivenReclaimNUMAFromDedicatedDonor(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)

	// targetDriven donor: 4 cpus, frozen target = 4 (it lends nothing), quota 0.
	tdCPUs := coresInNUMA(topology, 0, 0, 2)
	// legacy donor: 4 cpus, requestQuantity = 2 (it has 2 spare).
	legCPUs := coresInNUMA(topology, 0, 2, 4)

	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			eligible: numa, preferred: machine.NewCPUSet(),
		},
		{
			key: "td", requestGroupKey: "td", class: advisorBlockClassDedicated,
			quantity: 4, requestQuantity: 4, eligible: numa, preferred: tdCPUs,
			targetDriven: true, sourceTarget: 4, reclaimQuota: 0, numaID: 0,
		},
		{
			key: "leg", requestGroupKey: "leg", class: advisorBlockClassDedicated,
			quantity: 4, requestQuantity: 2, eligible: numa, preferred: legCPUs,
			targetDriven: false, numaID: 0,
		},
	}

	// The reclaim target on NUMA0 is target-driven (NUMA0 hosts a targetDriven
	// donor). It must only draw on targetDriven donors; td has quota 0, so no donor
	// can lend. The legacy donor's spare must NOT be used -> infeasible.
	_, err = pinHardReclaimPartitionDemands(demands, tdCPUs.Union(legCPUs), topology, false)
	require.Error(t, err)
}

// TestPinTargetDrivenDonorSatisfiesReclaim is the positive counterpart: a
// targetDriven donor with spare quota satisfies the reclaim target and keeps its
// frozen floor.
func TestPinTargetDrivenDonorSatisfiesReclaim(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)

	tdCPUs := coresInNUMA(topology, 0, 0, 2) // 4 cpus, 2 cores
	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			eligible: numa, preferred: machine.NewCPUSet(),
		},
		{
			key: "td", requestGroupKey: "td", class: advisorBlockClassDedicated,
			quantity: 4, requestQuantity: 4, eligible: numa, preferred: tdCPUs,
			targetDriven: true, sourceTarget: 2, reclaimQuota: 2, numaID: 0,
		},
	}
	pinned, err := pinHardReclaimPartitionDemands(demands, tdCPUs, topology, false)
	require.NoError(t, err)
	var afterTD machine.CPUSet
	for _, d := range pinned {
		if d.key == "td" {
			afterTD = d.preferred
		}
	}
	// donor retains exactly its frozen target of 2 cpus (one whole core).
	require.Equal(t, 2, afterTD.Size())
	requireCoreAligned(t, topology, afterTD)
}

// TestMixedPolicyGroupPerNUMAImmunity proves a single group can be target-driven on
// NUMA0 and legacy on NUMA1 without contamination: the valid assignment shrinks the
// NUMA0 source to its frozen target while leaving the NUMA1 legacy source untouched.
func TestMixedPolicyGroupPerNUMAImmunity(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)

	// g0: targetDriven on NUMA0, old footprint 10, frozen target 8.
	g0Old := coresInNUMA(topology, 0, 0, 5)
	require.Equal(t, 10, g0Old.Size())
	// g1: legacy on NUMA1, footprint 10 (unchanged).
	g1Old := coresInNUMA(topology, 1, 0, 5)
	require.Equal(t, 10, g1Old.Size())

	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 2,
			eligible: numa0, preferred: machine.NewCPUSet(),
		},
		{
			key: "g0", requestGroupKey: "g", class: advisorBlockClassDedicated,
			quantity: 8, requestQuantity: 10, eligible: numa0, preferred: g0Old,
			targetDriven: true, sourceTarget: 8, reclaimQuota: 2, numaID: 0,
		},
		{
			key: "g1", requestGroupKey: "g", class: advisorBlockClassDedicated,
			quantity: 10, requestQuantity: 10, eligible: numa1, preferred: g1Old,
			targetDriven: false, numaID: 1,
		},
	}
	// Valid: g0 shrinks to 8 (frozen), releasing one whole core (2 cpus) to reclaim;
	// g1 keeps 10 on NUMA1.
	g0After := coresInNUMA(topology, 0, 0, 4)
	require.Equal(t, 8, g0After.Size())
	reclaimed := coresInNUMA(topology, 0, 4, 5)
	require.Equal(t, 2, reclaimed.Size())
	assignments := map[string]machine.CPUSet{
		"rec": reclaimed,
		"g0":  g0After,
		"g1":  g1Old.Clone(),
	}
	proof, err := validateHardReclaimReplacement(demands, assignments, topology, map[int]int{0: 2})
	require.NoError(t, err)
	require.Equal(t, 8, proof.dedicatedAfterByGroup["g"].Intersection(numa0).Size())
	require.Equal(t, 10, proof.dedicatedAfterByGroup["g"].Intersection(numa1).Size())
}

// TestMixedPolicyGroupLegacyNUMABeforeAfterEnforced proves an assignment that breaks
// the legacy NUMA1 before==after invariant is rejected, even though NUMA0 of the
// same group is target-driven.
func TestMixedPolicyGroupLegacyNUMABeforeAfterEnforced(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)

	g0Old := coresInNUMA(topology, 0, 0, 4) // 8 cpus on NUMA0
	g1Old := coresInNUMA(topology, 1, 0, 5) // 10 cpus on NUMA1

	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 0,
			eligible: numa0, preferred: machine.NewCPUSet(),
		},
		{
			key: "g0", requestGroupKey: "g", class: advisorBlockClassDedicated,
			quantity: 8, requestQuantity: 8, eligible: numa0, preferred: g0Old,
			targetDriven: true, sourceTarget: 8, reclaimQuota: 0, numaID: 0,
		},
		{
			key: "g1", requestGroupKey: "g", class: advisorBlockClassDedicated,
			quantity: 10, requestQuantity: 10, eligible: numa0.Union(numa1), preferred: g1Old,
			targetDriven: false, numaID: 1,
		},
	}
	// Invalid: g1 (legacy) moves 2 cpus from NUMA1 onto NUMA0. NUMA1 before==after
	// must hold for a legacy NUMA, so the proof rejects this.
	g1After := g1Old.Intersection(numa1).Difference(coresInNUMA(topology, 1, 0, 1)).
		Union(coresInNUMA(topology, 0, 4, 5))
	require.Equal(t, 10, g1After.Size())
	assignments := map[string]machine.CPUSet{
		"rec": machine.NewCPUSet(),
		"g0":  g0Old.Clone(),
		"g1":  g1After,
	}
	_, err = validateHardReclaimReplacement(demands, assignments, topology, map[int]int{0: 0, 1: 0})
	require.Error(t, err)
}

// TestAllLegacyGroupKeepsBeforeAfter ensures the legacy path is unchanged when no
// source is target-driven: before==after ownership is preserved.
func TestAllLegacyGroupKeepsBeforeAfter(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(64, 1, 1)
	require.NoError(t, err)
	numa := topology.CPUDetails.CPUsInNUMANodes(0)

	old := coresInNUMA(topology, 0, 0, 5)
	demands := []partitionDemand{
		{
			key: "rec", class: advisorBlockClassMandatoryReclaim, quantity: 0,
			eligible: numa, preferred: machine.NewCPUSet(),
		},
		{
			key: "d", requestGroupKey: "d", class: advisorBlockClassDedicated,
			quantity: 10, requestQuantity: 10, eligible: numa, preferred: old,
			targetDriven: false, numaID: 0,
		},
	}
	assignments := map[string]machine.CPUSet{
		"rec": machine.NewCPUSet(),
		"d":   old.Clone(),
	}
	_, err = validateHardReclaimReplacement(demands, assignments, topology, map[int]int{0: 0})
	require.NoError(t, err)
}

// TestFakeNUMAExcludedFromGateUpgrade proves FakeNUMA dedicated descriptors never
// become target-driven even when the gate is enabled.
func TestFakeNUMAExcludedFromGateUpgrade(t *testing.T) {
	t.Parallel()
	descs := []advisorBlockDescriptor{
		{BlockID: "real", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 10, OldPreferred: cpus(12)},
		{BlockID: "fake", Class: advisorBlockClassDedicated, NUMAID: commonstate.FakedNUMAID, Quantity: 10, OldPreferred: cpus(12)},
	}
	out := applyTargetDrivenReclaimGate(descs, true)
	require.True(t, out[0].TargetDriven)
	require.False(t, out[1].TargetDriven)
	require.Equal(t, 0, out[1].SourceTarget)
	require.Equal(t, 0, out[1].ReclaimQuota)
}
