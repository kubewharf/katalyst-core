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

package qospolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

func Test_weightedQoSMBPolicy_GetPlan(t *testing.T) {
	t.Parallel()
	type args struct {
		totalMB   int
		currQoSMB map[task.QoSLevel]map[int]int
	}
	tests := []struct {
		name string
		args args
		want *plan.MBAlloc
	}{
		{
			name: "happy path",
			args: args{
				totalMB:   1_500,
				currQoSMB: mapQoSMB{"foo": {1: 200, 2: 200}, "bar": {1: 300, 4: 300}},
			},
			want: &plan.MBAlloc{
				Plan: mapQoSMB{"foo": {1: 300, 2: 300}, "bar": {1: 450, 4: 450}},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := weightedQoSMBPolicy{}
			assert.Equalf(t, tt.want, w.GetPlan(tt.args.totalMB, tt.args.currQoSMB), "GetPlan(%v, %v)", tt.args.totalMB, tt.args.currQoSMB)
		})
	}
}

func Test_weightedQoSMBPolicy_getProportionalPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		isTopLink bool
	}
	type args struct {
		totalMB   int
		currQoSMB map[task.QoSLevel]map[int]int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *plan.MBAlloc
	}{
		{
			name: "happy path",
			fields: fields{
				isTopLink: false,
			},
			args: args{
				totalMB: 1000,
				currQoSMB: map[task.QoSLevel]map[int]int{
					"foo": {1: 100, 2: 200},
					"bar": {1: 100, 2: 50, 3: 50},
				},
			},
			want: &plan.MBAlloc{Plan: mapQoSMB{
				"foo": {1: 200, 2: 400},
				"bar": {1: 200, 2: 100, 3: 100},
			}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &weightedQoSMBPolicy{
				isTopLink: tt.fields.isTopLink,
			}
			assert.Equalf(t, tt.want, w.getProportionalPlan(tt.args.totalMB, tt.args.currQoSMB), "getProportionalPlan(%v, %v)", tt.args.totalMB, tt.args.currQoSMB)
		})
	}
}

func Test_weightedQoSMBPolicy_getTopLevelPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		isTopLink bool
	}
	type args struct {
		totalMB   int
		currQoSMB map[task.QoSLevel]map[int]int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *plan.MBAlloc
	}{
		{
			name: "happy path",
			fields: fields{
				isTopLink: true,
			},
			args: args{
				totalMB:   1000,
				currQoSMB: mapQoSMB{"foo": {1: 100, 2: 100}},
			},
			want: &plan.MBAlloc{Plan: mapQoSMB{"foo": {1: 256_000, 2: 256_000}}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &weightedQoSMBPolicy{
				isTopLink: tt.fields.isTopLink,
			}
			assert.Equalf(t, tt.want, w.getTopLevelPlan(tt.args.totalMB, tt.args.currQoSMB), "getTopLevelPlan(%v, %v)", tt.args.totalMB, tt.args.currQoSMB)
		})
	}
}
