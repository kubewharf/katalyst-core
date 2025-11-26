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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestDeploySPDBaselineCoefficient_Cmp(t *testing.T) {
	t.Parallel()

	type args struct {
		c1 SPDBaselinePodMeta
	}
	tests := []struct {
		name string
		c    SPDBaselinePodMeta
		args args
		want int
	}{
		{
			name: "less",
			c: SPDBaselinePodMeta{
				TimeStamp: metav1.NewTime(time.UnixMilli(0)),
				PodName:   "pod2",
			},
			args: args{
				c1: SPDBaselinePodMeta{
					TimeStamp: metav1.NewTime(time.UnixMilli(1)),
					PodName:   "pod3",
				},
			},
			want: -1,
		},
		{
			name: "greater",
			c: SPDBaselinePodMeta{
				TimeStamp: metav1.NewTime(time.UnixMilli(1)),
				PodName:   "pod3",
			},
			args: args{
				c1: SPDBaselinePodMeta{
					TimeStamp: metav1.NewTime(time.UnixMilli(1)),
					PodName:   "pod2",
				},
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.c.Cmp(&tt.args.c1); got != tt.want {
				t.Errorf("Cmp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSolarSPDBaselineCoefficient_Cmp(t *testing.T) {
	t.Parallel()

	type args struct {
		c1 *SPDBaselinePodMeta
	}
	tests := []struct {
		name string
		c    *SPDBaselinePodMeta
		args args
		want int
	}{
		{
			name: "shard less",
			c: &SPDBaselinePodMeta{
				PodName:            "dp-foo-4-0",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 4.0,
			},
			args: args{
				c1: &SPDBaselinePodMeta{
					PodName:            "dp-foo-11-1",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 11.0,
				},
			},
			want: -1,
		},
		{
			name: "replica less",
			c: &SPDBaselinePodMeta{
				PodName:            "dp-foo-4-0",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 4.0,
			},
			args: args{
				c1: &SPDBaselinePodMeta{
					PodName:            "dp-foo-4-1",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 4.0,
				},
			},
			want: -1,
		},
		{
			name: "shard greater",
			c: &SPDBaselinePodMeta{
				PodName:            "dp-foo-9-0",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 9.0,
			},
			args: args{
				c1: &SPDBaselinePodMeta{
					PodName:            "dp-foo-4-0",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 4.0,
				},
			},
			want: 1,
		},
		{
			name: "replica greater",
			c: &SPDBaselinePodMeta{
				PodName:            "dp-foo-9-11",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 9.0,
			},
			args: args{
				c1: &SPDBaselinePodMeta{
					PodName:            "dp-foo-9-4",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 9.0,
				},
			},
			want: -1,
		},
		{
			name: "equal",
			c: &SPDBaselinePodMeta{
				PodName:            "dp-foo-9-4",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 9.0,
			},
			args: args{
				c1: &SPDBaselinePodMeta{
					PodName:            "dp-foo-9-4",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 9.0,
				},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.c.Cmp(tt.args.c1); got != tt.want {
				t.Errorf("Cmp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBaselineCoefficient_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    SPDBaselinePodMeta
		want string
	}{
		{
			name: "deploy SPD",
			c: SPDBaselinePodMeta{
				TimeStamp: metav1.NewTime(time.UnixMilli(1)),
				PodName:   "pod2",
			},
			want: "{\"timeStamp\":\"1970-01-01T00:00:00Z\",\"podName\":\"pod2\",\"customCompareKey\":null,\"customCompareValue\":null}",
		},
		{
			name: "solar SPD",
			c: SPDBaselinePodMeta{
				PodName:            "dp-foo-9-4",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 9.0,
			},
			want: "{\"timeStamp\":null,\"podName\":\"dp-foo-9-4\",\"customCompareKey\":\"shard_id\",\"customCompareValue\":9}",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.c.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPodBaselineCoefficient(t *testing.T) {
	t.Parallel()

	type args struct {
		pod              *v1.Pod
		customCompareKey *CustomCompareKey
	}
	tests := []struct {
		name string
		args args
		want *SPDBaselinePodMeta
	}{
		{
			name: "deploy spd normal",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.Local)),
					},
				},
			},
			want: &SPDBaselinePodMeta{
				TimeStamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.Local)),
				PodName:   "test-pod",
			},
		},
		{
			name: "solar spd normal",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-0-1",
						Labels: map[string]string{
							LabelStatefulSetExtensionName: "dp-foo-0",
						},
					},
				},
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
			},
			want: &SPDBaselinePodMeta{
				PodName:            "dp-foo-0-1",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 0.0,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetSPDBaselinePodMeta(tt.args.pod.ObjectMeta, tt.args.customCompareKey)
			if err != nil {
				t.Errorf("getSPDBaselinePodMeta() error = %v", err)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetDeploySPDBaselinePercentile(t *testing.T) {
	t.Parallel()

	type args struct {
		spd *v1alpha1.ServiceProfileDescriptor
	}
	tests := []struct {
		name    string
		args    args
		want    SPDBaselinePodMeta
		wantErr bool
	}{
		{
			name: "normal",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":\"2023-12-01T00:00:00Z\",\"podName\":\"pod1\"}",
						},
					},
				},
			},
			want: SPDBaselinePodMeta{
				TimeStamp: metav1.NewTime(time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)),
				PodName:   "pod1",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetSPDBaselineSentinel(tt.args.spd)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPDBaselineSentinel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, tt.want.PodName, got.PodName)
			assert.True(t, tt.want.TimeStamp.Equal(&got.TimeStamp))
		})
	}
}

func TestGetSolarSPDBaselinePercentile(t *testing.T) {
	t.Parallel()
	type args struct {
		spd *v1alpha1.ServiceProfileDescriptor
	}
	tests := []struct {
		name    string
		args    args
		want    *SPDBaselinePodMeta
		wantErr bool
	}{
		{
			name: "normal",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: SPDBaselinePodMeta{
								PodName:            "dp-foo-0-1",
								CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
								CustomCompareValue: 0.0,
							}.String(),
						},
					},
				},
			},
			want: &SPDBaselinePodMeta{
				PodName:            "dp-foo-0-1",
				CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
				CustomCompareValue: 0.0,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetSPDBaselineSentinel(tt.args.spd)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPDBaselineSentinel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			assert.Equal(t, tt.want.PodName, got.PodName)
			assert.Equal(t, tt.want.CustomCompareKey, got.CustomCompareKey)
			assert.Equal(t, tt.want.CustomCompareValue, got.CustomCompareValue)
		})
	}
}

