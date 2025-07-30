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
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	componentagent "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	podDebugAnnoKey = "qrm.katalyst.kubewharf.io/debug_pod"
)

type cpuTestCase struct {
	cpuNum      int
	socketNum   int
	numaNum     int
	fakeNUMANum int
}

func generateSharedNumaBindingPoolAllocationMeta(poolName string) commonstate.AllocationMeta {
	meta := commonstate.GenerateGenericPoolAllocationMeta(poolName)
	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}
	meta.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	meta.QoSLevel = consts.PodAnnotationQoSLevelSharedCores
	return meta
}

func getTestDynamicPolicyWithInitialization(topology *machine.CPUTopology, stateFileDirectory string) (*DynamicPolicy, error) {
	dynamicPolicy, err := getTestDynamicPolicyWithoutInitialization(topology, stateFileDirectory)
	if err != nil {
		return nil, err
	}

	err = dynamicPolicy.initReservePool()
	if err != nil {
		return nil, err
	}

	err = dynamicPolicy.initReclaimPool()
	if err != nil {
		return nil, err
	}

	dynamicPolicy.stopCh = make(chan struct{})
	return dynamicPolicy, nil
}

func getTestDynamicPolicyWithoutInitialization(topology *machine.CPUTopology, stateFileDirectory string) (*DynamicPolicy, error) {
	stateImpl, err := state.NewCheckpointState(stateFileDirectory, cpuPluginStateFileName, cpuconsts.CPUResourcePluginPolicyNameDynamic, topology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	if err != nil {
		return nil, err
	}

	extraTopologyInfo, _ := machine.GenerateDummyExtraTopology(topology.NumNUMANodes)
	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology:       topology,
		ExtraTopologyInfo: extraTopologyInfo,
	}

	reservedCPUs, _, err := calculator.TakeHTByNUMABalance(machineInfo, machineInfo.CPUDetails.CPUs().Clone(), 2)
	if err != nil {
		return nil, err
	}

	qosConfig := generic.NewQoSConfiguration()
	dynamicConfig := dynamic.NewDynamicAgentConfiguration()

	conf, err := options.NewOptions().Config()
	if err != nil {
		return nil, err
	}

	policyImplement := &DynamicPolicy{
		conf:                      conf,
		machineInfo:               machineInfo,
		qosConfig:                 qosConfig,
		dynamicConfig:             dynamicConfig,
		state:                     stateImpl,
		advisorValidator:          validator.NewCPUAdvisorValidator(stateImpl, machineInfo),
		featureGateManager:        featuregatenegotiation.NewFeatureGateManager(config.NewConfiguration()),
		reservedReclaimedCPUsSize: general.Max(reservedReclaimedCPUsSize, topology.NumNUMANodes),
		reservedCPUs:              reservedCPUs,
		enableReclaimNUMABinding:  true,
		emitter:                   metrics.DummyMetrics{},
		podDebugAnnoKeys:          []string{podDebugAnnoKey},
	}

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	policyImplement.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher:          &pod.PodFetcherStub{},
			KatalystMachineInfo: machineInfo,
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}

	// initialize hint optimizer
	err = policyImplement.initHintOptimizers()
	if err != nil {
		return nil, err
	}

	return policyImplement, nil
}

func TestInitPoolAndCalculator(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestInitPoolAndCalculator")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policyImpl, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = policyImpl.initReclaimPool()
	as.Nil(err)

	reclaimPoolAllocationInfo := policyImpl.state.GetAllocationInfo(commonstate.PoolNameReclaim, "")

	as.NotNil(reclaimPoolAllocationInfo)

	as.Equal(reclaimPoolAllocationInfo.AllocationResult.Size(), reservedReclaimedCPUsSize)
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemovePod")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
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
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
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
				string(v1.ResourceCPU): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         14,
					OriginalAggregatedQuantity: 14,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 4, Node: 3},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 4, Node: 3},
					},
				},
			},
		},
	}, resp)

	_, _ = dynamicPolicy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
		PodUid: req.PodUid,
	})

	_, err = dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.NotNil(err)
	as.True(strings.Contains(err.Error(), "is not show up in cpu plugin state"))
}

func TestAllocate(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		name                                  string
		req                                   *pluginapi.ResourceRequest
		expectedResp                          *pluginapi.ResourceAllocationResponse
		enableReclaim                         bool
		wantError                             bool
		cpuTopology                           *machine.CPUTopology
		enhancementDefaultValues              map[string]string
		allowSharedCoresOverlapReclaimedCores bool
	}{
		{
			name: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(v1.ResourceCPU),
				AllocationResult: nil,
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for container of debug pod",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 14, // ramp up
							AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15).String(),
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							AllocationResult:  machine.NewCPUSet(1, 3, 4, 6).String(),
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							AllocationResult:  machine.NewCPUSet(1, 9).String(),
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
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
			cpuTopology: cpuTopology,
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			name: "req for dedicated_cores with numa_binding without default numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2,
							AllocationResult:  machine.NewCPUSet(1, 9).String(),
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
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores numa_binding main container with enableReclaim false",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for shared_cores numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 1,
							AllocationResult:  machine.NewCPUSet(1).String(),
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:      "req for shared_cores numa_binding main container with invalid hint",
			wantError: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
			expectedResp: nil,
			cpuTopology:  cpuTopology,
		},
		{
			name: "req for shared_cores with numa_binding with allowSharedCoresOverlapReclaimedCores set to true",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 3,
							AllocationResult:  machine.NewCPUSet(1, 8, 9).String(),
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for system_cores with specified cpuset pool",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSystemCores,
					consts.PodAnnotationCPUEnhancementKey: `{"cpuset_pool": "reserve"}`,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 2, // reserve pool
							AllocationResult:  machine.NewCPUSet(0, 2).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSystemCores,
					consts.PodAnnotationCPUEnhancementCPUSet: "reserve",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for system_cores without specified cpuset pool",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: float64(cpuTopology.CPUDetails.CPUs().Size()), // default for all cpuset
							AllocationResult:  cpuTopology.CPUDetails.CPUs().String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{nil},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for reclaim actual numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 1,
							AllocationResult:  machine.NewCPUSet(1).String(),
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:          "req for reclaim non-actual numa_binding main container with enableReclaim true",
			enableReclaim: true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 0.3,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							AllocationResult:  machine.NewCPUSet(1, 3, 4, 6).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0, 1},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocate")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			if tc.enhancementDefaultValues != nil {
				dynamicPolicy.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
			}

			if tc.enableReclaim {
				dynamicPolicy.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			}

			if tc.allowSharedCoresOverlapReclaimedCores {
				dynamicPolicy.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
			}

			resp, err := dynamicPolicy.Allocate(context.Background(), tc.req)
			if tc.wantError {
				as.NotNil(err)
				return
			}

			as.Nilf(err, "failed in test case: %s", tc.name)
			tc.expectedResp.PodUid = tc.req.PodUid
			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.name)

			if tc.allowSharedCoresOverlapReclaimedCores {
				err := dynamicPolicy.adjustAllocationEntries(true)
				as.NotNil(err)
			}

			_ = os.RemoveAll(tmpDir)
		})
	}
}

