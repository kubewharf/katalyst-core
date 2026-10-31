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

package utils

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// e2e128CPUDetails mirrors the production single-SNB gen9 memory layout used by
// the E2E stall repro:
//
//	NUMA0={0-15,64-79}, NUMA1={16-31,80-95}, NUMA2={32-47,96-111}, NUMA3={48-63,112-127}.
//
// CPU c and c+64 share a physical core (SMT pair); coreID = numa*16 + cpu%16.
// This is the exact 128-CPU / 4-NUMA production shape.
func e2e128CPUDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	numaByBlock := []int{0, 1, 2, 3, 0, 1, 2, 3}
	for cpu := 0; cpu < 128; cpu++ {
		block := cpu / 16
		numa := numaByBlock[block]
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: numa, SocketID: numa, CoreID: numa*16 + cpu%16}
	}
	return details
}

func e2e128Topology() *machine.CPUTopology {
	return &machine.CPUTopology{
		NumCPUs:      128,
		NumCores:     64,
		NumSockets:   1,
		NumNUMANodes: 4,
		CPUDetails:   e2e128CPUDetails(),
	}
}

// e2e128Reclaim is the steady-state reclaim set: 2 whole cores per NUMA,
// confined entirely inside one NUMA. NUMA0 bucket {4,68}.
func e2e128Reclaim() machine.CPUSet {
	return machine.MustParse("4,20,36,52,68,84,100,116")
}

// TestAdmissionProjectionAuthorityUsesAppliedValidatedNonReclaim pins the actual
// admission projection authority fix on the production 128-CPU / 4-NUMA shape.
//
// Scenario (reconstructed from the E2E diagnostic at fc7a):
//
//   - Pre-admission AppliedView (the manager-owned validated prior partition):
//     reclaim8 incl NUMA0 bucket {4,68}; its validated NonReclaimPool is
//     0-127 minus reclaim8 = 120 CPUs, i.e. NUMA0 non-reclaim = {0-15,64-79}
//     minus {4,68} = 30 CPUs.
//   - DesiredView after the pending allocation: reclaim6 with NUMA0 removed
//     (buckets 1-3 only = {20,36,52,84,100,116}); its DesiredNonReclaimPool has
//     self-expanded to 0-127 minus reclaim6 = 122 CPUs, which already contains
//     the WHOLE NUMA0 slice {0-15,64-79} (32 CPUs). The business allocation
//     result stays 32 and is never mutated.
//   - The pending protection is the whole NUMA0 slice (32 CPUs), overlapping the
//     applied NUMA0 reclaim bucket {4,68}.
//
// Before the fix the projection clipped the pending whole-NUMA scope against the
// self-expanded DesiredNonReclaimPool (which already contains whole NUMA0), so
// the projection intersected the scope with itself and stayed at 32. That let the
// primary widen onto the {4,68} reclaim bucket, dropped reclaimed-0 out of the
// spec / required floor, and stalled at a bare ErrNoProgress with zero writes.
//
// The fix clips against the complement of the pre-admission AppliedView's
// validated ReclaimEffective instead. The pending whole-NUMA0 intersects the
// validated 30 on NUMA0, so the projection becomes 30 = {0-3,5-15,64-67,69-79}: the {4,68} reclaim bucket is
// retained in the spec / required floor and Converge writes the leaf shrink
// 32 -> 30 to drain {4,68} into the already-enforced bucket.
func TestAdmissionProjectionAuthorityUsesAppliedValidatedNonReclaim(t *testing.T) {
	t.Parallel()

	topo := e2e128Topology()
	reclaim := e2e128Reclaim()
	all := machine.MustParse("0-127")
	numa0 := machine.MustParse("0-15,64-79")                 // whole NUMA0 = 32
	sharedNUMA0 := machine.MustParse("0-3,5-15,64-67,69-79") // NUMA0 minus {4,68} = 30

	// Pre-admission validated prior partition: reclaim8 incl {4,68}.
	applied := &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:          machine.NewCPUSet(),
			NonReclaimPool:   all.Difference(reclaim).Clone(), // 120
			ReclaimEffective: reclaim.Clone(),                 // 8
		},
		Level: model.AppliedViewLevelFull,
	}

	// DesiredView after the pending allocation: reclaim6 (NUMA0 removed).
	// DesiredNonReclaimPool self-expanded to 122 and already contains whole NUMA0.
	desiredReclaim := machine.MustParse("20,36,52,84,100,116") // buckets 1-3
	desired := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:                 machine.NewCPUSet(),
			DesiredNonReclaimPool:   all.Difference(desiredReclaim).Clone(), // 122, contains whole NUMA0
			DesiredReclaimEffective: desiredReclaim.Clone(),                 // 6
			NonReclaimPool:          all.Difference(reclaim).Clone(),        // materialized validated 120
			ReclaimEffective:        reclaim.Clone(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}

	protections := []topology.PendingProtection{
		pendingScope("kubepods/pod840b", "pod840b", numa0.Clone()),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(desired, topo, protections, applied)
	require.Len(t, got, 1)

	// The projection must clip to the validated non-reclaim (30), NOT stay at the
	// self-expanded business non-reclaim (32).
	require.Equal(t, sharedNUMA0, got[0].CPUs,
		"projected pending must be the validated 30 (whole NUMA0 minus applied reclaim {4,68})")
	require.Equal(t, sharedNUMA0, got[0].SharedProjectedCPUs,
		"shared-projected must equal the clipped 30")

	// The business DesiredView is untouched: its self-expanded DesiredNonReclaimPool
	// still holds whole NUMA0 (business allocation 32 unchanged).
	require.True(t, numa0.IsSubsetOf(desired.DesiredNonReclaimPool),
		"business DesiredNonReclaimPool must remain business truth (whole NUMA0)")
}