func TestIsBaselinePod(t *testing.T) {
	t.Parallel()

	type args struct {
		pod              *v1.Pod
		baselinePercent  *int32
		baselineSentinel *SPDBaselinePodMeta
		customCompareKey *CustomCompareKey
	}
	tests := []struct {
		name              string
		args              args
		wantIsBaselinePod bool
		wantErr           bool
	}{
		{
			name: "deploy baseline pod",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
					},
				},
				baselinePercent: pointer.Int32(10),
				baselineSentinel: &SPDBaselinePodMeta{
					TimeStamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
					PodName:   "test-pod",
				},
			},
			wantIsBaselinePod: true,
		},
		{
			name: "deploy not baseline pod",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				baselinePercent: pointer.Int32(10),
				baselineSentinel: &SPDBaselinePodMeta{
					TimeStamp: metav1.Time{},
					PodName:   "",
				},
			},
			wantIsBaselinePod: false,
		},
		{
			name: "deploy baseline disabled",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				baselinePercent:  nil,
				baselineSentinel: nil,
			},
			wantIsBaselinePod: false,
		},
		{
			name: "deploy baseline 100%",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				baselinePercent:  pointer.Int32(100),
				baselineSentinel: nil,
			},
			wantIsBaselinePod: true,
		},
		{
			name: "deploy baseline 0%",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				baselinePercent:  pointer.Int32(0),
				baselineSentinel: nil,
			},
			wantIsBaselinePod: false,
		},
		{
			name: "solar shard baseline pod",
			args: args{
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-1-2",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-1",
							LabelStatefulSetExtensionReplica: "2",
						},
					},
				},
				baselinePercent: pointer.Int32(10),
				baselineSentinel: &SPDBaselinePodMeta{
					PodName:            "dp-foo-2-3",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 2.0,
				},
			},
			wantIsBaselinePod: true,
		},
		{
			name: "solar replica baseline pod",
			args: args{
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-2-2",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-2",
							LabelStatefulSetExtensionReplica: "2",
						},
					},
				},
				baselinePercent: pointer.Int32(10),
				baselineSentinel: &SPDBaselinePodMeta{
					PodName:            "dp-foo-2-3",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 2.0,
				},
			},
			wantIsBaselinePod: true,
		},
		{
			name: "solar not baseline pod",
			args: args{
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-3-2",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-3",
							LabelStatefulSetExtensionReplica: "2",
						},
					},
				},
				baselinePercent: pointer.Int32(10),
				baselineSentinel: &SPDBaselinePodMeta{
					PodName:            "dp-foo-2-3",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 2.0,
				},
			},
			wantIsBaselinePod: false,
		},
		{
			name: "solar baseline disabled",
			args: args{
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-3-2",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-3",
							LabelStatefulSetExtensionReplica: "2",
						},
					},
				},
				baselinePercent:  nil,
				baselineSentinel: nil,
			},
			wantIsBaselinePod: false,
		},
		{
			name: "solar baseline 100%",
			args: args{
				customCompareKey: &SPDBaselinePodMetaCustomCompareKeyShardID,
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-3-2",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-3",
							LabelStatefulSetExtensionReplica: "2",
						},
					},
				},
				baselinePercent:  pointer.Int32(100),
				baselineSentinel: nil,
			},
			wantIsBaselinePod: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := IsBaselinePod(tt.args.pod.ObjectMeta, tt.args.baselinePercent, tt.args.baselineSentinel, tt.args.customCompareKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsBaselinePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantIsBaselinePod {
				t.Errorf("IsBaselinePod() got = %v, want %v", got, tt.wantIsBaselinePod)
			}
		})
	}
}