func TestAllocateForPod(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateForPod")
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: "testNamespace",
		PodName:      "testName",
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = dynamicPolicy.AllocateForPod(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)
	highDensityCPUTopology, err := machine.GenerateDummyCPUTopology(384, 2, 12)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		name                     string
		req                      *pluginapi.ResourceRequest
		podEntries               state.PodEntries
		cpuNUMAHintPreferPolicy  string
		expectedResp             *pluginapi.ResourceHintsResponse
		cpuTopology              *machine.CPUTopology
		numaHeadroom             map[int]float64
		enhancementDefaultValues map[string]string
	}{
		{
			name: "req for container of debug pod",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
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
							{
								Nodes:     []uint64{0, 1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
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
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
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
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: "false",
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
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
							{
								Nodes:     []uint64{0, 1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
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
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: cpuTopology,
			enhancementDefaultValues: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		{
			name: "req for dedicated_cores with numa_binding & without default numa_exclusive main container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
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
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for shared_cores with numa_binding main container with default cpuNUMAHintPreferPolicy(spreading)",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},

						RequestQuantity: 2,
					},
				},
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 1,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: false,
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with packing cpuNUMAHintPreferPolicy(packing)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						RequestQuantity: 2,
					},
				},
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 1,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with dynamic_packing cpuNUMAHintPreferPolicy(apply packing)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
							OwnerPoolName: commonstate.PoolNameDedicated,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						RequestQuantity: 2,
					},
				},
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4-5,12,6-7,14"),
						OriginalAllocationResult: machine.MustParse("4-5,12,6-7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name:                    "req for shared_cores with numa_binding main container with dynamic_packing cpuNUMAHintPreferPolicy(apply spreading)",
			cpuNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812kkk": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812kkk",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("1,8,9"),
						OriginalAllocationResult: machine.MustParse("1,8,9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
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
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
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
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
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
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for dedicated_cores with numa_binding & numa_exclusive main container in high density machine",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 380,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
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
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
			cpuTopology: highDensityCPUTopology,
		},
		{
			name: "req for reclaimed_cores with actual numa_binding main container",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
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
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
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
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores with non-actual numa_binding main container",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 4,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
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
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
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
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
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
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "req for reclaimed_cores with actual numa_binding main container and an actual numa_binding container existed",
			numaHeadroom: map[int]float64{
				0: 2,
				1: 1,
				2: 2.5,
				3: 3,
			},
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
				},
			},
			podEntries: state.PodEntries{
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
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "0",
							},
							QoSLevel: consts.PodAnnotationQoSLevelReclaimedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("9"),
						OriginalAllocationResult: machine.MustParse("9"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("9,11,13,15"),
						OriginalAllocationResult: machine.MustParse("9,11,13,15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(9),
							1: machine.NewCPUSet(11),
							2: machine.NewCPUSet(13),
							3: machine.NewCPUSet(15),
						},
					},
				},
				"share-NUMA2": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA2"),
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
					},
				},
				"share-NUMA1": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA1"),
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
					},
				},
				"share-NUMA3": state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           generateSharedNumaBindingPoolAllocationMeta("share-NUMA3"),
						AllocationResult:         machine.MustParse("6,7,14"),
						OriginalAllocationResult: machine.MustParse("6,7,14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							3: machine.NewCPUSet(6, 7, 14),
						},
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812iii": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812iii",
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA2",
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
						AllocationResult:         machine.MustParse("4,5,12"),
						OriginalAllocationResult: machine.MustParse("4,5,12"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12),
						},
						RequestQuantity: 3,
					},
				},
				"373d08e4-7a6b-4293-aaaf-b135ff812ooo": state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812ooo",
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
						RequestQuantity: 3,
					},
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2},
								Preferred: true,
							},
							{
								Nodes:     []uint64{1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyHints")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			if tc.enhancementDefaultValues != nil {
				dynamicPolicy.qosConfig.QoSEnhancementDefaultValues = tc.enhancementDefaultValues
			}

			if tc.podEntries != nil {
				machineState, err := generateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries)
				as.Nil(err)

				dynamicPolicy.state.SetPodEntries(tc.podEntries, true)
				dynamicPolicy.state.SetMachineState(machineState, true)
			}

			if tc.numaHeadroom != nil {
				dynamicPolicy.state.SetNUMAHeadroom(tc.numaHeadroom, true)
			}

			if tc.cpuNUMAHintPreferPolicy != "" {
				dynamicPolicy.conf.CPUNUMAHintPreferPolicy = tc.cpuNUMAHintPreferPolicy

				if dynamicPolicy.conf.CPUNUMAHintPreferPolicy == cpuconsts.CPUNUMAHintPreferPolicyDynamicPacking {
					dynamicPolicy.conf.CPUNUMAHintPreferLowThreshold = 0.5
				}
			}

			dynamicPolicy.sharedCoresNUMABindingHintOptimizer, err = canonical.NewCanonicalHintOptimizer(dynamicPolicy.generateHintOptimizerFactoryOptions())
			as.NoError(err)

			dynamicPolicy.dedicatedCoresNUMABindingHintOptimizer = &hintoptimizer.DummyHintOptimizer{}

			resp, err := dynamicPolicy.GetTopologyHints(context.Background(), tc.req)
			as.Nil(err)

			tc.expectedResp.PodUid = tc.req.PodUid
			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.name)

			_ = os.RemoveAll(tmpDir)
		})
	}
}

