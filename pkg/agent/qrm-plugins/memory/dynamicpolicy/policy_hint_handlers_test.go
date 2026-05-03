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
	"os"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDynamicPolicy_numaBindingHintHandler(t *testing.T) {
	t.Parallel()

	type args struct {
		req *pluginapi.ResourceRequest
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		want    *pluginapi.ResourceHintsResponse
	}{
		{
			name: "test for sidecar container",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_SIDECAR,
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_SIDECAR,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): nil,
				},
			},
		},
		{
			name: "test for dedicated cores with numa binding without numa exclusive main container",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						string(v1.ResourceMemory): 1024 * 1024 * 1024,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
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
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
		},
		{
			name: "test for dedicated cores with numa binding with distribute evenly across numa",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						string(v1.ResourceMemory): 160 * 1024 * 1024 * 1024, // 160Gi
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
		},
		{
			name: "shared cores with numa binding and distribute evenly across numa will return error",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						string(v1.ResourceMemory): 160 * 1024 * 1024 * 1024, // 160Gi
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					},
				},
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "test for hugepages-2Mi dedicated cores with numa binding without numa exclusive main container",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						"hugepages-2Mi": 2 * 1024 * 1024 * 1024, // 2Gi
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					"hugepages-2Mi": {
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
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
		},
		{
			name: "test for hugepages-2Mi dedicated cores with numa binding with numa exclusive main container",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						"hugepages-2Mi": 4 * 1024 * 1024 * 1024,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					"hugepages-2Mi": {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
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
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                    consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
				},
			},
		},
		{
			name: "test for hugepages-2Mi dedicated cores without numa exclusive with distribute evenly across numa",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						"hugepages-2Mi": 4 * 1024 * 1024 * 1024, // 6Gi can be split into 2 or 4 numa nodes
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					"hugepages-2Mi": {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
		},
		{
			name: "not enough memory for hugepages-2Mi returns error",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						"hugepages-2Mi": 1000 * 1024 * 1024 * 1024, // 1000Gi
					},
				},
			},
			wantErr: true,
		},
		{
			name: "test for hugepages-1Gi dedicated cores without numa exclusive with distribute evenly across numa",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						"hugepages-1Gi": 16 * 1024 * 1024 * 1024, // 16Gi can fit onto 2 or 4 NUMA nodes
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					"hugepages-1Gi": {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
				},
			},
		},
		{
			name: "distribute evenly across numa and numa exclusive not supported",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						string(v1.ResourceMemory): 1024 * 1024 * 1024,
						"hugepages-2Mi":           2 * 1024 * 1024 * 1024,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                              consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding:             consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive:           consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
						consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNuma: consts.PodAnnotationCPUEnhancementDistributeEvenlyAcrossNumaEnable,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "get topology hints for both memory and hugepages-2Mi",
			args: args{
				req: &pluginapi.ResourceRequest{
					PodUid:        "pod1_uid",
					PodName:       "pod1",
					ContainerName: "container1",
					ContainerType: pluginapi.ContainerType_MAIN,
					ResourceName:  string(v1.ResourceMemory),
					ResourceRequests: map[string]float64{
						string(v1.ResourceMemory): 1024 * 1024 * 1024,
						"hugepages-2Mi":           2 * 1024 * 1024 * 1024,
					},
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
			},
			wantErr: false,
			want: &pluginapi.ResourceHintsResponse{
				PodUid:        "pod1_uid",
				PodName:       "pod1",
				ContainerName: "container1",
				ContainerType: pluginapi.ContainerType_MAIN,
				ResourceName:  string(v1.ResourceMemory),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(v1.ResourceMemory): {
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
					"hugepages-2Mi": {
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
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir, err := os.MkdirTemp("", "checkpoint-TestNumaBindingHintHandler")
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			assert.NoError(t, err)
			machineInfo := &info.MachineInfo{
				Topology: []info.Node{
					{
						Id:     0,
						Memory: 100 * 1024 * 1024 * 1024, // 100 GB
						HugePages: []info.HugePagesInfo{
							{
								PageSize: 2 * 1024, // 2Mi
								NumPages: 1024,
							},
							{
								PageSize: 1 * 1024 * 1024, // 1Gi
								NumPages: 8,
							},
						},
					},
					{
						Id:     1,
						Memory: 100 * 1024 * 1024 * 1024,
						HugePages: []info.HugePagesInfo{
							{
								PageSize: 2 * 1024, // 2Mi
								NumPages: 1024,
							},
							{
								PageSize: 1 * 1024 * 1024, // 1Gi
								NumPages: 8,
							},
						},
					},
					{
						Id:     2,
						Memory: 100 * 1024 * 1024 * 1024,
						HugePages: []info.HugePagesInfo{
							{
								PageSize: 2 * 1024, // 2Mi
								NumPages: 1024,
							},
							{
								PageSize: 1 * 1024 * 1024, // 1Gi
								NumPages: 8,
							},
						},
					},
					{
						Id:     3,
						Memory: 100 * 1024 * 1024 * 1024,
						HugePages: []info.HugePagesInfo{
							{
								PageSize: 2 * 1024, // 2Mi
								NumPages: 1024,
							},
							{
								PageSize: 1 * 1024 * 1024, // 1Gi
								NumPages: 8,
							},
						},
					},
				},
			}

			policy, err := getTestDynamicPolicyWithExtraResourcesWithInitialization(cpuTopology, machineInfo, tmpDir)
			assert.NoError(t, err)

			got, err := policy.numaBindingHintHandler(context.Background(), tt.args.req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
