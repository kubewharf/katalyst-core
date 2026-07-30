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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type FakeRegion struct {
	name                       string
	ownerPoolName              string
	regionType                 configapi.QoSRegionType
	bindingNumas               machine.CPUSet
	isNumaBinding              bool
	isNumaExclusive            bool
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

type fakeMetaReader struct {
	metacache.MetaReader
	featureGates map[string]*advisorsvc.FeatureGate
}

func (f fakeMetaReader) GetSupportedWantedFeatureGates() (map[string]*advisorsvc.FeatureGate, error) {
	return f.featureGates, nil
}

func NewFakeRegion(name string, regionType configapi.QoSRegionType, ownerPoolName string) *FakeRegion {
	return &FakeRegion{
		name:          name,
		regionType:    regionType,
		ownerPoolName: ownerPoolName,
	}
}

func (fake *FakeRegion) Name() string {
	return fake.name
}

func (fake *FakeRegion) Type() configapi.QoSRegionType {
	return fake.regionType
}

func (fake *FakeRegion) GetMetaInfo() string {
	return "fake"
}

func (fake *FakeRegion) OwnerPoolName() string {
	return fake.ownerPoolName
}

func (fake *FakeRegion) GetResourcePackageName() string {
	_, pkgName := resourcepackage.UnwrapOwnerPoolName(fake.ownerPoolName)
	return pkgName
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

func (fake *FakeRegion) GetPodsRequest() float64 {
	return 0
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
func (fake *FakeRegion) IsNumaExclusive() bool                      { return fake.isNumaExclusive }
func (fake *FakeRegion) SetThrottled(throttled bool)                { fake.throttled = throttled }
func (fake *FakeRegion) EnableReclaim() bool                        { return true }
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
	poolType      configapi.QoSRegionType
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
		"share-a": {
			PoolName: "share",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
		},
		"share-b": {
			PoolName: "share",
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10),
			},
		},
	}
	tests := []struct {
		name                                  string
		enableReclaimed                       bool
		allowSharedCoresOverlapReclaimedCores bool
		disableReclaimSelector                string
		resourcePackageConfig                 types.ResourcePackageConfig
		poolInfos                             []testCasePoolConfig
		wantErr                               bool
		expectPoolEntries                     map[string]map[int]types.CPUResource
		expectPoolOverlapInfo                 map[string]map[int]map[string]int
	}{
		{
			name:                                  "test-disable-reclaim-pkg-complex",
			enableReclaimed:                       true,
			disableReclaimSelector:                "disable-reclaim=true",
			allowSharedCoresOverlapReclaimedCores: true,
			resourcePackageConfig: types.ResourcePackageConfig{
				0: map[string]*types.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(1, 2, 3), // size 3
					},
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(4, 5), // size 2
					},
				},
				1: map[string]*types.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(1, 2, 3, 4, 5), // size 5
					},
				},
			},
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share", // ownerPoolName is share, pkg is empty
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1", // ownerPoolName is share-NUMA1, pkg is NUMA1
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 19, Quota: -1}, // allow expand to full size
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 11, Quota: -1}, // NUMA1 total 24, isolation 8, share req 8. allow expand but max is 24-8=16?
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					// NUMA 0: available 24, isolated 0, unused non-reclaimable: pkg1(size 3) - allocated 0 = 3
					// overlapReclaim pool calculation: shareReclaimCoresSize = 24 - 0 - 0 - 6 - 0 - 3 = 15
					// reclaimedCoresSize = 15 + 0 = 15
					// overlapSharePoolSizes = 24, overlapReclaimSize = 15
					-1: types.CPUResource{Size: 2, Quota: -1},
					// NUMA 1: available 24, isolated 8, unused non-reclaimable: pkg1(size 5) - allocated 0 = 5
					// shareReclaimCoresSize = 24 - 8 - 0 - 8 - 0 - 5 = 3
					// reclaimedCoresSize = 3 (but reservedForReclaim is 4, so it should be regulated to 4)
					// if regulated to 4, then overlapReclaimSize is 4
					// nonOverlap is 4-4=0
					1: types.CPUResource{Size: 0, Quota: -1},
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {
					-1: map[string]int{"share": 13}, // total unused non-reclaimable is 3. share size is 24, req is 6, max reclaim is 15. overlap is 15.
					1:  map[string]int{"share-NUMA1": 4},
				},
			},
		},
		{
			name:                   "test-disable-reclaim-pkg",
			enableReclaimed:        true,
			disableReclaimSelector: "disable-reclaim=true",
			resourcePackageConfig: types.ResourcePackageConfig{
				0: map[string]*types.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(1, 2, 3), // size 3
					},
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(4, 5), // size 2
					},
				},
			},
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 15, Quota: -1}, // Originally 18, but we deducted 3 unused non-reclaimable
					1:  types.CPUResource{Size: 16, Quota: -1},
				},
			},
		},
		{
			name:            "test1",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 18, Quota: -1},
					1:  types.CPUResource{Size: 16, Quota: -1},
				},
			},
		},
		{
			name:            "test2",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 20, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 20, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 4, Quota: -1},
					1:  types.CPUResource{Size: 4, Quota: -1},
				},
			},
		},
		{
			name:            "test3",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 18, Quota: -1},
					1:  types.CPUResource{Size: 8, Quota: -1},
				},
			},
		},
		{
			name:            "test4",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 20, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 12, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 4, Quota: -1},
					1:  types.CPUResource{Size: 4, Quota: -1},
				},
			},
		},
		{
			name:            "test5",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 15},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 20, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 16, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 4, Quota: -1},
					1:  types.CPUResource{Size: 4, Quota: -1},
				},
			},
		},
		{
			name:            "test6",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 15},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 15, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 18, Quota: -1},
					1:  types.CPUResource{Size: 5, Quota: -1},
				},
			},
		},
		{
			name:            "test7",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 18, Quota: -1},
					1:  types.CPUResource{Size: 4, Quota: -1},
				},
			},
		},
		{
			name:            "test8",
			enableReclaimed: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 6, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 18, Quota: -1},
					1:  types.CPUResource{Size: 8, Quota: -1},
				},
			},
		},
		{
			name:            "test9",
			enableReclaimed: false,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 20, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 12, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 4, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 4, Quota: -1},
					1:  types.CPUResource{Size: 4, Quota: -1},
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
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 24, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 0, Quota: -1},
					1:  types.CPUResource{Size: 0, Quota: -1},
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {-1: map[string]int{"share": 18}, 1: map[string]int{"share-NUMA1": 4}},
			},
		},
		{
			name:                                  "no share pool and isolated pool, allow shared_cores overlap reclaimed_cores",
			enableReclaimed:                       true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos:                             []testCasePoolConfig{},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 48, Quota: -1},
				},
			},
		},
		{
			name:                                  "share and isolated pool not throttled, overlap reclaimed cores, reclaim disabled",
			enableReclaimed:                       false,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-NUMA1",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share": {
					-1: types.CPUResource{Size: 24, Quota: -1},
				},
				"share-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 0, Quota: -1},
					1:  types.CPUResource{Size: 0, Quota: -1},
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {-1: map[string]int{"share": 4}, 1: map[string]int{"share-NUMA1": 4}},
			},
		},
		{
			name:                                  "isolated pools only, with numa binding",
			enableReclaimed:                       true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "isolation-NUMA1",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
				{
					poolName:      "isolation-NUMA1-pod2",
					poolType:      configapi.QoSRegionTypeIsolation,
					numa:          machine.NewCPUSet(1),
					isNumaBinding: true,
					provision: types.ControlKnob{
						configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: 8},
						configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: 4},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"isolation-NUMA1": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"isolation-NUMA1-pod2": {
					1: types.CPUResource{Size: 8, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					1:  types.CPUResource{Size: 8, Quota: -1},
					-1: types.CPUResource{Size: 24, Quota: -1},
				},
			},
		},
		{
			name:                                  "share and bach pool non binding NUMAs, overlap reclaimed cores",
			enableReclaimed:                       true,
			allowSharedCoresOverlapReclaimedCores: true,
			poolInfos: []testCasePoolConfig{
				{
					poolName:      "share-a",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0, 1),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 6},
					},
				},
				{
					poolName:      "share-b",
					poolType:      configapi.QoSRegionTypeShare,
					numa:          machine.NewCPUSet(0, 1),
					isNumaBinding: false,
					provision: types.ControlKnob{
						configapi.ControlKnobNonReclaimedCPURequirement: {Value: 8},
					},
				},
			},
			expectPoolEntries: map[string]map[int]types.CPUResource{
				"share-a": {
					-1: types.CPUResource{Size: 24, Quota: -1},
				},
				"share-b": {
					-1: types.CPUResource{Size: 24, Quota: -1},
				},
				"reserve": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
				"reclaim": {
					-1: types.CPUResource{Size: 0, Quota: -1},
				},
			},
			expectPoolOverlapInfo: map[string]map[int]map[string]int{
				"reclaim": {-1: map[string]int{"share-a": 18, "share-b": 16}},
			},
		},
		{
			name:                   "test with invalid disable-reclaim selector",
			disableReclaimSelector: "disable-reclaim=true,,invalid",
			wantErr:                true,
		},
	}

	reservedForReclaim := map[int]int{
		0: 4,
		1: 4,
	}

	numaAvailable := map[int]int{
		0: 24,
		1: 24,
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConf(t, tt.enableReclaimed, tt.disableReclaimSelector)

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
			if tt.resourcePackageConfig != nil {
				require.NoError(t, metaCache.SetResourcePackageConfig(tt.resourcePackageConfig))
			} else {
				require.NoError(t, metaCache.SetResourcePackageConfig(types.ResourcePackageConfig{0: map[string]*types.ResourcePackageState{}}))
			}

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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
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

func generateTestConf(t *testing.T, enableReclaim bool, disableReclaimSelector string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	suffix := rand.String(10)
	stateFileDir := "stateFileDir." + suffix
	checkpointDir := "checkpointDir." + suffix

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.RestrictRefPolicy = nil
	conf.CPUAdvisorConfiguration.ProvisionPolicies = map[configapi.QoSRegionType][]types.CPUProvisionPolicyName{
		configapi.QoSRegionTypeShare: {types.CPUProvisionPolicyCanonical},
	}
	conf.GetDynamicConfiguration().EnableReclaim = enableReclaim
	if disableReclaimSelector != "" {
		conf.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = disableReclaimSelector
	}
	return conf
}

func TestClampReclaimOverlapMetadata(t *testing.T) {
	t.Parallel()

	result := &types.InternalCPUCalculationResult{
		PoolOverlapInfo: map[string]map[int]map[string]int{
			commonstate.PoolNameReclaim: {
				0: {
					"share-a": 5,
					"share-b": 3,
				},
			},
		},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
			commonstate.PoolNameReclaim: {
				0: {
					"pod-a": {
						"main": 4,
					},
				},
			},
		},
	}

	got := clampReclaimOverlapMetadata(result, 0, 3)

	require.Equal(t, 3, got)
	require.Equal(t, map[string]int{"share-a": 3}, result.PoolOverlapInfo[commonstate.PoolNameReclaim][0])
	require.Empty(t, result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim][0])
}

