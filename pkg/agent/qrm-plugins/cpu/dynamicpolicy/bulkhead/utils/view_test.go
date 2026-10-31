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
	"errors"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func TestLeafPoolIdentityForAllocationNormalizesPoolWrappers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		allocation *cpustate.AllocationInfo
		want       model.CPUSetPoolIdentity
		wantOK     bool
	}{
		{
			name: "share resource package and numa suffix",
			allocation: &cpustate.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
				OwnerPoolName: resourcepackage.WrapOwnerPoolName("analytics-NUMA3", "package-a"),
				QoSLevel:      commonstate.PoolNameShare,
			}},
			want: model.CPUSetPoolIdentity{
				Kind: model.CPUSetPoolKindShare,
				Name: resourcepackage.WrapOwnerPoolName("analytics-NUMA3", "package-a"),
			},
			wantOK: true,
		},
		{
			name: "default share",
			allocation: &cpustate.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
				OwnerPoolName: commonstate.PoolNameShare,
				QoSLevel:      commonstate.PoolNameShare,
			}},
			want:   model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: commonstate.PoolNameShare},
			wantOK: true,
		},
		{
			name: "dedicated keeps pod identity",
			allocation: &cpustate.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
				OwnerPoolName: commonstate.PoolNameDedicated,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				PodNamespace:  "ns",
				PodName:       "pod",
			}},
			want:   model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "ns", PodName: "pod"},
			wantOK: true,
		},
		{
			name: "reclaim has no protected leaf owner",
			allocation: &cpustate.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
				OwnerPoolName: commonstate.PoolNameReclaim,
				QoSLevel:      commonstate.PoolNameReclaim,
			}},
			wantOK: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := LeafPoolIdentityForAllocation(tc.allocation)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("identity=(%+v,%v), want (%+v,%v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestNewCPUSetPartitionViewOptionsUsesProductionConfigurationConsistently(t *testing.T) {
	t.Parallel()

	defaultDynamic := dynamicconfig.NewDynamicAgentConfiguration()
	defaultDynamic.GetDynamicConfiguration().AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = 7
	coreConf := config.NewConfiguration()
	coreConf.EnableReserveCPUReversely = true
	coreConf.DynamicAgentConfiguration = defaultDynamic
	currentDynamic := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	currentDynamic.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = 5
	currentDynamic.EnableReclaim = true
	currentDynamic.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	currentDynamic.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.2
	topology := testTwoNUMATopologyN(32)

	inactiveOpts := NewCPUSetPartitionViewOptionsWithState(
		coreConf, currentDynamic, topology, CPUSetPartitionViewState{}, sets.NewInt())
	if inactiveOpts.HardPartitionEnabled || len(inactiveOpts.HardPartitionReclaimTargetPerNUMA) != 0 {
		t.Fatalf("configured inactive hard partition must not resolve targets: %+v", inactiveOpts)
	}

	state := cpustate.NewCPUPluginState(topology)
	opts := NewCPUSetPartitionViewOptionsWithState(
		coreConf, currentDynamic, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
	if opts.NonReclaimPoolMinSize != 5 || !opts.ReserveCPUReversely || !opts.HardPartitionEnabled {
		t.Fatalf("unexpected current options: %+v", opts)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 6 {
		t.Fatalf("NUMA 0 immutable reclaim target = %d, want 6 for 32 CPUs at ratio 0.2", got)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 6 {
		t.Fatalf("NUMA 1 immutable reclaim target = %d, want 6 for 32 CPUs at ratio 0.2", got)
	}

	currentDynamic.MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("16"),
	}
	opts = NewCPUSetPartitionViewOptionsWithState(
		coreConf, currentDynamic, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
	if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 8 {
		t.Fatalf("NUMA 0 configured reclaim target = %d, want 8", got)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 8 {
		t.Fatalf("NUMA 1 configured reclaim target = %d, want 8", got)
	}

	currentDynamic.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = 0
	opts = NewCPUSetPartitionViewOptionsWithState(
		coreConf, currentDynamic, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
	if opts.NonReclaimPoolMinSize != 7 {
		t.Fatalf("fallback NonReclaimPoolMinSize = %d, want 7", opts.NonReclaimPoolMinSize)
	}

	currentDynamic.EnableReclaim = false
	opts = NewCPUSetPartitionViewOptionsWithState(
		coreConf, currentDynamic, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
	if opts.HardPartitionEnabled {
		t.Fatal("hard partition must be ineffective when reclaim is disabled")
	}
}

func TestNewCPUSetPartitionViewOptionsUsesEligibleCapacityFromState(t *testing.T) {
	t.Parallel()

	topology := testTwoNUMATopologyN(32)
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.2

	state := cpustate.NewCPUPluginState(topology)
	machineState := state.GetMachineState()
	machineState[0].DefaultCPUSet = machine.NewCPUSet()
	for cpu := 0; cpu < 21; cpu++ {
		machineState[0].DefaultCPUSet.Add(cpu)
	}
	machineState[0].AllocatedCPUSet = machine.NewCPUSet()
	for cpu := 21; cpu < 32; cpu++ {
		machineState[0].AllocatedCPUSet.Add(cpu)
	}
	state.SetMachineState(machineState)

	opts := NewCPUSetPartitionViewOptionsWithState(
		nil, dynamicConf, topology, CPUSetPartitionViewState{
			State:        state,
			ReservedCPUs: machine.NewCPUSet(0),
		}, sets.NewInt(commonstate.FakedNUMAID))
	if opts.HardPartitionTargetError != nil {
		t.Fatalf("NewCPUSetPartitionViewOptions() error = %v", opts.HardPartitionTargetError)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 4 {
		t.Fatalf("NUMA 0 target = %d, want 4 from 20 eligible CPUs", got)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 6 {
		t.Fatalf("NUMA 1 target = %d, want 6 from 32 eligible CPUs", got)
	}
}

func TestNewCPUSetPartitionViewOptionsWithStateUsesReservedReclaimFloors(t *testing.T) {
	t.Parallel()

	topology := testTwoNUMASMTTopology()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true

	state := cpustate.NewCPUPluginState(topology)
	opts := NewCPUSetPartitionViewOptionsWithState(
		nil,
		dynamicConf,
		topology,
		CPUSetPartitionViewState{
			State:                         state,
			ReservedCPUs:                  machine.NewCPUSet(),
			ReservedReclaimedCPUs:         machine.NewCPUSet(0, 1, 2, 3),
			ReservedReclaimedCPUsFallback: 4,
		},
		sets.NewInt(commonstate.FakedNUMAID),
	)
	if opts.HardPartitionTargetError != nil {
		t.Fatalf("NewCPUSetPartitionViewOptionsWithState() error = %v", opts.HardPartitionTargetError)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 4 {
		t.Fatalf("NUMA 0 target = %d, want reserved reclaim floor 4", got)
	}
	if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 2 {
		t.Fatalf("NUMA 1 target = %d, want one-core baseline 2", got)
	}
}

func TestNewCPUSetPartitionViewOptionsRejectsIncompleteEligibleCapacityState(t *testing.T) {
	t.Parallel()

	topology := testTwoNUMATopologyN(32)
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true

	tests := []struct {
		name  string
		state cpustate.ReadonlyState
		want  string
	}{
		{
			name: "nil state",
			want: "missing state",
		},
		{
			name: "missing NUMA state",
			state: func() cpustate.ReadonlyState {
				state := cpustate.NewCPUPluginState(topology)
				machineState := state.GetMachineState()
				delete(machineState, 1)
				state.SetMachineState(machineState)
				return state
			}(),
			want: "missing machine state for NUMA 1",
		},
		{
			name: "incomplete NUMA state",
			state: func() cpustate.ReadonlyState {
				state := cpustate.NewCPUPluginState(topology)
				machineState := state.GetMachineState()
				machineState[0].DefaultCPUSet = machineState[0].DefaultCPUSet.Difference(machine.NewCPUSet(0))
				state.SetMachineState(machineState)
				return state
			}(),
			want: "does not cover NUMA topology",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := NewCPUSetPartitionViewOptionsWithState(
				nil, dynamicConf, topology, CPUSetPartitionViewState{State: tt.state}, sets.NewInt(commonstate.FakedNUMAID))
			if opts.HardPartitionTargetError == nil ||
				!strings.Contains(opts.HardPartitionTargetError.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", opts.HardPartitionTargetError, tt.want)
			}
		})
	}
}

func TestNewCPUSetPartitionViewOptionsHandlesEligibleCapacityCornerCases(t *testing.T) {
	t.Parallel()

	topology := testTwoNUMASMTTopology()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.5

	t.Run("less than one complete core resolves to zero", func(t *testing.T) {
		t.Parallel()
		state := cpustate.NewCPUPluginState(topology)
		machineState := state.GetMachineState()
		machineState[0].DefaultCPUSet = machine.NewCPUSet(0)
		machineState[0].AllocatedCPUSet = machine.NewCPUSet(1, 2, 3)
		state.SetMachineState(machineState)

		opts := NewCPUSetPartitionViewOptionsWithState(
			nil, dynamicConf, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
		if opts.HardPartitionTargetError != nil {
			t.Fatalf("NewCPUSetPartitionViewOptions() error = %v", opts.HardPartitionTargetError)
		}
		if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 0 {
			t.Fatalf("NUMA 0 target = %d, want 0 when only one SMT sibling is eligible", got)
		}
		if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 2 {
			t.Fatalf("NUMA 1 target = %d, want one complete physical core", got)
		}
	})

	t.Run("reserved CPUs are deducted only when eligible", func(t *testing.T) {
		t.Parallel()
		state := cpustate.NewCPUPluginState(topology)
		opts := NewCPUSetPartitionViewOptionsWithState(
			nil, dynamicConf, topology, CPUSetPartitionViewState{
				State:        state,
				ReservedCPUs: machine.NewCPUSet(0, 4, 99),
			}, sets.NewInt(commonstate.FakedNUMAID))
		if opts.HardPartitionTargetError != nil {
			t.Fatalf("NewCPUSetPartitionViewOptions() error = %v", opts.HardPartitionTargetError)
		}
		if got := opts.HardPartitionReclaimTargetPerNUMA[0]; got != 2 {
			t.Fatalf("NUMA 0 target = %d, want 2 after deducting one eligible sibling", got)
		}
		if got := opts.HardPartitionReclaimTargetPerNUMA[1]; got != 2 {
			t.Fatalf("NUMA 1 target = %d, want 2 after deducting one eligible sibling", got)
		}
	})

	t.Run("cross NUMA machine state is rejected", func(t *testing.T) {
		t.Parallel()
		state := cpustate.NewCPUPluginState(topology)
		machineState := state.GetMachineState()
		machineState[0].DefaultCPUSet.Add(4)
		state.SetMachineState(machineState)

		opts := NewCPUSetPartitionViewOptionsWithState(
			nil, dynamicConf, topology, CPUSetPartitionViewState{State: state}, sets.NewInt(commonstate.FakedNUMAID))
		if opts.HardPartitionTargetError == nil ||
			!strings.Contains(opts.HardPartitionTargetError.Error(), "outside NUMA topology: 4") {
			t.Fatalf("error = %v, want cross-NUMA machine-state error", opts.HardPartitionTargetError)
		}
	})
}

func TestNewCPUSetPartitionViewOptionsDoesNotRequireStateWhenHardPartitionInactive(t *testing.T) {
	t.Parallel()

	topology := testTwoNUMATopology()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true

	opts := NewCPUSetPartitionViewOptionsWithState(
		nil, dynamicConf, topology, CPUSetPartitionViewState{}, sets.NewInt())
	if opts.HardPartitionTargetError != nil {
		t.Fatalf("inactive hard partition unexpectedly resolved state: %v", opts.HardPartitionTargetError)
	}
	if opts.HardPartitionEnabled {
		t.Fatal("hard partition must stay inactive")
	}
}

func TestNewCPUSetPartitionViewOptionsRequiresTopologyWhenHardPartitionActive(t *testing.T) {
	t.Parallel()

	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration().GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true

	opts := NewCPUSetPartitionViewOptionsWithState(
		nil, dynamicConf, nil, CPUSetPartitionViewState{
			State: cpustate.NewCPUPluginState(nil),
		}, sets.NewInt(commonstate.FakedNUMAID))
	if opts.HardPartitionTargetError == nil ||
		!strings.Contains(opts.HardPartitionTargetError.Error(), "missing topology") {
		t.Fatalf("error = %v, want missing-topology error", opts.HardPartitionTargetError)
	}
}

func TestBuildValidatedCPUSetPartitionViewRejectsNilStateForHardPartition(t *testing.T) {
	t.Parallel()

	_, _, err := BuildValidatedCPUSetPartitionView(nil, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		HardPartitionEnabled:              true,
		HardPartitionReclaimTargetPerNUMA: map[int]int{0: 2, 1: 2},
	})
	if err == nil || !strings.Contains(err.Error(), "missing state") {
		t.Fatalf("error = %v, want missing-state error", err)
	}
}

func TestValidateCPUSetPartitionViewRequiresExactNUMABucketProjection(t *testing.T) {
	t.Parallel()

	valid := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(0, 1, 2, 3, 4),
		ReclaimEffective:        machine.NewCPUSet(5, 6, 7),
		DesiredReclaimEffective: machine.NewCPUSet(5, 6, 7),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(),
			1: machine.NewCPUSet(5, 6, 7),
		},
		DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(),
			1: machine.NewCPUSet(5, 6, 7),
		},
	}}
	if err := ValidateCPUSetPartitionView(valid, testTwoNUMATopology()); err != nil {
		t.Fatalf("exact projection with empty NUMA bucket rejected: %v", err)
	}

	invalid := valid.DeepCopy()
	invalid.ReclaimEffectivePerNUMA[0] = machine.NewCPUSet(0)
	invalid.ReclaimEffectivePerNUMA[1] = machine.NewCPUSet(6, 7)
	if err := ValidateCPUSetPartitionView(invalid, testTwoNUMATopology()); err == nil ||
		!strings.Contains(err.Error(), "reclaim effective NUMA bucket 0") {
		t.Fatalf("ValidateCPUSetPartitionView() error = %v, want exact NUMA projection error", err)
	}
}

func TestValidateCPUSetPartitionViewRejectsCPUsOutsideTopology(t *testing.T) {
	t.Parallel()

	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(0, 1, 8),
		ReclaimEffective:        machine.NewCPUSet(2, 3),
		DesiredReclaimEffective: machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
			1: machine.NewCPUSet(),
		},
		DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
			1: machine.NewCPUSet(),
		},
	}}

	err := ValidateCPUSetPartitionView(view, testTwoNUMATopology())
	if err == nil || !strings.Contains(err.Error(), "outside machine topology") {
		t.Fatalf("ValidateCPUSetPartitionView() error = %v, want outside-topology error", err)
	}
}

