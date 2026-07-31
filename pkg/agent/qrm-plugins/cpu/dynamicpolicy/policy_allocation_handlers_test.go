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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/accompanyresource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type rampUpReclaimCtxKey string

type ctxCheckingPodFetcher struct {
	metapod.PodFetcherStub
	key  rampUpReclaimCtxKey
	want string
}

type rollbackStoreState struct {
	state.State
	storeErr             error
	storeCalls           int
	setPodEntryPersists  []bool
	setMachinePersists   []bool
	setPodEntrySnapshots []state.PodEntries
}

func (s *rollbackStoreState) SetPodEntries(podEntries state.PodEntries, persist bool) {
	s.setPodEntryPersists = append(s.setPodEntryPersists, persist)
	s.setPodEntrySnapshots = append(s.setPodEntrySnapshots, podEntries.Clone())
	s.State.SetPodEntries(podEntries, persist)
}

func (s *rollbackStoreState) SetMachineState(numaNodeMap state.NUMANodeMap, persist bool) {
	s.setMachinePersists = append(s.setMachinePersists, persist)
	s.State.SetMachineState(numaNodeMap, persist)
}

func (s *rollbackStoreState) StoreState() error {
	s.storeCalls++
	return s.storeErr
}

type failingAccompanyResourcePlugin struct {
	err error
}

func (p *failingAccompanyResourcePlugin) ResourceName() string {
	return "failing-accompany-resource"
}

func (p *failingAccompanyResourcePlugin) GetAccompanyResourceTopologyHints(_ *pluginapi.ResourceRequest, _ *pluginapi.ListOfTopologyHints) error {
	return nil
}

func (p *failingAccompanyResourcePlugin) AllocateAccompanyResource(_ *pluginapi.ResourceRequest, _ *pluginapi.ResourceAllocationResponse) error {
	return p.err
}

func (p *failingAccompanyResourcePlugin) ReleaseAccompanyResource(_ *pluginapi.RemovePodRequest) error {
	return nil
}

func (f *ctxCheckingPodFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	if got, _ := ctx.Value(f.key).(string); got == f.want {
		return nil, fmt.Errorf("ctx marker preserved for %s", podUID)
	}
	return nil, fmt.Errorf("ctx marker missing for %s", podUID)
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

func TestSharedCoresWithoutNUMABindingAllocationHandlerWritesHardReclaimPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSharedCoresWithoutNUMABindingAllocationHandlerWritesHardReclaimPool")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	req := &pluginapi.ResourceRequest{
		PodUid:         "shared-hard-reclaim",
		PodNamespace:   "default",
		PodName:        "shared-hard-reclaim",
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
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resp, err := p.sharedCoresWithoutNUMABindingAllocationHandler(nil, req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocationInfo)

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.False(t, reclaimInfo.AllocationResult.IsEmpty())
	require.True(t, reclaimInfo.AllocationResult.IsSubsetOf(allocationInfo.AllocationResult), "reclaim=%s allocation=%s", reclaimInfo.AllocationResult.String(), allocationInfo.AllocationResult.String())
}

func TestSharedCoresWithoutNUMABindingHardReclaimCheckpointIsAtomic(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSharedCoresWithoutNUMABindingHardReclaimCheckpointIsAtomic")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	req := &pluginapi.ResourceRequest{
		PodUid:         "shared-hard-reclaim-atomic-success",
		PodNamespace:   "default",
		PodName:        "shared-hard-reclaim-atomic-success",
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
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resp, err := p.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, true)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 1, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
}

func TestSharedCoresWithoutNUMABindingHardReclaimRollsBackOnPlannerFailure(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSharedCoresWithoutNUMABindingHardReclaimRollsBackOnPlannerFailure")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	rollbackEntries := p.state.GetPodEntries()
	rollbackMachineState := p.state.GetMachineState()
	req := &pluginapi.ResourceRequest{
		PodUid:         "shared-hard-reclaim-atomic-failure",
		PodNamespace:   "default",
		PodName:        "shared-hard-reclaim-atomic-failure",
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
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
	}

	resp, err := p.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, true)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "calculate ramp-up reclaim target")
	require.ErrorContains(t, err, "bootstrap target must be positive")
	require.Equal(t, 1, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestSharedCoresWithNUMABindingAllocationHandlerWritesHardReclaimPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestSharedCoresWithNUMABindingAllocationHandlerWritesHardReclaimPool")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	req := &pluginapi.ResourceRequest{
		PodUid:         "snb-hard-reclaim",
		PodNamespace:   "default",
		PodName:        "snb-hard-reclaim",
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

	resp, err := p.sharedCoresWithNUMABindingAllocationHandler(context.Background(), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocationInfo)

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.False(t, reclaimInfo.AllocationResult.IsEmpty())
	require.True(t, reclaimInfo.AllocationResult.Intersection(allocationInfo.AllocationResult).IsEmpty(), "reclaim=%s allocation=%s", reclaimInfo.AllocationResult.String(), allocationInfo.AllocationResult.String())
}

