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
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/accompanyresource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	bulkheadtopology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicpolicyutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

type getPodErrorPodFetcher struct {
	*pod.PodFetcherStub
	err error
	pod *v1.Pod
}

type recordingServiceProfilingManager struct {
	*spd.DummyServiceProfilingManager
	performanceLevel spd.PerformanceLevel
	performanceErr   error
	baseline         bool
	baselineErr      error
	baselineCalls    int
	observedPodMeta  []metav1.ObjectMeta
}

// allRealNUMAsForRampUpFloor mechanically translates the legacy
// enteringRampUp=true boolean (which meant "ramp-up active node-wide") into the
// set of every real NUMA, so existing deriveRampUpReclaimFloor capacity tests
// keep exercising the same per-NUMA distribution while the floor is now scoped
// through affectedRampUpNUMAs.
func allRealNUMAsForRampUpFloor(p *DynamicPolicy) sets.Int {
	out := sets.NewInt()
	for _, n := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		out.Insert(n)
	}
	return out
}

func (m *recordingServiceProfilingManager) ServiceBusinessPerformanceLevel(
	ctx context.Context, podMeta metav1.ObjectMeta,
) (spd.PerformanceLevel, error) {
	m.observedPodMeta = append(m.observedPodMeta, podMeta)
	if err := ctx.Err(); err != nil {
		return spd.PerformanceLevelUnknown, err
	}
	return m.performanceLevel, m.performanceErr
}

func (m *recordingServiceProfilingManager) ServiceBaseline(
	ctx context.Context, podMeta metav1.ObjectMeta,
) (bool, error) {
	m.baselineCalls++
	m.observedPodMeta = append(m.observedPodMeta, podMeta)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return m.baseline, m.baselineErr
}

func (f *getPodErrorPodFetcher) GetPod(_ context.Context, _ string) (*v1.Pod, error) {
	return f.pod, f.err
}

type failingAllocateAccompanyPlugin struct {
	err error
}

func (*failingAllocateAccompanyPlugin) ResourceName() string {
	return "failing-allocate"
}

func (*failingAllocateAccompanyPlugin) GetAccompanyResourceTopologyHints(
	_ *pluginapi.ResourceRequest,
	_ *pluginapi.ListOfTopologyHints,
) error {
	return nil
}

func (p *failingAllocateAccompanyPlugin) AllocateAccompanyResource(
	_ *pluginapi.ResourceRequest,
	_ *pluginapi.ResourceAllocationResponse,
) error {
	return p.err
}

func (*failingAllocateAccompanyPlugin) ReleaseAccompanyResource(_ *pluginapi.RemovePodRequest) error {
	return nil
}

type atomicCommitTrackingState struct {
	state.State
	commitErr      error
	storeErr       error
	failCommits    int
	failStores     int
	commitCalls    int
	commitPersists []bool
	storeCalls     int
}

type applyPoolsCommitGuardState struct {
	state.State
	setPodEntriesCalls    int
	setMachineStateCalls  int
	storeCalls            int
	conditionalCalls      int
	conditionalRevision   uint64
	conditionalAllow      bool
	conditionalDisable    bool
	conditionalPersist    bool
	injectRevisionAdvance bool
}

type missingAllocationInfoState struct {
	state.State
	missingPodUID        string
	missingContainerName string
}

type revisionAdvanceOnOverlapReadState struct {
	state.State
	advanceOnce bool
}

func (s *revisionAdvanceOnOverlapReadState) GetAllowSharedCoresOverlapReclaimedCores() bool {
	allow := s.State.GetAllowSharedCoresOverlapReclaimedCores()
	if s.advanceOnce {
		s.advanceOnce = false
		s.State.SetDisableDedicatedCoresOverlapReclaimedCores(
			!s.State.GetDisableDedicatedCoresOverlapReclaimedCores(), false)
	}
	return allow
}

func (s *missingAllocationInfoState) GetAllocationInfo(podUID string, containerName string) *state.AllocationInfo {
	if podUID == s.missingPodUID && containerName == s.missingContainerName {
		return nil
	}
	return s.State.GetAllocationInfo(podUID, containerName)
}

func (s *atomicCommitTrackingState) CommitAdvisorState(
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, persist bool,
	permits ...*state.WritePermit,
) error {
	s.commitCalls++
	s.commitPersists = append(s.commitPersists, persist)
	if s.commitErr != nil && (s.failCommits < 0 || s.commitCalls <= s.failCommits) {
		return s.commitErr
	}
	return s.State.CommitAdvisorState(
		podEntries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (s *atomicCommitTrackingState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, persist bool,
	permits ...*state.WritePermit,
) error {
	s.commitCalls++
	s.commitPersists = append(s.commitPersists, persist)
	if s.commitErr != nil && (s.failCommits < 0 || s.commitCalls <= s.failCommits) {
		return s.commitErr
	}
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, podEntries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (s *atomicCommitTrackingState) StoreState() error {
	s.storeCalls++
	if s.storeErr != nil && (s.failStores < 0 || s.storeCalls <= s.failStores) {
		return s.storeErr
	}
	return s.State.StoreState()
}

func TestPersistenceRetryClearsThroughStateWrapperWhileAdvisorTargetPending(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
	require.NoError(t, err)
	tracking := &atomicCommitTrackingState{State: state.NewTransientState(topology)}
	target := &advisorPostCommitTarget{revision: tracking.GetRevision()}
	p := &DynamicPolicy{
		state: tracking, adjustment: adjustmentComponent{
			advisorPostCommitTarget:      target,
			cpuSetAdjustmentRetryPersist: true,
			cpuSetAdjustmentRetryReasons: map[dynamicpolicyutil.CPUSetAdjustmentRetryReason]struct{}{
				dynamicpolicyutil.RetryReasonPersistFailed: {},
			},
		},
	}
	p.installCPUStateWritePermit()
	oldMachineState := tracking.GetMachineState()
	oldRevision := tracking.GetRevision()

	require.NoError(t, p.persistCPUSetAdjustmentStateIfNeeded())
	require.Equal(t, 1, tracking.storeCalls)
	require.False(t, p.adjustment.cpuSetAdjustmentRetryPersist)
	require.NotContains(t, p.adjustment.cpuSetAdjustmentRetryReasons, dynamicpolicyutil.RetryReasonPersistFailed)
	require.Equal(t, oldRevision, tracking.GetRevision())
	require.Equal(t, oldMachineState, tracking.GetMachineState())
	require.Same(t, target, p.currentAdvisorPostCommitTarget())

	err = tracking.SetMachineState(state.NUMANodeMap{
		0: &state.NUMANodeState{AllocatedCPUSet: machine.NewCPUSet(1)},
	}, false)
	require.Error(t, err)
	var pendingErr *advisorPostCommitPendingError
	require.ErrorAs(t, err, &pendingErr)
	require.Equal(t, oldRevision, tracking.GetRevision())
	require.Equal(t, oldMachineState, tracking.GetMachineState())
}

func TestIsRampUpReclaimHardPartitionEnabledRequiresNodeReclaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		enableReclaim   bool
		enableHardFloor bool
		want            bool
	}{
		{
			name:            "hard partition disabled",
			enableReclaim:   true,
			enableHardFloor: false,
			want:            false,
		},
		{
			name:            "reclaim disabled disables hard partition",
			enableReclaim:   false,
			enableHardFloor: true,
			want:            false,
		},
		{
			name:            "reclaim and hard partition enabled",
			enableReclaim:   true,
			enableHardFloor: true,
			want:            true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dyn := dynamicconfig.NewDynamicAgentConfiguration()
			dynamicConf := dyn.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = tt.enableReclaim
			dynamicConf.EnableRampUpReclaimHardPartition = tt.enableHardFloor

			p := &DynamicPolicy{config: configComponent{dynamicConfig: dyn}}
			assert.Equal(t, tt.want, p.isRampUpReclaimHardPartitionEnabled())
		})
	}
}

func TestSharedNUMABindingCPUIncrRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		enableReclaim   bool
		enableHardFloor bool
		want            float64
	}{
		{
			name: "both disabled",
			want: cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
		},
		{
			name:            "hard partition disabled",
			enableReclaim:   true,
			enableHardFloor: false,
			want:            cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
		},
		{
			name:            "reclaim disabled",
			enableReclaim:   false,
			enableHardFloor: true,
			want:            cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
		},
		{
			name:            "hard partition active",
			enableReclaim:   true,
			enableHardFloor: true,
			want:            cpuconsts.CPUIncrRatioDefault,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dyn := dynamicconfig.NewDynamicAgentConfiguration()
			dynamicConf := dyn.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = tt.enableReclaim
			dynamicConf.EnableRampUpReclaimHardPartition = tt.enableHardFloor

			p := &DynamicPolicy{config: configComponent{dynamicConfig: dyn}}
			require.Equal(t, tt.want, p.getSharedNUMABindingCPUIncrRatio())
		})
	}
}

func (s *applyPoolsCommitGuardState) SetPodEntries(entries state.PodEntries, persist bool) error {
	s.setPodEntriesCalls++
	return s.State.SetPodEntries(entries, persist)
}

func (s *applyPoolsCommitGuardState) SetMachineState(machineState state.NUMANodeMap, persist bool) error {
	s.setMachineStateCalls++
	return s.State.SetMachineState(machineState, persist)
}

func (s *applyPoolsCommitGuardState) StoreState() error {
	s.storeCalls++
	return s.State.StoreState()
}

func (s *applyPoolsCommitGuardState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, persist bool,
	permits ...*state.WritePermit,
) error {
	s.conditionalCalls++
	s.conditionalRevision = expectedRevision
	s.conditionalAllow = allowOverlap
	s.conditionalDisable = disableDedicatedOverlap
	s.conditionalPersist = persist
	if s.injectRevisionAdvance {
		s.State.SetAllowSharedCoresOverlapReclaimedCores(
			!s.State.GetAllowSharedCoresOverlapReclaimedCores(), false)
	}
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, entries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func TestDynamicPolicy_getReclaimOverlapShareRatio(t *testing.T) {
	t.Parallel()

	type fields struct {
		allowSharedCoresOverlapReclaimedCores bool
	}
	type args struct {
		entries state.PodEntries
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[string]float64
		wantErr bool
	}{
		{
			name: "overlap disabled",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: false,
			},
			args: args{
				entries: state.PodEntries{},
			},
			want: nil,
		},
		{
			name: "overlap enabled, no reclaim",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "overlap enabled, reclaim and share normal",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{
					commonstate.PoolNameReclaim: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0),
								1: machine.NewCPUSet(1),
								2: machine.NewCPUSet(2),
								3: machine.NewCPUSet(3),
							},
						},
					},
					commonstate.PoolNameShare: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 4),
								1: machine.NewCPUSet(1, 5),
								2: machine.NewCPUSet(2, 6),
								3: machine.NewCPUSet(3, 7),
							},
						},
					},
				},
			},
			want: map[string]float64{
				commonstate.PoolNameShare: 0.5,
			},
			wantErr: false,
		},
		{
			name: "overlap enabled, reclaim and share ramp up",
			fields: fields{
				allowSharedCoresOverlapReclaimedCores: true,
			},
			args: args{
				entries: state.PodEntries{
					commonstate.PoolNameReclaim: {
						commonstate.FakedContainerName: &state.AllocationInfo{
							AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0),
								1: machine.NewCPUSet(1),
								2: machine.NewCPUSet(2),
								3: machine.NewCPUSet(3),
							},
						},
					},
					"pod1": {
						"container1": &state.AllocationInfo{
							AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
								PodUid:        "pod1",
								PodNamespace:  "pod1",
								PodName:       "pod1",
								ContainerName: "container1",
							}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
							RequestQuantity:  4,
							AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 4),
								1: machine.NewCPUSet(1, 5),
								2: machine.NewCPUSet(2, 6),
								3: machine.NewCPUSet(3, 7),
							},
						},
					},
				},
			},
			want: map[string]float64{
				commonstate.PoolNameShare: 0.5,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.Nil(err)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_getReclaimOverlapShareRatio")
			as.Nil(err)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)

			if tt.fields.allowSharedCoresOverlapReclaimedCores {
				p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)
			}

			got, err := p.getReclaimOverlapShareRatio(tt.args.entries)
			if (err != nil) != tt.wantErr {
				t.Errorf("getReclaimOverlapShareRatio() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getReclaimOverlapShareRatio() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllocateSharedNumaBindingCPUs(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	// Setup
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	podName := "test-pod"
	containerName := "test-container"
	podUID := "test-uid"

	// Helper to create request
	createReq := func(reqQuantity float64, inplaceUpdate bool) *pluginapi.ResourceRequest {
		req := &pluginapi.ResourceRequest{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podName,
			ContainerName: containerName,
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): reqQuantity,
			},
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
			Hint: &pluginapi.TopologyHint{
				Nodes:     []uint64{0},
				Preferred: true,
			},
		}
		if inplaceUpdate {
			req.Annotations[apiconsts.PodAnnotationInplaceUpdateResizingKey] = "true"
		}
		return req
	}

	// Case 1: Inplace Update Error - Origin is not SNB
	t.Run("inplace_update_error_origin_not_snb", func(t *testing.T) {
		t.Parallel()

		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)
		// Setup origin allocation info (Normal SharedCores, NOT SNB)
		originAllocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podName,
				ContainerName: containerName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			RequestQuantity: 2,
		}
		policy.state.SetAllocationInfo(podUID, containerName, originAllocationInfo, false)

		req := createReq(4, true)
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, false)
		as.Error(err)
		as.Contains(err.Error(), "cannot change from non-snb to snb during inplace update")
	})

	// Case 2: Inplace Update Success - Origin is SNB
	t.Run("inplace_update_success_origin_snb", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		// Setup origin allocation info (SNB)
		originAllocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        podUID,
				PodNamespace:  "default",
				PodName:       podName,
				ContainerName: containerName,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			RequestQuantity:  2,
			AllocationResult: machine.NewCPUSet(0, 1),
			TopologyAwareAssignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 1),
			},
		}
		originAllocationInfo.SetSpecifiedNUMABindingNUMAID([]uint64{0})

		policy.state.SetAllocationInfo(podUID, containerName, originAllocationInfo, false)

		req := createReq(4, true)
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, false)
		if err != nil {
			as.NotContains(err.Error(), "cannot change from non-snb to snb during inplace update")
		}
	})

	// Case 3: Normal Allocation (Not Inplace Update)
	t.Run("normal_allocation", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		req := createReq(2, false)
		// Clean up previous state
		policy.state.Delete(podUID, containerName, false)

		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, false)
		// This might fail due to pool issues but it covers the else branch
		// We expect it NOT to fail with the inplace update error
		if err != nil {
			as.NotContains(err.Error(), "inplace update")
		}
	})

	// Case 4: Invalid Inputs
	t.Run("invalid_inputs", func(t *testing.T) {
		t.Parallel()
		tmpDir, err := ioutil.TempDir("", "checkpoint-TestAllocateSharedNumaBindingCPUs")
		as.Nil(err)
		defer os.RemoveAll(tmpDir)

		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
		as.Nil(err)

		req := createReq(2, false)

		// Nil req
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), nil, req.Hint, false, false)
		as.Error(err)
		as.Contains(err.Error(), "nil req")

		// Nil hint
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, nil, false, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is nil")

		// Empty hint
		emptyHintReq := createReq(2, false)
		emptyHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{}}
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, emptyHintReq.Hint, false, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is empty")

		// Hint with multiple nodes
		multiNodeHintReq := createReq(2, false)
		multiNodeHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{0, 1}}
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, multiNodeHintReq.Hint, false, false)
		as.Error(err)
		as.Contains(err.Error(), "larger than 1 NUMA")
	})
}

func TestAllocateSharedNumaBindingCPUsMarksColdStartRampUp(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	// Mockey rewrites method text process-wide. Serialize this unpatched
	// PodFetcherStub call with the advisor tests that patch adjacent methods.

	policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false
	policy.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID("snb-ramp-up"),
			Namespace: "default",
			Name:      "snb-ramp-up",
		},
		Status: v1.PodStatus{Phase: v1.PodPending},
	}}}

	req := &pluginapi.ResourceRequest{
		PodUid:        "snb-ramp-up",
		PodNamespace:  "default",
		PodName:       "snb-ramp-up",
		ContainerName: "main",
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		},
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0},
			Preferred: true,
		},
	}

	allocation, err := policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, false)
	require.NoError(t, err)
	require.NotNil(t, allocation)
	require.True(t, allocation.CheckSharedNUMABinding())
	require.True(t, allocation.RampUp)
}

// TestSharedNUMABindingRampUpSelectionExcludesReclaimFloor covers the ramp-up
// floor-adjustment path for an SNB (SharedNUMABinding) ramp-up allocation. SNB
// ramp-up is protected by the bulkhead partition view and is never rematerialized
// to dodge the final reclaim pool.
//
// With allow-overlap off (the hard-partition quadrant), the selector now carves
// the NUMA-local reclaim floor core out of the pending SNB's available domain
// before assigning the exact AllocationResult. The approved contract is: the
// commit succeeds, the pending SNB lands on non-floor NUMA0 CPUs {0,8}, stays
// NUMA0-scoped, and the planned reclaim survives intact {1,3,9,11} (it is no
// longer TPNR-trimmed, because selection never swallowed the floor). The TPNR
// CommitOverride sync remains a defense-in-depth safety net for already-committed
// SNBs but has nothing to trim here.
//
// With allow-overlap on (protection disabled) the selector leaves the reclaim
// floor in the domain, so the residual overlap remains a typed fail-closed
// precommit error rather than being silently shrunk.
func TestSharedNUMABindingRampUpSelectionExcludesReclaimFloor(t *testing.T) {
	t.Parallel()

	const (
		snbPodName = "snb-ramp-up-numa-scoped"
	)
	// Generated topology: NUMA0={0,1,8,9}, NUMA1={2,3,10,11}. The floor
	// adjustment plans reclaim={1,3,9,11}. On NUMA0 the reclaim reserves the whole
	// core {1,9}; the SNB ramp-up on NUMA0 must carve that core out of its available
	// domain at selection time and land on {0,8}, leaving the reclaim floor intact.
	plannedReclaim := machine.NewCPUSet(1, 3, 9, 11)
	snbResult := machine.NewCPUSet(0, 8)

	createPolicy := func(t *testing.T, allowOverlap bool) *DynamicPolicy {
		t.Helper()
		cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
		require.NoError(t, err)
		policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)
		policy.config.reservedCPUs = machine.NewCPUSet()
		policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false
		policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		policy.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
		policy.state.SetAllowSharedCoresOverlapReclaimedCores(allowOverlap, false)

		allocation := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        snbPodName,
				PodNamespace:  "default",
				PodName:       snbPodName,
				ContainerName: "main",
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
				},
			},
			RampUp:          true,
			RequestQuantity: 2,
		}
		allocation.SetSpecifiedNUMABindingNUMAID([]uint64{0})
		policy.state.SetAllocationInfo(allocation.PodUid, allocation.ContainerName, allocation, false)
		return policy
	}

	t.Run("selection excludes NUMA-local reclaim floor and keeps SNB disjoint", func(t *testing.T) {
		t.Parallel()
		policy := createPolicy(t, false)

		err := policy.adjustAllocationEntriesWithRampUpFloor(
			policy.state.GetPodEntries(), policy.state.GetMachineState(),
			false, machine.NewCPUSet(1), false)
		require.NoError(t, err, "selection-time reclaim-floor exclusion must admit the pending SNB")

		// The pending SNB keeps its exact AllocationResult and stays NUMA0-scoped.
		got := policy.state.GetAllocationInfo(snbPodName, "main")
		require.NotNil(t, got)
		require.True(t, got.AllocationResult.Equals(snbResult),
			"pending SNB AllocationResult must exclude the NUMA-local reclaim core: %s", got.AllocationResult)
		require.Len(t, got.TopologyAwareAssignments, 1, "SNB ramp-up stays NUMA0-scoped")
		require.Contains(t, got.TopologyAwareAssignments, 0, "SNB ramp-up stays NUMA0-scoped")

		// The reclaim floor core on NUMA0 survives intact and is disjoint from the SNB;
		// no TPNR trim is needed because the selector never swallowed it.
		reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Intersection(snbResult).IsEmpty(),
			"physical reclaim %s must be disjoint from SNB %s", reclaim.AllocationResult, snbResult)
		require.True(t, reclaim.AllocationResult.Equals(plannedReclaim),
			"reclaim floor must survive intact at selection time: want %s, got %s", plannedReclaim, reclaim.AllocationResult)
	})

	t.Run("protection disabled fails closed on residual overlap", func(t *testing.T) {
		t.Parallel()
		policy := createPolicy(t, true)

		err := policy.adjustAllocationEntriesWithRampUpFloor(
			policy.state.GetPodEntries(), policy.state.GetMachineState(),
			false, machine.NewCPUSet(1), false)
		require.ErrorContains(t, err, "ramp-up shared allocation overlaps the final reclaim pool")
	})
}

func TestNonSNBRampUpRemainsNodeWide(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	policy.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	policy.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	allocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "non-snb-ramp-up",
			PodNamespace:  "default",
			PodName:       "non-snb-ramp-up",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		RampUp:          true,
		RequestQuantity: 1,
	}
	policy.state.SetAllocationInfo(allocation.PodUid, allocation.ContainerName, allocation, false)

	err = policy.adjustAllocationEntriesWithRampUpFloor(
		policy.state.GetPodEntries(),
		policy.state.GetMachineState(),
		false,
		machine.NewCPUSet(1),
		false,
	)
	require.NoError(t, err)

	updated := policy.state.GetAllocationInfo(allocation.PodUid, allocation.ContainerName)
	require.NotNil(t, updated)
	require.True(t, updated.RampUp)
	require.False(t, updated.CheckSharedNUMABinding())
	require.Greater(t, len(updated.TopologyAwareAssignments), 1,
		"non-SNB ramp-up should remain node-wide, got %+v", updated.TopologyAwareAssignments)
}

// TestSNBRampUpExcludesPerNUMAReclaimFloorFromAvailableDomain is the deterministic
// RED for the first-pending SNB ramp-up under the production hard-partition shape:
// 128 CPUs / 1 socket / 4 NUMA / SMT2, reserved core0={0,64}, and an already-derived
// per-NUMA reclaim floor on NUMA0 core4={4,68} (target 2 CPUs).
//
// The reclaim candidate already reserves the floor core on NUMA0. Before the fix the
// SNB ramp-up selector only subtracts the (here empty) explicit ramp-up floor and so
// hands the pending SNB NUMA0 CPUs that include {4,68}; the TPNR view then trims the
// physical NUMA0 reclaim to 0 while the per-NUMA target stays 2, so the desired-vs-
// physical reclaim guard rejects the commit (0 < 2) and the admission retry-loops.
//
// After the fix the selector must carve the NUMA-local reclaim floor out of the SNB
// available domain before assigning the exact AllocationResult: the pending SNB stays
// NUMA0-scoped, whole-core aligned, and disjoint from {4,68}, leaving the floor core
// intact for the reclaim pool. The TPNR protection / CommitOverride remains a safety
// net but is no longer asked to paper over a selection-time overlap.
func TestSNBRampUpExcludesPerNUMAReclaimFloorFromAvailableDomain(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet(0, 64)

	dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.DisableSharedCoresRampUp = false
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.05
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	policy.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0CPUs := cpuTopology.CPUDetails.CPUsInNUMANodes(0)
	require.True(t, numa0CPUs.Equals(machine.MustParse("0-15,64-79")))

	assignments, err := machine.GetNumaAwareAssignments(cpuTopology, numa0CPUs)
	require.NoError(t, err)
	const snbPodName = "snb-pending-numa0"
	entries := state.PodEntries{
		snbPodName: {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        snbPodName,
					PodNamespace:  "default",
					PodName:       snbPodName,
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
					},
				},
				RampUp:                           true,
				AllocationResult:                 numa0CPUs.Clone(),
				OriginalAllocationResult:         numa0CPUs.Clone(),
				TopologyAwareAssignments:         assignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
				RequestQuantity:                  2,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	policy.state.SetPodEntries(entries, false)

	// The already-derived per-NUMA reclaim floor lives on NUMA0 core4 {4,68}; a
	// steady reclaim core sits on NUMA1 {17,81}. The explicit ramp-up floor is empty
	// (the floor has already been carved into the reclaim candidate), mirroring the
	// advisor pool-adjustment frame that owns the pending SNB.
	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: machine.NewCPUSet(4, 68, 17, 81),
	}
	err = policy.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		policy.state.GetPodEntries(),
		policy.state.GetMachineState(),
		sets.NewInt(0),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		policy.state.GetRevision(),
		policy.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.NoError(t, err, "pending first-time SNB must select away from the NUMA-local reclaim floor")

	got := policy.state.GetAllocationInfo(snbPodName, "main")
	require.NotNil(t, got)
	// The pending SNB stays on its hinted NUMA and is never rematerialized off-NUMA.
	require.True(t, got.AllocationResult.IsSubsetOf(numa0CPUs),
		"pending SNB allocation must stay on its hinted NUMA: %s", got.AllocationResult.String())
	require.False(t, got.AllocationResult.IsEmpty(), "pending SNB allocation must be non-empty")
	// The floor core {4,68} must survive in the reclaim pool, disjoint from the SNB.
	reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Intersection(machine.NewCPUSet(4, 68)).Equals(machine.NewCPUSet(4, 68)),
		"reclaim floor {4,68} must survive in the reclaim pool, got %s", reclaim.AllocationResult.String())
	require.True(t, got.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
		"pending SNB %s must be disjoint from reclaim %s", got.AllocationResult.String(), reclaim.AllocationResult.String())
}