func TestClampReclaimOverlapMetadataClearsZeroBudget(t *testing.T) {
	t.Parallel()

	result := &types.InternalCPUCalculationResult{
		PoolOverlapInfo: map[string]map[int]map[string]int{
			commonstate.PoolNameReclaim: {
				0: {"share": 2},
			},
		},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
			commonstate.PoolNameReclaim: {
				0: {"pod": {"main": 1}},
			},
		},
	}

	got := clampReclaimOverlapMetadata(result, 0, 0)

	require.Zero(t, got)
	require.Empty(t, result.PoolOverlapInfo[commonstate.PoolNameReclaim][0])
	require.Empty(t, result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim][0])
}

func TestCalculateReclaimPoolDisableDedicatedCoresOverlapReclaimedCores(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		calculate func(*ProvisionAssemblerCommon, *reclaimPoolCalculationData, *types.InternalCPUCalculationResult) (int, int, float64, error)
		data      *reclaimPoolCalculationData
		wantSize  int
	}{
		{
			name:      "overlap reclaim pool keeps shared capacity only",
			calculate: (*ProvisionAssemblerCommon).calculateOverlapReclaimPool,
			data: &reclaimPoolCalculationData{
				shareInfo: regionInfo{
					requirements:  map[string]int{"share": 2},
					requests:      map[string]int{"share": 4},
					reclaimEnable: map[string]bool{"share": true},
				},
				dedicatedInfo: regionInfo{
					requirements:  map[string]int{"dedicated": 2},
					requests:      map[string]int{"dedicated": 4},
					reclaimEnable: map[string]bool{"dedicated": true},
					podSet:        map[string]types.PodSet{"dedicated": {"pod": {"container": {}}}},
				},
				shareAndIsolateDedicatedPoolSizes:      map[string]int{"share": 4, "dedicated": 4},
				dedicatedPoolSizes:                     map[string]int{"dedicated": 4},
				dedicatedReclaimCoresSize:              2,
				shareAndIsolatedDedicatedPoolAvailable: 8,
				nodeEnableReclaim:                      true,
			},
			wantSize: 2,
		},
		{
			name:      "non-overlap reclaim pool excludes dedicated capacity",
			calculate: (*ProvisionAssemblerCommon).calculateNonOverlapReclaimPool,
			data: &reclaimPoolCalculationData{
				dedicatedInfo: regionInfo{
					requirements:  map[string]int{"dedicated": 2},
					requests:      map[string]int{"dedicated": 4},
					reclaimEnable: map[string]bool{"dedicated": true},
					podSet:        map[string]types.PodSet{"dedicated": {"pod": {"container": {}}}},
				},
				dedicatedReclaimCoresSize: 2,
				nodeEnableReclaim:         true,
			},
			wantSize: 0,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConf(t, true, "")
			conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true
			pa := &ProvisionAssemblerCommon{conf: conf, metaReader: fakeMetaReader{}}
			result := &types.InternalCPUCalculationResult{
				PoolOverlapInfo:             map[string]map[int]map[string]int{},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
			}

			reclaimedCoresSize, _, _, err := tt.calculate(pa, tt.data, result)

			require.NoError(t, err)
			require.Equal(t, tt.wantSize, reclaimedCoresSize)
			require.Empty(t, result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim][0])
		})
	}
}