func TestBuildCPUSetPartitionViewFromTargetOwnsVerifiedReclaim(t *testing.T) {
	t.Parallel()

	desired := model.NewDesiredView()
	desired.Reserve = machine.NewCPUSet(0)
	desired.Dedicated = machine.NewCPUSet(1)
	desired.SharePool = machine.NewCPUSet(2)
	desired.ReclaimRaw = machine.NewCPUSet(3, 4, 5)
	desired.ContainerCPUSetByPod["pod"] = map[string]machine.CPUSet{
		"container": machine.NewCPUSet(1),
	}
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 0},
		3: {NUMANodeID: 0},
		4: {NUMANodeID: 1},
		5: {NUMANodeID: 1},
	}}
	target := machine.NewCPUSet(3, 5)

	got := BuildCPUSetPartitionViewFromTarget(desired, target, topology)

	if got == nil {
		t.Fatal("BuildCPUSetPartitionViewFromTarget() returned nil")
	}
	if !got.ReclaimEffective.Equals(target) {
		t.Fatalf("reclaim effective = %s, want %s", got.ReclaimEffective.String(), target.String())
	}
	if !got.NonReclaimPool.Equals(machine.NewCPUSet(1, 2, 4)) {
		t.Fatalf("non-reclaim pool = %s, want 1-2,4", got.NonReclaimPool.String())
	}
	if !got.ReclaimEffectivePerNUMA[0].Equals(machine.NewCPUSet(3)) ||
		!got.ReclaimEffectivePerNUMA[1].Equals(machine.NewCPUSet(5)) {
		t.Fatalf("reclaim per NUMA = %#v, want numa0=3 numa1=5", got.ReclaimEffectivePerNUMA)
	}

	target.Add(99)
	desired.ContainerCPUSetByPod["pod"]["container"].Add(98)
	if got.ReclaimEffective.Contains(99) || got.ContainerCPUSetByPod["pod"]["container"].Contains(98) {
		t.Fatal("returned view aliases caller-owned target or desired view")
	}
}

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

	view, _ := BuildCPUSetPartitionView(state, &machine.CPUTopology{CPUDetails: machine.CPUDetails{
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

func sharedRampUpAllocation(podUID string, cpus machine.CPUSet, snb bool) *cpustate.AllocationInfo {
	meta := commonstate.AllocationMeta{
		PodUid:        podUID,
		ContainerName: "main",
		OwnerPoolName: commonstate.PoolNameShare,
		QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
	}
	if snb {
		meta.Annotations = map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		}
	}
	return &cpustate.AllocationInfo{AllocationMeta: meta, RampUp: true, AllocationResult: cpus}
}

// TestPendingSharedProjectionReturned proves the prepare snapshot returns exactly
// the eligible non-SNB shared ramp-up allocations as a transient side-channel set,
// gates it behind HardPartition=false && overlap=false, excludes steady/SNB/dedicated
// cases, and — critically — keeps the hint OFF the business DesiredView: it must
// not survive DeepCopy and must not affect DesiredView equality/revision/checkpoint.
func TestPendingSharedProjectionReturned(t *testing.T) {
	t.Parallel()
	topo := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}, 2: {NUMANodeID: 0},
		3: {NUMANodeID: 0}, 4: {NUMANodeID: 0}, 5: {NUMANodeID: 0},
	}}
	build := func(overlap, hardPartition bool) (*model.DesiredView, map[string]map[string]struct{}) {
		state := cpustate.NewCPUPluginState(nil)
		state.SetAllowSharedCoresOverlapReclaimedCores(overlap)
		// Eligible: shared ramp-up non-SNB.
		state.SetAllocationInfo("eligible", "main", sharedRampUpAllocation("eligible", machine.NewCPUSet(0), false))
		// Not eligible: shared ramp-up but SNB.
		state.SetAllocationInfo("snb", "main", sharedRampUpAllocation("snb", machine.NewCPUSet(1), true))
		// Not eligible: steady shared (RampUp off).
		steady := sharedRampUpAllocation("steady", machine.NewCPUSet(2), false)
		steady.RampUp = false
		state.SetAllocationInfo("steady", "main", steady)
		// Not eligible: dedicated.
		state.SetAllocationInfo("ded", "main", &cpustate.AllocationInfo{
			AllocationMeta:   commonstate.AllocationMeta{PodUid: "ded", ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores},
			AllocationResult: machine.NewCPUSet(3),
		})
		opts := CPUSetPartitionViewOptions{HardPartitionEnabled: hardPartition}
		return BuildCPUSetPartitionView(state, topo, opts)
	}

	// Default: HardPartition=false, overlap=false => only the eligible shared
	// ramp-up non-SNB container is returned, classified per (podUID, container).
	view, projection := build(false, false)
	if _, ok := projection["eligible"]["main"]; !ok {
		t.Fatalf("eligible shared ramp-up non-SNB must be returned: %v", projection)
	}
	for _, pod := range []string{"snb", "steady", "ded"} {
		if _, ok := projection[pod]; ok {
			t.Fatalf("%s must not be returned as eligible: %v", pod, projection)
		}
	}

	// overlap=true gate disables the hint entirely (unchanged).
	if _, p := build(true, false); len(p) != 0 {
		t.Fatalf("overlap=true must disable projection hint: %v", p)
	}
	// HardPartition=true must NOT disable the hint: the eligible non-SNB shared
	// ramp-up container is still classified for projection. The active ramp-up
	// floor itself must not be the thing that turns pending projection off (sa8).
	if _, hp := build(false, true); len(hp) == 0 {
		t.Fatalf("hard partition must still surface the eligible shared ramp-up container: %v", hp)
	} else if _, ok := hp["eligible"]["main"]; !ok {
		t.Fatalf("hard partition must return the eligible non-SNB shared ramp-up container: %v", hp)
	}

	// The hint is a side channel: a DeepCopy of the business view must NOT carry
	// it, and two equal business views must compare equal regardless of the hint.
	cp := view.DeepCopy()
	if !model.EqualDesiredView(view, cp) {
		t.Fatalf("business DesiredView must be self-equal under DeepCopy")
	}
}