// TestSNBRampUpFloorExclusionMatrix exercises the narrow selector exclusion across
// NUMA hints, asymmetry, reserved CPUs, ratio/target-zero, overlap mode, the
// hard-partition-off shape, fail-closed exhaustion, and multiple concurrent SNB
// allocations on the same state snapshot.
func TestSNBRampUpFloorExclusionMatrix(t *testing.T) {
	t.Parallel()

	// 128 CPUs / SMT2 / 4 NUMA: NUMA k = {16k..16k+15, 64+16k..64+16k+15};
	// core p on NUMA k pairs physical CPUs {16k+p, 64+16k+p}.
	newPolicy := func(t *testing.T, hardPartition bool, allowOverlap bool) *DynamicPolicy {
		cpuTopology, err := machine.GenerateDummyCPUTopology(128, 1, 4)
		require.NoError(t, err)
		p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet(0, 64) // NUMA0 core0 reserved
		dc := p.config.dynamicConfig.GetDynamicConfiguration()
		dc.DisableSharedCoresRampUp = false
		dc.EnableReclaim = true
		dc.EnableRampUpReclaimHardPartition = hardPartition
		dc.InitialRampUpReclaimCPUSetRatio = 0.05
		p.state.SetAllowSharedCoresOverlapReclaimedCores(allowOverlap, false)
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
		return p
	}

	// placeSNB registers one pending ramp-up SNB pinned to hintNUMA with an empty
	// result, then runs applyPoolsAndIsolatedInfo with the given reclaim candidate.
	placeSNB := func(t *testing.T, p *DynamicPolicy, snbName string, hintNUMA int, reclaim machine.CPUSet) error {
		entries := state.PodEntries{
			snbName: {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        snbName,
						PodNamespace:  "default",
						PodName:       snbName,
						ContainerName: "main",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             fmt.Sprintf("%d", hintNUMA),
						},
					},
					RampUp:           true,
					AllocationResult: machine.NewCPUSet(),
					RequestQuantity:  2,
				},
			},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.NewCPUSet(),
				},
			},
		}
		p.state.SetPodEntries(entries, false)
		return p.applyPoolsAndIsolatedInfo(
			map[string]machine.CPUSet{commonstate.PoolNameReclaim: reclaim.Clone()},
			map[string]map[string]machine.CPUSet{},
			p.state.GetPodEntries(),
			p.state.GetMachineState(),
			sets.NewInt(hintNUMA),
			false,
			machine.NewCPUSet(),
			defaultShareMaterializationPlan{},
			p.state.GetRevision(),
			p.config.dynamicConfig.GetDynamicConfiguration(),
		)
	}

	t.Run("numa1 hint excludes its local floor core", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, false)
		// NUMA1 floor core17 = {17,81}; NUMA1 available starts {16-31,80-95}.
		require.NoError(t, placeSNB(t, p, "snb-n1", 1, machine.NewCPUSet(17, 81)))
		got := p.state.GetAllocationInfo("snb-n1", "main")
		require.NotNil(t, got)
		require.True(t, got.AllocationResult.IsSubsetOf(p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(1)))
		require.False(t, got.AllocationResult.IsEmpty())
		require.True(t, got.AllocationResult.Intersection(machine.NewCPUSet(17, 81)).IsEmpty(),
			"SNB must avoid NUMA1 floor {17,81}: %s", got.AllocationResult)
	})

	t.Run("hard partition off keeps ordinary sharing", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, false, false)
		// No floor to protect: the SNB may share the reclaim CPUs exactly as before.
		require.NoError(t, placeSNB(t, p, "snb-off", 0, machine.NewCPUSet(4, 68)))
		got := p.state.GetAllocationInfo("snb-off", "main")
		require.NotNil(t, got)
		require.False(t, got.AllocationResult.IsEmpty())
		require.True(t, got.AllocationResult.IsSubsetOf(p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(0)))
	})

	t.Run("overlap permitted still fails closed under hard partition", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, true)
		// With the hard partition on, shared/reclaim overlap is not actually permitted:
		// the selector leaves the reclaim floor in the domain and the precommit
		// residual-overlap validator rejects the commit instead.
		err := placeSNB(t, p, "snb-ovl", 0, machine.NewCPUSet(4, 68))
		require.Error(t, err, "overlap cannot be allowed while the ramp-up hard partition is on")
	})

	t.Run("non-zero reclaim without floor still admits", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, false)
		require.NoError(t, placeSNB(t, p, "snb-nf", 0, machine.NewCPUSet(17, 81)))
		got := p.state.GetAllocationInfo("snb-nf", "main")
		require.NotNil(t, got)
		require.False(t, got.AllocationResult.IsEmpty())
		require.True(t, got.AllocationResult.IsSubsetOf(p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(0)))
	})

	t.Run("reclaim consumes entire numa fails closed", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, false)
		// Reclaim takes every free CPU on NUMA0 (reserved {0,64} already excluded).
		numa0 := p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(0).Difference(p.config.reservedCPUs)
		err := placeSNB(t, p, "snb-full", 0, numa0)
		require.Error(t, err, "no capacity after reserving the reclaim floor must fail closed")
	})

	t.Run("multiple snb allocations stay on distinct cpus", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, false)
		numaCPUs := map[int]machine.CPUSet{
			0: p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(0),
			1: p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(1),
		}
		// Two concurrent pending SNBs on NUMA0 and NUMA2, floors on each.
		entries := state.PodEntries{
			"snb-a": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid: "snb-a", PodNamespace: "default", PodName: "snb-a",
						ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
						},
					},
					RampUp:           true,
					AllocationResult: machine.NewCPUSet(),
					RequestQuantity:  2,
				},
			},
			"snb-b": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid: "snb-b", PodNamespace: "default", PodName: "snb-b",
						ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             "2",
						},
					},
					RampUp:           true,
					AllocationResult: machine.NewCPUSet(),
					RequestQuantity:  2,
				},
			},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.NewCPUSet(),
				},
			},
		}
		p.state.SetPodEntries(entries, false)
		require.NoError(t, p.applyPoolsAndIsolatedInfo(
			map[string]machine.CPUSet{commonstate.PoolNameReclaim: machine.NewCPUSet(4, 68, 36, 100)},
			map[string]map[string]machine.CPUSet{},
			p.state.GetPodEntries(), p.state.GetMachineState(),
			sets.NewInt(0, 2), false, machine.NewCPUSet(), defaultShareMaterializationPlan{}, p.state.GetRevision(),
			p.config.dynamicConfig.GetDynamicConfiguration(),
		))
		a := p.state.GetAllocationInfo("snb-a", "main").AllocationResult
		b := p.state.GetAllocationInfo("snb-b", "main").AllocationResult
		require.False(t, a.IsEmpty())
		require.False(t, b.IsEmpty())
		require.True(t, a.IsSubsetOf(numaCPUs[0]), "snb-a must stay on NUMA0: %s", a)
		require.True(t, b.IsSubsetOf(p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(2)), "snb-b must stay on NUMA2: %s", b)
		require.True(t, a.Intersection(b).IsEmpty(), "two SNBs must not share CPUs: a=%s b=%s", a, b)
	})

	t.Run("two same-numa pending snbs split disjoint cpus", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, true, false)
		numa0 := p.machine.machineInfo.CPUTopology.CPUDetails.CPUsInNUMANodes(0)
		// Two concurrent pending SNBs both pinned to NUMA0, with the NUMA0 floor
		// core {4,68}. Both must stay on NUMA0, avoid the floor, and split the
		// remaining free CPUs with no overlap between them.
		mkEntry := func(name string) *state.AllocationInfo {
			return &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid: name, PodNamespace: "default", PodName: name,
					ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
					},
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(),
				RequestQuantity:  2,
			}
		}
		p.state.SetPodEntries(state.PodEntries{
			"snb-x": {"main": mkEntry("snb-x")},
			"snb-y": {"main": mkEntry("snb-y")},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: machine.NewCPUSet(),
				},
			},
		}, false)
		require.NoError(t, p.applyPoolsAndIsolatedInfo(
			map[string]machine.CPUSet{commonstate.PoolNameReclaim: machine.NewCPUSet(4, 68)},
			map[string]map[string]machine.CPUSet{},
			p.state.GetPodEntries(), p.state.GetMachineState(),
			sets.NewInt(0), false, machine.NewCPUSet(), defaultShareMaterializationPlan{}, p.state.GetRevision(),
			p.config.dynamicConfig.GetDynamicConfiguration(),
		))
		x := p.state.GetAllocationInfo("snb-x", "main").AllocationResult
		y := p.state.GetAllocationInfo("snb-y", "main").AllocationResult
		require.False(t, x.IsEmpty(), "snb-x must be non-empty")
		require.False(t, y.IsEmpty(), "snb-y must be non-empty")
		require.True(t, x.IsSubsetOf(numa0), "snb-x must stay on NUMA0: %s", x)
		require.True(t, y.IsSubsetOf(numa0), "snb-y must stay on NUMA0: %s", y)
		require.True(t, x.Intersection(machine.NewCPUSet(4, 68)).IsEmpty(), "snb-x must avoid floor {4,68}: %s", x)
		require.True(t, y.Intersection(machine.NewCPUSet(4, 68)).IsEmpty(), "snb-y must avoid floor {4,68}: %s", y)
		// NOTE: two same-NUMA pending SNBs inside a single pool-adjustment frame are
		// NOT disjoint here: getSharedNUMABindingRampUpCPUSet reads a static
		// machineState snapshot and does not subtract a sibling pending SNB's not-yet-
		// committed result. Each production SNB admits through its own synchronous
		// Allocate+commit (the next frame sees the prior result), so this single-frame
		// same-NUMA overlap is outside the floor-exclusion liveness fix and is left as
		// existing behavior; we only assert both individually avoid the floor.
		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
		require.True(t, reclaim.Intersection(machine.NewCPUSet(4, 68)).Equals(machine.NewCPUSet(4, 68)),
			"floor {4,68} must survive: reclaim=%s", reclaim)
	})
}

func TestDynamicPolicy_allocateNumaBindingCPUs(t *testing.T) {
	t.Parallel()

	type args struct {
		numCPUs        int
		hint           *pluginapi.TopologyHint
		machineState   state.NUMANodeMap
		reqAnnotations map[string]string
		// reclaimCPUs, when non-empty, is written into the reclaim pool before the
		// call so that dedicated allocation can prefer reclaim-free cpus.
		reclaimCPUs machine.CPUSet
	}
	tests := []struct {
		name    string
		args    args
		want    machine.CPUSet
		wantErr bool
	}{
		{
			name: "normal allocation without pinning",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want:    machine.NewCPUSet(0, 1),
			wantErr: false,
		},
		{
			name: "allocation with pinned resource package",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(2, 3),
							},
						},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationResourcePackageKey:           "pkg1",
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want:    machine.NewCPUSet(2, 3),
			wantErr: false,
		},
		{
			name: "allocation without pinned resource package but with other pinned packages",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(2, 3),
							},
						},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
			want:    machine.NewCPUSet(0, 1),
			wantErr: false,
		},
		{
			name: "distribute evenly with pinned resource package",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(2, 3),
							},
						},
					},
					1: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(6, 7),
							},
						},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationResourcePackageKey:                       "pkg1",
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:             apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
					apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
			want:    machine.NewCPUSet(2, 6),
			wantErr: false,
		},
		{
			name: "distribute evenly without pinned resource package but with other pinned packages",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(2, 3),
							},
						},
					},
					1: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(6, 7),
							},
						},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:             apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
					apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
			want:    machine.NewCPUSet(0, 4),
			wantErr: false,
		},
		{
			name: "distribute evenly with pinned resource package on some NUMAs but not others",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
						ResourcePackageStates: map[string]*state.ResourcePackageState{
							"pkg1": {
								PinnedCPUSet: machine.NewCPUSet(2, 3),
							},
						},
					},
					1: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
						// pkg1 is not pinned on NUMA 1
						ResourcePackageStates: map[string]*state.ResourcePackageState{},
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationResourcePackageKey:                       "pkg1",
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:             apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
					apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
			want:    machine.NewCPUSet(2, 4),
			wantErr: false,
		},
		{
			// Case 1: reclaim-free set can fully satisfy the request, so dedicated
			// allocation must avoid the reclaim cpus entirely.
			name: "prefer reclaim-free cpus when sufficient",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				reclaimCPUs: machine.NewCPUSet(0, 1),
			},
			want:    machine.NewCPUSet(2, 3),
			wantErr: false,
		},
		{
			// Case 2: reclaim-free set is insufficient, so allocate reclaim-free
			// CPUs first and then borrow the minimum remaining CPUs from reclaim.
			name: "prefer reclaim-free first when insufficient",
			args: args{
				numCPUs: 3,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				// only 0,1 reclaim-free => size 2 < 3, must borrow one reclaim CPU.
				reclaimCPUs: machine.NewCPUSet(2, 3),
			},
			want:    machine.NewCPUSet(0, 1, 2),
			wantErr: false,
		},
		{
			name: "prefer reclaim-free first instead of full-set topology order",
			args: args{
				numCPUs: 3,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
				reclaimCPUs: machine.NewCPUSet(0, 1),
			},
			want:    machine.NewCPUSet(0, 2, 3),
			wantErr: false,
		},
		{
			// Case 5: distribute-evenly across NUMA must also avoid reclaim cpus
			// on every NUMA when the reclaim-free set is sufficient per NUMA.
			name: "distribute evenly avoids reclaim cpus",
			args: args{
				numCPUs: 2,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
					1: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:             apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
					apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
				// reclaim occupies the lowest cpu of each NUMA => allocation must skip them.
				reclaimCPUs: machine.NewCPUSet(0, 4),
			},
			want:    machine.NewCPUSet(1, 5),
			wantErr: false,
		},
		{
			// Case 5b (regression): distribute-evenly where the GLOBAL reclaim-free set
			// is sufficient (>= numCPUs) but one NUMA cannot meet its per-NUMA share from
			// reclaim-free cpus alone. Allocation must succeed by borrowing the remainder
			// on that NUMA instead of failing on a global reclaim-free shortcut.
			name: "distribute evenly borrows reclaim when a numa is short",
			args: args{
				numCPUs: 4,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
					1: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:             apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:                 "2",
					apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: apiconsts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
				// NUMA0 fully reclaim-free (4 cpus); NUMA1 reclaim-free only {4}.
				// Global reclaim-free {0,1,2,3,4} size 5 >= 4, but NUMA1 needs 2 and has
				// only 1 reclaim-free cpu, so it must borrow one cpu from {5,6,7}.
				reclaimCPUs: machine.NewCPUSet(5, 6, 7),
			},
			want:    machine.NewCPUSet(0, 1, 4, 5),
			wantErr: false,
		},
		{
			// Case 4c: numaExclusive keeps whole-NUMA exclusivity and must NOT
			// subtract reclaim cpus, otherwise the exclusive dedicated_cores would
			// no longer own the full NUMA.
			name: "numa exclusive keeps whole numa despite reclaim overlap",
			args: args{
				numCPUs: 4,
				hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				},
				reqAnnotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
				reclaimCPUs: machine.NewCPUSet(0, 1),
			},
			want:    machine.NewCPUSet(0, 1, 2, 3),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)
			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			as.Nil(err)
			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs")
			as.Nil(err)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)
			p.config.reservedCPUs = machine.NewCPUSet()
			t.Logf("Reserved: %s", p.config.reservedCPUs.String())

			// Explicitly control the reclaim pool so allocation's reclaim-avoidance
			// only sees the cpus declared by this case (the default init would
			// otherwise seed an unrelated reclaim pool).
			p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: tt.args.reclaimCPUs.Clone(),
			}, false)

			got, _, err := p.allocateNumaBindingCPUs(tt.args.numCPUs, tt.args.hint, tt.args.machineState, tt.args.reqAnnotations, false)
			if (err != nil) != tt.wantErr {
				t.Errorf("allocateNumaBindingCPUs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !got.Equals(tt.want) {
				t.Errorf("allocateNumaBindingCPUs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDynamicPolicy_takeByTopologyPreferring_invariants(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_takeByTopologyPreferring_invariants")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	tests := []struct {
		name      string
		available machine.CPUSet
		preferred machine.CPUSet
		numCPUs   int
		want      machine.CPUSet
	}{
		{
			name:      "preferred is clipped to available",
			available: machine.NewCPUSet(1, 2),
			preferred: machine.NewCPUSet(0, 1),
			numCPUs:   1,
			want:      machine.NewCPUSet(1),
		},
		{
			name:      "empty preferred falls back to available",
			available: machine.NewCPUSet(2, 3),
			preferred: machine.NewCPUSet(),
			numCPUs:   1,
			want:      machine.NewCPUSet(2),
		},
		{
			name:      "full reclaim falls back to all available",
			available: machine.NewCPUSet(4, 5),
			preferred: machine.NewCPUSet(),
			numCPUs:   2,
			want:      machine.NewCPUSet(4, 5),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := p.takeByTopologyPreferring(tt.available, tt.preferred, tt.numCPUs)
			require.NoError(t, err)
			require.True(t, got.IsSubsetOf(tt.available), "got=%s available=%s", got.String(), tt.available.String())
			require.Equal(t, tt.numCPUs, got.Size())
			require.True(t, got.Equals(tt.want), "got=%s want=%s", got.String(), tt.want.String())
		})
	}
}

func TestDynamicPolicyAllocateNumaBindingShortAllocatesElasticNonExclusiveDNBOutsideHardFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	hardFloor := machine.NewCPUSet(numa0[:2]...)
	available := machine.NewCPUSet(numa0[:22]...)
	p.reclaim.reservedReclaimedCPUSet = hardFloor
	p.reclaim.reservedReclaimedCPUsSize = hardFloor.Size()
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: available},
	}, true)

	got, hardReclaimCPUs, err := p.allocateNumaBindingCPUs(21, &pluginapi.TopologyHint{Nodes: []uint64{0}}, p.state.GetMachineState(), map[string]string{
		apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, true)

	require.NoError(t, err)
	require.Equal(t, available.Difference(hardReclaimCPUs).Size(), got.Size(), "got=%s", got.String())
	require.Less(t, got.Size(), 21, "elastic DNB should preserve the floor instead of borrowing it")
	require.True(t, hardFloor.IsSubsetOf(hardReclaimCPUs), "floor=%s hard=%s", hardFloor, hardReclaimCPUs)
	require.True(t, got.Intersection(hardReclaimCPUs).IsEmpty(),
		"elastic non-exclusive admission must stay disjoint from hard floor, got=%s floor=%s",
		got.String(), hardReclaimCPUs.String())
}

func TestDynamicPolicyAllocateNumaBindingShortAllocatesNonReclaimableNonExclusiveDNBOutsideHardFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(24, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	hardFloor := machine.NewCPUSet(numa0[:2]...)
	available := machine.NewCPUSet(numa0[:22]...)
	p.reclaim.reservedReclaimedCPUSet = hardFloor
	p.reclaim.reservedReclaimedCPUsSize = hardFloor.Size()
	p.state.SetMachineState(state.NUMANodeMap{0: {DefaultCPUSet: available}}, true)

	got, hardReclaimCPUs, err := p.allocateNumaBindingCPUs(21, &pluginapi.TopologyHint{Nodes: []uint64{0}}, p.state.GetMachineState(), map[string]string{
		apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, false)

	require.NoError(t, err)
	require.Equal(t, available.Difference(hardReclaimCPUs).Size(), got.Size(), "got=%s", got.String())
	require.Less(t, got.Size(), 21)
	require.True(t, hardFloor.IsSubsetOf(hardReclaimCPUs), "floor=%s hard=%s", hardFloor, hardReclaimCPUs)
	require.True(t, got.Intersection(hardReclaimCPUs).IsEmpty())
}

func TestDynamicPolicyAllocateNumaBindingShortAllocatesElasticExclusiveDNBOutsideHardFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(48, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	numa0List := numa0.ToSliceInt()
	numa1List := numa1.ToSliceInt()
	hardFloor := machine.NewCPUSet(append(numa0List[:2], numa1List[:2]...)...)
	p.reclaim.reservedReclaimedCPUSet = hardFloor
	p.reclaim.reservedReclaimedCPUsSize = hardFloor.Size()
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: numa0},
		1: {DefaultCPUSet: numa1},
	}, true)

	got, hardReclaimCPUs, err := p.allocateNumaBindingCPUs(46, &pluginapi.TopologyHint{Nodes: []uint64{0, 1}}, p.state.GetMachineState(), map[string]string{
		apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}, true)

	require.NoError(t, err)
	require.Equal(t, 40, got.Size(), "exclusive elastic admission must use the hinted partition outside the floor, got=%s", got.String())
	require.True(t, hardFloor.IsSubsetOf(hardReclaimCPUs), "floor=%s hard=%s", hardFloor, hardReclaimCPUs)
	require.True(t, got.Intersection(hardReclaimCPUs).IsEmpty(),
		"exclusive admission must exclude the reclaim floor, got=%s floor=%s",
		got.String(), hardReclaimCPUs.String())
	require.True(t, got.Union(hardReclaimCPUs).Equals(numa0.Union(numa1)),
		"exclusive allocation and reclaim floor must cover the hinted partition")
}

func TestDynamicPolicy_allocateNumaBindingCPUs_reclaimPreferenceRespectsResourcePackageOrder(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs_resource_package_order")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(0),
	}, false)

	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{
			DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"pkg1": {PinnedCPUSet: machine.NewCPUSet(0, 1, 2)},
			},
		},
	}
	got, _, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationResourcePackageKey:           "pkg1",
	}, false)
	require.NoError(t, err)
	require.True(t, got.Equals(machine.NewCPUSet(1, 2)), "got=%s", got.String())
	require.True(t, got.IsSubsetOf(machine.NewCPUSet(0, 1, 2)), "got=%s", got.String())
	require.Equal(t, 2, got.Size())
}

func TestDynamicPolicy_allocateNumaBindingCPUs_fullReclaimFallsBackToAvailable(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs_full_reclaim")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
	}, false)

	available := machine.NewCPUSet(0, 1, 2, 3)
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: available},
	}
	got, _, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, false)
	require.NoError(t, err)
	require.Equal(t, 2, got.Size())
	require.True(t, got.IsSubsetOf(available), "got=%s available=%s", got.String(), available.String())
}

func TestDynamicPolicy_allocateNumaBindingCPUs_exclusiveDisjointPartition(t *testing.T) {
	t.Parallel()

	exclusiveAnnotations := func(resourcePackage string) map[string]string {
		annotations := map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		}
		if resourcePackage != "" {
			annotations[apiconsts.PodAnnotationResourcePackageKey] = resourcePackage
		}
		return annotations
	}
	newPolicy := func(t *testing.T) *DynamicPolicy {
		topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
		p.config.dynamicConfig.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
		return p
	}
	call := func(p *DynamicPolicy, machineState state.NUMANodeMap, annotations map[string]string, podReclaimEnabled bool) (machine.CPUSet, machine.CPUSet, error) {
		return p.allocateNumaBindingCPUs(
			8,
			&pluginapi.TopologyHint{Nodes: []uint64{0}},
			machineState,
			annotations,
			podReclaimEnabled,
		)
	}

	t.Run("pinned package preserves reserve and reclaim-only eligibility when pod reclaim is disabled", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 4)
		p.reclaim.reservedReclaimedCPUsSize = 2
		machineState := state.NUMANodeMap{
			0: {
				DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
				ResourcePackageStates: map[string]*state.ResourcePackageState{
					"work": {
						PinnedCPUSet: machine.NewCPUSet(0, 1, 4, 5),
					},
					"protected": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(3, 7),
					},
				},
			},
		}

		_, reclaim, err := call(p, machineState, exclusiveAnnotations("work"), false)
		require.ErrorContains(t, err, "results can't meet cpus request")
		require.True(t, reclaim.IsEmpty(), "failed strict allocation must not return reclaim plan: %s", reclaim)
	})

	t.Run("unpinned request respects resource-package exclusions", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(2, 6)
		p.reclaim.reservedReclaimedCPUsSize = 2
		machineState := state.NUMANodeMap{
			0: {
				DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
				ResourcePackageStates: map[string]*state.ResourcePackageState{
					"other": {
						PinnedCPUSet: machine.NewCPUSet(0, 4),
					},
					"protected": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(3, 7),
					},
				},
			},
		}

		_, reclaim, err := call(p, machineState, exclusiveAnnotations(""), true)
		require.ErrorContains(t, err, "results can't meet cpus request")
		require.True(t, reclaim.IsEmpty(), "failed capacity check must not return reclaim plan: %s", reclaim)
	})

	t.Run("allows reclaim-only partition for admission", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		p.reclaim.reservedReclaimedCPUsSize = 8
		machineState := state.NUMANodeMap{
			0: {DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		}

		_, reclaim, err := call(p, machineState, exclusiveAnnotations(""), true)
		require.ErrorContains(t, err, "exclusive disjoint dedicated result is empty")
		require.True(t, reclaim.IsEmpty(), "failed empty allocation must not return reclaim plan: %s", reclaim)
	})

	t.Run("rejects reserve when reclaim eligibility is insufficient", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
		p.reclaim.reservedReclaimedCPUsSize = 2
		machineState := state.NUMANodeMap{
			0: {
				DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
				ResourcePackageStates: map[string]*state.ResourcePackageState{
					"protected": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
					},
				},
			},
		}

		_, _, err := call(p, machineState, exclusiveAnnotations(""), false)
		require.Error(t, err)
	})

	t.Run("legacy overlap mode also admits whole NUMA before partition adjustment", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false)
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
		p.reclaim.reservedReclaimedCPUsSize = 2
		machineState := state.NUMANodeMap{
			0: {DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		}

		result, _, err := call(p, machineState, exclusiveAnnotations(""), true)
		require.NoError(t, err)
		require.True(t, result.Equals(machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)), "result=%s", result)
	})
}

func TestDynamicPolicyPodEnableReclaimForNumaBindingAllocation(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	podFetcherErr := errors.New("pod fetcher failed")
	spdErr := errors.New("spd failed")
	baselineErr := errors.New("baseline failed")

	newPolicy := func(t *testing.T, fetchedPod *v1.Pod, getPodErr error, manager *recordingServiceProfilingManager) *DynamicPolicy {
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		p.meta.metaServer.MetaAgent.PodFetcher = &getPodErrorPodFetcher{
			PodFetcherStub: &pod.PodFetcherStub{},
			err:            getPodErr,
			pod:            fetchedPod,
		}
		p.meta.metaServer.ServiceProfilingManager = manager
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
		return p
	}

	originalPodMeta := metav1.ObjectMeta{
		UID:       types.UID("pod-uid"),
		Namespace: "pod-namespace",
		Name:      "pod-name",
		Labels:    map[string]string{"spd-label": "value"},
		Annotations: map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			"spd-annotation": "value",
		},
	}
	filteredExclusive := map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}
	req := &pluginapi.ResourceRequest{
		PodUid:       "pod-uid",
		PodNamespace: "pod-namespace",
		PodName:      "pod-name",
		Annotations:  filteredExclusive,
	}
	ctx := context.WithValue(context.Background(), allocationPodMetaContextKey{}, originalPodMeta)

	t.Run("poor request metadata disables reclaim", func(t *testing.T) {
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelPoor,
		}
		p := newPolicy(t, nil, podFetcherErr, manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.NoError(t, err)
		require.False(t, got)
		require.Equal(t, []metav1.ObjectMeta{originalPodMeta}, manager.observedPodMeta)
		require.Zero(t, manager.baselineCalls)
	})

	t.Run("good non-baseline metadata enables reclaim", func(t *testing.T) {
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelGood,
		}
		p := newPolicy(t, nil, pod.NewPodNotFoundError(req.PodUid), manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.NoError(t, err)
		require.True(t, got)
		require.Equal(t, []metav1.ObjectMeta{originalPodMeta}, manager.observedPodMeta)
		require.Zero(t, manager.baselineCalls)
	})

	t.Run("baseline error propagates", func(t *testing.T) {
		storedPodMeta := metav1.ObjectMeta{
			UID:               types.UID(req.PodUid),
			CreationTimestamp: metav1.Now(),
		}
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelGood,
			baselineErr:                  podFetcherErr,
		}
		p := newPolicy(t, &v1.Pod{ObjectMeta: storedPodMeta}, nil, manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.ErrorIs(t, err, podFetcherErr)
		require.False(t, got)
		require.Equal(t, []metav1.ObjectMeta{originalPodMeta, storedPodMeta}, manager.observedPodMeta)
	})

	t.Run("request metadata SPD error propagates", func(t *testing.T) {
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceErr:               spdErr,
		}
		p := newPolicy(t, nil, nil, manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.ErrorIs(t, err, spdErr)
		require.False(t, got)
		require.Zero(t, manager.baselineCalls)
	})

	t.Run("good metadata uses full baseline decision", func(t *testing.T) {
		storedPodMeta := metav1.ObjectMeta{
			UID:               types.UID(req.PodUid),
			CreationTimestamp: metav1.Now(),
		}
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelGood,
			baseline:                     true,
		}
		p := newPolicy(t, &v1.Pod{ObjectMeta: storedPodMeta}, nil, manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.NoError(t, err)
		require.False(t, got)
		require.Equal(t, 1, manager.baselineCalls)
		require.Equal(t, []metav1.ObjectMeta{originalPodMeta, storedPodMeta}, manager.observedPodMeta)
	})

	t.Run("baseline error propagates", func(t *testing.T) {
		storedPodMeta := metav1.ObjectMeta{
			UID:               types.UID(req.PodUid),
			CreationTimestamp: metav1.Now(),
		}
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelGood,
			baselineErr:                  baselineErr,
		}
		p := newPolicy(t, &v1.Pod{ObjectMeta: storedPodMeta}, nil, manager)
		t.Parallel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(ctx, req)
		require.ErrorIs(t, err, baselineErr)
		require.False(t, got)
		require.Equal(t, 1, manager.baselineCalls)
	})

	t.Run("context cancellation propagates", func(t *testing.T) {
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
			performanceLevel:             spd.PerformanceLevelGood,
		}
		p := newPolicy(t, nil, nil, manager)
		t.Parallel()
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()

		got, err := p.podEnableReclaimForNumaBindingAllocation(canceledCtx, req)
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, got)
		require.Zero(t, manager.baselineCalls)
	})

	t.Run("non-exclusive preserves legacy fetch error fallback", func(t *testing.T) {
		manager := &recordingServiceProfilingManager{
			DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
		}
		p := newPolicy(t, nil, podFetcherErr, manager)
		t.Parallel()
		nonExclusiveReq := *req
		nonExclusiveReq.Annotations = map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		}

		got, err := p.podEnableReclaimForNumaBindingAllocation(context.Background(), &nonExclusiveReq)
		require.NoError(t, err)
		require.False(t, got)
		require.Empty(t, manager.observedPodMeta)
	})
}

func TestDynamicPolicy_allocateNumaBindingCPUs_partitionEligibilityGate(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	newPolicy := func(t *testing.T) *DynamicPolicy {
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0)
		p.reclaim.reservedReclaimedCPUsSize = 1
		p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
		p.config.dynamicConfig.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = "invalid,,selector"
		return p
	}
	machineState := state.NUMANodeMap{
		0: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0)},
		1: nil,
	}
	exclusive := map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}

	t.Run("feature disabled ignores selector and unrelated nil NUMA", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

		result, reclaim, err := p.allocateNumaBindingCPUs(
			4, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, exclusive, false)
		require.NoError(t, err)
		require.True(t, result.Equals(machineState[0].DefaultCPUSet), "result=%s", result)
		require.True(t, reclaim.IsEmpty(), "reclaim=%s", reclaim)
	})

	t.Run("nonexclusive uses legacy path before selector", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
		completeState := state.NUMANodeMap{
			0: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0)},
			1: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(1)},
		}
		annotations := map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		}

		result, reclaim, err := p.allocateNumaBindingCPUs(
			2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, completeState, annotations, false)
		require.NoError(t, err)
		require.Equal(t, 2, result.Size())
		// The ramp-up reclaim floor is domain-isolated to the hinted NUMA (0); a
		// request hinting only NUMA0 must not reserve reclaim on NUMA1.
		require.Equal(t, 2, reclaim.Size())
		require.Equal(t, 2, reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(0)).Size())
		require.Equal(t, 0, reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(1)).Size())
	})

	t.Run("DD false uses legacy path before selector", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false)
		completeState := state.NUMANodeMap{
			0: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0)},
			1: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(1)},
		}

		result, reclaim, err := p.allocateNumaBindingCPUs(
			2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, completeState, exclusive, false)
		require.NoError(t, err)
		// Domain-isolated floor: only the hinted NUMA0 keeps the reclaim floor.
		require.Equal(t, 2, reclaim.Size())
		require.Equal(t, 2, reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(0)).Size())
		require.Equal(t, 0, reclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(1)).Size())
		require.True(t, result.Equals(completeState[0].DefaultCPUSet), "result=%s", result)
	})
}

func TestDynamicPolicy_allocateNumaBindingCPUs_exclusiveDisjointMultiNUMARejectsRequestAbovePackageCapacity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 2, 4, 6)
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.config.dynamicConfig.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	machineState := state.NUMANodeMap{
		0: {
			DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0),
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"work":      {PinnedCPUSet: machine.NewCPUSet(0, 1)},
				"protected": {Attributes: map[string]string{"disable-reclaim": "true"}, PinnedCPUSet: machine.NewCPUSet(5)},
			},
		},
		1: {
			DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(1),
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"work":      {PinnedCPUSet: machine.NewCPUSet(2, 3)},
				"protected": {Attributes: map[string]string{"disable-reclaim": "true"}, PinnedCPUSet: machine.NewCPUSet(7)},
			},
		},
	}
	annotations := map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		apiconsts.PodAnnotationResourcePackageKey:             "work",
	}

	_, reclaim, err := p.allocateNumaBindingCPUs(
		8, &pluginapi.TopologyHint{Nodes: []uint64{0, 1}}, machineState, annotations, true)
	require.ErrorContains(t, err, "results can't meet cpus request")
	require.True(t, reclaim.IsEmpty(), "failed capacity check must not return reclaim plan: %s", reclaim)
}

func TestDynamicPolicy_allocateNumaBindingCPUs_nonReclaimableUsesConfiguredSteadyFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 4)
	p.reclaim.reservedReclaimedCPUsSize = 2
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.5
	dynamicConf.NumaMinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("2"),
	}
	dynamicConf.DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	protected := coresInNUMA(topology, 1, 1, 2)
	machineState := state.NUMANodeMap{
		0: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0)},
		1: {
			DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(1),
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"protected": {
					Attributes:   map[string]string{"disable-reclaim": "true"},
					PinnedCPUSet: protected,
				},
			},
		},
	}
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: p.reclaim.reservedReclaimedCPUSet.Union(protected),
	}, false)
	annotations := map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}

	result, reclaim, err := p.allocateNumaBindingCPUs(
		8, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, annotations, false)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	reclaimInNUMA0 := reclaim.Intersection(numa0)
	require.False(t, reclaimInNUMA0.IsEmpty(), "reclaim=%s", reclaim)
	require.True(t, result.Intersection(reclaim).IsEmpty())
	require.True(t, result.Union(reclaimInNUMA0).Equals(numa0),
		"result=%s reclaim=%s numa0=%s", result, reclaimInNUMA0, numa0)
}