func TestAssembleProvisionDisableDedicatedNUMAExclusiveOverlapRejectsReservedReclaim(t *testing.T) {
	conf := generateTestConf(t, true, "")
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true

	dedicatedRegion := NewFakeRegion("dedicated-numa-exclusive", configapi.QoSRegionTypeDedicated, "dedicated-numa-exclusive")
	dedicatedRegion.bindingNumas = machine.NewCPUSet(0)
	dedicatedRegion.isNumaBinding = true
	dedicatedRegion.isNumaExclusive = true

	regionMap := map[string]region.QoSRegion{dedicatedRegion.Name(): dedicatedRegion}
	reservedForReclaim := map[int]int{0: 1}
	numaAvailable := map[int]int{0: 8}
	nonBindingNumas := machine.NewCPUSet()
	allowSharedCoresOverlapReclaimedCores := true
	pa := NewProvisionAssemblerCommon(
		conf, nil, &regionMap, &reservedForReclaim, &numaAvailable, &nonBindingNumas,
		&allowSharedCoresOverlapReclaimedCores, metacache.NewDummyMetaCacheImp(), nil, metrics.DummyMetrics{},
	)

	_, err := pa.AssembleProvision()

	require.Error(t, err)
	require.ErrorContains(t, err, "NUMA 0")
	require.ErrorContains(t, err, "reservedForReclaim 1")
	require.ErrorContains(t, err, "dedicated NUMA exclusive")
}

func TestClampByReclaimedCPUMaxRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		size               int
		limit              float64
		ratio              float64
		cpuCount           int
		reservedForReclaim int
		wantSize           int
		wantLimit          float64
	}{
		{
			name:               "ratio disabled",
			size:               40,
			limit:              30,
			ratio:              0,
			cpuCount:           96,
			reservedForReclaim: 30,
			wantSize:           40,
			wantLimit:          30,
		},
		{
			name:               "floor fractional cap to even",
			size:               40,
			limit:              40,
			ratio:              0.3,
			cpuCount:           24,
			reservedForReclaim: 2,
			wantSize:           6,
			wantLimit:          6,
		},
		{
			name:               "size below cap is unchanged",
			size:               8,
			limit:              8,
			ratio:              0.5,
			cpuCount:           24,
			reservedForReclaim: 2,
			wantSize:           8,
			wantLimit:          8,
		},
		{
			name:               "negative limit sentinel is unchanged",
			size:               20,
			limit:              -1,
			ratio:              0.5,
			cpuCount:           24,
			reservedForReclaim: 2,
			wantSize:           12,
			wantLimit:          -1,
		},
		{
			name:               "cap equal to reserved is allowed",
			size:               8,
			limit:              8,
			ratio:              0.25,
			cpuCount:           8,
			reservedForReclaim: 2,
			wantSize:           2,
			wantLimit:          2,
		},
		{
			name:               "cap below reserved returns reserved",
			size:               8,
			limit:              8,
			ratio:              0.1,
			cpuCount:           8,
			reservedForReclaim: 3,
			wantSize:           3,
			wantLimit:          3,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSize, gotLimit := clampByReclaimedCPUMaxRatio(
				tt.size,
				tt.limit,
				tt.ratio,
				tt.cpuCount,
				tt.reservedForReclaim,
			)
			require.Equal(t, tt.wantSize, gotSize)
			require.Equal(t, tt.wantLimit, gotLimit)
		})
	}
}

