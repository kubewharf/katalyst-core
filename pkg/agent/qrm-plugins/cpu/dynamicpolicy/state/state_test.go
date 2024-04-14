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
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	testutil "k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuPluginStateFileName = "cpu_plugin_state"
	policyName             = "dynamic"
)

// assertStateEqual marks provided test as failed if provided states differ
func assertStateEqual(t *testing.T, restoredState, expectedState State) {
	as := require.New(t)

	expectedMachineState := expectedState.GetMachineState()
	restoredMachineState := restoredState.GetMachineState()
	as.Equalf(expectedMachineState, restoredMachineState, "machineState mismatches")

	expectedPodEntries := expectedState.GetPodEntries()
	restoredPodEntries := restoredState.GetPodEntries()
	as.Equalf(expectedPodEntries, restoredPodEntries, "podEntries mismatch")
}

func TestNewCheckpointState(t *testing.T) {
	t.Parallel()

	testName := "test"
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)

	testCases := []struct {
		description       string
		checkpointContent string
		expectedError     string
		expectedState     *cpuPluginState
	}{
		{
			"Restore non-existing checkpoint",
			"",
			"",
			&cpuPluginState{
				podEntries:     make(PodEntries),
				machineState:   GetDefaultMachineState(cpuTopology),
				socketTopology: cpuTopology.GetSocketTopology(),
				cpuTopology:    cpuTopology,
			},
		},
		{
			"Restore valid checkpoint",
			`{
	"policyName": "dynamic",
	"machineState": {
		"0": {
			"default_cpuset": "0-1,8-9",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "8",
						"original_allocation_result": "8",
						"topology_aware_assignments": {
							"0": "8"
						},
						"original_topology_aware_assignments": {
							"0": "8"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "1,9",
						"original_allocation_result": "1,9",
						"topology_aware_assignments": {
							"0": "1,9"
						},
						"original_topology_aware_assignments": {
							"0": "1,9"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "1,9",
						"original_allocation_result": "1,9",
						"topology_aware_assignments": {
							"0": "1,9"
						},
						"original_topology_aware_assignments": {
							"0": "1,9"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"1": {
			"default_cpuset": "2-3,10-11",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "10",
						"original_allocation_result": "10",
						"topology_aware_assignments": {
							"1": "10"
						},
						"original_topology_aware_assignments": {
							"1": "10"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "3,11",
						"original_allocation_result": "3,11",
						"topology_aware_assignments": {
							"1": "3,11"
						},
						"original_topology_aware_assignments": {
							"1": "3,11"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "3,11",
						"original_allocation_result": "3,11",
						"topology_aware_assignments": {
							"1": "3,11"
						},
						"original_topology_aware_assignments": {
							"1": "3,11"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"2": {
			"default_cpuset": "4-5,12-13",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "5,13",
						"original_allocation_result": "5,13",
						"topology_aware_assignments": {
							"2": "5,13"
						},
						"original_topology_aware_assignments": {
							"2": "5,13"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "4,12",
						"original_allocation_result": "4,12",
						"topology_aware_assignments": {
							"2": "4,12"
						},
						"original_topology_aware_assignments": {
							"2": "4,12"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "4,12",
						"original_allocation_result": "4,12",
						"topology_aware_assignments": {
							"2": "4,12"
						},
						"original_topology_aware_assignments": {
							"2": "4,12"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"3": {
			"default_cpuset": "6-7,14-15",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "6-7,14-15",
						"original_allocation_result": "6-7,14-15",
						"topology_aware_assignments": {
							"3": "6-7,14-15"
						},
						"original_topology_aware_assignments": {
							"3": "6-7,14-15"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				}
			}
		}
	},
	"pod_entries": {
		"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
			"test": {
				"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "reclaim",
				"allocation_result": "5-8,10,13-15",
				"original_allocation_result": "5-8,10,13-15",
				"topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"original_topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
				},
				"qosLevel": "reclaimed_cores",
				"request_quantity": 2
			}
		},
		"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
			"test": {
				"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"qosLevel": "shared_cores",
				"request_quantity": 2
			}
		},
		"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
			"test": {
				"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"qosLevel": "shared_cores",
				"request_quantity": 2
			}
		},
		"reclaim": {
			"": {
				"pod_uid": "reclaim",
				"owner_pool_name": "reclaim",
				"allocation_result": "5-8,10,13-15",
				"original_allocation_result": "5-8,10,13-15",
				"topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"original_topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"init_timestamp": "",
				"labels": null,
				"annotations": null,
				"qosLevel": ""
			}
		},
		"share": {
			"": {
				"pod_uid": "share",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": null,
				"annotations": null,
				"qosLevel": ""
			}
		}
	},
	"checksum": 1743112210
}`,
			"",
			&cpuPluginState{
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
				machineState: NUMANodeMap{
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
			},
		},
		{
			"Restore checkpoint with invalid checksum",
			`{
	"policyName": "dynamic",
	"machineState": {
		"0": {
			"default_cpuset": "0-1,8-9",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "8",
						"original_allocation_result": "8",
						"topology_aware_assignments": {
							"0": "8"
						},
						"original_topology_aware_assignments": {
							"0": "8"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "1,9",
						"original_allocation_result": "1,9",
						"topology_aware_assignments": {
							"0": "1,9"
						},
						"original_topology_aware_assignments": {
							"0": "1,9"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "1,9",
						"original_allocation_result": "1,9",
						"topology_aware_assignments": {
							"0": "1,9"
						},
						"original_topology_aware_assignments": {
							"0": "1,9"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"1": {
			"default_cpuset": "2-3,10-11",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "10",
						"original_allocation_result": "10",
						"topology_aware_assignments": {
							"1": "10"
						},
						"original_topology_aware_assignments": {
							"1": "10"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "3,11",
						"original_allocation_result": "3,11",
						"topology_aware_assignments": {
							"1": "3,11"
						},
						"original_topology_aware_assignments": {
							"1": "3,11"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "3,11",
						"original_allocation_result": "3,11",
						"topology_aware_assignments": {
							"1": "3,11"
						},
						"original_topology_aware_assignments": {
							"1": "3,11"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"2": {
			"default_cpuset": "4-5,12-13",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "5,13",
						"original_allocation_result": "5,13",
						"topology_aware_assignments": {
							"2": "5,13"
						},
						"original_topology_aware_assignments": {
							"2": "5,13"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				},
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
					"test": {
						"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "4,12",
						"original_allocation_result": "4,12",
						"topology_aware_assignments": {
							"2": "4,12"
						},
						"original_topology_aware_assignments": {
							"2": "4,12"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				},
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
					"test": {
						"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "share",
						"allocation_result": "4,12",
						"original_allocation_result": "4,12",
						"topology_aware_assignments": {
							"2": "4,12"
						},
						"original_topology_aware_assignments": {
							"2": "4,12"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "shared_cores"
						},
						"qosLevel": "shared_cores",
						"request_quantity": 2
					}
				}
			}
		},
		"3": {
			"default_cpuset": "6-7,14-15",
			"allocated_cpuset": "",
			"pod_entries": {
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
					"test": {
						"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
						"pod_namespace": "test",
						"pod_name": "test",
						"container_name": "test",
						"container_type": "MAIN",
						"owner_pool_name": "reclaim",
						"allocation_result": "6-7,14-15",
						"original_allocation_result": "6-7,14-15",
						"topology_aware_assignments": {
							"3": "6-7,14-15"
						},
						"original_topology_aware_assignments": {
							"3": "6-7,14-15"
						},
						"init_timestamp": "",
						"labels": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"annotations": {
							"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
						},
						"qosLevel": "reclaimed_cores",
						"request_quantity": 2
					}
				}
			}
		}
	},
	"pod_entries": {
		"2432d068-c5a0-46ba-a7bd-b69d9bd16961": {
			"test": {
				"pod_uid": "2432d068-c5a0-46ba-a7bd-b69d9bd16961",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "reclaim",
				"allocation_result": "5-8,10,13-15",
				"original_allocation_result": "5-8,10,13-15",
				"topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"original_topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "reclaimed_cores"
				},
				"qosLevel": "reclaimed_cores",
				"request_quantity": 2
			}
		},
		"373d08e4-7a6b-4293-aaaf-b135ff8123bf": {
			"test": {
				"pod_uid": "373d08e4-7a6b-4293-aaaf-b135ff8123bf",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"qosLevel": "shared_cores",
				"request_quantity": 2
			}
		},
		"ec6e2f30-c78a-4bc4-9576-c916db5281a3": {
			"test": {
				"pod_uid": "ec6e2f30-c78a-4bc4-9576-c916db5281a3",
				"pod_namespace": "test",
				"pod_name": "test",
				"container_name": "test",
				"container_type": "MAIN",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"annotations": {
					"katalyst.kubewharf.io/qos_level": "shared_cores"
				},
				"qosLevel": "shared_cores",
				"request_quantity": 2
			}
		},
		"reclaim": {
			"": {
				"pod_uid": "reclaim",
				"owner_pool_name": "reclaim",
				"allocation_result": "5-8,10,13-15",
				"original_allocation_result": "5-8,10,13-15",
				"topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"original_topology_aware_assignments": {
					"0": "8",
					"1": "10",
					"2": "5,13",
					"3": "6-7,14-15"
				},
				"init_timestamp": "",
				"labels": null,
				"annotations": null,
				"qosLevel": ""
			}
		},
		"share": {
			"": {
				"pod_uid": "share",
				"owner_pool_name": "share",
				"allocation_result": "1,3-4,9,11-12",
				"original_allocation_result": "1,3-4,9,11-12",
				"topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"original_topology_aware_assignments": {
					"0": "1,9",
					"1": "3,11",
					"2": "4,12"
				},
				"init_timestamp": "",
				"labels": null,
				"annotations": null,
				"qosLevel": ""
			}
		}
	},
	"checksum": 2840585175
}`,
			"checkpoint is corrupted",
			&cpuPluginState{},
		},
		{
			"Restore checkpoint with invalid JSON",
			`{`,
			"unexpected end of JSON input",
			&cpuPluginState{},
		},
	}

	// create temp dir
	testingDir, err := ioutil.TempDir("", "TestNewCheckpointState")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)

	// create checkpoint manager for testing
	cpm, err := checkpointmanager.NewCheckpointManager(testingDir)
	assert.NoError(t, err, "could not create testing checkpoint manager")

	for _, tc := range testCases {
		// ensure there is no previous checkpoint
		require.NoError(t, cpm.RemoveCheckpoint(cpuPluginStateFileName), "could not remove testing checkpoint")

		// prepare checkpoint for testing
		if strings.TrimSpace(tc.checkpointContent) != "" {
			checkpoint := &testutil.MockCheckpoint{Content: tc.checkpointContent}
			require.NoError(t, cpm.CreateCheckpoint(cpuPluginStateFileName, checkpoint), "could not create testing checkpoint")
		}

		restoredState, err := NewCheckpointState(testingDir, cpuPluginStateFileName, policyName, cpuTopology, false)
		if strings.TrimSpace(tc.expectedError) != "" {
			require.Error(t, err)
			require.Contains(t, err.Error(), "could not restore state from checkpoint:")
			require.Contains(t, err.Error(), tc.expectedError)

			// test skip corruption
			if strings.Contains(err.Error(), "checkpoint is corrupted") {
				_, err = NewCheckpointState(testingDir, cpuPluginStateFileName, policyName, cpuTopology, true)
				require.Nil(t, err)
			}
		} else {
			require.NoError(t, err, "unexpected error while creating checkpointState")
			// compare state after restoration with the one expected
			assertStateEqual(t, restoredState, tc.expectedState)
		}
	}
}