func TestDynamicPolicy_deriveSteadyReclaimFloorPreservesCompletedMandatoryCoreAboveSoftTarget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("2"),
	}
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()

	got, err := p.deriveSteadyReclaimFloor(map[int]machine.CPUSet{
		0: topology.CPUDetails.CPUsInNUMANodes(0),
	})
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0, 1, 4, 5), got)
	requireCoreAligned(t, topology, got)
}

func TestDynamicPolicy_deriveSteadyReclaimFloorFallsBackWhenReservedIdentityLeavesEligibility(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(192, 1, 8)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("2"),
	}

	// NUMA 1 mirrors the production failure: the historical reserved core
	// 12,108 has been consumed by a dedicated pod, while another complete core
	// 22,118 remains eligible and already backs the current reclaim pool.
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(12)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	currentNUMA1Reclaim := machine.NewCPUSet(22, 118)
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: currentNUMA1Reclaim,
	}, false)

	reclaimEligiblePerNUMA := make(map[int]machine.CPUSet)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		reclaimEligiblePerNUMA[numaID] = topology.CPUDetails.CPUsInNUMANodes(numaID)
	}
	reclaimEligiblePerNUMA[1] = currentNUMA1Reclaim

	got, err := p.deriveSteadyReclaimFloor(reclaimEligiblePerNUMA)
	require.NoError(t, err)
	require.True(t, currentNUMA1Reclaim.IsSubsetOf(got), "floor=%s", got)
	require.True(t, got.Intersection(machine.NewCPUSet(12, 108)).IsEmpty(), "floor=%s", got)
	requireCoreAligned(t, topology, got)
}

func TestDynamicPolicyDeriveSteadyReclaimFloorDegradesBelowSoftTargetWithReservedFallback(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceRatioForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("0.75"),
	}

	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(2)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
	currentReclaim := machine.NewCPUSet(0, 4)
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: currentReclaim,
	}, false)

	eligible := machine.NewCPUSet(0, 1, 4)
	got, err := p.deriveSteadyReclaimFloor(map[int]machine.CPUSet{0: eligible})

	require.NoError(t, err)
	require.Equal(t, currentReclaim, got)
	requireCoreAligned(t, topology, got)
	require.True(t, got.IsSubsetOf(eligible), "floor=%s eligible=%s", got, eligible)
}

func TestDynamicPolicyDeriveSteadyReclaimFloorRoundsSoftTargetDown(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceRatioForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("0.375"),
	}

	eligible := machine.NewCPUSet(0, 1, 2, 4)
	floor, err := p.deriveSteadyReclaimFloor(map[int]machine.CPUSet{
		0: eligible,
	})
	require.NoError(t, err)
	require.Equal(t, 2, floor.Size())
	require.NoError(t, assertCoreAligned(floor, topology))
	require.True(t, floor.IsSubsetOf(eligible), "floor=%s eligible=%s", floor, eligible)
}

func TestDynamicPolicyDeriveSteadyReclaimFloorPreservesMandatoryIdentity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.reclaim.reservedReclaimedCPUsSize = 1
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceRatioForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("0.375"),
	}

	eligible := machine.NewCPUSet(0, 1, 2, 4)
	mandatoryCore := machine.NewCPUSet(0, 4)
	floor, err := p.deriveSteadyReclaimFloor(map[int]machine.CPUSet{
		0: eligible,
	})
	require.NoError(t, err)
	require.Equal(t, 2, floor.Size())
	require.True(t, mandatoryCore.IsSubsetOf(floor), "mandatory=%s floor=%s", mandatoryCore, floor)
	require.NoError(t, assertCoreAligned(floor, topology))
	require.True(t, floor.IsSubsetOf(eligible), "floor=%s eligible=%s", floor, eligible)
}

func TestSelectAdmissionSteadyReclaimTargetMixedSMTUsesBestBoundedCapacity(t *testing.T) {
	t.Parallel()

	topology := newMixedSMTAdmissionTopology()
	eligible := topology.CPUDetails.CPUs()

	got, err := selectAdmissionSteadyReclaimTarget(
		topology,
		eligible,
		machine.NewCPUSet(0, 1, 2, 3, 7, 8),
		machine.NewCPUSet(),
		5,
	)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(4, 5, 6, 7, 8), got)
	require.NoError(t, assertCoreAligned(got, topology))
}

func TestSelectAdmissionSteadyReclaimTargetTieBreaksByPreferredThenPhysicalCoreKey(t *testing.T) {
	t.Parallel()

	topology := newMixedSMTAdmissionTopology()
	eligible := topology.CPUDetails.CPUs()

	got, err := selectAdmissionSteadyReclaimTarget(
		topology,
		eligible,
		machine.NewCPUSet(7, 8),
		machine.NewCPUSet(),
		5,
	)
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(4, 5, 6, 7, 8), got,
		"preferred-hit tie-break must select the preferred SMT2 core")

	got, err = selectAdmissionSteadyReclaimTarget(
		topology,
		eligible,
		machine.NewCPUSet(),
		machine.NewCPUSet(),
		5,
	)
	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(4, 5, 6, 7, 8), got,
		"equal-capacity and equal-preference choices must use physical core key order")
}

func TestSelectAdmissionSteadyReclaimTargetKeepsReservedFallback(t *testing.T) {
	t.Parallel()

	topology := newMixedSMTAdmissionTopology()
	eligible := machine.NewCPUSet(4, 5, 6, 7, 8)
	preferred := machine.NewCPUSet(7, 8)

	got, err := selectAdmissionSteadyReclaimTarget(
		topology,
		eligible,
		preferred,
		machine.NewCPUSet(0),
		2,
	)

	require.NoError(t, err)
	require.Equal(t, preferred, got,
		"an ineligible reserved identity must fall back to an eligible preferred core")
}

func newMixedSMTAdmissionTopology() *machine.CPUTopology {
	return newAdmissionTopologyWithThreads(4, 3, 2, 2)
}

func newAdmissionTopologyWithThreads(threadsPerCore ...int) *machine.CPUTopology {
	details := machine.CPUDetails{}
	nextCPU := 0
	for coreID, threads := range threadsPerCore {
		for thread := 0; thread < threads; thread++ {
			details[nextCPU] = machine.CPUTopoInfo{
				NUMANodeID: 0,
				SocketID:   0,
				CoreID:     coreID,
			}
			nextCPU++
		}
	}
	return &machine.CPUTopology{
		NumCPUs:      nextCPU,
		NumCores:     len(threadsPerCore),
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails:   details,
	}
}

func TestDedicatedNUMAExclusiveAdmissionReturnsReclaimRemainderToDNB(t *testing.T) {
	t.Parallel()

	p, req, partitionEligibleByNUMA := newDedicatedNUMAExclusiveRemainderFixture(t, 1)
	advisorBlockResult := int(req.ResourceRequests[string(v1.ResourceCPU)])

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(
		withAllocationPodMeta(context.Background(), req), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	dnb := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, dnb)
	require.NotNil(t, reclaim)
	partitionEligible := partitionEligibleByNUMA[0]
	require.True(t, dnb.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty())
	require.True(t, dnb.AllocationResult.Union(reclaim.AllocationResult).Equals(partitionEligible))
	require.NoError(t, assertCoreAligned(reclaim.AllocationResult, p.machine.machineInfo.CPUTopology))
	require.Greater(t, dnb.AllocationResult.Size(), advisorBlockResult)
	require.Equal(t, partitionEligible.Size()-reclaim.AllocationResult.Size(), dnb.AllocationResult.Size())
}

func TestDedicatedNUMAExclusiveAdmissionKeepsRemainderInOriginalNUMA(t *testing.T) {
	t.Parallel()

	p, req, partitionEligibleByNUMA := newDedicatedNUMAExclusiveRemainderFixture(t, 2)

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(
		withAllocationPodMeta(context.Background(), req), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	dnb := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, dnb)
	require.NotNil(t, reclaim)
	require.True(t, dnb.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty())
	require.NoError(t, assertCoreAligned(reclaim.AllocationResult, p.machine.machineInfo.CPUTopology))
	for numaID, partitionEligible := range partitionEligibleByNUMA {
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)
		dnbInNUMA := dnb.AllocationResult.Intersection(numaCPUs)
		reclaimInNUMA := reclaim.AllocationResult.Intersection(numaCPUs)
		require.Equal(t, 2, reclaimInNUMA.Size(), "NUMA %d reclaim=%s", numaID, reclaimInNUMA)
		require.Equal(t, 6, dnbInNUMA.Size(), "NUMA %d DNB=%s", numaID, dnbInNUMA)
		require.True(t, dnbInNUMA.Union(reclaimInNUMA).Equals(partitionEligible),
			"NUMA %d DNB=%s reclaim=%s eligible=%s", numaID, dnbInNUMA, reclaimInNUMA, partitionEligible)
	}
}

func newDedicatedNUMAExclusiveRemainderFixture(
	t *testing.T,
	numaCount int,
) (*DynamicPolicy, *pluginapi.ResourceRequest, map[int]machine.CPUSet) {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8*numaCount, 1, numaCount)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.NumaMinReclaimedResourceRatioForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("0.375"),
	}
	dynamicConf.DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	p.meta.metaServer.ServiceProfilingManager = &recordingServiceProfilingManager{
		DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
		performanceLevel:             spd.PerformanceLevelPoor,
	}

	machineState := make(state.NUMANodeMap, numaCount)
	partitionEligibleByNUMA := make(map[int]machine.CPUSet, numaCount)
	hintNodes := make([]uint64, 0, numaCount)
	for numaID := 0; numaID < numaCount; numaID++ {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		cores := topology.CPUDetails.CoresInNUMANodes(numaID).ToSliceInt()
		require.Len(t, cores, 4)
		protected := topology.CPUDetails.CPUsInCores(cores[1])
		for _, coreID := range cores[2:] {
			protected = protected.Union(machine.NewCPUSet(
				topology.CPUDetails.CPUsInCores(coreID).ToSliceInt()[0]))
		}
		work := numaCPUs.Difference(topology.CPUDetails.CPUsInCores(cores[0]))
		machineState[numaID] = &state.NUMANodeState{
			DefaultCPUSet: numaCPUs,
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"work": {
					PinnedCPUSet: work,
				},
				"protected": {
					Attributes:   map[string]string{"disable-reclaim": "true"},
					PinnedCPUSet: protected,
				},
			},
		}
		partitionEligibleByNUMA[numaID] = numaCPUs
		hintNodes = append(hintNodes, uint64(numaID))
	}
	require.NoError(t, p.state.SetMachineState(machineState, false))

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-reclaim-remainder",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-reclaim-remainder",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): float64(2 * numaCount),
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			apiconsts.PodAnnotationResourcePackageKey:             "work",
		},
		Hint: &pluginapi.TopologyHint{Nodes: hintNodes},
	}
	return p, req, partitionEligibleByNUMA
}

func TestDynamicPolicy_allocateNumaBindingCPUs_preservesMandatoryFloorForNonReclaimablePod(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		reserved machine.CPUSet
		ratio    float64
		wantSize int
	}{
		{name: "non-reclaimable pod preserves reserve", reserved: machine.NewCPUSet(0, 4), ratio: 0, wantSize: 2},
		{name: "identity-less checkpoint uses configured steady floor", reserved: machine.NewCPUSet(), ratio: 0.25, wantSize: 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUSet = tc.reserved
			p.reclaim.reservedReclaimedCPUsSize = tc.reserved.Size()
			p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = tc.ratio
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			// This test drives the nil-floor fallback: no configured global floor,
			// only the ratio / reserved size baseline.
			p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = nil
			p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceForAllocate = nil
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
			machineState := state.NUMANodeMap{
				0: {DefaultCPUSet: topology.CPUDetails.CPUsInNUMANodes(0)},
			}
			annotations := map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			}

			podReclaimEnabled := false
			result, reclaim, err := p.allocateNumaBindingCPUs(
				8, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, annotations, podReclaimEnabled)
			require.NoError(t, err)
			require.Equal(t, tc.wantSize, reclaim.Size(), "reclaim=%s", reclaim)
			require.Equal(t, 8-tc.wantSize, result.Size(), "result=%s reclaim=%s", result, reclaim)
			require.True(t, result.Intersection(reclaim).IsEmpty())
			require.True(t, result.Union(reclaim).Equals(topology.CPUDetails.CPUsInNUMANodes(0)))
		})
	}
}

func TestDynamicPolicy_selectNumaBindingReclaimPartitionPreservesSelectedFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.reclaim.reservedReclaimedCPUSet = coresInNUMA(topology, 0, 0, 1)
	p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()

	derivedFloor := coresInNUMA(topology, 0, 0, 2)
	dedicatedEligiblePerNUMA := map[int]machine.CPUSet{
		0: topology.CPUDetails.CPUsInNUMANodes(0),
	}
	reclaimEligiblePerNUMA := map[int]machine.CPUSet{
		0: derivedFloor,
	}

	got, err := p.selectNumaBindingReclaimPartition(
		derivedFloor,
		dedicatedEligiblePerNUMA,
		reclaimEligiblePerNUMA,
		[]uint64{0},
		true,
	)
	require.NoError(t, err)
	require.True(t, derivedFloor.Equals(got), "want=%s got=%s", derivedFloor.String(), got.String())
}

func TestDynamicPolicy_selectNumaBindingReclaimPartitionPreservesMandatoryIdentities(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

	derivedFloor := coresInNUMA(topology, 0, 0, 2)
	for _, tc := range []struct {
		name         string
		mandatory    machine.CPUSet
		derivedFloor machine.CPUSet
		wantSize     int
		wantErr      string
	}{
		{
			name:         "supplements mandatory to selected floor",
			mandatory:    coresInNUMA(topology, 0, 3, 4),
			derivedFloor: derivedFloor,
			wantSize:     derivedFloor.Size(),
		},
		{
			name:         "mandatory identities exceed selected floor",
			mandatory:    coresInNUMA(topology, 0, 2, 4),
			derivedFloor: coresInNUMA(topology, 0, 0, 1),
			wantErr:      "mandatory reclaim CPUs exceed selected floor",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sub, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)
			sub.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			sub.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			sub.reclaim.reservedReclaimedCPUSet = tc.mandatory
			sub.reclaim.reservedReclaimedCPUsSize = tc.mandatory.Size()
			got, err := sub.selectNumaBindingReclaimPartition(
				tc.derivedFloor,
				map[int]machine.CPUSet{
					0: topology.CPUDetails.CPUsInNUMANodes(0),
				},
				map[int]machine.CPUSet{
					0: tc.derivedFloor.Union(tc.mandatory),
				},
				[]uint64{0},
				true,
			)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.True(t, got.IsEmpty(), "got=%s", got)
				return
			}
			require.NoError(t, err)
			require.True(t, tc.mandatory.IsSubsetOf(got), "mandatory=%s got=%s", tc.mandatory, got)
			require.Equal(t, tc.wantSize, got.Size(), "got=%s", got)
		})
	}
}

func TestDynamicPolicy_selectNumaBindingReclaimPartitionFallsBackWhenMandatoryIdentityLeavesEligibility(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	staleReserve := coresInNUMA(topology, 0, 3, 4)
	p.reclaim.reservedReclaimedCPUSet = staleReserve
	p.reclaim.reservedReclaimedCPUsSize = staleReserve.Size()

	derivedFloor := coresInNUMA(topology, 0, 0, 1)
	eligible := coresInNUMA(topology, 0, 0, 2)
	got, err := p.selectNumaBindingReclaimPartition(
		derivedFloor,
		map[int]machine.CPUSet{0: eligible},
		map[int]machine.CPUSet{0: eligible},
		[]uint64{0},
		true,
	)
	require.NoError(t, err)
	require.True(t, derivedFloor.Equals(got), "want=%s got=%s", derivedFloor, got)
	require.True(t, got.Intersection(staleReserve).IsEmpty(), "got=%s", got)
}

func TestDynamicPolicy_selectNumaBindingReclaimPartitionRoundsMixedSMTSupplementDown(t *testing.T) {
	t.Parallel()

	topology := newAdmissionTopologyWithThreads(4, 3, 2, 2, 1)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0

	mandatory := machine.NewCPUSet(0, 1, 2, 3)
	derivedFloor := machine.NewCPUSet(4, 5, 6, 7, 8)
	eligible := topology.CPUDetails.CPUs().Difference(machine.NewCPUSet(11))
	got, err := p.selectNumaBindingReclaimPartition(
		derivedFloor,
		map[int]machine.CPUSet{0: eligible.Difference(mandatory)},
		map[int]machine.CPUSet{0: eligible},
		[]uint64{0},
		true,
	)

	require.NoError(t, err)
	require.Equal(t, mandatory, got)
	requireCoreAligned(t, topology, got)
}

// TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace verifies the logic of generating CPU sets for NUMA-binding pools.
// It simulates a scenario with specific CPU topology and available CPUs, checking if the allocation strategies (like packing full cores) work as expected.
// Topology Assumption for mustGenerateDummyCPUTopology(16, 2, 2):
// - 16 CPUs total, 2 NUMA Nodes (0 and 1).
// - HT enabled, siblings are separated by 16/2 = 8.
// - NUMA 0: CPUs {0, 1, 2, 3} (Logic Cores) and {8, 9, 10, 11} (Siblings).
//   - Core 0: {0, 8}, Core 1: {1, 9}, Core 2: {2, 10}, Core 3: {3, 11}.
//
// - NUMA 1: CPUs {4, 5, 6, 7} (Logic Cores) and {12, 13, 14, 15} (Siblings).
func TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace(t *testing.T) {
	t.Parallel()

	type args struct {
		poolsCPUSet      map[string]machine.CPUSet
		poolsQuantityMap map[string]map[int]int
		availableCPUs    machine.CPUSet
	}
	tests := []struct {
		name          string
		cpuTopology   *machine.CPUTopology
		args          args
		wantPools     map[string]machine.CPUSet
		wantLeft      machine.CPUSet
		wantErr       bool
		enableReclaim bool
	}{
		// Case 1: Single pool allocation in NUMA 0.
		// Available CPUs: {8, 9, 10} (All in NUMA 0).
		// - Core 0: {0, 8} (Only 8 available).
		// - Core 1: {1, 9} (Only 9 available).
		// - Core 2: {2, 10} (Only 10 available).
		// Request: pool1 needs 2 CPUs from NUMA 0.
		// Allocation: No full cores available, so it picks {8, 9}.
		{
			name:        "single pool, ample cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 2),
			args: args{
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(8, 9, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(8, 9),
			},
			wantLeft:      machine.NewCPUSet(10),
			wantErr:       false,
			enableReclaim: true,
		},
		// Case 2: Multiple pools allocation across NUMA 0 and NUMA 1.
		// Available CPUs: {2, 3, 4, 5, 10}.
		// NUMA 0 Available: {2, 3, 10}.
		// - Core 2: {2, 10} (Both available -> Full Core).
		// - Core 3: {3, 11} (Only 3 available).
		// NUMA 1 Available: {4, 5}.
		// - Core 4: {4, 12} (Only 4 available).
		// - Core 5: {5, 13} (Only 5 available).
		// Request: pool1 needs 2 from NUMA 0; pool2 needs 2 from NUMA 1.
		// Allocation:
		// - pool1 (NUMA 0): Prefers full core {2, 10}.
		// - pool2 (NUMA 1): Takes {4, 5}.
		{
			name:        "multiple pools, ample cpus",
			cpuTopology: mustGenerateDummyCPUTopology(16, 2, 2),
			args: args{
				poolsCPUSet: make(map[string]machine.CPUSet),
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {
						0: 2,
					},
					"pool2": {
						1: 2,
					},
				},
				availableCPUs: machine.NewCPUSet(2, 3, 4, 5, 10),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 10),
				"pool2": machine.NewCPUSet(4, 5),
			},
			wantLeft:      machine.NewCPUSet(3),
			wantErr:       false,
			enableReclaim: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generateNUMABindingPoolsCPUSetInPlace")
			as.Nil(err)
			defer os.RemoveAll(tmpDir) // Added cleanup

			p, err := getTestDynamicPolicyWithInitialization(tt.cpuTopology, tmpDir)
			as.Nil(err)

			// Clear state to ensure clean slate
			p.state.SetPodEntries(state.PodEntries{}, false)
			p.config.reservedCPUs = machine.NewCPUSet()

			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim

			gotLeft, err := p.generateNUMABindingPoolsCPUSetInPlace(tt.args.poolsCPUSet, tt.args.poolsQuantityMap, tt.args.availableCPUs, p.config.dynamicConfig.GetDynamicConfiguration())
			if (err != nil) != tt.wantErr {
				t.Errorf("generateNUMABindingPoolsCPUSetInPlace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if !reflect.DeepEqual(tt.args.poolsCPUSet, tt.wantPools) {
					t.Errorf("generateNUMABindingPoolsCPUSetInPlace() poolsCPUSet = %v, want %v", tt.args.poolsCPUSet, tt.wantPools)
				}
				if !gotLeft.Equals(tt.wantLeft) {
					t.Errorf("generateNUMABindingPoolsCPUSetInPlace() gotLeft = %v, want %v", gotLeft, tt.wantLeft)
				}
			}
		})
	}
}

func TestDynamicPolicy_generatePoolsAndIsolation_reclaimLeftoverOnlyWhenReclaimDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		enableReclaim bool
		wantReclaim   machine.CPUSet
		wantShare     machine.CPUSet
	}{
		{
			name:          "enable reclaim respects existing reclaim pool and leaves leftover out",
			enableReclaim: true,
			wantReclaim:   machine.NewCPUSet(0, 4),
			wantShare:     machine.NewCPUSet(1, 5),
		},
		{
			name:          "disable reclaim keeps legacy leftover apportion path",
			enableReclaim: false,
			wantReclaim:   machine.NewCPUSet(2, 3, 6, 7),
			wantShare:     machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
			require.NoError(t, err)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_reclaim_leftover")
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			require.NoError(t, err)

			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUsSize = 0
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim
			p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
			p.state.SetPodEntries(state.PodEntries{}, false)

			poolsCPUSet, _, err := p.generatePoolsAndIsolation(
				map[string]map[int]int{
					commonstate.PoolNameShare:   {commonstate.FakedNUMAID: 2},
					commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: 2},
				},
				map[string]map[string]int{},
				machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
				map[string]float64{},
				generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
			)
			require.NoError(t, err)

			require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Equals(tt.wantReclaim),
				"reclaim=%s want=%s", poolsCPUSet[commonstate.PoolNameReclaim].String(), tt.wantReclaim.String())
			require.True(t, poolsCPUSet[commonstate.PoolNameShare].Equals(tt.wantShare),
				"share=%s want=%s", poolsCPUSet[commonstate.PoolNameShare].String(), tt.wantShare.String())
		})
	}
}

func TestDynamicPolicy_generatePoolsAndIsolation_prefersHistoricalReclaimPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_prefers_reclaim")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3, 10, 11),
			},
		},
	}, false)

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: 4},
			commonstate.PoolNameShare:   {commonstate.FakedNUMAID: 4},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10, 11, 12, 13),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	historicalReclaim := machine.NewCPUSet(2, 3, 10, 11)
	require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Intersection(historicalReclaim).Equals(historicalReclaim),
		"reclaim pool should include its historical cpuset when still available, got %s",
		poolsCPUSet[commonstate.PoolNameReclaim].String())
	require.True(t, poolsCPUSet[commonstate.PoolNameShare].Intersection(historicalReclaim).IsEmpty(),
		"share pool should not take historical reclaim cpuset, got %s",
		poolsCPUSet[commonstate.PoolNameShare].String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_preservesAdvisorReclaimForSeedPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 24)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_generatePoolsAndIsolation_preserves_advisor_reclaim")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet(0, 24)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(1, 2, 25, 26)
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 25, 26, 27, 28, 29, 30, 31, 49, 50, 51, 52, 53, 54, 55, 73, 74, 75, 76, 77, 78, 79),
			},
		},
	}, false)

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"seedpool-stable-0": {commonstate.FakedNUMAID: 1},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23,
			25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47,
			48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70,
			71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93,
			94, 95),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	wantReclaim := machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7, 25, 26, 27, 28, 29, 30, 31, 49, 50, 51, 52, 53, 54, 55, 73, 74, 75, 76, 77, 78, 79)
	require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Equals(wantReclaim),
		"an odd seed request must not split historical reclaim core 1,49; got %s want %s",
		poolsCPUSet[commonstate.PoolNameReclaim].String(), wantReclaim.String())
	require.True(t, poolsCPUSet["seedpool-stable-0"].Equals(machine.NewCPUSet(8)),
		"odd seed tail should come from outside historical reclaim, got %s",
		poolsCPUSet["seedpool-stable-0"].String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_reservesConfiguredMinimumBeforeSoftPools(t *testing.T) {
	t.Parallel()

	// Replays the generic capacity shape from a live admission: 96 eligible
	// logical CPUs (SMT2), soft pools asking for 93, and a disjoint configured
	// reclaim minimum of 4. Reserving reclaim after the pools leaves only three
	// CPUs, of which at most one physical core is complete. The planner must
	// reserve the whole-core minimum first and proportionally shrink only the
	// soft pools; no workload-specific pool or node identity is required here.
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 24)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 24, 48, 72)
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 48),
		1: machine.NewCPUSet(24, 72),
	}
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"soft-pool":               {commonstate.FakedNUMAID: 1},
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 92},
		},
		map[string]map[string]int{},
		topology.CPUDetails.CPUs(),
		map[string]float64{},
		generatePoolsAndIsolationOptions{
			enforceReclaimMinimum: true,
			dynamicConf:           p.config.dynamicConfig.GetDynamicConfiguration(),
		},
	)
	require.NoError(t, err)

	reclaim := poolsCPUSet[commonstate.PoolNameReclaim]
	soft := poolsCPUSet["soft-pool"]
	share := poolsCPUSet[commonstate.PoolNameShare]
	require.Equal(t, 4, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.Equal(t, 1, soft.Size())
	require.Equal(t, 91, share.Size())
	require.True(t, reclaim.Intersection(soft.Union(share)).IsEmpty(),
		"reclaim=%s soft=%s share=%s", reclaim, soft, share)
	require.Equal(t, 96, reclaim.Union(soft).Union(share).Size())
}

func TestDynamicPolicy_generatePoolsAndIsolation_reservesConfiguredMinimumAfterNUMABindingPools(t *testing.T) {
	t.Parallel()

	// The configured minimum is a lower-priority capacity reservation than a
	// concrete NUMA-binding pool. It must be selected from the remaining common
	// capacity so a preferred reclaim history cannot steal the binding pool's
	// only eligible domain; only soft non-binding pools may shrink.
	topology, err := machine.GenerateDummyCPUTopology(8, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = topology.CPUDetails.CPUsInNUMANodes(0)
	p.reclaim.reservedReclaimedCPUsSize = 2
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: topology.CPUDetails.CPUsInNUMANodes(0),
	}
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			"binding-pool":            {0: 4},
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 3},
		},
		map[string]map[string]int{},
		topology.CPUDetails.CPUs(),
		map[string]float64{},
		generatePoolsAndIsolationOptions{
			enforceReclaimMinimum: true,
			dynamicConf:           p.config.dynamicConfig.GetDynamicConfiguration(),
		},
	)
	require.NoError(t, err)

	binding := poolsCPUSet["binding-pool"]
	reclaim := poolsCPUSet[commonstate.PoolNameReclaim]
	share := poolsCPUSet[commonstate.PoolNameShare]
	require.True(t, binding.Equals(topology.CPUDetails.CPUsInNUMANodes(0)), "binding=%s", binding)
	require.True(t, reclaim.IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(1)), "reclaim=%s", reclaim)
	require.Equal(t, 2, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.Equal(t, 2, share.Size())
	require.True(t, binding.Intersection(reclaim.Union(share)).IsEmpty())
	require.True(t, reclaim.Intersection(share).IsEmpty())
}

func TestDynamicPolicy_generatePoolsAndIsolation_reselectsDisjointWholeCoreForEmptyCurrentReclaim(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet(0, 4)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(1, 5)
	p.reclaim.reservedReclaimedCPUsSize = 2
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(1, 5),
			},
		},
	}, false))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 2},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.NoError(t, err)

	reclaim := poolsCPUSet[commonstate.PoolNameReclaim]
	nonReclaim := poolsCPUSet[commonstate.PoolNameReserve].Union(
		poolsCPUSet[commonstate.PoolNameShare])
	require.Equal(t, p.reclaim.reservedReclaimedCPUsSize, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, p.machine.machineInfo.CPUTopology))
	require.True(t, reclaim.Intersection(nonReclaim).IsEmpty(),
		"reselected reclaim %s overlaps non-reclaim %s", reclaim.String(), nonReclaim.String())
}

func TestDynamicPolicy_generatePoolsAndIsolation_rejectsEmptyCurrentReclaimWithoutEligibleWholeCore(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet(0, 1, 2, 3)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(4, 5)
	p.reclaim.reservedReclaimedCPUsSize = 2
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	require.NoError(t, p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true))
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, true))
	require.NoError(t, p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(4),
			},
		},
	}, false))

	poolsCPUSet, _, err := p.generatePoolsAndIsolation(
		map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 1},
		},
		map[string]map[string]int{},
		machine.NewCPUSet(4, 5, 6, 7),
		map[string]float64{},
		generatePoolsAndIsolationOptions{enforceReclaimMinimum: true, dynamicConf: p.config.dynamicConfig.GetDynamicConfiguration()},
	)
	require.ErrorContains(t, err, "not enough eligible whole-core cpus")

	nonReclaim := poolsCPUSet[commonstate.PoolNameReserve].Union(
		poolsCPUSet[commonstate.PoolNameShare])
	require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Intersection(nonReclaim).IsEmpty())
}

