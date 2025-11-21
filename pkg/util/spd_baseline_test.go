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
		c1 *DeploySPDBaselinePodMeta
	}
	tests := []struct {
		name string
		c    *DeploySPDBaselinePodMeta
		args args
		want int
	}{
		{
			name: "less",
			c: &DeploySPDBaselinePodMeta{
				metav1.NewTime(time.UnixMilli(0)),
				"pod2",
			},
			args: args{
				c1: &DeploySPDBaselinePodMeta{
					metav1.NewTime(time.UnixMilli(1)),
					"pod3",
				},
			},
			want: -1,
		},
		{
			name: "greater",
			c: &DeploySPDBaselinePodMeta{
				metav1.NewTime(time.UnixMilli(1)),
				"pod3",
			},
			args: args{
				c1: &DeploySPDBaselinePodMeta{
					metav1.NewTime(time.UnixMilli(1)),
					"pod2",
				},
			},
			want: 1,
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

func TestSolarSPDBaselineCoefficient_Cmp(t *testing.T) {
	t.Parallel()

	type args struct {
		c1 *SolarSPDBaselinePodMeta
	}
	tests := []struct {
		name string
		c    *SolarSPDBaselinePodMeta
		args args
		want int
	}{
		{
			name: "shard less",
			c: &SolarSPDBaselinePodMeta{
				"dp-foo-4-0",
				4,
				0,
			},
			args: args{
				c1: &SolarSPDBaselinePodMeta{
					"dp-foo-11-1",
					11,
					1,
				},
			},
			want: -1,
		},
		{
			name: "replica less",
			c: &SolarSPDBaselinePodMeta{
				"dp-foo-4-0",
				4,
				0,
			},
			args: args{
				c1: &SolarSPDBaselinePodMeta{
					"dp-foo-4-1",
					11,
					1,
				},
			},
			want: -1,
		},
		{
			name: "shard greater",
			c: &SolarSPDBaselinePodMeta{
				"dp-foo-9-0",
				9,
				0,
			},
			args: args{
				c1: &SolarSPDBaselinePodMeta{
					"dp-foo-4-0",
					4,
					0,
				},
			},
			want: 1,
		},
		{
			name: "replica greater",
			c: &SolarSPDBaselinePodMeta{
				"dp-foo-9-11",
				9,
				11,
			},
			args: args{
				c1: &SolarSPDBaselinePodMeta{
					"dp-foo-9-4",
					9,
					4,
				},
			},
			want: 1,
		},
		{
			name: "equal",
			c: &SolarSPDBaselinePodMeta{
				"dp-foo-9-4",
				9,
				4,
			},
			args: args{
				c1: &SolarSPDBaselinePodMeta{
					"dp-foo-9-4",
					9,
					4,
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
			c:    &DeploySPDBaselinePodMeta{metav1.NewTime(time.UnixMilli(1)), "pod2"},
			want: "{\"timeStamp\":\"1970-01-01T00:00:00Z\",\"podName\":\"pod2\"}",
		},
		{
			name: "solar SPD",
			c:    &SolarSPDBaselinePodMeta{"dp-foo-9-4", 9, 4},
			want: "{\"podName\":\"dp-foo-9-4\",\"shardID\":9,\"replicaID\":4}",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SPDBaselinePodMetaToString(tt.c); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPodBaselineCoefficient(t *testing.T) {
	t.Parallel()

	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want SPDBaselinePodMeta
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
			want: &DeploySPDBaselinePodMeta{
				metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.Local)),
				"test-pod",
			},
		},
		{
			name: "solar spd normal",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo-0-1",
						Labels: map[string]string{
							LabelStatefulSetExtensionName:    "dp-foo-0",
							LabelStatefulSetExtensionReplica: "1",
						},
					},
				},
			},
			want: &SolarSPDBaselinePodMeta{
				PodName:   "dp-foo-0-1",
				ShardID:   0,
				ReplicaID: 1,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetSPDBaselinePodMeta(tt.args.pod.ObjectMeta)
			if err != nil {
				t.Errorf("GetSPDBaselinePodMeta() error = %v", err)
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
		want    DeploySPDBaselinePodMeta
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
			want: DeploySPDBaselinePodMeta{
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
			deploySPDBaselinePodMeta, ok := got.(*DeploySPDBaselinePodMeta)
			if !ok {
				t.Errorf("GetSPDBaselineSentinel() error = %v", err)
				return
			}
			assert.Equal(t, tt.want.PodName, deploySPDBaselinePodMeta.PodName)
			assert.True(t, tt.want.TimeStamp.Equal(&deploySPDBaselinePodMeta.TimeStamp))
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
		want    SolarSPDBaselinePodMeta
		wantErr bool
	}{
		{
			name: "normal",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "dp-foo",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: "{\"podName\":\"dp-foo-0-1\",\"shardID\":0,\"replicaID\":1}",
						},
					},
				},
			},
			want: SolarSPDBaselinePodMeta{
				PodName:   "dp-foo-0-1",
				ShardID:   0,
				ReplicaID: 1,
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
			solarSPDBaselinePodMeta, ok := got.(*SolarSPDBaselinePodMeta)
			if !ok {
				t.Errorf("GetSPDBaselineSentinel() error = %v", err)
				return
			}
			assert.Equal(t, tt.want.PodName, solarSPDBaselinePodMeta.PodName)
			assert.Equal(t, tt.want.ShardID, solarSPDBaselinePodMeta.ShardID)
			assert.Equal(t, tt.want.ReplicaID, solarSPDBaselinePodMeta.ReplicaID)
		})
	}
}

func TestIsBaselinePod(t *testing.T) {
	t.Parallel()

	type args struct {
		pod              *v1.Pod
		baselinePercent  *int32
		baselineSentinel SPDBaselinePodMeta
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
				baselineSentinel: &DeploySPDBaselinePodMeta{
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
				baselineSentinel: &DeploySPDBaselinePodMeta{
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
				baselineSentinel: &SolarSPDBaselinePodMeta{
					PodName:   "dp-foo-2-3",
					ReplicaID: 3,
					ShardID:   2,
				},
			},
			wantIsBaselinePod: true,
		},
		{
			name: "solar replica baseline pod",
			args: args{
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
				baselineSentinel: &SolarSPDBaselinePodMeta{
					PodName:   "dp-foo-2-3",
					ReplicaID: 3,
					ShardID:   2,
				},
			},
			wantIsBaselinePod: true,
		},
		{
			name: "solar not baseline pod",
			args: args{
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
				baselineSentinel: &SolarSPDBaselinePodMeta{
					PodName:   "dp-foo-2-3",
					ReplicaID: 3,
					ShardID:   2,
				},
			},
			wantIsBaselinePod: false,
		},
		{
			name: "solar baseline disabled",
			args: args{
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

			got, err := IsBaselinePod(tt.args.pod.ObjectMeta, tt.args.baselinePercent, tt.args.baselineSentinel)
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
		c   SPDBaselinePodMeta
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
				c: &DeploySPDBaselinePodMeta{
					metav1.NewTime(time.UnixMilli(1690848000)),
					"test-spd",
				},
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-spd",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: "{\"timeStamp\":\"1970-01-20T13:40:48Z\",\"podName\":\"test-spd\"}",
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
				c: &SolarSPDBaselinePodMeta{
					"dp-foo-9-4",
					9,
					4,
				},
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dp-foo",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselineSentinelKey: "{\"podName\":\"dp-foo-9-4\",\"shardID\":9,\"replicaID\":4}",
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