func TestBuildCPUSetPartitionViewPoolOwners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		buildState func() cpustate.ReadonlyState
		wantOwners map[model.CPUSetPoolIdentity]model.DesiredPoolOwner
	}{
		{
			name: "records final pool owners and excludes non-reportable containers",
			buildState: func() cpustate.ReadonlyState {
				state := cpustate.NewCPUPluginState(nil)
				state.SetAllowSharedCoresOverlapReclaimedCores(false)
				for poolName, cpus := range map[string]machine.CPUSet{
					commonstate.PoolNameReserve: machine.NewCPUSet(15),
					commonstate.PoolNameReclaim: machine.NewCPUSet(6, 7),
					"rp-a/share-NUMA0":          machine.NewCPUSet(0, 1),
					"rp-b/share-NUMA1":          machine.NewCPUSet(8, 9),
					"isolation-final":           machine.NewCPUSet(4, 5, 10),
					"system-services":           machine.NewCPUSet(11),
				} {
					state.SetAllocationInfo(poolName, commonstate.FakedContainerName, &cpustate.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(poolName),
						AllocationResult: cpus,
					})
				}

				state.SetAllocationInfo("snb-ramp-up", "main", &cpustate.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "snb-ramp-up",
						ContainerName: "main",
						OwnerPoolName: "isolation-transient",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							apiconsts.PodAnnotationResourcePackageKey:           "rp-c",
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
						},
					},
					RampUp:           true,
					AllocationResult: machine.NewCPUSet(6),
				})
				for containerName, cpus := range map[string]machine.CPUSet{
					"main":    machine.NewCPUSet(2),
					"sidecar": machine.NewCPUSet(3),
				} {
					state.SetAllocationInfo("dedicated-uid", containerName, &cpustate.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "dedicated-uid",
							PodNamespace:  "default",
							PodName:       "dedicated-pod",
							ContainerName: containerName,
							OwnerPoolName: commonstate.PoolNameDedicated,
							QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						AllocationResult: cpus,
					})
				}
				state.SetAllocationInfo("isolation-uid", "main", &cpustate.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "isolation-uid",
						PodNamespace:  "default",
						PodName:       "isolation-pod",
						ContainerName: "main",
						OwnerPoolName: "rp-a/isolation-workload",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: machine.NewCPUSet(4, 5),
				})
				state.SetAllocationInfo("malformed-isolation", "main", &cpustate.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						ContainerName: "main",
						OwnerPoolName: "rp-a/isolation-malformed",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: machine.NewCPUSet(10),
				})
				for podUID, allocation := range map[string]*cpustate.AllocationInfo{
					"reserve-container": {
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "reserve-container",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameReserve,
						},
						AllocationResult: machine.NewCPUSet(12),
					},
					"system-container": {
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "system-container",
							ContainerName: "main",
							OwnerPoolName: "system-services",
							QoSLevel:      apiconsts.PodAnnotationQoSLevelSystemCores,
						},
						AllocationResult: machine.NewCPUSet(11),
					},
					"reclaimed-container": {
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "reclaimed-container",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameReclaim,
							QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						AllocationResult: machine.NewCPUSet(7),
					},
					"shared-container": {
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "shared-container",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameShare,
							QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						AllocationResult: machine.NewCPUSet(13),
					},
				} {
					state.SetAllocationInfo(podUID, "main", allocation)
				}
				state.SetAllocationInfo("nil-entry", "main", nil)
				return state
			},
			wantOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
				{Kind: model.CPUSetPoolKindReclaim}: {
					ExpectedCPUSet: machine.NewCPUSet(7),
				},
				{Kind: model.CPUSetPoolKindShare, Name: "rp-a/share-NUMA0"}: {
					ExpectedCPUSet: machine.NewCPUSet(0, 1),
				},
				{Kind: model.CPUSetPoolKindShare, Name: "rp-b/share-NUMA1"}: {
					ExpectedCPUSet: machine.NewCPUSet(8, 9),
				},
				{Kind: model.CPUSetPoolKindShare, Name: "rp-c/share-NUMA0"}: {
					ExpectedCPUSet: machine.NewCPUSet(6),
				},
				{Kind: model.CPUSetPoolKindDedicated, PodNamespace: "default", PodName: "dedicated-pod"}: {
					ProofPodUID:    "dedicated-uid",
					ExpectedCPUSet: machine.NewCPUSet(2, 3),
					ContainerCPUSetByName: map[string]machine.CPUSet{
						"main":    machine.NewCPUSet(2),
						"sidecar": machine.NewCPUSet(3),
					},
				},
				{Kind: model.CPUSetPoolKindIsolation, PodNamespace: "default", PodName: "isolation-pod"}: {
					ProofPodUID:    "isolation-uid",
					ExpectedCPUSet: machine.NewCPUSet(4, 5),
					ContainerCPUSetByName: map[string]machine.CPUSet{
						"main": machine.NewCPUSet(4, 5),
					},
				},
				{Kind: model.CPUSetPoolKindIsolation}: {
					ExpectedCPUSet: machine.NewCPUSet(10),
					ContainerCPUSetByName: map[string]machine.CPUSet{
						"main": machine.NewCPUSet(10),
					},
				},
			},
		},
		{
			name: "shared owner uses final overlap-filtered share pool map",
			buildState: func() cpustate.ReadonlyState {
				state := cpustate.NewCPUPluginState(nil)
				state.SetAllowSharedCoresOverlapReclaimedCores(true)
				state.SetAllocationInfo("share-NUMA0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("share-NUMA0"),
					AllocationResult: machine.NewCPUSet(1),
				})
				state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.NewCPUSet(2),
				})
				state.SetAllocationInfo("snb-ramp-up", "main", &cpustate.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "snb-ramp-up",
						ContainerName: "main",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
						},
					},
					RampUp:           true,
					AllocationResult: machine.NewCPUSet(2, 3),
				})
				return state
			},
			wantOwners: map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{
				{Kind: model.CPUSetPoolKindReclaim}: {
					ExpectedCPUSet: machine.NewCPUSet(2),
				},
				{Kind: model.CPUSetPoolKindShare, Name: "share-NUMA0"}: {
					ExpectedCPUSet: machine.NewCPUSet(1, 3),
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			view, _ := BuildCPUSetPartitionView(tt.buildState(), testTwoNUMATopologyN(8), CPUSetPartitionViewOptions{})
			if len(view.PoolOwners) != len(tt.wantOwners) {
				t.Fatalf("PoolOwners count = %d, want %d: %#v", len(view.PoolOwners), len(tt.wantOwners), view.PoolOwners)
			}
			for identity, want := range tt.wantOwners {
				got, ok := view.PoolOwners[identity]
				if !ok {
					t.Errorf("PoolOwners missing identity %+v", identity)
					continue
				}
				if !got.ExpectedCPUSet.Equals(want.ExpectedCPUSet) {
					t.Errorf("PoolOwners[%+v].ExpectedCPUSet = %s, want %s",
						identity, got.ExpectedCPUSet.String(), want.ExpectedCPUSet.String())
				}
				if len(got.ContainerCPUSetByName) != len(want.ContainerCPUSetByName) {
					t.Errorf("PoolOwners[%+v] container count = %d, want %d",
						identity, len(got.ContainerCPUSetByName), len(want.ContainerCPUSetByName))
					continue
				}
				for containerName, wantCPUSet := range want.ContainerCPUSetByName {
					if gotCPUSet, ok := got.ContainerCPUSetByName[containerName]; !ok || !gotCPUSet.Equals(wantCPUSet) {
						t.Errorf("PoolOwners[%+v].ContainerCPUSetByName[%q] = %s, want %s",
							identity, containerName, gotCPUSet.String(), wantCPUSet.String())
					}
				}
			}
		})
	}
}