func TestGetPodTopologyHints(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetPodTopologyHints")
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: "testNamespace",
		PodName:      "testName",
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = dynamicPolicy.GetPodTopologyHints(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareAllocatableResources")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	resp, err := dynamicPolicy.GetTopologyAwareAllocatableResources(context.Background(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	as.Nil(err)

	as.Equal(&pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:   false,
				IsScalarResource: true,
				TopologyAwareAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 3, Node: 0},
					{ResourceValue: 3, Node: 1},
					{ResourceValue: 4, Node: 2},
					{ResourceValue: 4, Node: 3},
				},
				TopologyAwareCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
					{ResourceValue: 4, Node: 0},
					{ResourceValue: 4, Node: 1},
					{ResourceValue: 4, Node: 2},
					{ResourceValue: 4, Node: 3},
				},
				AggregatedAllocatableQuantity: 14,
				AggregatedCapacityQuantity:    16,
			},
		},
	}, resp)
}

func TestGetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.GetTopologyAwareResourcesResponse
		cpuTopology  *machine.CPUTopology
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
			},
			err:         fmt.Errorf("error occurred"),
			cpuTopology: cpuTopology,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
						string(v1.ResourceCPU): {
							IsNodeResource:             false,
							IsScalarResource:           true,
							AggregatedQuantity:         14,
							OriginalAggregatedQuantity: 14,
							TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 3, Node: 0},
								{ResourceValue: 3, Node: 1},
								{ResourceValue: 4, Node: 2},
								{ResourceValue: 4, Node: 3},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 3, Node: 0},
								{ResourceValue: 3, Node: 1},
								{ResourceValue: 4, Node: 2},
								{ResourceValue: 4, Node: 3},
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
						string(v1.ResourceCPU): {
							IsNodeResource:             false,
							IsScalarResource:           true,
							AggregatedQuantity:         4,
							OriginalAggregatedQuantity: 4,
							TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 1, Node: 0},
								{ResourceValue: 1, Node: 1},
								{ResourceValue: 1, Node: 2},
								{ResourceValue: 1, Node: 3},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 1, Node: 0},
								{ResourceValue: 1, Node: 1},
								{ResourceValue: 1, Node: 2},
								{ResourceValue: 1, Node: 3},
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyAwareResources")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
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

		if tc.req.Annotations[consts.PodAnnotationQoSLevelKey] == consts.PodAnnotationQoSLevelSharedCores {
			originalTransitionPeriod := dynamicPolicy.transitionPeriod
			dynamicPolicy.transitionPeriod = time.Millisecond * 10
			time.Sleep(20 * time.Millisecond)
			_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
			dynamicPolicy.transitionPeriod = originalTransitionPeriod
			as.Nil(err)
			allocationInfo := dynamicPolicy.state.GetAllocationInfo(tc.req.PodUid, testName)
			as.NotNil(allocationInfo)
			as.Equal(false, allocationInfo.RampUp)

			resp, err = dynamicPolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        tc.req.PodUid,
				ContainerName: testName,
			})

			as.Nil(err)
			as.NotNil(resp)

			tc.expectedResp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
				string(v1.ResourceCPU): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         10,
					OriginalAggregatedQuantity: 10,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 2, Node: 2},
						{ResourceValue: 2, Node: 3},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 3, Node: 0},
						{ResourceValue: 3, Node: 1},
						{ResourceValue: 2, Node: 2},
						{ResourceValue: 2, Node: 3},
					},
				},
			}

			as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)
		}

		os.Remove(tmpDir)
	}
}

func TestGetResourcesAllocation(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocation")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)
	dynamicPolicy.transitionPeriod = time.Millisecond * 10

	testName := "test"

	// test for shared_cores
	req := &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
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

	reclaim := dynamicPolicy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	as.NotNil(reclaim)

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).Difference(reclaim.AllocationResult).String(),
	}, resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	// test after ramping up
	originalTransitionPeriod := dynamicPolicy.transitionPeriod
	dynamicPolicy.transitionPeriod = time.Millisecond * 10
	time.Sleep(20 * time.Millisecond)
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	dynamicPolicy.transitionPeriod = originalTransitionPeriod
	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)

	resp2, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 7, 8, 9, 10, 11).String(),
	}, resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	// test for reclaimed_cores
	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
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

	resp2, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)

	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 4,
		AllocationResult:  machine.NewCPUSet(12, 13, 14, 15).String(),
	}, resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	req = &pluginapi.ResourceRequest{
		PodUid:         string(uuid.NewUUID()),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	dynamicPolicy.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
	dynamicPolicy.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	dynamicPolicy.state.SetAllocationInfo(commonstate.PoolNameReclaim, "", &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:         machine.MustParse("1,3,4-5"),
		OriginalAllocationResult: machine.MustParse("1,3,4-5"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1),
			1: machine.NewCPUSet(3),
			2: machine.NewCPUSet(4, 5),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1),
			1: machine.NewCPUSet(3),
			2: machine.NewCPUSet(4, 5),
		},
	}, true)
	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// test after ramping up
	originalTransitionPeriod = dynamicPolicy.transitionPeriod
	dynamicPolicy.transitionPeriod = time.Millisecond * 10
	time.Sleep(20 * time.Millisecond)
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	dynamicPolicy.transitionPeriod = originalTransitionPeriod
	allocationInfo = dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)

	resp3, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resp3.PodResources[req.PodUid])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(&pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 14,
		AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15).String(),
	}, resp3.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])

	reclaimEntry := dynamicPolicy.state.GetAllocationInfo(commonstate.PoolNameReclaim, "")
	as.NotNil(reclaimEntry)
	as.Equal(6, reclaimEntry.AllocationResult.Size()) // ceil("14 * (4 / 10)") == 6
}

