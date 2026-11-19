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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func requireReclaimOnEveryNUMA(t *testing.T, topology *machine.CPUTopology, reclaim machine.CPUSet) {
	t.Helper()
	require.NoError(t, assertCoreAligned(reclaim, topology))
	for _, n := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		require.Falsef(t, reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(n)).IsEmpty(),
			"NUMA %d lost reclaim: %s", n, reclaim)
	}
}

func TestColdStartReclaimCoversEveryEligibleNUMA(t *testing.T) {
	t.Parallel()

	highNUMA, err := machine.GenerateDummyCPUTopology(96, 2, 24)
	require.NoError(t, err)
	for _, topology := range []*machine.CPUTopology{highNUMA, handBuiltMixedSMT(t)} {
		for _, reserved := range []machine.CPUSet{machine.NewCPUSet(), machine.NewCPUSet(0)} {
			topology, reserved := topology, reserved
			t.Run(fmt.Sprintf("NUMAs=%d/reserved=%s", topology.NumNUMANodes, reserved), func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				conf := freshRealConstructorConf(t, dir)
				conf.ReservedCPUList = reserved.String()
				ok, _, err := NewDynamicPolicy(freshRealConstructorContext(t, topology, dir), conf, nil, "cold-reclaim-domains")
				require.NoError(t, err)
				require.True(t, ok)
				restored := openCheckpointState(t, topology, dir)
				reclaim := reclaimPoolCPUSet(restored.GetPodEntries())
				requireReclaimOnEveryNUMA(t, topology, reclaim)
				require.True(t, reclaim.Intersection(reserved).IsEmpty())
				ok, _, err = NewDynamicPolicy(freshRealConstructorContext(t, topology, dir), conf, nil, "cold-reclaim-domains")
				require.NoError(t, err)
				require.True(t, ok)
				require.True(t, reclaim.Equals(reclaimPoolCPUSet(openCheckpointState(t, topology, dir).GetPodEntries())),
					"restart must preserve committed reclaim identity")
			})
		}
	}
}

func TestReclaimPresenceKeepsLastDonorCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
	require.NoError(t, err)
	prior := machine.MustParse("0,16,32-33,48,64,80,96-97,112")
	selected := machine.MustParse("0,16,32-33,64,80,96-97")
	got, err := reclaimNUMAPresence(topology, selected, prior, topology.CPUDetails.CPUs())
	require.NoError(t, err)
	require.Equal(t, selected.Size(), got.Size())
	requireReclaimOnEveryNUMA(t, topology, got)

	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 8
	p.reclaim.reservedReclaimedCPUSet = prior
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim), AllocationResult: prior,
		}},
	}, false))
	pools, isolated, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 120}},
		map[string]map[string]int{}, topology.CPUDetails.CPUs(), nil,
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn})
	require.NoError(t, err)
	requireReclaimOnEveryNUMA(t, topology, pools[commonstate.PoolNameReclaim])
	require.NoError(t, p.applyPoolsAndIsolatedInfo(pools, isolated, p.state.GetPodEntries(),
		p.state.GetMachineState(), nil, true, machine.NewCPUSet(), defaultShareMaterializationPlan{}, p.state.GetRevision(), dyn))
	requireReclaimOnEveryNUMA(t, topology, reclaimPoolCPUSet(p.state.GetPodEntries()))
}

func TestSaturatedShareReservesEveryNUMA(t *testing.T) {
	t.Parallel()

	for _, numas := range []int{2, 4} {
		for _, withHistory := range []bool{false, true} {
			for _, demand := range []int{128, 160} {
				numas, withHistory, demand := numas, withHistory, demand
				t.Run(fmt.Sprintf("numas=%d/history=%v/demand=%d", numas, withHistory, demand), func(t *testing.T) {
					t.Parallel()
					topology, err := machine.GenerateDummyCPUTopology(128, 1, numas)
					require.NoError(t, err)
					p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
					require.NoError(t, err)
					p.config.reservedCPUs = machine.NewCPUSet()
					prior := machine.NewCPUSet()
					for _, n := range topology.CPUDetails.NUMANodes().ToSliceInt() {
						core, err := selectReclaimSeed(topology, topology.CPUDetails.CPUsInNUMANodes(n), machine.NewCPUSet(), 0)
						require.NoError(t, err)
						prior = prior.Union(core)
					}
					p.reclaim.reservedReclaimedCPUsSize = prior.Size()
					p.reclaim.reservedReclaimedCPUSet = prior
					dyn := p.config.dynamicConfig.GetDynamicConfiguration()
					dyn.EnableReclaim = true
					require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
					require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
					if withHistory {
						require.NoError(t, p.state.SetPodEntries(state.PodEntries{
							commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
								AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim), AllocationResult: prior,
							}},
						}, false))
					}
					pools, isolated, err := p.generatePoolsAndIsolation(
						map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: demand}},
						map[string]map[string]int{}, topology.CPUDetails.CPUs(), nil,
						generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn})
					require.NoError(t, err)
					requireReclaimOnEveryNUMA(t, topology, pools[commonstate.PoolNameReclaim])
					require.True(t, pools[commonstate.PoolNameReclaim].Intersection(pools[commonstate.PoolNameShare]).IsEmpty())
					require.NoError(t, p.applyPoolsAndIsolatedInfo(pools, isolated, p.state.GetPodEntries(),
						p.state.GetMachineState(), nil, true, machine.NewCPUSet(), defaultShareMaterializationPlan{}, p.state.GetRevision(), dyn))
					requireReclaimOnEveryNUMA(t, topology, reclaimPoolCPUSet(p.state.GetPodEntries()))
				})
			}
		}
	}
}

