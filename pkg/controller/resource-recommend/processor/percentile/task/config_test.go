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

package task

import (
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"

	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func TestGetProcessConfig(t *testing.T) {
	type args struct {
		extensions processortypes.TaskConfigStr
	}
	tests := []struct {
		name    string
		args    args
		want    *ProcessConfig
		wantErr bool
	}{
		{
			name: "case-1",
			args: args{
				extensions: processortypes.TaskConfigStr(`{"decayHalfLife":222,"key2":123}`),
			},
			want: &ProcessConfig{
				DecayHalfLife: time.Hour * 24,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetTaskConfig(tt.args.extensions)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetTaskConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			fmt.Printf("%v", got)
		})
	}
}

func TestHistogramOptionsFactory(t *testing.T) {
	type want struct {
		BucketsNum int
		BucketFind struct {
			value  float64
			bucket int
		}
		StartBucket struct {
			bucket int
			value  float64
		}
		Epsilon float64
	}
	tests := []struct {
		name    string
		args    v1.ResourceName
		want    want
		wantErr bool
	}{
		{
			name: "case1",
			args: v1.ResourceCPU,
			want: want{
				BucketsNum: 176,
				BucketFind: struct {
					value  float64
					bucket int
				}{value: 10, bucket: 80},
				StartBucket: struct {
					bucket int
					value  float64
				}{bucket: 10, value: 0.1257789253554882},
				Epsilon: DefaultEpsilon,
			},
			wantErr: false,
		},
		{
			name: "case2",
			args: v1.ResourceMemory,
			want: want{
				BucketsNum: 176,
				BucketFind: struct {
					value  float64
					bucket int
				}{value: 976875765, bucket: 36},
				StartBucket: struct {
					bucket int
					value  float64
				}{bucket: 90, value: 1.5946073009825298e+10},
				Epsilon: DefaultEpsilon,
			},
			wantErr: false,
		},
		{
			name:    "case3",
			args:    v1.ResourceName("errName"),
			want:    want{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HistogramOptionsFactory(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("HistogramOptionsFactory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if NumBuckets := got.NumBuckets(); NumBuckets != tt.want.BucketsNum {
				t.Errorf("HistogramOptionsFactory() NumBuckets gotT = %v, want %v", NumBuckets, tt.want.BucketsNum)
				// return
			}
			if gotT := got.FindBucket(tt.want.BucketFind.value); gotT != tt.want.BucketFind.bucket {
				t.Errorf("HistogramOptionsFactory() FindBucket gotT = %v, want %v", gotT, tt.want.BucketFind.bucket)
				// return
			}
			if gotT := got.GetBucketStart(tt.want.StartBucket.bucket); gotT != tt.want.StartBucket.value {
				t.Errorf("HistogramOptionsFactory() GetBucketStart gotT = %v, want %v", gotT, tt.want.StartBucket.value)
				// return
			}
			if gotT := got.Epsilon(); gotT != tt.want.Epsilon {
				t.Errorf("HistogramOptionsFactory() Epsilon gotT = %v, want %v", gotT, tt.want.Epsilon)
				// return
			}
		})
	}
}

func TestGetDefaultTaskProcessInterval(t *testing.T) {
	tests := []struct {
		name    string
		args    v1.ResourceName
		wantT   time.Duration
		wantErr bool
	}{
		{
			name:    "case1",
			args:    v1.ResourceCPU,
			wantT:   DefaultCPUTaskProcessInterval,
			wantErr: false,
		},
		{
			name:    "case2",
			args:    v1.ResourceMemory,
			wantT:   DefaultMemTaskProcessInterval,
			wantErr: false,
		},
		{
			name:    "case3",
			args:    v1.ResourceName("errName"),
			wantT:   0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotT, err := GetDefaultTaskProcessInterval(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDefaultTaskProcessInterval() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotT != tt.wantT {
				t.Errorf("GetDefaultTaskProcessInterval() gotT = %v, want %v", gotT, tt.wantT)
			}
		})
	}
}
