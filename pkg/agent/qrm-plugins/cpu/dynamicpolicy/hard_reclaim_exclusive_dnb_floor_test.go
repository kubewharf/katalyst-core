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

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newExclusiveDNBPolicy builds a 128-CPU / 4-NUMA / SMT2 policy with the
// ramp-up hard partition enabled and NUMA-exclusive/disjoint-partition gates
// on, mirroring the production shape under which the per-NUMA reclaim floor is
// enforced. NUMA0 = {0..15,64..79}; core c on NUMA0 = {c, c+64}.
func newExclusiveDNBPolicy(t *testing.T) *DynamicPolicy {
	topology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	dyn.EnableRampUpReclaimHardPartition = true
	dyn.InitialRampUpReclaimCPUSetRatio = 0
	dyn.DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	return p
}

func exclusiveAnnotations() map[string]string {
	return map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}
}

func wholeNUMAMachineState(t *testing.T, p *DynamicPolicy) state.NUMANodeMap {
	machineState := state.NUMANodeMap{}
	for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		machineState[numaID] = &state.NUMANodeState{
			DefaultCPUSet: p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID),
		}
	}
	return machineState
}

func setLiveReclaimPool(t *testing.T, p *DynamicPolicy, cpus machine.CPUSet) {
	require.NoError(t, p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: cpus.Clone(),
		}, false))
}

// shrinkResult shrinks the admission result around the hard reclaim floor and
// returns the post-shrink DNB result, mirroring the production shrink step.
func shrinkResult(t *testing.T, topology *machine.CPUTopology, result, floor machine.CPUSet) machine.CPUSet {
	ai := &state.AllocationInfo{
		AllocationResult:         result.Clone(),
		OriginalAllocationResult: result.Clone(),
	}
	_, err := shrinkAllocationInfoForHardReclaimFloor(topology, ai, floor)
	require.NoError(t, err)
	return ai.AllocationResult
}

// TestDynamicPolicy_exclusiveDNBAnchorsReclaimFloorOnLivePool is the regression
// test for the NUMA-exclusive DNB reclaim-floor bug: when the live, validated
// reclaim pool on a hint NUMA has relocated away from the static reserved
// anchor, the admission floor must be anchored on the live pool. The derived
// floor otherwise lands on the stale anchor, the DNB swallows the live floor,
// and the shadow per-NUMA reclaim-floor guard trips floor_exhausted.
func TestDynamicPolicy_exclusiveDNBAnchorsReclaimFloorOnLivePool(t *testing.T) {
	t.Parallel()

	p := newExclusiveDNBPolicy(t)
	topology := p.machine.machineInfo.CPUTopology

	// Static reserved anchor = core0 {0,64}; live, relocated reclaim = core14 {14,78}.
	p.reclaim.reservedReclaimedCPUSet = coresInNUMA(topology, 0, 0, 1)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	liveFloor := coresInNUMA(topology, 0, 14, 15)
	setLiveReclaimPool(t, p, liveFloor)

	machineState := wholeNUMAMachineState(t, p)

	result, floor, err := p.allocateNumaBindingCPUs(
		30, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusiveAnnotations(), true)
	require.NoError(t, err)

	// The floor is anchored on the live relocated reclaim, not the stale anchor.
	require.True(t, liveFloor.IsSubsetOf(floor), "floor=%s want live=%s", floor, liveFloor)
	require.True(t, floor.Equals(liveFloor), "floor=%s live=%s", floor, liveFloor)

	shrunk := shrinkResult(t, topology, result, floor)
	// The live floor is preserved (not swallowed) and the DNB still gets 30 CPUs.
	require.True(t, shrunk.Intersection(liveFloor).IsEmpty(),
		"live floor swallowed: shrunk=%s live=%s", shrunk, liveFloor)
	require.Equal(t, 30, shrunk.Size(), "shrunk=%s", shrunk)
	require.True(t, shrunk.Union(floor).Equals(result))
}

// TestDynamicPolicy_exclusiveDNBAnchorEqualsLiveLeavesFloor verifies that when
// the static reserved anchor already equals the live reclaim pool on the hinted
// NUMA, the derived floor is unchanged (no spurious re-anchor).
func TestDynamicPolicy_exclusiveDNBAnchorEqualsLiveLeavesFloor(t *testing.T) {
	t.Parallel()

	p := newExclusiveDNBPolicy(t)
	topology := p.machine.machineInfo.CPUTopology

	anchor := coresInNUMA(topology, 0, 0, 1)
	p.reclaim.reservedReclaimedCPUSet = anchor.Clone()
	p.reclaim.reservedReclaimedCPUsSize = anchor.Size()
	setLiveReclaimPool(t, p, anchor.Clone())

	machineState := wholeNUMAMachineState(t, p)

	result, floor, err := p.allocateNumaBindingCPUs(
		2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusiveAnnotations(), true)
	require.NoError(t, err)
	require.True(t, anchor.IsSubsetOf(floor), "floor=%s anchor=%s", floor, anchor)
	shrunk := shrinkResult(t, topology, result, floor)
	require.True(t, shrunk.Intersection(anchor).IsEmpty(), "shrunk=%s", shrunk)
}