func TestClearState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testName := "test"
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description  string
		cpuTopology  *machine.CPUTopology
		podEntries   PodEntries
		machineState NUMANodeMap
	}{
		{
			description: "valid state cleaning",
			cpuTopology: cpuTopology,
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
			machineState: NUMANodeMap{
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
		},
	}

	for i, tc := range testCases {
		i, tc := i, tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			// create temp dir
			testingDir, err := ioutil.TempDir("", fmt.Sprintf("dynamic_policy_state_test_%d", i))
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(testingDir)

			state1, err := NewCheckpointState(testingDir, cpuPluginStateFileName, policyName, tc.cpuTopology, false)
			as.Nil(err)

			state1.ClearState()

			state1.SetMachineState(tc.machineState)
			state1.SetPodEntries(tc.podEntries)

			state2, err := NewCheckpointState(testingDir, cpuPluginStateFileName, policyName, tc.cpuTopology, false)
			as.Nil(err)
			assertStateEqual(t, state2, state1)
		})
	}
}

func TestCheckpointStateHelpers(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	testName := "test"

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description  string
		cpuTopology  *machine.CPUTopology
		podEntries   PodEntries
		machineState NUMANodeMap
	}{
		{
			description: "valid state cleaning",
			cpuTopology: cpuTopology,
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
			machineState: NUMANodeMap{
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
		},
	}

	// create temp dir
	testingDir, err := ioutil.TempDir("", "dynamic_policy_state_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			state, err := NewCheckpointState(testingDir, cpuPluginStateFileName, policyName, tc.cpuTopology, false)
			as.Nil(err)

			state.ClearState()

			state.SetMachineState(tc.machineState)
			as.Equalf(tc.machineState, state.GetMachineState(), "failed in test case: %s", tc.description)

			state.SetPodEntries(tc.podEntries)
			as.Equalf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)

			state.ClearState()

			as.NotEqualf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)
			for podUID, containerEntries := range tc.podEntries {
				for containerName, allocationInfo := range containerEntries {
					state.SetAllocationInfo(podUID, containerName, allocationInfo)
				}
			}
			as.Equalf(tc.podEntries, state.GetPodEntries(), "failed in test case: %s", tc.description)
		})
	}
}

