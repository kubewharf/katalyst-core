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
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
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

			statePolicy, stateErr := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
			require.NoError(t, stateErr)
			statePolicy.state.SetReservedCPUs(machine.NewCPUSet())
			p := &DynamicPolicy{
				machineInfo: &machine.KatalystMachineInfo{
					CPUTopology: cpuTopology,
				},
				state:                               statePolicy.state,
				numaBindingResultAnnotationKey:      "katalyst-test/nume-bind-result",
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
				numaBindingResultAnnotationKey: "numa_binding",
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

func newTestPolicyForCPUTotalRequestThreshold(t *testing.T, ratio float64) *DynamicPolicy {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	policy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationInplaceUpdateResizingKey,
	}

	policy.conf.CPUQRMPluginConfig.TotalRequestThresholdHintOptimizerConfig.CPUTotalRequestThresholdRatio = ratio
	return policy
}

func newCPUTotalRequestThresholdReq(qosLevel string, reqCPU float64, inplaceResize bool) *pluginapi.ResourceRequest {
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey:          qosLevel,
		consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
	}
	if inplaceResize {
		annotations[consts.PodAnnotationInplaceUpdateResizingKey] = "true"
	}

	return &pluginapi.ResourceRequest{
		PodUid:         "pod-uid",
		PodNamespace:   "default",
		PodName:        "pod",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): reqCPU,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: qosLevel,
		},
		Annotations: annotations,
	}
}

func newCPUTotalRequestThresholdAllocationInfo(podUID, qosLevel string, numaID int, request float64, numaBinding bool) *state.AllocationInfo {
	ownerPoolName := commonstate.PoolNameShare
	switch qosLevel {
	case consts.PodAnnotationQoSLevelDedicatedCores:
		ownerPoolName = commonstate.PoolNameDedicated
	case consts.PodAnnotationQoSLevelReclaimedCores:
		ownerPoolName = commonstate.PoolNameReclaim
	}

	annotations := map[string]string{}
	if numaBinding {
		annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}

	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			OwnerPoolName: ownerPoolName,
			Annotations:   annotations,
			QoSLevel:      qosLevel,
		},
		TopologyAwareAssignments: map[int]machine.CPUSet{
			numaID: machine.NewCPUSet(numaID),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			numaID: machine.NewCPUSet(numaID),
		},
		RequestQuantity: request,
	}
}

func setCPUTotalRequestThresholdPodEntries(t *testing.T, policy *DynamicPolicy, podEntries state.PodEntries) {
	t.Helper()

	machineState, err := generateMachineStateFromPodEntries(policy.machineInfo.CPUTopology, podEntries, policy.state.GetMachineState())
	require.NoError(t, err)

	policy.state.SetPodEntries(podEntries, false)
	policy.state.SetMachineState(machineState, false)
}

