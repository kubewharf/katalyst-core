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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type systemExclusiveCommitFailureState struct {
	state.State
	err      error
	attempts int
}

func newResidualCleanupLivenessPolicy(t *testing.T) *DynamicPolicy {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	return p
}

func TestClearResidualStateProgressingTargetDefersHealthy(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, p.clearResidualStateAfterPodList(ctx, nil))
	require.Same(t, target, p.currentAdvisorPostCommitTarget(),
		"an aged published target still owns durable publication and must not be abandoned")
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestClearResidualStateUnchangedStuckTargetReportsError(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	err := p.clearResidualStateAfterPodList(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "stuck")
}

func TestClearResidualStateRecoversMatureResidualBehindStuckAdvisorTarget(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	const podUID = "mature-residual-behind-stuck-target"
	p.state.SetPodEntries(state.PodEntries{
		podUID: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{PodUid: podUID, ContainerName: "main"},
			},
		},
	}, false)
	p.residualHitMap = map[string]int64{
		podUID: maxResidualTime.Nanoseconds() / stateCheckPeriod.Nanoseconds(),
	}
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.FileExists(t, p.advisorPostCommitCheckpointPath())

	err := p.clearResidualStateAfterPodList(context.Background(), nil)
	require.NoError(t, err)
	require.NotContains(t, p.state.GetPodEntries(), podUID)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, advisorPostCommitPhaseCanonicalReconcile,
		p.currentAdvisorPostCommitProgress().phase)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func TestClearResidualStateAgesResidualOnlyOncePerInvocation(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	const podUID = "residual"
	p.state.SetPodEntries(state.PodEntries{
		podUID: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{PodUid: podUID, ContainerName: "main"},
			},
		},
	}, false)
	p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, p.clearResidualStateAfterPodList(ctx, nil))
	require.Equal(t, int64(1), p.residualHitMap[podUID])
}

func TestClearResidualStateTargetChangeWakesBoundedRetry(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	const podUID = "residual-during-target-replacement"
	p.state.SetPodEntries(state.PodEntries{
		podUID: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{PodUid: podUID, ContainerName: "main"},
			},
		},
	}, false)
	beforeRevision := p.state.GetRevision()
	beforeEntries := p.state.GetPodEntries()
	beforeMachineState := p.state.GetMachineState()
	targetA := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, beforeRevision)
	result := make(chan error, 1)
	go func() {
		result <- p.clearResidualStateAfterPodList(context.Background(), nil)
	}()

	require.Eventually(t, func() bool {
		p.Lock()
		defer p.Unlock()
		return p.residualHitMap[podUID] == 1
	}, time.Second, time.Millisecond, "cleanup did not enter bounded wait for target A")

	targetB := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, beforeRevision)
	require.NotSame(t, targetA, targetB)

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatalf("cleanup did not wake after replacing target revision %d", targetA.revision)
	}
	require.Equal(t, beforeRevision, p.state.GetRevision())
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachineState, p.state.GetMachineState())
}

func TestClearResidualStateNeverMutatesWhileFenced(t *testing.T) {
	t.Parallel()
	p := newResidualCleanupLivenessPolicy(t)
	const podUID = "mature-residual"
	entries := state.PodEntries{
		podUID: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{PodUid: podUID, ContainerName: "main"},
			},
		},
	}
	p.state.SetPodEntries(entries, false)
	p.residualHitMap = map[string]int64{
		podUID: maxResidualTime.Nanoseconds() / stateCheckPeriod.Nanoseconds(),
	}
	p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, p.clearResidualStateAfterPodList(ctx, nil))
	require.Contains(t, p.state.GetPodEntries(), podUID)
}

func (s *systemExclusiveCommitFailureState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
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
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision,
		podEntries,
		machineState,
		allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores,
		persist,
		permits...,
	)
}

