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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/stretchr/testify/require"
)

func getTestDynamicPolicyWithInitialization(topology *machine.CPUTopology, stateFileDirectory string) (*DynamicPolicy, error) {
	dynamicPolicy, err := getTestDynamicPolicyWithoutInitialization(topology, stateFileDirectory)
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
	stateImpl, err := state.NewCheckpointState(stateFileDirectory, cpuPluginStateFileName, CPUResourcePluginPolicyNameDynamic, topology, false)
	if err != nil {
		return nil, err
	}

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: topology,
	}

	reservedCPUs, _, err := calculator.TakeHTByNUMABalance(machineInfo, machineInfo.CPUDetails.CPUs().Clone(), 2)
	if err != nil {
		return nil, err
	}

	qosConfig := generic.NewQoSConfiguration()

	policyImplement := &DynamicPolicy{
		machineInfo:  machineInfo,
		qosConfig:    qosConfig,
		state:        stateImpl,
		reservedCPUs: reservedCPUs,
		emitter:      metrics.DummyMetrics{},
	}

	state.GetContainerRequestedCores = policyImplement.getContainerRequestedCores

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
	}

	return policyImplement, nil
}

func TestInitPoolAndCalculator(t *testing.T) {
	as := require.New(t)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	policyImpl, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = policyImpl.initReclaimPool()
	as.Nil(err)

	reclaimPoolAllocationInfo := policyImpl.state.GetAllocationInfo(state.PoolNameReclaim, "")

	as.NotNil(reclaimPoolAllocationInfo)

	as.Equal(reclaimPoolAllocationInfo.AllocationResult.Size(), reservedReclaimedCPUsSize)
}

func TestRemovePod(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

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

	dynamicPolicy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
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
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceAllocationResponse
		cpuTopology  *machine.CPUTopology
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
							AllocatedQuantity: 14, //ramp up
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
							AllocationResult:  machine.NewCPUSet(1, 3, 9, 11).String(),
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
			description: "req for dedicated_cores with numa_binding main container",
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
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
		as.Nil(err)

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

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceHintsResponse
		cpuTopology  *machine.CPUTopology
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
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
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
			description: "req for dedicated_cores with numa_binding main container",
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
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: "true",
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
		as.Nil(err)

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
								{ResourceValue: 2, Node: 0},
								{ResourceValue: 2, Node: 1},
							},
							OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
								{ResourceValue: 2, Node: 0},
								{ResourceValue: 2, Node: 1},
							},
						},
					},
				},
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
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
			originalTransitionPeriod := transitionPeriod
			transitionPeriod = time.Second
			time.Sleep(2 * time.Second)
			_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
			transitionPeriod = originalTransitionPeriod
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
						{ResourceValue: 2, Node: 0},
						{ResourceValue: 2, Node: 1},
						{ResourceValue: 4, Node: 2},
						{ResourceValue: 2, Node: 3},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 2, Node: 0},
						{ResourceValue: 2, Node: 1},
						{ResourceValue: 4, Node: 2},
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
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
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

	as.NotNil(resp1.PodResources[req.PodUid])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(resp1.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 14,
		AllocationResult:  cpuTopology.CPUDetails.CPUs().Difference(dynamicPolicy.reservedCPUs).String(),
	})

	// test after ramping up
	originalTransitionPeriod := transitionPeriod
	transitionPeriod = time.Second
	time.Sleep(2 * time.Second)
	_, err = dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	transitionPeriod = originalTransitionPeriod
	allocationInfo := dynamicPolicy.state.GetAllocationInfo(req.PodUid, testName)
	as.NotNil(allocationInfo)
	as.Equal(allocationInfo.RampUp, false)

	resp2, err := dynamicPolicy.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	as.Nil(err)
	as.NotNil(resp2.PodResources[req.PodUid])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName])
	as.NotNil(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)])
	as.Equal(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 10,
		AllocationResult:  machine.NewCPUSet(1, 3, 4, 5, 6, 9, 11, 12, 13, 14).String(),
	})

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
	as.Equal(resp2.PodResources[req.PodUid].ContainerResources[testName].ResourceAllocation[string(v1.ResourceCPU)], &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
		IsNodeResource:    false,
		IsScalarResource:  true,
		AllocatedQuantity: 4,
		AllocationResult:  machine.NewCPUSet(7, 8, 10, 15).String(),
	})
}