func TestCPUTotalRequestThresholdRejectExistingAllocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		qosLevel       string
		reqCount       float64
		inplaceResize  bool
		allocationInfo *state.AllocationInfo
		expectedError  bool
	}{
		{
			name:          "non-vpa shared numa binding with request less than cpu threshold",
			qosLevel:      consts.PodAnnotationQoSLevelSharedCores,
			reqCount:      1.5,
			inplaceResize: false,
			allocationInfo: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-uid",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "main",
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1, 2, 3),
				},
				RequestQuantity: 1,
			},
			expectedError: false,
		},
		{
			name:          "vpa shared numa binding with request equals cpu threshold",
			qosLevel:      consts.PodAnnotationQoSLevelSharedCores,
			reqCount:      1.5,
			inplaceResize: true,
			allocationInfo: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-uid",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "main",
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1, 2, 3),
				},
				RequestQuantity: 1,
			},
			expectedError: false,
		},
		{
			name:          "non-vpa shared numa binding with request greater than cpu threshold reuses existing hint",
			qosLevel:      consts.PodAnnotationQoSLevelSharedCores,
			reqCount:      1.8,
			inplaceResize: false,
			allocationInfo: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-uid",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "main",
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1, 2, 3),
				},
				RequestQuantity: 1,
			},
			expectedError: false,
		},
		{
			name:          "vpa shared numa binding with request greater than cpu threshold",
			qosLevel:      consts.PodAnnotationQoSLevelSharedCores,
			reqCount:      1.8,
			inplaceResize: true,
			allocationInfo: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-uid",
					PodNamespace:  "default",
					PodName:       "pod",
					ContainerName: "main",
					Annotations: map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
				},
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1, 2, 3),
				},
				RequestQuantity: 1,
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := newTestPolicyForCPUTotalRequestThreshold(t, 0.5)
			req := newCPUTotalRequestThresholdReq(tt.qosLevel, tt.reqCount, tt.inplaceResize)
			if tt.allocationInfo != nil {
				policy.state.SetAllocationInfo(tt.allocationInfo.AllocationMeta.PodUid, tt.allocationInfo.AllocationMeta.ContainerName, tt.allocationInfo, true)
				if tt.allocationInfo.CheckNUMABinding() {
					req.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
				}
			}
			_, err := policy.GetTopologyHints(context.Background(), req)

			if tt.expectedError {
				require.ErrorContains(t, err, "exceeds threshold")
				require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCPUTotalRequestThresholdRejectByNUMATotalRequest(t *testing.T) {
	t.Parallel()

	type existingRequest struct {
		qosLevel    string
		request     float64
		numaBinding bool
	}

	for _, tt := range []struct {
		name             string
		existingRequests map[int][]existingRequest
		reqCount         float64
		expectedError    bool
		expectedRespSize int
	}{
		{
			name: "one numa exceeds threshold by total request",
			existingRequests: map[int][]existingRequest{
				2: {{qosLevel: consts.PodAnnotationQoSLevelDedicatedCores, request: 0.6, numaBinding: true}},
			},
			reqCount:         1.5,
			expectedRespSize: 3,
		},
		{
			name: "total request equals threshold",
			existingRequests: map[int][]existingRequest{
				2: {{qosLevel: consts.PodAnnotationQoSLevelReclaimedCores, request: 0.5, numaBinding: true}},
			},
			reqCount:         1.5,
			expectedRespSize: 4,
		},
		{
			name: "non-binding shared request is ignored",
			existingRequests: map[int][]existingRequest{
				2: {{qosLevel: consts.PodAnnotationQoSLevelSharedCores, request: 10, numaBinding: false}},
			},
			reqCount:         1.5,
			expectedRespSize: 4,
		},
		{
			name: "numa-binding reclaimed request is ignored",
			existingRequests: map[int][]existingRequest{
				2: {{qosLevel: consts.PodAnnotationQoSLevelReclaimedCores, request: 10, numaBinding: true}},
			},
			reqCount:         1.5,
			expectedRespSize: 4,
		},
		{
			name: "all numas exceed threshold by total request",
			existingRequests: map[int][]existingRequest{
				0: {{qosLevel: consts.PodAnnotationQoSLevelSharedCores, request: 0.1, numaBinding: true}},
				1: {{qosLevel: consts.PodAnnotationQoSLevelDedicatedCores, request: 0.1, numaBinding: true}},
				2: {{qosLevel: consts.PodAnnotationQoSLevelSharedCores, request: 0.6, numaBinding: true}},
				3: {{qosLevel: consts.PodAnnotationQoSLevelDedicatedCores, request: 0.6, numaBinding: true}},
			},
			reqCount:         1.5,
			expectedError:    true,
			expectedRespSize: 0,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := newTestPolicyForCPUTotalRequestThreshold(t, 0.5)
			podEntries := state.PodEntries{}
			for numaID, requests := range tt.existingRequests {
				for i, existing := range requests {
					podUID := fmt.Sprintf("existing-%s-pod-%d-%d", existing.qosLevel, numaID, i)
					podEntries[podUID] = state.ContainerEntries{
						"main": newCPUTotalRequestThresholdAllocationInfo(podUID, existing.qosLevel, numaID, existing.request, existing.numaBinding),
					}
				}
			}
			setCPUTotalRequestThresholdPodEntries(t, policy, podEntries)

			req := newCPUTotalRequestThresholdReq(consts.PodAnnotationQoSLevelSharedCores, tt.reqCount, false)
			resp, err := policy.GetTopologyHints(context.Background(), req)
			if tt.expectedError {
				require.ErrorContains(t, err, "exceeds threshold")
				require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
			} else {
				require.NoError(t, err)
			}
			if resp != nil {
				require.Equal(t, tt.expectedRespSize, len(resp.ResourceHints[string(v1.ResourceCPU)].Hints))
			} else {
				require.Equal(t, tt.expectedRespSize, 0)
			}
		})
	}
}