func TestReclaimPresenceMultipleMissingAndInsufficientBudget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
	require.NoError(t, err)
	prior := machine.MustParse("0,16,32,48,64,80,96,112")
	for _, selected := range []machine.CPUSet{
		machine.MustParse("0-3,64-67"),
		machine.MustParse("0-2,64-66"),
	} {
		before := selected.Clone()
		result, err := reclaimNUMAPresence(topology, selected, prior, topology.CPUDetails.CPUs())
		if selected.Size() < prior.Size() {
			require.Error(t, err, "cannot fill four domains from only three whole cores")
		} else {
			require.NoError(t, err)
			require.Equal(t, selected.Size(), result.Size())
			requireReclaimOnEveryNUMA(t, topology, result)
		}
		require.True(t, before.Equals(selected), "failure and success must not mutate input")
	}
	_, err = reclaimNUMAPresence(topology, machine.MustParse("0-3,64-67"), prior,
		topology.CPUDetails.CPUs().Difference(machine.MustParse("48-63,112-127")).Union(machine.NewCPUSet(48)))
	require.Error(t, err, "a single SMT sibling is not replacement capacity")
}

func TestSaturatedShareRejectsNUMAWithoutWholeCore(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
	before, revision := p.state.GetPodEntries(), p.state.GetRevision()
	available := topology.CPUDetails.CPUsInNUMANodes(0).Union(machine.NewCPUSet(4))
	_, _, err = p.generatePoolsAndIsolation(map[string]map[int]int{
		commonstate.PoolNameShare: {commonstate.FakedNUMAID: 16},
	}, nil, available, nil, generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn})
	require.Error(t, err)
	require.Equal(t, before, p.state.GetPodEntries())
	require.Equal(t, revision, p.state.GetRevision())
}

func TestExplicitReclaimPreservesEveryCommittedNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	prior := machine.NewCPUSet()
	for _, n := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		core, err := selectReclaimSeed(topology, topology.CPUDetails.CPUsInNUMANodes(n), machine.NewCPUSet(), 0)
		require.NoError(t, err)
		prior = prior.Union(core)
	}
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = prior.Size()
	p.reclaim.reservedReclaimedCPUSet = prior
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim), AllocationResult: prior,
		}},
	}, false))
	for _, demand := range []int{2, 12} {
		pools, isolated, err := p.generatePoolsAndIsolation(
			map[string]map[int]int{
				commonstate.PoolNameShare:   {commonstate.FakedNUMAID: demand},
				commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: prior.Size()},
			},
			nil, topology.CPUDetails.CPUs(), nil,
			generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn})
		require.NoError(t, err)
		requireReclaimOnEveryNUMA(t, topology, pools[commonstate.PoolNameReclaim])
		require.NoError(t, p.applyPoolsAndIsolatedInfo(pools, isolated, p.state.GetPodEntries(),
			p.state.GetMachineState(), nil, true, machine.NewCPUSet(), defaultShareMaterializationPlan{}, p.state.GetRevision(), dyn))
		requireReclaimOnEveryNUMA(t, topology, reclaimPoolCPUSet(p.state.GetPodEntries()))
	}

	// The caller already removed and debited an explicit ramp floor. Only
	// domains not covered by that floor need budget in this allocator.
	floor := prior.Intersection(topology.CPUDetails.CPUsInNUMANodes(0))
	pools, _, err := p.generatePoolsAndIsolation(map[string]map[int]int{
		commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: prior.Size() - floor.Size()},
	}, nil, topology.CPUDetails.CPUs().Difference(floor), nil,
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn, rampUpReclaimFloor: floor})
	require.NoError(t, err)
	requireReclaimOnEveryNUMA(t, topology, pools[commonstate.PoolNameReclaim].Union(floor))

	// An explicit quantity smaller than one whole core per committed NUMA is
	// infeasible and must fail before mutating state.
	small := prior.Intersection(topology.CPUDetails.CPUsInNUMANodes(0))
	p2, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p2.config.reservedCPUs = machine.NewCPUSet()
	p2.reclaim.reservedReclaimedCPUsSize = small.Size()
	p2.reclaim.reservedReclaimedCPUSet = small
	p2.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p2.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p2.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p2.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim), AllocationResult: prior,
		}},
	}, false))
	before, revision := p2.state.GetPodEntries(), p2.state.GetRevision()
	for _, demand := range []int{2, 12} {
		_, _, err = p2.generatePoolsAndIsolation(
			map[string]map[int]int{
				commonstate.PoolNameShare:   {commonstate.FakedNUMAID: demand},
				commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: small.Size()},
			},
			nil, topology.CPUDetails.CPUs(), nil,
			generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p2.config.dynamicConfig.GetDynamicConfiguration()})
		require.Error(t, err)
		require.Equal(t, before, p2.state.GetPodEntries())
		require.Equal(t, revision, p2.state.GetRevision())
	}
}