func TestAllocateByQoSAwareServerListAndWatchResp(t *testing.T) {
	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	pod1UID := string(uuid.NewUUID())
	pod2UID := string(uuid.NewUUID())
	pod3UID := string(uuid.NewUUID())
	testName := "test"

	testCases := []struct {
		description          string
		podEntries           state.PodEntries
		expectedPodEntries   state.PodEntries
		expectedMachineState state.NUMANodeMap
		lwResp               *advisorapi.ListAndWatchResponse
		cpuTopology          *machine.CPUTopology
	}{
		{
			description: "two shared_cores containers and one reclaimed_cores container",
			podEntries: state.PodEntries{
				pod1UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameShare,
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
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod2UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameShare,
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
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod3UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameReclaim,
						AllocationResult:         machine.NewCPUSet(7, 8, 10, 15),
						OriginalAllocationResult: machine.NewCPUSet(7, 8, 10, 15),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(8),
							1: machine.NewCPUSet(10),
							3: machine.NewCPUSet(7, 15),
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
			lwResp: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					state.PoolNameShare: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameShare,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {
										Blocks: []*advisorapi.Block{
											{
												Result: 6,
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
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameShare,
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
				pod2UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod2UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameShare,
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
				pod3UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod3UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameReclaim,
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
				state.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReclaim,
						OwnerPoolName:            state.PoolNameReclaim,
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
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
				state.PoolNameReserve: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReserve,
						OwnerPoolName:            state.PoolNameReserve,
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
								PodUid:                   pod1UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod2UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod3UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameReclaim,
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
				1: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(1).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod1UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod2UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod3UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameReclaim,
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
				2: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(2).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod1UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod1UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod2UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod2UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
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
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod3UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameReclaim,
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
				3: &state.NUMANodeState{
					DefaultCPUSet:   cpuTopology.CPUDetails.CPUsInNUMANodes(3).Clone(),
					AllocatedCPUSet: machine.NewCPUSet(),
					PodEntries: state.PodEntries{
						pod3UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod3UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameReclaim,
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
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
		as.Nil(err)

		dynamicPolicy.enableReclaim = true

		machineState, err := state.GenerateCPUMachineStateByPodEntries(tc.cpuTopology, tc.podEntries)
		as.Nil(err)

		dynamicPolicy.state.SetPodEntries(tc.podEntries)
		dynamicPolicy.state.SetMachineState(machineState)

		err = dynamicPolicy.allocateByCPUAdvisorServerListAndWatchResp(tc.lwResp)
		as.Nilf(err, "dynamicPolicy.allocateByCPUAdvisorServerListAndWatchResp got err: %v", err)

		bs1, _ := json.Marshal(dynamicPolicy.state.GetMachineState())
		bs2, _ := json.Marshal(tc.expectedMachineState)
		fmt.Println(string(bs1))
		fmt.Println(string(bs2))
		as.Equalf(tc.expectedPodEntries, dynamicPolicy.state.GetPodEntries(), "failed in test case: %s", tc.description)
		as.Equalf(tc.expectedMachineState, dynamicPolicy.state.GetMachineState(), "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
	}
}

func TestGetReadonlyState(t *testing.T) {
	as := require.New(t)
	readonlyState, err := GetReadonlyState()
	as.NotNil(err)
	as.Nil(readonlyState)
}

func TestClearResidualState(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.clearResidualState()
}

func TestStart(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Start()
	as.Nil(err)
}

func TestStop(t *testing.T) {
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
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
	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.checkCPUSet()
}