func TestDedicatedCoresWithNUMAExclusiveAllocationHandlerWritesHardReclaimPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNUMAExclusiveAllocationHandlerWritesHardReclaimPool")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	req := &pluginapi.ResourceRequest{
		PodUid:         "exclusive-dnb-hard-reclaim",
		PodNamespace:   "default",
		PodName:        "exclusive-dnb-hard-reclaim",
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
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:       "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocationInfo)
	require.True(t, allocationInfo.CheckDedicatedNUMABindingNUMAExclusive())

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.False(t, reclaimInfo.AllocationResult.IsEmpty())
	require.Equal(t, reclaimInfo.TopologyAwareAssignments, reclaimInfo.OriginalTopologyAwareAssignments)
}

func TestDedicatedCoresWithNonExclusiveNUMABindingAllocationHandlerWritesHardReclaimPool(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNonExclusiveNUMABindingAllocationHandlerWritesHardReclaimPool")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	req := &pluginapi.ResourceRequest{
		PodUid:         "non-exclusive-dnb-hard-reclaim",
		PodNamespace:   "default",
		PodName:        "non-exclusive-dnb-hard-reclaim",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, false)
	require.NoError(t, err)
	require.NotNil(t, resp)

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, allocationInfo)
	require.True(t, allocationInfo.RampUp)
	require.False(t, allocationInfo.CheckDedicatedNUMABindingNUMAExclusive())
	specifiedNUMAID, err := allocationInfo.GetSpecifiedNUMABindingNUMAID()
	require.NoError(t, err)
	require.Equal(t, 0, specifiedNUMAID)

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	hardReclaim := machine.NewCPUSet(0)
	require.True(t, hardReclaim.IsSubsetOf(reclaimInfo.AllocationResult), "hard=%s reclaim=%s", hardReclaim.String(), reclaimInfo.AllocationResult.String())
	require.True(t, reclaimInfo.OriginalAllocationResult.Equals(reclaimInfo.AllocationResult))
	require.Equal(t, reclaimInfo.TopologyAwareAssignments, reclaimInfo.OriginalTopologyAwareAssignments)
	require.True(t, reclaimInfo.AllocationResult.Intersection(allocationInfo.AllocationResult).IsEmpty(),
		"reclaim=%s allocation=%s", reclaimInfo.AllocationResult.String(), allocationInfo.AllocationResult.String())

	foundPlannerWrite := false
	for _, entries := range wrappedState.setPodEntrySnapshots {
		if entries[commonstate.PoolNameReclaim] == nil || entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName] == nil {
			continue
		}
		plannedReclaimInfo := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
		if plannedReclaimInfo.AllocationResult.Equals(hardReclaim) &&
			reflect.DeepEqual(plannedReclaimInfo.TopologyAwareAssignments, map[int]machine.CPUSet{0: hardReclaim}) {
			foundPlannerWrite = true
			break
		}
	}
	require.True(t, foundPlannerWrite, "expected planner SetPodEntries snapshot to write hard reclaim %s", hardReclaim.String())
}

func TestDedicatedCoresWithNonExclusiveNUMABindingHardReclaimCheckpointIsAtomic(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNonExclusiveNUMABindingHardReclaimCheckpointIsAtomic")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	req := &pluginapi.ResourceRequest{
		PodUid:         "non-exclusive-dnb-hard-reclaim-atomic-success",
		PodNamespace:   "default",
		PodName:        "non-exclusive-dnb-hard-reclaim-atomic-success",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, true)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 1, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
}