func TestBuildCPUSetPartitionViewProtectsSNBRampUpTransiently(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3),
	})
	state.SetAllocationInfo("snb-pod", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-pod",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(2),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{})

	assertCPUSet(t, "snb container", view.ContainerCPUSetByPod["snb-pod"]["main"], "2")
	// The active SNB CPU stays in the share pool for RDT/declared-pool bookkeeping,
	// but it is excluded from the reclaim derive and protected transiently.
	assertCPUSet(t, "share pool keeps snb ramp-up for RDT", view.SharePool, "1-2")
	assertCPUSet(t, "transient protected non reclaim", view.TransientProtectedNonReclaim, "2")
	// DesiredReclaimEffective stays the SysAdvisor target (the full raw reclaim budget 2-3);
	// the physical ReclaimEffective is transient-trimmed so it never reclaims CPU 2.
	assertCPUSet(t, "desired reclaim effective (SA target)", view.DesiredReclaimEffective, "2-3")
	assertCPUSet(t, "non reclaim protects snb ramp-up", view.NonReclaimPool, "1-2,4-7")
	assertCPUSet(t, "reclaim excludes snb ramp-up", view.ReclaimEffective, "3")
}

func TestNormalizeDefaultSharePlaceholder(t *testing.T) {
	t.Parallel()

	baseline := machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7)
	reclaim := machine.NewCPUSet(2, 3, 6, 7)
	dedicated := machine.NewCPUSet(5)
	isolation := machine.NewCPUSet(4)
	rampUp := machine.NewCPUSet(2)
	shareMap := map[string]machine.CPUSet{
		commonstate.PoolNameShare: baseline.Clone(),
		"share-NUMA0":             machine.NewCPUSet(2),
	}
	rampUpByPool := map[string]machine.CPUSet{"share-NUMA0": machine.NewCPUSet(2)}

	gotShare, gotMap, normalized := normalizeDefaultSharePlaceholder(
		false, baseline, baseline, shareMap, reclaim, dedicated, isolation, rampUp, rampUpByPool)
	if !normalized {
		t.Fatal("full default share placeholder with non-empty reclaim must be normalized")
	}
	assertCPUSet(t, "normalized share", gotShare, "1-2")
	assertCPUSet(t, "normalized default share map", gotMap[commonstate.PoolNameShare], "1")
	assertCPUSet(t, "normalized ramp-up share map", gotMap["share-NUMA0"], "2")

	// The helper is pure: caller-owned inputs remain unchanged and running it
	// again is idempotent because the normalized share no longer matches the
	// default placeholder fingerprint.
	assertCPUSet(t, "input share", baseline, "1-7")
	assertCPUSet(t, "input default share map", shareMap[commonstate.PoolNameShare], "1-7")
	shareAgain, mapAgain, normalizedAgain := normalizeDefaultSharePlaceholder(
		false, baseline, gotShare, gotMap, reclaim, dedicated, isolation, rampUp, rampUpByPool)
	if normalizedAgain || !shareAgain.Equals(gotShare) || !mapAgain[commonstate.PoolNameShare].Equals(gotMap[commonstate.PoolNameShare]) {
		t.Fatalf("normalization is not idempotent: normalized=%t share=%s map=%s",
			normalizedAgain, shareAgain.String(), mapAgain[commonstate.PoolNameShare].String())
	}

	for _, tc := range []struct {
		name         string
		allowOverlap bool
		share        machine.CPUSet
		raw          machine.CPUSet
	}{
		{name: "overlap", allowOverlap: true, share: baseline, raw: reclaim},
		{name: "cold start without reclaim", share: baseline, raw: machine.NewCPUSet()},
		{name: "explicit partial share", share: machine.NewCPUSet(1, 4), raw: reclaim},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, changed := normalizeDefaultSharePlaceholder(
				tc.allowOverlap, baseline, tc.share, shareMap, tc.raw,
				dedicated, isolation, rampUp, rampUpByPool)
			if changed || !got.Equals(tc.share) {
				t.Fatalf("unexpected normalization: changed=%t share=%s want=%s", changed, got.String(), tc.share.String())
			}
		})
	}
}

