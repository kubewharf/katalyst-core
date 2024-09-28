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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type mockQoSPolicy struct {
	mock.Mock
	QoSMBPolicy
}

func (m *mockQoSPolicy) GetPlan(upperBoundMB int, groups map[task.QoSGroup]*monitor.MBQoSGroup, isTopTier bool) *plan.MBAlloc {
	args := m.Called(upperBoundMB, groups, isTopTier)
	return args.Get(0).(*plan.MBAlloc)
}

func Test_priorityChainedMBPolicy_GetPlan(t *testing.T) {
	t.Parallel()

	currPolicy := new(mockQoSPolicy)
	currPolicy.On("GetPlan", 120_000, map[task.QoSGroup]*monitor.MBQoSGroup{
		"dedicated": {CCDMB: map[int]*monitor.MBData{2: {ReadsMB: 15_000}, 3: {ReadsMB: 15_000}, 4: {ReadsMB: 20_000}, 5: {ReadsMB: 20_000}}},
		"shared_50": {CCDMB: map[int]*monitor.MBData{0: {ReadsMB: 7_000}, 1: {ReadsMB: 10_000}, 7: {ReadsMB: 5_000}}},
		"system":    {CCDMB: map[int]*monitor.MBData{0: {ReadsMB: 3_000}, 7: {ReadsMB: 5_000}}},
	}, true).Return(&plan.MBAlloc{Plan: map[task.QoSGroup]map[int]int{
		"dedicated": {2: 25_000, 3: 25_000, 4: 25_000, 5: 25_000},
		"shared_50": {0: 25_000, 1: 25_000, 7: 25_000},
		"system":    {0: 25_000, 7: 25000},
	}})

	nextPolicy := new(mockQoSPolicy)
	nextPolicy.On("GetPlan", 20_000, map[task.QoSGroup]*monitor.MBQoSGroup{
		"shared_30": {CCDMB: map[int]*monitor.MBData{6: {ReadsMB: 7_000}}},
	}, false).Return(&plan.MBAlloc{Plan: map[task.QoSGroup]map[int]int{
		"shared_30": {6: 20_000},
	}})

	type fields struct {
		topTiers map[task.QoSGroup]struct{}
		tier     QoSMBPolicy
		next     QoSMBPolicy
	}
	type args struct {
		totalMB   int
		groups    map[task.QoSGroup]*monitor.MBQoSGroup
		isTopTier bool
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
				topTiers: map[task.QoSGroup]struct{}{
					"dedicated": {},
					"shared_50": {},
					"system":    {},
				},
				tier: currPolicy,
				next: nextPolicy,
			},
			args: args{
				totalMB: 120_000,
				groups: map[task.QoSGroup]*monitor.MBQoSGroup{
					"dedicated": {CCDMB: map[int]*monitor.MBData{2: {ReadsMB: 15_000}, 3: {ReadsMB: 15_000}, 4: {ReadsMB: 20_000}, 5: {ReadsMB: 20_000}}},
					"shared_50": {CCDMB: map[int]*monitor.MBData{0: {ReadsMB: 7_000}, 1: {ReadsMB: 10_000}, 7: {ReadsMB: 5_000}}},
					"shared_30": {CCDMB: map[int]*monitor.MBData{6: {ReadsMB: 7_000}}},
					"system":    {CCDMB: map[int]*monitor.MBData{0: {ReadsMB: 3_000}, 7: {ReadsMB: 5_000}}},
				},
				isTopTier: true,
			},
			want: &plan.MBAlloc{
				Plan: map[task.QoSGroup]map[int]int{
					"dedicated": {2: 25_000, 3: 25_000, 4: 25_000, 5: 25_000},
					"shared_50": {0: 25_000, 1: 25_000, 7: 25_000},
					"shared_30": {6: 20_000},
					"system":    {0: 25_000, 7: 25_000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := chainedQosPolicy{
				currQoSLevels: tt.fields.topTiers,
				current:       tt.fields.tier,
				next:          tt.fields.next,
			}
			assert.Equalf(t, tt.want, p.GetPlan(tt.args.totalMB, tt.args.groups, tt.args.isTopTier), "GetPlan(%v, %v)", tt.args.totalMB, tt.args.groups)
		})
	}
}
