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
						PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameShare,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
					testName: &AllocationInfo{
						PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameShare,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 2,
					},
				},
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
					testName: &AllocationInfo{
						PodUid:                   "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameReclaim,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
						RequestQuantity: 2,
					},
				},
				PoolNameReclaim: ContainerEntries{
					"": &AllocationInfo{
						PodUid:                   PoolNameReclaim,
						OwnerPoolName:            PoolNameReclaim,
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
				PoolNameShare: ContainerEntries{
					"": &AllocationInfo{
						PodUid:                   PoolNameShare,
						OwnerPoolName:            PoolNameShare,
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
								PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.NewCPUSet(1, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 9),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameReclaim,
								AllocationResult:         machine.MustParse("8"),
								OriginalAllocationResult: machine.MustParse("8"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(8),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
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
								PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.MustParse("3,11"),
								OriginalAllocationResult: machine.MustParse("3,11"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 11),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameReclaim,
								AllocationResult:         machine.MustParse("10"),
								OriginalAllocationResult: machine.MustParse("10"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(10),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
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
								PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"ec6e2f30-c78a-4bc4-9576-c916db5281a3": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameShare,
								AllocationResult:         machine.MustParse("4,12"),
								OriginalAllocationResult: machine.MustParse("4,12"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 12),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
								RequestQuantity: 2,
							},
						},
						"2432d068-c5a0-46ba-a7bd-b69d9bd16961": ContainerEntries{
							testName: &AllocationInfo{
								PodUid:                   "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameReclaim,
								AllocationResult:         machine.MustParse("5,13"),
								OriginalAllocationResult: machine.MustParse("5,13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(5, 13),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
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
								PodUid:                   "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            PoolNameReclaim,
								AllocationResult:         machine.MustParse("6,7,14,15"),
								OriginalAllocationResult: machine.MustParse("6,7,14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7, 14, 15),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
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

		machineState, err := GenerateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries, cpuconsts.CPUResourcePluginPolicyNameDynamic)
		as.Nil(err)

		as.Equalf(tc.expectedMachineState, machineState, "failed in test case: %s", tc.description)

		_ = os.RemoveAll(tmpDir)
	}
}

func TestGetSpecifiedPoolName(t *testing.T) {
	t.Parallel()

	type args struct {
		qosLevel               string
		cpusetEnhancementValue string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "shared_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelSharedCores,
			},
			want: PoolNameShare,
		},
		{
			name: "shared_cores with non-empty cpusetEnhancementValue",
			args: args{
				qosLevel:               consts.PodAnnotationQoSLevelSharedCores,
				cpusetEnhancementValue: "offline",
			},
			want: "offline",
		},
		{
			name: "dedicated_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
			},
			want: PoolNameDedicated,
		},
		{
			name: "reclaimed_cores with empty cpusetEnhancementValue",
			args: args{
				qosLevel: consts.PodAnnotationQoSLevelReclaimedCores,
			},
			want: PoolNameReclaim,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetSpecifiedPoolName(tt.args.qosLevel, tt.args.cpusetEnhancementValue); got != tt.want {
				t.Errorf("GetSpecifiedPoolName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountAllocationInfosToPoolsQuantityMap(t *testing.T) {
	t.Parallel()
	testName := "test"

	type args struct {
		allocationInfos  []*AllocationInfo
		poolsQuantityMap map[string]map[int]int
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
						PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            "share-NUMA3",
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  testName,
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						RampUp:         false,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "3",
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameShare,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
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
					FakedNUMAID: 4,
				},
			},

			wantErr: false,
		},
		{
			name: "count allocationInfos to pools quantity map with invalid hint",
			args: args{
				allocationInfos: []*AllocationInfo{
					{
						PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            "share-NUMA3",
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  testName,
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						RampUp:         false,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "2-3",
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameShare,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
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
						PodUid:                   "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            "share-NUMA3",
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812bbb",
						PodNamespace:   testName,
						PodName:        testName,
						ContainerName:  testName,
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						RampUp:         false,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:          "2-3",
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
						RequestQuantity: 1.1,
					},
					{
						PodUid:                   "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            PoolNameShare,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelSharedCores,
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
			if err := CountAllocationInfosToPoolsQuantityMap(tt.args.allocationInfos, tt.args.poolsQuantityMap, func(allocationInfo *AllocationInfo) float64 {
				return allocationInfo.RequestQuantity
			}); (err != nil) != tt.wantErr {
				t.Errorf("CountAllocationInfosToPoolsQuantityMap() error = %v, wantErr %v", err, tt.wantErr)
			} else if err == nil {
				if !reflect.DeepEqual(tt.args.poolsQuantityMap, tt.want) {
					t.Errorf("CountAllocationInfosToPoolsQuantityMap() = %v, want %v", tt.args.poolsQuantityMap, tt.want)
				}
			}
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
