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

package dynamicpolicy

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	appagent "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/oom"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	podDebugAnnoKey = "qrm.katalyst.kubewharf.io/debug_pod"
)

var (
	fakeConf = &config.Configuration{
		AgentConfiguration: &configagent.AgentConfiguration{
			GenericAgentConfiguration: &configagent.GenericAgentConfiguration{
				GenericQRMPluginConfiguration: &qrmconfig.GenericQRMPluginConfiguration{
					UseKubeletReservedConfig: false,
				},
			},
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrmconfig.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrmconfig.MemoryQRMPluginConfig{
						ReservedMemoryGB: 4,
					},
				},
			},
		},
	}
)

func getTestDynamicPolicyWithInitialization(topology *machine.CPUTopology, machineInfo *info.MachineInfo, stateFileDirectory string) (*DynamicPolicy, error) {

	reservedMemory, err := getReservedMemory(fakeConf, &metaserver.MetaServer{}, machineInfo)
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
		memconsts.MemoryResourcePluginPolicyNameDynamic, topology, machineInfo, resourcesReservedMemory, false)
	if err != nil {
		return nil, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policyImplement := &DynamicPolicy{
		topology:         topology,
		qosConfig:        qosConfig,
		state:            stateImpl,
		emitter:          metrics.DummyMetrics{},
		migratingMemory:  make(map[string]map[string]bool),
		stopCh:           make(chan struct{}),
		podDebugAnnoKeys: []string{podDebugAnnoKey},
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
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestCheckMemorySet")
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
	dynamicPolicy.checkMemorySet(nil, nil, nil, nil, nil)
}

func TestClearResidualState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestClearResidualState")
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
	dynamicPolicy.clearResidualState(nil, nil, nil, nil, nil)
}

func TestSetMemoryMigrate(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSetMemoryMigrate")
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
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemovePod")
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
	t.Parallel()

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
			description: "req for container of debug pod",
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
					podDebugAnnoKey: "",
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
							IsNodeResource:   false,
							IsScalarResource: true,
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
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocate")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		if tc.enhancementDefaultValues != nil {
			dynamicPolicy.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
		}

		dynamicPolicy.enableMemoryAdvisor = true
		dynamicPolicy.advisorClient = advisorsvc.NewStubAdvisorServiceClient()

		resp, err := dynamicPolicy.Allocate(context.Background(), tc.req)
		as.Nil(err)

		tc.expectedResp.PodUid = tc.req.PodUid
		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
	}
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

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
			description: "req for container of debug pod",
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
					podDebugAnnoKey: "",
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
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyHints")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		if tc.enhancementDefaultValues != nil {
			dynamicPolicy.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
		}

		resp, err := dynamicPolicy.GetTopologyHints(context.Background(), tc.req)
		as.Nil(err)

		tc.expectedResp.PodUid = tc.req.PodUid
		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
	}
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareAllocatableResources")
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
	t.Parallel()

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
							TopologyAwareQuantityList:         nil,
							OriginalTopologyAwareQuantityList: nil,
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
							TopologyAwareQuantityList:         nil,
							OriginalTopologyAwareQuantityList: nil,
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
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareResources")
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
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocation")
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
	t.Parallel()

	as := require.New(t)
	readonlyState, err := GetReadonlyState()
	if readonlyState == nil {
		as.NotNil(err)
	}
}