func TestCheckCPUSetHealth(t *testing.T) {
	t.Parallel()

	newHealthRecorder := func(policy *DynamicPolicy) *general.HealthzCheckResult {
		result := &general.HealthzCheckResult{Ready: true}
		ops := policy.cpuHealthOps()
		ops.updateHealthzState = func(state general.HealthzCheckState, message string) error {
			result.Ready = state == general.HealthzCheckStateReady
			result.Message = message
			return nil
		}
		ops.updateHealthzStateByError = func(err error) error {
			result.Ready = false
			result.Message = err.Error()
			return nil
		}
		return result
	}
	newAllocation := func(podUID, containerName, qosLevel string, allocation, original machine.CPUSet) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid: podUID, PodNamespace: "test-ns", PodName: podUID,
				ContainerName: containerName, ContainerType: v1alpha1.ContainerType_MAIN.String(),
				QoSLevel:    qosLevel,
				Annotations: map[string]string{consts.PodAnnotationAggregatedRequestsKey: "1"},
			},
			AllocationResult: allocation, OriginalAllocationResult: original, RequestQuantity: 1,
		}
	}
	newPod := func(podUID, containerName, containerID string) *v1.Pod {
		return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID), Namespace: "test-ns", Name: podUID},
			Spec: v1.PodSpec{Containers: []v1.Container{{
				Name:      containerName,
				Resources: v1.ResourceRequirements{Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}},
			}}},
			Status: v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{{
				Name: containerName, ContainerID: "containerd://" + containerID,
				State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			}}},
		}
	}
	assertReady := func(t *testing.T, result *general.HealthzCheckResult) {
		t.Helper()
		require.True(t, result.Ready, result.Message)
	}

	t.Run("actual shared and dedicated overlap is not masked by committed cpusets", func(t *testing.T) {
		t.Parallel()
		policy := newTestDynamicPolicy(t, "check-cpuset-actual-overlap")
		policy.emitter.emitter = &metrics.DummyMetrics{}
		sharedUID, dedicatedUID := "health-shared", "health-dedicated"
		sharedContainer, dedicatedContainer := "shared", "dedicated"
		policy.state.SetPodEntries(state.PodEntries{
			sharedUID: {sharedContainer: newAllocation(sharedUID, sharedContainer,
				consts.PodAnnotationQoSLevelSharedCores, machine.MustParse("0-1"), machine.MustParse("0-1"))},
			dedicatedUID: {dedicatedContainer: newAllocation(dedicatedUID, dedicatedContainer,
				consts.PodAnnotationQoSLevelDedicatedCores, machine.MustParse("2-3"), machine.MustParse("4-5"))},
		}, false)
		policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
		policy.meta.metaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &pod.PodFetcherStub{
			PodList: []*v1.Pod{
				newPod(sharedUID, sharedContainer, "shared-id"),
				newPod(dedicatedUID, dedicatedContainer, "dedicated-id"),
			},
		}}}
		policy.cpuSetHealthOps = &cpuSetHealthOps{
			getContainerAbsCgroupPath: func(string, string, string) (string, error) { return "/test/cgroup", nil },
			getCPUSetWithAbsolutePath: func(string) (*cgroupcommon.CPUSetStats, error) {
				return &cgroupcommon.CPUSetStats{CPUs: "4-5"}, nil
			},
		}
		result := newHealthRecorder(policy)
		policy.checkCPUSet(nil, nil, nil, nil, nil)
		require.False(t, result.Ready)
		require.Equal(t, "cpuset overlap", result.Message)
	})

	t.Run("container id lookup failure is transient", func(t *testing.T) {
		t.Parallel()
		policy := newTestDynamicPolicy(t, "check-cpuset-container-id-error")
		policy.emitter.emitter = &metrics.DummyMetrics{}
		policy.state.SetPodEntries(state.PodEntries{"missing-pod": {"main": newAllocation(
			"missing-pod", "main", consts.PodAnnotationQoSLevelDedicatedCores,
			machine.MustParse("0-1"), machine.MustParse("0-1"))}}, false)
		policy.meta.metaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &pod.PodFetcherStub{}}}
		result := newHealthRecorder(policy)
		policy.checkCPUSet(nil, nil, nil, nil, nil)
		assertReady(t, result)
	})

	t.Run("container cgroup path lookup failure is transient", func(t *testing.T) {
		t.Parallel()
		policy := newTestDynamicPolicy(t, "check-cpuset-cgroup-path-error")
		policy.emitter.emitter = &metrics.DummyMetrics{}
		policy.state.SetPodEntries(state.PodEntries{"missing-cgroup": {"main": newAllocation(
			"missing-cgroup", "main", consts.PodAnnotationQoSLevelDedicatedCores,
			machine.MustParse("0-1"), machine.MustParse("0-1"))}}, false)
		policy.meta.metaServer = &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &pod.PodFetcherStub{
			PodList: []*v1.Pod{newPod("missing-cgroup", "main", "missing-id")},
		}}}
		result := newHealthRecorder(policy)
		policy.checkCPUSet(nil, nil, nil, nil, nil)
		assertReady(t, result)
	})
}