func TestDedicatedCoresWithNonExclusiveNUMABindingStoreFailureRollsBackMemory(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNonExclusiveNUMABindingStoreFailureRollsBackMemory")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	storeErr := errors.New("dedicated store failed")
	wrappedState := &rollbackStoreState{
		State:    p.state,
		storeErr: storeErr,
	}
	p.state = wrappedState

	rollbackEntries := p.state.GetPodEntries()
	rollbackMachineState := p.state.GetMachineState()
	req := &pluginapi.ResourceRequest{
		PodUid:         "non-exclusive-dnb-store-failure",
		PodNamespace:   "default",
		PodName:        "non-exclusive-dnb-store-failure",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, true)
	require.Nil(t, resp)
	require.ErrorIs(t, err, storeErr)
	require.ErrorContains(t, err, "store dedicated NUMA-binding allocation checkpoint failed")
	require.Equal(t, 1, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestDedicatedCoresWithNUMABindingFirstAllocationAccompanyFailureRollsBackMemory(t *testing.T) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNUMABindingFirstAllocationAccompanyFailureRollsBackMemory")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	accompanyErr := errors.New("first allocation accompany failed")
	oldRegistry := AccompanyResourceRegistry
	AccompanyResourceRegistry = accompanyresource.NewRegistry()
	require.NoError(t, AccompanyResourceRegistry.RegisterPlugin(&failingAccompanyResourcePlugin{err: accompanyErr}))
	defer func() {
		AccompanyResourceRegistry = oldRegistry
	}()

	rollbackEntries := p.state.GetPodEntries()
	rollbackMachineState := p.state.GetMachineState()
	req := &pluginapi.ResourceRequest{
		PodUid:         "first-dnb-accompany-failure",
		PodNamespace:   "default",
		PodName:        "first-dnb-accompany-failure",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, true)

	require.Nil(t, resp)
	require.ErrorContains(t, err, "accompany resource AugmentAllocationResult failed")
	require.ErrorContains(t, err, accompanyErr.Error())
	require.Equal(t, 2, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestDedicatedCoresWithNUMABindingReallocationFailureKeepsOldAllocation(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNUMABindingReallocationFailureKeepsOldAllocation")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	req := &pluginapi.ResourceRequest{
		PodUid:         "dnb-reallocation-failure",
		PodNamespace:   "default",
		PodName:        "dnb-reallocation-failure",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, true)
	require.NoError(t, err)
	require.NotNil(t, resp)

	oldAllocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, oldAllocationInfo)
	oldAllocationInfo = oldAllocationInfo.Clone()
	rollbackEntries := p.state.GetPodEntries().Clone()
	rollbackMachineState := p.state.GetMachineState().Clone()
	wrappedState.storeCalls = 0

	failedReq := req
	failedReq.ResourceRequests = map[string]float64{
		string(v1.ResourceCPU): 9,
	}

	resp, err = p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), failedReq, true)
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, 1, wrappedState.storeCalls)

	currentAllocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	require.NotNil(t, currentAllocationInfo)
	require.True(t, currentAllocationInfo.AllocationResult.Equals(oldAllocationInfo.AllocationResult),
		"current=%s old=%s", currentAllocationInfo.AllocationResult.String(), oldAllocationInfo.AllocationResult.String())
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestDedicatedCoresWithNonExclusiveNUMABindingHardReclaimRollsBackOnAdjustFailure(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDedicatedCoresWithNonExclusiveNUMABindingHardReclaimRollsBackOnAdjustFailure")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	wrappedState := &rollbackStoreState{State: p.state}
	p.state = wrappedState

	rollbackEntries := p.state.GetPodEntries()
	rollbackMachineState := p.state.GetMachineState()
	adjustErr := errors.New("adjust handler failed")
	require.NoError(t, p.RegisterCPUSetAdjustmentHandler("fail-adjust", func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
		return adjustErr
	}))
	req := &pluginapi.ResourceRequest{
		PodUid:         "non-exclusive-dnb-hard-reclaim-atomic-failure",
		PodNamespace:   "default",
		PodName:        "non-exclusive-dnb-hard-reclaim-atomic-failure",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, true)
	require.Nil(t, resp)
	require.ErrorContains(t, err, "adjustAllocationEntries failed")
	require.ErrorContains(t, err, adjustErr.Error())
	require.Equal(t, 1, wrappedState.storeCalls)
	require.NotContains(t, wrappedState.setPodEntryPersists, true)
	require.NotContains(t, wrappedState.setMachinePersists, true)
	require.Nil(t, p.state.GetAllocationInfo(req.PodUid, req.ContainerName))
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestWriteRampUpReclaimPoolTargetFallsBackOnPodEnableReclaimContextError(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestWriteRampUpReclaimPoolTargetFallsBackOnPodEnableReclaimContextError")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true

	ctxKey := rampUpReclaimCtxKey("ramp-up-reclaim")
	p.metaServer.PodFetcher = &ctxCheckingPodFetcher{key: ctxKey, want: "preserve-me"}

	err = p.writeRampUpReclaimPoolTarget(context.WithValue(context.Background(), ctxKey, "preserve-me"), &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:        "ctx-pod",
			PodNamespace:  "default",
			PodName:       "ctx-pod",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(0, 1),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		},
	}, false, p.state.GetPodEntries(), p.state.GetMachineState())
	require.NoError(t, err)

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.True(t, reclaimInfo.AllocationResult.Equals(machine.NewCPUSet(0)), "reclaim=%s", reclaimInfo.AllocationResult.String())
}

