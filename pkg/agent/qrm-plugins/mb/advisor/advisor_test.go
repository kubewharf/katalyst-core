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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/adjuster"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/distributor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/quota"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/sankey"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"k8s.io/apimachinery/pkg/util/sets"
)

func Test_EnhancedAdvisor_GetPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		domains               domain.Domains
		defaultDomainCapacity int
		XDomGroups            sets.String
		GroupCapacityInMB     map[string]int
		quotaStrategy         quota.Decider
		flower                sankey.DomainFlower
		adjusters             map[string]adjuster.Adjuster
	}
	type args struct {
		ctx        context.Context
		domainsMon *monitor.DomainStats
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *plan.MBPlan
		wantErr bool
	}{
		{
			name: "same priority with not enough capacity for two and a higher priority",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"/-100": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"/-100": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"/-100": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"/-100": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
						},
						"/-100": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 4250, 3: 4250},
				"machine-60":   {1: 4250, 4: 4250},
				"/-100":        {2: 20_000, 5: 20_000},
			}},
			wantErr: false,
		},
		{
			name: "same priority with not enough capacity for two and a lower priority",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 10_000,
								TotalMB:  20_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 0,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 0,
								TotalMB:  10_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 14_250, 3: 14_250},
				"machine-60":   {1: 14_250, 4: 14_250},
				"share-50":     {2: 0, 5: 0},
			}},
			wantErr: false,
		},
		{
			name: "same priority with not enough capacity for one and a lower priority",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  10_000,
									RemoteMB: 10_000,
									TotalMB:  20_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  20_000,
									RemoteMB: 20_000,
									TotalMB:  40_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 0,
									TotalMB:  10_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  20_000,
								RemoteMB: 20_000,
								TotalMB:  40_000,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 20_000,
								TotalMB:  20_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  20_000,
								RemoteMB: 20_000,
								TotalMB:  40_000,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 20_000,
								TotalMB:  40_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 0,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 0,
								TotalMB:  10_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 14_250, 3: 14_250},
				"machine-60":   {1: 14_250, 4: 14_250},
				"share-50":     {2: 0, 5: 0},
			}},
			wantErr: false,
		},
		{
			name: "same priority with partial enough capacity and a lower priority",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 5_000,
									TotalMB:  15_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 5_000,
									TotalMB:  15_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  10_000,
									RemoteMB: 5_000,
									TotalMB:  15_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  10_000,
									RemoteMB: 5_000,
									TotalMB:  15_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 5_000,
								TotalMB:  15_000,
							},
							1: {
								LocalMB:  10_000,
								RemoteMB: 5_000,
								TotalMB:  15_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 15_000, 3: 15_000},
				"machine-60":   {1: 15_000, 4: 15_000},
				"share-50":     {2: 8500, 5: 8500},
			}},
			wantErr: false,
		},
		{
			name: "same priority with fully enough capacity and a lower priority",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 5_000,
								TotalMB:  10_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 15_000, 3: 15_000},
				"machine-60":   {1: 15_000, 4: 15_000},
				"share-50":     {2: 10_000, 5: 10_000},
			}},
			wantErr: false,
		},
		{
			name: "same priority with shared ccd",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								1: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								4: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								1: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								4: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  500,
									RemoteMB: 500,
									TotalMB:  1000,
								},
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  5_500,
								RemoteMB: 5_500,
								TotalMB:  11_000,
							},
							1: {
								LocalMB:  5_500,
								RemoteMB: 5_500,
								TotalMB:  11_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  5_500,
								RemoteMB: 5_500,
								TotalMB:  11_000,
							},
							1: {
								LocalMB:  5_500,
								RemoteMB: 5_500,
								TotalMB:  11_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
						},
					},
				},
			},
			want: &plan.MBPlan{MBGroups: map[string]plan.GroupCCDPlan{
				"dedicated-60": {0: 15_000, 3: 15_000},
				"machine-60":   {1: 15_000, 4: 15_000},
				"share-50":     {2: 10_000, 5: 10_000},
			}},
			wantErr: false,
		},
		{
			name: "invalid incomings with shared ccd",
			fields: fields{
				domains: domain.Domains{
					0: domain.NewDomain(0, sets.NewInt(0, 1, 2), 88888),
					1: domain.NewDomain(1, sets.NewInt(3, 4, 5), 88888),
				},
				defaultDomainCapacity: 30_000,
				XDomGroups:            nil,
				GroupCapacityInMB:     nil,
				quotaStrategy:         quota.New(),
				flower:                sankey.New(),
				adjusters:             map[string]adjuster.Adjuster{},
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainStats{
					Incomings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								1: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								4: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					Outgoings: map[int]monitor.GroupMBStats{
						0: {
							"dedicated-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								1: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								0: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
								1: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								2: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
						1: {
							"dedicated-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
								4: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
							},
							"machine-60": map[int]monitor.MBInfo{
								3: {
									LocalMB:  4_000,
									RemoteMB: 5_000,
									TotalMB:  9_000,
								},
								4: {
									LocalMB:  5_000,
									RemoteMB: 5_000,
									TotalMB:  10_000,
								},
							},
							"share-50": map[int]monitor.MBInfo{
								5: {
									LocalMB:  5_000,
									RemoteMB: 0,
									TotalMB:  5_000,
								},
							},
						},
					},
					OutgoingGroupSumStat: map[string][]monitor.MBInfo{
						"dedicated-60": {
							0: {
								LocalMB:  9_000,
								RemoteMB: 10_000,
								TotalMB:  19_000,
							},
							1: {
								LocalMB:  9_000,
								RemoteMB: 10_000,
								TotalMB:  19_000,
							},
						},
						"machine-60": {
							0: {
								LocalMB:  9_000,
								RemoteMB: 10_000,
								TotalMB:  19_000,
							},
							1: {
								LocalMB:  9_000,
								RemoteMB: 10_000,
								TotalMB:  19_000,
							},
						},
						"share-50": {
							0: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
							1: {
								LocalMB:  5_000,
								RemoteMB: 0,
								TotalMB:  5_000,
							},
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &domainAdvisor{
				domains:               tt.fields.domains,
				defaultDomainCapacity: tt.fields.defaultDomainCapacity,
				capPercent:            100,
				xDomGroups:            tt.fields.XDomGroups,
				groupCapacityInMB:     tt.fields.GroupCapacityInMB,
				quotaStrategy:         tt.fields.quotaStrategy,
				flower:                tt.fields.flower,
				adjusters:             tt.fields.adjusters,
				ccdDistribute:         distributor.New(0, 20_000),
				emitter:               &metrics.DummyMetrics{},
			}
			advisor := &EnhancedAdvisor{inner: d}
			got, err := advisor.GetPlan(tt.args.ctx, tt.args.domainsMon)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetPlan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPlan() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_EnhancedAdvisor_combinedDomainStats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		domainsMon    *monitor.DomainStats
		wantStats     *monitor.DomainStats
		wantGroupInfo *monitor.GroupInfo
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
			wantGroupInfo: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
			wantGroupInfo: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
		{
			name: "multiple domains with same weight groups",
			domainsMon: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
					1: {
						"dedicated-60": map[int]monitor.MBInfo{
							2: {LocalMB: 12_000, RemoteMB: 6_000, TotalMB: 18_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							3: {LocalMB: 7_000, RemoteMB: 3_000, TotalMB: 10_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
					},
					1: {
						"dedicated-60": map[int]monitor.MBInfo{
							2: {LocalMB: 12_000, RemoteMB: 6_000, TotalMB: 18_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							3: {LocalMB: 7_000, RemoteMB: 3_000, TotalMB: 10_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"dedicated-60": {
						{LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						{LocalMB: 12_000, RemoteMB: 6_000, TotalMB: 18_000},
					},
					"machine-60": {
						{LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						{LocalMB: 7_000, RemoteMB: 3_000, TotalMB: 10_000},
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
					1: {
						"combined-9000": map[int]monitor.MBInfo{
							2: {LocalMB: 12_000, RemoteMB: 6_000, TotalMB: 18_000},
							3: {LocalMB: 7_000, RemoteMB: 3_000, TotalMB: 10_000},
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
					1: {
						"combined-9000": map[int]monitor.MBInfo{
							2: {LocalMB: 12_000, RemoteMB: 6_000, TotalMB: 18_000},
							3: {LocalMB: 7_000, RemoteMB: 3_000, TotalMB: 10_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"combined-9000": {
						{LocalMB: 18_000, RemoteMB: 9_000, TotalMB: 27_000},
						{LocalMB: 19_000, RemoteMB: 9_000, TotalMB: 28_000},
					},
				},
			},
			wantGroupInfo: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
						},
					},
					1: {
						"combined-9000": {
							"dedicated-60": {2: struct{}{}},
							"machine-60":   {3: struct{}{}},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "three groups with same weight",
			domainsMon: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
						"system-60": map[int]monitor.MBInfo{
							2: {LocalMB: 6_000, RemoteMB: 3_000, TotalMB: 9_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"dedicated-60": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
						},
						"machine-60": map[int]monitor.MBInfo{
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
						},
						"system-60": map[int]monitor.MBInfo{
							2: {LocalMB: 6_000, RemoteMB: 3_000, TotalMB: 9_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"dedicated-60": {
						{LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
					},
					"machine-60": {
						{LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
					},
					"system-60": {
						{LocalMB: 6_000, RemoteMB: 3_000, TotalMB: 9_000},
					},
				},
			},
			wantStats: &monitor.DomainStats{
				Incomings: map[int]monitor.GroupMBStats{
					0: {
						"combined-9000": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
							2: {LocalMB: 6_000, RemoteMB: 3_000, TotalMB: 9_000},
						},
					},
				},
				Outgoings: map[int]monitor.GroupMBStats{
					0: {
						"combined-9000": map[int]monitor.MBInfo{
							0: {LocalMB: 10_000, RemoteMB: 5_000, TotalMB: 15_000},
							1: {LocalMB: 8_000, RemoteMB: 4_000, TotalMB: 12_000},
							2: {LocalMB: 6_000, RemoteMB: 3_000, TotalMB: 9_000},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]monitor.MBInfo{
					"combined-9000": {
						{LocalMB: 24_000, RemoteMB: 12_000, TotalMB: 36_000},
					},
				},
			},
			wantGroupInfo: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
							"system-60":    {2: struct{}{}},
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
			d := &EnhancedAdvisor{}
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

func Test_EnhancedAdvisor_splitPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mbPlan     *plan.MBPlan
		groupInfos *monitor.GroupInfo
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
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
		{
			name: "mixed combined and non-combined groups",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000},
					"share-50":      {2: 8_000, 3: 7_000},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
					"share-50":     {2: 8_000, 3: 7_000},
				},
			},
		},
		{
			name: "multiple combined groups in different domains",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000, 2: 6_000, 3: 5_500},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
						},
					},
					1: {
						"combined-9000": {
							"dedicated-60": {2: struct{}{}},
							"machine-60":   {3: struct{}{}},
						},
					},
				},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000, 2: 6_000},
					"machine-60":   {1: 4_000, 3: 5_500},
				},
			},
		},
		{
			name: "combined group with partial CCD match",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000, 2: 3_000},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
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
		{
			name: "multiple combined groups with different priorities",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000},
					"combined-1050": {2: 8_000, 3: 7_000},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
						},
						"combined-1050": {
							"share-50":     {2: struct{}{}},
							"share-alt-50": {3: struct{}{}},
						},
					},
				},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000},
					"machine-60":   {1: 4_000},
					"share-50":     {2: 8_000},
					"share-alt-50": {3: 7_000},
				},
			},
		},
		{
			name: "empty group info - remove combined groups",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{},
			},
		},
		{
			name: "three groups combined and split",
			mbPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"combined-9000": {0: 5_000, 1: 4_000, 2: 3_500},
				},
			},
			groupInfos: &monitor.GroupInfo{
				DomainGroups: map[int]monitor.DomainGroupMapping{
					0: {
						"combined-9000": {
							"dedicated-60": {0: struct{}{}},
							"machine-60":   {1: struct{}{}},
							"system-60":    {2: struct{}{}},
						},
					},
				},
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated-60": {0: 5_000},
					"machine-60":   {1: 4_000},
					"system-60":    {2: 3_500},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &EnhancedAdvisor{}
			got := d.splitPlan(tt.mbPlan, tt.groupInfos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitPlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
