//go:build linux
// +build linux

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

package state

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGenerateCPUMachineStateByPodEntries(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description          string
		podEntries           PodEntries
		expectedMachineState NUMANodeMap
		cpuTopology          *machine.CPUTopology
	}{
		{
			description: "only one pod entry",
			podEntries: PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
					testName: &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameReclaim,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("5-8,10,13-15"),
						OriginalAllocationResult: machine.MustParse("5-8,10,13-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							2: machine.NewCPUSet(5, 13),
							3: machine.NewCPUSet(6, 7, 14, 15),
						},
					},
				},
				commonstate.PoolNameShare: ContainerEntries{
					"": &AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
					},
				},
			},
			expectedMachineState: NUMANodeMap{
				0: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,

									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("8"),
								OriginalAllocationResult: machine.MustParse("8"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("10"),
								OriginalAllocationResult: machine.MustParse("10"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				2: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"373d08e4-7a6b-4293-aaaf-b135ff8123bf": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameShare,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("5,13"),
								OriginalAllocationResult: machine.MustParse("5,13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				3: &NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: PodEntries{
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameReclaim,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.MustParse("6,7,14,15"),
								OriginalAllocationResult: machine.MustParse("6,7,14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGenerateCPUMachineStateByPodEntries")
		as.Nil(err)

		machineState, err := GenerateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries, nil)
		as.Nil(err)

		as.Equalf(tc.expectedMachineState, machineState, "failed in test case: %s", tc.description)

		_ = os.RemoveAll(tmpDir)
	}
}

func TestGetUnitedPoolsCPUs(t *testing.T) {
	t.Parallel()

	makePoolEntry := func(poolName string, allocationResult machine.CPUSet) ContainerEntries {
		return ContainerEntries{
			commonstate.FakedContainerName: &AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(poolName),
				AllocationResult: allocationResult,
			},
		}
	}

	tests := []struct {
		name    string
		entries PodEntries
		filters []func(poolName string) bool
		want    machine.CPUSet
	}{
		{
			name: "union pools matched by any filter and ignore non-pool entries",
			entries: PodEntries{
				commonstate.PoolNameShare:             makePoolEntry(commonstate.PoolNameShare, machine.MustParse("1-2")),
				commonstate.PoolNameReclaim:           makePoolEntry(commonstate.PoolNameReclaim, machine.MustParse("3,5")),
				commonstate.PoolNameReserve:           makePoolEntry(commonstate.PoolNameReserve, machine.MustParse("6")),
				commonstate.PoolNameInterrupt:         makePoolEntry(commonstate.PoolNameInterrupt, machine.MustParse("7-9")),
				commonstate.GetSystemPoolName("test"): makePoolEntry(commonstate.GetSystemPoolName("test"), machine.MustParse("10-11")),
				"pod-uid": {
					"main": &AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod-uid",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameShare,
						},
						AllocationResult: machine.MustParse("9"),
					},
				},
			},
			filters: []func(poolName string) bool{
				IsForbiddenPool,
				commonstate.IsSystemPool,
			},
			want: machine.MustParse("7-11"),
		},
		{
			name: "skip nil pool entries and unmatched pools",
			entries: PodEntries{
				commonstate.PoolNameShare: {
					commonstate.FakedContainerName: nil,
				},
				commonstate.PoolNameReclaim: makePoolEntry(commonstate.PoolNameReclaim, machine.MustParse("3-4")),
			},
			filters: []func(poolName string) bool{
				func(poolName string) bool { return poolName == commonstate.PoolNameShare },
			},
			want: machine.NewCPUSet(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetUnitedPoolsCPUs(tt.entries, tt.filters...)
			require.Truef(t, got.Equals(tt.want), "GetUnitedPoolsCPUs() = %s, want %s", got.String(), tt.want.String())
		})
	}
}