func TestSharedCoresWithoutNUMABindingAllocationHandlerFallsBackOnPodEnableReclaimError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		nilMetaServer bool
	}{
		{
			name:          "metaServer nil",
			nilMetaServer: true,
		},
		{
			name: "GetPod failure",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			p.reservedCPUs = machine.NewCPUSet()
			p.reservedReclaimedCPUSet = machine.NewCPUSet(0, 2)
			p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			if tt.nilMetaServer {
				p.metaServer = nil
			}

			req := &pluginapi.ResourceRequest{
				PodUid:         "shared-fallback-" + tt.name,
				PodNamespace:   "default",
				PodName:        "shared-fallback",
				ContainerName:  "main",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			}
			pooledCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
			pooledAssignments, err := machine.GetNumaAwareAssignments(cpuTopology, pooledCPUs)
			require.NoError(t, err)
			p.state.SetAllocationInfo(req.PodUid, req.ContainerName, &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
					commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				RampUp:                           true,
				AllocationResult:                 pooledCPUs,
				OriginalAllocationResult:         pooledCPUs.Clone(),
				TopologyAwareAssignments:         pooledAssignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(pooledAssignments),
				InitTimestamp:                    time.Now().Format(qrmutil.QRMTimeFormat),
				RequestQuantity:                  2,
			}, false)

			resp, err := p.sharedCoresWithoutNUMABindingAllocationHandler(context.Background(), req, false)
			require.NoError(t, err)
			require.NotNil(t, resp)

			reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, reclaimInfo)
			require.True(t, machine.NewCPUSet(0).IsSubsetOf(reclaimInfo.AllocationResult), "reclaim=%s", reclaimInfo.AllocationResult.String())
		})
	}
}

func TestDedicatedCoresWithNUMABindingAllocationHandlerFallsBackOnPodEnableReclaimError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		nilMetaServer bool
	}{
		{
			name:          "metaServer nil",
			nilMetaServer: true,
		},
		{
			name: "GetPod failure",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			p.reservedCPUs = machine.NewCPUSet()
			p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
			p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			if tt.nilMetaServer {
				p.metaServer = nil
			}

			req := &pluginapi.ResourceRequest{
				PodUid:         "dnb-fallback-" + tt.name,
				PodNamespace:   "default",
				PodName:        "dnb-fallback",
				ContainerName:  "main",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{
					string(v1.ResourceCPU): 2,
				},
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
					apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
				},
				Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
			}

			resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, false)
			require.NoError(t, err)
			require.NotNil(t, resp)

			allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
			require.NotNil(t, allocationInfo)
			require.True(t, allocationInfo.RampUp)
			require.False(t, allocationInfo.CheckDedicatedNUMABindingNUMAExclusive())
		})
	}
}

func TestWriteRampUpReclaimPoolTargetFallsBackOnPodEnableReclaimError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		nilMetaServer bool
	}{
		{
			name:          "metaServer nil",
			nilMetaServer: true,
		},
		{
			name: "GetPod failure",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
			require.NoError(t, err)
			p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
			p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
			if tt.nilMetaServer {
				p.metaServer = nil
			}

			err = p.writeRampUpReclaimPoolTarget(context.Background(), &state.AllocationInfo{
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "snb-fallback-" + tt.name,
					PodNamespace:  "default",
					PodName:       "snb-fallback",
					ContainerName: "main",
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(0, 1),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
				},
			}, false, p.state.GetPodEntries(), p.state.GetMachineState())
			require.NoError(t, err)

			reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, reclaimInfo)
			require.True(t, reclaimInfo.AllocationResult.Equals(machine.NewCPUSet(0)), "reclaim=%s", reclaimInfo.AllocationResult.String())
		})
	}
}