func TestGenerateResourcesMachineStateFromPodEntries(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	reservedMemory, err := getReservedMemory(fakeConf, &metaserver.MetaServer{}, machineInfo)
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

func TestHandleAdvisorResp(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	reservedMemory, err := getReservedMemory(fakeConf, &metaserver.MetaServer{}, machineInfo)
	as.Nil(err)

	resourcesReservedMemory := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	testName := "test"

	testCases := []struct {
		description                string
		podResourceEntries         state.PodResourceEntries
		expectedPodResourceEntries state.PodResourceEntries
		expectedMachineState       state.NUMANodeResourcesMap
		lwResp                     *advisorsvc.ListAndWatchResponse
	}{
		{
			description: "one shared_cores container, one reclaimed_cores container, one dedicated_cores container",
			podResourceEntries: state.PodResourceEntries{
				v1.ResourceMemory: state.PodEntries{
					pod1UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod1UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
							RampUp:               false,
							AggregatedQuantity:   7516192768,
							NumaAllocationResult: machine.NewCPUSet(0),
							TopologyAwareAllocations: map[int]uint64{
								0: 7516192768,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
					pod2UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod2UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
							RampUp:               false,
							NumaAllocationResult: machine.NewCPUSet(1, 2, 3),
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
					},
					pod3UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod3UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelReclaimedCores,
							RampUp:               false,
							NumaAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
						},
					},
				},
			},
			lwResp: &advisorsvc.ListAndWatchResponse{
				PodEntries: map[string]*advisorsvc.CalculationEntries{
					pod1UID: {
						ContainerEntries: map[string]*advisorsvc.CalculationInfo{
							testName: {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{
										string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): "5516192768",
									},
								},
							},
						},
					},
					pod2UID: {
						ContainerEntries: map[string]*advisorsvc.CalculationInfo{
							testName: {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{
										string(memoryadvisor.ControlKnobKeyDropCache): "true",
									},
								},
							},
						},
					},
					pod3UID: {
						ContainerEntries: map[string]*advisorsvc.CalculationInfo{
							testName: {
								CalculationResult: &advisorsvc.CalculationResult{
									Values: map[string]string{
										string(memoryadvisor.ControlKnobKeyCPUSetMems): "2-3",
									},
								},
							},
						},
					},
				},
			},
			expectedPodResourceEntries: state.PodResourceEntries{
				v1.ResourceMemory: state.PodEntries{
					pod1UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod1UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
							RampUp:               false,
							AggregatedQuantity:   7516192768,
							NumaAllocationResult: machine.NewCPUSet(0),
							TopologyAwareAllocations: map[int]uint64{
								0: 7516192768,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
								string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
									ControlKnobValue: "5516192768",
									OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
								},
							},
						},
					},
					pod2UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod2UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
							RampUp:               false,
							NumaAllocationResult: machine.NewCPUSet(1, 2, 3),
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
						},
					},
					pod3UID: state.ContainerEntries{
						testName: &state.AllocationInfo{
							PodUid:               pod3UID,
							PodNamespace:         testName,
							PodName:              testName,
							ContainerName:        testName,
							ContainerType:        pluginapi.ContainerType_MAIN.String(),
							ContainerIndex:       0,
							QoSLevel:             consts.PodAnnotationQoSLevelReclaimedCores,
							RampUp:               false,
							NumaAllocationResult: machine.NewCPUSet(2, 3),
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
							},
							ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
						},
					},
				},
			},
			expectedMachineState: state.NUMANodeResourcesMap{
				v1.ResourceMemory: {
					0: &state.NUMANodeState{
						TotalMemSize:   8589934592,
						SystemReserved: 1073741824,
						Allocatable:    7516192768,
						Allocated:      7516192768,
						Free:           0,
						PodEntries: state.PodEntries{
							pod1UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod1UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
									RampUp:               false,
									AggregatedQuantity:   7516192768,
									NumaAllocationResult: machine.NewCPUSet(0),
									TopologyAwareAllocations: map[int]uint64{
										0: 7516192768,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
										consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
									},
									ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
										string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
											ControlKnobValue: "5516192768",
											OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
										},
									},
								},
							},
						},
					},
					1: &state.NUMANodeState{
						TotalMemSize:   8589934592,
						SystemReserved: 1073741824,
						Allocatable:    7516192768,
						Allocated:      0,
						Free:           7516192768,
						PodEntries: state.PodEntries{
							pod2UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod2UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
									RampUp:               false,
									NumaAllocationResult: machine.NewCPUSet(1),
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
								},
							},
						},
					},
					2: &state.NUMANodeState{
						TotalMemSize:   8589934592,
						SystemReserved: 1073741824,
						Allocatable:    7516192768,
						Allocated:      0,
						Free:           7516192768,
						PodEntries: state.PodEntries{
							pod2UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod2UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
									RampUp:               false,
									NumaAllocationResult: machine.NewCPUSet(2),
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
								},
							},
							pod3UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod3UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelReclaimedCores,
									RampUp:               false,
									NumaAllocationResult: machine.NewCPUSet(2),
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
								},
							},
						},
					},
					3: &state.NUMANodeState{
						TotalMemSize:   8589934592,
						SystemReserved: 1073741824,
						Allocatable:    7516192768,
						Allocated:      0,
						Free:           7516192768,
						PodEntries: state.PodEntries{
							pod2UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod2UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
									RampUp:               false,
									NumaAllocationResult: machine.NewCPUSet(3),
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
									},
									ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
								},
							},
							pod3UID: state.ContainerEntries{
								testName: &state.AllocationInfo{
									PodUid:               pod3UID,
									PodNamespace:         testName,
									PodName:              testName,
									ContainerName:        testName,
									ContainerType:        pluginapi.ContainerType_MAIN.String(),
									ContainerIndex:       0,
									QoSLevel:             consts.PodAnnotationQoSLevelReclaimedCores,
									RampUp:               false,
									NumaAllocationResult: machine.NewCPUSet(3),
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
									},
									ExtraControlKnobInfo: make(map[string]commonstate.ControlKnobInfo),
								},
							},
						},
					},
				},
			},
		},
		{
			description:        "apply memory limits in invalid high level cgroup relative path",
			podResourceEntries: nil,
			lwResp: &advisorsvc.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CgroupPath: "invalid",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{
								string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): "12345",
							},
						},
					},
				},
			},
			expectedPodResourceEntries: nil,
			expectedMachineState:       nil,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestHandleAdvisorResp")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
		as.Nil(err)

		dynamicPolicy.metaServer = &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								UID: types.UID(pod2UID),
							},
							Spec: v1.PodSpec{
								Containers: []v1.Container{
									{Name: testName},
								},
							},
						},
					},
				},
			},
		}

		memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyMemoryLimitInBytes,
			memoryadvisor.ControlKnobHandlerWithChecker(handleAdvisorMemoryLimitInBytes))
		memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyCPUSetMems,
			memoryadvisor.ControlKnobHandlerWithChecker(handleAdvisorCPUSetMems))
		memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyDropCache,
			memoryadvisor.ControlKnobHandlerWithChecker(dynamicPolicy.handleAdvisorDropCache))

		machineState, err := state.GenerateMachineStateFromPodEntries(machineInfo, tc.podResourceEntries, resourcesReservedMemory)
		as.Nil(err)

		if tc.podResourceEntries != nil {
			dynamicPolicy.state.SetPodResourceEntries(tc.podResourceEntries)
			dynamicPolicy.state.SetMachineState(machineState)
		}

		err = dynamicPolicy.handleAdvisorResp(tc.lwResp)
		as.Nilf(err, "dynamicPolicy.handleAdvisorResp got err: %v, case: %s", err, tc.description)

		if tc.expectedPodResourceEntries != nil {
			as.Equalf(tc.expectedPodResourceEntries, dynamicPolicy.state.GetPodResourceEntries(),
				"PodResourceEntries mismatches with expected one, failed in test case: %s", tc.description)
		}

		if tc.expectedMachineState != nil {
			as.Equalf(tc.expectedMachineState, dynamicPolicy.state.GetMachineState(),
				"MachineState mismatches with expected one, failed in test case: %s", tc.description)
		}

		os.RemoveAll(tmpDir)
	}
}

