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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// newPoolPlacementPolicy builds an isolated policy on a synthetic 96-CPU SMT2
// fixture. GenerateDummyCPUTopology(96, 2, 2) pairs siblings at i and i+48 and
// splits the two NUMA nodes as 0..23/48..71 and 24..47/72..95; this fixture is
// purely synthetic and must not be read as any original machine NUMA layout.
func newPoolPlacementPolicy(t *testing.T) *DynamicPolicy {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-pool-placement")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))
	return p
}

// seedPoolEntry writes a committed faked pool entry whose AllocationResult is
// the pool's historical placement.
func seedPoolEntry(entries state.PodEntries, poolName string, cset machine.CPUSet) {
	entries[poolName] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(poolName),
			AllocationResult: cset.Clone(),
		},
	}
}

// TestDynamicPolicy_generatePoolsAndIsolation_namedPoolReusesOwnHistoryEqualQty
// drives the real generatePoolsAndIsolation entry. A generic named shared pool
// that keeps its previous equal quantity must reuse its own historical cpuset
// byte-for-byte; a second named pool on the other NUMA keeps its own history
// instead of being pulled by sorted-order NUMA balancing.
func TestDynamicPolicy_generatePoolsAndIsolation_namedPoolReusesOwnHistoryEqualQty(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	genericpoolAOwn := machine.NewCPUSet(0, 48, 1, 49)
	poolBOwn := machine.NewCPUSet(24, 72, 25, 73)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	seedPoolEntry(entries, "pool-b", poolBOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(
		0, 48, 1, 49, 2, 50, 3, 51,
		24, 72, 25, 73, 26, 74, 27, 75,
	)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["genericpool-a"].Equals(genericpoolAOwn),
		"genericpool-a must reuse its own equal-qty history, got %s want %s",
		poolsCPUSet["genericpool-a"].String(), genericpoolAOwn.String())
	require.True(t, poolsCPUSet["pool-b"].Equals(poolBOwn),
		"pool-b must reuse its own equal-qty history, got %s want %s",
		poolsCPUSet["pool-b"].String(), poolBOwn.String())
	require.True(t, poolsCPUSet["genericpool-a"].Intersection(poolsCPUSet["pool-b"]).IsEmpty())
}

// TestDynamicPolicy_generatePoolsAndIsolation_highHistoryKeptNotFirstFit is the
// permanent generic regression for a high committed own history on non-first-fit
// cores: genericpool-a owns {7,11,55,59} (siblings +48, four valid hardware cores
// across both synthetic NUMAs) and asks 4 cpus. The first-fit distractors
// {0,4,48,52} must not displace the committed history; the pool keeps own
// byte-for-byte instead of landing on first-fit.
func TestDynamicPolicy_generatePoolsAndIsolation_highHistoryKeptNotFirstFit(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	own := machine.NewCPUSet(7, 11, 55, 59)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", own)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 4, 48, 52).Union(own)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	got := poolsCPUSet["genericpool-a"]
	require.True(t, got.Equals(own),
		"genericpool-a must keep committed own history, got %s want %s", got.String(), own.String())
	require.Equal(t, 4, got.Size())
	require.NoError(t, assertCoreAligned(got, p.machine.machineInfo.CPUTopology))
}

// TestDynamicPolicy_generatePoolsAndIsolation_namedPoolsKeepOwnHistorySameNUMANoGrab
// places two named pools' histories on adjacent cores of the same NUMA. The
// earlier-sorted pool must not grab the later pool's historical cores when both
// can satisfy the request from their own tiers.
func TestDynamicPolicy_generatePoolsAndIsolation_namedPoolsKeepOwnHistorySameNUMANoGrab(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	genericpoolAOwn := machine.NewCPUSet(0, 48, 1, 49)
	poolBOwn := machine.NewCPUSet(2, 50, 3, 51)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	seedPoolEntry(entries, "pool-b", poolBOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 3, 51)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["genericpool-a"].Equals(genericpoolAOwn),
		"genericpool-a=%s want %s", poolsCPUSet["genericpool-a"].String(), genericpoolAOwn.String())
	require.True(t, poolsCPUSet["pool-b"].Equals(poolBOwn),
		"pool-b=%s want %s", poolsCPUSet["pool-b"].String(), poolBOwn.String())
}