func TestWriteRampUpReclaimPoolTargetRollsBackFullStateOnPlannerError(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestWriteRampUpReclaimPoolTargetRollsBackFullStateOnPlannerError")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	rollbackEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"existing-pod": {
			"main": {
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "existing-pod",
					ContainerName: "main",
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
	}
	rollbackMachineState := state.NUMANodeMap{
		0: {DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3)},
		1: {DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7)},
	}

	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(6, 7),
			},
		},
		"mutated-pod": {
			"main": {
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "mutated-pod",
					ContainerName: "main",
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				AllocationResult: machine.NewCPUSet(6),
			},
		},
	}, false)
	p.state.SetMachineState(state.NUMANodeMap{
		0: {DefaultCPUSet: machine.NewCPUSet(6)},
	}, false)
	err = p.writeRampUpReclaimPoolTarget(context.Background(), &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:        "snb-pod",
			PodNamespace:  "default",
			PodName:       "snb-pod",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
		},
	}, false, rollbackEntries, rollbackMachineState)
	require.ErrorContains(t, err, "eligible CPUSet for hard ramp-up reclaim must not be empty")

	gotEntries := p.state.GetPodEntries()
	require.Contains(t, gotEntries, "existing-pod")
	require.NotContains(t, gotEntries, "mutated-pod")
	require.True(t, gotEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.Equals(machine.NewCPUSet(0)))

	gotMachineState := p.state.GetMachineState()
	require.Len(t, gotMachineState, 2)
	require.True(t, gotMachineState[0].DefaultCPUSet.Equals(machine.NewCPUSet(0, 1, 2, 3)))
	require.True(t, gotMachineState[1].DefaultCPUSet.Equals(machine.NewCPUSet(4, 5, 6, 7)))
}

func TestWriteRampUpReclaimPoolTargetRollbackPersistsCheckpointForReload(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestWriteRampUpReclaimPoolTargetRollbackPersistsCheckpointForReload")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	rollbackEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"existing-pod": {
			"main": {
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "existing-pod",
					ContainerName: "main",
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
	}
	rollbackMachineState, err := generateMachineStateFromPodEntries(cpuTopology, rollbackEntries, p.state.GetMachineState())
	require.NoError(t, err)

	mutatedEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(6, 7),
			},
		},
		"mutated-pod": {
			"main": {
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "mutated-pod",
					ContainerName: "main",
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				AllocationResult: machine.NewCPUSet(6),
			},
		},
	}
	mutatedMachineState, err := generateMachineStateFromPodEntries(cpuTopology, mutatedEntries, p.state.GetMachineState())
	require.NoError(t, err)
	p.state.SetPodEntries(mutatedEntries, true)
	p.state.SetMachineState(mutatedMachineState, true)

	err = p.writeRampUpReclaimPoolTarget(context.Background(), &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:        "snb-pod",
			PodNamespace:  "default",
			PodName:       "snb-pod",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
		},
	}, true, rollbackEntries, rollbackMachineState)
	require.ErrorContains(t, err, "eligible CPUSet for hard ramp-up reclaim must not be empty")

	reloadedPolicy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	gotEntries := reloadedPolicy.state.GetPodEntries()
	require.Contains(t, gotEntries, "existing-pod")
	require.NotContains(t, gotEntries, "mutated-pod")
	require.True(t, gotEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.Equals(machine.NewCPUSet(0)))

	gotMachineState := reloadedPolicy.state.GetMachineState()
	require.True(t, reflect.DeepEqual(rollbackMachineState, gotMachineState), "want %s, got %s", rollbackMachineState.String(), gotMachineState.String())
}

