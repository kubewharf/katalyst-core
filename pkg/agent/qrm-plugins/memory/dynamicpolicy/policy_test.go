//go:build !linux
// +build !linux

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
	"io/ioutil"
	"os"
	"strings"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func getTestDynamicPolicyWithInitialization(topology *machine.CPUTopology, machineInfo *info.MachineInfo, stateFileDirectory string) (*DynamicPolicy, error) {
	reservedMemory, err := getReservedMemory(machineInfo, 4)
	if err != nil {
		return nil, err
	}

	resourcesReservedMemory := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}

	qosConfig := generic.NewQoSConfiguration()
	qosConfig.SetExpandQoSLevelSelector(consts.PodAnnotationQoSLevelSharedCores, map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
	})
	qosConfig.SetExpandQoSLevelSelector(consts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
	})
	qosConfig.SetExpandQoSLevelSelector(consts.PodAnnotationQoSLevelReclaimedCores, map[string]string{
		consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
	})

	stateImpl, err := state.NewCheckpointState(stateFileDirectory, memoryPluginStateFileName,
		MemoryResourcePluginPolicyNameDynamic, topology, machineInfo, resourcesReservedMemory, false)
	if err != nil {
		return nil, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policyImplement := &DynamicPolicy{
		topology:        topology,
		qosConfig:       qosConfig,
		state:           stateImpl,
		emitter:         metrics.DummyMetrics{},
		migratingMemory: make(map[string]map[string]bool),
		stopCh:          make(chan struct{}),
	}

	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
	}

	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
	}

	return policyImplement, nil
}

func TestCheckMemorySet(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.state.SetPodResourceEntries(state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"podUID": state.ContainerEntries{
				"testName": &state.AllocationInfo{
					PodUid:               "podUID",
					PodNamespace:         "testName",
					PodName:              "testName",
					ContainerName:        "testName",
					ContainerType:        pluginapi.ContainerType_MAIN.String(),
					ContainerIndex:       0,
					QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
					RampUp:               false,
					AggregatedQuantity:   9663676416,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 9663676416,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
		},
	})

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}
	dynamicPolicy.checkMemorySet()
}

func TestClearResidualState(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}
	dynamicPolicy.clearResidualState()
}

func TestSetMemoryMigrate(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.state.SetPodResourceEntries(state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"podUID": state.ContainerEntries{
				"testName": &state.AllocationInfo{
					PodUid:               "podUID",
					PodNamespace:         "testName",
					PodName:              "testName",
					ContainerName:        "testName",
					ContainerType:        pluginapi.ContainerType_MAIN.String(),
					ContainerIndex:       0,
					QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
					RampUp:               false,
					AggregatedQuantity:   9663676416,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 9663676416,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			"podUID-1": state.ContainerEntries{
				"testName-1": &state.AllocationInfo{
					PodUid:               "podUID-1",
					PodNamespace:         "testName-1",
					PodName:              "testName-1",
					ContainerName:        "testName-1",
					ContainerType:        pluginapi.ContainerType_MAIN.String(),
					ContainerIndex:       0,
					QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
					RampUp:               false,
					AggregatedQuantity:   9663676416,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 9663676416,
					},
				},
			},
		},
	})

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}
	dynamicPolicy.setMemoryMigrate()
}

func TestRemovePod(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	testName := "test"

	// test for gt
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp, err := dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.Nil(err)

	as.Equal(&pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       req.PodUid,
		PodNamespace: testName,
		PodName:      testName,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: testName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				string(v1.ResourceMemory): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         7516192768,
					OriginalAggregatedQuantity: 7516192768,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 7516192768, Node: 0},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 7516192768, Node: 0},
					},
				},
			},
		},
	}, resp)

	dynamicPolicy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
		PodUid: req.PodUid,
	})

	_, err = dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.NotNil(err)
	as.True(strings.Contains(err.Error(), "is not show up in memory plugin state"))
}

func TestAllocate(t *testing.T) {
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description              string
		req                      *pluginapi.ResourceRequest
		expectedResp             *pluginapi.ResourceAllocationResponse
		enhancementDefaultValues map[string]string
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1048576,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(v1.ResourceMemory),
				AllocationResult: nil,
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			description: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 0,
							AllocationResult:  machine.NewCPUSet(0, 1, 2, 3).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			description: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 0,
							AllocationResult:  machine.NewCPUSet(0, 1, 2, 3).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2147483648,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 7516192768,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2147483648,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2147483648,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2147483648,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 7516192768,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & without default numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2147483648,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceMemory): {
							OciPropertyName:   util.OCIPropertyNameCPUSetMems,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2147483648,
							AllocationResult:  machine.NewCPUSet(0).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		if tc.enhancementDefaultValues != nil {
			dynamicPolicy.qosConfig.EnhancementDefaultValues = tc.enhancementDefaultValues
		}

		resp, err := dynamicPolicy.Allocate(context.Background(), tc.req)
		as.Nil(err)

		tc.expectedResp.PodUid = tc.req.PodUid
		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
	}
}

