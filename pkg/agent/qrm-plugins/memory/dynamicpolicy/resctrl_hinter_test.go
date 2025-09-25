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
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/atomic"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

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
		config     *qrm.ResctrlConfig
		resctrl    *fsResctrl
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint:          false,
					CPUSetPoolToSharedSubgroup: map[string]int{"batch": 30},
					DefaultSharedSubgroup:      50,
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					MonGroupEnabledClosIDs: []string{"dedicated", "shared-50"},
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
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "shared-30",
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					MonGroupEnabledClosIDs: []string{"dedicated", "shared-30"},
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
			want: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								"test-key":                             "test-value",
								"rdt.resources.beta.kubernetes.io/pod": "shared-30",
							},
						},
					},
				},
			},
		},
		{
			name: "default shared-50, and mon_groups not over limit",
			fields: fields{
				config: &qrm.ResctrlConfig{
					EnableResctrlHint:     true,
					DefaultSharedSubgroup: 50,
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint:     true,
					DefaultSharedSubgroup: 50,
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					MonGroupEnabledClosIDs: []string{"dedicated", "shared-50"},
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
			r := newResctrlHinter(tt.fields.config, metrics.DummyMetrics{})

			if tt.fields.resctrl != nil {
				root := t.TempDir()
				if tt.fields.resctrl.numRmids != "" {
					os.MkdirAll(path.Join(root, "info/L3_MON"), 0o755)
					err := os.WriteFile(path.Join(root, "info/L3_MON/num_rmids"), []byte(tt.fields.resctrl.numRmids), 0o644)
					assert.NoError(t, err)
				}
				for _, dir := range tt.fields.resctrl.dirs {
					err := os.MkdirAll(path.Join(root, dir), 0o755)
					assert.NoError(t, err)
				}
				hinter := r.(*resctrlHinter)
				hinter.root = root
				hinter.monGroupsMaxCount = atomic.NewInt64(hinter.getMonGroupsMaxCount())
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
