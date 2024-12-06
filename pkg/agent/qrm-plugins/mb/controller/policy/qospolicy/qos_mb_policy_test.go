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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestBuildHiPrioDetectedQoSMBPolicy(t *testing.T) {
	t.Parallel()
	smartPolicy := BuildHiPrioDetectedQoSMBPolicy()

	type args struct {
		totalMB     int
		mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup
		isTopMost   bool
	}

	tests := []struct {
		name string
		args args
		want map[qosgroup.QoSGroup]map[int]int
	}{
		{
			name: "no high priority groups, no limit on shared-30",
			args: args{
				totalMB: 120_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"system": {
						CCDs:  sets.Int{1: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{1: {TotalMB: 100}},
					},
					"shared-30": {
						CCDs:  sets.Int{2: sets.Empty{}, 3: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{2: {TotalMB: 100}, 3: {TotalMB: 100}},
					},
				},
				isTopMost: true,
			},
			want: map[qosgroup.QoSGroup]map[int]int{
				"system":    {1: 30_000},
				"shared-30": {2: 30_000, 3: 30_000},
			},
		},
		{
			name: "yes shared-50 - shared-30 being limited",
			args: args{
				totalMB: 120_000,
				mbQoSGroups: map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
					"shared-50": {
						CCDs:  sets.Int{1: sets.Empty{}, 4: sets.Empty{}, 5: sets.Empty{}, 6: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{1: {TotalMB: 20_000}, 4: {TotalMB: 20_000}, 5: {TotalMB: 20_000}, 6: {TotalMB: 20_000}},
					},
					"system": {
						CCDs:  sets.Int{1: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{1: {TotalMB: 20_000}},
					},
					"shared-30": {
						CCDs:  sets.Int{2: sets.Empty{}, 3: sets.Empty{}},
						CCDMB: map[int]*monitor.MBData{2: {TotalMB: 100}, 3: {TotalMB: 100}},
					},
				},
				isTopMost: true,
			},
			want: map[qosgroup.QoSGroup]map[int]int{
				"shared-50": {1: 30_000, 4: 30_000, 5: 30_000, 6: 30_000},
				"system":    {1: 30_000},
				"shared-30": {2: 10_000, 3: 10_000},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, smartPolicy.GetPlan(tt.args.totalMB, tt.args.mbQoSGroups, tt.args.isTopMost).Plan,
				"getTopMostPlan(%v, %v)", tt.args.totalMB, tt.args.mbQoSGroups)
		})
	}
}
