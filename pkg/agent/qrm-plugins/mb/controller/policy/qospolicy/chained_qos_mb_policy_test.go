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
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type mockBoundedPolicy struct {
	mock.Mock
	QoSMBPolicy
}

func (m *mockBoundedPolicy) GetPlan(upperBoundMB int, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	args := m.Called(upperBoundMB, currQoSMB)
	return args.Get(0).(*plan.MBAlloc)
}

func Test_priorityChainedMBPolicy_GetPlan(t *testing.T) {
	t.Parallel()

	weightedPolicy := new(mockBoundedPolicy)
	weightedPolicy.On("GetPlan", 46341, mapQoSMB{
		"dedicated_cores": {12: 10_000, 13: 10_000},
	}).Return(&plan.MBAlloc{Plan: map[task.QoSLevel]map[int]int{
		"dedicated_cores": {12: 25_000, 13: 14_414},
	}})

	nextPolicy := new(mockBoundedPolicy)
	nextPolicy.On("GetPlan", 48659, mapQoSMB{
		"shared_cores":    {8: 8_000, 9: 8_000},
		"reclaimed_cores": {8: 1_000, 9: 1_000},
		"system_cores":    {9: 3_000},
	}).Return(&plan.MBAlloc{Plan: map[task.QoSLevel]map[int]int{
		"shared_cores":    {8: 20_000, 9: 20_000},
		"reclaimed_cores": {8: 1_000, 9: 1_000},
		"system_cores":    {9: 3_000},
	}})

	type fields struct {
		topTiers map[task.QoSLevel]struct{}
		tier     QoSMBPolicy
		next     QoSMBPolicy
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
				topTiers: map[task.QoSLevel]struct{}{"dedicated_cores": {}},
				tier:     weightedPolicy,
				next:     nextPolicy,
			},
			args: args{
				totalMB: 95_000,
				currQoSMB: mapQoSMB{
					"dedicated_cores": {12: 10_000, 13: 10_000},
					"shared_cores":    {8: 8_000, 9: 8_000},
					"reclaimed_cores": {8: 1_000, 9: 1_000},
					"system_cores":    {9: 3_000},
				},
			},
			want: &plan.MBAlloc{
				Plan: map[consts.QoSLevel]map[int]int{
					"dedicated_cores": {12: 25_000, 13: 14_414},
					"shared_cores":    {8: 20_000, 9: 20_000},
					"reclaimed_cores": {8: 1_000, 9: 1_000},
					"system_cores":    {9: 3_000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := priorityChainedMBPolicy{
				qosTier: tt.fields.topTiers,
				tier:    tt.fields.tier,
				next:    tt.fields.next,
			}
			assert.Equalf(t, tt.want, p.GetPlan(tt.args.totalMB, tt.args.currQoSMB), "GetPlan(%v, %v)", tt.args.totalMB, tt.args.currQoSMB)
		})
	}
}