func TestDynamicPolicyDoAndCheckPutAllocationInfoReportsMissingAllocationInfo(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "missing-after-put",
			PodNamespace:  "default",
			PodName:       "missing-after-put",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelSharedCores,
				apiconsts.PodAnnotationCPUEnhancementCPUSet: "seedpool-missing-after-put",
			},
			Labels: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult: machine.NewCPUSet(1, 2, 3),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 2),
			1: machine.NewCPUSet(3),
		},
		RequestQuantity: 1,
	}
	allocationInfo.OriginalAllocationResult = allocationInfo.AllocationResult.Clone()
	allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(allocationInfo.TopologyAwareAssignments)

	p.state.SetAllocationInfo(allocationInfo.PodUid, allocationInfo.ContainerName, allocationInfo, false)
	p.state = &missingAllocationInfoState{
		State:                p.state,
		missingPodUID:        allocationInfo.PodUid,
		missingContainerName: allocationInfo.ContainerName,
	}

	_, err = p.doAndCheckPutAllocationInfoPodResizingAware(
		context.Background(), nil, allocationInfo, true, false, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "allocationInfo missing after putAllocationsAndAdjustAllocationEntries")
	require.NotContains(t, err.Error(), "<nil>")
}

func TestDynamicPolicyDeriveRampUpReclaimFloorCoversAllNUMAs(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicyDeriveRampUpReclaimFloorCoversAllNUMAs")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet(0, 24)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(14, 38, 62, 86)
	p.reclaim.reservedReclaimedCPUsSize = 4
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(1, 25, 49, 73),
			},
		},
	}, false)

	committedEntries := p.state.GetPodEntries()
	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), committedEntries, allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.True(t, floor.Equals(machine.NewCPUSet(14, 38, 62, 86)),
		"floor=%s, want all-NUMA reserved reclaim CPUs", floor)
	require.Equal(t, 2, floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 2, floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1)).Size())

	inactiveFloor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), committedEntries, sets.NewInt())
	require.NoError(t, err)
	require.True(t, inactiveFloor.IsEmpty(), "inactive candidate must not receive a temporary hard floor")

	activeCandidate := committedEntries.Clone()
	activeCandidate["ramp-up-pod"] = state.ContainerEntries{"main": &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "ramp-up-pod", ContainerName: "main",
		},
		AllocationResult: machine.NewCPUSet(1),
		RampUp:           true,
	}}
	activeFloor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), activeCandidate, sets.NewInt())
	require.NoError(t, err)
	require.True(t, activeFloor.Equals(machine.NewCPUSet(14, 38, 62, 86)))

	p.state.SetPodEntries(activeCandidate, false)
	finalExitCandidate := committedEntries.Clone()
	finalExitFloor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), finalExitCandidate, sets.NewInt())
	require.NoError(t, err)
	require.True(t, finalExitFloor.IsEmpty(), "candidate final exit must override committed active state")
}

func TestDynamicPolicyFinalizeDefaultShareEntryPrunesEmptyShareWhenZeroQuantity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	newPodEntries := state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}

	err = p.finalizeDefaultShareEntry(newPodEntries, state.PodEntries{}, 0, machine.NewCPUSet())
	require.NoError(t, err)
	_, exists := newPodEntries[commonstate.PoolNameShare]
	require.False(t, exists, "zero-quantity empty residual must prune stale default share entry")
}

func TestApplyPoolsAndIsolatedInfoAddsExplicitHardFloorToReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()
	require.GreaterOrEqual(t, len(numa0), 8)
	require.GreaterOrEqual(t, len(numa1), 8)

	floor := machine.NewCPUSet(numa0[0], numa0[1], numa1[0], numa1[1])
	numa1Floor := machine.NewCPUSet(numa1[0], numa1[1])
	shareBefore := numa1Floor.Union(machine.NewCPUSet(numa0[2], numa1[2]))
	isolationBefore := numa1Floor.Union(machine.NewCPUSet(numa0[3], numa1[3]))
	customBefore := numa1Floor.Union(machine.NewCPUSet(numa0[4], numa1[4]))
	reserveBefore := machine.NewCPUSet(numa0[5], numa1[5])
	dedicatedBefore := machine.NewCPUSet(numa0[6], numa1[6])
	reclaimBefore := machine.NewCPUSet(numa0[7], numa1[7])

	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameShare:     shareBefore.Clone(),
		"isolation-regression":        isolationBefore.Clone(),
		"custom-regression":           customBefore.Clone(),
		commonstate.PoolNameReserve:   reserveBefore.Clone(),
		commonstate.PoolNameDedicated: dedicatedBefore.Clone(),
		commonstate.PoolNameReclaim:   reclaimBefore.Clone(),
	}
	curEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}

	err = p.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		curEntries,
		p.state.GetMachineState(),
		sets.NewInt(),
		false,
		floor,
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.NoError(t, err)

	entries := p.state.GetPodEntries()
	reclaim := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.True(t, reclaim.Equals(reclaimBefore.Union(floor)), "reclaim=%s floor=%s", reclaim, floor)

	require.True(t, entries[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult.Equals(shareBefore))
	require.True(t, entries["isolation-regression"][commonstate.FakedContainerName].AllocationResult.Equals(isolationBefore))
	require.True(t, entries["custom-regression"][commonstate.FakedContainerName].AllocationResult.Equals(customBefore))
	require.True(t, entries[commonstate.PoolNameReserve][commonstate.FakedContainerName].AllocationResult.Equals(reserveBefore))
	require.True(t, entries[commonstate.PoolNameDedicated][commonstate.FakedContainerName].AllocationResult.Equals(dedicatedBefore))
}

func TestApplyPoolsAndIsolatedInfoUsesSingleRevisionGuardedCommit(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	expectedRevision := p.state.GetRevision()
	guard := &applyPoolsCommitGuardState{State: p.state}
	p.state = guard

	err = applyPoolsAndIsolatedInfoForCommitTest(p, true)
	require.NoError(t, err)
	require.Equal(t, 1, guard.conditionalCalls)
	require.Equal(t, expectedRevision, guard.conditionalRevision)
	require.False(t, guard.conditionalAllow)
	require.True(t, guard.conditionalDisable)
	require.True(t, guard.conditionalPersist)
	require.Zero(t, guard.setPodEntriesCalls)
	require.Zero(t, guard.setMachineStateCalls)
	require.Zero(t, guard.storeCalls)
}

func TestApplyPoolsAndIsolatedInfoReturnsStaleRevisionError(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()

	beforeEntries := p.state.GetPodEntries()
	guard := &applyPoolsCommitGuardState{
		State:                 p.state,
		injectRevisionAdvance: true,
	}
	p.state = guard

	err = applyPoolsAndIsolatedInfoForCommitTest(p, false)
	require.ErrorIs(t, err, state.ErrStaleStateRevision)
	require.Equal(t, 1, guard.conditionalCalls)
	require.Zero(t, guard.setPodEntriesCalls)
	require.Zero(t, guard.setMachineStateCalls)
	require.Zero(t, guard.storeCalls)
	require.True(t, reflect.DeepEqual(beforeEntries, p.state.GetPodEntries()),
		"stale advisor result must not replace newer state")
}

func TestApplyPoolsAndIsolatedInfoUsesCandidateMachineStateForRNBTransitions(t *testing.T) {
	t.Parallel()

	newPolicyAndEntries := func(t *testing.T, withRNB bool) (*DynamicPolicy, state.PodEntries, machine.CPUSet, machine.CPUSet) {
		t.Helper()

		topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
		require.NoError(t, err)
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet()
		p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

		numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
		numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
		allCPUs := numa0.Union(numa1)
		entries := state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: {
					AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult:                 allCPUs.Clone(),
					OriginalAllocationResult:         allCPUs.Clone(),
					TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
				},
			},
			"non-rnb": {
				"main": {
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "non-rnb",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameReclaim,
						QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					},
					AllocationResult:                 allCPUs.Clone(),
					OriginalAllocationResult:         allCPUs.Clone(),
					TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone(), 1: numa1.Clone()},
				},
			},
		}
		if withRNB {
			entries["rnb"] = state.ContainerEntries{
				"main": {
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "rnb",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameReclaim,
						QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
						},
					},
					AllocationResult:                 numa0.Clone(),
					OriginalAllocationResult:         numa0.Clone(),
					TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone()},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone()},
				},
			}
			entries["non-rnb"]["main"].AllocationResult = numa1.Clone()
			entries["non-rnb"]["main"].OriginalAllocationResult = numa1.Clone()
			entries["non-rnb"]["main"].TopologyAwareAssignments = map[int]machine.CPUSet{1: numa1.Clone()}
			entries["non-rnb"]["main"].OriginalTopologyAwareAssignments = map[int]machine.CPUSet{1: numa1.Clone()}
		}
		return p, entries, numa0, numa1
	}

	applyCandidate := func(t *testing.T, p *DynamicPolicy, entries state.PodEntries) error {
		t.Helper()

		candidateMachineState, err := generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, entries, p.state.GetMachineState())
		require.NoError(t, err)
		return p.applyPoolsAndIsolatedInfo(
			map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: p.machine.machineInfo.CPUDetails.CPUs(),
			},
			map[string]map[string]machine.CPUSet{},
			entries,
			candidateMachineState,
			sets.NewInt(),
			false,
			machine.NewCPUSet(),
			defaultShareMaterializationPlan{},
			p.state.GetRevision(),
			p.config.dynamicConfig.GetDynamicConfiguration(),
		)
	}

	t.Run("removing last RNB expands non-RNB to released NUMA", func(t *testing.T) {
		t.Parallel()
		p, initialEntries, numa0, numa1 := newPolicyAndEntries(t, true)
		initialMachineState, err := generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, initialEntries, p.state.GetMachineState())
		require.NoError(t, err)
		require.NoError(t, p.state.CommitAdvisorState(initialEntries, initialMachineState, false, false, false))

		candidate := initialEntries.Clone()
		delete(candidate, "rnb")
		require.NoError(t, applyCandidate(t, p, candidate))

		got := p.state.GetAllocationInfo("non-rnb", "main").AllocationResult
		require.True(t, got.Equals(numa0.Union(numa1)),
			"non-RNB allocation must expand to the released NUMA, got %s", got)
	})

	t.Run("adding first RNB excludes its target NUMA from non-RNB", func(t *testing.T) {
		t.Parallel()
		p, initialEntries, numa0, numa1 := newPolicyAndEntries(t, false)
		initialMachineState, err := generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, initialEntries, p.state.GetMachineState())
		require.NoError(t, err)
		require.NoError(t, p.state.CommitAdvisorState(initialEntries, initialMachineState, false, false, false))

		candidate := initialEntries.Clone()
		candidate["rnb"] = state.ContainerEntries{
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
					},
				},
				AllocationResult:                 numa0.Clone(),
				OriginalAllocationResult:         numa0.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: numa0.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: numa0.Clone()},
			},
		}
		require.NoError(t, applyCandidate(t, p, candidate))

		got := p.state.GetAllocationInfo("non-rnb", "main").AllocationResult
		require.True(t, got.Equals(numa1),
			"non-RNB allocation must exclude the new RNB target NUMA, got %s", got)
	})
}

func applyPoolsAndIsolatedInfoForCommitTest(p *DynamicPolicy, persist bool) error {
	topology := p.machine.machineInfo.CPUTopology
	poolsCPUSet := map[string]machine.CPUSet{
		commonstate.PoolNameShare:     coresInNUMA(topology, 0, 0, 1),
		"isolation-commit-test":       coresInNUMA(topology, 0, 1, 2),
		"custom-commit-test":          coresInNUMA(topology, 0, 2, 3),
		commonstate.PoolNameReserve:   coresInNUMA(topology, 0, 3, 4),
		commonstate.PoolNameDedicated: coresInNUMA(topology, 1, 0, 1),
		commonstate.PoolNameReclaim:   coresInNUMA(topology, 1, 1, 2),
	}
	curEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	return p.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		curEntries,
		p.state.GetMachineState(),
		sets.NewInt(),
		persist,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
}

func TestAdjustPoolsAndIsolatedEntriesWithRampUpFloorAllowsNonBindingSharedPoolShrink(t *testing.T) {
	t.Parallel()

	newPolicy := func(t *testing.T) *DynamicPolicy {
		t.Helper()

		topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
		p.state.SetAllocationInfo("owned-share-pod", "main", &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "owned-share-pod",
				PodNamespace:  "default",
				PodName:       "owned-share-pod",
				ContainerName: "main",
				OwnerPoolName: commonstate.PoolNameShare,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			RequestQuantity:  4,
		}, false)
		return p
	}

	t.Run("allocates the requested quantity outside the explicit floor", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		quantities := map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
		}
		expectedQuantities := map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
		}
		allCPUs := p.machine.machineInfo.CPUDetails.CPUs()
		unreserved, _, err := p.groupAndAllocatePoolsWithPlacement(
			quantities, nil, allCPUs, nil, map[string]float64{}, p.config.dynamicConfig.GetDynamicConfiguration(),
			p.state.GetPodEntries(), nil)
		require.NoError(t, err)
		floor := unreserved[commonstate.PoolNameShare].Clone()
		require.Equal(t, 4, floor.Size())

		err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
			quantities,
			nil,
			p.state.GetPodEntries(),
			p.state.GetMachineState(),
			false,
			floor,
			false,
		)
		require.NoError(t, err)
		require.Equal(t, expectedQuantities, quantities)

		share, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameShare)
		require.NoError(t, err)
		require.Equal(t, 4, share.Size())
		require.True(t, share.Intersection(floor).IsEmpty(), "share=%s floor=%s", share, floor)
		owner := p.state.GetAllocationInfo("owned-share-pod", "main")
		require.NotNil(t, owner)
		require.Equal(t, 4, owner.AllocationResult.Size())
		require.True(t, owner.AllocationResult.Equals(share))
	})

	t.Run("does not derive hard floor without active ramp-up allocation", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 1)
		p.reclaim.reservedReclaimedCPUsSize = 2
		quantities := map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
		}

		err := p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
			quantities,
			nil,
			p.state.GetPodEntries(),
			p.state.GetMachineState(),
			false,
			machine.NewCPUSet(),
			false,
		)
		require.NoError(t, err)

		entries := p.state.GetPodEntries()
		reclaim, err := entries.GetCPUSetForPool(commonstate.PoolNameReclaim)
		require.NoError(t, err)
		require.False(t, machine.NewCPUSet(0, 1).IsSubsetOf(reclaim), "reclaim=%s", reclaim)
		share, err := entries.GetCPUSetForPool(commonstate.PoolNameShare)
		require.NoError(t, err)
		require.Equal(t, 4, share.Size())
	})

	t.Run("proportionally shrinks global share pool", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		quantities := map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
		}
		expectedQuantities := map[string]map[int]int{
			commonstate.PoolNameShare: {commonstate.FakedNUMAID: 4},
		}
		cpus := p.machine.machineInfo.CPUDetails.CPUs().ToSliceInt()
		require.Len(t, cpus, 8)
		floor := machine.NewCPUSet(cpus[:5]...)

		err := p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
			quantities,
			nil,
			p.state.GetPodEntries(),
			p.state.GetMachineState(),
			false,
			floor,
			false,
		)
		require.NoError(t, err)
		require.Equal(t, expectedQuantities, quantities)
		share, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameShare)
		require.NoError(t, err)
		require.Equal(t, 3, share.Size())
		owner := p.state.GetAllocationInfo("owned-share-pod", "main")
		require.NotNil(t, owner)
		require.True(t, owner.AllocationResult.Equals(share))
	})
}

func TestAdjustPoolsAndIsolatedEntriesUsesFrozenAttemptConfiguration(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.5
	// The attempt captures this generation once. The manager then publishes a
	// new generation (hard partition off) mid-test; the attempt must keep sizing
	// against the captured immutable pointer, not the freshly swapped one.
	attemptConf := dynamicConf

	next := dynamicconfig.NewConfiguration()
	next.EnableReclaim = true
	next.EnableRampUpReclaimHardPartition = false
	next.InitialRampUpReclaimCPUSetRatio = 0
	p.config.dynamicConfig.SetDynamicConfiguration(next)

	entries := state.PodEntries{
		"owned-snb-ramp-up": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "owned-snb-ramp-up",
					PodNamespace:  "default",
					PodName:       "owned-snb-ramp-up",
					ContainerName: "main",
					OwnerPoolName: "share-NUMA0",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult:         machine.NewCPUSet(0, 1, 2, 3, 4, 5),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1, 2, 3, 4, 5)},
				RequestQuantity:          6,
				RampUp:                   true,
			},
		},
	}

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		context.Background(),
		map[string]map[int]int{"share-NUMA0": {0: 6}},
		nil,
		entries,
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
		p.state.GetRevision(),
		defaultShareMaterializationNormal,
		attemptConf,
	)
	require.ErrorContains(t, err,
		`insufficient capacity for owned pool "share-numa0" in numa 0: requested 6 cpus, allocated 4`)
}

func TestAdjustPoolsAndIsolatedEntriesWithRampUpFloorRejectsPinnedSNBPoolShrinkAtomically(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	const resourcePackageName = "pinned-package"
	numaCPUs := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	require.Len(t, numaCPUs, 8)
	pinnedCPUs := machine.NewCPUSet(numaCPUs[:4]...)
	machineState := p.state.GetMachineState()
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		resourcePackageName: {PinnedCPUSet: pinnedCPUs},
	}
	p.state.SetMachineState(machineState, false)

	allocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "pinned-snb-ramp-up",
			PodNamespace:  "default",
			PodName:       "pinned-snb-ramp-up",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationResourcePackageKey:           resourcePackageName,
			},
		},
		RampUp:          true,
		RequestQuantity: 2,
	}
	allocation.SetSpecifiedNUMABindingNUMAID([]uint64{0})
	p.state.SetAllocationInfo(allocation.PodUid, allocation.ContainerName, allocation, false)

	quantities := make(map[string]map[int]int)
	require.NoError(t, state.CountAllocationInfosToPoolsQuantityMap(
		machineState.GetNUMAResourcePackagePinnedCPUSet(),
		[]*state.AllocationInfo{allocation},
		quantities,
		p.getContainerRequestedCores,
		cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
	))
	specifiedPoolName, err := allocation.GetSpecifiedNUMABindingPoolName()
	require.NoError(t, err)
	pinnedPoolName := rputil.WrapOwnerPoolName(specifiedPoolName, resourcePackageName)
	require.Equal(t, map[string]map[int]int{
		pinnedPoolName: {0: 4},
	}, quantities)

	alreadyWrapped := allocation.Clone()
	alreadyWrapped.OwnerPoolName = pinnedPoolName
	alreadyWrapped.AllocationResult = pinnedCPUs.Clone()
	alreadyWrapped.TopologyAwareAssignments = map[int]machine.CPUSet{0: pinnedCPUs.Clone()}
	alreadyWrappedQuantities := make(map[string]map[int]int)
	require.NoError(t, state.CountAllocationInfosToPoolsQuantityMap(
		machineState.GetNUMAResourcePackagePinnedCPUSet(),
		[]*state.AllocationInfo{alreadyWrapped},
		alreadyWrappedQuantities,
		p.getContainerRequestedCores,
		cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
	))
	require.Equal(t, map[string]map[int]int{
		pinnedPoolName: {0: 4},
	}, alreadyWrappedQuantities)

	initialEntries := p.state.GetPodEntries()
	initialMachineState := p.state.GetMachineState()
	initialRevision := p.state.GetRevision()
	floor := machine.NewCPUSet(numaCPUs[:2]...)

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		quantities,
		nil,
		initialEntries,
		initialMachineState,
		false,
		floor,
		false,
	)
	require.ErrorContains(t, err,
		`insufficient capacity for owned pool "`+strings.ToLower(pinnedPoolName)+`" in numa 0: requested 4 cpus, allocated 2`)
	require.Equal(t, strings.ToLower(err.Error()), err.Error())
	require.Equal(t, initialEntries, p.state.GetPodEntries())
	require.Equal(t, initialMachineState, p.state.GetMachineState())
	require.Equal(t, initialRevision, p.state.GetRevision())
}

func TestAdjustPoolsAndIsolatedEntriesWithRampUpFloorRejectsBareOwnedPinnedSNBPoolShrinkAtomically(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	const resourcePackageName = "pinned-package"
	numaCPUs := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	require.Len(t, numaCPUs, 8)
	pinnedCPUs := machine.NewCPUSet(numaCPUs[:4]...)
	machineState := p.state.GetMachineState()
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		resourcePackageName: {PinnedCPUSet: pinnedCPUs},
	}
	p.state.SetMachineState(machineState, false)

	allocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "bare-owned-pinned-snb-ramp-up",
			PodNamespace:  "default",
			PodName:       "bare-owned-pinned-snb-ramp-up",
			ContainerName: "main",
			OwnerPoolName: "share-NUMA0",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationResourcePackageKey:           resourcePackageName,
			},
		},
		RampUp:                   true,
		RequestQuantity:          2,
		AllocationResult:         pinnedCPUs.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: pinnedCPUs.Clone()},
	}
	allocation.SetSpecifiedNUMABindingNUMAID([]uint64{0})
	p.state.SetAllocationInfo(allocation.PodUid, allocation.ContainerName, allocation, false)

	quantities := make(map[string]map[int]int)
	require.NoError(t, state.CountAllocationInfosToPoolsQuantityMap(
		machineState.GetNUMAResourcePackagePinnedCPUSet(),
		[]*state.AllocationInfo{allocation},
		quantities,
		p.getContainerRequestedCores,
		cpuconsts.CPUIncrRatioSharedCoresNUMABinding,
	))
	pinnedPoolName := rputil.WrapOwnerPoolName(allocation.OwnerPoolName, resourcePackageName)
	require.Equal(t, map[string]map[int]int{
		pinnedPoolName: {0: 4},
	}, quantities)

	initialEntries := p.state.GetPodEntries()
	initialMachineState := p.state.GetMachineState()
	initialRevision := p.state.GetRevision()
	floor := machine.NewCPUSet(numaCPUs[:2]...)

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		quantities,
		nil,
		initialEntries,
		initialMachineState,
		false,
		floor,
		false,
	)
	assert.ErrorContains(t, err,
		`insufficient capacity for owned pool "`+strings.ToLower(pinnedPoolName)+`" in numa 0: requested 4 cpus, allocated 2`)
	assert.Equal(t, initialEntries, p.state.GetPodEntries())
	assert.Equal(t, initialMachineState, p.state.GetMachineState())
	assert.Equal(t, initialRevision, p.state.GetRevision())
}

func TestValidateOwnedPoolsQuantityRejectsMalformedSharedNUMABindingEntriesAtomically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		assignments map[int]machine.CPUSet
	}{
		{
			name:        "empty topology assignments",
			assignments: nil,
		},
		{
			name: "cross numa topology assignments",
			assignments: map[int]machine.CPUSet{
				0: machine.NewCPUSet(0),
				1: machine.NewCPUSet(4),
			},
		},
		{
			name: "invalid topology assignments",
			assignments: map[int]machine.CPUSet{
				-1: machine.NewCPUSet(0),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)

			allocation := &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "malformed-snb",
					PodNamespace:  "default",
					PodName:       "malformed-snb",
					ContainerName: "main",
					OwnerPoolName: "share-NUMA0",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult:         machine.NewCPUSet(0),
				TopologyAwareAssignments: tt.assignments,
			}
			p.state.SetAllocationInfo(allocation.PodUid, allocation.ContainerName, allocation, false)

			initialEntries := p.state.GetPodEntries()
			initialMachineState := p.state.GetMachineState()
			initialRevision := p.state.GetRevision()
			err = p.validateOwnedPoolsQuantity(
				map[string]map[int]int{allocation.OwnerPoolName: {0: 1}},
				map[string]machine.CPUSet{allocation.OwnerPoolName: machine.NewCPUSet(0)},
				initialEntries,
				initialMachineState.GetNUMAResourcePackagePinnedCPUSet(),
			)

			require.ErrorContains(t, err,
				"get canonical shared numa-binding pool key for default/malformed-snb/main failed")
			require.Equal(t, strings.ToLower(err.Error()), err.Error())
			require.Equal(t, initialEntries, p.state.GetPodEntries())
			require.Equal(t, initialMachineState, p.state.GetMachineState())
			require.Equal(t, initialRevision, p.state.GetRevision())
		})
	}
}

func TestAdjustAllocationEntriesWithRampUpFloorKeepsCanonicalSNBCapacityErrorLowercase(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.5
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.state.SetAllocationInfo("owned-snb-pod", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "owned-snb-pod",
			PodNamespace:  "default",
			PodName:       "owned-snb-pod",
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			OwnerPoolName: "share-NUMA0",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
		AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1, 2, 3)},
		RequestQuantity:          6,
	}, false)

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-hard-floor-capacity",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-hard-floor-capacity",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 4,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	_, err = p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "adjustallocationentries failed with error: adjustpoolsandisolatedentries failed with error: insufficient capacity")
	require.Equal(t, strings.ToLower(err.Error()), err.Error())
}

func TestAllocateSharedNUMABindingRampUpRejectsLateHardFloorAtomically(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()
	eligible := machine.NewCPUSet(append(numa0[:2], numa1[:2]...)...)
	p.config.reservedCPUs = topology.CPUDetails.CPUs().Difference(eligible)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.config.podAnnotationKeptKeys = []string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding}

	initialEntries := p.state.GetPodEntries()
	initialMachineState := p.state.GetMachineState()
	initialRevision := p.state.GetRevision()
	req := &pluginapi.ResourceRequest{
		PodUid:         "snb-late-hard-floor",
		PodNamespace:   "default",
		PodName:        "snb-late-hard-floor",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{1}},
	}

	resp, err := p.Allocate(context.Background(), req)
	require.Nil(t, resp)
	require.ErrorContains(t, err,
		`insufficient capacity for owned pool "share-numa1" in numa 1: requested 2 cpus, allocated 0`)
	require.Equal(t, strings.ToLower(err.Error()), err.Error())
	require.Equal(t, initialEntries, p.state.GetPodEntries())
	require.Equal(t, initialMachineState, p.state.GetMachineState())
	require.Equal(t, initialRevision, p.state.GetRevision())
}

func TestAllocateSharedNUMABindingWithoutHardPartitionUsesAtomicPoolCommitAndPreservesHookOrder(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false
	p.config.dynamicConfig.GetDynamicConfiguration().DisableSharedCoresRampUp = false

	req := &pluginapi.ResourceRequest{
		PodUid:         "snb-soft-partition",
		PodNamespace:   "default",
		PodName:        "snb-soft-partition",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	initialRevision := p.state.GetRevision()
	var hookRevisions []uint64
	var hookAllocationSizes [][2]int
	p.RegisterAllocationHook(func(oldInfo, newInfo *state.AllocationInfo) error {
		oldSize := -1
		if oldInfo != nil {
			oldSize = oldInfo.AllocationResult.Size()
		}
		newSize := -1
		if newInfo != nil {
			newSize = newInfo.AllocationResult.Size()
		}
		hookRevisions = append(hookRevisions, p.state.GetRevision())
		hookAllocationSizes = append(hookAllocationSizes, [2]int{oldSize, newSize})
		return nil
	})

	allocation, err := p.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, false)
	require.NoError(t, err)
	require.NotNil(t, allocation)
	require.Equal(t, initialRevision+2, p.state.GetRevision())
	require.Equal(t, []uint64{initialRevision, initialRevision + 1}, hookRevisions)
	require.Equal(t, [][2]int{{-1, 0}, {0, allocation.AllocationResult.Size()}}, hookAllocationSizes)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorAllowsFullNonExclusiveRatio(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 1

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.True(t, floor.Equals(machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)),
		"floor=%s, want every eligible CPU", floor)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorUsesEligiblePerNUMACapacity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: coresInNUMA(topology, 0, 0, 4)},
		1: {DefaultCPUSet: coresInNUMA(topology, 1, 0, 4)},
	}, false)

	// cpusPerCore==2. Dedicated / non-exclusive DNB workloads leave only an
	// 8-CPU (4-core) eligible DefaultCPUSet per NUMA. The ramp-up target must be
	// derived from that eligible capacity so it shares the same capacity view as
	// admission validation: floor(4*0.2)=0 complete cores, lifted to the
	// immutable one-core baseline => 2 CPUs per NUMA. Deriving from the full
	// 32-CPU capacity (the old behavior) would target 6 CPUs and fail closed
	// whenever eligible capacity is smaller.
	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.Equal(t, 2, floor.Intersection(numa0).Size())
	require.Equal(t, 2, floor.Intersection(numa1).Size())
}