// TestAdmissionProjectionAuthorityNilAppliedViewFailClosed guards the
// nil/initial applied view contract: with no manager-owned validated prior
// partition and no materialized validated fallback, there is nothing safe to
// clip against, so the pending protection must be preserved verbatim as an exact
// seed (fail-closed) rather than clipped against the self-expanded business
// DesiredNonReclaimPool (which would widen onto a reclaim bucket).
func TestAdmissionProjectionAuthorityNilAppliedViewFailClosed(t *testing.T) {
	t.Parallel()

	topo := e2e128Topology()
	numa0 := machine.MustParse("0-15,64-79")

	// Business view self-expanded to whole NUMA0, but no validated prior partition
	// (applied view nil, materialized NonReclaimPool empty).
	desired := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:               machine.NewCPUSet(),
			DesiredNonReclaimPool: numa0.Clone(),
			NonReclaimPool:        machine.NewCPUSet(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{},
	}

	protections := []topology.PendingProtection{
		pendingScope("kubepods/pod840b", "pod840b", numa0.Clone()),
	}

	got := ProjectPendingProtectionsToDesiredNonReclaim(desired, topo, protections, nil)
	require.Len(t, got, 1)
	require.Equal(t, numa0, got[0].CPUs,
		"no validated authority => pending preserved verbatim as exact (fail-closed)")
	require.True(t, got[0].SharedProjectedCPUs.IsEmpty(),
		"shared-projected must stay empty for a preserved exact seed")
}

func TestShadowAppliedFloorYieldsToPendingDedicatedOwnerTransfer(t *testing.T) {
	t.Parallel()

	topo := e2e128Topology()
	all := topo.CPUDetails.CPUs()
	numa0 := topo.CPUDetails.CPUsInNUMANodes(0)
	oldFloor := machine.MustParse("4,20,68,84")
	desiredFloor := machine.MustParse("20,84")
	const podUID = "pending-dedicated-owner"

	desired := &model.DesiredView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:                        machine.NewCPUSet(),
			Dedicated:                      numa0.Clone(),
			DesiredNonReclaimPool:          all.Difference(desiredFloor),
			DesiredReclaimEffective:        desiredFloor.Clone(),
			DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{1: desiredFloor.Clone()},
			NonReclaimPool:                 all.Difference(oldFloor),
			ReclaimEffective:               oldFloor.Clone(),
		},
		PoolOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
			{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "owner"}: {
				ProofPodUID:    podUID,
				ExpectedCPUSet: numa0.Clone(),
			},
		},
	}
	applied := &model.CPUSetPartitionView{
		Reserve:          machine.NewCPUSet(),
		NonReclaimPool:   all.Difference(oldFloor),
		ReclaimEffective: oldFloor.Clone(),
	}
	protections := []topology.PendingProtection{{
		PodUID: podUID,
		CPUs:   numa0.Clone(),
		Source: topology.PendingProtectionSourceExpectedPod,
	}}

	shadow := DeriveShadowPartitionWithAppliedEnforcement(desired, topo, protections, applied)
	require.False(t, shadow.HoldCurrent, "pending dedicated ownership must not retain a superseded applied floor: %s", shadow.HoldReason)
	require.Equal(t, desiredFloor, shadow.ReclaimEffective)
	require.Equal(t, desiredFloor, shadow.ReclaimEffectivePerNUMA[1])
	require.True(t, shadow.ReclaimEffectivePerNUMA[0].IsEmpty())
}