func TestEmitExceededMetricsWithNilDynamicConfiguration(t *testing.T) {
	t.Parallel()
	policy := &DynamicPolicy{emitter: emitterComponent{emitter: &metrics.DummyMetrics{}}, config: configComponent{dynamicConfig: dynamicconfig.NewDynamicAgentConfiguration()}}
	policy.config.dynamicConfig.SetDynamicConfiguration(nil)
	podUID := "nil-dynamic-configuration"
	podEntries := state.PodEntries{podUID: {"main": {
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: podUID, ContainerName: "main", ContainerType: v1alpha1.ContainerType_MAIN.String(),
			QoSLevel: consts.PodAnnotationQoSLevelSharedCores,
		},
	}}}
	cpusetState := &cpusetPodState{
		cpuset: machine.MustParse("0"), totalMilliCPURequest: 2000,
		podMap: map[string]*v1.Pod{podUID: {ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns", Name: podUID}}},
	}
	require.NotPanics(t, func() {
		policy.emitExceededMetrics(podEntries, "0", cpusetState, 0.5, false)
	})
}

func TestBuildCPUSetPodStateMap(t *testing.T) {
	t.Parallel()
	t.Run("test-empty-input", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		result := p.buildCPUSetPodStateMap(context.Background(), state.PodEntries{}, make(map[string]map[string]machine.CPUSet))
		assert.Empty(t, result)
	})

	t.Run("test-with-pods", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								UID:       "test-pod",
								Namespace: "test-namespace",
								Name:      "test-pod",
							},
							Spec: v1.PodSpec{
								Containers: []v1.Container{
									{
										Name: "test-container",
										Resources: v1.ResourceRequirements{
											Requests: v1.ResourceList{
												v1.ResourceCPU: resource.MustParse("4"),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}}}

		podUID := "test-pod"
		containerName := "test-container"
		cpuset := machine.MustParse("0-3")

		podEntries := state.PodEntries{
			podUID: {
				containerName: &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						PodNamespace:  "test-namespace",
						PodName:       "test-pod",
						ContainerName: containerName,
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: cpuset,
				},
			},
		}

		actualCPUSets := map[string]map[string]machine.CPUSet{
			podUID: {
				containerName: cpuset,
			},
		}

		result := p.buildCPUSetPodStateMap(context.Background(), podEntries, actualCPUSets)
		require.Len(t, result, 1)
		assert.Equal(t, cpuset, result["0-3"].cpuset)
	})
}

func TestCalculateTotalCPURequest(t *testing.T) {
	t.Parallel()
	t.Run("test-no-subset", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		cs := &cpusetPodState{
			totalMilliCPURequest: 1000,
		}
		result := p.calculateTotalCPURequest("0-3", cs, make(map[string]*cpusetPodState))
		assert.Equal(t, int64(1000), result)
	})

	t.Run("test-with-subset", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-3"),
			totalMilliCPURequest: 1000,
		}

		cpusetPodStateMap := map[string]*cpusetPodState{
			"0-1": {
				cpuset:               machine.MustParse("0-1"),
				totalMilliCPURequest: 500,
			},
			"2-3": {
				cpuset:               machine.MustParse("2-3"),
				totalMilliCPURequest: 500,
			},
		}

		result := p.calculateTotalCPURequest("0-3", cs, cpusetPodStateMap)
		assert.Equal(t, int64(2000), result)
	})
}