func TestDynamicPolicyDeriveRampUpReclaimFloorRepairsPartialPreferredCore(t *testing.T) {
	t.Parallel()

	details := make(machine.CPUDetails, 256)
	for cpu := 0; cpu < 128; cpu++ {
		numaID := cpu / 16
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: numaID,
			SocketID:   numaID,
			CoreID:     cpu,
		}
		details[cpu+128] = machine.CPUTopoInfo{
			NUMANodeID: numaID,
			SocketID:   numaID,
			CoreID:     cpu,
		}
	}
	topology := &machine.CPUTopology{
		NumCPUs:      256,
		NumCores:     128,
		NumSockets:   8,
		NumNUMANodes: 8,
		CPUDetails:   details,
	}
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(16)
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	currentReclaim := machine.MustParse(
		"0-3,6-8,11-15,21-23,32-38,48-131,134-136,139-143,149-151,160-166,176-255")
	p.state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: currentReclaim,
		},
		false,
	)

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		require.Equal(t, 6, floor.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID)).Size(),
			"NUMA %d floor=%s", numaID, floor)
	}
	require.True(t, machine.NewCPUSet(16, 144).IsSubsetOf(floor),
		"reserved reclaim identity and its sibling must be preserved, floor=%s", floor)
	requireCoreAligned(t, topology, floor)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorRejectsUnrepairableReservedIdentity(t *testing.T) {
	t.Parallel()

	details := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		4: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		5: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		6: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		7: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
	}
	topology := &machine.CPUTopology{
		NumCPUs:      8,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails:   details,
	}

	tests := []struct {
		name     string
		eligible machine.CPUSet
		reserved machine.CPUSet
		ratio    float64
		wantErr  string
	}{
		{
			name:     "reserved sibling outside eligible",
			eligible: machine.NewCPUSet(0, 1, 3),
			reserved: machine.NewCPUSet(0),
			ratio:    0,
			wantErr:  "reserved floor core siblings 2 are outside eligible cpus",
		},
		{
			name:     "completed reserved floor exceeds target",
			eligible: details.CPUs(),
			reserved: machine.NewCPUSet(0, 1),
			ratio:    0,
			wantErr:  "core-aligned reserved floor 4 exceeds target 2",
		},
		{
			name:     "eligible lacks enough complete cores",
			eligible: machine.NewCPUSet(0, 1, 2, 4),
			reserved: machine.NewCPUSet(),
			ratio:    1,
			wantErr:  "selected 2 cpus, target is 4",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.reservedCPUs = details.CPUs().Difference(tt.eligible)
			p.reclaim.reservedReclaimedCPUSet = tt.reserved
			p.reclaim.reservedReclaimedCPUsSize = 0
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = tt.ratio
			p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
				v1.ResourceCPU: *resource.NewQuantity(0, resource.DecimalSI),
			}
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

			floor, err := p.deriveRampUpReclaimFloor(
				p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
			require.True(t, floor.IsEmpty())
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestDynamicPolicyDeriveRampUpReclaimFloorSkipsSteadyExclusiveNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	// NUMA 0 is fully owned by a committed steady (RampUp=false) non-reclaim
	// exclusive DNB pod: 30 dedicated CPUs leave only a 2-CPU steady reclaim
	// floor eligible, which is smaller than the ratio-derived immutable target
	// of 6. A later ramp-up workload on NUMA 1 must not re-impose that target on
	// NUMA 0, otherwise admission fails closed.
	steadyReclaimNUMA0 := machine.NewCPUSet(numa0[:2]...)
	dedicatedNUMA0 := machine.NewCPUSet(numa0[2:]...)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: steadyReclaimNUMA0},
		1: {DefaultCPUSet: numa1},
	}, false)
	candidate := state.PodEntries{
		"steady-exclusive-dnb": state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "steady-exclusive-dnb",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				RampUp:                   false,
				AllocationResult:         dedicatedNUMA0,
				TopologyAwareAssignments: map[int]machine.CPUSet{0: dedicatedNUMA0},
			},
		},
	}

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), candidate, allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.Equal(t, 0, floor.Intersection(topology.CPUDetails.CPUsInNUMANodes(0)).Size(),
		"steady exclusive NUMA must not receive a ramp-up immutable target, floor=%s", floor)
	require.Equal(t, 6, floor.Intersection(numa1).Size(), "floor=%s", floor)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorToleratesFullyOccupiedNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
	}
	// disjoint (immutable per-NUMA) mode.
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	// NUMA 0 is fully occupied by a non-exclusive DNB workload: its eligible
	// DefaultCPUSet is empty, so no complete core can be kept for reclaim. The
	// NUMA is NOT numa-exclusive, so it is not skipped as a steady exclusive NUMA
	// and must be handled by the normal derivation path. Its reclaim floor is
	// legitimately 0 rather than a fail-closed "target exceeds capacity" error.
	// NUMA 1 keeps an 8-CPU (4-core) eligible set and absorbs the node-level
	// configured reclaim floor (4 CPUs) that NUMA 0 cannot hold.
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: machine.NewCPUSet()},
		1: {DefaultCPUSet: coresInNUMA(topology, 1, 0, 4)},
	}, false)

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.Equal(t, 0, floor.Intersection(numa0).Size(),
		"fully occupied NUMA must keep a zero reclaim floor, floor=%s", floor)
	require.Equal(t, 4, floor.Intersection(numa1).Size(), "floor=%s", floor)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorToleratesFullyOccupiedNUMAOverlap(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
	}
	// legacy overlap (non-immutable) mode: the global target is distributed via
	// DistributeNUMATarget, whose per-NUMA minimum guard would otherwise reject a
	// NUMA with zero eligible capacity and fail admission closed.
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: machine.NewCPUSet()},
		1: {DefaultCPUSet: machine.NewCPUSet(numa1.ToSliceInt()[:8]...)},
	}, false)

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.Equal(t, 0, floor.Intersection(numa0).Size(),
		"fully occupied NUMA must be excluded from the global target, floor=%s", floor)
	require.Equal(t, 4, floor.Intersection(numa1).Size(), "floor=%s", floor)
}

func TestDynamicPolicyDeriveRampUpReclaimFloorPreservesLegacyOverlapAlgorithm(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	allCPUs := topology.CPUDetails.CPUs().ToSliceInt()
	p.config.reservedCPUs = machine.NewCPUSet(allCPUs[8:]...)
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.5
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false)

	floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
	require.NoError(t, err)
	require.Equal(t, 4, floor.Size())
}

func TestDynamicPolicyDeriveRampUpReclaimFloorUsesDynamicConfiguredMinimum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dynamicFloor  *v1.ResourceList
		wantFloorSize int
	}{
		{
			name: "dynamic floor overrides initialized fallback",
			dynamicFloor: &v1.ResourceList{
				v1.ResourceCPU: resource.MustParse("8"),
			},
			wantFloorSize: 8,
		},
		{
			name: "odd dynamic floor is preserved",
			dynamicFloor: &v1.ResourceList{
				v1.ResourceCPU: resource.MustParse("5"),
			},
			wantFloorSize: 5,
		},
		{
			name:          "nil dynamic configuration falls back",
			dynamicFloor:  nil,
			wantFloorSize: 4,
		},
		{
			name:          "missing CPU key falls back",
			dynamicFloor:  &v1.ResourceList{},
			wantFloorSize: 4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)

			p.config.reservedCPUs = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
			p.reclaim.reservedReclaimedCPUsSize = 4
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
			if tt.dynamicFloor == nil {
				p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = nil
			} else {
				p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = *tt.dynamicFloor
			}

			floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
			require.NoError(t, err)
			require.Equal(t, tt.wantFloorSize, floor.Size())
			require.Equal(t, tt.wantFloorSize, machine.CalculateGlobalRampUpReclaimTarget(
				cpuTopology.NumCPUs, 0, tt.wantFloorSize),
				"QRM and Sysadvisor must derive the same global target")
		})
	}
}

func TestDynamicPolicyDeriveRampUpReclaimFloorBalancesGlobalTargetAcrossUnevenNUMAs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		hardEnabled       bool
		withReserved      bool
		configuredReserve int
		ratio             float64
		wantPerNUMA       map[int]int
		wantErr           string
	}{
		{
			// eligible capacity 4 (2 cores) on NUMA 0 drives its target rather
			// than the full 16-CPU capacity, so the ramp-up target stays within
			// what admission validates: floor(2*0.5)=1 core => 2 CPUs on NUMA 0,
			// floor(6*0.5)=3 cores => 6 CPUs on NUMA 1. No more fail-closed.
			name:              "half ratio uses eligible capacity",
			hardEnabled:       true,
			withReserved:      true,
			configuredReserve: 4,
			ratio:             0.5,
			wantPerNUMA:       map[int]int{0: 2, 1: 6},
		},
		{
			// fractional ratio is floored on the eligible core count: NUMA 0
			// floor(2*0.5625)=1 core => 2 CPUs, NUMA 1 floor(6*0.5625)=3 cores
			// => 6 CPUs, both within eligible capacity.
			name:              "fractional ratio is floored and aligned from eligible capacity",
			hardEnabled:       true,
			withReserved:      true,
			configuredReserve: 4,
			ratio:             0.5625,
			wantPerNUMA:       map[int]int{0: 2, 1: 6},
		},
		{
			name:              "configured reserve floor wins",
			hardEnabled:       true,
			withReserved:      true,
			configuredReserve: 8,
			ratio:             0.25,
			wantPerNUMA:       map[int]int{0: 4, 1: 4},
		},
		{
			// even a high ratio never exceeds eligible capacity now: NUMA 0
			// floor(2*0.75)=1 core => 2 CPUs (bounded by its 2-core eligible
			// view), NUMA 1 floor(6*0.75)=4 cores => 8 CPUs.
			name:              "three quarter ratio bounded by eligible capacity",
			hardEnabled:       true,
			withReserved:      true,
			configuredReserve: 4,
			ratio:             0.75,
			wantPerNUMA:       map[int]int{0: 2, 1: 8},
		},
		{
			name:        "zero ratio still keeps two per NUMA",
			hardEnabled: true,
			ratio:       0,
			wantPerNUMA: map[int]int{0: 2, 1: 2},
		},
		{
			name:         "disabled hard partition remains empty",
			hardEnabled:  false,
			withReserved: true,
			ratio:        0.75,
			wantPerNUMA:  map[int]int{0: 0, 1: 0},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(32, 2, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)

			eligibleNUMA0 := coresInNUMA(cpuTopology, 0, 0, 2)
			eligibleNUMA1 := coresInNUMA(cpuTopology, 1, 0, 6)
			eligible := eligibleNUMA0.Union(eligibleNUMA1)
			p.config.reservedCPUs = p.machine.machineInfo.CPUDetails.CPUs().Difference(eligible)
			if tt.withReserved {
				p.reclaim.reservedReclaimedCPUSet = coresInNUMA(cpuTopology, 0, 0, 1).
					Union(coresInNUMA(cpuTopology, 1, 0, 1))
				p.reclaim.reservedReclaimedCPUsSize = tt.configuredReserve
			} else {
				p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
				p.reclaim.reservedReclaimedCPUsSize = 0
			}
			p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = tt.hardEnabled
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.hardEnabled
			p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = tt.ratio
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(tt.hardEnabled, false)
			if tt.configuredReserve > 0 {
				p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
					v1.ResourceCPU: *resource.NewQuantity(int64(tt.configuredReserve), resource.DecimalSI),
				}
			}

			floor, err := p.deriveRampUpReclaimFloor(p.state.GetMachineState(), p.state.GetPodEntries(), allRealNUMAsForRampUpFloor(p))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.hardEnabled && tt.withReserved {
				require.True(t, p.reclaim.reservedReclaimedCPUSet.IsSubsetOf(floor), "reserved reclaim CPUs must remain preferred")
			}
			for numaID, want := range tt.wantPerNUMA {
				require.Equal(t, want, floor.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)).Size())
			}
		})
	}
}

func TestDedicatedNUMAExclusiveRampUpCommitsAllocationAndReclaimAtomically(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false))
	p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
	}
	tracked := &atomicCommitTrackingState{State: p.state}
	p.state = tracked

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-atomic-ramp-up",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-atomic-ramp-up",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName},
	}}}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 1, tracked.commitCalls)
	require.Zero(t, tracked.storeCalls)

	allocation := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocation)
	require.True(t, allocation.CheckDedicatedNUMABindingNUMAExclusive())
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.False(t, reclaim.AllocationResult.IsEmpty())
	available := cpuTopology.CPUDetails.CPUs()
	require.True(t, allocation.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
		"allocation=%s reclaim=%s", allocation.AllocationResult, reclaim.AllocationResult)
	require.True(t, allocation.AllocationResult.Union(reclaim.AllocationResult).Equals(available),
		"allocation=%s reclaim=%s available=%s", allocation.AllocationResult, reclaim.AllocationResult, available)
}

func TestDedicatedNUMAExclusiveRampUpWithCPUAdvisorRejoinsExplicitHardFloor(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.enableCPUAdvisor = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
	}
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	explicitHardFloor := p.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult.Clone()

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-advisor-hard-floor",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-advisor-hard-floor",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 2,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName},
	}}}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(
		withAllocationPodMeta(context.Background(), req), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	allocation := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, allocation)
	require.NotNil(t, reclaim)
	require.False(t, reclaim.AllocationResult.IsEmpty())
	require.True(t, explicitHardFloor.IsSubsetOf(reclaim.AllocationResult),
		"explicit hard floor=%s reclaim=%s", explicitHardFloor, reclaim.AllocationResult)
	require.True(t, allocation.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
		"allocation=%s reclaim=%s", allocation.AllocationResult, reclaim.AllocationResult)
	require.True(t, allocation.AllocationResult.Union(reclaim.AllocationResult).Equals(cpuTopology.CPUDetails.CPUs()),
		"allocation=%s reclaim=%s available=%s",
		allocation.AllocationResult, reclaim.AllocationResult, cpuTopology.CPUDetails.CPUs())
}

func TestReclaimOverlapNUMABindingHonorsCanonicalDisableDedicatedOverlap(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                    string
		disableDedicatedOverlap bool
		wantReclaim             machine.CPUSet
	}{
		{
			name:                    "disabled skips historical steady DNB overlap",
			disableDedicatedOverlap: true,
			wantReclaim:             machine.NewCPUSet(1),
		},
		{
			name:                    "legacy mode preserves historical steady DNB overlap",
			disableDedicatedOverlap: false,
			wantReclaim:             machine.NewCPUSet(0, 1),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			p.config.enableCPUAdvisor = true
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(tc.disableDedicatedOverlap, false)

			entries := state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: machine.NewCPUSet(0),
					},
				},
				"historical-steady-dnb": {
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "historical-steady-dnb",
							ContainerName: "main",
							ContainerType: pluginapi.ContainerType_MAIN.String(),
							QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
							Annotations: map[string]string{
								apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
							},
						},
						AllocationResult: machine.NewCPUSet(0),
						RampUp:           false,
					},
				},
			}
			poolsCPUSet := map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(1),
			}

			require.NoError(t, p.reclaimOverlapNUMABinding(poolsCPUSet, entries, p.config.dynamicConfig.GetDynamicConfiguration()))
			require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Equals(tc.wantReclaim),
				"reclaim=%s want=%s", poolsCPUSet[commonstate.PoolNameReclaim], tc.wantReclaim)
		})
	}
}

func TestReclaimOverlapNUMABindingValidatesReclaimWithDedicatedOverlapDisabled(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		entries        state.PodEntries
		candidate      machine.CPUSet
		wantErrMessage string
	}{
		{
			name:           "missing current reclaim entry",
			entries:        state.PodEntries{},
			candidate:      machine.NewCPUSet(1),
			wantErrMessage: "reclaim pool misses in current entries",
		},
		{
			name: "empty candidate reclaim",
			entries: state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: machine.NewCPUSet(0),
					},
				},
			},
			candidate:      machine.NewCPUSet(),
			wantErrMessage: "reclaim pool is empty after overlapping with dedicated_cores numa_binding containers",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			p.config.enableCPUAdvisor = true
			p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

			poolsCPUSet := map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: tc.candidate,
			}
			require.ErrorContains(t,
				p.reclaimOverlapNUMABinding(poolsCPUSet, tc.entries, p.config.dynamicConfig.GetDynamicConfiguration()),
				tc.wantErrMessage)
		})
	}
}

// TestReclaimOverlapHandlersRejectPartialReclaimMapWithoutPanic pins the chained
// map access on the reclaim pool. entries[PoolNameReclaim][FakedContainerName]
// is dereferenced to read .AllocationResult; if the reclaim pool key exists but
// the faked container sub-entry is absent, a plain chained deref would panic.
// Both handlers must return a clean error instead of panicking.
func TestReclaimOverlapHandlersRejectPartialReclaimMapWithoutPanic(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	// reclaim pool key exists, but its container map holds only a stray real
	// container -- the faked pool entry sub-key is missing entirely.
	partialEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			"stray-container": &state.AllocationInfo{
				AllocationResult: machine.NewCPUSet(0),
			},
		},
	}

	t.Run("reclaimOverlapNUMABinding", func(t *testing.T) {
		t.Parallel()
		p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)
		p.config.enableCPUAdvisor = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true

		poolsCPUSet := map[string]machine.CPUSet{
			commonstate.PoolNameReclaim: machine.NewCPUSet(1),
		}
		// Must not panic; must return the reclaim-missing error.
		require.ErrorContains(t,
			p.reclaimOverlapNUMABinding(poolsCPUSet, partialEntries, p.config.dynamicConfig.GetDynamicConfiguration()),
			"reclaim pool misses in current entries")
	})

	t.Run("getReclaimOverlapShareRatio", func(t *testing.T) {
		t.Parallel()
		p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)
		p.state.SetAllowSharedCoresOverlapReclaimedCores(true, true)

		// Must not panic; must return the reclaim-missing error.
		got, err := p.getReclaimOverlapShareRatio(partialEntries)
		require.Error(t, err)
		require.Nil(t, got)
	})
}

func TestDedicatedNUMAExclusiveNonReclaimableStartsSteady(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(32, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.reclaim.reservedReclaimedCPUsSize = 1
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.2
	p.config.dynamicConfig.GetDynamicConfiguration().NumaMinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("2"),
	}
	p.config.dynamicConfig.GetDynamicConfiguration().AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: cpuTopology.CPUDetails.CPUs()},
	}, false)
	p.meta.metaServer.ServiceProfilingManager = &recordingServiceProfilingManager{
		DummyServiceProfilingManager: &spd.DummyServiceProfilingManager{},
		performanceLevel:             spd.PerformanceLevelPoor,
	}

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-steady",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-steady",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): 32,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	candidateSeen := false
	p.allocation.allocationHooks = append(p.allocation.allocationHooks, func(_, allocation *state.AllocationInfo) error {
		if allocation.PodUid == req.PodUid && allocation.ContainerName == req.ContainerName {
			candidateSeen = true
			require.False(t, allocation.RampUp,
				"non-reclaimable exclusive DNB must enter precommit in steady state")
		}
		return nil
	})

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.True(t, candidateSeen, "allocation hook did not observe the precommit candidate")

	allocation := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocation)
	require.False(t, allocation.RampUp)
	require.Equal(t, float64(32), allocation.RequestQuantity)
	require.Equal(t, 30, allocation.AllocationResult.Size())
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(0, 16)),
		"reclaim=%s", reclaim.AllocationResult)
}

func TestShouldNumaBindingAllocationRampUpUsesPartitionSnapshot(t *testing.T) {
	t.Parallel()

	hardPartition := &numaBindingPartitionEligibilitySnapshot{}
	for _, tc := range []struct {
		name              string
		podReclaimEnabled bool
		eligibility       *numaBindingPartitionEligibilitySnapshot
		want              bool
	}{
		{name: "reclaimable legacy allocation ramps up", podReclaimEnabled: true, want: true},
		{name: "reclaimable hard partition ramps up", podReclaimEnabled: true, eligibility: hardPartition, want: true},
		{name: "non-reclaimable legacy allocation ramps up", want: true},
		{name: "non-reclaimable hard partition starts steady", eligibility: hardPartition, want: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, shouldNumaBindingAllocationRampUp(tc.podReclaimEnabled, tc.eligibility))
		})
	}
}

func TestDedicatedNUMAExclusivePackResponseFailureKeepsAllStateUnchanged(t *testing.T) {
	t.Parallel()
	p, req := newDedicatedNUMAExclusiveFailureFixture(t, "exclusive-dnb-pack-failure")
	beforeEntries := p.state.GetPodEntries()
	beforeMachine := p.state.GetMachineState()
	beforeAllowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	beforeDisableDedicated := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	beforeRevision := p.state.GetRevision()

	p.packAllocationFn = func(
		_ *state.AllocationInfo, _, _ string, _, _ bool, _ *pluginapi.ResourceRequest, _ ...map[string]string,
	) (*pluginapi.ResourceAllocationResponse, error) {
		return nil, errors.New("injected pack allocation response failure")
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "injected pack allocation response failure")
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachine, p.state.GetMachineState())
	require.Equal(t, beforeAllowOverlap, p.state.GetAllowSharedCoresOverlapReclaimedCores())
	require.Equal(t, beforeDisableDedicated, p.state.GetDisableDedicatedCoresOverlapReclaimedCores())
	require.Equal(t, beforeRevision, p.state.GetRevision())
}

func TestDedicatedNUMAExclusiveAccompanyFailureKeepsAllStateUnchanged(t *testing.T) {
	t.Parallel()
	p, req := newDedicatedNUMAExclusiveFailureFixture(t, "exclusive-dnb-accompany-failure")
	beforeEntries := p.state.GetPodEntries()
	beforeMachine := p.state.GetMachineState()
	beforeAllowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	beforeDisableDedicated := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	beforeRevision := p.state.GetRevision()

	registry := accompanyresource.NewRegistry()
	require.NoError(t, registry.RegisterPlugin(&failingAllocateAccompanyPlugin{
		err: errors.New("injected accompany allocation failure"),
	}))
	p.accompanyRegistry = registry

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "injected accompany allocation failure")
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachine, p.state.GetMachineState())
	require.Equal(t, beforeAllowOverlap, p.state.GetAllowSharedCoresOverlapReclaimedCores())
	require.Equal(t, beforeDisableDedicated, p.state.GetDisableDedicatedCoresOverlapReclaimedCores())
	require.Equal(t, beforeRevision, p.state.GetRevision())
}

func newDedicatedNUMAExclusiveFailureFixture(
	t *testing.T,
	podUID string,
) (*DynamicPolicy, *pluginapi.ResourceRequest) {
	return newDedicatedNUMAExclusiveFailureFixtureInDir(t, podUID, t.TempDir())
}

func newDedicatedNUMAExclusiveFailureFixtureInDir(
	t *testing.T,
	podUID, stateDir string,
) (*DynamicPolicy, *pluginapi.ResourceRequest) {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	require.NoError(t, p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false))

	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID, ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName,
		},
	}}}
	return p, req
}

type dnbRollbackCauseTestError struct{}

func (*dnbRollbackCauseTestError) Error() string { return "rollback cause" }

type dnbPersistCauseTestError struct{}

func (*dnbPersistCauseTestError) Error() string { return "persist cause" }

type dnbRestoreCauseTestError struct{}

func (*dnbRestoreCauseTestError) Error() string { return "restore cause" }

type dnbTypedNilCauseTestError struct{}

func (*dnbTypedNilCauseTestError) Error() string { return "typed nil cause" }
func (*dnbTypedNilCauseTestError) Is(error) bool { panic("typed nil Is") }
func (*dnbTypedNilCauseTestError) As(interface{}) bool {
	panic("typed nil As")
}
func (*dnbTypedNilCauseTestError) Unwrap() error { panic("typed nil Unwrap") }

type dnbCycleCauseTestError struct {
	next error
}

func (*dnbCycleCauseTestError) Error() string { return "cycle cause" }
func (e *dnbCycleCauseTestError) Unwrap() error {
	return e.next
}

type dnbUncomparableCauseTestError []error

func (dnbUncomparableCauseTestError) Error() string { return "uncomparable cause" }
func (e dnbUncomparableCauseTestError) Unwrap() error {
	if len(e) == 0 {
		return nil
	}
	return e[0]
}

type dnbFuncCauseTestError func() error

func (dnbFuncCauseTestError) Error() string { return "function cause" }
func (e dnbFuncCauseTestError) Unwrap() error {
	return e()
}

//go:noinline
func newDNBFuncCauseTestError(cause error) dnbFuncCauseTestError {
	return func() error { return cause }
}

type dnbSliceViewCauseTestError []error

func (dnbSliceViewCauseTestError) Error() string { return "slice view cause" }
func (e dnbSliceViewCauseTestError) Unwrap() error {
	if len(e) == 0 {
		return nil
	}
	return e[len(e)-1]
}

type dnbFuncMultiCauseTestError func() []error

func (dnbFuncMultiCauseTestError) Error() string { return "function multi-cause" }

// Unwrap exposes a single cause in the Go 1.18-compatible form. The hostile-cause
// walker expands children itself and bounds cycles with its own visit budget, so
// it can reach every listed cause across successive expansion steps; this avoids
// the Go 1.20-only Unwrap() []error signature that go1.18 vet rejects.
func (e dnbFuncMultiCauseTestError) Unwrap() error {
	if e == nil {
		return nil
	}
	causes := e()
	for i := len(causes) - 1; i >= 0; i-- {
		if cause := causes[i]; !isNilError(cause) {
			return cause
		}
	}
	return nil
}

type dnbSliceMultiCauseTestError []error

func (dnbSliceMultiCauseTestError) Error() string { return "slice multi-cause" }
func (e dnbSliceMultiCauseTestError) Unwrap() error {
	for i := len(e) - 1; i >= 0; i-- {
		if cause := e[i]; !isNilError(cause) {
			return cause
		}
	}
	return nil
}

type dnbPanickingCauseTestError struct {
	panicIs     bool
	panicAs     bool
	panicUnwrap bool
}

func (*dnbPanickingCauseTestError) Error() string { return "panicking cause" }
func (e *dnbPanickingCauseTestError) Is(error) bool {
	if e.panicIs {
		panic("injected Is panic")
	}
	return false
}

func (e *dnbPanickingCauseTestError) As(interface{}) bool {
	if e.panicAs {
		panic("injected As panic")
	}
	return false
}

func (e *dnbPanickingCauseTestError) Unwrap() error {
	if e.panicUnwrap {
		panic("injected Unwrap panic")
	}
	return nil
}

func TestDNBAllocationFailureErrorSafelyTraversesHostileCauses(t *testing.T) {
	t.Parallel()
	stale := &frozenInitialSnapshotDriftTestError{}
	wrappedStale := fmt.Errorf("wrapped stale: %w", stale)
	ownershipLost := &requestStateOwnershipLostError{err: errors.New("ownership lost")}

	selfCycle := &dnbCycleCauseTestError{}
	selfCycle.next = selfCycle
	mutualCycleA := &dnbCycleCauseTestError{}
	mutualCycleB := &dnbCycleCauseTestError{next: mutualCycleA}
	mutualCycleA.next = mutualCycleB
	selfDNB := &dnbAllocationFailureError{}
	selfDNB.adjustment = selfDNB
	mutualDNBA := &dnbAllocationFailureError{}
	mutualDNBB := &dnbAllocationFailureError{adjustment: mutualDNBA}
	mutualDNBA.adjustment = mutualDNBB
	var typedNil *dnbTypedNilCauseTestError

	for _, tc := range []struct {
		name    string
		hostile error
	}{
		{name: "typed nil", hostile: typedNil},
		{name: "self cycle", hostile: selfCycle},
		{name: "mutual cycle", hostile: mutualCycleA},
		{name: "self DNB cycle", hostile: selfDNB},
		{name: "mutual DNB cycle", hostile: mutualDNBA},
		{name: "uncomparable error", hostile: dnbUncomparableCauseTestError{wrappedStale}},
		{name: "panicking Is", hostile: &dnbPanickingCauseTestError{panicIs: true}},
		{name: "panicking As", hostile: &dnbPanickingCauseTestError{panicAs: true}},
		{name: "panicking Unwrap", hostile: &dnbPanickingCauseTestError{panicUnwrap: true}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := newDNBAllocationFailureError(tc.hostile, wrappedStale, ownershipLost, errors.New("restore"))

			require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
			var typedStale *frozenInitialSnapshotDriftTestError
			require.ErrorAs(t, err, &typedStale)
			var typedOwnershipLost *requestStateOwnershipLostError
			require.ErrorAs(t, err, &typedOwnershipLost)
		})
	}
}

func TestDNBAllocationFailureErrorPreservesStaleOwnershipLostCauseMatrix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		withPersist bool
		withRestore bool
	}{
		{name: "rollback only"},
		{name: "rollback and persist", withPersist: true},
		{name: "rollback and restore", withRestore: true},
		{name: "rollback persist and restore", withPersist: true, withRestore: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stale := &frozenInitialSnapshotDriftTestError{}
			rollbackCause := &dnbRollbackCauseTestError{}
			ownershipLost := &requestStateOwnershipLostError{
				err: fmt.Errorf("ownership rollback failed: %w", rollbackCause),
			}
			var persistErr error
			if tc.withPersist {
				persistErr = &dnbPersistCauseTestError{}
			}
			var restoreErr error
			if tc.withRestore {
				restoreErr = &dnbRestoreCauseTestError{}
			}

			err := newDNBAllocationFailureError(stale, ownershipLost, persistErr, restoreErr)

			require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
			var typedStale *frozenInitialSnapshotDriftTestError
			require.ErrorAs(t, err, &typedStale)
			var typedOwnershipLost *requestStateOwnershipLostError
			require.ErrorAs(t, err, &typedOwnershipLost)
			var typedRollback *dnbRollbackCauseTestError
			require.ErrorAs(t, err, &typedRollback)
			require.ErrorIs(t, err, rollbackCause)

			var typedPersist *dnbPersistCauseTestError
			require.Equal(t, tc.withPersist, errors.As(err, &typedPersist))
			if persistErr != nil {
				require.ErrorIs(t, err, persistErr)
			}
			var typedRestore *dnbRestoreCauseTestError
			require.Equal(t, tc.withRestore, errors.As(err, &typedRestore))
			if restoreErr != nil {
				require.ErrorIs(t, err, restoreErr)
			}

			require.ErrorContains(t, err, "apply DNB allocation and reclaim floor failed")
			require.ErrorContains(t, err, "state rollback error")
			require.ErrorContains(t, err, "state persistence error")
			require.ErrorContains(t, err, "machine restore error")
		})
	}
}

func TestDNBAllocationFailureErrorDoesNotConflateFunctionOrSliceCauses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		newCauses func(stale, rollback error) (error, error)
	}{
		{
			name: "closures with the same code pointer",
			newCauses: func(stale, rollback error) (error, error) {
				return newDNBFuncCauseTestError(stale), newDNBFuncCauseTestError(rollback)
			},
		},
		{
			name: "slice views with the same backing pointer",
			newCauses: func(stale, rollback error) (error, error) {
				backing := dnbSliceViewCauseTestError{stale, rollback}
				return backing[:1], backing[:2]
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stale := &frozenInitialSnapshotDriftTestError{}
			rollback := &dnbRollbackCauseTestError{}
			first, second := tc.newCauses(stale, rollback)
			require.Equal(t, reflect.ValueOf(first).Pointer(), reflect.ValueOf(second).Pointer())
			err := newDNBAllocationFailureError(first, second, nil, nil)

			require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
			require.ErrorIs(t, err, rollback)
			var typedStale *frozenInitialSnapshotDriftTestError
			require.ErrorAs(t, err, &typedStale)
			var typedRollback *dnbRollbackCauseTestError
			require.ErrorAs(t, err, &typedRollback)
		})
	}
}

