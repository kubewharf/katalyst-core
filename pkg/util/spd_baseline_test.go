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
		c1 BaselineCoefficient
	}
	tests := []struct {
		name string
		c    BaselineCoefficient
		args args
		want int
	}{
		{
			name: "less",
			c: BaselineCoefficient{
				0,
				2,
			},
			args: args{
				c1: BaselineCoefficient{
					1,
					3,
				},
			},
			want: -1,
		},
		{
			name: "greater",
			c: BaselineCoefficient{
				1,
				3,
			},
			args: args{
				c1: BaselineCoefficient{
					1,
					2,
				},
			},
			want: 1,
		},
		{
			name: "lens not equal",
			c: BaselineCoefficient{
				2,
				3,
			},
			args: args{
				c1: BaselineCoefficient{
					2,
				},
			},
			want: 0,
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
		c    BaselineCoefficient
		want string
	}{
		{
			name: "normal",
			c: BaselineCoefficient{
				1, 2,
			},
			want: "1,2",
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
		want BaselineCoefficient
	}{
		{
			name: "normal",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod",
						CreationTimestamp: metav1.NewTime(time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)),
					},
				},
			},
			want: BaselineCoefficient{
				1690848000,
				3141654459,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetPodBaselineCoefficient(tt.args.pod); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPodBaselineCoefficient() = %v, want %v", got, tt.want)
			}
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
		want    BaselineCoefficient
		wantErr bool
	}{
		{
			name: "normal",
			args: args{
				spd: &v1alpha1.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-spd",
						Annotations: map[string]string{
							consts.SPDAnnotationBaselinePercentileKey: "1,2",
						},
					},
				},
			},
			want: BaselineCoefficient{
				1,
				2,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetSPDBaselinePercentile(tt.args.spd)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSPDBaselinePercentile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetSPDBaselinePercentile() got = %v, want %v", got, tt.want)
			}
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
		name    string
		args    args
		want    bool
		want1   bool
		wantErr bool
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
							consts.SPDAnnotationBaselinePercentileKey: "1690848001,3141654459",
						},
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselineRatio: pointer.Float32(0.1),
					},
				},
			},
			want:  true,
			want1: true,
		},
		{
			name: "no baseline pod",
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
							consts.SPDAnnotationBaselinePercentileKey: "1690848001,3141654459",
						},
					},
					Spec: v1alpha1.ServiceProfileDescriptorSpec{
						BaselineRatio: pointer.Float32(0.1),
					},
				},
			},
			want:  false,
			want1: true,
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
			want:  false,
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := IsBaselinePod(tt.args.pod, tt.args.spd)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsBaselinePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsBaselinePod() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("IsBaselinePod() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func TestSetSPDBaselinePercentile(t *testing.T) {
	t.Parallel()

	type args struct {
		spd *v1alpha1.ServiceProfileDescriptor
		c   *BaselineCoefficient
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
				c: &BaselineCoefficient{
					1690848000,
					3141654459,
				},
			},
			wantSPD: &v1alpha1.ServiceProfileDescriptor{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-spd",
					Annotations: map[string]string{
						consts.SPDAnnotationBaselinePercentileKey: "1690848000,3141654459",
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
							consts.SPDAnnotationBaselinePercentileKey: "1690848000,3141654459",
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
			SetSPDBaselinePercentile(tt.args.spd, tt.args.c)
			assert.Equal(t, tt.args.spd, tt.wantSPD)
		})
	}
}