func TestEmitExceededMetrics(t *testing.T) {
	t.Parallel()
	t.Run("test-shared-cores", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{emitter: emitterComponent{emitter: &metrics.DummyMetrics{}}, config: configComponent{dynamicConfig: dynamicconfig.NewDynamicAgentConfiguration()}}

		podUID := "test-pod"
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:       "test-pod",
				Namespace: "test-namespace",
				Name:      "test-pod",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
				},
			},
		}

		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-1"),
			totalMilliCPURequest: 3000,
			podMap: map[string]*v1.Pod{
				podUID: pod,
			},
		}

		podEntries := state.PodEntries{
			podUID: {
				"test-container": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "test-container",
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
		}

		p.emitExceededMetrics(podEntries, "0-1", cs, 0, false)
	})

	t.Run("test-dedicated-cores", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{emitter: emitterComponent{emitter: &metrics.DummyMetrics{}}, config: configComponent{dynamicConfig: dynamicconfig.NewDynamicAgentConfiguration()}}

		podUID := "test-pod"
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:       "test-pod",
				Namespace: "test-namespace",
				Name:      "test-pod",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
				},
			},
		}

		cs := &cpusetPodState{
			cpuset:               machine.MustParse("0-1"),
			totalMilliCPURequest: 3000,
			podMap: map[string]*v1.Pod{
				podUID: pod,
			},
		}

		podEntries := state.PodEntries{
			podUID: {
				"test-container": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        podUID,
						ContainerName: "test-container",
						ContainerType: v1alpha1.ContainerType_MAIN.String(),
						QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
		}

		p.emitExceededMetrics(podEntries, "0-1", cs, 0, false)
	})
}

func TestSyncContainerCPUIdle(t *testing.T) {
	t.Parallel()

	t.Run("empty policy does not panic", func(t *testing.T) {
		t.Parallel()

		p := &DynamicPolicy{}
		assert.NotPanics(t, func() {
			p.syncContainerCPUIdle(nil, nil, nil, nil, nil)
		})
	})

	t.Run("routes component meta and config fields", func(t *testing.T) {
		t.Parallel()

		// After the component-grouping refactor the periodic sync reads
		// p.meta.metaServer and p.config.conf; wire both so a regression back to
		// the removed flat p.metaServer / p.conf fields is caught at compile time.
		p := &DynamicPolicy{
			meta: metaComponent{metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{PodFetcher: &pod.PodFetcherStub{}},
			}},
			config: configComponent{conf: config.NewConfiguration()},
		}
		assert.NotPanics(t, func() {
			p.syncContainerCPUIdle(nil, nil, nil, nil, nil)
		})
	})
}

type stubNodeFetcher struct {
	node *v1.Node
	err  error
}

func (s *stubNodeFetcher) Run(_ context.Context) {}

func (s *stubNodeFetcher) GetNode(_ context.Context) (*v1.Node, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.node, nil
}

func newCPUSet(start, size int) machine.CPUSet {
	cpus := make([]int, 0, size)
	for i := 0; i < size; i++ {
		cpus = append(cpus, start+i)
	}
	return machine.NewCPUSet(cpus...)
}

func newPoolAllocationInfo(t *testing.T, policy *DynamicPolicy, poolName string, allocation machine.CPUSet) *state.AllocationInfo {
	t.Helper()

	assignments, err := machine.GetNumaAwareAssignments(policy.machine.machineInfo.CPUTopology, allocation)
	require.NoError(t, err)

	return &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(poolName),
		AllocationResult:                 allocation,
		OriginalAllocationResult:         allocation.Clone(),
		TopologyAwareAssignments:         assignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
	}
}

func newSystemAllocationInfo(t *testing.T, policy *DynamicPolicy, podUID, containerName, specifiedPool string, allocation machine.CPUSet) *state.AllocationInfo {
	t.Helper()

	assignments, err := machine.GetNumaAwareAssignments(policy.machine.machineInfo.CPUTopology, allocation)
	require.NoError(t, err)

	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "test-ns",
			PodName:       podUID,
			ContainerName: containerName,
			ContainerType: v1alpha1.ContainerType_MAIN.String(),
			QoSLevel:      consts.PodAnnotationQoSLevelSystemCores,
			OwnerPoolName: commonstate.GetSystemPoolName(specifiedPool),
			Annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementCPUSet: specifiedPool,
			},
		},
		AllocationResult:                 allocation,
		OriginalAllocationResult:         allocation.Clone(),
		TopologyAwareAssignments:         assignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
	}
}

