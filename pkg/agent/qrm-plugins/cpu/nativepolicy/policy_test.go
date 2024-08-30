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

package nativepolicy

import (
	"context"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	podDebugAnnoKey = "qrm.katalyst.kubewharf.io/debug_pod"
)

func getTestNativePolicy(topology *machine.CPUTopology, stateFileDirectory string) (*NativePolicy, error) {
	stateImpl, err := state.NewCheckpointState(stateFileDirectory, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameNative, topology, false)
	if err != nil {
		return nil, err
	}

	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: topology,
	}

	dynamicConfig := dynamic.NewDynamicAgentConfiguration()

	policyImplement := &NativePolicy{
		machineInfo:      machineInfo,
		emitter:          metrics.DummyMetrics{},
		residualHitMap:   make(map[string]int64),
		cpusToReuse:      make(map[string]machine.CPUSet),
		state:            stateImpl,
		dynamicConfig:    dynamicConfig,
		podDebugAnnoKeys: []string{podDebugAnnoKey},
		reservedCPUs:     machine.NewCPUSet(),
	}

	return policyImplement, nil
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestRemovePod")
	as.Nil(err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	nativePolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
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
		Labels:         map[string]string{},
		Annotations:    map[string]string{},
		NativeQosClass: string(v1.PodQOSGuaranteed),
	}

	_, err = nativePolicy.Allocate(context.Background(), req)
	as.Nil(err)

	resp, err := nativePolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.Nil(err)

	expetced := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       req.PodUid,
		PodNamespace: testName,
		PodName:      testName,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: testName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				string(v1.ResourceCPU): {
					IsNodeResource:             false,
					IsScalarResource:           true,
					AggregatedQuantity:         2,
					OriginalAggregatedQuantity: 2,
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 2, Node: 0},
					},
					OriginalTopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{ResourceValue: 2, Node: 0},
					},
				},
			},
		},
	}

	as.Equal(expetced, resp)

	_, _ = nativePolicy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{
		PodUid: req.PodUid,
	})

	_, err = nativePolicy.GetTopologyAwareResources(context.Background(), &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        req.PodUid,
		ContainerName: testName,
	})
	as.NotNil(err)
	as.True(strings.Contains(err.Error(), "is not show up in cpu plugin state"))
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

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
				NativeQosClass: string(v1.PodQOSGuaranteed),
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceCPU): nil,
				},
				NativeQosClass: string(v1.PodQOSGuaranteed),
			},
			cpuTopology: cpuTopology,
		},
		{
			description: "req for guaranteed QoS and integer cores main container",
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
				NativeQosClass: string(v1.PodQOSGuaranteed),
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
								Nodes:     []uint64{0, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 2},
								Preferred: false,
							},
							{
								Nodes:     []uint64{1, 3},
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
				NativeQosClass: string(v1.PodQOSGuaranteed),
			},
			cpuTopology: cpuTopology,
		},
		{
			description: "req for shared pool contaienr",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 1.5,
				},
				NativeQosClass: string(v1.PodQOSGuaranteed),
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
				NativeQosClass: string(v1.PodQOSGuaranteed),
			},
			cpuTopology: cpuTopology,
		},
	}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetTopologyHints")
		as.Nil(err)

		nativePolicy, err := getTestNativePolicy(tc.cpuTopology, tmpDir)
		as.Nil(err)

		resp, err := nativePolicy.GetTopologyHints(context.Background(), tc.req)
		as.Nil(err)

		tc.expectedResp.PodUid = tc.req.PodUid
		as.Equalf(tc.expectedResp, resp, "failed in test case: %s", tc.description)

		_ = os.RemoveAll(tmpDir)
	}
}

func TestGetPodTopologyHints(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetPodTopologyHints")
	as.Nil(err)

	nativePolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
	as.Nil(err)

	testName := "testPod"
	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: testName,
		PodName:      testName,
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = nativePolicy.GetPodTopologyHints(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestAllocateForPod(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateForPod")
	as.Nil(err)

	nativePolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
	as.Nil(err)

	testName := "testPod"
	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: testName,
		PodName:      testName,
		ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
	}
	_, err = nativePolicy.AllocateForPod(context.Background(), req)
	as.ErrorIs(err, util.ErrNotImplemented)

	_ = os.RemoveAll(tmpDir)
}

func TestGetReadonlyState(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	readonlyState, err := GetReadonlyState()
	as.NotNil(err)
	as.Nil(readonlyState)
}

func TestClearResidualState(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestClearResidualState")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
	as.Nil(err)

	dynamicPolicy.clearResidualState()
}

func TestStart(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	tmpDir, err := ioutil.TempDir("", "checkpoint_TestStart")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	dynamicPolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Start()
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

	dynamicPolicy, err := getTestNativePolicy(cpuTopology, tmpDir)
	as.Nil(err)

	err = dynamicPolicy.Stop()
	as.Nil(err)
}
