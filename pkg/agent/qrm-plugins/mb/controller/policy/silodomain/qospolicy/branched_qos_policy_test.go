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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_valveQoSMBPolicy_GetPlan(t *testing.T) {
	t.Parallel()

	upperPolicy := new(mockQoSPolicy)
	upperPolicy.On("GetPlan", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
			"upper": {},
		}})

	lowerPolicy := new(mockQoSPolicy)
	lowerPolicy.On("GetPlan", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
			"lower": {},
		}})

	type fields struct {
		either QoSMBPolicy
		or     QoSMBPolicy
		filter func(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup, isTopMost bool) bool
	}
	type args struct {
		totalMB     int
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
		isTopMost   bool
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *plan.MBAlloc
	}{
		{
			name: "happy path of upper",
			fields: fields{
				either: upperPolicy,
				or:     lowerPolicy,
				filter: func(_ map[qosgroup.QoSGroup]*stat.MBQoSGroup, _ bool) bool {
					return true
				},
			},
			args: args{},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"upper": {},
			}},
		},
		{
			name: "happy path of lower",
			fields: fields{
				either: upperPolicy,
				or:     lowerPolicy,
				filter: func(_ map[qosgroup.QoSGroup]*stat.MBQoSGroup, _ bool) bool {
					return false
				},
			},
			args: args{},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"lower": {},
			}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := branchedQoSPolicy{
				either: tt.fields.either,
				or:     tt.fields.or,
				filter: tt.fields.filter,
			}
			assert.Equalf(t, tt.want, v.GetPlan(tt.args.totalMB, tt.args.mbQoSGroups, nil, tt.args.isTopMost), "GetPlan(%v, %v, %v)", tt.args.totalMB, tt.args.mbQoSGroups, tt.args.isTopMost)
		})
	}
}
