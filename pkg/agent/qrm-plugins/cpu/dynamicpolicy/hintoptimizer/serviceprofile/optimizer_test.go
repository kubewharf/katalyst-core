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

package serviceprofile

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	agentpod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func Test_spreadNormalizeScore(t *testing.T) {
	t.Parallel()
	type args struct {
		scores []NUMAScore
	}
	tests := []struct {
		name    string
		args    args
		want    []NUMAScore
		wantErr bool
	}{
		{
			name: "test1",
			args: args{
				scores: []NUMAScore{
					{
						Score: 10,
						ID:    0,
					},
					{
						Score: 200,
						ID:    1,
					},
					{
						Score: 400,
						ID:    2,
					},
				},
			},
			want: []NUMAScore{
				{
					Score: 100,
					ID:    0,
				},
				{
					Score: 52.5,
					ID:    1,
				},
				{
					Score: 2.5,
					ID:    2,
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := spreadNormalizeScore(tt.args.scores); (err != nil) != tt.wantErr {
				t.Errorf("spreadNormalizeScore() error = %v, wantErr %v", err, tt.wantErr)
			}
			assert.Equal(t, tt.want, tt.args.scores)
		})
	}
}

func TestNewServiceProfileHintOptimizer(t *testing.T) {
	t.Parallel()
	type args struct {
		metaServer *metaserver.MetaServer
		conf       qrm.ServiceProfileHintOptimizerConfig
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "test1",
			args: args{
				metaServer: &metaserver.MetaServer{},
				conf:       qrm.ServiceProfileHintOptimizerConfig{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := NewServiceProfileHintOptimizer(metrics.DummyMetrics{}, tt.args.metaServer, tt.args.conf)
			assert.NotNil(t, o)
		})
	}
}

func Test_serviceProfileHintOptimizer_OptimizeHints(t *testing.T) {
	t.Parallel()
	type fields struct {
		profilingManager spd.ServiceProfilingManager
		podFetcher       agentpod.PodFetcher
		resourceWeight   map[v1.ResourceName]float64
		resourceFuncs    map[v1.ResourceName]resourceFuncs
	}
	type args struct {
		req          *pluginapi.ResourceRequest
		hints        *pluginapi.ListOfTopologyHints
		machineState state.NUMANodeMap
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.ListOfTopologyHints
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "test1",
			fields: fields{
				profilingManager: spd.NewServiceProfilingManager(spd.DummySPDFetcher{
					SPD: &workloadapi.ServiceProfileDescriptor{
						Status: workloadapi.ServiceProfileDescriptorStatus{
							AggMetrics: []workloadapi.AggPodMetrics{
								{
									Aggregator: workloadapi.Avg,
									Items: []v1beta1.PodMetrics{
										{
											Timestamp: metav1.NewTime(time.Date(1970, 0, 0, 0, 0, 0, 0, time.UTC)),
											Window:    metav1.Duration{Duration: time.Hour},
											Containers: []v1beta1.ContainerMetrics{
												{
													Name: "container-1",
													Usage: map[v1.ResourceName]resource.Quantity{
														v1.ResourceCPU: resource.MustParse("10"),
													},
												},
												{
													Name: "container-2",
													Usage: map[v1.ResourceName]resource.Quantity{
														v1.ResourceCPU: resource.MustParse("10"),
													},
												},
											},
										},
										{
											Timestamp: metav1.NewTime(time.Date(1970, 0, 0, 1, 0, 0, 0, time.UTC)),
											Window:    metav1.Duration{Duration: time.Hour},
											Containers: []v1beta1.ContainerMetrics{
												{
													Name: "container-1",
													Usage: map[v1.ResourceName]resource.Quantity{
														v1.ResourceCPU: resource.MustParse("20"),
													},
												},
												{
													Name: "container-2",
													Usage: map[v1.ResourceName]resource.Quantity{
														v1.ResourceCPU: resource.MustParse("20"),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}),
				resourceFuncs: map[v1.ResourceName]resourceFuncs{
					v1.ResourceCPU: {
						defaultRequestFunc: func(podUID string) (resource.Quantity, error) {
							return resource.MustParse("10"), nil
						},
						normalizeScoreFunc: spreadNormalizeScore,
					},
					v1.ResourceMemory: {
						defaultRequestFunc: func(podUID string) (resource.Quantity, error) {
							return resource.MustParse("10Gi"), nil
						},
						normalizeScoreFunc: spreadNormalizeScore,
					},
				},
				resourceWeight: map[v1.ResourceName]float64{
					v1.ResourceCPU:    1.,
					v1.ResourceMemory: 1.,
				},
			},
			args: args{
				req: &pluginapi.ResourceRequest{},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes: []uint64{0},
						},
						{
							Nodes: []uint64{1},
						},
						{
							Nodes: []uint64{2},
						},
					},
				},
				machineState: state.NUMANodeMap{
					0: &state.NUMANodeState{
						PodEntries: state.PodEntries{
							"pod-1": {
								"container-1": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-1",
										ContainerType: pluginapi.ContainerType_MAIN.String(),
										ContainerName: "container-1",
										QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
										Annotations: map[string]string{
											apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
											apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										},
									},
								},
							},
						},
					},
					1: &state.NUMANodeState{
						PodEntries: state.PodEntries{
							"pod-2": {
								"container-2": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-2",
										ContainerType: pluginapi.ContainerType_MAIN.String(),
										ContainerName: "container-2",
										QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
										Annotations: map[string]string{
											apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
											apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										},
									},
								},
							},
							"pod-3": {
								"container-2": &state.AllocationInfo{
									AllocationMeta: commonstate.AllocationMeta{
										PodUid:        "pod-3",
										ContainerType: pluginapi.ContainerType_MAIN.String(),
										ContainerName: "container-3",
										QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
										Annotations: map[string]string{
											apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
											apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
										},
									},
								},
							},
						},
					},
					2: &state.NUMANodeState{},
				},
			},
			want: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{
						Nodes:     []uint64{2},
						Preferred: true,
					},
					{
						Nodes: []uint64{0},
					},
					{
						Nodes: []uint64{1},
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &serviceProfileHintOptimizer{
				emitter:               metrics.DummyMetrics{},
				profilingManager:      tt.fields.profilingManager,
				podFetcher:            tt.fields.podFetcher,
				resourceWeight:        tt.fields.resourceWeight,
				resourceFuncs:         tt.fields.resourceFuncs,
				defaultQuantitiesSize: 2,
			}
			tt.wantErr(t, p.OptimizeHints(tt.args.req, tt.args.hints, tt.args.machineState), fmt.Sprintf("OptimizeHints(%v, %v, %v)", tt.args.req, tt.args.hints, tt.args.machineState))
			assert.Equal(t, tt.want, tt.args.hints)
		})
	}
}

func Test_serviceProfileHintOptimizer_getCPURequest(t *testing.T) {
	t.Parallel()
	type fields struct {
		podFetcher agentpod.PodFetcher
	}
	type args struct {
		podUID string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    resource.Quantity
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "test1",
			fields: fields{
				podFetcher: &agentpod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								UID: "pod-1",
							},
							Spec: v1.PodSpec{
								Containers: []v1.Container{
									{
										Name: "container-1",
										Resources: v1.ResourceRequirements{
											Requests: v1.ResourceList{
												v1.ResourceCPU: resource.MustParse("10"),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			args: args{
				podUID: "pod-1",
			},
			want:    resource.MustParse("10"),
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &serviceProfileHintOptimizer{
				podFetcher: tt.fields.podFetcher,
			}
			got, err := p.getCPURequest(tt.args.podUID)
			if !tt.wantErr(t, err, fmt.Sprintf("getCPURequest(%v)", tt.args.podUID)) {
				return
			}
			assert.Equalf(t, tt.want, got, "getCPURequest(%v)", tt.args.podUID)
		})
	}
}

func Test_serviceProfileHintOptimizer_getMemoryRequest(t *testing.T) {
	t.Parallel()
	type fields struct {
		podFetcher agentpod.PodFetcher
	}
	type args struct {
		podUID string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    resource.Quantity
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "test1",
			fields: fields{
				podFetcher: &agentpod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								UID: "pod-1",
							},
							Spec: v1.PodSpec{
								Containers: []v1.Container{
									{
										Name: "container-1",
										Resources: v1.ResourceRequirements{
											Requests: v1.ResourceList{
												v1.ResourceMemory: resource.MustParse("10Gi"),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			args: args{
				podUID: "pod-1",
			},
			want:    resource.MustParse("10Gi"),
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &serviceProfileHintOptimizer{
				podFetcher: tt.fields.podFetcher,
			}
			got, err := p.getMemoryRequest(tt.args.podUID)
			if !tt.wantErr(t, err, fmt.Sprintf("getMemoryRequest(%v)", tt.args.podUID)) {
				return
			}
			assert.Equalf(t, tt.want, got, "getMemoryRequest(%v)", tt.args.podUID)
		})
	}
}