// TestDynamicPolicy_generatePoolsAndIsolation_namedPoolTrimsWithinOwnHistory
// shrinks a named pool below its history. The trim stays on complete cores taken
// from the own tier instead of spilling outside the historical domain.
func TestDynamicPolicy_generatePoolsAndIsolation_namedPoolTrimsWithinOwnHistory(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	genericpoolAOwn := machine.NewCPUSet(0, 48, 1, 49)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 2},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	got := poolsCPUSet["genericpool-a"]
	require.Equal(t, 2, got.Size())
	require.True(t, got.Intersection(genericpoolAOwn).Equals(got),
		"trim must stay inside own history, got %s own=%s", got.String(), genericpoolAOwn.String())
	require.NoError(t, assertCoreAligned(got, p.machine.machineInfo.CPUTopology))
}

// TestDynamicPolicy_generatePoolsAndIsolation_namedPoolGrowsFromOwnHistory
// grows a named pool above its history. The own whole core is consumed first and
// the extra CPUs fill from the remainder; own history is a soft preference and
// never hard-pins or blocks capacity.
func TestDynamicPolicy_generatePoolsAndIsolation_namedPoolGrowsFromOwnHistory(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	genericpoolAOwn := machine.NewCPUSet(0, 48)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 24, 72, 25, 73)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	got := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, got.Size())
	require.True(t, got.Contains(0) && got.Contains(48),
		"growth must keep the own whole core first, got %s", got.String())
	require.NoError(t, assertCoreAligned(got, p.machine.machineInfo.CPUTopology))
}

// TestBuildRequestedPoolOwnPreferredCPUs_pathScoped freezes own history only for
// pools actually requested on this non-binding allocation path. Eligibility
// follows the requested path (a generic named pool, a common isolation pool, ...),
// not a special-name list; pools absent from the request keep no own tier.
func TestBuildRequestedPoolOwnPreferredCPUs_pathScoped(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", machine.NewCPUSet(0, 48, 1, 49))
	seedPoolEntry(entries, "pool-b", machine.NewCPUSet(24, 72))
	seedPoolEntry(entries, commonstate.PoolNamePrefixIsolation+"-podx", machine.NewCPUSet(6, 54))
	seedPoolEntry(entries, commonstate.PoolNameShare, machine.NewCPUSet(2, 50))
	seedPoolEntry(entries, commonstate.PoolNameReclaim, machine.NewCPUSet(4, 52))

	own := buildPoolPlacementSnapshot(entries, nil, map[string]map[int]int{
		"genericpool-a": {commonstate.FakedNUMAID: 1},
		"pool-b":        {commonstate.FakedNUMAID: 1},
		commonstate.PoolNamePrefixIsolation + "-podx": {commonstate.FakedNUMAID: 1},
	}, nil, machine.NewCPUSet(0, 48, 1, 49, 24, 72, 6, 54), false).ownByPool

	require.True(t, own["genericpool-a"].Equals(machine.NewCPUSet(0, 48, 1, 49)))
	require.True(t, own["pool-b"].Equals(machine.NewCPUSet(24, 72)))
	require.True(t, own[commonstate.PoolNamePrefixIsolation+"-podx"].Equals(machine.NewCPUSet(6, 54)))
	// Not requested on this path: no own tier frozen.
	require.NotContains(t, own, commonstate.PoolNameShare)
	require.NotContains(t, own, commonstate.PoolNameReclaim)
}

// TestDynamicPolicy_generatePoolsAndIsolation_overlapNamedPoolOwnHistoryPreserved
// runs the allow-shared-overlap proportional path separately. The named pool's own
// history still wins over a borrowed isolation CPU, and pools stay disjoint.
func TestDynamicPolicy_generatePoolsAndIsolation_overlapNamedPoolOwnHistoryPreserved(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true))

	genericpoolAOwn := machine.NewCPUSet(0, 48, 1, 49)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	// A shared_cores isolation container historically borrowed 2,50 from genericpool-a.
	entries["podx"] = state.ContainerEntries{
		"containerx": &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "podx",
				ContainerName: "containerx",
				OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-podx",
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				Annotations: map[string]string{
					apiconsts.PodAnnotationCPUEnhancementCPUSet: "genericpool-a",
				},
			},
			AllocationResult: machine.NewCPUSet(2, 50),
		},
	}
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 3, 51)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{"genericpool-a": 0.5},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	got := poolsCPUSet["genericpool-a"]
	// The overlap branch scales the pool over every available CPU; own history
	// stays present ahead of the borrowed cpus instead of forcing a no-grow
	// exact match, which this branch cannot provide.
	require.Equal(t, available.Size(), got.Size())
	require.True(t, got.Intersection(genericpoolAOwn).Equals(genericpoolAOwn),
		"overlap path keeps own history present, got %s", got.String())
	require.NoError(t, assertCoreAligned(got, p.machine.machineInfo.CPUTopology))
}

