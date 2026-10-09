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
	"errors"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type mockResctrlManager struct {
	count          int64
	createCalls    int
	createErr      error
	reconcileCalls int
	reconcileState resctrl.ClosReconcileState
}

func (m *mockResctrlManager) Run(stopCh <-chan struct{}) {}
func (m *mockResctrlManager) Create(podUID, closID string, createMonGroup bool) error {
	m.createCalls++
	return m.createErr
}

func (m *mockResctrlManager) ReconcileClos(state resctrl.ClosReconcileState) error {
	m.reconcileCalls++
	m.reconcileState = state
	return nil
}
func (m *mockResctrlManager) GetMonGroupsCount() (int64, error) { return m.count, nil }

func TestResctrlHinterReconcileClosUsesDynamicDisableRDT(t *testing.T) {
	t.Parallel()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().RDTConfig.DisableRDT = true
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		dynamicConf: dynamicConf,
		manager:     manager,
	}

	entries := state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"pod-a": state.ContainerEntries{
				"main": &state.AllocationInfo{},
			},
		},
	}
	hinter.reconcileClos(entries)

	if !manager.reconcileState.DisableRDT {
		t.Fatal("ReconcileClos DisableRDT = false, want true")
	}
	assert.True(t, manager.reconcileState.ActivePodUIDs.Has("pod-a"))
}

func TestResctrlHinterDisableRDTSynchronouslyGatesHintAndAllocate(t *testing.T) {
	t.Parallel()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().RDTConfig.DisableRDT = true
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlHint: true,
			EnabledQoS:        []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		},
		enabledQoS:           sets.NewString(apiconsts.PodAnnotationQoSLevelSharedCores),
		closidEnablingGroups: sets.NewString(),
		monGroupsMaxCount:    atomic.NewInt64(0),
		dynamicConf:          dynamicConf,
		manager:              manager,
	}
	meta := commonstate.AllocationMeta{
		PodUid:        "pod-a",
		OwnerPoolName: "share",
		QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
	}

	for _, allocate := range []bool{false, true} {
		allocation := &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
		}
		hinter.hintResourceAllocation(meta, allocation, allocate)
		require.Equal(t, &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceMemory): {
					Annotations: map[string]string{
						util.AnnotationRdtClosID: "",
					},
				},
			},
		}, allocation)
	}
	require.Zero(t, manager.createCalls)
}

func TestResctrlHinterEnableResctrlHintTakesPrecedenceOverDisableRDT(t *testing.T) {
	t.Parallel()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().RDTConfig.DisableRDT = true
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlHint: false,
			EnabledQoS:        []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		},
		enabledQoS:           sets.NewString(apiconsts.PodAnnotationQoSLevelSharedCores),
		closidEnablingGroups: sets.NewString(),
		monGroupsMaxCount:    atomic.NewInt64(0),
		dynamicConf:          dynamicConf,
		manager:              manager,
	}
	allocation := &pluginapi.ResourceAllocation{
		ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
	}

	hinter.HintResourceAllocation(commonstate.AllocationMeta{
		PodUid:        "pod-a",
		OwnerPoolName: "share",
		QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
	}, allocation)

	require.Empty(t, allocation.ResourceAllocation)
	require.Zero(t, manager.createCalls)
}

func TestResctrlHinterDisableRDTKeepsEnabledQoSBoundary(t *testing.T) {
	t.Parallel()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().RDTConfig.DisableRDT = true
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlHint: true,
			EnabledQoS:        []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		},
		enabledQoS:           sets.NewString(apiconsts.PodAnnotationQoSLevelSharedCores),
		closidEnablingGroups: sets.NewString(),
		monGroupsMaxCount:    atomic.NewInt64(0),
		dynamicConf:          dynamicConf,
		manager:              manager,
	}
	allocation := &pluginapi.ResourceAllocation{
		ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
	}

	hinter.HintResourceAllocation(commonstate.AllocationMeta{
		PodUid:        "pod-a",
		OwnerPoolName: "dedicated",
		QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
	}, allocation)

	require.Empty(t, allocation.ResourceAllocation)
	require.Zero(t, manager.createCalls)
}

func TestResctrlHinterAllocateDoesNotInjectClosWhenCreateFails(t *testing.T) {
	t.Parallel()
	manager := &mockResctrlManager{createErr: errors.New("disable transition pending")}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlHint: true,
			EnabledQoS:        []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		},
		enabledQoS:           sets.NewString(apiconsts.PodAnnotationQoSLevelSharedCores),
		closidEnablingGroups: sets.NewString(),
		monGroupsMaxCount:    atomic.NewInt64(0),
		manager:              manager,
	}
	allocation := &pluginapi.ResourceAllocation{
		ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
	}

	hinter.Allocate(commonstate.AllocationMeta{
		PodUid:        "pod-a",
		OwnerPoolName: "share",
		QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
	}, allocation)

	require.Equal(t, 1, manager.createCalls)
	require.Empty(t, allocation.ResourceAllocation)
}