func TestWriteRampUpReclaimPoolTargetRollbackReturnsStoreError(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestWriteRampUpReclaimPoolTargetRollbackReturnsStoreError")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	storeErr := errors.New("store rollback checkpoint failed")
	wrappedState := &rollbackStoreState{
		State:    p.state,
		storeErr: storeErr,
	}
	p.state = wrappedState

	rollbackEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"existing-pod": {
			"main": {
				AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
					PodUid:        "existing-pod",
					ContainerName: "main",
				}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
	}
	rollbackMachineState, err := generateMachineStateFromPodEntries(cpuTopology, rollbackEntries, p.state.GetMachineState())
	require.NoError(t, err)

	err = p.writeRampUpReclaimPoolTarget(context.Background(), &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:        "snb-pod",
			PodNamespace:  "default",
			PodName:       "snb-pod",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2, 3),
		},
	}, true, rollbackEntries, rollbackMachineState)
	require.ErrorIs(t, err, storeErr)
	require.ErrorContains(t, err, "eligible CPUSet for hard ramp-up reclaim must not be empty")
	require.ErrorContains(t, err, "store rollback checkpoint failed")
	require.Equal(t, 1, wrappedState.storeCalls)
	require.Equal(t, []bool{false}, wrappedState.setPodEntryPersists)
	require.Equal(t, []bool{false}, wrappedState.setMachinePersists)
}

func TestWriteRampUpReclaimPoolTargetSuccessReturnsStoreError(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestWriteRampUpReclaimPoolTargetSuccessReturnsStoreError")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	storeErr := errors.New("store success checkpoint failed")
	wrappedState := &rollbackStoreState{
		State:    p.state,
		storeErr: storeErr,
	}
	p.state = wrappedState

	rollbackEntries := p.state.GetPodEntries()
	rollbackMachineState := p.state.GetMachineState()
	err = p.writeRampUpReclaimPoolTarget(context.Background(), &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:        "snb-pod",
			PodNamespace:  "default",
			PodName:       "snb-pod",
			ContainerName: "main",
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(0, 1),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		},
	}, true, p.state.GetPodEntries(), p.state.GetMachineState())
	require.ErrorIs(t, err, storeErr)
	require.ErrorContains(t, err, "store ramp-up reclaim pool target checkpoint failed")
	require.Equal(t, 1, wrappedState.storeCalls)
	require.Equal(t, []bool{false, false}, wrappedState.setPodEntryPersists)
	require.Equal(t, []bool{false, false}, wrappedState.setMachinePersists)
	require.Equal(t, rollbackEntries, p.state.GetPodEntries())
	require.True(t, reflect.DeepEqual(rollbackMachineState, p.state.GetMachineState()))
}