func TestSetExtraControlKnobByConfigForAllocationInfo(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	pod1UID := string(uuid.NewUUID())
	testName := "test"

	testMemLimitAnnoKey := "test_mem_limit_anno_key"

	testCases := []struct {
		description             string
		pod                     *v1.Pod
		inputAllocationInfo     *state.AllocationInfo
		extraControlKnobConfigs commonstate.ExtraControlKnobConfigs
		outputAllocationInfo    *state.AllocationInfo
	}{
		{
			description: "input allocationInfo already has extra control knob entry corresponding to extraControlKnobConfigs",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{testMemLimitAnnoKey: "6516192768"},
				},
			},
			inputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
					string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
						ControlKnobValue: "5516192768",
						OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
			extraControlKnobConfigs: map[string]commonstate.ExtraControlKnobConfig{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
					PodExplicitlyAnnotationKey: testMemLimitAnnoKey,
					QoSLevelToDefaultValue: map[string]string{
						consts.PodAnnotationQoSLevelDedicatedCores: "1516192768",
						consts.PodAnnotationQoSLevelSharedCores:    "2516192768",
						consts.PodAnnotationQoSLevelReclaimedCores: "3516192768",
					},
					ControlKnobInfo: commonstate.ControlKnobInfo{
						OciPropertyName: util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
			outputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
					string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
						ControlKnobValue: "5516192768",
						OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
		},
		{
			description: "set allocationInfo default control knob value by annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{testMemLimitAnnoKey: "6516192768"},
				},
			},
			inputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			extraControlKnobConfigs: map[string]commonstate.ExtraControlKnobConfig{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
					PodExplicitlyAnnotationKey: testMemLimitAnnoKey,
					QoSLevelToDefaultValue: map[string]string{
						consts.PodAnnotationQoSLevelDedicatedCores: "1516192768",
						consts.PodAnnotationQoSLevelSharedCores:    "2516192768",
						consts.PodAnnotationQoSLevelReclaimedCores: "3516192768",
					},
					ControlKnobInfo: commonstate.ControlKnobInfo{
						OciPropertyName: util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
			outputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
					string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
						ControlKnobValue: "6516192768",
						OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
		},
		{
			description: "set allocationInfo default control knob value by qos level",
			pod:         &v1.Pod{},
			inputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			extraControlKnobConfigs: map[string]commonstate.ExtraControlKnobConfig{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
					PodExplicitlyAnnotationKey: testMemLimitAnnoKey,
					QoSLevelToDefaultValue: map[string]string{
						consts.PodAnnotationQoSLevelDedicatedCores: "1516192768",
						consts.PodAnnotationQoSLevelSharedCores:    "2516192768",
						consts.PodAnnotationQoSLevelReclaimedCores: "3516192768",
					},
					ControlKnobInfo: commonstate.ControlKnobInfo{
						OciPropertyName: util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
			outputAllocationInfo: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
					string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
						ControlKnobValue: "1516192768",
						OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		setExtraControlKnobByConfigForAllocationInfo(tc.inputAllocationInfo, tc.extraControlKnobConfigs, tc.pod)
		as.Equalf(tc.outputAllocationInfo, tc.inputAllocationInfo, "failed in test case: %s", tc.description)
	}
}

func TestSetExtraControlKnobByConfigs(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSetExtraControlKnobByConfigs")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	pod1UID := string(uuid.NewUUID())
	testName := "test"

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID: types.UID(pod1UID),
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: testName},
							},
						},
					},
				},
			},
		},
	}

	testMemLimitAnnoKey := "test_mem_limit_anno_key"
	dynamicPolicy.extraControlKnobConfigs = commonstate.ExtraControlKnobConfigs{
		string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
			PodExplicitlyAnnotationKey: testMemLimitAnnoKey,
			QoSLevelToDefaultValue: map[string]string{
				consts.PodAnnotationQoSLevelDedicatedCores: "1516192768",
				consts.PodAnnotationQoSLevelSharedCores:    "2516192768",
				consts.PodAnnotationQoSLevelReclaimedCores: "3516192768",
			},
			ControlKnobInfo: commonstate.ControlKnobInfo{
				OciPropertyName: util.OCIPropertyNameMemoryLimitInBytes,
			},
		},
	}

	podEntries := state.PodEntries{
		pod1UID: state.ContainerEntries{
			testName: &state.AllocationInfo{
				PodUid:               pod1UID,
				PodNamespace:         testName,
				PodName:              testName,
				ContainerName:        testName,
				ContainerType:        pluginapi.ContainerType_MAIN.String(),
				ContainerIndex:       0,
				QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
				RampUp:               false,
				AggregatedQuantity:   7516192768,
				NumaAllocationResult: machine.NewCPUSet(0),
				TopologyAwareAllocations: map[int]uint64{
					0: 7516192768,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
		},
	}

	podResourceEntries := state.PodResourceEntries{
		v1.ResourceMemory: podEntries,
	}

	reservedMemory, err := getReservedMemory(fakeConf, &metaserver.MetaServer{}, machineInfo)
	as.Nil(err)

	reserved := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(machineInfo, podResourceEntries, reserved)
	as.Nil(err)

	dynamicPolicy.state.SetPodResourceEntries(podResourceEntries)
	dynamicPolicy.state.SetMachineState(resourcesMachineState)

	dynamicPolicy.setExtraControlKnobByConfigs(nil, nil, nil, nil, nil)

	expectedAllocationInfo := &state.AllocationInfo{
		PodUid:               pod1UID,
		PodNamespace:         testName,
		PodName:              testName,
		ContainerName:        testName,
		ContainerType:        pluginapi.ContainerType_MAIN.String(),
		ContainerIndex:       0,
		QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
		RampUp:               false,
		AggregatedQuantity:   7516192768,
		NumaAllocationResult: machine.NewCPUSet(0),
		TopologyAwareAllocations: map[int]uint64{
			0: 7516192768,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
		ExtraControlKnobInfo: map[string]commonstate.ControlKnobInfo{
			string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): {
				ControlKnobValue: "1516192768",
				OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
			},
		},
	}

	as.Equal(expectedAllocationInfo, dynamicPolicy.state.GetPodResourceEntries()[v1.ResourceMemory][pod1UID][testName])
}

func makeMetaServer() *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	machineInfo, _ := machine.GenerateDummyMachineInfo(4, 32)

	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo:      machineInfo,
				CPUTopology:      cpuTopology,
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
			},
			PodFetcher: &pod.PodFetcherStub{},
		},
		ExternalManager: external.InitExternalManager(&pod.PodFetcherStub{}),
	}
}