func TestGetTopologyHints(t *testing.T) {
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description              string
		req                      *pluginapi.ResourceRequest
		expectedResp             *pluginapi.ResourceHintsResponse
		enhancementDefaultValues map[string]string
	}{
		{
			description: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			description: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 10737418240,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "false"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 10737418240,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			description: "req for dedicated_cores with numa_binding & without numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		if tc.enhancementDefaultValues != nil {
			dynamicPolicy.qosConfig.EnhancementDefaultValues = tc.enhancementDefaultValues
		}

		resp, err := dynamicPolicy.GetTopologyHints(context.Background(), tc.req)
		as.Nil(err)

		tc.expectedResp.PodUid = tc.req.PodUid
		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
	}
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	resp, err := dynamicPolicy.GetTopologyAwareAllocatableResources(context.Background(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	as.Nil(err)

	as.Equal(&pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:   false,
				IsScalarResource: true,
				TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 7516192768, Node: 0},
					{ResourceValue: 7516192768, Node: 1},
					{ResourceValue: 7516192768, Node: 2},
					{ResourceValue: 7516192768, Node: 3},
				},
				TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 8589934592, Node: 0},
					{ResourceValue: 8589934592, Node: 1},
					{ResourceValue: 8589934592, Node: 2},
					{ResourceValue: 8589934592, Node: 3},
				},
				AggregatedAllocatableQuantity: 30064771072,
				AggregatedCapacityQuantity:    34359738368,
			},
		},
	}, resp)
}

func TestGetTopologyAwareResources(t *testing.T) {
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.GetTopologyAwareResourcesResponse
		err          error
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 2,
				},
			},
			err: fmt.Errorf("error occurred"),
		},
		{
			description: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodNamespace: testName,
				PodName:      testName,
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: testName,
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						string(v1.ResourceMemory): {
							IsNodeResource:                    false,
							IsScalarResource:                  true,
							AggregatedQuantity:                0,
							OriginalAggregatedQuantity:        0,
							TopologyAwareQuantityList:         []*pluginapi.TopologyAwareQuantity{},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{},
						},
					},
				},
			},
		},
		{
			description: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 1073741824,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodNamespace: testName,
				PodName:      testName,
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: testName,
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						string(v1.ResourceMemory): {
							IsNodeResource:                    false,
							IsScalarResource:                  true,
							AggregatedQuantity:                0,
							OriginalAggregatedQuantity:        0,
							TopologyAwareQuantityList:         []*pluginapi.TopologyAwareQuantity{},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{},
						},
					},
				},
			},
		},
		{
			description: "req for dedicated_cores with numa_binding main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceMemory),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceMemory): 10737418240,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.GetTopologyAwareResourcesResponse{
				PodNamespace: testName,
				PodName:      testName,
				ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
					ContainerName: testName,
					AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
						string(v1.ResourceMemory): {
							IsNodeResource:             false,
							IsScalarResource:           true,
							AggregatedQuantity:         15032385536,
							OriginalAggregatedQuantity: 15032385536,
							TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 7516192768, Node: 0},
								{ResourceValue: 7516192768, Node: 1},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 7516192768, Node: 0},
								{ResourceValue: 7516192768, Node: 1},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		_, err = dynamicPolicy.Allocate(context.Background(), tc.req)
		as.Nil(err)

		resp, err := dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
			PodUid:        tc.req.PodUid,
			ContainerName: testName,
		})

		if tc.err != nil {
			as.NotNil(err)
			continue
		} else {
			as.Nil(err)
			tc.expectedResp.PodUid = tc.req.PodUid
		}

		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		os.Remove(tmpDir)
	}
}

func TestGetResourcesAllocation(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	testName := "test"

	// test for shared_cores
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 1073741824,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp1, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)])
	as.Equal(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetMems,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 0,
		AllocationResult:  machine.NewCPUSet(0, 1, 2, 3).String(),
	})

	// test for reclaimed_cores
	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 1073741824,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp2, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)])
	as.Equal(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetMems,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 0,
		AllocationResult:  machine.NewCPUSet(0, 1, 2, 3).String(),
	})

	os.RemoveAll(tmpDir)
	dynamicPolicy, err = getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	// test for dedicated_cores with numa_binding
	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceMemory),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			string(v1.ResourceMemory): 2147483648,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp3, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp3.PodResources[req.PodUid])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)])
	as.Equal(resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceMemory)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetMems,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 7516192768,
		AllocationResult:  machine.NewCPUSet(0).String(),
	})
}

func TestGetReadonlyState(t *testing.T) {
	as := require.New(t)
	readonlyState, err := GetReadonlyState()
	as.NotNil(err)
	as.Nil(readonlyState)
}

func TestGenerateResourcesMachineStateFromPodEntries(t *testing.T) {
	as := require.New(t)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	reservedMemory, err := getReservedMemory(machineInfo, 4)
	as.Nil(err)

	podUID := string(uuid.NewUUID())
	testName := "test"

	podEntries := state.PodEntries{
		podUID: state.ContainerEntries{
			testName: &state.AllocationInfo{
				PodUid:               podUID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				RampUp:               false,
				AggregatedQuantity:   9663676416,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 9663676416,
				},
			},
		},
	}

	podResourceEntries := state.PodResourceEntries{
		v1.ResourceMemory: podEntries,
	}

	reserved := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(machineInfo, podResourceEntries, reserved)
	as.Nil(err)

	as.NotNil(resourcesMachineState[v1.ResourceMemory][0])
	as.Equal(uint64(9663676416), resourcesMachineState[v1.ResourceMemory][0].Allocatable)
}