func TestResctrlHinterHintDoesNotInjectClosWhenConfirmationFails(t *testing.T) {
	t.Parallel()
	manager := &mockResctrlManager{createErr: resctrl.ErrRDTUnavailable}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			EnableResctrlHint: true,
			EnabledQoS:        []string{apiconsts.PodAnnotationQoSLevelSharedCores},
		},
		enabledQoS:           sets.NewString(apiconsts.PodAnnotationQoSLevelSharedCores),
		closidEnablingGroups: sets.NewString(),
		monGroupsMaxCount:    atomic.NewInt64(0),
		manager:              manager,
	}
	allocation := &pluginapi.ResourceAllocation{
		ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{},
	}

	hinter.HintResourceAllocation(commonstate.AllocationMeta{
		PodUid:        "pod-a",
		OwnerPoolName: "share",
		QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
	}, allocation)

	require.Equal(t, 1, manager.createCalls)
	require.Empty(t, allocation.ResourceAllocation)
}

func TestResctrlHinterReconcileClosBuildsExpectedClosIDsFromState(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	machineInfo, err := machine.GenerateDummyMachineInfo(1, 8)
	require.NoError(t, err)
	memoryState, err := state.NewMemoryPluginState(topology, machineInfo, nil, nil, nil)
	require.NoError(t, err)
	memoryState.SetPodResourceEntries(state.PodResourceEntries{
		v1.ResourceMemory: {
			"pod-a": {
				"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "main",
					OwnerPoolName: "batch",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				}},
			},
			"pod-b": {
				"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-b",
					ContainerName: "main",
					OwnerPoolName: "dedicated",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				}},
			},
			"pod-c": {
				"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-c",
					ContainerName: "main",
					OwnerPoolName: "reclaim",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				}},
			},
		},
	})

	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config: &qrmresctrl.ResctrlConfig{
			CPUSetPoolToSharedSubgroup: map[string]int{"batch": 3},
		},
		manager: manager,
		state:   memoryState,
	}

	hinter.reconcileClos(memoryState.GetPodResourceEntries())

	assert.True(t, manager.reconcileState.ExpectedClosIDs.Has("share-03"))
	assert.True(t, manager.reconcileState.ExpectedClosIDs.Has("dedicated"))
	assert.True(t, manager.reconcileState.ExpectedClosIDs.Has("reclaim"))
}

func TestResctrlHinterReconcileClosReusesSingleSnapshot(t *testing.T) {
	t.Parallel()
	// The expected CLOS set and the active pod UIDs must both derive from the
	// same entries passed in; reconcileClos must not re-fetch pod-resource
	// state (which would risk a divergent snapshot mid-cycle).
	entries := state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"pod-a": state.ContainerEntries{
				"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					OwnerPoolName: "dedicated",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				}},
			},
		},
	}
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config:  &qrmresctrl.ResctrlConfig{},
		manager: manager,
	}

	hinter.reconcileClos(entries)

	require.Equal(t, 1, manager.reconcileCalls)
	require.True(t, manager.reconcileState.ActivePodUIDs.Has("pod-a"))
	require.True(t, manager.reconcileState.ExpectedClosIDs.Has("dedicated"))
}

func TestResctrlHinterSkipsReconcileOnMalformedAllocation(t *testing.T) {
	t.Parallel()
	// An unknown QoS with an empty owner pool makes ResolvePoolClosID error.
	// Reconciling with the resulting empty expected set would remove every
	// live CLOS, so the cycle must be skipped instead.
	entries := state.PodResourceEntries{
		v1.ResourceMemory: state.PodEntries{
			"pod-a": state.ContainerEntries{
				"main": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					OwnerPoolName: "",
					QoSLevel:      "totally-unknown-qos",
				}},
			},
		},
	}
	manager := &mockResctrlManager{}
	hinter := &resctrlHinter{
		config:  &qrmresctrl.ResctrlConfig{},
		manager: manager,
	}

	hinter.reconcileClos(entries)

	require.Zero(t, manager.reconcileCalls,
		"ReconcileClos must be skipped, not run with an empty expected set after a malformed allocation")
}