func makeTestGenericContext(t *testing.T) *appagent.GenericContext {
	genericCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{})
	assert.NoError(t, err)

	return &appagent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     makeMetaServer(),
		PluginManager:  nil,
	}
}

func TestNewAndStartDynamicPolicy(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestNewAndStartDynamicPolicy")
	as.Nil(err)

	agentCtx := makeTestGenericContext(t)
	_, component, err := NewDynamicPolicy(agentCtx, &config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{},
		AgentConfiguration: &configagent.AgentConfiguration{
			GenericAgentConfiguration: &configagent.GenericAgentConfiguration{
				QRMAdvisorConfiguration: &global.QRMAdvisorConfiguration{},
				GenericQRMPluginConfiguration: &qrmconfig.GenericQRMPluginConfiguration{
					StateFileDirectory:  tmpDir,
					QRMPluginSocketDirs: []string{path.Join(tmpDir, "test.sock")},
				},
			},
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrmconfig.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrmconfig.MemoryQRMPluginConfig{
						PolicyName:                 memconsts.MemoryResourcePluginPolicyNameDynamic,
						ReservedMemoryGB:           4,
						SkipMemoryStateCorruption:  true,
						EnableSettingMemoryMigrate: false,
						EnableMemoryAdvisor:        false,
						ExtraControlKnobConfigFile: "",
					},
				},
			},
		},
	}, nil, "test_dynamic_policy")
	as.Nil(err)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		component.Run(ctx)
		wg.Done()
	}()

	cancel()
	wg.Wait()
}