func FuzzNormalizeDefaultSharePlaceholder(f *testing.F) {
	for _, seed := range []struct {
		reclaim   uint16
		dedicated uint16
		isolation uint16
		rampUp    uint16
	}{
		{0x00f0, 0x000c, 0x0003, 0x0010},
		{0xaaaa, 0x0005, 0x0050, 0x0800},
		{0xffff, 0x0000, 0x0000, 0x0001},
	} {
		f.Add(seed.reclaim, seed.dedicated, seed.isolation, seed.rampUp)
	}

	f.Fuzz(func(t *testing.T, reclaimMask, dedicatedMask, isolationMask, rampUpMask uint16) {
		baseline := machine.NewCPUSet()
		for cpu := 0; cpu < 16; cpu++ {
			baseline.Add(cpu)
		}
		fromMask := func(mask uint16) machine.CPUSet {
			out := machine.NewCPUSet()
			for cpu := 0; cpu < 16; cpu++ {
				if mask&(1<<cpu) != 0 {
					out.Add(cpu)
				}
			}
			return out
		}
		reclaim := fromMask(reclaimMask)
		if reclaim.IsEmpty() {
			return
		}
		dedicated := fromMask(dedicatedMask)
		isolation := fromMask(isolationMask)
		rampUp := fromMask(rampUpMask)
		shareMap := map[string]machine.CPUSet{commonstate.PoolNameShare: baseline.Clone()}
		rampUpByPool := map[string]machine.CPUSet{commonstate.PoolNameShare: rampUp.Clone()}

		got, gotMap, changed := normalizeDefaultSharePlaceholder(
			false, baseline, baseline, shareMap, reclaim, dedicated, isolation, rampUp, rampUpByPool)
		if !changed {
			t.Fatal("non-overlap full default share with non-empty reclaim was not normalized")
		}
		fixed := reclaim.Union(dedicated).Union(isolation)
		if overlap := got.Intersection(fixed).Difference(rampUp); !overlap.IsEmpty() {
			t.Fatalf("normalized share retains fixed owners outside explicit ramp-up: %s", overlap.String())
		}
		if missing := rampUp.Difference(got); !missing.IsEmpty() {
			t.Fatalf("normalized share lost explicit ramp-up owners: %s", missing.String())
		}
		if !shareMap[commonstate.PoolNameShare].Equals(baseline) {
			t.Fatal("normalization mutated caller-owned share map")
		}
		gotAgain, mapAgain, _ := normalizeDefaultSharePlaceholder(
			false, baseline, got, gotMap, reclaim, dedicated, isolation, rampUp, rampUpByPool)
		if !gotAgain.Equals(got) || !mapAgain[commonstate.PoolNameShare].Equals(gotMap[commonstate.PoolNameShare]) {
			t.Fatal("normalization output is not idempotent")
		}
	})
}

func TestSoftDefaultSharePlaceholderDoesNotCollapseReclaim(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 4),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})

	view, _, err := BuildValidatedCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v", err)
	}
	assertCPUSet(t, "soft default share residual", view.SharePool, "1,5")
	assertCPUSet(t, "soft default share map residual", view.SharePoolMap[commonstate.PoolNameShare], "1,5")
	assertCPUSet(t, "soft non reclaim", view.NonReclaimPool, "1,5")
	assertCPUSet(t, "soft reclaim effective", view.ReclaimEffective, "2-3,6-7")
	assertCPUSet(t, "soft desired reclaim effective", view.DesiredReclaimEffective, "2-3,6-7")
	if !view.NonReclaimPool.Intersection(view.DesiredReclaimEffective).IsEmpty() {
		t.Fatalf("non-reclaim and desired reclaim overlap: non=%s reclaim=%s",
			view.NonReclaimPool.String(), view.DesiredReclaimEffective.String())
	}
	if excess := view.DesiredReclaimEffective.Difference(view.ReclaimRaw); !excess.IsEmpty() {
		t.Fatalf("desired reclaim exceeds raw: desired=%s raw=%s excess=%s",
			view.DesiredReclaimEffective.String(), view.ReclaimRaw.String(), excess.String())
	}
}

func TestImp6DefaultShareNormalizationReachesSystemSibling(t *testing.T) {
	t.Parallel()

	topologyFixture := make4NUMASMTTopology()
	allCPUs := topologyFixture.CPUDetails.CPUs()
	reclaim := machine.NewCPUSet(4, 20, 36, 52, 68, 84, 100, 116)
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: allCPUs.Clone(),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: reclaim.Clone(),
	})

	view, _, err := BuildValidatedCPUSetPartitionView(state, topologyFixture, CPUSetPartitionViewOptions{})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v", err)
	}
	if !view.DesiredReclaimEffective.Equals(reclaim) {
		t.Fatalf("desired reclaim = %s, want %s", view.DesiredReclaimEffective.String(), reclaim.String())
	}
	for numaID := 0; numaID < 4; numaID++ {
		if got := view.DesiredReclaimEffectivePerNUMA[numaID].Size(); got != 2 {
			t.Fatalf("NUMA %d desired reclaim size = %d, want one SMT2 core", numaID, got)
		}
	}

	cfg := bulkheadconfig.BulkheadConfiguration{BulkheadPrimaryRelPath: "custom-primary-root"}
	specs, err := BuildTopologyNodeSpecsFromView(cfg, &view.CPUSetPartitionView, topologyFixture.CPUDetails, []string{"system"}, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView() error = %v", err)
	}
	for _, spec := range specs {
		if spec.Rel == "system" {
			if !spec.CPUs.Equals(reclaim) {
				t.Fatalf("system reclaim sibling CPUs = %s, want %s", spec.CPUs.String(), reclaim.String())
			}
			shareEntry := state.GetPodEntries()[commonstate.PoolNameShare][commonstate.FakedContainerName]
			if shareEntry == nil || !shareEntry.AllocationResult.Equals(allCPUs) {
				t.Fatalf("business default share allocation was mutated: %#v", shareEntry)
			}
			reclaimEntry := state.GetPodEntries()[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
			if reclaimEntry == nil || !reclaimEntry.AllocationResult.Equals(reclaim) {
				t.Fatalf("business reclaim allocation was mutated: %#v", reclaimEntry)
			}
			return
		}
	}
	t.Fatal("system reclaim sibling spec missing")
}

func TestGateAHardPartitionDefaultShareBaselineDoesNotSwallowReclaim(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 4),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})

	view, _, err := BuildValidatedCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		HardPartitionEnabled:              true,
		HardPartitionReclaimTargetPerNUMA: map[int]int{0: 2, 1: 2},
	})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v", err)
	}

	assertCPUSet(t, "default share residual", view.SharePool, "1,5")
	assertCPUSet(t, "default share map residual", view.SharePoolMap[commonstate.PoolNameShare], "1,5")
	assertCPUSet(t, "non reclaim residual", view.NonReclaimPool, "1,5")
	assertCPUSet(t, "reclaim effective", view.ReclaimEffective, "2-3,6-7")
	assertCPUSet(t, "reclaim numa 0", view.ReclaimEffectivePerNUMA[0], "2-3")
	assertCPUSet(t, "reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "6-7")
}

func TestGateAHardPartitionDefaultShareBaselineExcludesFixedOwners(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})
	state.SetAllocationInfo("isolation-0", commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("isolation-0"),
		AllocationResult: machine.NewCPUSet(4),
	})
	state.SetAllocationInfo("dedicated-pod", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "dedicated-pod",
			ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		AllocationResult: machine.NewCPUSet(5),
	})

	view, _, err := BuildValidatedCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		HardPartitionEnabled:              true,
		HardPartitionReclaimTargetPerNUMA: map[int]int{0: 2, 1: 2},
	})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v", err)
	}

	assertCPUSet(t, "default share excludes fixed owners", view.SharePool, "1")
	assertCPUSet(t, "default share map excludes fixed owners", view.SharePoolMap[commonstate.PoolNameShare], "1")
	assertCPUSet(t, "dedicated remains dedicated", view.Dedicated, "5")
	assertCPUSet(t, "isolation remains isolated", view.Isolation, "4")
	assertCPUSet(t, "reclaim effective", view.ReclaimEffective, "2-3,6-7")
}

func TestGateBSNBRampUpUsesDeclaredShareNUMAPoolForRDT(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 4),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 5),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})
	state.SetAllocationInfo("snb-ramp-up", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-ramp-up",
			ContainerName: "main",
			OwnerPoolName: "isolation-snb-ramp-up",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
			},
		},
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(2),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		HardPartitionEnabled: true,
	})

	assertCPUSet(t, "aggregate share protects SNB ramp-up", view.SharePool, "1-2,5")
	assertCPUSet(t, "default share RDT target", view.SharePoolMap[commonstate.PoolNameShare], "1,5")
	assertCPUSet(t, "declared share-NUMA0 RDT target", view.SharePoolMap["share-NUMA0"], "2")
	if _, ok := view.SharePoolMap["isolation-snb-ramp-up"]; ok {
		t.Fatal("SharePoolMap contains isolation owner instead of declared share-NUMA0 pool")
	}
}

func TestBuildCPUSetPartitionViewPreservesTwoReservedCPUs(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 24),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 25),
	})
	view, _ := BuildCPUSetPartitionView(state, &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}, 24: {NUMANodeID: 1}, 25: {NUMANodeID: 1},
	}}, CPUSetPartitionViewOptions{})

	assertCPUSet(t, "reserve", view.Reserve, "0,24")
	if got := view.Reserve.Size(); got != 2 {
		t.Fatalf("reserve size = %d, want 2", got)
	}
}

func TestBuildCPUSetPartitionViewNilInputs(t *testing.T) {
	t.Parallel()

	view, _ := BuildCPUSetPartitionView(nil, nil, CPUSetPartitionViewOptions{})
	if view == nil || !view.ReclaimEffective.IsEmpty() || len(view.ContainerCPUSetByPod) != 0 {
		t.Fatalf("unexpected nil input view: %#v", view)
	}
	if view.DeepCopy() == nil {
		t.Fatalf("DeepCopy of non-nil empty view returned nil")
	}
	if (*model.DesiredView)(nil).DeepCopy() != nil {
		t.Fatalf("DeepCopy of nil view should be nil")
	}
}