// TestDynamicPolicy_generatePoolsAndIsolation_proportionalNamedPoolOwnHistory
// runs the insufficient-capacity proportional division separately. Pools stay
// disjoint, sizes cover the available set, and each pool keeps its own CPUs where
// the scaled request still allows it.
func TestDynamicPolicy_generatePoolsAndIsolation_proportionalNamedPoolOwnHistory(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	genericpoolAOwn := machine.NewCPUSet(0, 48, 1, 49)
	poolBOwn := machine.NewCPUSet(24, 72, 25, 73)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", genericpoolAOwn)
	seedPoolEntry(entries, "pool-b", poolBOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	// Only 6 CPUs are eligible although both pools request 4; proportional
	// division must not duplicate or drop CPUs.
	available := machine.NewCPUSet(0, 48, 1, 49, 24, 72)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	a := poolsCPUSet["genericpool-a"]
	b := poolsCPUSet["pool-b"]
	require.True(t, a.Intersection(b).IsEmpty(), "proportional pools must be disjoint, a=%s b=%s", a.String(), b.String())
	require.Equal(t, available.Size(), a.Size()+b.Size(), "a=%s b=%s", a.String(), b.String())
	require.True(t, a.Intersection(genericpoolAOwn).Size() >= 2,
		"scaled genericpool-a should retain own cores where possible, a=%s", a.String())
}

// TestDynamicPolicy_generatePoolsAndIsolation_commonIsolationPoolReusesOwnHistory
// places a common isolation pool on the normal generate path (isolatedQuantityMap
// empty). Its own frozen history is reused equal-qty exactly, the same as a
// generic named share pool; own history never becomes a borrowed preference for
// the source share pool.
func TestDynamicPolicy_generatePoolsAndIsolation_commonIsolationPoolReusesOwnHistory(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	isolationOwn := machine.NewCPUSet(0, 48, 1, 49)
	entries := state.PodEntries{}
	seedPoolEntry(entries, commonstate.PoolNamePrefixIsolation+"-podx", isolationOwn)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 3, 51, 24, 72, 25, 73)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNamePrefixIsolation + "-podx": {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	require.True(t, poolsCPUSet[commonstate.PoolNamePrefixIsolation+"-podx"].Equals(isolationOwn),
		"common isolation pool must reuse own history, got %s want %s",
		poolsCPUSet[commonstate.PoolNamePrefixIsolation+"-podx"].String(), isolationOwn.String())
}

// TestDynamicPolicy_generatePoolsAndIsolation_ownHistoryUnavailableSoftMoves proves
// the own tier is soft: when the historical cpuset is no longer available the pool
// moves to eligible cpus without erroring, hard-pinning, or blocking capacity.
func TestDynamicPolicy_generatePoolsAndIsolation_ownHistoryUnavailableSoftMoves(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	own := machine.NewCPUSet(0, 48, 1, 49)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", own)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	// Own history is absent from eligible; only NUMA1 spare cores are available.
	available := machine.NewCPUSet(24, 72, 25, 73, 26, 74, 27, 75)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	got := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, got.Size())
	require.True(t, got.Intersection(own).IsEmpty(), "unavailable own history must not be forced, got %s", got.String())
	require.NoError(t, assertCoreAligned(got, p.machine.machineInfo.CPUTopology))
}