func TestResctrlProcessor_HintResp(t *testing.T) {
	t.Parallel()

	genRespTest := func() *pluginapi.ResourceAllocationResponse {
		return &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					"memory": {
						Annotations: map[string]string{
							"test-key": "test-value",
						},
					},
				},
			},
		}
	}

	type fsResctrl struct {
		numRmids string
		dirs     []string
	}
	type fields struct {
		config     *qrmresctrl.ResctrlConfig
		resctrl    *fsResctrl
		manager    *mockResctrlManager
		isAllocate bool
	}
	type args struct {
		qosLevel string
		req      *pluginapi.ResourceRequest
		resp     *pluginapi.ResourceAllocationResponse
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *pluginapi.ResourceAllocationResponse
	}{
		{
			name: "default nil no change",
			fields: fields{
				config: nil,
			},
			args: args{
				qosLevel: "shared_cores",
				req:      &pluginapi.ResourceRequest{},
				resp:     genRespTest(),
			},
			want: genRespTest(),
		},
		{
			name: "disabled opt no change",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint:          false,
					CPUSetPoolToSharedSubgroup: map[string]int{"batch": 30},
					EnabledQoS:                 []string{"shared_cores"},
				},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"cpuset_pool": "batch",
					},
				},
				resp: genRespTest(),
			},
			want: genRespTest(),
		},
		{
			name: "batch is share-30 if specified so, and no pod mon-group",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					EnabledQoS:             []string{"shared_cores"},
					MonGroupEnabledClosIDs: []string{"dedicated", "share-50"},
				},
				manager: &mockResctrlManager{},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"cpuset_pool": "batch",
					},
				},
				resp: genRespTest(),
			},
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "share-30",
								"rdt.resources.beta.kubernetes.io/need-mon-groups": "false",
							},
						},
					},
				},
			},
		},
		{
			name: "batch is share-30, and default yes pod mon-group",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					EnabledQoS:             []string{"shared_cores"},
					MonGroupEnabledClosIDs: []string{"dedicated", "share-30"},
				},
				manager: &mockResctrlManager{},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"cpuset_pool": "batch",
					},
				},
				resp: genRespTest(),
			},
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "share-30",
							},
						},
					},
				},
			},
		},
		{
			name: "default shared-50, and mon_groups not over limit",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint:     true,
					EnabledQoS:            []string{"shared_cores"},
					MonGroupMaxCountRatio: 0.6, // monGroupsMaxCount = 3
				},
				resctrl: &fsResctrl{
					numRmids: "5",
					dirs: []string{
						"info",
						"shared-50/mon_groups/pod1",
						"shared-50/mon_groups/pod2",
					},
				},
				isAllocate: true,
			},
			args: args{
				qosLevel: "shared_cores",
				req:      &pluginapi.ResourceRequest{},
				resp:     genRespTest(),
			},
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "shared-50",
							},
						},
					},
				},
			},
		},
		{
			name: "default shared-50, and mon_groups is over limit",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint:     true,
					EnabledQoS:            []string{"shared_cores"},
					MonGroupMaxCountRatio: 0.6, // monGroupsMaxCount = 3
				},
				resctrl: &fsResctrl{
					numRmids: "5",
					dirs: []string{
						"info",
						"shared-50/mon_groups/pod1",
						"shared-50/mon_groups/pod2",
						"shared-50/mon_groups/pod3",
					},
				},
				isAllocate: true,
			},
			args: args{
				qosLevel: "shared_cores",
				req:      &pluginapi.ResourceRequest{},
				resp:     genRespTest(),
			},
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "shared-50",
								"rdt.resources.beta.kubernetes.io/need-mon-groups": "false",
							},
						},
					},
				},
			},
		},
		{
			name: "resp is nil",
			fields: fields{
				config: &qrmresctrl.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					EnabledQoS:             []string{"shared_cores"},
					MonGroupEnabledClosIDs: []string{"dedicated", "share-50"},
				},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"cpuset_pool": "batch",
					},
				},
				resp: &pluginapi.ResourceAllocationResponse{
					AllocationResult: nil,
				},
			},
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: nil,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResctrlHinter(tt.fields.config, nil, metrics.DummyMetrics{}, nil)

			if tt.fields.manager != nil {
				r.(*resctrlHinter).manager = tt.fields.manager
			}

			if tt.fields.resctrl != nil {
				root := t.TempDir()
				if tt.fields.resctrl.numRmids != "" {
					os.MkdirAll(path.Join(root, "info/L3_MON"), 0o755)
					err := os.WriteFile(path.Join(root, "info/L3_MON/num_rmids"), []byte(tt.fields.resctrl.numRmids), 0o644)
					assert.NoError(t, err)
				}

				monGroupsCount := int64(0)
				for _, dir := range tt.fields.resctrl.dirs {
					// Count mon_groups
					// dirs example: "share-50/mon_groups/pod1"
					if path.Base(path.Dir(dir)) == "mon_groups" {
						monGroupsCount++
					}
				}

				hinter := r.(*resctrlHinter)
				hinter.root = root
				hinter.monGroupsMaxCount = atomic.NewInt64(hinter.getMonGroupsMaxCount())
				hinter.manager = &mockResctrlManager{count: monGroupsCount}
			}

			meta := state.GenerateMemoryContainerAllocationMeta(tt.args.req, tt.args.qosLevel)
			if tt.fields.isAllocate {
				r.Allocate(meta, tt.args.resp.AllocationResult)
			} else {
				r.HintResourceAllocation(meta, tt.args.resp.AllocationResult)
			}
			assert.Equalf(t, tt.want, tt.args.resp, "HintResourceAllocation(%v, %v, %v)", tt.args.qosLevel, tt.args.req, tt.args.resp)
		})
	}
}