func TestGetResourcesAllocationKeepsRampUpStateWhenHardPartitionEnabled(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetResourcesAllocationKeepsRampUpStateWhenHardPartitionEnabled")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.transitionPeriod = time.Millisecond

	allocationResult := machine.NewCPUSet(0, 1, 2, 3)
	p.state.SetAllocationInfo("hard-ramp-up-pod", "main", &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(&pluginapi.ResourceRequest{
			PodUid:         "hard-ramp-up-pod",
			PodNamespace:   "default",
			PodName:        "hard-ramp-up-pod",
			ContainerName:  "main",
			ContainerType:  pluginapi.ContainerType_MAIN,
			ContainerIndex: 0,
		}, commonstate.EmptyOwnerPoolName, apiconsts.PodAnnotationQoSLevelSharedCores),
		RampUp:                   true,
		InitTimestamp:            time.Now().Add(-time.Hour).Format(qrmutil.QRMTimeFormat),
		AllocationResult:         allocationResult,
		OriginalAllocationResult: allocationResult.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: allocationResult,
		},
	}, false)
	reclaimCPUs := machine.NewCPUSet(0)
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:         reclaimCPUs,
		OriginalAllocationResult: reclaimCPUs.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: reclaimCPUs,
		},
	}, false)

	_, err = p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	allocationInfo := p.state.GetAllocationInfo("hard-ramp-up-pod", "main")
	require.NotNil(t, allocationInfo)
	require.True(t, allocationInfo.RampUp)

	reclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimInfo)
	require.True(t, reclaimInfo.AllocationResult.Equals(reclaimCPUs), "reclaim=%s", reclaimInfo.AllocationResult.String())
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
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false)
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
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false)
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

		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false)
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
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), nil, req.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "nil req")

		// Nil hint
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, nil, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is nil")

		// Empty hint
		emptyHintReq := createReq(2, false)
		emptyHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{}}
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, emptyHintReq.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "hint is empty")

		// Hint with multiple nodes
		multiNodeHintReq := createReq(2, false)
		multiNodeHintReq.Hint = &pluginapi.TopologyHint{Nodes: []uint64{0, 1}}
		_, err = policy.allocateSharedNumaBindingCPUs(context.Background(), req, multiNodeHintReq.Hint, false)
		as.Error(err)
		as.Contains(err.Error(), "larger than 1 NUMA")
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
		reclaimCPUs                                machine.CPUSet
		disableDedicatedCoresOverlapReclaimedCores bool
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
			name: "dedicated isolation rejects non-exclusive fallback to reclaim cpus",
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
				reclaimCPUs: machine.NewCPUSet(2, 3),
				disableDedicatedCoresOverlapReclaimedCores: true,
			},
			want:    machine.NewCPUSet(),
			wantErr: true,
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
			p.reservedCPUs = machine.NewCPUSet()
			t.Logf("Reserved: %s", p.reservedCPUs.String())

			// Explicitly control the reclaim pool so allocation's reclaim-avoidance
			// only sees the cpus declared by this case (the default init would
			// otherwise seed an unrelated reclaim pool).
			p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: tt.args.reclaimCPUs.Clone(),
			}, false)
			p.state.SetDisableDedicatedCoresOverlapReclaimedCores(
				tt.args.disableDedicatedCoresOverlapReclaimedCores, false,
			)

			got, hardReclaim, err := p.allocateNumaBindingCPUs(tt.args.numCPUs, tt.args.hint, tt.args.machineState, tt.args.reqAnnotations, true)
			if (err != nil) != tt.wantErr {
				t.Errorf("allocateNumaBindingCPUs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !hardReclaim.IsEmpty() {
				t.Errorf("allocateNumaBindingCPUs() hardReclaim = %v, want empty in legacy mode", hardReclaim)
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
			got, err := p.takeByTopologyPreferring(tt.available, tt.preferred, tt.numCPUs)
			require.NoError(t, err)
			require.True(t, got.IsSubsetOf(tt.available), "got=%s available=%s", got.String(), tt.available.String())
			require.Equal(t, tt.numCPUs, got.Size())
			require.True(t, got.Equals(tt.want), "got=%s want=%s", got.String(), tt.want.String())
		})
	}
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
	p.reservedCPUs = machine.NewCPUSet()
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
	got, hardReclaim, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationResourcePackageKey:           "pkg1",
	}, true)
	require.NoError(t, err)
	require.True(t, hardReclaim.IsEmpty())
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
	p.reservedCPUs = machine.NewCPUSet()
	p.state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
	}, false)

	available := machine.NewCPUSet(0, 1, 2, 3)
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: available},
	}
	got, hardReclaim, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, true)
	require.NoError(t, err)
	require.True(t, hardReclaim.IsEmpty())
	require.Equal(t, 2, got.Size())
	require.True(t, got.IsSubsetOf(available), "got=%s available=%s", got.String(), available.String())
}

func TestDynamicPolicy_allocateNumaBindingCPUs_hardPartitionAvoidsReclaim(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs_hard_partition")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	available := machine.NewCPUSet(0, 1, 2, 3)
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: available},
	}
	got, hardReclaim, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, true)
	require.NoError(t, err)
	require.False(t, hardReclaim.IsEmpty())
	require.True(t, hardReclaim.IsSubsetOf(available), "hard=%s available=%s", hardReclaim.String(), available.String())
	require.True(t, got.IsSubsetOf(available), "got=%s available=%s", got.String(), available.String())
	require.True(t, got.Intersection(hardReclaim).IsEmpty(), "got=%s hard=%s", got.String(), hardReclaim.String())
	require.Equal(t, 2, got.Size())
}

func TestDynamicPolicy_allocateNumaBindingCPUs_hardPartitionUsesPodReclaimDecision(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs_hard_partition_pod_decision")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.5

	available := machine.NewCPUSet(0, 1, 2, 3)
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: available},
	}
	got, hardReclaim, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	}, false)
	require.NoError(t, err)
	require.True(t, hardReclaim.Equals(machine.NewCPUSet(0)), "hard=%s", hardReclaim.String())
	require.Equal(t, 2, got.Size())
	require.True(t, got.Intersection(hardReclaim).IsEmpty(), "got=%s hard=%s", got.String(), hardReclaim.String())
}

