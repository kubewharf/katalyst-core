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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func Test_getTopMostPlan(t *testing.T) {
	t.Parallel()
	type args struct {
		totalMB     int
		mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name string
		args args
		want *plan.MBAlloc
	}{
		{
			name: "happy path",
			args: args{
				totalMB: 1_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"dedicated": {CCDs: sets.Int{4: sets.Empty{}, 5: sets.Empty{}}},
					"shared-50": {CCDs: sets.Int{0: sets.Empty{}, 1: sets.Empty{}}},
					"system":    {CCDs: sets.Int{1: sets.Empty{}}},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"dedicated": {4: 25_000, 5: 25_000},
				"shared-50": {0: 25_000, 1: 25_000},
				"system":    {1: 25_000},
			}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, getTopMostPlan(tt.args.totalMB, tt.args.mbQoSGroups), "getTopMostPlan(%v, %v)", tt.args.totalMB, tt.args.mbQoSGroups)
		})
	}
}

func Test_getProportionalPlan(t *testing.T) {
	t.Parallel()
	type args struct {
		total       int
		mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup
	}
	tests := []struct {
		name string
		args args
		want *plan.MBAlloc
	}{
		{
			name: "happy path",
			args: args{
				total: 21_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"shared-30": {CCDMB: map[int]*monitor.MBData{
						6: {TotalMB: 20_000},
						7: {TotalMB: 10_000},
					}},
				},
			},
			want: &plan.MBAlloc{Plan: map[qosgroup.QoSGroup]map[int]int{
				"shared-30": {
					6: 14_000,
					7: 8_000,
				},
			}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, getLeafPlan(tt.args.total, tt.args.mbQoSGroups), "getProportionalPlan(%v, %v)", tt.args.total, tt.args.mbQoSGroups)
		})
	}
}
