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
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/priority"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

func TestPriorityAdvisorKeepsSharedAndSharePhysicalGroupsOnSameCCD(t *testing.T) {
	t.Parallel()

	domainsMon := &monitor.DomainStats{
		Incomings: map[int]monitor.DomainMonStat{
			0: {
				"shared-50": {0: {TotalMB: 4_000}},
				"share-50":  {0: {TotalMB: 2_000}},
			},
		},
		Outgoings: map[int]monitor.DomainMonStat{
			0: {
				"shared-50": {0: {TotalMB: 4_000}},
				"share-50":  {0: {TotalMB: 2_000}},
			},
		},
		OutgoingGroupSumStat: map[string][]monitor.MBInfo{
			"shared-50": {{TotalMB: 4_000}},
			"share-50":  {{TotalMB: 2_000}},
		},
	}

	advisor := &priorityAdvisor{
		uniqPriorityAdvisor: newUniqPriorityAdvisor(
			&metrics.DummyMetrics{},
			domain.Domains{0: domain.NewDomain(0, sets.NewInt(0), 20_000)},
			0,
			20_000,
			20_000,
			100,
			nil,
			nil,
			nil,
		),
	}

	combinedStats, groupInfos, err := advisor.combinedDomainStats(domainsMon)
	require.NoError(t, err)
	assert.Equal(t, domainsMon, combinedStats)
	assert.Equal(t, &groupInfo{DomainGroups: map[int]domainGroupMapping{0: {}}}, groupInfos)

	gotPlan, err := advisor.GetPlan(context.Background(), domainsMon)
	require.NoError(t, err)
	assert.Contains(t, gotPlan.MBGroups, "shared-50")
	assert.Contains(t, gotPlan.MBGroups, "share-50")
	// both groups must be placed on CCD 0 (the only available CCD).
	assert.Contains(t, gotPlan.MBGroups["shared-50"], 0)
	assert.Contains(t, gotPlan.MBGroups["share-50"], 0)
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

func Test_priorityAdvisor_getDomainQuotaSuppression(t *testing.T) {
	t.Parallel()
	type fields struct {
		uniqPriorityAdvisor *uniqPriorityAdvisor
	}
	type args struct {
		groupInfos *groupInfo
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int]map[string]map[int]string
	}{
		{
			name: "happy path",
			fields: fields{
				uniqPriorityAdvisor: &uniqPriorityAdvisor{
					lastQuotaSuppressedCCDs: map[int]map[string][]int{
						1: {"combined-9000": {9, 27}},
					},
				},
			},
			args: args{
				groupInfos: &groupInfo{
					DomainGroups: map[int]domainGroupMapping{
						1: map[string]combinedGroupMapping{
							"combined-9000": map[string]ccdSet{
								"dedicated": sets.NewInt(27),
								"machine":   sets.NewInt(9),
							},
						},
					},
				},
			},
			want: map[int]map[string]map[int]string{
				1: {
					"dedicated": {27: "domain_stress"},
					"machine":   {9: "domain_stress"},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &priorityAdvisor{
				uniqPriorityAdvisor: tt.fields.uniqPriorityAdvisor,
			}
			assert.Equalf(t, tt.want, a.getDomainQuotaSuppression(tt.args.groupInfos), "getDomainQuotaSuppression(%v)", tt.args.groupInfos)
		})
	}
}