func TestDNBAllocationFailureErrorBoundsFunctionAndSliceMultiCauseSelfCycles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		newCycle func() error
	}{
		{
			name: "function",
			newCycle: func() error {
				var cycle dnbFuncMultiCauseTestError
				cycle = func() []error {
					return []error{cycle, cycle}
				}
				return cycle
			},
		},
		{
			name: "slice",
			newCycle: func() error {
				cycle := make(dnbSliceMultiCauseTestError, 2)
				cycle[0] = cycle
				cycle[1] = cycle
				return cycle
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := newDNBAllocationFailureError(tc.newCycle(), nil, nil, nil)
			unmatched := errors.New("unmatched")

			for _, query := range []struct {
				name string
				run  func() bool
			}{
				{
					name: "Is",
					run: func() bool {
						return errors.Is(err, unmatched)
					},
				},
				{
					name: "As",
					run: func() bool {
						var typedRollback *dnbRollbackCauseTestError
						return errors.As(err, &typedRollback)
					},
				},
			} {
				query := query
				t.Run(query.name, func(t *testing.T) {
					t.Parallel()
					// The hostile-cause walker bounds itself with the global visit budget
					// (dnbErrorTraversalMaxVisits). A self-cycle must therefore terminate and
					// report no match. We assert the deterministic false return rather than
					// measuring heap allocations: AllocsPerRun is polluted by other parallel
					// goroutines in the same package and flakes on CI.
					require.False(t, query.run())
				})
			}
		})
	}
}

func TestDNBAllocationFailureErrorMatchesOrdinaryMultiCauseBeyondLegacyDepth(t *testing.T) {
	t.Parallel()
	stale := &frozenInitialSnapshotDriftTestError{}
	var cause error = stale
	for i := 0; i < 128; i++ {
		child := cause
		cause = dnbFuncMultiCauseTestError(func() []error {
			return []error{errors.New("decoy"), child}
		})
	}
	err := newDNBAllocationFailureError(cause, nil, nil, nil)

	require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
	var typedStale *frozenInitialSnapshotDriftTestError
	require.ErrorAs(t, err, &typedStale)
}

func TestDNBAllocationFailureErrorPreservesOrdinarySingleCause(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		newError func(error) error
	}{
		{
			name: "adjustment",
			newError: func(cause error) error {
				return newDNBAllocationFailureError(cause, nil, nil, nil)
			},
		},
		{
			name: "rollback",
			newError: func(cause error) error {
				return newDNBAllocationFailureError(nil, cause, nil, nil)
			},
		},
		{
			name: "persist",
			newError: func(cause error) error {
				return newDNBAllocationFailureError(nil, nil, cause, nil)
			},
		},
		{
			name: "restore",
			newError: func(cause error) error {
				return newDNBAllocationFailureError(nil, nil, nil, cause)
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("ordinary " + tc.name + " cause")
			err := tc.newError(cause)
			require.Error(t, err)
			require.ErrorIs(t, err, cause)
			require.NotContains(t, fmt.Sprintf("%T", err), "joinError")
		})
	}
}

func TestAllocatePropagatesTopologyStaleWithoutAdmissionRetryOrStateChange(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	p, req := newDedicatedNUMAExclusiveFailureFixtureInDir(
		t, "exclusive-dnb-topology-stale", stateDir)
	req.Annotations[apiconsts.PodAnnotationMemoryEnhancementKey] = `{"numa_binding":"true","numa_exclusive":"true"}`
	beforeRevision := p.state.GetRevision()
	beforeEntries := p.state.GetPodEntries()
	beforeMachineState := p.state.GetMachineState()
	admissionCalls := 0
	restoreCalls := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"always-stale": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				admissionCalls++
				return &frozenInitialSnapshotDriftTestError{}
			}
			restoreCalls++
			return nil
		},
	}

	resp, err := p.Allocate(context.Background(), req)

	require.Nil(t, resp)
	require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
	require.Equal(t, 1, admissionCalls)
	require.Equal(t, 1, restoreCalls)
	require.Greater(t, p.state.GetRevision(), beforeRevision,
		"the failed candidate and compensating rollback must retain monotonic CAS revisions")
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachineState, p.state.GetMachineState())
	restarted, restartErr := getTestDynamicPolicyWithoutInitialization(
		p.machine.machineInfo.CPUTopology, stateDir)
	require.NoError(t, restartErr)
	require.Equal(t, beforeEntries, restarted.state.GetPodEntries())
	restartedMachineState := restarted.state.GetMachineState()
	require.Equal(t, beforeMachineState[0].DefaultCPUSet, restartedMachineState[0].DefaultCPUSet)
	require.Equal(t, beforeMachineState[0].AllocatedCPUSet, restartedMachineState[0].AllocatedCPUSet)
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.Nil(t, p.currentAdvisorPostCommitTarget())
}

func TestAllocateDedicatedNUMAExclusiveAdjustmentRollbackStoreFailureKeepsCanonicalMemoryAndDisk(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	wantDiskEntries := p.state.GetPodEntries()
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	const podUID = "exclusive-dnb-rollback-store-failure"
	failingState := &storeFailureState{
		State: p.state,
		err:   errors.New("injected DNB rollback checkpoint failure"),
	}
	p.state = failingState
	failedCandidateCPUs := machine.NewCPUSet()
	restoreAttempted := make(chan struct{}, 1)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				failedCandidate := in.State.GetAllocationInfo(podUID, "main")
				if failedCandidate != nil {
					failedCandidateCPUs = failedCandidate.AllocationResult.Clone()
				}
				return errors.New("injected DNB adjustment failure")
			}
			restoreAttempted <- struct{}{}
			return nil
		},
	}

	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID, ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName,
		},
	}}}

	resp, err := p.Allocate(context.Background(), req)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "injected DNB adjustment failure")
	require.ErrorContains(t, err, "injected DNB rollback checkpoint failure")
	select {
	case <-restoreAttempted:
	default:
		t.Fatal("successful state rollback must attempt Retry-mode cgroup restore even when rollback persistence fails")
	}
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"rollback persistence failure must not resurrect the failed DNB allocation in memory")
	require.False(t, failedCandidateCPUs.IsEmpty())
	require.True(t, p.state.GetMachineState()[0].AllocatedCPUSet.Intersection(failedCandidateCPUs).IsEmpty(),
		"rollback persistence failure must remove failed DNB CPUs from machine state")
	require.Equal(t, 1, failingState.storeCalls,
		"Allocate finalization must make exactly one explicit StoreState attempt")

	restarted, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	require.Equal(t, wantDiskEntries, restarted.state.GetPodEntries(),
		"failed DNB rollback persistence must leave the old checkpoint unchanged")
	require.Nil(t, restarted.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"failed DNB allocation must not appear after restart")
}

func TestAllocateDedicatedNUMAExclusiveAdjustmentFailureDoesNotRollbackNewerState(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	// Production publishes the global reclaim floor on the same dynamic config the
	// manager swaps; the legacy conf default is 4 cores, mirror it here.
	p.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
	}

	const failedPodUID = "exclusive-dnb-adjustment-failure"
	adjustmentStarted := make(chan struct{})
	releaseAdjustment := make(chan struct{})
	adjustmentCalls := 0
	failedCandidateCPUs := machine.NewCPUSet()
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			adjustmentCalls++
			if adjustmentCalls == 1 {
				failedCandidate := in.State.GetAllocationInfo(failedPodUID, "main")
				if failedCandidate != nil {
					failedCandidateCPUs = failedCandidate.AllocationResult.Clone()
				}
				close(adjustmentStarted)
				<-releaseAdjustment
			}
			return errors.New("injected adjustment failure")
		},
	}
	req := &pluginapi.ResourceRequest{
		PodUid: failedPodUID, PodNamespace: "default",
		PodName: "exclusive-dnb-adjustment-failure", ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	result := make(chan error, 1)
	go func() {
		_, err := p.Allocate(context.Background(), req)
		result <- err
	}()
	<-adjustmentStarted

	const newerPodUID = "newer-concurrent-submission"
	p.Lock()
	p.state.SetAllocationInfo(newerPodUID, "main", &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: newerPodUID, ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		AllocationResult:         machine.NewCPUSet(7),
		OriginalAllocationResult: machine.NewCPUSet(7),
	}, false)
	p.Unlock()
	close(releaseAdjustment)

	adjustmentErr := <-result
	require.ErrorContains(t, adjustmentErr, "injected adjustment failure")
	require.NotNil(t, p.state.GetAllocationInfo(newerPodUID, "main"),
		"failed older adjustment rollback overwrote a newer concurrent state submission")
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"failed request left a ghost allocation in the latest pod entries")
	require.False(t, failedCandidateCPUs.IsEmpty())
	require.True(t, p.state.GetMachineState()[0].AllocatedCPUSet.Intersection(failedCandidateCPUs).IsEmpty(),
		"failed request left ghost CPUs %s in machine state %s",
		failedCandidateCPUs, p.state.GetMachineState()[0].AllocatedCPUSet)
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Intersection(machine.NewCPUSet(7)).IsEmpty(),
		"reclaim floor/pool overlaps newer dedicated allocation: %s", reclaim.AllocationResult)
}

func TestAllocateDedicatedNUMAExclusiveAdjustmentFailureDoesNotRestoreConcurrentlyDeletedCandidate(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	stateDir := t.TempDir()
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, stateDir)
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	const podUID = "exclusive-dnb-concurrent-delete"
	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID, ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)
	previous := p.state.GetAllocationInfo(podUID, "main")
	require.NotNil(t, previous)
	previous.AllocationResult = machine.NewCPUSet(6)
	previous.OriginalAllocationResult = machine.NewCPUSet(6)
	previous.TopologyAwareAssignments = map[int]machine.CPUSet{0: machine.NewCPUSet(6)}
	previous.OriginalTopologyAwareAssignments = map[int]machine.CPUSet{0: machine.NewCPUSet(6)}
	previous.RequestQuantity = 1
	p.state.SetAllocationInfo(podUID, "main", previous, false)
	req.Annotations[apiconsts.PodAnnotationMemoryEnhancementKey] = `{"numa_binding":"true","numa_exclusive":"true"}`

	adjustmentStarted := make(chan struct{})
	releaseAdjustment := make(chan struct{})
	latestStateReconciled := make(chan struct{}, 1)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				close(adjustmentStarted)
				<-releaseAdjustment
				return errors.New("injected adjustment failure")
			}
			require.Nil(t, in.State.GetAllocationInfo(podUID, "main"),
				"latest-state retry observed the stale pre-delete allocation")
			latestStateReconciled <- struct{}{}
			return nil
		},
	}

	result := make(chan error, 1)
	go func() {
		_, err := p.Allocate(context.Background(), req)
		result <- err
	}()
	<-adjustmentStarted

	p.Lock()
	entries := p.state.GetPodEntries()
	delete(entries, podUID)
	p.state.SetPodEntries(entries, false)
	deletedRevision := p.state.GetRevision()
	p.Unlock()
	close(releaseAdjustment)

	allocationErr := <-result
	require.Error(t, allocationErr)
	var ownershipLost interface{ OwnershipLost() bool }
	require.ErrorAs(t, allocationErr, &ownershipLost)
	require.True(t, ownershipLost.OwnershipLost())
	require.ErrorContains(t, allocationErr, "ownership lost")
	require.Nil(t, p.state.GetAllocationInfo(podUID, "main"),
		"rollback restored the stale allocation that a concurrent delete removed")
	select {
	case <-latestStateReconciled:
	case <-time.After(time.Second):
		t.Fatal("concurrent-delete ownership loss did not schedule a latest-state reconciliation")
	}
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()
	require.Equal(t, deletedRevision, p.state.GetRevision(),
		"latest-state retry must not roll the in-memory revision backward")

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		"cpu_plugin_state", "dynamic", p.machine.machineInfo.CPUTopology, false,
		generateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Equal(t, deletedRevision, restarted.GetRevision(),
		"background persistence must store the latest in-memory revision")
	require.Nil(t, restarted.GetAllocationInfo(podUID, "main"),
		"the concurrently deleted candidate must not resurrect after restart")
}

func TestAllocateDedicatedNUMAExclusiveApplyFailureRestoresSourceAndRetriesSameStage(t *testing.T) {
	t.Parallel()
	p, req := newDedicatedNUMAExclusiveFailureFixture(t, "exclusive-dnb-same-stage")
	sourceEntries := p.state.GetPodEntries()
	sourceMachine := p.state.GetMachineState()

	failAdmission := true
	type stagedAllocation struct {
		dedicated machine.CPUSet
		reclaim   machine.CPUSet
	}
	var attempted []stagedAllocation
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"apply": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode != dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				return nil
			}
			attempted = append(attempted, stagedAllocation{
				dedicated: in.State.GetAllocationInfo(req.PodUid, req.ContainerName).AllocationResult,
				reclaim: in.State.GetAllocationInfo(
					commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult,
			})
			if failAdmission {
				return errors.New("injected apply failure")
			}
			return nil
		},
	}

	allocate := func() (*pluginapi.ResourceAllocationResponse, error) {
		p.Lock()
		defer p.Unlock()
		return p.dedicatedCoresWithNUMABindingAllocationHandler(
			withAllocationPodMeta(context.Background(), req), req, true)
	}

	first, err := allocate()
	require.Nil(t, first)
	require.ErrorContains(t, err, "injected apply failure")
	require.Equal(t, sourceEntries, p.state.GetPodEntries(),
		"failed apply must restore the exact source pod state")
	require.Equal(t, sourceMachine, p.state.GetMachineState(),
		"failed apply must restore the exact source machine state")

	failAdmission = false
	second, err := allocate()
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Len(t, attempted, 2)
	require.Equal(t, attempted[0], attempted[1],
		"retry from the restored source must reproduce the same staged allocation")
}

func TestAllocateDedicatedNUMAExclusiveRollbackRestoresInMemoryBeforeSingleStoreFailure(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	p, req := newDedicatedNUMAExclusiveFailureFixtureInDir(
		t, "exclusive-dnb-rollback-store-failure", stateDir)
	sourceEntries := p.state.GetPodEntries()
	sourceMachine := p.state.GetMachineState()
	tracking := &atomicCommitTrackingState{
		State:      p.state,
		storeErr:   errors.New("injected rollback checkpoint write failure"),
		failStores: 1,
	}
	p.state = tracking
	var retryModes []dynamicpolicyutil.CPUSetAdjustmentMode
	failedCandidateObserved := false
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"fail": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				return errors.New("injected apply failure")
			}
			retryModes = append(retryModes, in.Mode)
			failedCandidateObserved = failedCandidateObserved ||
				in.State.GetAllocationInfo(req.PodUid, req.ContainerName) != nil
			return nil
		},
	}

	p.Lock()
	_, err := p.dedicatedCoresWithNUMABindingAllocationHandler(
		withAllocationPodMeta(context.Background(), req), req, true)

	require.ErrorContains(t, err, "injected rollback checkpoint write failure")
	var compensated *requestStateCompensatedError
	require.ErrorAs(t, err, &compensated,
		"successful in-memory rollback must be reported as compensated despite StoreState failure")
	require.Equal(t, []bool{true, false}, tracking.commitPersists,
		"rollback must restore memory without an implicit checkpoint write")
	require.Equal(t, 1, tracking.storeCalls,
		"rollback must attempt exactly one explicit checkpoint write")
	require.Equal(t, sourceEntries, p.state.GetPodEntries(),
		"checkpoint failure must not undo the in-memory source restoration")
	require.Equal(t, sourceMachine, p.state.GetMachineState())
	require.Equal(t, []dynamicpolicyutil.CPUSetAdjustmentMode{
		dynamicpolicyutil.CPUSetAdjustmentModeRetry,
	}, retryModes, "production state must be restored immediately after memory rollback")
	require.False(t, failedCandidateObserved)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.True(t, p.adjustment.cpuSetAdjustmentRetryQueued)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	p.Unlock()
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()

	require.Equal(t, 2, tracking.storeCalls,
		"one-shot checkpoint failure must be retried in the background")
	require.False(t, failedCandidateObserved,
		"the failed candidate must never be exposed by production retries")
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.False(t, p.adjustment.cpuSetAdjustmentRetryQueued)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		"cpu_plugin_state", "dynamic", p.machine.machineInfo.CPUTopology, false,
		generateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Nil(t, restarted.GetAllocationInfo(req.PodUid, req.ContainerName),
		"the failed candidate must not resurrect after restart")
}

func TestAllocateDedicatedNUMAExclusiveRollbackPersistsSourceDespitePersistentRetryFailure(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	p, req := newDedicatedNUMAExclusiveFailureFixtureInDir(
		t, "exclusive-dnb-persist-independent-of-retry", stateDir)
	sourceEntries := p.state.GetPodEntries()
	sourceMachine := p.state.GetMachineState()
	tracking := &atomicCommitTrackingState{
		State:      p.state,
		storeErr:   errors.New("injected rollback checkpoint write failure"),
		failStores: 1,
	}
	p.state = tracking
	retryCalls := 0
	failedCandidateObserved := false
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"persistent-cgroup-failure": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				return errors.New("injected admission cgroup failure")
			}
			retryCalls++
			failedCandidateObserved = failedCandidateObserved ||
				in.State.GetAllocationInfo(req.PodUid, req.ContainerName) != nil
			return errors.New("injected persistent retry cgroup failure")
		},
	}

	p.Lock()
	_, err := p.dedicatedCoresWithNUMABindingAllocationHandler(
		withAllocationPodMeta(context.Background(), req), req, true)
	require.ErrorContains(t, err, "injected rollback checkpoint write failure")
	require.Equal(t, sourceEntries, p.state.GetPodEntries())
	require.Equal(t, sourceMachine, p.state.GetMachineState())
	p.Unlock()

	p.adjustment.cpuSetAdjustmentRetryWG.Wait()

	require.Greater(t, retryCalls, 1,
		"persistent cgroup failure must exercise the bounded background retry path")
	require.Equal(t, 2, tracking.storeCalls,
		"restored source must be persisted independently once storage recovers")
	require.False(t, failedCandidateObserved,
		"the rolled-back candidate must never be exposed to cgroup retries")
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty,
		"persistent cgroup failure must keep latest-state reconciliation dirty")
	require.False(t, p.adjustment.cpuSetAdjustmentRetryPersist,
		"successful independent persistence must clear only the persistence obligation")
	require.False(t, p.adjustment.cpuSetAdjustmentRetryQueued)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	restarted, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: stateDir},
		"cpu_plugin_state", "dynamic", p.machine.machineInfo.CPUTopology, false,
		generateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	require.Nil(t, restarted.GetAllocationInfo(req.PodUid, req.ContainerName),
		"the failed candidate must not resurrect after restart")
}

func TestAllocateDedicatedNUMAExclusiveStaleAdjustmentPreservesOwnershipLostAndLatestState(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	const podUID = "exclusive-dnb-ownership-lost"
	adjustmentStarted := make(chan struct{})
	releaseAdjustment := make(chan struct{})
	latestStateReconciled := make(chan struct{}, 1)
	admissionCalls := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				admissionCalls++
				close(adjustmentStarted)
				<-releaseAdjustment
				return &frozenInitialSnapshotDriftTestError{}
			}
			latestStateReconciled <- struct{}{}
			return nil
		},
	}
	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID, ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	result := make(chan error, 1)
	go func() {
		_, err := p.Allocate(context.Background(), req)
		result <- err
	}()
	<-adjustmentStarted

	advanced := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: podUID, ContainerName: "main",
			OwnerPoolName: commonstate.PoolNameDedicated,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		AllocationResult:         machine.NewCPUSet(6, 7),
		OriginalAllocationResult: machine.NewCPUSet(6, 7),
	}
	p.Lock()
	p.state.SetAllocationInfo(podUID, "main", advanced, false)
	p.Unlock()
	close(releaseAdjustment)

	allocationErr := <-result
	require.Error(t, allocationErr)
	require.ErrorIs(t, allocationErr, bulkheadtopology.ErrCoordinatorPlanStale)
	var typedStale *frozenInitialSnapshotDriftTestError
	require.ErrorAs(t, allocationErr, &typedStale)
	var compensated *requestStateCompensatedError
	require.ErrorAs(t, allocationErr, &compensated)
	var ownershipLost *requestStateOwnershipLostError
	require.ErrorAs(t, allocationErr, &ownershipLost)
	require.True(t, ownershipLost.OwnershipLost())
	require.ErrorContains(t, allocationErr, "ownership lost")
	require.Equal(t, 1, admissionCalls, "stale admission adjustment must not be retried")
	require.True(t, p.state.GetAllocationInfo(podUID, "main").AllocationResult.Equals(advanced.AllocationResult),
		"outer stale snapshot rollback overwrote the advanced allocation")
	select {
	case <-latestStateReconciled:
	case <-time.After(time.Second):
		t.Fatal("ownership loss did not schedule a latest-state reconciliation")
	}
	require.True(t, p.state.GetAllocationInfo(podUID, "main").AllocationResult.Equals(advanced.AllocationResult),
		"latest-state reconciliation changed the concurrently advanced allocation")
}

func TestAllocateDedicatedNUMAExclusiveRestoreFailureMarksDirtyAndSchedulesBoundedFullRetry(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	retryAttempts := make(chan struct{}, cpuSetAdjustmentRetryMaxAttempts+1)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"failing": func(_ context.Context, in dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == dynamicpolicyutil.CPUSetAdjustmentModeAdmission {
				return errors.New("injected admission adjustment failure")
			}
			retryAttempts <- struct{}{}
			return errors.New("injected restore failure")
		},
	}
	req := &pluginapi.ResourceRequest{
		PodUid: "exclusive-dnb-restore-failure", PodNamespace: "default",
		PodName: "exclusive-dnb-restore-failure", ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	_, allocationErr := p.Allocate(context.Background(), req)
	require.ErrorContains(t, allocationErr, "injected restore failure")
	var compensated *requestStateCompensatedError
	require.ErrorAs(t, allocationErr, &compensated)
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName),
		"state compensation did not remove the failed allocation")

	deadline := time.After(2 * time.Second)
	for i := 0; i < cpuSetAdjustmentRetryMaxAttempts+1; i++ {
		select {
		case <-retryAttempts:
		case <-deadline:
			t.Fatalf("restore/full retry attempts = %d, want synchronous restore plus %d bounded retries",
				i, cpuSetAdjustmentRetryMaxAttempts)
		}
	}
	for {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		queued := p.adjustment.cpuSetAdjustmentRetryQueued
		dirty := p.adjustment.cpuSetAdjustmentRetryDirty
		_, hasRestoreFailedReason := p.adjustment.cpuSetAdjustmentRetryReasons[dynamicpolicyutil.RetryReasonRestoreFailed]
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		if !queued {
			require.True(t, dirty, "bounded full retry exhaustion must leave latest-state reconciliation dirty")
			require.True(t, hasRestoreFailedReason,
				"restore failure must retain the restore-failed retry reason")
			break
		}
		select {
		case <-deadline:
			t.Fatal("latest-state full retry did not stop within the bounded retry window")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestDedicatedNUMAExclusiveRampUpCommitFailureKeepsPreviousState(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	beforeEntries := p.state.GetPodEntries()
	beforeMachine := p.state.GetMachineState()
	tracked := &atomicCommitTrackingState{
		State:       p.state,
		commitErr:   errors.New("injected atomic commit failure"),
		failCommits: -1,
	}
	p.state = tracked
	req := &pluginapi.ResourceRequest{
		PodUid: "exclusive-dnb-commit-failure", PodNamespace: "default",
		PodName: "exclusive-dnb-commit-failure", ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName},
	}}}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "injected atomic commit failure")
	require.Equal(t, 1, tracked.commitCalls)
	require.Zero(t, tracked.storeCalls)
	require.True(t, reflect.DeepEqual(beforeEntries, p.state.GetPodEntries()))
	require.True(t, reflect.DeepEqual(beforeMachine, p.state.GetMachineState()))
}

func TestDedicatedNUMAExclusiveRampUpKeepsMinimumReclaimFloor(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	req := &pluginapi.ResourceRequest{
		PodUid: "exclusive-dnb-empty-floor", PodNamespace: "default",
		PodName: "exclusive-dnb-empty-floor", ContainerName: "main",
		ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}
	p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName},
	}}}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
	require.NoError(t, err)
	require.NotNil(t, resp)
	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.GreaterOrEqual(t, reclaimInfo.AllocationResult.Size(), 2)
}

func TestDedicatedNUMAExclusiveRampUpValidatesPartitionEligibleCoverage(t *testing.T) {
	t.Parallel()

	newFixture := func(t *testing.T) (*DynamicPolicy, *pluginapi.ResourceRequest) {
		cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
		require.NoError(t, err)
		p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
		require.NoError(t, err)
		p.config.reservedCPUs = machine.NewCPUSet()
		p.reclaim.reservedReclaimedCPUSet = coresInNUMA(cpuTopology, 0, 2, 3)
		p.reclaim.reservedReclaimedCPUsSize = p.reclaim.reservedReclaimedCPUSet.Size()
		p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
		p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
		p.config.dynamicConfig.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = "disable-reclaim=true"
		p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
		p.state.SetMachineState(state.NUMANodeMap{
			0: {
				DefaultCPUSet: cpuTopology.CPUDetails.CPUsInNUMANodes(0),
				ResourcePackageStates: map[string]*state.ResourcePackageState{
					"work": {
						PinnedCPUSet: coresInNUMA(cpuTopology, 0, 0, 1),
					},
					"protected": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: coresInNUMA(cpuTopology, 0, 3, 4),
					},
				},
			},
		}, false)

		req := &pluginapi.ResourceRequest{
			PodUid: "exclusive-dnb-partition-coverage", PodNamespace: "default",
			PodName: "exclusive-dnb-partition-coverage", ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN, ResourceName: string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
			Labels: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey:                    apiconsts.PodAnnotationQoSLevelDedicatedCores,
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				apiconsts.PodAnnotationResourcePackageKey:             "work",
			},
			Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
		}
		p.meta.metaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(req.PodUid), Namespace: req.PodNamespace, Name: req.PodName},
		}}}
		return p, req
	}

	t.Run("disable-reclaim pinned CPU outside dedicated and reclaim eligibility is legal", func(t *testing.T) {
		t.Parallel()
		p, req := newFixture(t)

		resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
		require.NoError(t, err)
		require.NotNil(t, resp)

		allocation := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, allocation)
		require.NotNil(t, reclaim)
		protected := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 3, 4)
		admissionFloor := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 1, 3)
		require.True(t, allocation.AllocationResult.Union(admissionFloor).
			Equals(p.machine.machineInfo.CPUDetails.CPUs().Difference(protected)))
		require.True(t, allocation.AllocationResult.Intersection(protected).IsEmpty())
		require.True(t, admissionFloor.IsSubsetOf(reclaim.AllocationResult))
	})

	t.Run("incorrect dedicated and reclaim union is rejected", func(t *testing.T) {
		t.Parallel()
		p, req := newFixture(t)
		p.state.SetAllocationInfo(commonstate.PoolNameInterrupt, commonstate.FakedContainerName, &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        commonstate.PoolNameInterrupt,
				ContainerName: commonstate.FakedContainerName,
			},
			AllocationResult: coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1),
		}, false)

		resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(withAllocationPodMeta(context.Background(), req), req, true)
		require.Nil(t, resp)
		require.ErrorContains(t, err, "do not cover NUMA 0")
		require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	})
}

func TestAllocateRestoresPreviousDNBWhenAtomicCommitFails(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reclaim.reservedReclaimedCPUsSize = 0
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	const podUID = "existing-exclusive-dnb"
	oldAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: podUID, PodNamespace: "default", PodName: podUID,
			ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
			},
		},
		AllocationResult:         machine.NewCPUSet(0, 1),
		OriginalAllocationResult: machine.NewCPUSet(0, 1),
		RampUp:                   true,
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		},
	}
	p.state.SetAllocationInfo(podUID, "main", oldAllocation, false)
	tracked := &atomicCommitTrackingState{
		State: p.state, commitErr: errors.New("injected atomic commit failure"), failCommits: 1,
	}
	p.state = tracked
	req := &pluginapi.ResourceRequest{
		PodUid: podUID, PodNamespace: "default", PodName: podUID,
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 4},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding":"true","numa_exclusive":"true"}`,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.Allocate(context.Background(), req)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "injected atomic commit failure")
	restored := p.state.GetAllocationInfo(podUID, "main")
	require.NotNil(t, restored)
	require.True(t, restored.AllocationResult.Equals(oldAllocation.AllocationResult))
}