func TestGetDefaultMachineState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description          string
		cpuTopology          *machine.CPUTopology
		expectedMachineState NUMANodeMap
	}{
		{
			description: "nil cpuTopology",
		},
		{
			description: "non-nil cpuTopology",
			cpuTopology: cpuTopology,
			expectedMachineState: NUMANodeMap{
				0: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(0, 1, 8, 9),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				1: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(2, 3, 10, 11),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				2: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(4, 5, 12, 13),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
				3: &NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(6, 7, 14, 15),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries:      make(PodEntries),
				},
			},
		},
	}

	for _, tc := range testCases {
		actualMachineState := GetDefaultMachineState(tc.cpuTopology)
		as.Equalf(actualMachineState, tc.expectedMachineState, "failed in test case: %s", tc.description)
	}
}

func TestGetSocketTopology(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testCases := []struct {
		description            string
		cpuTopology            *machine.CPUTopology
		expectedSocketTopology map[int]string
	}{
		{
			description: "nil cpuTopology",
		},
		{
			description: "non-nil cpuTopology",
			cpuTopology: cpuTopology,
			expectedSocketTopology: map[int]string{
				0: "0-1",
				1: "2-3",
			},
		},
	}

	for _, tc := range testCases {
		actualSocketToplogy := tc.cpuTopology.GetSocketTopology()
		as.Equalf(tc.expectedSocketTopology, actualSocketToplogy, "failed in test case: %s", tc.description)
	}
}