func TestBuildCPUSetPartitionViewKeepsDenseReclaimPerNUMAKeys(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(5, 6),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{})

	assertCPUSet(t, "desired reclaim numa 0 empty", view.DesiredReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "desired reclaim numa 1", view.DesiredReclaimEffectivePerNUMA[1], "5-6")
	assertCPUSet(t, "effective reclaim numa 0 empty", view.ReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "effective reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "5-6")
	if _, ok := view.ReclaimEffectivePerNUMA[0]; !ok {
		t.Fatalf("empty NUMA 0 reclaim bucket must be preserved")
	}
	if _, ok := view.DesiredReclaimEffectivePerNUMA[0]; !ok {
		t.Fatalf("empty NUMA 0 desired reclaim bucket must be preserved")
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

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "reserve", view.Reserve, "0")
	assertCPUSet(t, "non reclaim absorbs CPUs without raw reclaim", view.NonReclaimPool, "1-7")
	assertCPUSet(t, "reclaim effective is empty without raw reclaim", view.ReclaimEffective, "")
	assertCPUSet(t, "empty reclaim numa 0", view.ReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "empty reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "")
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

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		TransientProtectedNonReclaim: machine.NewCPUSet(3, 4),
	})

	assertCPUSet(t, "desired non reclaim", view.DesiredNonReclaimPool, "1-7")
	assertCPUSet(t, "desired reclaim", view.DesiredReclaimEffective, "")
	assertCPUSet(t, "protected non reclaim", view.TransientProtectedNonReclaim, "3-4")
	assertCPUSet(t, "applied non reclaim", view.NonReclaimPool, "1-7")
	assertCPUSet(t, "applied reclaim", view.ReclaimEffective, "")
	if !view.NonReclaimPool.Intersection(view.ReclaimEffective).IsEmpty() {
		t.Fatalf("applied non reclaim and reclaim overlap: non=%s reclaim=%s", view.NonReclaimPool.String(), view.ReclaimEffective.String())
	}
	assertCPUSet(t, "empty reclaim numa 0", view.ReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "empty reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "")
}

func TestApplyTransientProtectedNonReclaimRebuildsReclaimPerNUMA(t *testing.T) {
	t.Parallel()

	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		Reserve:                        machine.NewCPUSet(),
		DesiredNonReclaimPool:          machine.NewCPUSet(1, 2),
		DesiredReclaimEffective:        machine.NewCPUSet(3, 4, 5, 6, 7),
		NonReclaimPool:                 machine.NewCPUSet(1, 2),
		ReclaimEffective:               machine.NewCPUSet(3, 4, 5, 6, 7),
		ReclaimEffectivePerNUMA:        map[int]machine.CPUSet{0: machine.NewCPUSet(3), 1: machine.NewCPUSet(4, 5, 6, 7)},
		DesiredReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(3), 1: machine.NewCPUSet(4, 5, 6, 7)},
	}}

	ApplyTransientProtectedNonReclaim(view, testTwoNUMATopology(), machine.NewCPUSet(3, 4))

	assertCPUSet(t, "applied reclaim", view.ReclaimEffective, "5-7")
	assertCPUSet(t, "applied reclaim numa 0 empty", view.ReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "applied reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "5-7")
	err := ValidateCPUSetPartitionView(view, testTwoNUMATopology())
	if err != nil {
		t.Fatalf("ValidateCPUSetPartitionView() rejected exact projection with empty NUMA bucket: %v", err)
	}
}

// TestBuildCPUSetPartitionViewProtectsActiveSNBRampUp is the production-shape RED
// guard: a 128-CPU / 4-NUMA / SMT2 node, share/reclaim overlap OFF and
// HardPartition ON. SysAdvisor publishes a raw reclaim budget of 100 CPUs (0-99) that
// TOTALLY COVERS an already-active shared-NUMA-binding ramp-up container holding the
// exact 24 CPUs 4-15,68-79. The active SNB CPUs must be protected on the physical
// reclaim side via the transient protected non-reclaim set, while DesiredReclaimEffective
// stays at the SysAdvisor target and ReclaimEffective becomes the physically enforced,
// transient-trimmed set.
func TestBuildCPUSetPartitionViewProtectsActiveSNBRampUp(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology() // 128 CPUs, 64 cores (SMT2), 4 NUMA nodes
	protected := machine.MustParse("4-15,68-79")

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	// SysAdvisor raw reclaim budget: 100 CPUs, fully covering the active SNB set.
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.MustParse("0-99"),
	})
	// Active SNB ramp-up container (SharedCores + numa-binding annotation + RampUp).
	state.SetAllocationInfo("snb-1", "main", sharedRampUpAllocation("snb-1", protected.Clone(), true))

	view, _ := BuildCPUSetPartitionView(state, topo, CPUSetPartitionViewOptions{HardPartitionEnabled: true})

	// The active SNB exact CPUs are now the transient protected non-reclaim set.
	assertCPUSet(t, "transient protected non reclaim", view.TransientProtectedNonReclaim, "4-15,68-79")
	// DesiredReclaimEffective remains the SysAdvisor target (the raw reclaim budget),
	// NOT shrunk by the transient protection.
	assertCPUSet(t, "desired reclaim effective (SA target)", view.DesiredReclaimEffective, "0-99")
	// The physical ReclaimEffective must never reclaim the active SNB CPUs.
	if overlap := view.ReclaimEffective.Intersection(protected); !overlap.IsEmpty() {
		t.Fatalf("active SNB CPUs leaked into physical reclaim: %s", overlap.String())
	}
	// Non-reclaim pool claims the active SNB CPUs and is disjoint from reclaim.
	if !protected.IsSubsetOf(view.NonReclaimPool) {
		t.Fatalf("non reclaim pool %s does not contain protected SNB %s", view.NonReclaimPool.String(), protected.String())
	}
	if cross := view.NonReclaimPool.Intersection(view.ReclaimEffective); !cross.IsEmpty() {
		t.Fatalf("non reclaim pool and physical reclaim overlap: %s", cross.String())
	}
}

// TestBuildCPUSetPartitionViewSNBProtectionBoundaries pins the predicate gates and
// the overlap disablement: only RampUp && Shared && SharedNUMABinding && !Reclaimed &&
// nonempty allocations are protected; the protection is silent when overlap is on.
func TestBuildCPUSetPartitionViewSNBProtectionBoundaries(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	build := func(overlap bool, allocations ...*cpustate.AllocationInfo) *model.DesiredView {
		s := cpustate.NewCPUPluginState(nil)
		s.SetAllowSharedCoresOverlapReclaimedCores(overlap)
		s.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: machine.MustParse("0-99"),
		})
		for _, ai := range allocations {
			s.SetAllocationInfo(ai.PodUid, ai.ContainerName, ai)
		}
		view, _ := BuildCPUSetPartitionView(s, topo, CPUSetPartitionViewOptions{HardPartitionEnabled: true})
		return view
	}

	// (a) overlap=true disables the transient protection entirely.
	if v := build(true, sharedRampUpAllocation("snb-1", machine.MustParse("4-5"), true)); !v.TransientProtectedNonReclaim.IsEmpty() {
		t.Fatalf("overlap=true must disable transient SNB protection, got %s", v.TransientProtectedNonReclaim.String())
	}

	// (b) non-SNB shared ramp-up must NOT be protected as exact (it projects instead).
	if v := build(false, sharedRampUpAllocation("share-1", machine.MustParse("4-5"), false)); !v.TransientProtectedNonReclaim.IsEmpty() {
		t.Fatalf("non-SNB shared ramp-up must not be transient protected, got %s", v.TransientProtectedNonReclaim.String())
	}

	// (c) materialized (RampUp=false) SNB must NOT be protected.
	steadySNB := sharedRampUpAllocation("snb-2", machine.MustParse("4-5"), true)
	steadySNB.RampUp = false
	if v := build(false, steadySNB); !v.TransientProtectedNonReclaim.IsEmpty() {
		t.Fatalf("materialized SNB must not be transient protected, got %s", v.TransientProtectedNonReclaim.String())
	}

	// (d) multiple active SNB containers union into one protected set.
	v := build(false,
		sharedRampUpAllocation("snb-a", machine.MustParse("4-5"), true),
		sharedRampUpAllocation("snb-b", machine.MustParse("68-69"), true),
	)
	assertCPUSet(t, "union of active SNB containers", v.TransientProtectedNonReclaim, "4-5,68-69")
}