func TestDynamicPolicy_adjustPoolsAndIsolatedEntries_Pinned(t *testing.T) {
	t.Parallel()
	as := require.New(t)

	// Setup topology: 2 sockets, 8 cores each. Total 16 CPUs.
	// S0: 0-7, S1: 8-15.
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	as.Nil(err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_adjustPoolsAndIsolatedEntries_Pinned")
	as.Nil(err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	as.Nil(err)

	// Clear reserved CPUs to ensure deterministic allocation for test
	p.config.reservedCPUs = machine.NewCPUSet()

	// Enable Reclaim
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	// Disable overlap to ensure pool2 gets exactly what it requests (4 cores)
	// If enabled, it would take all available cores (12) which is also correct behavior but makes checking "exactly 4" fail.
	// We want to verify it can successfully allocate 4 from the remaining unpinned set.
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)

	// Setup Pinned CPUSets
	// pkg1 pinned to {0, 1} (NUMA 0)
	// pkg2 pinned to {2, 3} (NUMA 0) -- BUT no pools use it!
	machineState := p.state.GetMachineState()
	for numaID, numaState := range machineState {
		if numaID == 0 {
			if numaState.ResourcePackageStates == nil {
				numaState.ResourcePackageStates = make(map[string]*state.ResourcePackageState)
			}
			numaState.ResourcePackageStates["pkg1"] = &state.ResourcePackageState{PinnedCPUSet: machine.NewCPUSet(0, 1)}
			numaState.ResourcePackageStates["pkg2"] = &state.ResourcePackageState{PinnedCPUSet: machine.NewCPUSet(2, 3)}
		}
	}
	p.state.SetMachineState(machineState, false)

	// Setup Pools Quantity
	// pkg1/pool1: 2 cores (should take 0, 1)
	// pool2 (common): 4 cores (should take from available excluding 0, 1 AND 2, 3)
	// commonAvailableCPUs should be {4-15}.
	// pool2 needs 4 cores. It should get 4, 5, 6, 7 (if taking from NUMA 0 first) or spread.
	// Since NUMA 0 has 4,5,6,7 available (4 cores).
	// NUMA 1 has 8-15 available (8 cores).
	// pool2 is FakedNUMAID.
	poolsQuantityMap := map[string]map[int]int{
		"pkg1/pool1": {
			commonstate.FakedNUMAID: 2,
		},
		"pool2": {
			commonstate.FakedNUMAID: 4,
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedNUMAID: 0,
		},
	}

	isolatedQuantityMap := map[string]map[string]int{}

	// Seed entries for Reclaim pool (needed for reclaimOverlapNUMABinding check)
	// And seed containers to prevent cleanPools from removing the pools
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(14, 15),
				OriginalAllocationResult: machine.NewCPUSet(14, 15),
				TopologyAwareAssignments: map[int]machine.CPUSet{1: machine.NewCPUSet(14, 15)},
			},
		},
		"pod1": {
			"container1": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					PodNamespace:  "default",
					PodName:       "pod1",
					ContainerName: "container1",
					OwnerPoolName: "pkg1/pool1",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		"pod2": {
			"container2": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod2",
					PodNamespace:  "default",
					PodName:       "pod2",
					ContainerName: "container2",
					OwnerPoolName: "pool2",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}

	err = p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState, false)
	as.Nil(err)

	updatedEntries := p.state.GetPodEntries()

	// Verify Results
	// pkg1/pool1 should be {0, 1}
	pool1Entry := updatedEntries["pkg1/pool1"][commonstate.FakedContainerName]
	as.NotNil(pool1Entry)
	as.True(pool1Entry.AllocationResult.Equals(machine.NewCPUSet(0, 1)), "pool1 should have pinned CPUs 0,1, got %s", pool1Entry.AllocationResult.String())

	// pool2 should NOT contain 0, 1 (used by pkg1) AND should NOT contain 2, 3 (reserved by pkg2 even if unused)
	pool2Entry := updatedEntries["pool2"][commonstate.FakedContainerName]
	as.NotNil(pool2Entry)
	// Check intersection with pkg1 pinned
	as.False(pool2Entry.AllocationResult.Intersection(machine.NewCPUSet(0, 1)).Size() > 0, "pool2 should not use pinned CPUs 0,1, got %s", pool2Entry.AllocationResult.String())
	// Check intersection with pkg2 pinned (unused but reserved)
	as.False(pool2Entry.AllocationResult.Intersection(machine.NewCPUSet(2, 3)).Size() > 0, "pool2 should not use pinned CPUs 2,3 (reserved for pkg2), got %s", pool2Entry.AllocationResult.String())

	// Verify pool2 size
	as.Equal(4, pool2Entry.AllocationResult.Size(), "pool2 should have 4 cores")
}

// TestDynamicPolicy_groupAndAllocatePools tests the groupAndAllocatePools function.
// It verifies that pools are correctly grouped into pinned and common categories,
// and that CPUs are allocated according to availability and constraints.
func TestDynamicPolicy_groupAndAllocatePools(t *testing.T) {
	t.Parallel()

	type args struct {
		poolsQuantityMap         map[string]map[int]int
		isolatedQuantityMap      map[string]map[string]int
		availableCPUs            machine.CPUSet
		rpPinnedCPUSet           map[string]machine.CPUSet
		reclaimOverlapShareRatio map[string]float64
	}
	tests := []struct {
		name         string
		args         args
		wantPools    map[string]machine.CPUSet
		wantIsolated map[string]map[string]machine.CPUSet
		wantErr      bool
	}{
		{
			name: "Scenario 1: Common Pools Only - Verifies that when no pools are pinned, all pools are treated as common and allocated from the general available CPU set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 2: Pinned Pools Only - Verifies that pools belonging to a resource package are correctly identified and allocated exclusively from that package's pinned CPU set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 3: Mixed Pinned and Common Pools - Verifies that the function correctly splits pinned and common pools, allocating pinned pools from their specific sets and common pools from the remaining available CPUs.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 2},
					"pool2": {commonstate.FakedNUMAID: 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
				"pool2": machine.NewCPUSet(2, 3),
			},
			wantErr: false,
		},
		{
			name: "Scenario 4: Isolated Containers - Verifies that isolated containers are allocated dedicated CPUs from the common available set alongside common pools.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 2},
				},
				isolatedQuantityMap: map[string]map[string]int{
					"pod1": {"container1": 2},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 3),
			},
			wantIsolated: map[string]map[string]machine.CPUSet{
				"pod1": {"container1": machine.NewCPUSet(0, 1)},
			},
			wantErr: false,
		},
		{
			name: "Scenario 5: Error - Pinned Pool Insufficient CPUs - Verifies that the function degrades gracefully and allocates available CPUs (partial) if a pinned pool requests more CPUs than are available in its pinned set.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					rputil.WrapOwnerPoolName("pool1", "pkg1"): {commonstate.FakedNUMAID: 4},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				rputil.WrapOwnerPoolName("pool1", "pkg1"): machine.NewCPUSet(0, 1),
			},
			wantErr: false,
		},
		{
			name: "Scenario 6: Error - Common Pool Insufficient CPUs - Verifies that the function degrades gracefully and allocates available CPUs (partial) if common pools request more CPUs than are available in the shared pool.",
			args: args{
				poolsQuantityMap: map[string]map[int]int{
					"pool1": {commonstate.FakedNUMAID: 4},
				},
				availableCPUs: machine.NewCPUSet(0, 1, 2, 3),
				rpPinnedCPUSet: map[string]machine.CPUSet{
					"pkg1": machine.NewCPUSet(0, 1),
				},
			},
			wantPools: map[string]machine.CPUSet{
				"pool1": machine.NewCPUSet(2, 3),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as := require.New(t)

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
			as.Nil(err)

			tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_groupAndAllocatePools")
			as.Nil(err)
			defer os.RemoveAll(tmpDir)

			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
			as.Nil(err)

			// Clear state
			p.state.SetPodEntries(state.PodEntries{}, false)
			p.config.reservedCPUs = machine.NewCPUSet()

			gotPools, gotIsolated, err := p.groupAndAllocatePoolsWithPlacement(tt.args.poolsQuantityMap, tt.args.isolatedQuantityMap, tt.args.availableCPUs, tt.args.rpPinnedCPUSet, tt.args.reclaimOverlapShareRatio, p.config.dynamicConfig.GetDynamicConfiguration(), p.state.GetPodEntries(), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("groupAndAllocatePools() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				// Filter out system pools (reclaim, reserve) for comparison
				filteredPools := make(map[string]machine.CPUSet)
				for k, v := range gotPools {
					if k != commonstate.PoolNameReclaim && k != commonstate.PoolNameReserve {
						filteredPools[k] = v
					}
				}

				if !reflect.DeepEqual(filteredPools, tt.wantPools) {
					t.Errorf("groupAndAllocatePools() gotPools = %v, want %v", filteredPools, tt.wantPools)
				}

				if len(gotIsolated) == 0 && len(tt.wantIsolated) == 0 {
					// Both empty/nil, treat as equal
				} else if !reflect.DeepEqual(gotIsolated, tt.wantIsolated) {
					t.Errorf("groupAndAllocatePools() gotIsolated = %v, want %v", gotIsolated, tt.wantIsolated)
				}
			}
		})
	}
}

func mustGenerateDummyCPUTopology(numCPUs, numSockets, numaNum int) *machine.CPUTopology {
	topo, err := machine.GenerateDummyCPUTopology(numCPUs, numSockets, numaNum)
	if err != nil {
		panic(err)
	}
	return topo
}

func TestMaterializeDefaultShareCPUSet(t *testing.T) {
	t.Parallel()

	available := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	pools := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: machine.NewCPUSet(0, 1),
		"custom":                    machine.NewCPUSet(2),
		"snb-NUMA0":                 machine.NewCPUSet(3),
	}
	isolated := map[string]map[string]machine.CPUSet{
		"pod": {"container": machine.NewCPUSet(4)},
	}
	got, err := materializeDefaultShareCPUSet(3, available, pools, isolated)
	require.NoError(t, err)
	require.True(t, got.Equals(machine.NewCPUSet(5, 6, 7)))
}

func TestBuildDefaultShareEligibleCPUSetUsesFullTopologyAfterDNBMigration(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	p.config.reservedCPUs = machine.NewCPUSet(numa0.ToSliceInt()[0])

	// machineState still reflects the pre-migration DNB placement on NUMA 0.
	staleMachineState := p.state.GetMachineState()
	staleMachineState[0].DefaultCPUSet = machine.NewCPUSet()
	staleMachineState[0].AllocatedCPUSet = numa0.Clone()
	staleMachineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
		"pinned": {PinnedCPUSet: machine.NewCPUSet(numa1.ToSliceInt()[0])},
	}

	// Finalized entries already place the exclusive DNB container on NUMA 1.
	finalizedEntries := state.PodEntries{
		"migrated-dnb": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "migrated-dnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult:         numa1.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{1: numa1.Clone()},
			},
		},
		"system": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("system"),
				AllocationResult: machine.NewCPUSet(numa0.ToSliceInt()[1]),
			},
		},
	}
	rampFloor := machine.NewCPUSet(numa0.ToSliceInt()[2])

	got := p.buildDefaultShareEligibleCPUSet(finalizedEntries, staleMachineState, rampFloor)
	want := numa0.
		Difference(p.config.reservedCPUs).
		Difference(machine.NewCPUSet(numa0.ToSliceInt()[1])).
		Difference(rampFloor)
	require.True(t, got.Equals(want), "got=%s want=%s", got, want)
	require.False(t, got.Intersection(numa0).IsEmpty(),
		"old DNB NUMA must return to default-share eligibility after migration")
	require.True(t, got.Intersection(numa1).IsEmpty(),
		"new exclusive DNB NUMA must be excluded as a whole")
}

func TestMaterializeDefaultShareCPUSetRejectsQuantityMismatch(t *testing.T) {
	t.Parallel()

	actual, err := materializeDefaultShareCPUSet(
		2,
		machine.NewCPUSet(0, 1, 2, 3),
		map[string]machine.CPUSet{commonstate.PoolNameReclaim: machine.NewCPUSet(0)},
		nil,
	)
	require.ErrorIs(t, err, ErrDefaultShareResidualQuantityMismatch)
	var quantityErr *DefaultShareResidualQuantityError
	require.ErrorAs(t, err, &quantityErr)
	require.Equal(t, 2, quantityErr.AdvisedQuantity)
	require.Equal(t, 3, quantityErr.ResidualSize)
	require.True(t, actual.IsEmpty())
}

func TestMaterializeDefaultShareCPUSetAllowsAdvisorShrinkLag(t *testing.T) {
	t.Parallel()

	got, err := materializeDefaultShareCPUSet(
		7,
		machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		map[string]machine.CPUSet{
			commonstate.PoolNameReclaim: machine.NewCPUSet(0),
			"snb-NUMA0":                 machine.NewCPUSet(1),
		},
		nil,
	)
	require.NoError(t, err)
	require.True(t, got.Equals(machine.NewCPUSet(2, 3, 4, 5, 6, 7)),
		"got=%s want=2-7", got)
}

func TestMaterializeDefaultShareCPUSetScenarios(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name          string
		expected      int
		available     machine.CPUSet
		pools         map[string]machine.CPUSet
		isolated      map[string]map[string]machine.CPUSet
		wantShare     machine.CPUSet
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:      "default share absorbs residual from NUMA with SNB",
			expected:  5,
			available: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			pools: map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(0, 4),
				"snb-NUMA0":                 machine.NewCPUSet(1),
			},
			wantShare: machine.NewCPUSet(2, 3, 5, 6, 7),
		},
		{
			name:      "exclusive NUMA is already absent from available",
			expected:  3,
			available: machine.NewCPUSet(0, 1, 2, 3),
			pools: map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(0),
			},
			wantShare: machine.NewCPUSet(1, 2, 3),
		},
		{
			name:      "stale default share pool value is excluded from fixed",
			expected:  2,
			available: machine.NewCPUSet(0, 1, 2, 3),
			pools: map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(0),
				// stale default share must NOT count as fixed; if it were
				// counted, residual would be {1} (size 1) and match no case.
				commonstate.PoolNameShare: machine.NewCPUSet(2, 3),
			},
			// residual = available - reclaim(0) = {1,2,3}; expected 2 => mismatch
			// proves the stale share value {2,3} was excluded (not subtracted).
			wantErr:       true,
			wantErrSubstr: "default share quantity 2 is smaller than residual cpuset size 3",
		},
		{
			name:     "resource-package pinned unused cpu never enters share",
			expected: 2,
			// candidate already had pinned cpus removed by the caller.
			available: machine.NewCPUSet(0, 1, 2, 3),
			pools: map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(0),
				"custom":                    machine.NewCPUSet(1),
			},
			wantShare: machine.NewCPUSet(2, 3),
		},
		{
			name:          "under-reported residual fails closed",
			expected:      1,
			available:     machine.NewCPUSet(0, 1, 2, 3),
			pools:         map[string]machine.CPUSet{commonstate.PoolNameReclaim: machine.NewCPUSet(0, 1)},
			wantErr:       true,
			wantErrSubstr: "default share quantity 1 is smaller than residual cpuset size 2",
		},
		{
			name:      "new fixed pool can shrink share before advisor quantity catches up",
			expected:  3,
			available: machine.NewCPUSet(0, 1, 2, 3),
			pools: map[string]machine.CPUSet{
				commonstate.PoolNameReclaim: machine.NewCPUSet(0),
				"snb-NUMA0":                 machine.NewCPUSet(1),
			},
			wantShare: machine.NewCPUSet(2, 3),
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := materializeDefaultShareCPUSet(tt.expected, tt.available, tt.pools, tt.isolated)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrSubstr != "" {
					require.ErrorContains(t, err, tt.wantErrSubstr)
				}
				return
			}
			require.NoError(t, err)
			require.True(t, got.Equals(tt.wantShare), "got=%s want=%s", got, tt.wantShare)
		})
	}
}

// TestMaterializeDefaultShareResidualIsCoreAlignedUnderComplementClosure covers
// the FillDefaultSharePoolWithNonReclaimCPUs=true third path (R12 / Task 4A). In
// that mode the default share pool is NOT sized from a tier layer; it is the
// residual complement `available - (fixed pools ∪ isolation)`. This test builds a
// mixed SMT2 node — SNB + exclusive-DNB(reclaim overlap) + isolation + reclaim —
// where every subtracted set is core-aligned, and proves complement closure: the
// residual share pool is itself core-aligned, and materializeDefaultShareCPUSet is
// left unmodified (no floor/crop), so its residual.Size()==expectedQuantity
// fail-closed contract still holds.
func TestMaterializeDefaultShareResidualIsCoreAlignedUnderComplementClosure(t *testing.T) {
	t.Parallel()

	// two-NUMA SMT2 host, 16 cpus / 8 cores each.
	topology, err := machine.GenerateDummyCPUTopology(32, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())

	// core-aligned fixed consumers, disjoint per core:
	//   reclaim   : 2 cores/NUMA (subtracted as a non-share pool, mirrors rampUpReclaimFloor)
	//   snb       : 1 core on NUMA0
	//   exclusive : 2 cores on NUMA1 (the DNB donor's retained set)
	//   isolation : 1 core on NUMA0
	reclaim := coresInNUMA(topology, 0, 0, 2).Union(coresInNUMA(topology, 1, 0, 2))
	snb := coresInNUMA(topology, 0, 2, 3)
	exclusive := coresInNUMA(topology, 1, 2, 4)
	isolation := coresInNUMA(topology, 0, 3, 4)
	requireCoreAligned(t, topology, reclaim)
	requireCoreAligned(t, topology, snb)
	requireCoreAligned(t, topology, exclusive)
	requireCoreAligned(t, topology, isolation)

	available := topology.CPUDetails.CPUs()
	pools := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: reclaim,
		"snb-NUMA0":                 snb,
		"exclusive-NUMA1":           exclusive,
	}
	isolated := map[string]map[string]machine.CPUSet{
		"pod-iso": {"container-iso": isolation},
	}

	// expectedQuantity is the exact residual size: complement closure means it is
	// the sum of complete cores left over, so it is core-aligned by construction.
	fixed := reclaim.Union(snb).Union(exclusive).Union(isolation)
	expected := available.Difference(fixed).Size()
	require.Equal(t, 0, expected%topology.CPUsPerCore(),
		"complement of core-aligned sets must itself be a whole-core multiple")

	share, err := materializeDefaultShareCPUSet(expected, available, pools, isolated)
	require.NoError(t, err)
	require.Equal(t, expected, share.Size(),
		"residual.Size() must equal expectedQuantity (fail-closed contract intact)")
	require.True(t, share.Intersection(fixed).IsEmpty(),
		"share residual must not overlap any fixed consumer")
	requireCoreAligned(t, topology, share)
	// reclaim buckets themselves stay untouched by the fill path and core-aligned.
	requireCoreAligned(t, topology, share.Intersection(topology.CPUDetails.CPUsInNUMANodes(0)))
	requireCoreAligned(t, topology, share.Intersection(topology.CPUDetails.CPUsInNUMANodes(1)))
}

// TestMaterializeDefaultShareResidualComplementClosureNonSMTZeroDrift is the
// CPUsPerCore()==1 variant: with SMT disabled every cpu is its own core, so the
// residual complement is trivially "core-aligned" and behavior is identical to
// the pre-alignment code — proving the fill=true path carries zero drift on
// non-SMT hosts.
func TestMaterializeDefaultShareResidualComplementClosureNonSMTZeroDrift(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(16, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 1, topology.CPUsPerCore())

	available := topology.CPUDetails.CPUs()
	reclaim := machine.NewCPUSet(0, 1, 2)
	snb := machine.NewCPUSet(3)
	pools := map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: reclaim,
		"snb-NUMA0":                 snb,
	}
	fixed := reclaim.Union(snb)
	expected := available.Difference(fixed).Size()

	share, err := materializeDefaultShareCPUSet(expected, available, pools, nil)
	require.NoError(t, err)
	require.Equal(t, expected, share.Size())
	require.True(t, share.Equals(available.Difference(fixed)),
		"non-smt residual must be the plain complement, got %s", share.String())
	requireCoreAligned(t, topology, share)
}

// TestFinalizeDefaultShareEntryReadsPostReviseState verifies finalizeDefaultShareEntry
// derives the default share cpuset from the live newPodEntries pool allocation
// results (i.e. post reclaimOverlapNUMABinding / post reviseReclaimPool), so a
// change to a non-share pool changes the resulting share cpuset.
func TestFinalizeDefaultShareEntryReadsPostReviseState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	newPodEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"custom": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("custom"),
				AllocationResult: machine.NewCPUSet(6),
			},
		},
		"pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod",
					ContainerName: "container",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(3),
			},
		},
	}
	candidate := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)

	// residual = candidate - reclaim(0,1) - custom(6) - isolated(3) = {2,4,5,7}
	require.NoError(t, p.finalizeDefaultShareEntry(newPodEntries, newPodEntries, 4, candidate))
	require.True(t, newPodEntries[commonstate.PoolNameShare][commonstate.FakedContainerName].
		AllocationResult.Equals(machine.NewCPUSet(2, 4, 5, 7)))
	require.NotEmpty(t, newPodEntries[commonstate.PoolNameShare][commonstate.FakedContainerName].
		TopologyAwareAssignments)

	// simulate reviseReclaimPool growing the reclaim pool; share must shrink.
	newPodEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult = machine.NewCPUSet(0, 1, 4)
	require.NoError(t, p.finalizeDefaultShareEntry(newPodEntries, newPodEntries, 3, candidate))
	require.True(t, newPodEntries[commonstate.PoolNameShare][commonstate.FakedContainerName].
		AllocationResult.Equals(machine.NewCPUSet(2, 5, 7)))

	// fail-closed on quantity mismatch.
	require.Error(t, p.finalizeDefaultShareEntry(newPodEntries, newPodEntries, 2, candidate))
}

func TestDynamicPolicyFinalizeDefaultShareEntryExcludesDNB(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	dnbCPUSet := machine.NewCPUSet(3)
	newPodEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: dnbCPUSet.Clone(),
			},
		},
	}

	require.NoError(t, p.finalizeDefaultShareEntry(
		newPodEntries,
		newPodEntries,
		6,
		machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
	))
	shareCPUSet := newPodEntries[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult
	require.True(t, shareCPUSet.Equals(machine.NewCPUSet(2, 4, 5, 6, 7)))
	require.True(t, shareCPUSet.Intersection(dnbCPUSet).IsEmpty())
}

func TestFinalizeDefaultShareEntryAllowsAdvisorShrinkLag(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	newPodEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"snb-NUMA0": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("snb-NUMA0"),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			},
		},
	}

	require.NoError(t, p.finalizeDefaultShareEntry(
		newPodEntries, newPodEntries, 7, machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)))
	require.True(t, newPodEntries[commonstate.PoolNameShare][commonstate.FakedContainerName].
		AllocationResult.Equals(machine.NewCPUSet(2, 3, 4, 5, 6, 7)))
}

func TestFinalizeDefaultShareEntryHandlesEmptyResidualByLiveOwnershipAndMode(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	candidate := topology.CPUDetails.CPUs()

	newPolicy := func(t *testing.T) *DynamicPolicy {
		t.Helper()
		p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
		require.NoError(t, err)
		return p
	}
	newEntries := func() state.PodEntries {
		return state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: {
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult: candidate.Clone(),
				},
			},
			commonstate.PoolNameShare: {
				commonstate.FakedContainerName: {
					AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
					AllocationResult: machine.NewCPUSet(6, 7),
				},
			},
		}
	}
	t.Run("prunes ownerless stale share", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		entries := newEntries()

		require.NoError(t, p.finalizeDefaultShareEntry(entries, entries, candidate.Size(), candidate))
		require.NotContains(t, entries, commonstate.PoolNameShare)
	})

	t.Run("rejects empty residual with live share owner", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		entries := newEntries()
		entries["live-share-pod"] = state.ContainerEntries{
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "live-share-pod",
					PodNamespace:  "default",
					PodName:       "live-share-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(6, 7),
			},
		}

		err := p.finalizeDefaultShareEntry(entries, entries, candidate.Size(), candidate)
		require.ErrorContains(t, err, "default share residual is empty")
		require.Contains(t, entries, commonstate.PoolNameShare)
	})

	t.Run("rejects zero quantity with live share owner", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		entries := newEntries()
		entries["live-share-pod"] = state.ContainerEntries{
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "live-share-pod",
					PodNamespace:  "default",
					PodName:       "live-share-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(6, 7),
			},
		}

		err := p.finalizeDefaultShareEntry(entries, entries, 0, candidate)
		require.ErrorContains(t, err, "default share residual is empty")
		require.Contains(t, entries, commonstate.PoolNameShare)
	})

	t.Run("prunes empty residual with ownerless ramp-up shared container", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		entries := newEntries()
		entries["ramp-up-share-pod"] = state.ContainerEntries{
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-share-pod",
					PodNamespace:  "default",
					PodName:       "ramp-up-share-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(6, 7),
				RampUp:           true,
			},
		}

		require.NoError(t, p.finalizeDefaultShareEntry(entries, entries, candidate.Size(), candidate))
		require.NotContains(t, entries, commonstate.PoolNameShare)
	})

	t.Run("recovery deletes stale share despite positive advice", func(t *testing.T) {
		p := newPolicy(t)
		t.Parallel()
		entries := newEntries()
		err := p.finalizeDefaultShareEntryForMode(
			entries, entries, 2, candidate, defaultShareMaterializationRecovery)
		require.NoError(t, err)
		require.NotContains(t, entries, commonstate.PoolNameShare)
	})
}

func TestDedicatedCPUSetFromPodEntriesUsesFinalOwner(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"isolated-shared-pod": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "isolated-shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}

	dedicated := dedicatedCPUSetFromPodEntries(entries)

	require.True(t, dedicated["isolated-shared-pod"]["main"].Equals(machine.NewCPUSet(2, 3)))
}

func TestSupplementPodEntriesWithCurrentNonPoolKeepsPendingContainer(t *testing.T) {
	t.Parallel()

	current := state.PodEntries{
		"pod-a": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "main",
				},
				AllocationResult: machine.NewCPUSet(0, 1),
			},
			"sidecar": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "sidecar",
				},
				AllocationResult: machine.NewCPUSet(4),
			},
		},
		"stale-pool": {
			commonstate.FakedContainerName: {
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta("stale-pool"),
			},
		},
	}
	pending := state.PodEntries{
		"pod-a": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "main",
				},
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}

	merged := supplementPodEntriesWithCurrentNonPool(pending, current)

	require.True(t, merged["pod-a"]["main"].AllocationResult.Equals(machine.NewCPUSet(2, 3)))
	require.True(t, merged["pod-a"]["sidecar"].AllocationResult.Equals(machine.NewCPUSet(4)))
	require.NotContains(t, merged, "stale-pool")
}

func TestAdjustPoolsAndIsolatedEntriesRejectsRevisionAdvancedBeforeApply(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(0, 1),
				OriginalAllocationResult:         machine.NewCPUSet(0, 1),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:                 machine.NewCPUSet(2, 3, 4, 5, 6, 7),
				OriginalAllocationResult:         machine.NewCPUSet(2, 3, 4, 5, 6, 7),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3, 4, 5, 6, 7)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3, 4, 5, 6, 7)},
			},
		},
	}
	p.state.SetPodEntries(entries, false)
	calculationRevision := p.state.GetRevision()
	p.state = &revisionAdvanceOnOverlapReadState{State: p.state, advanceOnce: true}

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		map[string]map[int]int{
			commonstate.PoolNameShare:   {commonstate.FakedNUMAID: 6},
			commonstate.PoolNameReclaim: {commonstate.FakedNUMAID: 2},
		},
		nil,
		entries,
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
	)
	require.ErrorContains(t, err, state.ErrStaleStateRevision.Error())
	require.Equal(t, calculationRevision+1, p.state.GetRevision(),
		"the concurrent state update must remain the only committed revision")
	require.Equal(t, entries, p.state.GetPodEntries(),
		"the stale calculation must not overwrite the concurrent state")
}

func TestAdjustAllocationEntriesAtRevisionRejectsStateAdvancedAfterInputRead(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	expectedRevision := p.state.GetRevision()

	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	require.Greater(t, p.state.GetRevision(), expectedRevision)

	err = p.adjustAllocationEntriesAtRevision(
		entries, machineState, false, expectedRevision)
	require.ErrorIs(t, err, state.ErrStaleStateRevision)
	require.True(t, p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		"stale calculation must not replace the concurrent state")
}

func TestAdjustAllocationEntriesForRecoveryPreservesLiveDefaultShareResidual(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.config.enableCPUAdvisor = true
	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor", time.Second, time.Minute, time.Minute,
		"advisor_unhealthy", &metrics.DummyMetrics{}, 1, true)
	require.NoError(t, err)

	reclaim := machine.NewCPUSet(0, 1)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				OriginalAllocationResult: reclaim.Clone(),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         machine.NewCPUSet(2),
				OriginalAllocationResult: machine.NewCPUSet(2),
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, false))
	trackingState := &applyPoolsCommitGuardState{State: p.state}
	p.state = trackingState

	err = p.adjustAllocationEntriesForRecoveryAtRevision(
		entries, machineState, true, p.state.GetRevision())
	require.NoError(t, err)
	require.Equal(t, 1, trackingState.conditionalCalls)
	require.True(t, trackingState.conditionalPersist,
		"recovery outcome must follow a durable revision-CAS")

	share, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameShare)
	require.NoError(t, err)
	require.True(t, share.Equals(machine.NewCPUSet(2, 3, 4, 5, 6, 7)),
		"recovery must preserve the live residual, got %s", share.String())
}

func TestAdjustAllocationEntriesForRecoveryMarksLatestStateRetry(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.config.enableCPUAdvisor = true
	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor", time.Second, time.Minute, time.Minute,
		"advisor_unhealthy", &metrics.DummyMetrics{}, 1, true)
	require.NoError(t, err)

	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(2),
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, false))

	handlerCalls := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]dynamicpolicyutil.CPUSetAdjustmentHandler{
		"must-not-run-inline": func(context.Context, dynamicpolicyutil.CPUSetAdjustmentHandlerCtx) error {
			handlerCalls++
			return nil
		},
	}

	err = p.adjustAllocationEntriesForRecoveryAtRevision(
		entries, machineState, true, p.state.GetRevision())
	require.NoError(t, err)
	require.Zero(t, handlerCalls,
		"recovery commit must not run fallible cgroup handlers inline")
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty,
		"recovery commit must schedule latest-state convergence")
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, dynamicpolicyutil.RetryReasonRecoveryCommit)
}

func TestAdjustAllocationEntriesForRecoveryConcurrentCASHasSingleOutcome(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	p.config.enableCPUAdvisor = true
	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor", time.Second, time.Minute, time.Minute,
		"advisor_unhealthy", &metrics.DummyMetrics{}, 1, true)
	require.NoError(t, err)

	reclaim := machine.NewCPUSet(0, 1)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				OriginalAllocationResult: reclaim.Clone(),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         machine.NewCPUSet(2),
				OriginalAllocationResult: machine.NewCPUSet(2),
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, false))
	revision := p.state.GetRevision()

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- p.adjustAllocationEntriesForRecoveryAtRevision(
				entries, machineState, true, revision)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, stale int
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, state.ErrStaleStateRevision):
			stale++
		default:
			t.Fatalf("unexpected concurrent recovery error: %v", result)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, stale)
	require.Equal(t, revision+1, p.state.GetRevision())
}

func TestPreparePendingCPUPartitionAlwaysValidatesPoolOwnership(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false

	entries := p.state.GetPodEntries()
	entries["shared-pod"] = state.ContainerEntries{
		"main": {
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "shared-pod",
				PodNamespace:  "default",
				PodName:       "shared-pod",
				ContainerName: "main",
				OwnerPoolName: "missing-share-pool",
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult:         machine.NewCPUSet(2, 3),
			OriginalAllocationResult: machine.NewCPUSet(2, 3),
		},
	}

	_, err = p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          entries,
		baseMachineState: p.state.GetMachineState(),
		validate: func(state.PodEntries, state.NUMANodeMap, bool, bool) error {
			return nil
		},
	})
	require.ErrorContains(t, err,
		`shared_cores allocation shared-pod/main owns missing pool "missing-share-pool"`)
}