func TestGetExpectedSystemExclusivePools(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		policy        *DynamicPolicy
		expectedPools map[string]int
		wantNil       bool
	}{
		{
			name: "skip invalid size and add system prefix",
			policy: &DynamicPolicy{config: configComponent{dynamicConfig: func() *dynamicconfig.DynamicAgentConfiguration {
				conf := dynamicconfig.NewConfiguration()
				conf.SystemExclusivePool = map[string]int{
					"latency": 2,
					"broken":  0,
				}
				cfg := dynamicconfig.NewDynamicAgentConfiguration()
				cfg.SetDynamicConfiguration(conf)
				return cfg
			}()}},
			expectedPools: map[string]int{
				commonstate.GetSystemPoolName("latency"): 2,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.policy.getExpectedSystemExclusivePools()

			if tc.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.expectedPools, got)
		})
	}
}

func TestCalculateSystemExclusivePoolChanges(t *testing.T) {
	t.Parallel()

	t.Run("respect configured shrink bounds", func(t *testing.T) {
		t.Parallel()

		ratio := 0.2
		minShrink := int64(1)
		maxShrink := int64(2)
		conf := dynamicconfig.NewConfiguration()
		conf.SystemExclusivePoolShrinkRatio = &ratio
		conf.SystemExclusivePoolShrinkMin = &minShrink
		conf.SystemExclusivePoolShrinkMax = &maxShrink

		policy := newTestDynamicPolicy(t, "calculate-system-exclusive-pool-changes-0")
		policy.config.dynamicConfig.SetDynamicConfiguration(conf)

		currentPools := map[string]*state.AllocationInfo{
			"system-latency": {AllocationResult: newCPUSet(0, 10)},
			"system-batch":   {AllocationResult: newCPUSet(10, 4)},
			"system-unused":  {AllocationResult: newCPUSet(14, 2)},
		}
		expectedPools := map[string]int{
			"system-latency": 7,
			"system-batch":   7,
		}

		toCreate, toUpdate, toDelete := policy.calculateSystemExclusivePoolChanges(currentPools, expectedPools)

		assert.Empty(t, toCreate)
		assert.Equal(t, map[string]int{
			"system-latency": -2,
			"system-batch":   3,
		}, toUpdate)
		assert.Equal(t, []string{"system-unused"}, toDelete.List())
	})

	t.Run("shrink is capped by expected delta", func(t *testing.T) {
		t.Parallel()

		policy := newTestDynamicPolicy(t, "calculate-system-exclusive-pool-changes-1")

		currentPools := map[string]*state.AllocationInfo{
			"system-latency": {AllocationResult: newCPUSet(0, 10)},
		}
		expectedPools := map[string]int{
			"system-latency": 9,
		}

		toCreate, toUpdate, toDelete := policy.calculateSystemExclusivePoolChanges(currentPools, expectedPools)

		assert.Empty(t, toCreate)
		assert.Equal(t, map[string]int{"system-latency": -1}, toUpdate)
		assert.Empty(t, toDelete.List())
	})

	t.Run("create pool only with pods", func(t *testing.T) {
		t.Parallel()

		policy := newTestDynamicPolicy(t, "calculate-system-exclusive-pool-changes-2")
		policy.state.SetPodEntries(state.PodEntries{
			"pod-with-pool": {
				"main": newSystemAllocationInfo(t, policy, "pod-with-pool", "main", "new1", newCPUSet(4, 2)),
			},
		}, false)

		expectedPools := map[string]int{
			"system-new1": 2,
			"system-new2": 2,
		}

		toCreate, toUpdate, toDelete := policy.calculateSystemExclusivePoolChanges(nil, expectedPools)

		assert.Equal(t, map[string]int{"system-new1": 2}, toCreate)
		assert.Empty(t, toUpdate)
		assert.Empty(t, toDelete.List())
	})
}