// TestDynamicPolicy_generatePoolsAndIsolation_earlyGrowthSpillDoesNotGrabLaterExact
// runs genericpool-a (own history insufficient, grows) before pool-b (legal exact
// own history). The early pool's spill must not consume pool-b's valid exact set;
// pool-b keeps its history byte-for-byte while genericpool-a fills from its own
// history plus other free cores.
func TestDynamicPolicy_generatePoolsAndIsolation_earlyGrowthSpillDoesNotGrabLaterExact(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	ownA := machine.NewCPUSet(0, 48)
	ownB := machine.NewCPUSet(24, 72, 25, 73)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", ownA)
	seedPoolEntry(entries, "pool-b", ownB)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 24, 72, 25, 73)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	b := poolsCPUSet["pool-b"]
	require.True(t, b.Equals(ownB), "later pool-b keeps legal exact own history, got %s", b.String())

	a := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, a.Size())
	require.True(t, a.Contains(0) && a.Contains(48), "early pool reuses own history, got %s", a.String())
	require.True(t, a.Intersection(b).IsEmpty(), "pools disjoint, a=%s b=%s", a.String(), b.String())
	require.NoError(t, assertCoreAligned(a, p.machine.machineInfo.CPUTopology))
}

// TestBuildRequestedPoolOwnPreferredCPUs_frozenCloneIndependent reads the committed
// faked entry through the live state and freezes a clone: local mutation/clear of
// the source entries afterwards must not change the frozen history, and a missing
// pool contributes nothing instead of silently returning an empty default.
func TestBuildRequestedPoolOwnPreferredCPUs_frozenCloneIndependent(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	history := machine.NewCPUSet(0, 48, 1, 49)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", history)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	frozen := buildPoolPlacementSnapshot(
		p.state.GetPodEntries(), nil,
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 1},
			"pool-b":        {commonstate.FakedNUMAID: 1},
		}, nil, p.machine.machineInfo.CPUDetails.CPUs(), false).ownByPool
	require.True(t, frozen["genericpool-a"].Equals(history), "frozen history read non-empty, got %s", frozen["genericpool-a"].String())
	_, hasB := frozen["pool-b"]
	require.False(t, hasB, "missing pool contributes no entry")

	// Clear the locally held source map; the frozen clone must be unaffected.
	for k := range entries {
		delete(entries, k)
	}
	require.True(t, frozen["genericpool-a"].Equals(history), "frozen clone survives source clear")
}

// TestDynamicPolicy_generatePoolsAndIsolation_sameNUMAGrowthSpillDoesNotGrabLaterExact
// keeps both pools on NUMA0: genericpool-a owns only a partial history below its
// request, while pool-b holds a legal exact own history. The global legal-exact
// preselect reserves pool-b up front, so genericpool-a's same-NUMA growth spill
// cannot take pool-b's exact set.
func TestDynamicPolicy_generatePoolsAndIsolation_sameNUMAGrowthSpillDoesNotGrabLaterExact(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	ownA := machine.NewCPUSet(0, 48)
	ownB := machine.NewCPUSet(2, 50, 3, 51)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", ownA)
	seedPoolEntry(entries, "pool-b", ownB)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 3, 51)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	b := poolsCPUSet["pool-b"]
	require.True(t, b.Equals(ownB), "pool-b keeps legal exact own history, got %s", b.String())

	a := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, a.Size())
	require.True(t, a.Contains(0) && a.Contains(48), "genericpool-a keeps own history, got %s", a.String())
	require.True(t, a.Intersection(b).IsEmpty(), "pools disjoint, a=%s b=%s", a.String(), b.String())
	require.NoError(t, assertCoreAligned(a, p.machine.machineInfo.CPUTopology))
}

// TestGeneratePreservesCommittedFakedEntriesThroughEntry proves the generate entry
// does not silently drop committed faked pool history: after a real
// generatePoolsAndIsolation call, p.state still holds both committed entries, and
// the returned placement keeps each history byte-for-byte.
func TestGeneratePreservesCommittedFakedEntriesThroughEntry(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	ownA := machine.NewCPUSet(0, 48, 1, 49)
	ownB := machine.NewCPUSet(2, 50, 3, 51)
	entries := state.PodEntries{}
	seedPoolEntry(entries, "genericpool-a", ownA)
	seedPoolEntry(entries, "pool-b", ownB)
	require.NoError(t, p.state.SetPodEntries(entries, false))

	available := machine.NewCPUSet(0, 48, 1, 49, 2, 50, 3, 51)
	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"genericpool-a": {commonstate.FakedNUMAID: 4},
			"pool-b":        {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		available,
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["genericpool-a"].Equals(ownA))
	require.True(t, poolsCPUSet["pool-b"].Equals(ownB))

	// Committed history must survive the generate call itself; the caller's
	// post-commit write happens after generate returns.
	after := p.state.GetPodEntries()
	require.NotNil(t, after["genericpool-a"][commonstate.FakedContainerName])
	require.NotNil(t, after["pool-b"][commonstate.FakedContainerName])
	require.True(t, after["genericpool-a"][commonstate.FakedContainerName].AllocationResult.Equals(ownA))
	require.True(t, after["pool-b"][commonstate.FakedContainerName].AllocationResult.Equals(ownB))
}