func TestAllocateByQoSAwareServerListAndWatchResp(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	pod4UID := string(uuid.NewUUID())
	testName := "test"

	fmt.Println("1", pod1UID)
	fmt.Println("2", pod2UID)
	fmt.Println("3", pod3UID)
	fmt.Println("4", pod4UID)

	testCases := []struct {
		name                 string
		podEntries           state.PodEntries
		expectedPodEntries   state.PodEntries
		expectedMachineState state.NUMANodeMap
		expectedNUMAHeadroom map[int]float64
		lwResp               *advisorapi.ListAndWatchResponse
		cpuTopology          *machine.CPUTopology
	}{
		{
			name: "two shared_cores containers and one reclaimed_cores container",
			podEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
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
						AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
						OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
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
						AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
						OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 9),
							1: machine.NewCPUSet(3, 11),
							2: machine.NewCPUSet(4, 5, 11, 12),
							3: machine.NewCPUSet(6, 14),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
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
						AllocationResult:         machine.NewCPUSet(7, 8, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 8, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
							4: machine.NewCPUSet(6),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
							4: machine.NewCPUSet(6),
						},
						RequestQuantity: 2,
					},
				},
			},
			lwResp: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReserve,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f65",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
				},
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{
								string(advisorapi.ControlKnobKeyCPUNUMAHeadroom): `{"0": 10.1, "1": 12.2}`,
							},
						},
					},
				},
			},
			expectedPodEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
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
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
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
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
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
				commonstate.PoolNameReserve: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
						AllocationResult:         machine.MustParse("0,2"),
						OriginalAllocationResult: machine.MustParse("0,2"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
					},
				},
			},
			expectedMachineState: state.NUMANodeMap{
				0: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(0).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
			expectedNUMAHeadroom: map[int]float64{
				0: 10.1,
				1: 12.2,
			},
			cpuTopology: cpuTopology,
		},
		{
			name: "two shared_cores containers and one reclaimed_cores container and one dedicated_cores",
			podEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
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
						AllocationResult:         machine.MustParse("4-5,6-7"),
						OriginalAllocationResult: machine.MustParse("4-5,6-7"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
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
						AllocationResult:         machine.MustParse("4-5,6-7"),
						OriginalAllocationResult: machine.MustParse("4-5,6-7"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
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
						AllocationResult:         machine.NewCPUSet(12, 13, 14, 15),
						OriginalAllocationResult: machine.NewCPUSet(12, 13, 14, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(12, 13),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(12, 13),
							3: machine.NewCPUSet(14, 15),
						},
						RequestQuantity: 2,
					},
				},
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod4UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						RequestQuantity: 2,
					},
				},
			},
			lwResp: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReserve,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f65",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f30",
												Result:  1,
											},
										},
									},
									1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f31",
												Result:  1,
											},
										},
									},
									-1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f32",
												Result:  2,
											},
										},
									},
								},
							},
						},
					},
					pod4UID: {
						Entries: map[string]*advisorapi.CalculationInfo{
							testName: {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd1f20",
												Result:  2,
											},
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f30",
												Result:  1,
											},
										},
									},
									1: {
										Blocks: []*advisorapi.Block{
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd1f21",
												Result:  2,
											},
											{
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f31",
												Result:  1,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedPodEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod1UID,
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
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod2UID,
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
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						RequestQuantity: 2,
					},
				},
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod3UID,
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
						AllocationResult:         machine.MustParse("1,3,14-15"),
						OriginalAllocationResult: machine.MustParse("1,3,14-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						RequestQuantity: 2,
					},
				},
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         pod4UID,
							PodNamespace:   testName,
							PodName:        testName,
							ContainerName:  testName,
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  commonstate.PoolNameDedicated,
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
							},
							QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1, 8, 9),
							1: machine.NewCPUSet(3, 10, 11),
						},
						RequestQuantity: 2,
					},
				},
				commonstate.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.MustParse("1,3,14-15"),
						OriginalAllocationResult: machine.MustParse("1,3,14-15"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(1),
							1: machine.NewCPUSet(3),
							3: machine.NewCPUSet(14, 15),
						},
					},
				},
				commonstate.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						AllocationResult:         machine.MustParse("4,5,6,7,12,13"),
						OriginalAllocationResult: machine.MustParse("4,5,6,7,12,13"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							2: machine.NewCPUSet(4, 5, 12, 13),
							3: machine.NewCPUSet(6, 7),
						},
					},
				},
				commonstate.PoolNameReserve: state.ContainerEntries{
					"": &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
						AllocationResult:         machine.MustParse("0,2"),
						OriginalAllocationResult: machine.MustParse("0,2"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
					},
				},
			},
			expectedMachineState: state.NUMANodeMap{
				0: &state.NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(0),
					AllocatedCPUSet: machine.NewCPUSet(1, 8, 9),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
								AllocationResult:         machine.MustParse("1"),
								OriginalAllocationResult: machine.MustParse("1"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
								},
								RequestQuantity: 2,
							},
						},
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod4UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameDedicated,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
									QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(1, 8, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 8, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								RequestQuantity: 2,
							},
						},
					},
				},
				1: &state.NUMANodeState{
					DefaultCPUSet:   machine.NewCPUSet(2),
					AllocatedCPUSet: machine.NewCPUSet(3, 10, 11),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
								AllocationResult:         machine.MustParse("3"),
								OriginalAllocationResult: machine.MustParse("3"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
								},
								RequestQuantity: 2,
							},
						},
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod4UID,
									PodNamespace:   testName,
									PodName:        testName,
									ContainerName:  testName,
									ContainerType:  pluginapi.ContainerType_MAIN.String(),
									ContainerIndex: 0,
									OwnerPoolName:  commonstate.PoolNameDedicated,
									Labels: map[string]string{
										consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
									},
									Annotations: map[string]string{
										consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
										consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
									},
									QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								RampUp:                   false,
								AllocationResult:         machine.NewCPUSet(3, 10, 11),
								OriginalAllocationResult: machine.NewCPUSet(3, 10, 11),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
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
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
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
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
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
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								RequestQuantity: 2,
							},
						},
						//pod3UID: state.ContainerEntries{
						//	testName: &state.AllocationInfo{
						//		AllocationMeta: commonstate.AllocationMeta{
						//			PodUid:                   pod3UID,
						//			PodNamespace:             testName,
						//			PodName:                  testName,
						//			ContainerName:            testName,
						//			ContainerType:            pluginapi.ContainerType_MAIN.String(),
						//			ContainerIndex:           0,
						//			OwnerPoolName:           commonstate.PoolNameReclaim,
						//			Labels: map[string]string{
						//				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//			},
						//			Annotations: map[string]string{
						//				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//			},
						//			QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
						//		},
						//		RampUp:                   false,
						//		AllocationResult:         machine.MustParse("5,13"),
						//		OriginalAllocationResult: machine.MustParse("5,13"),
						//		TopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		RequestQuantity: 2,
						//	},
						//},
					},
				},
				3: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod1UID,
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
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								RequestQuantity: 2,
							},
						},
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod2UID,
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
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								RequestQuantity: 2,
							},
						},
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								AllocationMeta: commonstate.AllocationMeta{
									PodUid:         pod3UID,
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
								AllocationResult:         machine.MustParse("14,15"),
								OriginalAllocationResult: machine.MustParse("14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateByQoSAwareServerListAndWatchResp")
			as.Nil(err)

			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
			as.Nil(err)

			dynamicPolicy.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true

			machineState, err := generateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries)
			as.Nil(err)

			dynamicPolicy.state.SetPodEntries(tc.podEntries, true)
			dynamicPolicy.state.SetMachineState(machineState, true)
			dynamicPolicy.initReservePool()

			emptyMap := map[string]*advisorsvc.FeatureGate{}
			err = dynamicPolicy.allocateByCPUAdvisor(nil, tc.lwResp, emptyMap)
			as.Nilf(err, "dynamicPolicy.allocateByCPUAdvisorServerListAndWatchResp got err: %v, case: %s", err, tc.name)

			getPodEntries := dynamicPolicy.state.GetPodEntries()
			match, err := entriesMatch(tc.expectedPodEntries, getPodEntries)
			as.Nilf(err, "failed in test case: %s", tc.name)
			as.Equalf(true, match, "failed in test case: %s", tc.name)

			match, err = machineStateMatch(tc.expectedMachineState, dynamicPolicy.state.GetMachineState())
			as.Nilf(err, "failed in test case: %s", tc.name)
			as.Equalf(true, match, "failed in test case: %s", tc.name)

			as.Equalf(tc.expectedNUMAHeadroom, dynamicPolicy.state.GetNUMAHeadroom(),
				"failed in test case: %s", tc.name)

			os.RemoveAll(tmpDir)
		})
	}
}

