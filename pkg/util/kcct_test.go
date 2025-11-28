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

package util

import (
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	nonDefaultNumaFreeBelowWatermarkTimesThreshold    = 5
	nonDefaultSystemKswapdRateThreshold               = 3000
	nonDefaultSystemKswapdRateExceedDurationThreshold = 5
	nonDefaultNumaEvictionRankingMetrics              = []string{"metric1", "metric2"}
	nonDefaultSystemEvictionRankingMetrics            = []string{"metric3"}
)

func generateTestDuration(t time.Duration) *metav1.Duration {
	return &metav1.Duration{
		Duration: t,
	}
}

func generateTestTime(t string) time.Time {
	nt, err := time.Parse(time.RFC3339, t)
	if err != nil {
		klog.Error(err)
	}
	return nt
}

func generateTestMetaV1Time(t string) metav1.Time {
	return metav1.NewTime(generateTestTime(t))
}

func toTestUnstructured(obj interface{}) *unstructured.Unstructured {
	ret, err := native.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	return ret
}

func Test_kccTargetResource_GetCollisionCount(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   *int32
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32(10),
					},
				}),
			},
			want: pointer.Int32(10),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetCollisionCount(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCollisionCount() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetCanary(t *testing.T) {
	t.Parallel()

	numberCanary := intstr.FromInt(10)
	percentageCanary := intstr.FromString("10%")

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   *intstr.IntOrString
	}{
		{
			name: "no updateStrategy",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: nil,
		},
		{
			name: "no rollingUpdate",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							UpdateStrategy: v1alpha1.ConfigUpdateStrategy{},
						},
					},
				}),
			},
			want: nil,
		},
		{
			name: "no canary",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							UpdateStrategy: v1alpha1.ConfigUpdateStrategy{
								RollingUpdate: &v1alpha1.RollingUpdateConfig{},
							},
						},
					},
				}),
			},
			want: nil,
		},
		{
			name: "with number canary",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							UpdateStrategy: v1alpha1.ConfigUpdateStrategy{
								RollingUpdate: &v1alpha1.RollingUpdateConfig{
									Canary: &numberCanary,
								},
							},
						},
					},
				}),
			},
			want: &numberCanary,
		},
		{
			name: "with percentage canary",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							UpdateStrategy: v1alpha1.ConfigUpdateStrategy{
								RollingUpdate: &v1alpha1.RollingUpdateConfig{
									Canary: &percentageCanary,
								},
							},
						},
					},
				}),
			},
			want: &percentageCanary,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetCanary(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCanary() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetPaused(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{
			name: "unspecified",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: false,
		},
		{
			name: "not paused",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							Paused: false,
						},
					},
				}),
			},
			want: false,
		},
		{
			name: "paused",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							Paused: true,
						},
					},
				}),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetPaused(); got != tt.want {
				t.Errorf("GetPaused() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetLabelSelector(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: "",
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
					},
				}),
			},
			want: "aa=bb",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetLabelSelector(); got != tt.want {
				t.Errorf("GetLabelSelector() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetLastDuration(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   *time.Duration
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							EphemeralSelector: v1alpha1.EphemeralSelector{
								LastDuration: generateTestDuration(10 * time.Hour),
							},
						},
					},
				}),
			},
			want: pointer.Duration(10 * time.Hour),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetLastDuration(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetLastDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetNodeNames(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   []string
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							EphemeralSelector: v1alpha1.EphemeralSelector{
								NodeNames: []string{
									"node-1",
									"node-2",
								},
							},
						},
					},
				}),
			},
			want: []string{
				"node-1",
				"node-2",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetNodeNames(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetObservedGeneration(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   int64
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: 0,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 10,
					},
				}),
			},
			want: 10,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetObservedGeneration(); got != tt.want {
				t.Errorf("GetObservedGeneration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetRevisionHistoryLimit(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   int64
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: 0,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							RevisionHistoryLimit: 3,
						},
					},
				}),
			},
			want: 3,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetRevisionHistoryLimit(); got != tt.want {
				t.Errorf("GetRevisionHistoryLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetCollisionCount(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		count *int32
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *unstructured.Unstructured
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			args: args{
				pointer.Int32(30),
			},
			want: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: pointer.Int32(30),
				},
			}),
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32(10),
					},
				}),
			},
			args: args{
				pointer.Int32(20),
			},
			want: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: pointer.Int32(20),
				},
			}),
		},
		{
			name: "test-3",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32(10),
					},
				}),
			},
			args: args{
				nil,
			},
			want: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: nil,
				},
			}),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			g.SetCollisionCount(tt.args.count)
			if !reflect.DeepEqual(tt.fields.Unstructured, tt.want) {
				t.Errorf("SetCollisionCount() = %v, want %v", tt.fields.Unstructured, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetObservedGeneration(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		generation int64
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *unstructured.Unstructured
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 10,
					},
				}),
			},
			args: args{
				20,
			},
			want: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				Status: v1alpha1.GenericConfigStatus{
					ObservedGeneration: 20,
				},
			}),
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			args: args{
				30,
			},
			want: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				Status: v1alpha1.GenericConfigStatus{
					ObservedGeneration: 30,
				},
			}),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			g.SetObservedGeneration(tt.args.generation)
			if !reflect.DeepEqual(tt.fields.Unstructured, tt.want) {
				t.Errorf("SetObservedGeneration() = %v, want %v", tt.fields.Unstructured, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetConfig(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		conf interface{}
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    interface{}
		wantErr bool
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{},
					},
				}),
			},
			args: args{
				&v1alpha1.AdminQoSConfiguration{},
			},
			want: &v1alpha1.AdminQoSConfiguration{
				Spec: v1alpha1.AdminQoSConfigurationSpec{
					Config: v1alpha1.AdminQoSConfig{},
				},
			},
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			args: args{
				&v1alpha1.AdminQoSConfiguration{},
			},
			want: &v1alpha1.AdminQoSConfiguration{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if err := g.Unmarshal(tt.args.conf); (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !reflect.DeepEqual(tt.args.conf, tt.want) {
				t.Errorf("Unmarshal() = %v, want %v", tt.args.conf, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetGenericStatus(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   v1alpha1.GenericConfigStatus
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
							},
						},
					},
				}),
			},
			want: v1alpha1.GenericConfigStatus{
				Conditions: []v1alpha1.GenericConfigCondition{
					{
						Type:   v1alpha1.ConfigConditionTypeValid,
						Status: v1.ConditionTrue,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetGenericStatus(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGenericStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetGenericStatus(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		status v1alpha1.GenericConfigStatus
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   v1alpha1.GenericConfigStatus
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Status: v1alpha1.GenericConfigStatus{
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
							},
						},
					},
				}),
			},
			args: args{
				v1alpha1.GenericConfigStatus{
					Conditions: []v1alpha1.GenericConfigCondition{
						{
							Type:   v1alpha1.ConfigConditionTypeValid,
							Status: v1.ConditionFalse,
						},
					},
				},
			},
			want: v1alpha1.GenericConfigStatus{
				Conditions: []v1alpha1.GenericConfigCondition{
					{
						Type:   v1alpha1.ConfigConditionTypeValid,
						Status: v1.ConditionFalse,
					},
				},
			},
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			args: args{
				v1alpha1.GenericConfigStatus{
					Conditions: []v1alpha1.GenericConfigCondition{
						{
							Type:               v1alpha1.ConfigConditionTypeValid,
							Status:             v1.ConditionFalse,
							LastTransitionTime: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
						},
					},
				},
			},
			want: v1alpha1.GenericConfigStatus{
				Conditions: []v1alpha1.GenericConfigCondition{
					{
						Type:               v1alpha1.ConfigConditionTypeValid,
						Status:             v1.ConditionFalse,
						LastTransitionTime: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			g.SetGenericStatus(tt.args.status)
			if !reflect.DeepEqual(tt.args.status, tt.want) {
				t.Errorf("SetGenericStatus() = %v, want %v", tt.args.status, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetIsValid(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{}),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.CheckExpired(time.Now()); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GenerateConfigHash(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	tests := []struct {
		name    string
		fields  fields
		want    string
		wantErr bool
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										"aa": 11,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										"bb": 22,
									},
								},
								MemoryPressureEvictionConfig: &v1alpha1.MemoryPressureEvictionConfig{
									NumaFreeBelowWatermarkTimesThreshold:    &nonDefaultNumaFreeBelowWatermarkTimesThreshold,
									SystemKswapdRateThreshold:               &nonDefaultSystemKswapdRateThreshold,
									SystemKswapdRateExceedDurationThreshold: &nonDefaultSystemKswapdRateExceedDurationThreshold,
									NumaEvictionRankingMetrics:              ConvertStringListToNumaEvictionRankingMetrics(nonDefaultNumaEvictionRankingMetrics),
									SystemEvictionRankingMetrics:            ConvertStringListToSystemEvictionRankingMetrics(nonDefaultSystemEvictionRankingMetrics),
								},
							},
						},
					},
				}),
			},
			want: "07f339db4d03",
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{},
					},
				}),
			},
			want: "44136fa355b3",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			got, err := g.GenerateConfigHash()
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateConfigHash() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GenerateConfigHash() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKCCTargetResource_IsExpired(t *testing.T) {
	t.Parallel()

	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		now time.Time
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							EphemeralSelector: v1alpha1.EphemeralSelector{
								LastDuration: generateTestDuration(5 * time.Hour),
							},
						},
					},
				}),
			},
			args: args{
				now: generateTestTime("2006-01-02T21:04:05Z"),
			},
			want: true,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							EphemeralSelector: v1alpha1.EphemeralSelector{
								LastDuration: generateTestDuration(5 * time.Hour),
							},
						},
					},
				}),
			},
			args: args{
				now: generateTestTime("2006-01-02T18:04:05Z"),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := KCCTargetResourceGeneral{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.CheckExpired(tt.args.now); got != tt.want {
				t.Errorf("CheckExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