// TestDynamicPolicy_exclusiveDNBLiveFloorOnUnrelatedNUMAUnaffected verifies
// that a live reclaim pool on a NUMA the DNB does not hint is not pulled into
// the hinted-NUMA floor, and pre-existing occupancy is otherwise unaffected.
func TestDynamicPolicy_exclusiveDNBLiveFloorOnUnrelatedNUMAUnaffected(t *testing.T) {
	t.Parallel()

	p := newExclusiveDNBPolicy(t)
	topology := p.machine.machineInfo.CPUTopology

	p.reclaim.reservedReclaimedCPUSet = coresInNUMA(topology, 0, 0, 1)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	// Live reclaim lives on NUMA1; the DNB hints NUMA0 only.
	setLiveReclaimPool(t, p, coresInNUMA(topology, 1, 0, 1))

	machineState := wholeNUMAMachineState(t, p)

	result, floor, err := p.allocateNumaBindingCPUs(
		2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusiveAnnotations(), true)
	require.NoError(t, err)
	// The hinted NUMA0 floor must not reach into NUMA1's live reclaim.
	require.True(t, floor.Intersection(topology.CPUDetails.CPUsInNUMANodes(1)).IsEmpty(),
		"floor=%s leaked into NUMA1", floor)
	shrunk := shrinkResult(t, topology, result, floor)
	require.Equal(t, result.Size()-floor.Intersection(result).Size(), shrunk.Size())
}

// TestDynamicPolicy_exclusiveDNBSkipsLiveCoreWithProtectedSibling verifies the
// HP / non-reclaimable gate: a live reclaim core whose sibling is protected
// (disable-reclaim) is skipped whole rather than forced back into the floor.
func TestDynamicPolicy_exclusiveDNBSkipsLiveCoreWithProtectedSibling(t *testing.T) {
	t.Parallel()

	p := newExclusiveDNBPolicy(t)
	topology := p.machine.machineInfo.CPUTopology

	p.reclaim.reservedReclaimedCPUSet = coresInNUMA(topology, 0, 0, 1)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	// Live reclaim core14 = {14,78}. Pin CPU 78 (the sibling) as protected.
	liveFloor := coresInNUMA(topology, 0, 14, 15)
	setLiveReclaimPool(t, p, liveFloor)
	protectedSibling := liveFloor.ToSliceInt()[1] // CPU 78

	machineState := wholeNUMAMachineState(t, p)
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		"protected": {
			Attributes:   map[string]string{"disable-reclaim": "true"},
			PinnedCPUSet: machine.NewCPUSet(protectedSibling),
		},
	}

	result, floor, err := p.allocateNumaBindingCPUs(
		2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusiveAnnotations(), true)
	require.NoError(t, err)
	// The protected sibling must never appear in the floor.
	require.True(t, floor.Intersection(machine.NewCPUSet(protectedSibling)).IsEmpty(),
		"floor=%s protected=%d", floor, protectedSibling)
	requireCoreAligned(t, topology, floor)
	_ = result
}

// TestDynamicPolicy_exclusiveDNBAnchorFloorUnderSteadyPath is the regression test
// for cont8: a NUMA-exclusive DNB whose own pod is NOT reclaim-enabled still takes
// over a hint NUMA whose committed live reclaim floor must be preserved. 616d only
// re-anchored the hinted-NUMA floor onto the live reclaim pool on the
// ramp-up/podReclaimEnabled path; the steady (podReclaimEnabled=false) path was
// left anchored on the static reserved identity, so the relocated live floor on
// NUMA0 was swallowed by the DNB and the shadow per-NUMA floor guard tripped
// floor_exhausted. Shape mirrors cont8: 128 CPU / 4 NUMA / SMT2, a static core0
// anchor on every NUMA, and the NUMA0 live core relocated from core0 {0,64} to
// core14 {14,78}.
func TestDynamicPolicy_exclusiveDNBAnchorFloorUnderSteadyPath(t *testing.T) {
	t.Parallel()

	p := newExclusiveDNBPolicy(t)
	topology := p.machine.machineInfo.CPUTopology

	// Static reserved anchor = core0 on every NUMA: {0,64},{16,80},{32,96},{48,112}.
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 64, 16, 80, 32, 96, 48, 112)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	// Live pool: NUMA0 relocated to core14 {14,78}; the other NUMAs stay put.
	liveNUMA0 := coresInNUMA(topology, 0, 14, 15)
	setLiveReclaimPool(t, p, machine.NewCPUSet().
		Union(liveNUMA0).
		Union(coresInNUMA(topology, 1, 16, 17)).
		Union(coresInNUMA(topology, 2, 32, 33)).
		Union(coresInNUMA(topology, 3, 48, 49)))

	machineState := wholeNUMAMachineState(t, p)

	// podReclaimEnabled = false: the steady eligibility path that cont8 exercises.
	result, floor, err := p.allocateNumaBindingCPUs(
		30, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusiveAnnotations(), false)
	require.NoError(t, err)

	// The NUMA0 floor must be anchored on the relocated live core {14,78}, not the
	// stale static anchor {0,64}.
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	floorNUMA0 := floor.Intersection(numa0)
	require.True(t, liveNUMA0.IsSubsetOf(floorNUMA0),
		"floor NUMA0=%s want live=%s", floorNUMA0, liveNUMA0)
	require.False(t, floorNUMA0.Intersection(coresInNUMA(topology, 0, 0, 1)).
		Equals(coresInNUMA(topology, 0, 0, 1)), "stale anchor still in floor: floor=%s", floorNUMA0)

	shrunk := shrinkResult(t, topology, result, floor)
	// The relocated live floor is preserved (not swallowed) and the DNB gets 30 CPUs.
	require.True(t, shrunk.Intersection(liveNUMA0).IsEmpty(),
		"live floor swallowed: shrunk=%s live=%s", shrunk, liveNUMA0)
	require.Equal(t, 30, shrunk.Size(), "shrunk=%s", shrunk)
}
