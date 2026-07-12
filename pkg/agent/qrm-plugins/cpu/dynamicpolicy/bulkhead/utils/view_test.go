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
	}})

	assertCPUSet(t, "reserve", view.Reserve, "0")
	assertCPUSet(t, "share", view.SharePool, "1")
	assertCPUSet(t, "reclaim raw", view.ReclaimRaw, "2-3")
	assertCPUSet(t, "dedicated", view.Dedicated, "5")
	assertCPUSet(t, "non reclaim", view.NonReclaimPool, "1,4-5")
	assertCPUSet(t, "reclaim effective", view.ReclaimEffective, "2-3")
	assertCPUSet(t, "reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "2-3")
	assertCPUSet(t, "container", view.ContainerCPUSetByPod["pod-1"]["main"], "5")

	copied := view.DeepCopy()
	if !EqualCPUSetPartitionView(view, copied) {
		t.Fatalf("deep copy should equal original")
	}
	copied.ContainerCPUSetByPod["pod-1"]["main"] = machine.NewCPUSet(6)
	if EqualCPUSetPartitionView(view, copied) {
		t.Fatalf("mutated copy should not equal original")
	}
	assertCPUSet(t, "original container unchanged", view.ContainerCPUSetByPod["pod-1"]["main"], "5")
	if EqualCPUSetPartitionView(nil, view) {
		t.Fatalf("nil and non-nil views must not be equal")
	}
}

func TestBuildCPUSetPartitionViewNilInputs(t *testing.T) {
	t.Parallel()

	view := BuildCPUSetPartitionView(nil, nil)
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

func assertCPUSet(t *testing.T, name string, got machine.CPUSet, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s cpuset = %s, want %s", name, got.String(), want)
	}
}