// TestAssemblePoolPreferredTiers_ordersOwnBeforeBorrowed keeps the frozen own
// history as tier 0 and borrowed/return preferences as later tiers; the own tier
// is never Unioned into the borrowed set.
func TestAssemblePoolPreferredTiers_ordersOwnBeforeBorrowed(t *testing.T) {
	t.Parallel()

	own := map[string]machine.CPUSet{
		"genericpool-a": machine.NewCPUSet(0, 48, 1, 49),
	}
	borrowed := map[string]machine.CPUSet{
		"genericpool-a": machine.NewCPUSet(2, 50),
		"pool-b":        machine.NewCPUSet(24, 72),
	}

	tiers := assemblePoolPreferredTiers(own, borrowed)

	require.Len(t, tiers["genericpool-a"], 2)
	require.True(t, tiers["genericpool-a"][0].Equals(machine.NewCPUSet(0, 48, 1, 49)),
		"tier 0 must be own history, got %s", tiers["genericpool-a"][0].String())
	require.True(t, tiers["genericpool-a"][1].Equals(machine.NewCPUSet(2, 50)),
		"tier 1 must be borrowed preference, got %s", tiers["genericpool-a"][1].String())
	require.Len(t, tiers["pool-b"], 1)
}

// TestDefaultShareBorrowedTierReclaim_Unchanged guards the default share pool's
// existing borrowed-isolation reclaim behavior after the tiers refactor: the
// borrowed tier still lets share maximize historical CPU reuse inside complete
// cores; own-history tiers do not replace it.
func TestDefaultShareBorrowedTierReclaim_Unchanged(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "container1",
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations:   map[string]string{},
				},
				AllocationResult: machine.NewCPUSet(8, 9, 10),
			},
		},
	}, false))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4}},
		map[string]map[string]int{},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	share := poolsCPUSet[commonstate.PoolNameShare]
	require.Equal(t, 4, share.Size())
	require.NoError(t, assertCoreAligned(share, cpuTopology))
	require.Equal(t, 2, share.Intersection(machine.NewCPUSet(8, 9, 10)).Size(),
		"borrowed tier reuse unchanged, share=%s", share.String())
}

// TestReclaimKeepsHistorical_Unchanged guards the reclaim pool's existing
// historical reuse and donor semantics: reclaim keeps its committed history
// before share consumes preferred cpus; the own-tier change must not route
// borrowed cpus away from reclaim or relax hard disjointness.
func TestReclaimKeepsHistorical_Unchanged(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3, 10, 11),
			},
		},
	}, false))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: 4},
			commonstate.PoolNameShare:   {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11, 12, 13),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	historicalReclaim := machine.NewCPUSet(2, 3, 10, 11)
	require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Intersection(historicalReclaim).Equals(historicalReclaim),
		"reclaim keeps history, got %s", poolsCPUSet[commonstate.PoolNameReclaim].String())
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Intersection(historicalReclaim).IsEmpty(),
		"share must not take reclaim history, got %s", poolsCPUSet[commonstate.PoolNameShare].String())
}