func TestDynamicPolicy_allocateNumaBindingCPUs_hardPartitionSplitsExclusiveDNB(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateNumaBindingCPUs_hard_partition_exclusive")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	available := machine.NewCPUSet(0, 1, 2, 3)
	machineState := state.NUMANodeMap{
		0: &state.NUMANodeState{DefaultCPUSet: available},
	}
	got, hardReclaim, err := p.allocateNumaBindingCPUs(2, &pluginapi.TopologyHint{Nodes: []uint64{0}}, machineState, map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
	}, true)
	require.NoError(t, err)
	require.False(t, hardReclaim.IsEmpty())
	require.False(t, got.IsEmpty())
	require.True(t, got.Intersection(hardReclaim).IsEmpty(), "got=%s hard=%s", got.String(), hardReclaim.String())
	require.True(t, got.Union(hardReclaim).Equals(available), "got=%s hard=%s available=%s", got.String(), hardReclaim.String(), available.String())
	require.GreaterOrEqual(t, got.Size(), 2)
}

func TestDynamicPolicy_dedicatedCoresWithNUMABindingAllocationHandler_DisabledHardPartitionSkipsReclaimLookup(t *testing.T) {
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_dedicatedCoresWithNUMABindingAllocationHandler_disabled_hard_partition")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedCPUs = machine.NewCPUSet()
	p.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.metaServer = nil

	req := &pluginapi.ResourceRequest{
		PodUid:         "pod-disabled-hard-partition",
		PodNamespace:   "default",
		PodName:        "pod-disabled-hard-partition",
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
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			apiconsts.PodAnnotationCPUEnhancementNumaNumber:     "1",
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}},
	}

	resp, err := p.dedicatedCoresWithNUMABindingAllocationHandler(context.Background(), req, false)

	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestDynamicPolicy_selectRampUpHardReclaimFromEligibleUsesPodReclaimDecision(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_selectRampUpHardReclaimFromEligible_pod_decision")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.reservedReclaimedCPUSet = machine.NewCPUSet(0)
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.5

	hardReclaim, err := p.selectRampUpHardReclaimFromEligible(machine.NewCPUSet(0, 1, 2, 3), false, false)
	require.NoError(t, err)
	require.True(t, hardReclaim.Equals(machine.NewCPUSet(0)), "hard=%s", hardReclaim.String())
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
			p.reservedCPUs = machine.NewCPUSet()

			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim

			gotLeft, err := p.generateNUMABindingPoolsCPUSetInPlace(tt.args.poolsCPUSet, tt.args.poolsQuantityMap, tt.args.availableCPUs)
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
			wantReclaim:   machine.NewCPUSet(2, 3, 4, 5),
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

			p.reservedCPUs = machine.NewCPUSet()
			p.reservedReclaimedCPUsSize = 0
			p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = tt.enableReclaim
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

	p.reservedCPUs = machine.NewCPUSet()
	p.reservedReclaimedCPUSet = machine.NewCPUSet()
	p.reservedReclaimedCPUsSize = 0
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(10, 11, 12, 13),
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
	)
	require.NoError(t, err)

	historicalReclaim := machine.NewCPUSet(10, 11, 12, 13)
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

	p.reservedCPUs = machine.NewCPUSet(0, 24)
	p.reservedReclaimedCPUSet = machine.NewCPUSet(1, 2, 25, 26)
	p.reservedReclaimedCPUsSize = 4
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, true)
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
	)
	require.NoError(t, err)

	wantReclaim := machine.NewCPUSet(2, 3, 4, 5, 6, 7, 25, 26, 27, 28, 29, 30, 31, 49, 50, 51, 52, 53, 54, 55, 73, 74, 75, 76, 77, 78, 79)
	require.True(t, poolsCPUSet[commonstate.PoolNameReclaim].Equals(wantReclaim),
		"reclaim should preserve existing advisor reclaim minus seed allocation, got %s want %s",
		poolsCPUSet[commonstate.PoolNameReclaim].String(), wantReclaim.String())
	require.True(t, poolsCPUSet["seedpool-stable-0"].Equals(machine.NewCPUSet(1)),
		"seed pool should take the first available cpu, got %s", poolsCPUSet["seedpool-stable-0"].String())
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
	p.reservedCPUs = machine.NewCPUSet()

	// Enable Reclaim
	p.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
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
			p.reservedCPUs = machine.NewCPUSet()

			gotPools, gotIsolated, err := p.groupAndAllocatePools(tt.args.poolsQuantityMap, tt.args.isolatedQuantityMap, tt.args.availableCPUs, tt.args.rpPinnedCPUSet, tt.args.reclaimOverlapShareRatio)
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