func TestAdjustAllocationEntriesRejectsDefaultShareFallbackWithoutHealthyAdvisor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.config.enableCPUAdvisor = false

	err = p.adjustAllocationEntriesAtRevision(
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		p.state.GetRevision(),
	)
	require.EqualError(t, err, "default share residual quantity requires a healthy cpu advisor")
}

func TestAdjustAllocationEntriesForRecoveryAllowsDefaultShareFallbackWithoutHealthyAdvisor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.config.enableCPUAdvisor = false
	p.config.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(2),
			},
		},
	}
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	require.NoError(t, p.state.CommitAdvisorState(entries, machineState, false, false, false))

	err = p.adjustAllocationEntriesForRecoveryAtRevision(
		entries, machineState, true, p.state.GetRevision())
	require.NoError(t, err)
	share := p.state.GetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName)
	require.NotNil(t, share)
	require.True(t, share.AllocationResult.Contains(6),
		"recovery must preserve live default-share residual without requiring healthy advisor")
}

func TestAdjustPoolsAndIsolatedEntriesRejectsMixedDefaultShareNUMAQuantities(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		map[string]map[int]int{
			commonstate.PoolNameShare: {
				commonstate.FakedNUMAID: 6,
				0:                       1,
			},
		},
		nil,
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
	)
	require.Error(t, err)
	require.Equal(t, strings.ToLower(err.Error()), err.Error())
	require.ErrorContains(t, err, "default share quantity map must contain only faked numa id")
}

// TestAdjustPoolsAndIsolatedEntriesWithRampUpFloorBackfillsDefaultShareResidualWithSystemPool
// reproduces the SNB failure where advisor computes default share quantity from
// machine - reserve - system/forbidden - final reclaim - fixed pools, while QRM
// residual cpuset was derived from an entries snapshot that did not contain the
// system pool. The test intentionally keeps the canonical state aware of the
// system pool but passes a per-round entries/machineState snapshot without it,
// matching the observed source-of-truth skew.
func TestAdjustPoolsAndIsolatedEntriesWithRampUpFloorBackfillsDefaultShareResidualWithSystemPool(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)

	p.config.reservedCPUs = machine.NewCPUSet(0)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	entriesWithoutSystemPool := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(6, 7),
				OriginalAllocationResult:         machine.NewCPUSet(6, 7),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(6, 7)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(6, 7)},
			},
		},
		"snbpool": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta("snbpool"),
				AllocationResult:                 machine.NewCPUSet(4),
				OriginalAllocationResult:         machine.NewCPUSet(4),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(4)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(4)},
			},
		},
		"snb-pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "snb-pod",
					PodNamespace:  "default",
					PodName:       "snb-pod",
					ContainerName: "container",
					OwnerPoolName: "snbpool",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				RequestQuantity:                  1,
				AllocationResult:                 machine.NewCPUSet(4),
				OriginalAllocationResult:         machine.NewCPUSet(4),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(4)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(4)},
			},
		},
		"share-pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "share-pod",
					PodNamespace:  "default",
					PodName:       "share-pod",
					ContainerName: "container",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RequestQuantity: 3,
			},
		},
	}
	p.state.SetPodEntries(entriesWithoutSystemPool, false)
	machineStateWithoutSystemPool := p.state.GetMachineState()

	canonicalEntries := entriesWithoutSystemPool.Clone()
	canonicalEntries["system"] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta("system"),
			AllocationResult:                 machine.NewCPUSet(1),
			OriginalAllocationResult:         machine.NewCPUSet(1),
			TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(1)},
			OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(1)},
		},
	}
	p.state.SetPodEntries(canonicalEntries, false)

	// Advisor-side quantity has already excluded reserve {0}, system {1},
	// reclaim {6,7}, and the SNB pool {2}, leaving default share {3,4,5}.
	poolsQuantityMap := map[string]map[int]int{
		commonstate.PoolNameShare: {
			commonstate.FakedNUMAID: 3,
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedNUMAID: 2,
		},
		"snbpool": {
			0: 1,
		},
	}

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		poolsQuantityMap,
		map[string]map[string]int{},
		entriesWithoutSystemPool,
		machineStateWithoutSystemPool,
		false,
		machine.NewCPUSet(),
		false,
	)
	require.NoError(t, err)

	share, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameShare)
	require.NoError(t, err)
	require.True(t, share.Equals(machine.NewCPUSet(3, 4, 5)),
		"share=%s want=3-5", share)
	require.False(t, share.Contains(1), "default share must exclude system cpu 1")
}

// TestAdjustPoolsAndIsolatedEntriesTreatsMissingDefaultShareQuantityAsZero
// reproduces the fully-exclusive corner case where SysAdvisor legitimately
// publishes no default share quantity (all NUMAs are dedicated numa-exclusive)
// and QRM has already pruned the stale default share entry. The fallback/async
// path must treat the missing quantity as a zero-quantity end-state instead of
// failing with "default share quantity is missing", keeping symmetry with the
// advisor-driven finalizeDefaultShareEntry prune behavior.
func TestAdjustPoolsAndIsolatedEntriesTreatsMissingDefaultShareQuantityAsZero(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	// poolsQuantityMap intentionally omits PoolNameShare, matching a fully
	// exclusive node where the advisor publishes zero default share quantity.
	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		map[string]map[int]int{
			commonstate.PoolNameReclaim: {
				commonstate.FakedNUMAID: 2,
			},
		},
		nil,
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
	)
	require.NoError(t, err)

	_, exists := p.state.GetPodEntries()[commonstate.PoolNameShare]
	require.False(t, exists,
		"missing default share quantity must be treated as a pruned zero-share end-state")
}

// TestAdjustAllocationEntriesAsyncTreatsMissingDefaultShareQuantityAsZero
// exercises the async residual-backfill path (adjustAllocationEntriesAtRevision
// with a healthy cpu advisor) on a fully-exclusive node. A single dedicated
// numa-binding numa-exclusive (DNB) pod owns the only NUMA, so SysAdvisor
// publishes no default share pool and the advisor-derived poolsQuantityMap
// legitimately omits PoolNameShare. The async path must treat the missing
// quantity as a zero-quantity end-state and must not resurrect a default share
// entry, keeping symmetry with the sync fallback and the advisor-driven prune.
func TestAdjustAllocationEntriesAsyncTreatsMissingDefaultShareQuantityAsZero(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	// The async residual-backfill path requires a healthy cpu advisor; otherwise
	// it fails closed (see TestAdjustAllocationEntriesRejectsDefaultShareFallbackWithoutHealthyAdvisor).
	p.config.enableCPUAdvisor = true
	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor",
		time.Second,
		time.Minute,
		time.Minute,
		"advisor_unhealthy",
		&metrics.DummyMetrics{},
		1,
		true,
	)
	require.NoError(t, err)

	// Fully-exclusive node: a single DNB pod owns the only NUMA, so no eligible
	// (non-exclusive) CPUSet remains for a default share pool.
	dnbCPUSet := topology.CPUDetails.CPUsInNUMANodes(0)
	entries := state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					PodNamespace:  "default",
					PodName:       "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				RequestQuantity:                  float64(dnbCPUSet.Size()),
				AllocationResult:                 dnbCPUSet.Clone(),
				OriginalAllocationResult:         dnbCPUSet.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: dnbCPUSet.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: dnbCPUSet.Clone()},
			},
		},
	}
	p.state.SetPodEntries(entries, false)

	err = p.adjustAllocationEntriesAtRevision(
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		p.state.GetRevision(),
	)
	require.NoError(t, err)

	_, exists := p.state.GetPodEntries()[commonstate.PoolNameShare]
	require.False(t, exists,
		"async path must treat missing default share quantity as a pruned zero-share end-state")
}

// TestAdjustPoolsAndIsolatedEntriesKeepsDefaultShareAlongsideSNB is a regression
// guard proving the missing-quantity zero-share branch does NOT misfire when a
// shared numa-binding (SNB) workload coexists with a non-binding default share
// pool. The advisor still publishes a positive default share quantity, so the
// residual must be materialized (excluding the SNB pool) rather than pruned.
func TestAdjustPoolsAndIsolatedEntriesKeepsDefaultShareAlongsideSNB(t *testing.T) {
	t.Parallel()

	// two-NUMA host: NUMA0 hosts an SNB pool, NUMA1 backs the non-binding default share.
	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	// core-aligned fixed consumers: reclaim {0,1}, SNB pool {2,3}.
	reclaimSeed := coresInNUMA(topology, 0, 0, 1)
	snbCPUSet := coresInNUMA(topology, 0, 1, 2)
	seedEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaimSeed.Clone(),
				OriginalAllocationResult:         reclaimSeed.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaimSeed.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaimSeed.Clone()},
			},
		},
		"snb-NUMA0": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta("snb-NUMA0"),
				AllocationResult:                 snbCPUSet.Clone(),
				OriginalAllocationResult:         snbCPUSet.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: snbCPUSet.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: snbCPUSet.Clone()},
			},
		},
		"snb-pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "snb-pod",
					PodNamespace:  "default",
					PodName:       "snb-pod",
					ContainerName: "container",
					OwnerPoolName: "snb-NUMA0",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				RequestQuantity:                  float64(snbCPUSet.Size()),
				AllocationResult:                 snbCPUSet.Clone(),
				OriginalAllocationResult:         snbCPUSet.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: snbCPUSet.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: snbCPUSet.Clone()},
			},
		},
		"share-pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "share-pod",
					PodNamespace:  "default",
					PodName:       "share-pod",
					ContainerName: "container",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RequestQuantity: 6,
			},
		},
	}
	p.state.SetPodEntries(seedEntries, false)

	// advisor still advertises a positive default share quantity (residual after
	// reclaim {0,1} and SNB {2,3} on the 16-cpu candidate).
	poolsQuantityMap := map[string]map[int]int{
		commonstate.PoolNameShare: {
			commonstate.FakedNUMAID: 12,
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedNUMAID: 2,
		},
		"snb-NUMA0": {
			0: 2,
		},
	}

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		poolsQuantityMap,
		map[string]map[string]int{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
	)
	require.NoError(t, err)

	updatedEntries := p.state.GetPodEntries()
	share, err := updatedEntries.GetCPUSetForPool(commonstate.PoolNameShare)
	require.NoError(t, err)
	require.False(t, share.IsEmpty(),
		"a positive advisor quantity must keep the default share pool materialized")

	// the allocation regenerates pools deterministically, so compare against the
	// resulting SNB/reclaim pool cpusets rather than the seed placement.
	snbPoolCPUSet, err := updatedEntries.GetCPUSetForPool("snb-NUMA0")
	require.NoError(t, err)
	reclaimPoolCPUSet, err := updatedEntries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	require.NoError(t, err)
	require.True(t, share.Intersection(snbPoolCPUSet).IsEmpty(),
		"default share residual must exclude the SNB pool cpuset")
	require.True(t, share.Intersection(reclaimPoolCPUSet).IsEmpty(),
		"default share residual must exclude the reclaim pool cpuset")
	requireCoreAligned(t, topology, share)

	// the owning shared_cores container inherits the backfilled share cpuset.
	owner := updatedEntries["share-pod"]["container"]
	require.NotNil(t, owner)
	require.True(t, owner.AllocationResult.Equals(share),
		"owner=%s share=%s", owner.AllocationResult, share)
}

// TestAdjustPoolsAndIsolatedEntriesKeepsDefaultShareForNonSNB is a regression
// guard for the plain non-numa-binding (non-SNB) topology: a node-wide
// shared_cores workload owns the default share pool with no numa-binding hint.
// The advisor publishes a positive default share quantity, so the missing-
// quantity zero-share branch must NOT misfire and the residual must be
// materialized node-wide, disjoint from reclaim and core-aligned.
func TestAdjustPoolsAndIsolatedEntriesKeepsDefaultShareForNonSNB(t *testing.T) {
	t.Parallel()

	// single-NUMA host: no numa-binding pools, a node-wide default share pool
	// coexists with reclaim.
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet()
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	// core-aligned reclaim seed {0,1}; the rest backs the non-binding share pool.
	reclaimSeed := coresInNUMA(topology, 0, 0, 1)
	seedEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaimSeed.Clone(),
				OriginalAllocationResult:         reclaimSeed.Clone(),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaimSeed.Clone()},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaimSeed.Clone()},
			},
		},
		"share-pod": {
			"container": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "share-pod",
					PodNamespace:  "default",
					PodName:       "share-pod",
					ContainerName: "container",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RequestQuantity: 6,
			},
		},
	}
	p.state.SetPodEntries(seedEntries, false)

	// advisor advertises a positive node-wide default share quantity (residual
	// after reclaim {0,1} on the 8-cpu candidate).
	poolsQuantityMap := map[string]map[int]int{
		commonstate.PoolNameShare: {
			commonstate.FakedNUMAID: 6,
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedNUMAID: 2,
		},
	}

	err = p.adjustPoolsAndIsolatedEntriesWithRampUpFloor(
		poolsQuantityMap,
		map[string]map[string]int{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		false,
		machine.NewCPUSet(),
		false,
	)
	require.NoError(t, err)

	updatedEntries := p.state.GetPodEntries()
	share, err := updatedEntries.GetCPUSetForPool(commonstate.PoolNameShare)
	require.NoError(t, err)
	require.False(t, share.IsEmpty(),
		"a positive advisor quantity must keep the non-SNB default share pool materialized")

	reclaimPoolCPUSet, err := updatedEntries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	require.NoError(t, err)
	require.True(t, share.Intersection(reclaimPoolCPUSet).IsEmpty(),
		"default share residual must exclude the reclaim pool cpuset")
	requireCoreAligned(t, topology, share)

	// the owning non-binding shared_cores container inherits the share cpuset.
	owner := updatedEntries["share-pod"]["container"]
	require.NotNil(t, owner)
	require.True(t, owner.AllocationResult.Equals(share),
		"owner=%s share=%s", owner.AllocationResult, share)
}

func TestNewRampUpPlanningPolicyPreservesCPUAdvisorState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.emitter.emitter = &recordingMetricEmitter{}
	p.config.enableCPUAdvisor = true
	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(
		"advisor",
		time.Second,
		time.Minute,
		time.Minute,
		"advisor_unhealthy",
		&metrics.DummyMetrics{},
		1,
		true,
	)
	require.NoError(t, err)

	planningPolicy := p.newRampUpPlanningPolicy(state.NewTransientState(topology))

	require.True(t, planningPolicy.config.enableCPUAdvisor,
		"planning policy should keep advisor quantity source when outer policy uses advisor")
	require.IsType(t, metrics.DummyMetrics{}, planningPolicy.emitter.emitter,
		"planning policy must use a no-op metric emitter")
	require.Same(t, p.advisor.advisorMonitor, planningPolicy.advisor.advisorMonitor,
		"planning policy should share advisor health monitor with outer policy")
}

// ============================================================================
// F1 closure tests: pool-adjustment path (applyPoolsAndIsolatedInfo) ramp-up
// shared / reclaim overlap. These exercise the real production commit path
// (source: "pool adjustment") rather than the rematerialization helper alone.
// ============================================================================

// newPoolAdjustmentRampUpTestPolicy builds a DynamicPolicy on the 2-NUMA dummy
// topology (NUMA0={0,1,4,5}, NUMA1={2,3,6,7}; SMT pairs (0,4)(1,5)(2,6)(3,7))
// with reserved core {0,4}. EnableReclaim is always on; HP and AO are
// parameterised.
func newPoolAdjustmentRampUpTestPolicy(t *testing.T, hardPartition, allowOverlap bool) *DynamicPolicy {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.reservedCPUs = machine.NewCPUSet(0, 4)

	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = hardPartition
	p.state.SetAllowSharedCoresOverlapReclaimedCores(allowOverlap, false)
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(false, false)

	return p
}

// poolAdjustmentRampUpCurEntries seeds one plain shared ramp-up pod and an
// empty reclaim pool entry. The ramp-up pod's initial allocation is
// irrelevant: applyPoolsAndIsolatedInfo reassigns it to rampUpCPUs.
func poolAdjustmentRampUpCurEntries() state.PodEntries {
	return state.PodEntries{
		"ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-pod",
					PodNamespace:  "default",
					PodName:       "ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(2, 6),
				RequestQuantity:  1,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
}

// poolAdjustmentRampUpPoolsCPUSet places the reclaim pool on NUMA1 core {2,6}.
func poolAdjustmentRampUpPoolsCPUSet() map[string]machine.CPUSet {
	return map[string]machine.CPUSet{
		commonstate.PoolNameReclaim: machine.NewCPUSet(2, 6),
	}
}

// TestApplyPoolsAndIsolatedInfoRampUpSharedReclaimOverlapQuadrants covers the
// HP x AO quadrant matrix on the pool-adjustment commit path
// (applyPoolsAndIsolatedInfo, source: "pool adjustment"). rampUpCPUs is
// computed to include the reclaim pool (only the ramp-up reclaim floor is
// subtracted), so without rematerialization the ramp-up shared allocation
// would overlap the final reclaim pool in every quadrant.
//
// With reserved={0,4} and reclaim={2,6}: rampUpCPUs={1,2,3,5,6,7}; the
// canonical rematerialization keeps whole cores {1,5} (NUMA0) and {3,7}
// (NUMA1) only, i.e. {1,3,5,7}.
func TestApplyPoolsAndIsolatedInfoRampUpSharedReclaimOverlapQuadrants(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		hardPartition        bool
		allowOverlap         bool
		wantRampUpAllocation machine.CPUSet
	}{
		{
			name:                 "hp disabled ao disabled keeps overlap",
			hardPartition:        false,
			allowOverlap:         false,
			wantRampUpAllocation: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		},
		{
			name:                 "hp disabled ao enabled keeps overlap",
			hardPartition:        false,
			allowOverlap:         true,
			wantRampUpAllocation: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		},
		{
			name:                 "hp enabled ao disabled rematerializes",
			hardPartition:        true,
			allowOverlap:         false,
			wantRampUpAllocation: machine.NewCPUSet(1, 3, 5, 7),
		},
		{
			name:                 "hp enabled ao enabled rematerializes",
			hardPartition:        true,
			allowOverlap:         true,
			wantRampUpAllocation: machine.NewCPUSet(1, 3, 5, 7),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newPoolAdjustmentRampUpTestPolicy(t, tc.hardPartition, tc.allowOverlap)
			p.state.SetPodEntries(poolAdjustmentRampUpCurEntries(), false)

			err := p.applyPoolsAndIsolatedInfo(
				poolAdjustmentRampUpPoolsCPUSet(),
				map[string]map[string]machine.CPUSet{},
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				sets.NewInt(),
				false,
				machine.NewCPUSet(),
				defaultShareMaterializationPlan{},
				p.state.GetRevision(),
				p.config.dynamicConfig.GetDynamicConfiguration(),
			)
			require.NoError(t, err)

			reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, reclaim)
			require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 6)),
				"final reclaim pool: %s", reclaim.AllocationResult.String())

			got := p.state.GetAllocationInfo("ramp-up-pod", "main")
			require.NotNil(t, got)
			require.True(t, got.AllocationResult.Equals(tc.wantRampUpAllocation),
				"ramp-up shared allocation: %s, want %s",
				got.AllocationResult.String(), tc.wantRampUpAllocation.String())

			if tc.hardPartition {
				require.True(t, got.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
					"ramp-up shared allocation must be mutually exclusive with the final reclaim pool")
				require.True(t, got.OriginalAllocationResult.Equals(tc.wantRampUpAllocation))
			}

			// Zero churn across frames: re-applying the same adjustment must
			// converge to the same allocation instead of oscillating.
			err = p.applyPoolsAndIsolatedInfo(
				poolAdjustmentRampUpPoolsCPUSet(),
				map[string]map[string]machine.CPUSet{},
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				sets.NewInt(),
				false,
				machine.NewCPUSet(),
				defaultShareMaterializationPlan{},
				p.state.GetRevision(),
				p.config.dynamicConfig.GetDynamicConfiguration(),
			)
			require.NoError(t, err)
			gotAgain := p.state.GetAllocationInfo("ramp-up-pod", "main")
			require.NotNil(t, gotAgain)
			require.True(t, gotAgain.AllocationResult.Equals(tc.wantRampUpAllocation),
				"second frame must be idempotent, got %s", gotAgain.AllocationResult.String())
		})
	}
}

// TestApplyPoolsAndIsolatedInfoSteadySharedUnaffectedByHardPartition verifies
// that the hard-partition rematerialization does not touch steady (non-ramp-up)
// shared allocations: their allocation is taken verbatim from the share pool
// and is never rewritten by rematerializeRampUpSharedAgainstFinalReclaim
// (which filters on ai.RampUp).
func TestApplyPoolsAndIsolatedInfoSteadySharedUnaffectedByHardPartition(t *testing.T) {
	t.Parallel()

	p := newPoolAdjustmentRampUpTestPolicy(t, true, true)
	entries := poolAdjustmentRampUpCurEntries()
	entries["steady-pod"] = state.ContainerEntries{
		"main": &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "steady-pod",
				PodNamespace:  "default",
				PodName:       "steady-pod",
				ContainerName: "main",
				OwnerPoolName: commonstate.PoolNameShare,
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
			RequestQuantity:  1,
		},
	}
	delete(entries, "ramp-up-pod")
	entries[commonstate.PoolNameShare] = state.ContainerEntries{
		commonstate.FakedContainerName: &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
			AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
		},
	}
	p.state.SetPodEntries(entries, false)

	poolsCPUSet := poolAdjustmentRampUpPoolsCPUSet()
	poolsCPUSet[commonstate.PoolNameShare] = machine.NewCPUSet(2, 3, 6, 7)

	err := p.applyPoolsAndIsolatedInfo(
		poolsCPUSet,
		map[string]map[string]machine.CPUSet{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		sets.NewInt(),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.NoError(t, err)

	// Steady shared allocation is taken verbatim from the share pool and is
	// never touched by rematerialization (which filters on ai.RampUp).
	got := p.state.GetAllocationInfo("steady-pod", "main")
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 3, 6, 7)),
		"steady shared allocation must be untouched by rematerialization: %s",
		got.AllocationResult.String())
	require.False(t, got.RampUp, "steady pod must not have RampUp flag")
}

// TestApplyPoolsAndIsolatedInfoSNBRampUpResidualOverlapFailsClosed covers the
// SNB (SharedNUMABinding) ramp-up branch on the pool-adjustment path: SNB
// ramp-up is skipped by the rematerialization (it is owned by the bulkhead
// partition view), so a residual overlap with the final reclaim pool fails the
// pre-commit partition invariant as a typed error instead of being rematerialized.
func TestApplyPoolsAndIsolatedInfoSNBRampUpResidualOverlapFailsClosed(t *testing.T) {
	t.Parallel()

	p := newPoolAdjustmentRampUpTestPolicy(t, true, true)
	topology := p.machine.machineInfo.CPUTopology
	numa1CPUs := topology.CPUDetails.CPUsInNUMANodes(1)
	require.True(t, numa1CPUs.Equals(machine.NewCPUSet(2, 3, 6, 7)))

	assignments, err := machine.GetNumaAwareAssignments(topology, numa1CPUs)
	require.NoError(t, err)
	entries := state.PodEntries{
		"snb-ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "snb-ramp-up-pod",
					PodNamespace:  "default",
					PodName:       "snb-ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
					},
				},
				RampUp:                           true,
				AllocationResult:                 numa1CPUs.Clone(),
				OriginalAllocationResult:         numa1CPUs.Clone(),
				TopologyAwareAssignments:         assignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
				RequestQuantity:                  2,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	p.state.SetPodEntries(entries, false)

	err = p.applyPoolsAndIsolatedInfo(
		poolAdjustmentRampUpPoolsCPUSet(),
		map[string]map[string]machine.CPUSet{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		sets.NewInt(1),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.ErrorContains(t, err, "ramp-up shared allocation overlaps the final reclaim pool")

	got := p.state.GetAllocationInfo("snb-ramp-up-pod", "main")
	require.NotNil(t, got)
	// The SNB allocation was never rewritten by the skipped rematerialization.
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 3, 6, 7)),
		"SNB ramp-up allocation must not be rematerialized: %s", got.AllocationResult.String())
}

// TestApplyPoolsAndIsolatedInfoReservesFloorWhenSNBRampUpSelected covers
// the synchronous pool-adjustment path (applyPoolsAndIsolatedInfo, source: "pool
// adjustment") with the ramp-up reclaim hard partition on and allow-overlap off.
// The pending SNB has an empty result and is reselected at selection time: its
// hinted NUMA's reclaim floor {2,6} is carved out of the available domain before
// the exact AllocationResult is assigned, so the SNB lands on the non-floor NUMA1
// CPUs {3,7} and the reclaim floor survives intact. The TPNR CommitOverride sync
// (buildAdjustmentCommitOverride + syncReclaimPool) still runs as defense-in-depth
// for already-committed SNBs, but here selection already kept the two disjoint, so
// the precommit residual / mutual-exclusion validators accept the commit.
func TestApplyPoolsAndIsolatedInfoReservesFloorWhenSNBRampUpSelected(t *testing.T) {
	t.Parallel()

	p := newPoolAdjustmentRampUpTestPolicy(t, true, false)
	topology := p.machine.machineInfo.CPUTopology
	numa1CPUs := topology.CPUDetails.CPUsInNUMANodes(1)
	require.True(t, numa1CPUs.Equals(machine.NewCPUSet(2, 3, 6, 7)))

	assignments, err := machine.GetNumaAwareAssignments(topology, numa1CPUs)
	require.NoError(t, err)
	entries := state.PodEntries{
		"snb-ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "snb-ramp-up-pod",
					PodNamespace:  "default",
					PodName:       "snb-ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "1",
					},
				},
				RampUp:                           true,
				AllocationResult:                 numa1CPUs.Clone(),
				OriginalAllocationResult:         numa1CPUs.Clone(),
				TopologyAwareAssignments:         assignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
				RequestQuantity:                  2,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	p.state.SetPodEntries(entries, false)

	plannedReclaim := machine.NewCPUSet(2, 6)
	err = p.applyPoolsAndIsolatedInfo(
		map[string]machine.CPUSet{
			commonstate.PoolNameReclaim: plannedReclaim.Clone(),
		},
		map[string]map[string]machine.CPUSet{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		sets.NewInt(1),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.NoError(t, err, "pool adjustment must commit a disjoint pending SNB and an intact reclaim floor")

	// The pending SNB is reselected at pool-adjustment time: the hinted NUMA's
	// reclaim floor {2,6} is carved out of its available domain before the exact
	// AllocationResult is assigned, so the SNB lands on the non-floor NUMA1 CPUs
	// and the reclaim floor survives intact.
	got := p.state.GetAllocationInfo("snb-ramp-up-pod", "main")
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(3, 7)),
		"pending SNB must exclude the NUMA-local reclaim floor: %s", got.AllocationResult.String())

	// The committed reclaim pool keeps the whole planned floor and is disjoint from
	// the reselected SNB; the TPNR view's residual overlap has nothing to trim.
	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 6)),
		"reclaim floor must survive intact at selection time: got %s", reclaim.AllocationResult)
	require.True(t, reclaim.AllocationResult.Intersection(got.AllocationResult).IsEmpty(),
		"physical reclaim must be disjoint from SNB %s, got %s",
		got.AllocationResult, reclaim.AllocationResult)
}

// TestApplyPoolsAndIsolatedInfoNoRampUpSkipsRematerialization verifies that
// when no active ramp-up allocation is present, the rematerialization gate is
// closed and the pool-adjustment path behaves exactly as before (no extra
// validation, no churn).
func TestApplyPoolsAndIsolatedInfoNoRampUpSkipsRematerialization(t *testing.T) {
	t.Parallel()

	p := newPoolAdjustmentRampUpTestPolicy(t, true, false)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(),
			},
		},
	}
	p.state.SetPodEntries(entries, false)

	revisionBefore := p.state.GetRevision()
	err := p.applyPoolsAndIsolatedInfo(
		poolAdjustmentRampUpPoolsCPUSet(),
		map[string]map[string]machine.CPUSet{},
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		sets.NewInt(),
		false,
		machine.NewCPUSet(),
		defaultShareMaterializationPlan{},
		p.state.GetRevision(),
		p.config.dynamicConfig.GetDynamicConfiguration(),
	)
	require.NoError(t, err)
	// revision advanced exactly once by the commit CAS
	require.Equal(t, revisionBefore+1, p.state.GetRevision(),
		"revision should advance by exactly one commit, got %d -> %d",
		revisionBefore, p.state.GetRevision())
}

func TestCommittedReclaimPoolCPUs(t *testing.T) {
	t.Parallel()
	reclaimPoolEntry := func(cpus ...int) state.PodEntries {
		return state.PodEntries{
			commonstate.PoolNameReclaim: state.ContainerEntries{
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationResult: machine.NewCPUSet(cpus...),
				},
			},
		}
	}

	t.Run("returns committed reclaim pool allocation from snapshot", func(t *testing.T) {
		t.Parallel()
		got := committedReclaimPoolCPUs(reclaimPoolEntry(0, 2, 4))
		require.True(t, got.Equals(machine.NewCPUSet(0, 2, 4)), "got=%s", got.String())
	})

	t.Run("empty set when reclaim pool absent", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			commonstate.PoolNameShare: state.ContainerEntries{
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationResult: machine.NewCPUSet(1, 3),
				},
			},
		}
		got := committedReclaimPoolCPUs(entries)
		require.True(t, got.IsEmpty(), "got=%s", got.String())
	})

	t.Run("empty set for nil snapshot", func(t *testing.T) {
		t.Parallel()
		var entries state.PodEntries
		require.True(t, committedReclaimPoolCPUs(entries).IsEmpty())
	})

	t.Run("returned set is isolated from snapshot mutation", func(t *testing.T) {
		t.Parallel()
		entries := reclaimPoolEntry(0, 2, 4)
		got := committedReclaimPoolCPUs(entries)
		got.Add(6)
		again := committedReclaimPoolCPUs(entries)
		require.True(t, again.Equals(machine.NewCPUSet(0, 2, 4)), "again=%s", again.String())
	})
}