// TestReview_hardConstraintRequestCapacityInfeasibleNotShrunk is a review negative
// case only: when request + reserved hard floor exceeds eligible capacity the
// taker must report infeasibility instead of silently shrinking the request and
// succeeding. The own soft tier must neither borrow the reserved floor nor relax
// it; this test does not alter the dedicated exclusive admission flow.
//
// Mirrored from an archived near-full admission (request 124 across 4 NUMA of 32
// with a 24-CPU hard reclaim floor: 124 + 24 > 128 leaves only 104, so the
// request is truly infeasible). Here the same arithmetic is expressed generically
// on a small synthetic fixture.
func TestReview_hardConstraintRequestCapacityInfeasibleNotShrunk(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-review-hard-constraint")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()

	// Reserved hard floor CPUs are never part of the eligible set.
	hardFloor := machine.NewCPUSet(4, 12)
	eligible := machine.NewCPUSet(0, 8, 1, 9, 2, 10).Difference(hardFloor)
	own := machine.NewCPUSet(0, 8, 1, 9)

	// 12 requested > 6 eligible: infeasible, no partial-success shrink.
	taken, remaining, err := p.takeByTieredPreferredCPUs(eligible,
		[]machine.CPUSet{own}, 12)
	require.Error(t, err)
	require.True(t, taken.IsEmpty(), "infeasible request must return no taken cpus, got %s", taken.String())
	require.True(t, remaining.Equals(eligible), "eligible set must be preserved on failure, got %s", remaining.String())
	require.True(t, hardFloor.Intersection(taken).IsEmpty(), "own soft tier must never borrow the hard floor")

	// A feasible request still honors the own tier and leaves the floor untouched.
	taken, remaining, err = p.takeByTieredPreferredCPUs(eligible,
		[]machine.CPUSet{own}, 2)
	require.NoError(t, err)
	require.True(t, taken.Equals(machine.NewCPUSet(0, 8)), "feasible own-tier reuse, got %s", taken.String())
	require.True(t, hardFloor.Intersection(taken).IsEmpty())
	require.True(t, remaining.Union(taken).Equals(eligible))
}

// TestCore_takeCPUsForPoolsInPlaceWithTiers_borrowedExactDoesNotReserveOrSteal (A):
// a borrowed/return tier that coincides with another pool's reserved own exact
// must neither be reserved nor steal that exact. genericpool-a carries only a
// borrowed tier (no own history); pool-b owns X. X is reserved for pool-b only.
func TestCore_takeCPUsForPoolsInPlaceWithTiers_borrowedExactDoesNotReserveOrSteal(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	x := machine.NewCPUSet(0, 48, 1, 49)
	spill := machine.NewCPUSet(24, 72, 25, 73)
	available := x.Union(spill)

	poolsQuantityMap := map[string]int{"genericpool-a": 4, "pool-b": 4}
	preferredTiersByPool := map[string][]machine.CPUSet{
		"genericpool-a": {x}, // borrowed/return tier only; no own history
		"pool-b":        {x}, // own history
	}
	ownHistoryByPool := map[string]machine.CPUSet{"pool-b": x}

	poolsCPUSet := map[string]machine.CPUSet{}
	_, err := p.takeCPUsForPoolsInPlaceWithTiers(
		poolsQuantityMap, poolsCPUSet, available, preferredTiersByPool, ownHistoryByPool)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["pool-b"].Equals(x),
		"pool-b keeps reserved own exact, got %s", poolsCPUSet["pool-b"].String())
	a := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, a.Size())
	require.True(t, a.Intersection(x).IsEmpty(),
		"borrowed tier must not steal pool-b own exact, a=%s x=%s", a.String(), x.String())
	require.True(t, a.Intersection(poolsCPUSet["pool-b"]).IsEmpty())
}

// TestCore_takeCPUsForPoolsInPlaceWithTiers_invalidOwnNotReserved (D): an own
// history that fails atomic-donor / whole-core validity must not be reserved; it
// falls back to soft spill instead of forcing a partial or failing capacity.
func TestCore_takeCPUsForPoolsInPlaceWithTiers_invalidOwnNotReserved(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	// Donor half-core {2,50}; own exact touches only part of the donor closure, so
	// it is invalid and must not be reserved.
	donor := machine.NewCPUSet(2, 50)
	invalidOwn := machine.NewCPUSet(0, 48, 2)
	spill := machine.NewCPUSet(24, 72, 25, 73, 1, 49)
	available := invalidOwn.Union(spill)

	poolsQuantityMap := map[string]int{"pool-b": 4}
	preferredTiersByPool := map[string][]machine.CPUSet{"pool-b": {invalidOwn}}
	ownHistoryByPool := map[string]machine.CPUSet{"pool-b": invalidOwn}

	poolsCPUSet := map[string]machine.CPUSet{}
	_, err := p.takeCPUsForPoolsInPlaceWithTiers(
		poolsQuantityMap, poolsCPUSet, available, preferredTiersByPool, ownHistoryByPool, donor)
	require.NoError(t, err)

	b := poolsCPUSet["pool-b"]
	require.Equal(t, 4, b.Size())
	require.True(t, b.Intersection(donor).IsEmpty(),
		"invalid partial own must not force donor touch, b=%s donor=%s", b.String(), donor.String())
	require.NoError(t, assertCoreAligned(b, p.machine.machineInfo.CPUTopology))
}

