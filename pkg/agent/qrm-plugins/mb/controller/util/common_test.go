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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

func TestWeightedSplit(t *testing.T) {
	t.Parallel()
	type args struct {
		total   int
		weights []int
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "happy path",
			args: args{
				total:   100,
				weights: []int{45, 60, 45},
			},
			want: []int{30, 40, 30},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := weightedSplit(tt.args.total, tt.args.weights); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("WeightedSplit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSum(t *testing.T) {
	t.Parallel()
	type args struct {
		qosCCDMB map[task.QoSLevel]map[int]int
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "happy path",
			args: args{
				qosCCDMB: map[task.QoSLevel]map[int]int{
					"dedicated": {2: 100, 3: 100},
					"shared":    {0: 3, 1: 3, 4: 3, 5: 3},
					"relaimed":  {0: 1, 1: 1},
					"system":    {4: 2, 5: 2},
				},
			},
			want: 218,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Sum(tt.args.qosCCDMB); got != tt.want {
				t.Errorf("Sum() = %v, want %v", got, tt.want)
			}
		})
	}
}
