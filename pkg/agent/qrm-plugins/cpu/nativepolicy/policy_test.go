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
	"errors"
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
	nativepolicyutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/nativepolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type nativeResidualCommitFailureState struct {
	state.State
	attempts int
	err      error
}

type nativeAllocationCommitFailureState struct {
	state.State
	err      error
	attempts int
}

func (s *nativeAllocationCommitFailureState) CommitAdvisorState(
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.attempts++
	return s.err
}

func (s *nativeAllocationCommitFailureState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.attempts++
	return s.err
}

func (s *nativeResidualCommitFailureState) StoreState() error {
	s.attempts++
	if s.attempts == 1 {
		return s.err
	}
	return s.State.StoreState()
}

func (s *nativeResidualCommitFailureState) CommitAdvisorState(
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.attempts++
	if s.attempts == 1 {
		return s.err
	}
	return s.State.CommitAdvisorState(
		podEntries,
		machineState,
		allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores,
		persist,
		permits...,
	)
}

const (
	podDebugAnnoKey = "qrm.katalyst.kubewharf.io/debug_pod"
)

func getTestNativePolicy(topology *machine.CPUTopology, stateFileDirectory string) (*NativePolicy, error) {
	stateDirectoryConfig := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: stateFileDirectory,
	}
	stateImpl, err := state.NewCheckpointState(stateDirectoryConfig, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameNative, topology, false, nativepolicyutil.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
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

func TestRemovePodPreservesAllocationWhenAtomicCommitFails(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	policy, err := getTestNativePolicy(topology, t.TempDir())
	require.NoError(t, err)
	req := &pluginapi.ResourceRequest{
		PodUid:        "pod",
		PodNamespace:  "namespace",
		PodName:       "pod",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		NativeQosClass: string(v1.PodQOSGuaranteed),
	}
	_, err = policy.Allocate(context.Background(), req)
	require.NoError(t, err)
	oldAllocation := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	oldMachineState := policy.state.GetMachineState()
	commitErr := errors.New("atomic remove commit failed")
	failingState := &nativeAllocationCommitFailureState{
		State: policy.state,
		err:   commitErr,
	}
	policy.state = failingState

	_, err = policy.RemovePod(context.Background(), &pluginapi.RemovePodRequest{PodUid: req.PodUid})

	require.ErrorIs(t, err, commitErr)
	require.Equal(t, 1, failingState.attempts)
	require.Equal(t, oldAllocation, policy.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, oldMachineState, policy.state.GetMachineState())
}

func TestAllocatePreservesExistingAllocationWhenAtomicCommitFails(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	policy, err := getTestNativePolicy(topology, t.TempDir())
	require.NoError(t, err)
	req := &pluginapi.ResourceRequest{
		PodUid:        "pod",
		PodNamespace:  "namespace",
		PodName:       "pod",
		ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 1,
		},
		NativeQosClass: string(v1.PodQOSGuaranteed),
	}
	_, err = policy.Allocate(context.Background(), req)
	require.NoError(t, err)
	oldAllocation := policy.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, oldAllocation)
	oldMachineState := policy.state.GetMachineState()

	commitErr := errors.New("atomic allocation commit failed")
	failingState := &nativeAllocationCommitFailureState{
		State: policy.state,
		err:   commitErr,
	}
	policy.state = failingState
	req.ResourceRequests[string(v1.ResourceCPU)] = 2

	_, err = policy.Allocate(context.Background(), req)

	require.ErrorIs(t, err, commitErr)
	require.Equal(t, 1, failingState.attempts)
	require.Equal(t, oldAllocation, policy.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, oldMachineState, policy.state.GetMachineState())
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
		tc := tc
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

func TestClearResidualStateRetriesFailedDurableCommit(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	policy, err := getTestNativePolicy(topology, t.TempDir())
	require.NoError(t, err)
	policy.metaServer = &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{}},
	}
	const podUID = "residual-pod"
	require.NoError(t, policy.state.SetAllocationInfo(podUID, "main", &state.AllocationInfo{
		AllocationResult: machine.NewCPUSet(0),
	}, true))
	policy.residualHitMap[podUID] = int64(maxResidualTime/stateCheckPeriod) - 1
	failingState := &nativeResidualCommitFailureState{
		State: policy.state,
		err:   errors.New("store failed"),
	}
	policy.state = failingState

	policy.clearResidualState()
	require.NotNil(t, policy.state.GetAllocationInfo(podUID, "main"),
		"failed durable cleanup must keep residual state for retry")

	policy.clearResidualState()
	require.Nil(t, policy.state.GetAllocationInfo(podUID, "main"))
	require.Equal(t, 2, failingState.attempts)
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
