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
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
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

	type fields struct {
		config *qrm.ResctrlConfig
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
						"katalyst.kubewharf.io/cpu_enhancement": `{"cpuset_pool":"batch"}`,
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
					MonGroupEnabledClosIDs: []string{"dedicated", "share-50"},
				},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_enhancement": `{"cpuset_pool":"batch"}`,
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
				config: &qrm.ResctrlConfig{
					EnableResctrlHint: true,
					CPUSetPoolToSharedSubgroup: map[string]int{
						"batch": 30,
					},
					MonGroupEnabledClosIDs: []string{"dedicated", "share-30"},
				},
			},
			args: args{
				qosLevel: "shared_cores",
				req: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"katalyst.kubewharf.io/cpu_enhancement": `{"cpuset_pool":"batch"}`,
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
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResctrlHinter(tt.fields.config)
			got := r.HintResp(tt.args.qosLevel, tt.args.req, tt.args.resp)
			assert.Equalf(t, tt.want, got, "HintResp(%v, %v, %v)", tt.args.qosLevel, tt.args.req, tt.args.resp)
		})
	}
}
