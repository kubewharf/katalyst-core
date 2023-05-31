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
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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
	reclaimedResourceConfig := adminqos.NewDynamicReclaimedResourceConfiguration()

	policyImplement := &DynamicPolicy{
		machineInfo:             machineInfo,
		qosConfig:               qosConfig,
		reclaimedResourceConfig: reclaimedResourceConfig,
		state:                   stateImpl,
		advisorValidator:        validator.NewCPUAdvisorValidator(stateImpl, machineInfo),
		reservedCPUs:            reservedCPUs,
		emitter:                 metrics.DummyMetrics{},
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
		description              string
		req                      *pluginapi.ResourceRequest
		expectedResp             *pluginapi.ResourceAllocationResponse
		cpuTopology              *machine.CPUTopology
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
			description: "req for dedicated_cores with numa_binding & numa_exclusive main container",
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
			description: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
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
			description: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
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
			description: "req for dedicated_cores with numa_binding without default numa_exclusive main container",
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
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
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

	testName := "test"

	testCases := []struct {
		description              string
		req                      *pluginapi.ResourceRequest
		expectedResp             *pluginapi.ResourceHintsResponse
		cpuTopology              *machine.CPUTopology
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
			description: "req for dedicated_cores with numa_binding & numa_exclusive main container",
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
			description: "req for dedicated_cores with numa_binding & not numa_exclusive main container",
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
			description: "req for dedicated_cores with numa_binding & default numa_exclusive true main container",
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
			description: "req for dedicated_cores with numa_binding & without default numa_exclusive main container",
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
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint")
		as.Nil(err)

		dynamicPolicy, err := getTestDynamicPolicyWithInitialization(tc.cpuTopology, tmpDir)
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
	pod4UID := string(uuid.NewUUID())
	testName := "test"

	fmt.Println("1", pod1UID)
	fmt.Println("2", pod2UID)
	fmt.Println("3", pod3UID)
	fmt.Println("4", pod4UID)

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
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					state.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReserve,
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
		{
			description: "two shared_cores containers and one reclaimed_cores container and one dedicated_cores",
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
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod4UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameDedicated,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
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
												BlockId: "1b7f4ee0-65af-41a4-bb38-5f1268fd2f66",
												Result:  6,
											},
										},
									},
								},
							},
						},
					},
					state.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReserve,
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
					state.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: state.PoolNameReclaim,
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
								OwnerPoolName: state.PoolNameDedicated,
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
						PodUid:                   pod1UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameShare,
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
				pod4UID: state.ContainerEntries{
					testName: &state.AllocationInfo{
						PodUid:                   pod4UID,
						PodNamespace:             testName,
						PodName:                  testName,
						ContainerName:            testName,
						ContainerType:            pluginapi.ContainerType_MAIN.String(),
						ContainerIndex:           0,
						RampUp:                   false,
						OwnerPoolName:            state.PoolNameDedicated,
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
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
						},
						QoSLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
						RequestQuantity: 2,
					},
				},
				state.PoolNameReclaim: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameReclaim,
						OwnerPoolName:            state.PoolNameReclaim,
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
				state.PoolNameShare: state.ContainerEntries{
					"": &state.AllocationInfo{
						PodUid:                   state.PoolNameShare,
						OwnerPoolName:            state.PoolNameShare,
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
					DefaultCPUSet:   machine.NewCPUSet(0),
					AllocatedCPUSet: machine.NewCPUSet(1, 8, 9),
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
								AllocationResult:         machine.MustParse("1"),
								OriginalAllocationResult: machine.MustParse("1"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1),
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
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod4UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameDedicated,
								AllocationResult:         machine.NewCPUSet(1, 8, 9),
								OriginalAllocationResult: machine.NewCPUSet(1, 8, 9),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(1, 8, 9),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
									consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
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
								PodUid:                   pod3UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameReclaim,
								AllocationResult:         machine.MustParse("3"),
								OriginalAllocationResult: machine.MustParse("3"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									0: machine.NewCPUSet(3),
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
						pod4UID: state.ContainerEntries{
							testName: &state.AllocationInfo{
								PodUid:                   pod4UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameDedicated,
								AllocationResult:         machine.NewCPUSet(3, 10, 11),
								OriginalAllocationResult: machine.NewCPUSet(3, 10, 11),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									1: machine.NewCPUSet(3, 10, 11),
								},
								Labels: map[string]string{
									consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
								},
								Annotations: map[string]string{
									consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
									consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
								},
								QoSLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
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
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
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
								AllocationResult:         machine.MustParse("4-5,12-13"),
								OriginalAllocationResult: machine.MustParse("4-5,12-13"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									2: machine.NewCPUSet(4, 5, 12, 13),
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
						//pod3UID: state.ContainerEntries{
						//	testName: &state.AllocationInfo{
						//		PodUid:                   pod3UID,
						//		PodNamespace:             testName,
						//		PodName:                  testName,
						//		ContainerName:            testName,
						//		ContainerType:            pluginapi.ContainerType_MAIN.String(),
						//		ContainerIndex:           0,
						//		RampUp:                   false,
						//		OwnerPoolName:            state.PoolNameReclaim,
						//		AllocationResult:         machine.MustParse("5,13"),
						//		OriginalAllocationResult: machine.MustParse("5,13"),
						//		TopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						//			2: machine.NewCPUSet(5, 13),
						//		},
						//		Labels: map[string]string{
						//			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//		},
						//		Annotations: map[string]string{
						//			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
						//		},
						//		QoSLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
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
								PodUid:                   pod1UID,
								PodNamespace:             testName,
								PodName:                  testName,
								ContainerName:            testName,
								ContainerType:            pluginapi.ContainerType_MAIN.String(),
								ContainerIndex:           0,
								RampUp:                   false,
								OwnerPoolName:            state.PoolNameShare,
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
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
								AllocationResult:         machine.MustParse("6,7"),
								OriginalAllocationResult: machine.MustParse("6,7"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(6, 7),
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
								AllocationResult:         machine.MustParse("14,15"),
								OriginalAllocationResult: machine.MustParse("14,15"),
								TopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
								},
								OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
									3: machine.NewCPUSet(14, 15),
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

		dynamicPolicy.reclaimedResourceConfig.SetEnableReclaim(true)

		machineState, err := state.GenerateMachineStateFromPodEntries(tc.cpuTopology, tc.podEntries)
		as.Nil(err)

		dynamicPolicy.state.SetPodEntries(tc.podEntries)
		dynamicPolicy.state.SetMachineState(machineState)
		dynamicPolicy.initReservePool()

		err = dynamicPolicy.allocateByCPUAdvisor(tc.lwResp)
		as.Nilf(err, "dynamicPolicy.allocateByCPUAdvisorServerListAndWatchResp got err: %v, case: %s", err, tc.description)

		match, err := entriesMatch(tc.expectedPodEntries, dynamicPolicy.state.GetPodEntries())
		as.Nilf(err, "failed in test case: %s", tc.description)
		as.Equalf(true, match, "failed in test case: %s", tc.description)

		match, err = machineStateMatch(tc.expectedMachineState, dynamicPolicy.state.GetMachineState())
		as.Nilf(err, "failed in test case: %s", tc.description)
		as.Equalf(true, match, "failed in test case: %s", tc.description)

		os.RemoveAll(tmpDir)
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

func TestSchedIdle(t *testing.T) {
	as := require.New(t)

	_, err1 := os.Stat("/sys/fs/cgroup/cpu/kubepods/cpu.idle")
	_, err2 := os.Stat("/sys/fs/cgroup/kubepods/cpu.idle")

	support := err1 == nil && err2 == nil

	as.Equalf(support, cgroupcm.IsCPUIdleSupported(), "sched idle support status isn't correct")

	if cgroupcm.IsCPUIdleSupported() {
		absCgroupPath := common.GetAbsCgroupPath("cpu", "test")

		fs := &utilfs.DefaultFs{}
		err := fs.MkdirAll(absCgroupPath, 0755)

		as.Nil(err)

		var enableCPUIdle bool
		cgroupcmutils.ApplyCPUWithRelativePath("test", &cgroupcm.CPUData{CpuIdlePtr: &enableCPUIdle})

		contents, err := ioutil.ReadFile(filepath.Join(absCgroupPath, "cpu.idle")) //nolint:gosec
		as.Nil(err)

		as.Equal("1", contents)
	}
}