// TestCore_takeCPUsForPoolsInPlaceWithTiers_partiallyProtectedPreferredDomainSpillsToCapacity
// guards the capacity bug: when the preferred domain shrinks below the request after
// subtracting another pool's reserved own history, the taker must spill from the
// remaining available set instead of failing even though capacity exists. A owns a
// partial history (2 cpus) and B reserves 4; A requests 4.
func TestCore_takeCPUsForPoolsInPlaceWithTiers_partiallyProtectedPreferredDomainSpillsToCapacity(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	ownA := machine.NewCPUSet(0, 48)
	ownB := machine.NewCPUSet(2, 50, 3, 51)
	spill := machine.NewCPUSet(24, 72, 25, 73)
	available := ownA.Union(ownB).Union(spill)

	poolsQuantityMap := map[string]int{"genericpool-a": 4, "pool-b": 4}
	preferredTiersByPool := map[string][]machine.CPUSet{
		"genericpool-a": {ownA},
		"pool-b":        {ownB},
	}
	ownHistoryByPool := map[string]machine.CPUSet{"pool-b": ownB}

	poolsCPUSet := map[string]machine.CPUSet{}
	_, err := p.takeCPUsForPoolsInPlaceWithTiers(
		poolsQuantityMap, poolsCPUSet, available, preferredTiersByPool, ownHistoryByPool)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["pool-b"].Equals(ownB),
		"pool-b keeps reserved own exact, got %s", poolsCPUSet["pool-b"].String())
	a := poolsCPUSet["genericpool-a"]
	require.Equal(t, 4, a.Size())
	require.True(t, a.Intersection(ownB).IsEmpty(), "a must not take reserved B, a=%s b=%s",
		a.String(), poolsCPUSet["pool-b"].String())
	require.True(t, a.Intersection(poolsCPUSet["pool-b"]).IsEmpty())
}

// TestCore_takeCPUsForPoolsInPlaceWithTiers_overlappingOwnWinnerReservesLoserSpills (E):
// two own histories overlap; the sorted-first winner reserves its full exact, the
// overlapping loser does not double-reserve and soft spills to the remainder.
func TestCore_takeCPUsForPoolsInPlaceWithTiers_overlappingOwnWinnerReservesLoserSpills(t *testing.T) {
	t.Parallel()

	p := newPoolPlacementPolicy(t)

	ownA := machine.NewCPUSet(0, 48, 1, 49)
	ownB := machine.NewCPUSet(1, 49, 24, 72) // overlaps ownA on {1,49}
	extra := machine.NewCPUSet(25, 73)
	available := ownA.Union(ownB).Union(extra)

	poolsQuantityMap := map[string]int{"genericpool-a": 4, "pool-b": 4}
	preferredTiersByPool := map[string][]machine.CPUSet{
		"genericpool-a": {ownA},
		"pool-b":        {ownB},
	}
	ownHistoryByPool := map[string]machine.CPUSet{
		"genericpool-a": ownA,
		"pool-b":        ownB,
	}

	poolsCPUSet := map[string]machine.CPUSet{}
	_, err := p.takeCPUsForPoolsInPlaceWithTiers(
		poolsQuantityMap, poolsCPUSet, available, preferredTiersByPool, ownHistoryByPool)
	require.NoError(t, err)

	require.True(t, poolsCPUSet["genericpool-a"].Equals(ownA),
		"winner genericpool-a reserves own exact, got %s", poolsCPUSet["genericpool-a"].String())
	b := poolsCPUSet["pool-b"]
	require.Equal(t, 4, b.Size())
	require.True(t, b.Intersection(poolsCPUSet["genericpool-a"]).IsEmpty(),
		"loser spills disjoint, a=%s b=%s",
		poolsCPUSet["genericpool-a"].String(), b.String())
	require.True(t, b.Intersection(ownA).IsEmpty(), "loser must not take winner's reserved cores")
}