func machineStateMatch(state1, state2 state.NUMANodeMap) (bool, error) {
	if len(state1) != len(state2) {
		return false, nil
	}

	for numaId, numaState := range state1 {
		if numaState == nil {
			return false, fmt.Errorf("empty allocationInfo")
		}

		if state2[numaId] == nil {
			fmt.Printf("NUMA: %d isn't found in state2\n", numaId)
			return false, nil
		}

		if numaState.DefaultCPUSet.Size() != state2[numaId].DefaultCPUSet.Size() {
			fmt.Printf("NUMA: %d DefaultCPUSet: (state1: %d, state2: %d) not match\n",
				numaId, numaState.DefaultCPUSet.Size(), state2[numaId].DefaultCPUSet.Size())
			return false, nil
		} else if numaState.AllocatedCPUSet.Size() != state2[numaId].AllocatedCPUSet.Size() {
			fmt.Printf("NUMA: %d AllocatedCPUSet: (state1: %d, state2: %d) not match\n",
				numaId, numaState.AllocatedCPUSet.Size(), state2[numaId].AllocatedCPUSet.Size())
			return false, nil
		}
	}

	return true, nil
}

func entriesMatch(entries1, entries2 state.PodEntries) (bool, error) {
	if len(entries1) != len(entries2) {
		return false, nil
	}

	for entryName, subEntries := range entries1 {
		for subEntryName, allocationInfo := range subEntries {
			if allocationInfo == nil {
				return false, fmt.Errorf("empty allocationInfo")
			}

			if entries2[entryName][subEntryName] == nil {
				fmt.Printf("%s:%s isn't found in entries2\n", entryName, subEntryName)
				return false, nil
			}

			if allocationInfo.AllocationResult.Size() != entries2[entryName][subEntryName].AllocationResult.Size() {
				fmt.Printf("%s:%s allocationResult: (entries1: %d, entries2: %d) isn't match\n",
					entryName, subEntryName, allocationInfo.AllocationResult.Size(),
					entries2[entryName][subEntryName].AllocationResult.Size())
				return false, nil
			}

			if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				for numaId, cset := range allocationInfo.TopologyAwareAssignments {
					if cset.Size() != entries2[entryName][subEntryName].TopologyAwareAssignments[numaId].Size() {
						fmt.Printf("%s:%s NUMA: %d allocationResult: (entries1: %d, entries2: %d) not match\n",
							entryName, subEntryName, numaId, cset.Size(),
							entries2[entryName][subEntryName].TopologyAwareAssignments[numaId].Size())
						return false, nil
					}
				}
			}
		}
	}

	return true, nil
}

func TestClearResidualState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestClearResidualState")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.clearResidualState(nil, nil, nil, nil, nil)
}

func TestStart(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStart")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Start()
	defer dynamicPolicy.Stop()
	as.Nil(err)
}

func TestStop(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStop")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Stop()
	as.Nil(err)
}