func TestSetSPDBaselinePercentile(t *testing.T) {
	t.Parallel()

	type args struct {
		spd *v1alpha1.ServiceProfileDescriptor
		c   *SPDBaselinePodMeta
	}
	tests := []struct {
		name    string
		args    args
		wantSPD *v1alpha1.ServiceProfileDescriptor
	}{
		{
			name: "deploy add",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
					},
				},
				c: &SPDBaselinePodMeta{
					TimeStamp: metav1.NewTime(time.UnixMilli(1690848000)),
					PodName:   "test-spd",
				},
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-spd",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":\"1970-01-20T13:40:48Z\",\"podName\":\"test-spd\",\"customCompareKey\":null,\"customCompareValue\":null}",
					},
				},
			},
		},
		{
			name: "deploy delete",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":\"1970-01-20T13:40:48Z\",\"podName\":\"test-spd\"}",
						},
					},
				},
				c: nil,
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-spd",
					Annotations: map[string]string{},
				},
			},
		},
		{
			name: "solar add",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo",
					},
				},
				c: &SPDBaselinePodMeta{
					PodName:            "dp-foo-9-4",
					CustomCompareKey:   &SPDBaselinePodMetaCustomCompareKeyShardID,
					CustomCompareValue: 9.0,
				},
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dp-foo",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":null,\"podName\":\"dp-foo-9-4\",\"customCompareKey\":\"shard_id\",\"customCompareValue\":9}",
					},
				},
			},
		},
		{
			name: "solar delete",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: "{\"podName\":\"dp-foo-9-4\",\"shardID\":9,\"replicaID\":4}",
						},
					},
				},
				c: nil,
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "dp-foo",
					Annotations: map[string]string{},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			SetSPDBaselineSentinel(tt.args.spd, tt.args.c)
			assert.Equal(t, tt.wantSPD, tt.args.spd)
		})
	}
}

func TestGetStseCustomSPDBaselinePodMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		podName     string
		labels      map[string]string
		initialVal  interface{}
		wantErr     bool
		errContains string
		wantValue   float64
	}{
		{
			name:       "success_parses_shard_3",
			podName:    "dp-foo-3-4",
			labels:     map[string]string{LabelStatefulSetExtensionName: "dp-foo-3"},
			initialVal: nil,
			wantErr:    false,
			wantValue:  3.0,
		},
		{
			name:        "invalid_format_two_parts",
			podName:     "dp-foo",
			labels:      map[string]string{LabelStatefulSetExtensionName: "dp-foo"},
			wantErr:     true,
			errContains: "invalid statefulsetextension format",
		},
		{
			name:        "non_numeric_shard",
			podName:     "dp-foo-abc",
			labels:      map[string]string{LabelStatefulSetExtensionName: "dp-foo-abc"},
			wantErr:     true,
			errContains: "invalid shard segment",
		},
		{
			name:        "missing_label_nil_map",
			labels:      nil,
			wantErr:     true,
			errContains: "invalid statefulsetextension format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			podMeta := metav1.ObjectMeta{Labels: tc.labels}
			spdBaselinePodMeta := &SPDBaselinePodMeta{}

			err := GetStseCustomSPDBaselinePodMeta(podMeta, spdBaselinePodMeta)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error to contain %q, got %v", tc.errContains, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			val, ok := spdBaselinePodMeta.CustomCompareValue.(float64)
			if !ok {
				t.Fatalf("CustomCompareValue type = %T, want float64", spdBaselinePodMeta.CustomCompareValue)
			}
			if val != tc.wantValue {
				t.Fatalf("CustomCompareValue = %v, want %v", val, tc.wantValue)
			}
		})
	}
}