func TestPoolCommitCannotUseSoftShareToExcuseMissingReclaimNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
	prior := machine.MustParse("0,16,32,48,64,80,96,112")
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim), AllocationResult: prior,
		}},
	}, true))
	before := p.state.GetPodEntries()
	revision := p.state.GetRevision()
	reclaim := machine.MustParse("0,64")
	err = p.applyPoolsAndIsolatedInfo(map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: reclaim,
		commonstate.PoolNameShare:   topology.CPUDetails.CPUs().Difference(reclaim),
	}, nil, before, p.state.GetMachineState(), nil, true,
		machine.NewCPUSet(), defaultShareMaterializationPlan{}, revision, dyn)
	require.Error(t, err)
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, before, p.state.GetPodEntries())
}

func TestConfiguredReclaimPresenceCountsReservedFloorOnce(t *testing.T) {
	t.Parallel()

	// A complete caller-owned floor covers its NUMA without being reallocated
	// from the residual budget; the opposite NUMA still needs its own core.
	for _, tc := range []struct {
		floorNUMA int
		partial   bool
	}{{0, false}, {1, false}, {0, true}, {1, true}} {
		tc := tc
		t.Run(fmt.Sprintf("floor NUMA %d/partial=%v", tc.floorNUMA, tc.partial), func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUsSize = 4
			dyn := p.config.dynamicConfig.GetDynamicConfiguration()
			dyn.EnableReclaim = true
			require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
			require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false))
			floorDomain := topology.CPUDetails.CPUsInNUMANodes(tc.floorNUMA)
			residualDomain := topology.CPUDetails.CPUsInNUMANodes(1 - tc.floorNUMA)
			floor, err := selectReclaimSeed(topology, floorDomain, machine.NewCPUSet(), 0)
			require.NoError(t, err)
			prior := floor.Union(residualDomain)
			if tc.partial {
				floor = machine.NewCPUSet(floor.ToSliceInt()[0])
			}
			require.NoError(t, p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName,
				&state.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: prior,
				}, false))
			before, revision := p.state.GetPodEntries(), p.state.GetRevision()
			pools, _, err := p.generatePoolsAndIsolation(map[string]map[int]int{
				"binding": {tc.floorNUMA: 8}, "ordinary": {commonstate.FakedNUMAID: 4},
			}, nil, topology.CPUDetails.CPUs().Difference(floor), nil,
				generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn, rampUpReclaimFloor: floor})
			if tc.partial {
				require.Error(t, err, "partial floor must not exempt its NUMA")
				require.Equal(t, before, p.state.GetPodEntries())
				require.Equal(t, revision, p.state.GetRevision())
				return
			}
			require.NoError(t, err, "complete floor must not be requested again from residual reclaim")
			reclaim := pools[commonstate.PoolNameReclaim]
			require.True(t, reclaim.Intersection(floor).IsEmpty())
			requireReclaimOnEveryNUMA(t, topology, reclaim.Union(floor))
			require.True(t, pools["binding"].IsSubsetOf(floorDomain.Difference(floor)))
			require.True(t, reclaim.Union(floor).Intersection(pools["binding"].Union(pools["ordinary"])).IsEmpty())
			require.Equal(t, before, p.state.GetPodEntries())
			require.Equal(t, revision, p.state.GetRevision())
		})
	}
}

func TestSoftReclaimPresenceAvoidsNUMABindingOwnership(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	prior := machine.MustParse("0,4,8,12")

	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = prior.Size()
	p.reclaim.reservedReclaimedCPUSet = prior.Clone()
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	dyn.EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: prior,
		}},
	}, false))

	pools, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"binding":                 {0: 1},
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 12},
		},
		nil, topology.CPUDetails.CPUs(), nil,
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: dyn})
	require.NoError(t, err)

	reclaim := pools[commonstate.PoolNameReclaim]
	binding := pools["binding"]
	requireReclaimOnEveryNUMA(t, topology, reclaim)
	require.True(t, reclaim.Intersection(binding).IsEmpty(),
		"reclaim %s overlaps NUMA-binding ownership %s", reclaim, binding)
}