func TestCheckCPUSet(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "cpu_qrm_plugin_TestCheckCPUSet")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.checkCPUSet(nil, nil, nil, nil, nil)
}

func TestSchedIdle(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	_, err1 := os.Stat("/sys/fs/cgroup/cpu/kubepods/cpu.idle")
	_, err2 := os.Stat("/sys/fs/cgroup/kubepods/cpu.idle")

	support := err1 == nil && err2 == nil

	as.Equalf(support, cgroupcm.IsCPUIdleSupported(), "sched idle support status isn't correct")

	if cgroupcm.IsCPUIdleSupported() {
		absCgroupPath := common.GetAbsCgroupPath("cpu", "test")

		fs := &utilfs.DefaultFs{}
		err := fs.MkdirAll(absCgroupPath, 0o755)

		as.Nil(err)

		var enableCPUIdle bool
		_ = cgroupcmutils.ApplyCPUWithRelativePath("test", &cgroupcm.CPUData{CpuIdlePtr: &enableCPUIdle})

		contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "cpu.idle")) //nolint:gosec
		as.Nil(err)

		as.Equal("1", contents)
	}
}

func TestRemoveContainer(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemoveContainer")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	podUID := string(uuid.NewUUID())
	containerName := "testName"
	testName := "testName"

	podEntries := state.PodEntries{
		podUID: state.ContainerEntries{
			containerName: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:         podUID,
					PodNamespace:   testName,
					PodName:        testName,
					ContainerName:  containerName,
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
				AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
				OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(1, 9),
					1: machine.NewCPUSet(3, 11),
					2: machine.NewCPUSet(4, 5, 11, 12),
					3: machine.NewCPUSet(6, 14),
				},
				RequestQuantity: 2,
			},
		},
	}

	dynamicPolicy.state.SetPodEntries(podEntries, true)

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.NotNil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName, true)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.Nil(allocationInfo)

	dynamicPolicy.removeContainer(podUID, containerName, true)

	allocationInfo = dynamicPolicy.state.GetAllocationInfo(podUID, containerName)
	as.Nil(allocationInfo)
}

func TestShoudSharedCoresRampUp(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestShoudSharedCoresRampUp")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.state.SetAllocationInfo(commonstate.PoolNameShare, "", &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
		OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
	}, true)

	existPodUID := uuid.NewUUID()
	existName := "exist"
	dynamicPolicy.state.SetAllocationInfo(string(existPodUID), existName, &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:         string(existPodUID),
			PodNamespace:   existName,
			PodName:        existName,
			ContainerName:  existName,
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
		AllocationResult:         machine.MustParse("1,3-6,9,11-14"),
		OriginalAllocationResult: machine.MustParse("1,3-6,9,11-14"),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		RequestQuantity: 2,
	}, true)

	testName := "test"
	podUID := uuid.NewUUID()
	dynamicPolicy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{
				PodList: []*v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      testName,
							Namespace: testName,
							UID:       podUID,
						},
						Status: v1.PodStatus{
							Phase: v1.PodRunning,
						},
					},
				},
			},
		},
	}

	req := &pluginapi.ResourceRequest{
		PodUid:         string(podUID),
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(false, allocationInfo.RampUp)
	as.Equal(allocationInfo.OwnerPoolName, commonstate.PoolNameShare)
}

func BenchmarkGetTopologyHints(b *testing.B) {
	klog.SetOutput(ioutil.Discard)
	klog.V(0)
	klog.LogToStderr(false)
	cpuCases := []cpuTestCase{
		{
			cpuNum:      96,
			socketNum:   2,
			numaNum:     4,
			fakeNUMANum: 4,
		},
		//{
		//	cpuNum:      128,
		//	socketNum:   2,
		//	numaNum:     4,
		//	fakeNUMANum: 4,
		//},
		//{
		//	cpuNum:      192,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 8,
		//},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 8,
		},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 16,
		},
		{
			cpuNum:      384,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 24,
		},
		//{
		//	cpuNum:      512,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 8,
		//},
		//{
		//	cpuNum:      512,
		//	socketNum:   2,
		//	numaNum:     8,
		//	fakeNUMANum: 16,
		//},
		{
			cpuNum:      512,
			socketNum:   2,
			numaNum:     8,
			fakeNUMANum: 32,
		},
	}

	testName := "test"

	req := &pluginapi.ResourceRequest{
		PodUid:           string(uuid.NewUUID()),
		PodNamespace:     testName,
		PodName:          testName,
		ContainerName:    testName,
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	for _, tc := range cpuCases {
		tmpDir, _ := ioutil.TempDir("", "checkpoint-BenchmarkGetTopologyHints")

		cpuTopology, _ := machine.GenerateDummyCPUTopology(tc.cpuNum, tc.socketNum, tc.fakeNUMANum)

		cpusPerNUMA := cpuTopology.NumCPUs / cpuTopology.NumNUMANodes

		dynamicPolicy, _ := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)

		for _, numaNeeded := range []int{1, 2, 4} {
			req.ResourceRequests[string(v1.ResourceCPU)] = float64(numaNeeded*cpusPerNUMA - 1)
			req.Annotations = map[string]string{
				consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
				consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
			}
			req.Labels = map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			}

			b.Run(fmt.Sprintf("%d cpus, %d sockets, %d NUMAs, %d fake-NUMAs, %d NUMAs package",
				tc.cpuNum, tc.socketNum, tc.numaNum, tc.fakeNUMANum, numaNeeded), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					dynamicPolicy.GetTopologyHints(context.Background(), req)
				}
			})

		}

		_ = os.RemoveAll(tmpDir)
	}
}