func TestPodEntriesHasActiveRampUp(t *testing.T) {
	t.Parallel()

	makeAllocation := func(containerType pluginapi.ContainerType, rampUp bool) *AllocationInfo {
		return &AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "pod",
				ContainerName: containerType.String(),
				ContainerType: containerType.String(),
			},
			RampUp: rampUp,
		}
	}
	makePoolEntry := func(rampUp bool) ContainerEntries {
		return ContainerEntries{
			commonstate.FakedContainerName: &AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				RampUp:         rampUp,
			},
		}
	}

	tests := []struct {
		name    string
		entries PodEntries
		want    bool
	}{
		{name: "empty", entries: PodEntries{}, want: false},
		{
			name: "pool only is ignored",
			entries: PodEntries{
				commonstate.PoolNameShare: makePoolEntry(true),
			},
			want: false,
		},
		{
			name: "nil real allocation is ignored",
			entries: PodEntries{
				"pod": {"main": nil},
			},
			want: false,
		},
		{
			name: "real allocation not in ramp up",
			entries: PodEntries{
				"pod": {"main": makeAllocation(pluginapi.ContainerType_MAIN, false)},
			},
			want: false,
		},
		{
			name: "real main allocation in ramp up",
			entries: PodEntries{
				"pod": {"main": makeAllocation(pluginapi.ContainerType_MAIN, true)},
			},
			want: true,
		},
		{
			name: "sidecar activates while main is inactive",
			entries: PodEntries{
				"pod": {
					"main":    makeAllocation(pluginapi.ContainerType_MAIN, false),
					"sidecar": makeAllocation(pluginapi.ContainerType_SIDECAR, true),
				},
			},
			want: true,
		},
		{
			name: "main activates while sidecar is inactive",
			entries: PodEntries{
				"pod": {
					"main":    makeAllocation(pluginapi.ContainerType_MAIN, true),
					"sidecar": makeAllocation(pluginapi.ContainerType_SIDECAR, false),
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.entries.HasActiveRampUp())
			require.Equal(t, tt.want, tt.entries.Clone().HasActiveRampUp())
		})
	}
}

func TestCountAllocationInfosToPoolsQuantityMap(t *testing.T) {
	t.Parallel()
	testName := "test"

	type args struct {
		numaResourcePackagePinnedCPUSet map[int]map[string]machine.CPUSet
		allocationInfos                 []*AllocationInfo
		poolsQuantityMap                map[string]map[int]int
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[int]int
		wantErr bool
	}{
		// TODO: Add test cases.
		{
			name: "count allocationInfos to pools quantity map normally",
			args: args{
				allocationInfos: []*AllocationInfo{
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "3",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:          false,
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 3.1,
					},
				},
				poolsQuantityMap: map[string]map[int]int{},
			},
			want: map[string]map[int]int{
				"share-NUMA3": {
					3: 5,
				},
				"share": {
					commonstate.FakedNUMAID: 4,
				},
			},

			wantErr: false,
		},
		{
			name: "count allocationInfos to pools quantity map with resource package",
			args: args{
				numaResourcePackagePinnedCPUSet: map[int]map[string]machine.CPUSet{
					3: {
						"package-a": machine.NewCPUSet(6, 7, 14),
					},
				},
				allocationInfos: []*AllocationInfo{
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ccc",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationResourcePackageKey:           "package-a",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 1.1,
					},
				},
				poolsQuantityMap: map[string]map[int]int{},
			},
			want: map[string]map[int]int{
				"package-a/share-NUMA3": {
					3: 3,
				},
			},
			wantErr: false,
		},
		{
			name: "count allocationInfos to pools quantity map with invalid hint",
			args: args{
				allocationInfos: []*AllocationInfo{
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "2-3",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:          false,
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 3.1,
					},
				},
				poolsQuantityMap: map[string]map[int]int{},
			},
			wantErr: true,
		},
		{
			name: "count allocationInfos to pools quantity map with nil poolsQuantityMap",
			args: args{
				allocationInfos: []*AllocationInfo{
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA3",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "2-3",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:          false,
						RequestQuantity: 1.1,
					},
					{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameShare,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,3-4,9,11-12"),
						OriginalAllocationResult: machine.MustParse("1,3-4,9,11-12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 12),
						},
						RequestQuantity: 3.1,
					},
				},
				poolsQuantityMap: nil,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := CountAllocationInfosToPoolsQuantityMap(tt.args.numaResourcePackagePinnedCPUSet, tt.args.allocationInfos, tt.args.poolsQuantityMap, func(allocationInfo *AllocationInfo) float64 {
				return allocationInfo.RequestQuantity
			}, cpuconsts.CPUIncrRatioSharedCoresNUMABinding); (err != nil) != tt.wantErr {
				t.Errorf("CountAllocationInfosToPoolsQuantityMap() error = %v, wantErr %v", err, tt.wantErr)
			} else if err == nil {
				if !reflect.DeepEqual(tt.args.poolsQuantityMap, tt.want) {
					t.Errorf("CountAllocationInfosToPoolsQuantityMap() = %v, want %v", tt.args.poolsQuantityMap, tt.want)
				}
			}
		})
	}
}

