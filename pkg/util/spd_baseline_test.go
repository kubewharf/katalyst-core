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

func TestBaselineCoefficient_Cmp(t *testing.T) {
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
				metav1.NewTime(time.UnixMilli(0)),
				"pod2",
			},
			args: args{
				c1: SPDBaselinePodMeta{
					metav1.NewTime(time.UnixMilli(1)),
					"pod3",
				},
			},
			want: -1,
		},
		{
			name: "greater",
			c: SPDBaselinePodMeta{
				metav1.NewTime(time.UnixMilli(1)),
				"pod3",
			},
			args: args{
				c1: SPDBaselinePodMeta{
					metav1.NewTime(time.UnixMilli(1)),
					"pod2",
				},
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			name: "normal",
			c:    SPDBaselinePodMeta{metav1.NewTime(time.UnixMilli(1)), "pod2"},
			want: "{\"timeStamp\":\"1970-01-01T00:00:00Z\",\"podName\":\"pod2\"}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPodBaselineCoefficient(t *testing.T) {
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want SPDBaselinePodMeta
	}{
		{
			name: "normal",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.Local)),
					},
				},
			},
			want: SPDBaselinePodMeta{
				metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.Local)),
				"test-pod",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetPodMeta(tt.args.pod))
		})
	}
}

func TestGetSPDBaselinePercentile(t *testing.T) {
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
		t.Run(tt.name, func(t *testing.T) {
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

func TestIsBaselinePod(t *testing.T) {
	t.Parallel()

	type args struct {
		pod *v1.Pod
		spd *v1alpha1.ServiceProfileDescriptor
	}
	tests := []struct {
		name                string
		args                args
		wantIsBaselinePod   bool
		wantBaselineEnabled bool
		wantErr             bool
	}{
		{
			name: "baseline pod",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
					},
				},
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: SPDBaselinePodMeta{
								TimeStamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
								PodName:   "test-pod",
							}.String(),
						},
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselinePercent: pointer.Int32(10),
					},
				},
			},
			wantIsBaselinePod:   true,
			wantBaselineEnabled: true,
		},
		{
			name: "not baseline pod",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselineSentinelKey: SPDBaselinePodMeta{
								TimeStamp: metav1.Time{},
								PodName:   "",
							}.String(),
						},
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselinePercent: pointer.Int32(10),
					},
				},
			},
			wantIsBaselinePod:   false,
			wantBaselineEnabled: true,
		},
		{
			name: "baseline disabled",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
					},
				},
			},
			wantIsBaselinePod:   false,
			wantBaselineEnabled: false,
		},
		{
			name: "baseline 100%",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselinePercent: pointer.Int32(100),
					},
				},
			},
			wantIsBaselinePod:   true,
			wantBaselineEnabled: true,
		},
		{
			name: "baseline 0%",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 2, 0, 0, 0, 0, time.UTC)),
					},
				},
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselinePercent: pointer.Int32(0),
					},
				},
			},
			wantIsBaselinePod:   false,
			wantBaselineEnabled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := IsBaselinePod(tt.args.pod, tt.args.spd)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsBaselinePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantIsBaselinePod {
				t.Errorf("IsBaselinePod() got = %v, want %v", got, tt.wantIsBaselinePod)
			}
			if got1 != tt.wantBaselineEnabled {
				t.Errorf("IsBaselinePod() got1 = %v, want %v", got1, tt.wantBaselineEnabled)
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
			name: "add",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
					},
				},
				c: &SPDBaselinePodMeta{
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
			name: "delete",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetSPDBaselineSentinel(tt.args.spd, tt.args.c)
			assert.Equal(t, tt.args.spd, tt.wantSPD)
		})
	}
}