func TestSNBAdmitWithSidecarReallocate(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBAdmitWithSidecarReallocate")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	sidecarName := "sidecar"
	podUID := string(uuid.NewUUID())
	// admit sidecar container
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  sidecarName,
		ContainerType:  pluginapi.ContainerType_SIDECAR,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 8}`,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), sidecarReq)
	as.Nil(err)
	as.Nil(res.ResourceHints[string(v1.ResourceCPU)])

	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)

	// admit main container
	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 1,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 6,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 8}`,
		},
	}

	res, err = dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	hints := res.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))
	req.Hint = hints[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// we cannot GetResourceAllocation here, it will
	// not sidecar container is wait for reallocate, only main container is in state file
	sidecarAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, sidecarName)
	as.Nil(sidecarAllocation)

	mainAllocation := dynamicPolicy.state.GetAllocationInfo(podUID, testName)
	as.NotNil(mainAllocation)

	// another container before reallocate with 4 core (because share-NUMA0 has only 11 core and 6 cores is allocated to podUID/test).
	anotherReq := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 4,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 4}`,
		},
	}

	// pod aggregated size is 8, the new container request is 4, 8 + 4 > 11 (share-NUMA0 size)
	_, err = dynamicPolicy.GetTopologyHints(context.Background(), anotherReq)
	as.ErrorContains(err, cpuutil.ErrNoAvailableCPUHints.Error())

	// reallocate sidecar
	_, err = dynamicPolicy.Allocate(context.Background(), sidecarReq)
	as.Nil(err)
	sidecarAllocation = dynamicPolicy.state.GetAllocationInfo(podUID, sidecarName)
	as.NotNil(sidecarAllocation)
}

func TestSNBCpuRequestWithFloat(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSNBCpuRequestWithFloat")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(12, 1, 1)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.podAnnotationKeptKeys = []string{
		consts.PodAnnotationMemoryEnhancementNumaBinding,
		consts.PodAnnotationInplaceUpdateResizingKey,
		consts.PodAnnotationAggregatedRequestsKey,
	}

	testName := "test"
	podUID := string(uuid.NewUUID())

	req := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test1",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 9.5,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 9.5}`,
		},
	}

	res, err := dynamicPolicy.GetTopologyHints(context.Background(), req)
	as.Nil(err)
	hints := res.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints))
	req.Hint = hints[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req)
	as.Nil(err)

	// admit another pod with 0.5c
	req2 := &pluginapi.ResourceRequest{
		PodUid:         podUID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  "test2",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 0.5,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey:  `{"numa_binding": "true", "numa_exclusive": "false"}`,
			consts.PodAnnotationAggregatedRequestsKey: `{"cpu": 0.5}`,
		},
	}

	res2, err := dynamicPolicy.GetTopologyHints(context.Background(), req2)
	as.Nil(err)
	hints2 := res2.ResourceHints[string(v1.ResourceCPU)].Hints
	as.NotZero(len(hints2))
	req2.Hint = hints2[0]

	_, err = dynamicPolicy.Allocate(context.Background(), req2)
	as.Nil(err)
}

type mockCPUAdvisor struct {
	mock.Mock
	advisorapi.CPUAdvisorServer
}

func (m *mockCPUAdvisor) GetAdvice(ctx context.Context, in *advisorapi.GetAdviceRequest) (*advisorapi.GetAdviceResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*advisorapi.GetAdviceResponse), args.Error(1)
}

func (m *mockCPUAdvisor) ListAndWatch(in *advisorsvc.Empty, srv advisorapi.CPUAdvisor_ListAndWatchServer) error {
	args := m.Called(in, srv)
	return args.Error(0)
}

func (m *mockCPUAdvisor) AddContainer(ctx context.Context, req *advisorsvc.ContainerMetadata) (*advisorsvc.AddContainerResponse, error) {
	return &advisorsvc.AddContainerResponse{}, nil
}

func (m *mockCPUAdvisor) RemovePod(ctx context.Context, req *advisorsvc.RemovePodRequest) (*advisorsvc.RemovePodResponse, error) {
	return &advisorsvc.RemovePodResponse{}, nil
}

func enableAdvisorForTest(dynamicPolicy *DynamicPolicy, pluginSocketDir string) {
	dynamicPolicy.enableCPUAdvisor = true
	dynamicPolicy.getAdviceInterval = 1 * time.Second

	dynamicPolicy.cpuPluginSocketAbsPath = filepath.Join(pluginSocketDir, "cpu-plugin.sock")
	dynamicPolicy.cpuAdvisorSocketAbsPath = filepath.Join(pluginSocketDir, "cpu-advisor.sock")
}