func TestBuildCPUSetPartitionViewPadsNonReclaimPoolReversely(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
		ReserveCPUReversely:   true,
	})

	assertCPUSet(t, "reverse non reclaim absorbs CPUs without raw reclaim", view.NonReclaimPool, "1-7")
	assertCPUSet(t, "reverse reclaim effective is empty without raw reclaim", view.ReclaimEffective, "")
	assertCPUSet(t, "reverse empty reclaim numa 0", view.ReclaimEffectivePerNUMA[0], "")
	assertCPUSet(t, "reverse empty reclaim numa 1", view.ReclaimEffectivePerNUMA[1], "")
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

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "non reclaim remains empty", view.NonReclaimPool, "")
	assertCPUSet(t, "reclaim effective remains raw", view.ReclaimEffective, "1-3")
}

func TestBuildCPUSetPartitionViewBoundsReclaimEffectiveByReclaimRaw(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(3, 4),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{})

	assertCPUSet(t, "reclaim effective is bounded by raw", view.ReclaimEffective, "3-4")
	assertCPUSet(t, "non reclaim absorbs CPUs outside raw", view.NonReclaimPool, "1-2,5-7")
	if excess := view.ReclaimEffective.Difference(view.ReclaimRaw); !excess.IsEmpty() {
		t.Fatalf("reclaim effective exceeds reclaim raw: effective=%s raw=%s excess=%s",
			view.ReclaimEffective.String(), view.ReclaimRaw.String(), excess.String())
	}
}

func TestBuildCPUSetPartitionViewCapsPaddingToCandidates(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5),
	})

	view, _ := BuildCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize: 4,
	})

	assertCPUSet(t, "non reclaim capped to candidates", view.NonReclaimPool, "6-7")
	assertCPUSet(t, "reclaim effective exhausted", view.ReclaimEffective, "")
}

func TestBuildCPUSetPartitionViewValidatesHardPartitionDistribution(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		reclaim              machine.CPUSet
		hardPartitionEnabled bool
		targetPerNUMA        map[int]int
		wantErr              string
	}{
		{
			name:                 "balanced two per NUMA",
			reclaim:              machine.NewCPUSet(0, 1, 4, 5),
			hardPartitionEnabled: true,
			targetPerNUMA:        map[int]int{0: 2, 1: 2},
		},
		{
			name:                 "asymmetric reclaim above immutable targets",
			reclaim:              machine.NewCPUSet(0, 1, 2, 3, 4, 5),
			hardPartitionEnabled: true,
			targetPerNUMA:        map[int]int{0: 2, 1: 2},
		},
		{
			name:                 "hard partition rejects four and zero",
			reclaim:              machine.NewCPUSet(0, 1, 2, 3),
			hardPartitionEnabled: true,
			targetPerNUMA:        map[int]int{0: 2, 1: 2},
			wantErr:              "NUMA 1 has 0 CPUs, target is 2",
		},
		{
			name:                 "hard partition rejects reclaim below configured target",
			reclaim:              machine.NewCPUSet(0, 1, 2, 4, 5),
			hardPartitionEnabled: true,
			targetPerNUMA:        map[int]int{0: 3, 1: 3},
			wantErr:              "NUMA 1 has 2 CPUs, target is 3",
		},
		{
			name:                 "disabled skips validation",
			reclaim:              machine.NewCPUSet(0, 1, 2, 3),
			hardPartitionEnabled: false,
			targetPerNUMA:        map[int]int{0: 2, 1: 2},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := cpustate.NewCPUPluginState(nil)
			state.SetAllowSharedCoresOverlapReclaimedCores(true)
			state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: tc.reclaim,
			})

			view, _, err := BuildValidatedCPUSetPartitionView(state, testTwoNUMATopology(), CPUSetPartitionViewOptions{
				HardPartitionEnabled:              tc.hardPartitionEnabled,
				HardPartitionReclaimTargetPerNUMA: tc.targetPerNUMA,
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v", err)
				}
				if view == nil {
					t.Fatal("BuildValidatedCPUSetPartitionView() returned nil view")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildValidatedCPUSetPartitionViewHardPartitionPreservesLegalReclaim(t *testing.T) {
	t.Parallel()

	const perNUMA = 96
	topology := testTwoNUMATopologyN(perNUMA)

	// NUMA0: cpus 0..32 (33 = 28 floor + 5 raw slack); NUMA1: cpus 96..123 (28 floor).
	reclaimCPUs := make([]int, 0, 61)
	for cpu := 0; cpu <= 32; cpu++ {
		reclaimCPUs = append(reclaimCPUs, cpu)
	}
	for cpu := perNUMA; cpu <= perNUMA+27; cpu++ {
		reclaimCPUs = append(reclaimCPUs, cpu)
	}
	reclaim := machine.NewCPUSet(reclaimCPUs...)

	newReclaimState := func() cpustate.ReadonlyState {
		state := cpustate.NewCPUPluginState(nil)
		// Production hard-partition path runs with advisor overlap disabled.
		state.SetAllowSharedCoresOverlapReclaimedCores(false)
		state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: reclaim,
		})
		return state
	}

	// Bulkhead projects the already materialized partition. It must not trim
	// legal reclaim merely because the per-NUMA quantities are asymmetric.
	view, _, err := BuildValidatedCPUSetPartitionView(newReclaimState(), topology, CPUSetPartitionViewOptions{
		HardPartitionEnabled:              true,
		HardPartitionReclaimTargetPerNUMA: map[int]int{0: 28, 1: 28},
	})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView(hard=true) error = %v, want legal 33/28 projection", err)
	}
	if view == nil {
		t.Fatal("BuildValidatedCPUSetPartitionView(hard=true) returned nil view")
	}
	numa0 := view.ReclaimEffectivePerNUMA[0].Size()
	numa1 := view.ReclaimEffectivePerNUMA[1].Size()
	if numa0 != 33 || numa1 != 28 {
		t.Fatalf("hard=true reclaim effective per NUMA = %d/%d, want unchanged 33/28", numa0, numa1)
	}
	// Projection must preserve the existing disjoint partition.
	if overlap := view.NonReclaimPool.Intersection(view.ReclaimEffective); !overlap.IsEmpty() {
		t.Fatalf("hard=true produced non-reclaim/reclaim overlap: %s", overlap.String())
	}
	if desired0 := view.DesiredReclaimEffectivePerNUMA[0].Size(); desired0 != 33 {
		t.Fatalf("hard=true desired reclaim NUMA0 = %d, want 33", desired0)
	}

	// hard=false: behavior unchanged, raw slack shape (33/28) preserved.
	softView, _ := BuildCPUSetPartitionView(newReclaimState(), topology, CPUSetPartitionViewOptions{
		HardPartitionEnabled: false,
	})
	if softView == nil {
		t.Fatal("BuildCPUSetPartitionView(hard=false) returned nil view")
	}
	if got0, got1 := softView.ReclaimEffectivePerNUMA[0].Size(), softView.ReclaimEffectivePerNUMA[1].Size(); got0 != 33 || got1 != 28 {
		t.Fatalf("hard=false reclaim effective per NUMA = %d/%d, want unchanged 33/28", got0, got1)
	}
}

func TestBuildValidatedCPUSetPartitionViewSkipsSteadyExclusiveNUMA(t *testing.T) {
	t.Parallel()

	const perNUMA = 32
	topology := testTwoNUMATopologyN(perNUMA)

	// NUMA0 is wholly owned by a committed steady (RampUp=false) non-reclaim
	// exclusive DNB: 30 dedicated CPUs leave only a 2-CPU steady reclaim reserve
	// ({0,1}). NUMA1 carries the ratio-derived ramp-up reclaim (6 CPUs). The
	// per-NUMA hard-partition target is 6 for both NUMAs; without the carve-out
	// NUMA0 fails admission closed (2 < 6) for every other ramp-up QoS.
	dedicatedNUMA0 := make([]int, 0, perNUMA-2)
	for cpu := 2; cpu < perNUMA; cpu++ {
		dedicatedNUMA0 = append(dedicatedNUMA0, cpu)
	}
	reclaimCPUs := []int{0, 1}
	for cpu := perNUMA; cpu < perNUMA+6; cpu++ {
		reclaimCPUs = append(reclaimCPUs, cpu)
	}

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(reclaimCPUs...),
	})
	state.SetAllocationInfo("steady-exclusive-dnb", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "steady-exclusive-dnb",
			ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		RampUp:                   false,
		AllocationResult:         machine.NewCPUSet(dedicatedNUMA0...),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(dedicatedNUMA0...)},
	})

	view, _, err := BuildValidatedCPUSetPartitionView(state, topology, CPUSetPartitionViewOptions{
		HardPartitionEnabled:              true,
		HardPartitionReclaimTargetPerNUMA: map[int]int{0: 6, 1: 6},
	})
	if err != nil {
		t.Fatalf("BuildValidatedCPUSetPartitionView() error = %v, want steady exclusive NUMA0 skipped", err)
	}
	if view == nil {
		t.Fatal("BuildValidatedCPUSetPartitionView() returned nil view")
	}
	if got := view.ReclaimEffectivePerNUMA[0].Size(); got != 2 {
		t.Fatalf("steady exclusive NUMA0 reclaim effective = %d, want unchanged 2", got)
	}
	if got := view.ReclaimEffectivePerNUMA[1].Size(); got != 6 {
		t.Fatalf("ramp-up NUMA1 reclaim effective = %d, want 6", got)
	}
}