func TestCountAllocationInfosToPoolsQuantityMapUsesProvidedSharedNUMABindingRatio(t *testing.T) {
	t.Parallel()

	newAllocation := func(podUID string, numaBinding bool) *AllocationInfo {
		annotations := map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		}
		ownerPoolName := commonstate.PoolNameShare
		allocationResult := machine.NewCPUSet(0)
		topologyAwareAssignments := map[int]machine.CPUSet{
			0: allocationResult.Clone(),
		}
		if numaBinding {
			annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
			ownerPoolName = "snb-pool"
		}

		return &AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podUID,
				ContainerName: "main",
				ContainerType: pluginapi.ContainerType_MAIN.String(),
				OwnerPoolName: ownerPoolName,
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: annotations,
				QoSLevel:    consts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult:         allocationResult,
			TopologyAwareAssignments: topologyAwareAssignments,
			RequestQuantity:          1,
		}
	}

	for _, tt := range []struct {
		name                          string
		allocationInfos               []*AllocationInfo
		sharedNUMABindingCPUIncrRatio float64
		want                          map[string]map[int]int
	}{
		{
			name: "SNB ratio one",
			allocationInfos: []*AllocationInfo{
				newAllocation("pod-0", true),
				newAllocation("pod-1", true),
				newAllocation("pod-2", true),
				newAllocation("pod-3", true),
			},
			sharedNUMABindingCPUIncrRatio: 1,
			want: map[string]map[int]int{
				"snb-pool": {0: 4},
			},
		},
		{
			name: "SNB ratio two",
			allocationInfos: []*AllocationInfo{
				newAllocation("pod-0", true),
				newAllocation("pod-1", true),
				newAllocation("pod-2", true),
				newAllocation("pod-3", true),
			},
			sharedNUMABindingCPUIncrRatio: 2,
			want: map[string]map[int]int{
				"snb-pool": {0: 8},
			},
		},
		{
			name: "non-binding shared ignores SNB ratio",
			allocationInfos: []*AllocationInfo{
				newAllocation("pod-0", false),
				newAllocation("pod-1", false),
			},
			sharedNUMABindingCPUIncrRatio: 2,
			want: map[string]map[int]int{
				commonstate.PoolNameShare: {commonstate.FakedNUMAID: 2},
			},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := make(map[string]map[int]int)
			err := CountAllocationInfosToPoolsQuantityMap(
				nil,
				tt.allocationInfos,
				got,
				func(allocationInfo *AllocationInfo) float64 {
					return allocationInfo.RequestQuantity
				},
				tt.sharedNUMABindingCPUIncrRatio,
			)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCPUPreciseCeil(t *testing.T) {
	t.Parallel()
	require.Equal(t, 188, CPUPreciseCeil(188.0000000001))
	require.Equal(t, 188, CPUPreciseCeil(187.9999999999))
	require.Equal(t, 189, CPUPreciseCeil(188.001))
	require.Equal(t, 188, CPUPreciseCeil(188.0001))
	array := []float64{4, 1, 4, 0.83, 1, 2, 4, 4, 0.83, 4, 4, 4, 1, 4, 48, 1, 0.507, 0.625, 2, 1, 5.54, 2, 4, 4, 2, 1, 0.01, 4, 1, 0.658, 2}
	sum := float64(0)
	for _, v := range array {
		sum += v
	}
	require.Equal(t, 118, CPUPreciseCeil(sum))
}