func TestUpdateSystemExclusivePool(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "update-system-exclusive-pool")
	poolToShrink := commonstate.GetSystemPoolName("latency")
	poolToExpand := commonstate.GetSystemPoolName("batch")

	policy.state.SetAllocationInfo(poolToShrink, commonstate.FakedContainerName,
		newPoolAllocationInfo(t, policy, poolToShrink, newCPUSet(0, 4)), false)
	policy.state.SetAllocationInfo(poolToExpand, commonstate.FakedContainerName,
		newPoolAllocationInfo(t, policy, poolToExpand, newCPUSet(4, 2)), false)

	availableAfterUpdate, err := policy.updateSystemExclusivePool(map[string]int{
		poolToShrink: -2,
		poolToExpand: 2,
	}, machine.NewCPUSet())
	require.NoError(t, err)

	shrunkPool := policy.state.GetAllocationInfo(poolToShrink, commonstate.FakedContainerName)
	expandedPool := policy.state.GetAllocationInfo(poolToExpand, commonstate.FakedContainerName)
	require.NotNil(t, shrunkPool)
	require.NotNil(t, expandedPool)

	assert.Equal(t, 2, shrunkPool.AllocationResult.Size())
	assert.Equal(t, 4, expandedPool.AllocationResult.Size())
	assert.True(t, availableAfterUpdate.IsEmpty())
	assert.True(t, shrunkPool.AllocationResult.Intersection(expandedPool.AllocationResult).IsEmpty())
}

func TestCreateSystemExclusivePool(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "create-system-exclusive-pool")
	poolName := commonstate.GetSystemPoolName("latency")
	availableCPUs := newCPUSet(0, 4)

	remainingCPUs, err := policy.createSystemExclusivePool(map[string]int{poolName: 2}, availableCPUs)
	require.NoError(t, err)

	createdPool := policy.state.GetAllocationInfo(poolName, commonstate.FakedContainerName)
	require.NotNil(t, createdPool)

	assert.Equal(t, 2, createdPool.AllocationResult.Size())
	assert.Equal(t, 2, remainingCPUs.Size())
	assert.True(t, createdPool.AllocationResult.Intersection(remainingCPUs).IsEmpty())
	assert.True(t, createdPool.AllocationResult.Union(remainingCPUs).Equals(availableCPUs))
}

func TestAdjustSystemCoresPodAllocation(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "adjust-system-cores-pod-allocation")
	defaultSystemCPUs := policy.machine.machineInfo.CPUDetails.CPUs()
	poolName := commonstate.GetSystemPoolName("latency")
	poolAllocation := newCPUSet(0, 2)

	policy.state.SetAllocationInfo(poolName, commonstate.FakedContainerName,
		newPoolAllocationInfo(t, policy, poolName, poolAllocation), false)
	policy.state.SetAllocationInfo("pod-with-pool", "main",
		newSystemAllocationInfo(t, policy, "pod-with-pool", "main", "latency", newCPUSet(4, 2)), false)
	policy.state.SetAllocationInfo("pod-with-missing-pool", "main",
		newSystemAllocationInfo(t, policy, "pod-with-missing-pool", "main", "missing", newCPUSet(6, 2)), false)

	err := policy.adjustSystemCoresPodAllocation()
	require.NoError(t, err)

	adjustedPod := policy.state.GetAllocationInfo("pod-with-pool", "main")
	missingPoolPod := policy.state.GetAllocationInfo("pod-with-missing-pool", "main")
	require.NotNil(t, adjustedPod)
	require.NotNil(t, missingPoolPod)

	assert.True(t, adjustedPod.AllocationResult.Equals(poolAllocation))
	assert.True(t, missingPoolPod.AllocationResult.Equals(defaultSystemCPUs))
}