func testTwoNUMATopologyN(perNUMA int) *machine.CPUTopology {
	details := machine.CPUDetails{}
	for cpu := 0; cpu < perNUMA; cpu++ {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 0, SocketID: 0, CoreID: cpu}
	}
	for i := 0; i < perNUMA; i++ {
		cpu := perNUMA + i
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 1, SocketID: 1, CoreID: cpu}
	}
	return &machine.CPUTopology{
		NumCPUs:      2 * perNUMA,
		NumCores:     2 * perNUMA,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails:   details,
	}
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

func testTwoNUMASMTTopology() *machine.CPUTopology {
	return &machine.CPUTopology{
		NumCPUs:      8,
		NumCores:     4,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			4: {NUMANodeID: 1, SocketID: 1, CoreID: 2},
			5: {NUMANodeID: 1, SocketID: 1, CoreID: 2},
			6: {NUMANodeID: 1, SocketID: 1, CoreID: 3},
			7: {NUMANodeID: 1, SocketID: 1, CoreID: 3},
		},
	}
}

func assertCPUSet(t *testing.T, name string, got machine.CPUSet, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s cpuset = %s, want %s", name, got.String(), want)
	}
}

// TestE2EPendingSharedProjectionSideChannel strings the real prepare snapshot to
// the shadow: BuildCPUSetPartitionView returns the per-container side-channel, the
// pending choke point mirrors it onto PendingProtection.SharedProjectedCPUs, and
// DeriveShadowPartition projects onto the shared non-reclaim domain while leaving
// the business view byte-identical and reclaim at its eight strided CPUs.
func TestE2EPendingSharedProjectionSideChannel(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	reclaim := machine.NewCPUSet(4, 20, 36, 52, 68, 84, 100, 116)
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: reclaim,
	})
	state.SetAllocationInfo("pod-1", "main", sharedRampUpAllocation("pod-1", machine.NewCPUSet(0), false))

	view, projection := BuildCPUSetPartitionView(state, topo, CPUSetPartitionViewOptions{})
	if _, ok := projection["pod-1"]["main"]; !ok {
		t.Fatalf("side-channel missing eligible container: %v", projection)
	}

	before := view.DeepCopy()
	pendingCPUs := view.ContainerCPUSetByPod["pod-1"]["main"]
	// Choke-point mirror: an eligible container projects exactly its own CPUs.
	prot := topology.PendingProtection{
		PodUID:              "pod-1",
		CPUs:                pendingCPUs,
		SharedProjectedCPUs: pendingCPUs.Clone(),
	}
	sh := DeriveShadowPartition(view, topo, []topology.PendingProtection{prot})
	if sh.Err != nil {
		t.Fatalf("shadow projection failed: %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("unexpected hold-current: %s", sh.HoldReason)
	}
	if !sh.ReclaimEffective.Equals(view.DesiredReclaimEffective) {
		t.Fatalf("reclaim drifted: got %s want %s", sh.ReclaimEffective, view.DesiredReclaimEffective)
	}
	if sh.ReclaimEffective.Size() != 8 {
		t.Fatalf("reclaim size = %d, want 8", sh.ReclaimEffective.Size())
	}
	if !model.EqualDesiredView(view, before) {
		t.Fatalf("business DesiredView mutated by shadow projection")
	}
}

// TestPendingProjectionLifecycleRounds proves the side-channel follows the
// allocation lifecycle: a pending ramp-up is classified, a materialized/resolved
// allocation is not, and delete-recreate under a new UID classifies fresh without
// leaking the old pod's hint.
func TestPendingProjectionLifecycleRounds(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()

	// Round A: pending shared ramp-up non-SNB => eligible.
	stateA := cpustate.NewCPUPluginState(nil)
	stateA.SetAllowSharedCoresOverlapReclaimedCores(false)
	stateA.SetAllocationInfo("pod-1", "main", sharedRampUpAllocation("pod-1", machine.NewCPUSet(0), false))
	_, projA := BuildCPUSetPartitionView(stateA, topo, CPUSetPartitionViewOptions{})
	if _, ok := projA["pod-1"]["main"]; !ok {
		t.Fatalf("pending ramp-up must be eligible: %v", projA)
	}

	// Round B: same pod materialized/resolved (RampUp off) => not projected.
	stateB := cpustate.NewCPUPluginState(nil)
	stateB.SetAllowSharedCoresOverlapReclaimedCores(false)
	materialized := sharedRampUpAllocation("pod-1", machine.NewCPUSet(0), false)
	materialized.RampUp = false
	stateB.SetAllocationInfo("pod-1", "main", materialized)
	_, projB := BuildCPUSetPartitionView(stateB, topo, CPUSetPartitionViewOptions{})
	if _, ok := projB["pod-1"]; ok {
		t.Fatalf("materialized/resolved allocation must not be projected: %v", projB)
	}

	// Round C: deleted pod-1 recreated as pod-NEW; old hint must not leak and the
	// new allocation is classified fresh from its own AllocationInfo.
	stateC := cpustate.NewCPUPluginState(nil)
	stateC.SetAllowSharedCoresOverlapReclaimedCores(false)
	stateC.SetAllocationInfo("pod-NEW", "main", sharedRampUpAllocation("pod-NEW", machine.NewCPUSet(1), false))
	_, projC := BuildCPUSetPartitionView(stateC, topo, CPUSetPartitionViewOptions{})
	if _, ok := projC["pod-1"]; ok {
		t.Fatalf("deleted pod-1 hint leaked after recreate: %v", projC)
	}
	if _, ok := projC["pod-NEW"]["main"]; !ok {
		t.Fatalf("recreated pod-NEW must be freshly classified: %v", projC)
	}
}

// TestSharedProjectionTypedErrorZeroPublish proves a deterministic projection
// contradiction (half-core domain) is a terminal typed, fail-closed error: it is
// visible via errors.Is, is NOT mapped to Deferred/hold-current, emits zero
// non-reclaim/reclaim partition, and leaves the business base untouched.
func TestSharedProjectionTypedErrorZeroPublish(t *testing.T) {
	t.Parallel()
	topo := make4NUMASMTTopology()
	base := baseWholeCoreView()
	// Split core 0: drop sibling 16 from the non-reclaim pool => half-core domain.
	base.DesiredNonReclaimPool = base.DesiredNonReclaimPool.Clone().Difference(machine.NewCPUSet(16))
	before := base.DesiredNonReclaimPool.Clone()

	prot := topology.PendingProtection{
		PodUID:              "r",
		CPUs:                machine.NewCPUSet(0),
		SharedProjectedCPUs: machine.NewCPUSet(0),
	}
	sh := DeriveShadowPartition(base, topo, []topology.PendingProtection{prot})
	if sh.Err == nil {
		t.Fatalf("expected typed projection error, got hold=%v reason=%q", sh.HoldCurrent, sh.HoldReason)
	}
	if !errors.Is(sh.Err, ErrSharedProjectionHalfCoreDomain) {
		t.Fatalf("want half-core domain typed error, got %v", sh.Err)
	}
	if sh.HoldCurrent {
		t.Fatalf("typed error must NOT be hold-current/Deferred")
	}
	if !sh.NonReclaimPool.IsEmpty() || !sh.ReclaimEffective.IsEmpty() {
		t.Fatalf("zero-write violated: nonReclaim=%s reclaim=%s", sh.NonReclaimPool, sh.ReclaimEffective)
	}
	if !base.DesiredNonReclaimPool.Equals(before) {
		t.Fatalf("business base mutated by failed projection")
	}
}
