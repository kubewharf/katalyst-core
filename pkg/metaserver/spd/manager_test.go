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

package spd

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func Test_serviceProfilingManager_ServiceBusinessPerformanceLevel(t *testing.T) {
	t.Parallel()

	type fields struct {
		nodeName string
		spd      *workloadapis.ServiceProfileDescriptor
		cnc      *v1alpha1.CustomNodeConfig
	}
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    PerformanceLevel
		wantErr bool
	}{
		{
			name: "service performance is good",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
					Spec: workloadapis.ServiceProfileDescriptorSpec{
						BusinessIndicator: []workloadapis.ServiceBusinessIndicatorSpec{
							{
								Name: workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Indicators: []workloadapis.Indicator{
									{
										IndicatorLevel: workloadapis.IndicatorLevelLowerBound,
										Value:          10,
									},
									{
										IndicatorLevel: workloadapis.IndicatorLevelUpperBound,
										Value:          100,
									},
								},
							},
						},
					},
					Status: workloadapis.ServiceProfileDescriptorStatus{
						BusinessStatus: []workloadapis.ServiceBusinessIndicatorStatus{
							{
								Name:    workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Current: pointer.Float32(40),
							},
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: PerformanceLevelGood,
		},
		{
			name: "service performance large than upper bound",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
					Spec: workloadapis.ServiceProfileDescriptorSpec{
						BusinessIndicator: []workloadapis.ServiceBusinessIndicatorSpec{
							{
								Name: workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Indicators: []workloadapis.Indicator{
									{
										IndicatorLevel: workloadapis.IndicatorLevelUpperBound,
										Value:          50,
									},
								},
							},
						},
					},
					Status: workloadapis.ServiceProfileDescriptorStatus{
						BusinessStatus: []workloadapis.ServiceBusinessIndicatorStatus{
							{
								Name:    workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Current: pointer.Float32(60),
							},
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: PerformanceLevelPoor,
		},
		{
			name: "service performance lower than lower bound",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
					Spec: workloadapis.ServiceProfileDescriptorSpec{
						BusinessIndicator: []workloadapis.ServiceBusinessIndicatorSpec{
							{
								Name: workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Indicators: []workloadapis.Indicator{
									{
										IndicatorLevel: workloadapis.IndicatorLevelLowerBound,
										Value:          20,
									},
								},
							},
						},
					},
					Status: workloadapis.ServiceProfileDescriptorStatus{
						BusinessStatus: []workloadapis.ServiceBusinessIndicatorStatus{
							{
								Name:    workloadapis.ServiceBusinessIndicatorNameRPCLatency,
								Current: pointer.Float32(10),
							},
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: PerformanceLevelPerfect,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "checkpoint-Test_serviceProfilingManager_ServiceBusinessPerformanceLevel")
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			conf := generateTestConfiguration(t, tt.fields.nodeName, dir)
			genericCtx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{
				tt.fields.spd,
				tt.fields.cnc,
			})
			require.NoError(t, err)

			cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration, genericCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
			s, err := NewSPDFetcher(genericCtx.Client, metrics.DummyMetrics{}, cncFetcher, conf)
			require.NoError(t, err)
			require.NotNil(t, s)

			m := NewServiceProfilingManager(s)
			require.NoError(t, err)

			// first get spd add spd key to cache
			_, _ = s.GetSPD(context.Background(), tt.args.pod)
			go m.Run(context.Background())
			time.Sleep(1 * time.Second)

			got, err := m.ServiceBusinessPerformanceLevel(context.Background(), tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("ServiceBusinessPerformanceLevel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ServiceBusinessPerformanceLevel() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_serviceProfilingManager_ServiceSystemPerformanceTarget(t *testing.T) {
	t.Parallel()

	type fields struct {
		nodeName string
		spd      *workloadapis.ServiceProfileDescriptor
		cnc      *v1alpha1.CustomNodeConfig
	}
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    IndicatorTarget
		wantErr bool
	}{
		{
			name: "service performance is good",
			fields: fields{
				nodeName: "node-1",
				spd: &workloadapis.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "spd-1",
						Namespace: "default",
						Annotations: map[string]string{
							pkgconsts.ServiceProfileDescriptorAnnotationKeyConfigHash: "3c7e3ff3f218",
						},
					},
					Spec: workloadapis.ServiceProfileDescriptorSpec{
						SystemIndicator: []workloadapis.ServiceSystemIndicatorSpec{
							{
								Name: workloadapis.ServiceSystemIndicatorNameCPUSchedWait,
								Indicators: []workloadapis.Indicator{
									{
										IndicatorLevel: workloadapis.IndicatorLevelLowerBound,
										Value:          10,
									},
									{
										IndicatorLevel: workloadapis.IndicatorLevelUpperBound,
										Value:          100,
									},
								},
							},
							{
								Name: workloadapis.ServiceSystemIndicatorNameCPI,
								Indicators: []workloadapis.Indicator{
									{
										IndicatorLevel: workloadapis.IndicatorLevelLowerBound,
										Value:          1.4,
									},
									{
										IndicatorLevel: workloadapis.IndicatorLevelUpperBound,
										Value:          2.4,
									},
								},
							},
						},
					},
				},
				cnc: &v1alpha1.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						ServiceProfileConfigList: []v1alpha1.TargetConfig{
							{
								ConfigName:      "spd-1",
								ConfigNamespace: "default",
								Hash:            "3c7e3ff3f218",
							},
						},
					},
				},
			},
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd-1",
						},
					},
				},
			},
			want: IndicatorTarget{
				string(workloadapis.ServiceSystemIndicatorNameCPUSchedWait): {
					UpperBound: pointer.Float64(100),
					LowerBound: pointer.Float64(10),
				},
				string(workloadapis.ServiceSystemIndicatorNameCPI): {
					UpperBound: pointer.Float64(2.4),
					LowerBound: pointer.Float64(1.4),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "checkpoint-Test_serviceProfilingManager_ServiceSystemPerformanceTarget")
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			conf := generateTestConfiguration(t, tt.fields.nodeName, dir)
			genericCtx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{
				tt.fields.spd,
				tt.fields.cnc,
			})
			require.NoError(t, err)

			cncFetcher := cnc.NewCachedCNCFetcher(conf.BaseConfiguration, conf.CNCConfiguration, genericCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs())
			s, err := NewSPDFetcher(genericCtx.Client, metrics.DummyMetrics{}, cncFetcher, conf)
			require.NoError(t, err)
			require.NotNil(t, s)

			m := NewServiceProfilingManager(s)
			require.NoError(t, err)

			// first get spd add pod spd key to cache
			_, _ = s.GetSPD(context.Background(), tt.args.pod)
			go m.Run(context.Background())
			time.Sleep(1 * time.Second)

			got, err := m.ServiceSystemPerformanceTarget(context.Background(), tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("ServiceSystemPerformanceTarget() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if apiequality.Semantic.DeepEqual(tt.want, got) {
				t.Errorf("ServiceSystemPerformanceTarget() got = %v, want %v", got, tt.want)
			}
		})
	}
}