func TestApplySystemExclusivePoolChanges(t *testing.T) {
	t.Parallel()

	policy := newTestDynamicPolicy(t, "apply-system-exclusive-pool-changes")
	policy.config.reservedCPUs = machine.NewCPUSet()
	defaultSystemCPUs := policy.machine.machineInfo.CPUDetails.CPUs()

	policy.state.SetAllocationInfo("pod-with-pool", "main",
		newSystemAllocationInfo(t, policy, "pod-with-pool", "main", "latency", defaultSystemCPUs), false)

	err := policy.applySystemExclusivePoolChanges(
		map[string]int{commonstate.GetSystemPoolName("latency"): 2},
		nil,
		nil,
	)
	require.NoError(t, err)

	poolAllocationInfo := policy.state.GetAllocationInfo(commonstate.GetSystemPoolName("latency"), commonstate.FakedContainerName)
	podAllocationInfo := policy.state.GetAllocationInfo("pod-with-pool", "main")
	require.NotNil(t, poolAllocationInfo)
	require.NotNil(t, podAllocationInfo)

	assert.Equal(t, 2, poolAllocationInfo.AllocationResult.Size())
	assert.True(t, podAllocationInfo.AllocationResult.Equals(poolAllocationInfo.AllocationResult))
}

func TestApplySystemExclusivePoolChangesRetriesFailedAtomicCommit(t *testing.T) {
	t.Parallel()
	policy := newTestDynamicPolicy(t, "apply-system-exclusive-pool-changes-atomic")
	policy.config.reservedCPUs = machine.NewCPUSet()
	defaultSystemCPUs := policy.machine.machineInfo.CPUDetails.CPUs()
	require.NoError(t, policy.state.SetAllocationInfo("pod-with-pool", "main",
		newSystemAllocationInfo(t, policy, "pod-with-pool", "main", "latency", defaultSystemCPUs), false))
	oldEntries := policy.state.GetPodEntries()
	oldMachineState := policy.state.GetMachineState()
	commitErr := errors.New("atomic system exclusive commit failed")
	failingState := &systemExclusiveCommitFailureState{
		State: policy.state,
		err:   commitErr,
	}
	policy.state = failingState
	toCreate := map[string]int{commonstate.GetSystemPoolName("latency"): 2}

	err := policy.applySystemExclusivePoolChanges(toCreate, nil, nil)

	require.ErrorIs(t, err, commitErr)
	require.Equal(t, oldEntries, policy.state.GetPodEntries())
	require.Equal(t, oldMachineState, policy.state.GetMachineState())

	err = policy.applySystemExclusivePoolChanges(toCreate, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 2, failingState.attempts)
	poolAllocationInfo := policy.state.GetAllocationInfo(
		commonstate.GetSystemPoolName("latency"), commonstate.FakedContainerName)
	require.NotNil(t, poolAllocationInfo)
	require.Equal(t, 2, poolAllocationInfo.AllocationResult.Size())
}

func TestGetSystemExclusivePoolMetricValueAndStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		currentPools  map[string]*state.AllocationInfo
		expectedPools map[string]int
		poolName      string
		wantValue     int64
		wantStatus    string
	}{
		{
			name: "ready",
			currentPools: map[string]*state.AllocationInfo{
				"system-latency": {AllocationResult: newCPUSet(0, 2)},
			},
			expectedPools: map[string]int{"system-latency": 2},
			poolName:      "system-latency",
			wantValue:     2,
			wantStatus:    "ready",
		},
		{
			name:          "absent",
			currentPools:  map[string]*state.AllocationInfo{},
			expectedPools: map[string]int{"system-latency": 2},
			poolName:      "system-latency",
			wantValue:     0,
			wantStatus:    "absent",
		},
		{
			name: "stale",
			currentPools: map[string]*state.AllocationInfo{
				"system-latency": {AllocationResult: newCPUSet(0, 2)},
			},
			expectedPools: map[string]int{},
			poolName:      "system-latency",
			wantValue:     2,
			wantStatus:    "stale",
		},
		{
			name: "updating",
			currentPools: map[string]*state.AllocationInfo{
				"system-latency": {AllocationResult: newCPUSet(0, 4)},
			},
			expectedPools: map[string]int{"system-latency": 2},
			poolName:      "system-latency",
			wantValue:     4,
			wantStatus:    "updating",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotStatus := getSystemExclusivePoolMetricValueAndStatus(tc.currentPools, tc.expectedPools, tc.poolName)
			assert.Equal(t, tc.wantValue, gotValue)
			assert.Equal(t, tc.wantStatus, gotStatus)
		})
	}
}