func TestRemoveContainer(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemoveContainer")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	podUID := "podUID"
	containerName := "testName"

	dynamicPolicy.state.SetPodResourceEntries(state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			podUID: state.ContainerEntries{
				containerName: &state.AllocationInfo{
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

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(v1.ResourceMemory, podUID, containerName)
	as.NotNil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(v1.ResourceMemory, podUID, containerName)
	as.Nil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(v1.ResourceMemory, podUID, containerName)
	as.Nil(allocationInfo)
}

func TestServeForAdvisor(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestServeForAdvisor")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	tmpMemPluginSvrSockDir, err := ioutil.TempDir("", "checkpoint-TestServeForAdvisor-MemSvrSock")
	as.Nil(err)

	dynamicPolicy.memoryPluginSocketAbsPath = path.Join(tmpMemPluginSvrSockDir, "memSvr.sock")
	stopCh := make(chan struct{})

	go func() {
		select {
		case <-time.After(time.Second):
			close(stopCh)
		}
	}()
	dynamicPolicy.serveForAdvisor(stopCh)
}

func TestDynamicPolicy_ListContainers(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_ListContainers")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo := &info.MachineInfo{}

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	allocationInfo := &state.AllocationInfo{
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
	}

	dynamicPolicy.state.SetPodResourceEntries(state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"podUID": state.ContainerEntries{
				"testName": allocationInfo,
			},
		},
	})

	containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
	as.True(found)

	type args struct {
		in0 context.Context
		in1 *advisorsvc.Empty
	}
	tests := []struct {
		name    string
		args    args
		want    *advisorsvc.ListContainersResponse
		wantErr bool
	}{
		{
			name: "test list contaienrs",
			args: args{
				in0: context.Background(),
				in1: &advisorsvc.Empty{},
			},
			want: &advisorsvc.ListContainersResponse{
				Containers: []*advisorsvc.ContainerMetadata{
					{
						PodUid:         allocationInfo.PodUid,
						PodNamespace:   allocationInfo.PodNamespace,
						PodName:        allocationInfo.PodName,
						ContainerName:  allocationInfo.ContainerName,
						ContainerIndex: allocationInfo.ContainerIndex,
						ContainerType:  pluginapi.ContainerType(containerType),
						QosLevel:       allocationInfo.QoSLevel,
						Labels:         maputil.CopySS(allocationInfo.Labels),
						Annotations:    maputil.CopySS(allocationInfo.Annotations),
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dynamicPolicy.ListContainers(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("DynamicPolicy.ListContainers() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DynamicPolicy.ListContainers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicPolicy_hasLastLevelEnhancementKey(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_hasLastLevelEnhancementKey")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

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
						consts.PodAnnotationMemoryEnhancementOOMPriority: strconv.Itoa(qos.DefaultDedicatedCoresOOMPriorityScore),
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

	type args struct {
		lastLevelEnhancementKey string
		podUID                  string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "test hasLastLevelEnhancementKey with key",
			args: args{
				lastLevelEnhancementKey: consts.PodAnnotationMemoryEnhancementOOMPriority,
				podUID:                  "podUID",
			},
			want: true,
		},
		{
			name: "test hasLastLevelEnhancementKey without key",
			args: args{
				lastLevelEnhancementKey: consts.PodAnnotationMemoryEnhancementOOMPriority,
				podUID:                  "podUID-1",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := dynamicPolicy.hasLastLevelEnhancementKey(tt.args.lastLevelEnhancementKey, tt.args.podUID); got != tt.want {
				t.Errorf("DynamicPolicy.hasLastLevelEnhancementKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

var once sync.Once

func bpfTestInit() {
	if err := rlimit.RemoveMemlock(); err != nil {
		fmt.Println("Error removing memlock:", err)
	}
}

// TempBPFFS creates a temporary directory on a BPF FS.
//
// The directory is automatically cleaned up at the end of the test run.
func TempBPFFS(tb testing.TB) string {
	tb.Helper()

	if err := os.MkdirAll("/sys/fs/bpf", 0755); err != nil {
		tb.Fatal("Failed to create /sys/fs/bpf directory:", err)
	}

	tmp, err := os.MkdirTemp("/sys/fs/bpf", "ebpf-test")
	if err != nil {
		tb.Fatal("Create temporary directory on BPFFS:", err)
	}
	tb.Cleanup(func() { os.RemoveAll(tmp) })

	return tmp
}

func createMap(t *testing.T) *ebpf.Map {
	t.Helper()

	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.Hash,
		KeySize:    8,
		ValueSize:  8,
		MaxEntries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestClearResidualOOMPriority(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	once.Do(bpfTestInit)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestClearResidualOOMPriority")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}

	dynamicPolicy.enableOOMPriority = true
	dynamicPolicy.enhancementHandlers = make(util.ResourceEnhancementHandlerMap)

	m := createMap(t)
	defer m.Close()

	tmp := TempBPFFS(t)
	path := filepath.Join(tmp, "map")

	if err := m.Pin(path); err != nil {
		t.Fatal(err)
	}

	pinned := m.IsPinned()
	as.True(pinned)

	m.Close()

	dynamicPolicy.clearResidualOOMPriority(&config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: dynamicPolicy.qosConfig,
		},
	}, nil, nil, dynamicPolicy.emitter, dynamicPolicy.metaServer)

	dynamicPolicy.oomPriorityMap, err = ebpf.LoadPinnedMap(path, &ebpf.LoadPinOptions{})
	if err != nil {
		t.Fatalf("load oom priority map at: %s failed with error: %v", path, err)
	}
	defer dynamicPolicy.oomPriorityMap.Close()

	time.Sleep(1 * time.Second)

	if err := dynamicPolicy.oomPriorityMap.Put(uint64(0), int64(100)); err != nil {
		t.Fatal("Can't put:", err)
	}

	dynamicPolicy.clearResidualOOMPriority(&config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: dynamicPolicy.qosConfig,
		},
	}, nil, nil, dynamicPolicy.emitter, dynamicPolicy.metaServer)
}

func TestSyncOOMPriority(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	once.Do(bpfTestInit)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSyncOOMPriority")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}

	m := createMap(t)
	defer m.Close()

	tmp := TempBPFFS(t)
	path := filepath.Join(tmp, "map")

	if err := m.Pin(path); err != nil {
		t.Fatal(err)
	}

	pinned := m.IsPinned()
	as.True(pinned)

	m.Close()

	dynamicPolicy.syncOOMPriority(&config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: dynamicPolicy.qosConfig,
		},
	}, nil, nil, dynamicPolicy.emitter, dynamicPolicy.metaServer)

	dynamicPolicy.oomPriorityMap, err = ebpf.LoadPinnedMap(path, &ebpf.LoadPinOptions{})
	if err != nil {
		t.Fatalf("load oom priority map at: %s failed with error: %v", path, err)
	}
	defer dynamicPolicy.oomPriorityMap.Close()

	time.Sleep(1 * time.Second)

	dynamicPolicy.syncOOMPriority(&config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: dynamicPolicy.qosConfig,
		},
	}, nil, nil, dynamicPolicy.emitter, nil)

	dynamicPolicy.syncOOMPriority(&config.Configuration{
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: dynamicPolicy.qosConfig,
		},
	}, nil, nil, dynamicPolicy.emitter, dynamicPolicy.metaServer)
}

func TestClearOOMPriority(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	once.Do(bpfTestInit)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestClearOOMPriority")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
	as.Nil(err)

	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
		ExternalManager: external.InitExternalManager(&pod.PodFetcherStub{}),
	}

	m := createMap(t)
	defer m.Close()

	tmp := TempBPFFS(t)
	path := filepath.Join(tmp, "map")

	if err := m.Pin(path); err != nil {
		t.Fatal(err)
	}

	pinned := m.IsPinned()
	as.True(pinned)

	m.Close()

	req1 := &pluginapi.ResourceRequest{
		PodUid: "pod-1",
	}

	req2 := &pluginapi.RemovePodRequest{
		PodUid: "pod-1",
	}

	dynamicPolicy.clearOOMPriority(context.Background(), dynamicPolicy.emitter,
		dynamicPolicy.metaServer, req1, nil)

	dynamicPolicy.oomPriorityMap, err = ebpf.LoadPinnedMap(path, &ebpf.LoadPinOptions{})
	if err != nil {
		t.Fatalf("load oom priority map at: %s failed with error: %v", path, err)
	}
	defer dynamicPolicy.oomPriorityMap.Close()

	time.Sleep(1 * time.Second)

	dynamicPolicy.clearOOMPriority(context.Background(), dynamicPolicy.emitter,
		dynamicPolicy.metaServer, req2, nil)
}

func TestPollOOMBPFInit(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	once.Do(bpfTestInit)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	as.Nil(err)

	m := createMap(t)
	defer m.Close()

	tmp := TempBPFFS(t)
	path := filepath.Join(tmp, "map")

	if err := m.Pin(path); err != nil {
		t.Fatal(err)
	}

	pinned := m.IsPinned()
	as.True(pinned)

	m.Close()

	type args struct {
		isNilMap      bool
		isNilInitFunc bool
	}
	tests := []struct {
		name      string
		args      args
		isWantNil bool
	}{
		{
			name: "test with nil ebpf map and nil initOOMPriorityBPF",
			args: args{
				isNilMap:      true,
				isNilInitFunc: true,
			},
			isWantNil: true,
		},
		{
			name: "test with nil ebpf map and dummyInitOOMPriorityBPF",
			args: args{
				isNilMap:      true,
				isNilInitFunc: false,
			},
			isWantNil: true,
		},
		{
			name: "test with loaded ebpf map and dummyInitOOMPriorityBPF",
			args: args{
				isNilMap:      false,
				isNilInitFunc: false,
			},
			isWantNil: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestPollOOMBPFInit")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			as.Nil(err)

			if !tt.args.isNilMap {
				dynamicPolicy.oomPriorityMapPinnedPath = path
			}

			if !tt.args.isNilInitFunc {
				oom.SetInitOOMPriorityBPFFunc(oom.DummyInitOOMPriorityBPF)
			}

			dynamicPolicy.stopCh = make(chan struct{})
			go dynamicPolicy.PollOOMBPFInit(dynamicPolicy.stopCh)
			time.Sleep(1 * time.Second)
			close(dynamicPolicy.stopCh)

			dynamicPolicy.oomPriorityMapLock.Lock()
			defer func() {
				if dynamicPolicy.oomPriorityMap != nil {
					dynamicPolicy.oomPriorityMap.Close()
				}
				dynamicPolicy.oomPriorityMapLock.Unlock()
			}()

			if tt.isWantNil && dynamicPolicy.oomPriorityMap != nil {
				t.Errorf("TestPollOOMBPFInit() failed: oomPriorityMap should be nil, but it's not")
			} else if !tt.isWantNil && dynamicPolicy.oomPriorityMap == nil {
				t.Errorf("TestPollOOMBPFInit() failed: oomPriorityMap should not be nil, but it is")
			}
		})
	}
}

func TestDynamicPolicy_adjustAllocationEntries(t *testing.T) {
	t.Parallel()

	metaServer := makeMetaServer()
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_adjustAllocationEntries")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)

	machineInfo, err := machine.GenerateDummyMachineInfo(4, 32)
	assert.NoError(t, err)

	metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("test-pod-1-uid"),
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "test-container-1",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Requests: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "test-container-1",
							ContainerID: "test-container-1-id",
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("test-pod-2-uid"),
					Name: "test-pod-2",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "test-container-2",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Requests: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "test-container-2",
							ContainerID: "test-container-2-id",
						},
					},
				},
			},
		},
	}
	podResourceEntries := state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"test-pod-2-uid": state.ContainerEntries{
				"test-container-2": &state.AllocationInfo{
					PodUid:               "test-pod-2-uid",
					PodNamespace:         "test",
					PodName:              "test-pod-2",
					ContainerName:        "test-container-2",
					ContainerType:        pluginapi.ContainerType_MAIN.String(),
					ContainerIndex:       0,
					QoSLevel:             consts.PodAnnotationQoSLevelDedicatedCores,
					RampUp:               false,
					AggregatedQuantity:   7516192768,
					NumaAllocationResult: machine.NewCPUSet(0),
					TopologyAwareAllocations: map[int]uint64{
						0: 7516192768,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			"test-pod-1-uid": state.ContainerEntries{
				"test-container-1": &state.AllocationInfo{
					PodUid:               "test-pod-1-uid",
					PodNamespace:         "test",
					PodName:              "test-pod-1",
					ContainerName:        "test-container-1",
					ContainerType:        pluginapi.ContainerType_MAIN.String(),
					ContainerIndex:       0,
					QoSLevel:             consts.PodAnnotationQoSLevelSharedCores,
					RampUp:               false,
					NumaAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
		},
	}
	type fields struct {
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "normal adjustment",
			fields: fields{
				emitter:    metrics.DummyMetrics{},
				metaServer: metaServer,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, machineInfo, tmpDir)
			assert.NoError(t, err)
			dynamicPolicy.emitter = tt.fields.emitter
			dynamicPolicy.metaServer = tt.fields.metaServer
			dynamicPolicy.asyncWorkers = asyncworker.NewAsyncWorkers(memoryPluginAsyncWorkersName, dynamicPolicy.emitter)
			dynamicPolicy.state.SetPodResourceEntries(podResourceEntries)
			reservedMemory, err := getReservedMemory(fakeConf, dynamicPolicy.metaServer, machineInfo)
			assert.NoError(t, err)

			resourcesReservedMemory := map[v1.ResourceName]map[int]uint64{
				v1.ResourceMemory: reservedMemory,
			}
			machineState, err := state.GenerateMachineStateFromPodEntries(machineInfo, podResourceEntries, resourcesReservedMemory)
			assert.NoError(t, err)
			dynamicPolicy.state.SetMachineState(machineState)

			if err := dynamicPolicy.adjustAllocationEntries(); (err != nil) != tt.wantErr {
				t.Errorf("DynamicPolicy.adjustAllocationEntries() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
