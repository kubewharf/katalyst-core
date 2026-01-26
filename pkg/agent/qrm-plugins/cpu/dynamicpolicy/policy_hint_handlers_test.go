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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCalculateHintsForNUMABindingSharedCores1(t *testing.T) {
	t.Parallel()

	testName := "test"
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	podEntries := state.PodEntries{
		"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
			testName: &state.AllocationInfo{
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
		"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
			testName: &state.AllocationInfo{
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
		"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
			testName: &state.AllocationInfo{
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
		commonstate.PoolNameReclaim: state.ContainerEntries{
			"": &state.AllocationInfo{
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
		commonstate.PoolNameShare: state.ContainerEntries{
			"": &state.AllocationInfo{
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
	}

	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
		1: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
		2: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff8123bf": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"ec6e2f30-c78a-4bc4-9576-c916db5281a3": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
		3: &state.NUMANodeState{
			DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
			AllocatedCPUSet: machine.NewCPUSet(),
			PodEntries: state.PodEntries{
				"2432d068-c5a0-46ba-a7bd-b69d9bd16961": state.ContainerEntries{
					testName: &state.AllocationInfo{
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
	}

	tests := []struct {
		name                         string
		request                      float64
		req                          *pluginapi.ResourceRequest
		enableSNBHighNumaPreference  bool
		optimizePolicy               []string
		preferUseExistNUMAHintResult bool
		expectedError                bool
		expectedHints                map[string]*pluginapi.ListOfTopologyHints
	}{
		{
			name:    "multiple numa nodes available, SNB high numa preference enabled",
			request: 1,
			req: &pluginapi.ResourceRequest{
				PodUid:        "938679740360",
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				ResourceName:  string(v1.ResourceCPU),
			},
			enableSNBHighNumaPreference:  true,
			optimizePolicy:               []string{},
			preferUseExistNUMAHintResult: false,
			expectedError:                false,
			expectedHints: map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): {
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{2}, Preferred: true},
						{Nodes: []uint64{3}, Preferred: true},
					},
				},
			},
		},
		{
			name:                         "min numa count greater than 1",
			request:                      10,
			req:                          &pluginapi.ResourceRequest{},
			enableSNBHighNumaPreference:  false,
			optimizePolicy:               []string{},
			preferUseExistNUMAHintResult: false,
			expectedError:                true,
			expectedHints:                nil,
		},
		{
			name:    "prefer existing result",
			request: 1,
			req: &pluginapi.ResourceRequest{
				PodUid:        "32523563464764",
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					"katalyst-test/nume-bind-result": "1",
				},
			},
			enableSNBHighNumaPreference:  false,
			optimizePolicy:               []string{},
			preferUseExistNUMAHintResult: true,
			expectedError:                false,
			expectedHints: map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): {
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: false},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{2}, Preferred: false},
						{Nodes: []uint64{3}, Preferred: false},
					},
				},
			},
		},
		{
			name:    "the existing results are flawed.",
			request: 1,
			req: &pluginapi.ResourceRequest{
				PodUid:        "32523563464764",
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				ResourceName:  string(v1.ResourceCPU),
				Annotations: map[string]string{
					"katalyst-test/nume-bind-result": "4c",
				},
			},
			enableSNBHighNumaPreference:  false,
			optimizePolicy:               []string{},
			preferUseExistNUMAHintResult: true,
			expectedError:                true,
			expectedHints:                nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &DynamicPolicy{
				machineInfo: &machine.KatalystMachineInfo{
					CPUTopology: cpuTopology,
				},
				NUMABindingResultAnnotationKey:      "katalyst-test/nume-bind-result",
				sharedCoresNUMABindingHintOptimizer: &hintoptimizer.DummyHintOptimizer{},
				dynamicConfig:                       dynamic.NewDynamicAgentConfiguration(),
			}
			p.dynamicConfig.GetDynamicConfiguration().PreferUseExistNUMAHintResult = tt.preferUseExistNUMAHintResult

			result, err := p.calculateHintsForNUMABindingSharedCores(tt.request, podEntries, machineState, tt.req)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tt.expectedHints, result)
			}
		})
	}
}

func TestPopulateHintsByAlreadyExistedNUMABindingResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		req           *pluginapi.ResourceRequest
		hints         *pluginapi.ListOfTopologyHints
		wantHints     *pluginapi.ListOfTopologyHints
		expectedError bool
	}{
		{
			name: "empty result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
		{
			name: "matching result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				Annotations: map[string]string{
					"numa_binding": "0",
				},
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
		{
			name: "non-matching result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				Annotations: map[string]string{
					"numa_binding": "2",
				},
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &DynamicPolicy{
				NUMABindingResultAnnotationKey: "numa_binding",
				emitter:                        &metrics.DummyMetrics{},
			}

			err := p.populateHintsByAlreadyExistedNUMABindingResult(tt.req, tt.hints)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.wantHints, tt.hints)
		})
	}
}