func TestAssembleProvisionUsesReservedWhenEvenRatioCapIsLower(t *testing.T) {
	t.Parallel()

	newAssembler := func(t *testing.T, regionMap map[string]region.QoSRegion, nonBindingNUMAs machine.CPUSet) *ProvisionAssemblerCommon {
		t.Helper()

		conf, err := options.NewOptions().Config()
		require.NoError(t, err)
		conf.GetDynamicConfiguration().ReclaimedCPUMaxRatio = 0.1

		cpuDetails := machine.CPUDetails{}
		for cpuID := 0; cpuID < 8; cpuID++ {
			cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: 0}
		}
		metaServer := &metaserver.MetaServer{
			MetaAgent: &metaagent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						NumCPUs:      8,
						NumCores:     8,
						NumSockets:   1,
						NumNUMANodes: 1,
						CPUDetails:   cpuDetails,
					},
				},
			},
		}

		metaReader := metacache.NewDummyMetaCacheImp()

		reservedForReclaim := map[int]int{0: 2}
		numaAvailable := map[int]int{0: 8}
		allowOverlap := true
		return NewProvisionAssemblerCommon(
			conf,
			nil,
			&regionMap,
			&reservedForReclaim,
			&numaAvailable,
			&nonBindingNUMAs,
			&allowOverlap,
			metaReader,
			metaServer,
			metrics.DummyMetrics{},
		).(*ProvisionAssemblerCommon)
	}

	t.Run("ordinary NUMA pool", func(t *testing.T) {
		t.Parallel()

		pa := newAssembler(t, map[string]region.QoSRegion{}, machine.NewCPUSet(0))
		result, err := pa.AssembleProvision()
		require.NoError(t, err)
		require.Equal(t, 2, result.PoolEntries[commonstate.PoolNameReclaim][commonstate.FakedNUMAID].Size)
	})

	t.Run("dedicated NUMA-exclusive region", func(t *testing.T) {
		t.Parallel()

		dedicatedRegion := NewFakeRegion("dedicated-exclusive", configapi.QoSRegionTypeDedicated, "dedicated-exclusive")
		dedicatedRegion.SetBindingNumas(machine.NewCPUSet(0))
		dedicatedRegion.SetIsNumaBinding(true)
		dedicatedRegion.isNumaExclusive = true
		dedicatedRegion.SetPods(types.PodSet{"pod": {"container": {}}})
		regionMap := map[string]region.QoSRegion{dedicatedRegion.Name(): dedicatedRegion}

		pa := newAssembler(t, regionMap, machine.NewCPUSet())
		result := &types.InternalCPUCalculationResult{
			PoolEntries:                 map[string]map[int]types.CPUResource{},
			PoolOverlapInfo:             map[string]map[int]map[string]int{},
			PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
		}
		err := pa.assembleDedicatedNUMAExclusiveRegion(dedicatedRegion, result)
		require.NoError(t, err)
		require.Equal(t, 2, result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim][0]["pod"]["container"])
	})
}
