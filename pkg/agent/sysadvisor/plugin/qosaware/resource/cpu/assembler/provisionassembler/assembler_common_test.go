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

package provisionassembler

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type FakeRegion struct {
	name                       string
	ownerPoolName              string
	regionType                 types.QoSRegionType
	bindingNumas               machine.CPUSet
	isNumaBinding              bool
	podSets                    types.PodSet
	controlKnob                types.ControlKnob
	headroom                   float64
	throttled                  bool
	provisionCurrentPolicyName types.CPUProvisionPolicyName
	provisionPolicyTopPriority types.CPUProvisionPolicyName
	headroomCurrentPolicyName  types.CPUHeadroomPolicyName
	headroomPolicyTopPriority  types.CPUHeadroomPolicyName
	controlEssentials          types.ControlEssentials
	essentials                 types.ResourceEssentials
}

func NewFakeRegion(name string, regionType types.QoSRegionType, ownerPoolName string) *FakeRegion {
	return &FakeRegion{
		name:          name,
		regionType:    regionType,
		ownerPoolName: ownerPoolName,
	}
}

func (fake *FakeRegion) Name() string {
	return fake.name
}

func (fake *FakeRegion) Type() types.QoSRegionType {
	return fake.regionType
}

func (fake *FakeRegion) OwnerPoolName() string {
	return fake.ownerPoolName
}

func (fake *FakeRegion) IsEmpty() bool {
	return false
}
func (fake *FakeRegion) Clear() {}
func (fake *FakeRegion) GetBindingNumas() machine.CPUSet {
	return fake.bindingNumas
}

func (fake *FakeRegion) SetPods(podSet types.PodSet) {
	fake.podSets = podSet
}

func (fake *FakeRegion) GetPods() types.PodSet {
	return fake.podSets
}

func (fake *FakeRegion) SetBindingNumas(bindingNumas machine.CPUSet) {
	fake.bindingNumas = bindingNumas
}

func (fake *FakeRegion) SetEssentials(essentials types.ResourceEssentials) {
	fake.essentials = essentials
}

func (fake *FakeRegion) SetIsNumaBinding(isNumaBinding bool) {
	fake.isNumaBinding = isNumaBinding
}

func (fake *FakeRegion) IsNumaBinding() bool {
	return fake.isNumaBinding
}
func (fake *FakeRegion) SetThrottled(throttled bool)                { fake.throttled = throttled }
func (fake *FakeRegion) AddContainer(ci *types.ContainerInfo) error { return nil }
func (fake *FakeRegion) TryUpdateProvision()                        {}
func (fake *FakeRegion) TryUpdateHeadroom()                         {}
func (fake *FakeRegion) UpdateStatus()                              {}
func (fake *FakeRegion) SetProvision(controlKnob types.ControlKnob) {
	fake.controlKnob = controlKnob
}

func (fake *FakeRegion) GetProvision() (types.ControlKnob, error) {
	return fake.controlKnob, nil
}

func (fake *FakeRegion) SetHeadroom(value float64) {
	fake.headroom = value
}

func (fake *FakeRegion) GetHeadroom() (float64, error) {
	return fake.headroom, nil
}

func (fake *FakeRegion) IsThrottled() bool {
	return fake.throttled
}

func (fake *FakeRegion) SetProvisionPolicy(policyTopPriority, currentPolicyName types.CPUProvisionPolicyName) {
	fake.provisionPolicyTopPriority = policyTopPriority
	fake.provisionCurrentPolicyName = currentPolicyName
}

func (fake *FakeRegion) GetProvisionPolicy() (types.CPUProvisionPolicyName, types.CPUProvisionPolicyName) {
	return fake.provisionPolicyTopPriority, fake.provisionCurrentPolicyName
}

func (fake *FakeRegion) SetHeadRoomPolicy(policyTopPriority, currentPolicyName types.CPUHeadroomPolicyName) {
	fake.headroomPolicyTopPriority = policyTopPriority
	fake.headroomCurrentPolicyName = currentPolicyName
}