// TestSwitchToGetAdviceRPC tests the cpu plugin can switch to GetAdvice RPC when cpu-advisor implements it,
// and switch back to ListAndWatch when cpu-advisor doesn't implement GetAdvice.
func TestSwitchBetweenAPIs(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		fn   func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy)
	}{
		{
			name: "switch from ListAndWatch to GetAdvice",
			fn: func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy) {
				// - start cpu-advisor that doesn't implement GetAdvice
				// - start cpu plugin and verify that it uses ListAndWatch
				// - 'upgrade' cpu-advisor to implement GetAdvice
				// - verify that cpu plugin switches to GetAdvice
				as := require.New(t)

				lwEndedChan := make(chan time.Time)
				unimplementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Once().
					Return((*advisorapi.GetAdviceResponse)(nil), status.Error(codes.Unimplemented, "GetAdvice not implemented"))
				cpuAdvisorServer.On("ListAndWatch", mock.Anything, mock.Anything).
					Once().
					WaitUntil(lwEndedChan).
					Return(nil).
					NotBefore(unimplementedGetAdviceCall)

				err := dynamicPolicy.Start()
				as.NoError(err)
				defer dynamicPolicy.Stop()

				// Wait for the plugin to call advisor
				time.Sleep(3 * time.Second)
				cpuAdvisorServer.AssertExpectations(t)

				// ListAndWatch in progress, simulate an upgrade
				unimplementedGetAdviceCall.Unset()

				getAdviceCalledChan := make(chan struct{})
				cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Run(func(args mock.Arguments) {
						getAdviceCalledChan <- struct{}{}
					}).
					Return(&advisorapi.GetAdviceResponse{}, nil)

				// terminate the old ListAndWatch
				lwEndedChan <- time.Now()

				// plugin should switch to GetAdvice and call it periodically
				for i := 0; i < 5; i++ {
					select {
					case <-getAdviceCalledChan:
					case <-time.After(dynamicPolicy.getAdviceInterval * 2):
						t.Fatalf("GetAdvice not called")
					}
				}
				// ListAndWatch should only be called once throughout
				cpuAdvisorServer.AssertExpectations(t)
			},
		},
		{
			name: "switch from GetAdvice to ListAndWatch",
			fn: func(t *testing.T, cpuAdvisorServer *mockCPUAdvisor, dynamicPolicy *DynamicPolicy) {
				// - start cpu-advisor that implements GetAdvice
				// - start cpu plugin and verify that it uses GetAdvice
				// - 'downgrade' cpu-advisor to a version that doesn't implement GetAdvice
				// - verify that cpu plugin switches to ListAndWatch
				as := require.New(t)

				getAdviceCalledChan := make(chan struct{})
				implementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Run(func(args mock.Arguments) {
						select {
						case getAdviceCalledChan <- struct{}{}:
						default:
						}
					}).
					Return(&advisorapi.GetAdviceResponse{}, nil)

				err := dynamicPolicy.Start()
				as.NoError(err)
				defer dynamicPolicy.Stop()

				// Ensure GetAdvice is called periodically
				for i := 0; i < 5; i++ {
					select {
					case <-getAdviceCalledChan:
					case <-time.After(dynamicPolicy.getAdviceInterval * 2):
						t.Fatalf("GetAdvice not called")
					}
				}
				cpuAdvisorServer.AssertExpectations(t)

				// simulate a downgrade
				// lock to ensure that policy doesn't call GetAdvice after we have removed the old expectation but haven't set the new one
				dynamicPolicy.Lock()
				implementedGetAdviceCall.Unset()
				unimplementedGetAdviceCall := cpuAdvisorServer.
					On("GetAdvice", mock.Anything, mock.Anything).
					Once().
					Return((*advisorapi.GetAdviceResponse)(nil), status.Error(codes.Unimplemented, "GetAdvice not implemented"))
				cpuAdvisorServer.On("ListAndWatch", mock.Anything, mock.Anything).
					After(3 * time.Hour).
					NotBefore(unimplementedGetAdviceCall).
					Once().
					Return(nil)
				dynamicPolicy.Unlock()

				time.Sleep(3 * time.Second)
				cpuAdvisorServer.AssertExpectations(t)
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			as := require.New(t)
			tmpDir, err := ioutil.TempDir("", "*")
			as.NoError(err)
			defer func() { _ = os.RemoveAll(tmpDir) }()

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.NoError(err)

			checkpointDir := filepath.Join(tmpDir, "checkpoint")
			dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, checkpointDir)
			as.NoError(err)
			podEntries := state.PodEntries{
				"373d08e4-7a6b-4293-aaaf-b135ff812aaa": state.ContainerEntries{
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   "default",
							PodName:        "test",
							ContainerName:  "main",
							ContainerType:  pluginapi.ContainerType_MAIN.String(),
							ContainerIndex: 0,
							OwnerPoolName:  "share-NUMA1",
							Labels: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelSharedCores,
								consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								cpuconsts.CPUStateAnnotationKeyNUMAHint:          "1",
							},
							QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
						},
						RampUp:                   false,
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
					"sidecar": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:         "373d08e4-7a6b-4293-aaaf-b135ff812aaa",
							PodNamespace:   "default",
							PodName:        "test",
							ContainerName:  "sidecar",
							ContainerType:  pluginapi.ContainerType_SIDECAR.String(),
							ContainerIndex: 1,
							OwnerPoolName:  commonstate.EmptyOwnerPoolName,
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
						AllocationResult:         machine.MustParse("3,10"),
						OriginalAllocationResult: machine.MustParse("3,10"),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(3, 10),
						},
						RequestQuantity: 2,
					},
				},
			}
			machineState, err := generateMachineStateFromPodEntries(cpuTopology, podEntries)
			as.Nil(err)

			dynamicPolicy.state.SetPodEntries(podEntries, true)
			dynamicPolicy.state.SetMachineState(machineState, true)

			enableAdvisorForTest(dynamicPolicy, tmpDir)

			grpcServer := grpc.NewServer()
			cpuAdvisorServer := &mockCPUAdvisor{}
			advisorapi.RegisterCPUAdvisorServer(grpcServer, cpuAdvisorServer)
			lis, err := net.Listen("unix", dynamicPolicy.cpuAdvisorSocketAbsPath)
			as.NoError(err)
			defer func() { _ = lis.Close() }()
			go func() { _ = grpcServer.Serve(lis) }()
			defer grpcServer.Stop()

			testCase.fn(t, cpuAdvisorServer, dynamicPolicy)
		})
	}
}

func generateTestConfiguration(t *testing.T, nodeName string, dir string) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.NodeName = nodeName
	testConfiguration.CheckpointManagerDir = dir
	return testConfiguration
}

func TestNewDynamicPolicy(t *testing.T) {
	t.Parallel()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)
	conf := config.NewConfiguration()
	kccConf := generateTestConfiguration(t, "test", "/tmp/metaserver1/TestNewDynamicConfigManager")
	kccMgr, _ := kcc.NewDynamicConfigManager(nil, nil, nil, kccConf)
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
		ConfigurationManager: kccMgr,
	}
	testingDir, err := ioutil.TempDir("", "dynamic_policy_new")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testingDir)
	conf.GenericQRMPluginConfiguration.StateFileDirectory = testingDir
	type args struct {
		agentCtx  *componentagent.GenericContext
		conf      *config.Configuration
		in2       interface{}
		agentName string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				agentCtx: &componentagent.GenericContext{
					GenericContext: &katalystbase.GenericContext{
						EmitterPool: metricspool.DummyMetricsEmitterPool{},
					},
					MetaServer: metaServer,
				},
				conf:      conf,
				agentName: "test",
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := NewDynamicPolicy(tt.args.agentCtx, tt.args.conf, nil, tt.args.agentName)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDynamicPolicy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NewDynamicPolicy() got = %v, want %v", got, tt.want)
			}
		})
	}
}