func TestCPUTotalRequestThresholdInplaceResizeByNUMATotalRequest(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name             string
		existingRequest  float64
		resizedRequest   float64
		expectedError    bool
		expectedRespSize int
	}{
		{
			name:             "exclude origin request and pass total request threshold",
			existingRequest:  0.4,
			resizedRequest:   1.5,
			expectedRespSize: 1,
		},
		{
			name:             "reject resized request by total request threshold",
			existingRequest:  0.6,
			resizedRequest:   1.5,
			expectedError:    true,
			expectedRespSize: 0,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := newTestPolicyForCPUTotalRequestThreshold(t, 0.5)
			podEntries := state.PodEntries{
				"pod-uid": {
					"main": newCPUTotalRequestThresholdAllocationInfo("pod-uid", consts.PodAnnotationQoSLevelSharedCores, 2, 1, true),
				},
				"existing-pod": {
					"main": newCPUTotalRequestThresholdAllocationInfo("existing-pod", consts.PodAnnotationQoSLevelSharedCores, 2, tt.existingRequest, true),
				},
			}
			setCPUTotalRequestThresholdPodEntries(t, policy, podEntries)

			req := newCPUTotalRequestThresholdReq(consts.PodAnnotationQoSLevelSharedCores, tt.resizedRequest, true)
			resp, err := policy.GetTopologyHints(context.Background(), req)
			if tt.expectedError {
				require.ErrorContains(t, err, "exceeds threshold")
				require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
			} else {
				require.NoError(t, err)
			}
			if resp != nil {
				require.Equal(t, tt.expectedRespSize, len(resp.ResourceHints[string(v1.ResourceCPU)].Hints))
			} else {
				require.Equal(t, tt.expectedRespSize, 0)
			}
		})
	}
}

func TestCPUTotalRequestThresholdRejectNewPod(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name             string
		reqCount         float64
		expectedError    bool
		expectedRespSize int
	}{
		{
			name:             "request less than cpu threshold",
			reqCount:         1,
			expectedRespSize: 4,
		},
		{
			name:             "request equals cpu threshold",
			reqCount:         1.5,
			expectedRespSize: 4,
		},
		{
			name:             "request greater than cpu threshold slightly",
			reqCount:         2,
			expectedRespSize: 2,
		},
		{
			name:             "request greater than cpu threshold",
			reqCount:         2.1,
			expectedError:    true,
			expectedRespSize: 0,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := newTestPolicyForCPUTotalRequestThreshold(t, 0.5)
			req := newCPUTotalRequestThresholdReq(consts.PodAnnotationQoSLevelSharedCores, tt.reqCount, false)

			resp, err := policy.GetTopologyHints(context.Background(), req)
			if tt.expectedError {
				require.ErrorContains(t, err, "exceeds threshold")
				require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
			} else {
				require.NoError(t, err)
			}
			if resp != nil {
				require.Equal(t, tt.expectedRespSize, len(resp.ResourceHints[string(v1.ResourceCPU)].Hints))
			} else {
				require.Equal(t, tt.expectedRespSize, 0)
			}
		})
	}
}
