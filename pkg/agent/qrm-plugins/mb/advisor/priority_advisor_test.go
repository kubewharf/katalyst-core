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

package advisor

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/priority"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func Test_priorityGroupDecorator_combinedDomainStats(t *testing.T) {
	t.Parallel()
	priority.GetInstance().AddWeight("machine", 9_000)
	tests := []struct {
		name          string
		domainsMon    *monitor.DomainStats
		wantStats     *monitor.DomainStats
		wantGroupInfo *groupInfo
		wantErr       bool
	}{
		{
			name: "single group per weight - no combination",
			domainsMon: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
							1: {LocalMB: 3_000, RemoteMB: 2_000, TotalMB: 5_000},
						},
					},
					1: {
						"share-50": map[int]monitor.MBInfo{
							2: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
							1: {LocalMB: 3_000, RemoteMB: 2_000, TotalMB: 5_000},
						},
					},
					1: {
						"share-50": map[int]monitor.MBInfo{
							2: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"dedicated-60": {
						{LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
						{LocalMB: 0, RemoteMB: 0, TotalMB: 0},
					},
					"share-50": {
						{LocalMB: 0, RemoteMB: 0, TotalMB: 0},
						{LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
					},
				},
			},
			wantStats: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
							1: {LocalMB: 3_000, RemoteMB: 2_000, TotalMB: 5_000},
						},
					},
					1: {
						"share-50": map[int]monitor.MBInfo{
							2: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
							1: {LocalMB: 3_000, RemoteMB: 2_000, TotalMB: 5_000},
						},
					},
					1: {
						"share-50": map[int]monitor.MBInfo{
							2: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"dedicated-60": {
						{LocalMB: 5_000, RemoteMB: 5_000, TotalMB: 10_000},
						{LocalMB: 0, RemoteMB: 0, TotalMB: 0},
					},
					"share-50": {
						{LocalMB: 0, RemoteMB: 0, TotalMB: 0},
						{LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
					},
				},
			},
			wantGroupInfo: &groupInfo{
				DomainGroups: map[int]domainGroupMapping{
					0: {},
					1: {},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple groups with same weight - combine groups",
			domainsMon: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"dedicated": {
						{LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
					},
					"machine": {
						{LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
					},
				},
			},
			wantStats: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"combined-9000": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"combined-9000": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"combined-9000": {
						{LocalMB: 18_000, RemoteMB: 9_000, TotalMB: 27_000},
					},
				},
			},
			wantGroupInfo: &groupInfo{
				DomainGroups: map[int]domainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated": {0: struct{}{}},
							"machine":   {1: struct{}{}},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &priorityAdvisor{}
			gotStats, gotGroupInfo, err := d.combinedDomainStats(tt.domainsMon)
			if (err != nil) != tt.wantErr {
				t.Errorf("combinedDomainStats() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotStats, tt.wantStats) {
				t.Errorf("combinedDomainStats() gotStats = %v, want %v", gotStats, tt.wantStats)
			}
			if !reflect.DeepEqual(gotGroupInfo, tt.wantGroupInfo) {
				t.Errorf("combinedDomainStats() gotGroupInfo = %v, want %v", gotGroupInfo, tt.wantGroupInfo)
			}
		})
	}
}

func Test_priorityGroupDecorator_splitPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mbPlan     *plan.MBPlan
		groupInfos *groupInfo
		want       *plan.MBPlan
	}{
		{
			name: "no combined groups - no change",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000, 1: 4_000},
					"machine-60":   {2: 6_000, 3: 5_500},
				},
			},
			groupInfos: &groupInfo{
				DomainGroups: map[int]domainGroupMapping{
					0: {},
					1: {},
				},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000, 1: 4_000},
					"machine-60":   {2: 6_000, 3: 5_500},
				},
			},
		},
		{
			name: "single combined group - split to original groups",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000},
				},
			},
			groupInfos: &groupInfo{
				DomainGroups: map[int]domainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
						},
					},
				},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000},
					"machine-60":   {1: 4_000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &priorityAdvisor{}
			got := d.splitPlan(tt.mbPlan, tt.groupInfos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
