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
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	nonDefaultNumaFreeBelowWatermarkTimesThreshold = 5
	nonDefaultSystemKswapdRateThreshold            = 3000
	nonDefaulSsystemKswapdRateExceedTimesThreshold = 5
	nonDefaultNumaEvictionRankingMetrics           = []string{"metric1", "metric2"}
	nonDefaultSystemEvictionRankingMetrics         = []string{"metric3"}
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
		klog.Error(err)
	}
	return ret
}

func Test_kccTargetResource_GetCollisionCount(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32Ptr(10),
					},
				}),
			},
			want: pointer.Int32Ptr(10),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetCollisionCount(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCollisionCount() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetHash(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: "",
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.KatalystCustomConfigAnnotationKeyConfigHash: "hash-1",
						},
					},
				}),
			},
			want: "hash-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetHash(); got != tt.want {
				t.Errorf("GetHash() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetLabelSelector(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: "",
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetLabelSelector(); got != tt.want {
				t.Errorf("GetLabelSelector() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetLastDuration(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetLastDuration(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetLastDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetNodeNames(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: nil,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetNodeNames(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetObservedGeneration(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: 0,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 10,
					},
				}),
			},
			want: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetObservedGeneration(); got != tt.want {
				t.Errorf("GetObservedGeneration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GetRevisionHistoryLimit(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: 0,
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetRevisionHistoryLimit(); got != tt.want {
				t.Errorf("GetRevisionHistoryLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetCollisionCount(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			args: args{
				pointer.Int32Ptr(30),
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: pointer.Int32Ptr(30),
				},
			}),
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32Ptr(10),
					},
				}),
			},
			args: args{
				pointer.Int32Ptr(20),
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: pointer.Int32Ptr(20),
				},
			}),
		},
		{
			name: "test-3",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Status: v1alpha1.GenericConfigStatus{
						CollisionCount: pointer.Int32Ptr(10),
					},
				}),
			},
			args: args{
				nil,
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				Status: v1alpha1.GenericConfigStatus{
					CollisionCount: nil,
				},
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			g.SetCollisionCount(tt.args.count)
			if !reflect.DeepEqual(tt.fields.Unstructured, tt.want) {
				t.Errorf("SetCollisionCount() = %v, want %v", tt.fields.Unstructured, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetHash(t *testing.T) {
	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		hash string
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			args: args{
				"hash-1",
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.KatalystCustomConfigAnnotationKeyConfigHash: "hash-1",
					},
				},
			}),
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							consts.KatalystCustomConfigAnnotationKeyConfigHash: "hash-1",
						},
					},
				}),
			},
			args: args{
				"hash-2",
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.KatalystCustomConfigAnnotationKeyConfigHash: "hash-2",
					},
				},
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			g.SetHash(tt.args.hash)
			if !reflect.DeepEqual(tt.fields.Unstructured, tt.want) {
				t.Errorf("SetHash() = %v, want %v", tt.fields.Unstructured, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetObservedGeneration(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 10,
					},
				}),
			},
			args: args{
				20,
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				Status: v1alpha1.GenericConfigStatus{
					ObservedGeneration: 20,
				},
			}),
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			args: args{
				30,
			},
			want: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
				Status: v1alpha1.GenericConfigStatus{
					ObservedGeneration: 30,
				},
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
						Config: v1alpha1.AgentConfig{},
					},
				}),
			},
			args: args{
				&v1alpha1.KatalystAgentConfig{},
			},
			want: &v1alpha1.KatalystAgentConfig{
				Spec: v1alpha1.KatalystAgentConfigSpec{
					Config: v1alpha1.AgentConfig{},
				},
			},
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			args: args{
				&v1alpha1.KatalystAgentConfig{},
			},
			want: &v1alpha1.KatalystAgentConfig{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.GetGenericStatus(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGenericStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_SetGenericStatus(t *testing.T) {
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
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
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{}),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.CheckExpired(time.Now()); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CheckExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kccTargetResource_GenerateConfigHash(t *testing.T) {
	type fields struct {
		Unstructured *unstructured.Unstructured
	}
	type args struct {
		length int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "test-1",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
						Config: v1alpha1.AgentConfig{
							ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
								EvictionThreshold: map[v1.ResourceName]float64{
									"aa": 11,
								},
							},
							MemoryEvictionPluginConfig: v1alpha1.MemoryEvictionPluginConfig{
								NumaFreeBelowWatermarkTimesThreshold: &nonDefaultNumaFreeBelowWatermarkTimesThreshold,
								SystemKswapdRateThreshold:            &nonDefaultSystemKswapdRateThreshold,
								SystemKswapdRateExceedTimesThreshold: &nonDefaulSsystemKswapdRateExceedTimesThreshold,
								NumaEvictionRankingMetrics:           nonDefaultNumaEvictionRankingMetrics,
								SystemEvictionRankingMetrics:         nonDefaultSystemEvictionRankingMetrics,
							},
						},
					},
				}),
			},
			args: args{
				12,
			},
			want: "37bcaf611bb5",
		},
		{
			name: "test-2",
			fields: fields{
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					Spec: v1alpha1.KatalystAgentConfigSpec{
						Config: v1alpha1.AgentConfig{},
					},
				}),
			},
			args: args{
				length: 12,
			},
			want: "007a15227162",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			got, err := g.GenerateConfigHash(tt.args.length)
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
					},
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
				Unstructured: toTestUnstructured(&v1alpha1.KatalystAgentConfig{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: generateTestMetaV1Time("2006-01-02T15:04:05Z"),
					},
					Spec: v1alpha1.KatalystAgentConfigSpec{
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
		t.Run(tt.name, func(t *testing.T) {
			g := KCCTargetResource{
				Unstructured: tt.fields.Unstructured,
			}
			if got := g.CheckExpired(tt.args.now); got != tt.want {
				t.Errorf("CheckExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
