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

package utils

import (
	"testing"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildCPUSetPartitionViewAndDeepCopy(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(true)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	state.SetAllocationInfo("share-NUMA0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("share-NUMA0"),
		AllocationResult: machine.NewCPUSet(6),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3),
	})
	state.SetAllocationInfo("isolation-0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("isolation-0"),
		AllocationResult: machine.NewCPUSet(4),
	})
	state.SetAllocationInfo("pod-1", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "pod-1",
			ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		AllocationResult: machine.NewCPUSet(5),
	})

	view := BuildCPUSetPartitionView(state, &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}, 2: {NUMANodeID: 1}, 3: {NUMANodeID: 1}, 4: {NUMANodeID: 1}, 5: {NUMANodeID: 1},
	}}, CPUSetPartitionViewOptions{})

	assertCPUSet(t, "reserve", view.Reserve, "0")
	assertCPUSet(t, "share", view.SharePool, "1,6")
	assertCPUSet(t, "share map default", view.SharePoolMap[commonstate.PoolNameShare], "1")
	assertCPUSet(t, "share map numa", view.SharePoolMap["share-NUMA0"], "6")
	assertCPUSet(t, "reclaim raw", view.ReclaimRaw, "2-3")
	assertCPUSet(t, "dedicated", view.Dedicated, "5")
	assertCPUSet(t, "non reclaim", view.NonReclaimPool, "1,4-6")
	assertCPUSet(t, "reclaim effective", view.ReclaimEffective, "2-3")
	assertCPUSet(t, "reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "2-3")
	assertCPUSet(t, "container", view.ContainerCPUSetByPod["pod-1"]["main"], "5")

	copied := view.DeepCopy()
	assertCPUSet(t, "copied reserve", copied.Reserve, "0")
	assertCPUSet(t, "copied share", copied.SharePool, "1,6")
	assertCPUSet(t, "copied share map default", copied.SharePoolMap[commonstate.PoolNameShare], "1")
	assertCPUSet(t, "copied share map numa", copied.SharePoolMap["share-NUMA0"], "6")
	assertCPUSet(t, "copied reclaim raw", copied.ReclaimRaw, "2-3")
	assertCPUSet(t, "copied dedicated", copied.Dedicated, "5")
	assertCPUSet(t, "copied non reclaim", copied.NonReclaimPool, "1,4-6")
	assertCPUSet(t, "copied reclaim effective", copied.ReclaimEffective, "2-3")
	assertCPUSet(t, "copied reclaim numa 1", copied.ReclaimEffectivePerNUMA[1], "2-3")
	assertCPUSet(t, "copied container", copied.ContainerCPUSetByPod["pod-1"]["main"], "5")
	copied.ContainerCPUSetByPod["pod-1"]["main"] = machine.NewCPUSet(6)
	assertCPUSet(t, "original container unchanged", view.ContainerCPUSetByPod["pod-1"]["main"], "5")
	copied.SharePoolMap["share-NUMA0"] = machine.NewCPUSet(7)
	assertCPUSet(t, "original share pool map unchanged", view.SharePoolMap["share-NUMA0"], "6")
}

func TestBuildCPUSetPartitionViewNilInputs(t *testing.T) {
	t.Parallel()

	view := BuildCPUSetPartitionView(nil, nil, CPUSetPartitionViewOptions{})
	if view == nil || !view.ReclaimEffective.IsEmpty() || len(view.ContainerCPUSetByPod) != 0 {
		t.Fatalf("unexpected nil input view: %#v", view)
	}
	if view.DeepCopy() == nil {
		t.Fatalf("DeepCopy of non-nil empty view returned nil")
	}
	if (*CPUSetPartitionView)(nil).DeepCopy() != nil {
		t.Fatalf("DeepCopy of nil view should be nil")
	}
}

func TestBuildCPUSetPartitionViewPadsNonReclaimPoolToMinSize(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(2),
	})

	view := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "reserve", view.Reserve, "0")
	assertCPUSet(t, "original share preserved and padded", view.NonReclaimPool, "1-2,4-5")
	assertCPUSet(t, "reclaim effective after padding", view.ReclaimEffective, "3,6-7")
	assertCPUSet(t, "reclaim numa 0 after padding", view.ReclaimEffectivePerNUMA[0], "3")
	assertCPUSet(t, "reclaim numa 1 after padding", view.ReclaimEffectivePerNUMA[1], "6-7")
}