func (fake *FakeRegion) GetHeadRoomPolicy() (types.CPUHeadroomPolicyName, types.CPUHeadroomPolicyName) {
	return fake.headroomPolicyTopPriority, fake.headroomCurrentPolicyName
}

func (fake *FakeRegion) GetStatus() types.RegionStatus {
	return types.RegionStatus{}
}

func (fake *FakeRegion) SetControlEssentials(controlEssentials types.ControlEssentials) {
	fake.controlEssentials = controlEssentials
}

func (fake *FakeRegion) GetControlEssentials() types.ControlEssentials {
	return fake.controlEssentials
}

type testCasePoolConfig struct {
	poolName      string
	poolType      types.QoSRegionType
	numa          machine.CPUSet
	isNumaBinding bool
	provision     types.ControlKnob
}

func TestAssembleProvision(t *testing.T) {
	t.Parallel()

	containerInfos := []types.ContainerInfo{
		{
			PodUID:              "pod1",
			ContainerName:       "container1",
			QoSLevel:            consts.PodAnnotationQoSLevelSharedCores,
			RegionNames:         sets.NewString("share-NUMA1"),
			OriginOwnerPoolName: "share-NUMA1",
			OwnerPoolName:       "share-NUMA1",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8),
			},
		},
	}
	_ = containerInfos

	poolInfos := map[string]types.PoolInfo{
		"share-NUMA1": {
			PoolName: "share-NUMA1",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8),
			},
			RegionNames: sets.NewString("share-NUMA1"),
		},
		"share": {
			PoolName: "share",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
		},
		"isolation-NUMA1": {
			PoolName: "isolation-NUMA1",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(20, 21, 22, 23),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(20, 21, 22, 23),
			},
			RegionNames: sets.NewString("isolation-NUMA1"),
		},
		"isolation-NUMA1-pod2": {
			PoolName: "isolation-NUMA1-pod2",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(20, 21, 22, 23),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				1: machine.NewCPUSet(20, 21, 22, 23),
			},
			RegionNames: sets.NewString("isolation-NUMA1-pod2"),
		},
	}
	tests := []struct {
		name                                  string
		enableReclaimed                       bool
		allowSharedCoresOverlapReclaimedCores bool
		poolInfos                             []testCasePoolConfig
		expectPoolEntries                     map[string]map[int]int
		expectPoolOverlapInfo                 map[string]map[int]map[string]int
	}{
		{
			name:            "test1",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 6,
				},
				"share-NUMA1": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  16,
				},
			},
		},
		{
			name:            "test2",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 20,
				},
				"share-NUMA1": {
					1: 20,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 4,
					1:  4,
				},
			},
		},
		{
			name:            "test3",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 6,
				},
				"share-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  8,
				},
			},
		},
		{
			name:            "test4",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 20,
				},
				"share-NUMA1": {
					1: 12,
				},
				"isolation-NUMA1": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 4,
					1:  4,
				},
			},
		},
		{
			name:            "test5",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 15},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 20,
				},
				"share-NUMA1": {
					1: 16,
				},
				"isolation-NUMA1": {
					1: 4,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 4,
					1:  4,
				},
			},
		},
		{
			name:            "test6",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 15},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 6,
				},
				"share-NUMA1": {
					1: 15,
				},
				"isolation-NUMA1": {
					1: 4,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  5,
				},
			},
		},
		{
			name:            "test7",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 6,
				},
				"share-NUMA1": {
					1: 4,
				},
				"isolation-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1-pod2": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  4,
				},
			},
		},
		{
			name:            "test8",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 6,
				},
				"share-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1": {
					1: 4,
				},
				"isolation-NUMA1-pod2": {
					1: 4,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  8,
				},
			},
		},
		{
			name:            "test9",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 20,
				},
				"share-NUMA1": {
					1: 12,
				},
				"isolation-NUMA1": {
					1: 4,
				},
				"isolation-NUMA1-pod2": {
					1: 4,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 4,
					1:  4,
				},
			},
		},
		{
			name:                                  "share and isolated pool not throttled, overlap reclaimed cores",
			enableReclaimed:                       true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 24,
				},
				"share-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1-pod2": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 18,
					1:  4,
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {-1: map[string]int{"share": 18}, 1: map[string]int{"share-NUMA1": 4}},
			},
		},
		{
			name:                                  "share and isolated pool not throttled, overlap reclaimed cores, reclaim disabled",
			enableReclaimed:                       false,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      types.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      types.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						types.ControlKnobNonReclaimedCPURequirementUpper: {Value: 8},
						types.ControlKnobNonReclaimedCPURequirementLower: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]int{
				"share": {
					-1: 24,
				},
				"share-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1": {
					1: 8,
				},
				"isolation-NUMA1-pod2": {
					1: 8,
				},
				"reserve": {
					-1: 0,
				},
				"reclaim": {
					-1: 4,
					1:  4,
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {-1: map[string]int{"share": 4}, 1: map[string]int{"share-NUMA1": 4}},
			},
		},
	}

	reservedForReclaim := map[int]int{
		0: 4,
		1: 4,
	}

	numaAvailable := map[int]int{
		0: 20,
		1: 20,
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConf(t, tt.enableReclaimed)

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
			require.NoError(t, err)

			metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
			require.NoError(t, err)
			defer func() {
				os.RemoveAll(conf.GenericSysAdvisorConfiguration.StateFileDirectory)
				os.RemoveAll(conf.MetaServerConfiguration.CheckpointManagerDir)
			}()

			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
			require.NoError(t, err)

			nonBindingNumas := machine.NewCPUSet()
			for numaID := range numaAvailable {
				nonBindingNumas.Add(numaID)
			}

			regionMap := map[string]region.QoSRegion{}
			for _, poolConfig := range tt.poolInfos {
				poolInfo, ok := poolInfos[poolConfig.poolName]
				require.True(t, ok, "pool config doesn't exist")
				require.NoError(t, metaCache.SetPoolInfo(poolInfo.PoolName, &poolInfo), "failed to set pool info %s", poolInfo.PoolName)
				region := NewFakeRegion(poolConfig.poolName, poolConfig.poolType, poolConfig.poolName)
				region.SetBindingNumas(poolConfig.numa)
				region.SetIsNumaBinding(poolConfig.isNumaBinding)
				region.SetProvision(poolConfig.provision)
				region.TryUpdateProvision()
				require.Equal(t, poolConfig.isNumaBinding, region.IsNumaBinding(), "invalid numa binding state")
				regionMap[region.name] = region

				if region.IsNumaBinding() {
					nonBindingNumas = nonBindingNumas.Difference(region.GetBindingNumas())
				}
			}

			common := NewProvisionAssemblerCommon(conf, nil, &regionMap, &reservedForReclaim, &numaAvailable, &nonBindingNumas, &tt.allowSharedCoresOverlapReclaimedCores, metaCache, metaServer, metrics.DummyMetrics{})
			result, err := common.AssembleProvision()
			require.NoErrorf(t, err, "failed to AssembleProvision: %s", err)
			require.NotNil(t, result, "invalid assembler result")
			t.Logf("%v", result)
			require.Equal(t, tt.expectPoolEntries, result.PoolEntries, "unexpected result")
			if len(tt.expectPoolOverlapInfo) > 0 {
				require.Equal(t, tt.expectPoolOverlapInfo, result.PoolOverlapInfo, "unexpected result")
			}
		})
	}
}

func generateTestConf(t *testing.T, enableReclaim bool) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	suffix := rand.String(10)
	stateFileDir := "stateFileDir." + suffix
	checkpointDir := "checkpointDir." + suffix

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.CPUShareConfiguration.RestrictRefPolicy = nil
	conf.CPUAdvisorConfiguration.ProvisionPolicies = map[types.QoSRegionType][]types.CPUProvisionPolicyName{
		types.QoSRegionTypeShare: {types.CPUProvisionPolicyCanonical},
	}
	conf.GetDynamicConfiguration().EnableReclaim = enableReclaim
	return conf
}