func TestBuildCPUSetPartitionViewAppliesTransientProtectedNonReclaim(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 2),
	})

	view := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		TransientProtectedNonReclaim: machine.NewCPUSet(3, 4),
	})

	assertCPUSet(t, "desired non reclaim", view.DesiredNonReclaimPool, "1-2")
	assertCPUSet(t, "desired reclaim", view.DesiredReclaimEffective, "3-7")
	assertCPUSet(t, "protected non reclaim", view.TransientProtectedNonReclaim, "3-4")
	assertCPUSet(t, "applied non reclaim", view.NonReclaimPool, "1-4")
	assertCPUSet(t, "applied reclaim", view.ReclaimEffective, "5-7")
	if !view.NonReclaimPool.Intersection(view.ReclaimEffective).IsEmpty() {
		t.Fatalf("applied non reclaim and reclaim overlap: non=%s reclaim=%s", view.NonReclaimPool.String(), view.ReclaimEffective.String())
	}
	assertCPUSet(t, "applied reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "5-7")
}

func TestApplyTransientProtectedNonReclaimRebuildsReclaimPerNUMA(t *testing.T) {
	t.Parallel()

	view := &CPUSetPartitionView{
		Reserve:                        machine.NewCPUSet(),
		DesiredNonReclaimPool:          machine.NewCPUSet(1, 2),
		DesiredReclaimEffective:        machine.NewCPUSet(3, 4, 5, 6, 7),
		NonReclaimPool:                 machine.NewCPUSet(1, 2),
		ReclaimEffective:               machine.NewCPUSet(3, 4, 5, 6, 7),
		ReclaimEffectivePerNUMA:        map[int]machine.CPUSet{0: machine.NewCPUSet(3), 1: machine.NewCPUSet(4, 5, 6, 7)},
		DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(3), 1: machine.NewCPUSet(4, 5, 6, 7)},
	}

	ApplyTransientProtectedNonReclaim(view, testTwoNUMATopology(), machine.NewCPUSet(3, 4))

	assertCPUSet(t, "applied reclaim", view.ReclaimEffective, "5-7")
	if _, ok := view.ReclaimEffectivePerNUMA[0]; ok {
		t.Fatalf("reclaim numa 0 should be removed after protected CPUs are deducted: %s", view.ReclaimEffectivePerNUMA[0].String())
	}
	assertCPUSet(t, "applied reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "5-7")
}

func TestBuildCPUSetPartitionViewPadsNonReclaimPoolReversely(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})

	view := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
		ReserveCPUReversely:   true,
	})

	assertCPUSet(t, "reverse padded non reclaim", view.NonReclaimPool, "2-3,6-7")
	assertCPUSet(t, "reverse reclaim effective", view.ReclaimEffective, "1,4-5")
	assertCPUSet(t, "reverse reclaim numa 0", view.ReclaimEffectivePerNUMA[0], "1")
	assertCPUSet(t, "reverse reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "4-5")
}

func TestBuildCPUSetPartitionViewDoesNotPadWhenOverlapAllowed(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(true)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})

	view := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "non reclaim remains empty", view.NonReclaimPool, "")
	assertCPUSet(t, "reclaim effective remains raw", view.ReclaimEffective, "1-3")
}

func TestBuildCPUSetPartitionViewCapsPaddingToCandidates(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5),
	})

	view := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "non reclaim capped to candidates", view.NonReclaimPool, "6-7")
	assertCPUSet(t, "reclaim effective exhausted", view.ReclaimEffective, "")
}

func testTwoNUMATopology() *machine.CPUTopology {
	return &machine.CPUTopology{
		NumCPUs:      8,
		NumCores:     8,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
			4: {NUMANodeID: 1, SocketID: 1, CoreID: 4},
			5: {NUMANodeID: 1, SocketID: 1, CoreID: 5},
			6: {NUMANodeID: 1, SocketID: 1, CoreID: 6},
			7: {NUMANodeID: 1, SocketID: 1, CoreID: 7},
		},
	}
}

func assertCPUSet(t *testing.T, name string, got machine.CPUSet, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s cpuset = %s, want %s", name, got.String(), want)
	}
}
