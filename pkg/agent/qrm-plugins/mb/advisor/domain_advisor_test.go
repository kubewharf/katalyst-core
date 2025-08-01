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

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/quota"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/sankey"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func Test_domainAdvisor_getEffectiveCapacity(t *testing.T) {
	t.Parallel()
	type fields struct {
		domains           domain.Domains
		XDomGroups        sets.String
		GroupCapacityInMB map[string]int
	}
	type args struct {
		domID         int
		incomingStats monitor.GroupMonStat
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "trivial base capacity",
			fields: fields{
				domains: domain.Domains{
					0: {
						ID:           0,
						CapacityInMB: 12_345,
					},
				},
			},
			args: args{
				domID:         0,
				incomingStats: monitor.GroupMonStat{},
			},
			want:    12_345,
			wantErr: false,
		},
		{
			name: "min of active settings takes effect",
			fields: fields{
				domains: domain.Domains{
					0: {
						ID:           0,
						CapacityInMB: 12_345,
					},
				},
				GroupCapacityInMB: map[string]int{"shared-60": 9_999, "shared-50": 11_111},
			},
			args: args{
				domID: 0,
				incomingStats: monitor.GroupMonStat{
					"shared-50": monitor.GroupCCDMB{0: {TotalMB: 5_555}},
					"shared-60": monitor.GroupCCDMB{1: {TotalMB: 2_222}},
				},
			},
			want:    9_999,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &domainAdvisor{
				domains:           tt.fields.domains,
				XDomGroups:        tt.fields.XDomGroups,
				GroupCapacityInMB: tt.fields.GroupCapacityInMB,
			}
			got, err := d.getEffectiveCapacity(tt.args.domID, tt.args.incomingStats)
			if (err != nil) != tt.wantErr {
				t.Errorf("getEffectiveCapacity() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getEffectiveCapacity() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_domainAdvisor_calcIncomingQuotas(t *testing.T) {
	t.Parallel()
	type fields struct {
		domains           domain.Domains
		XDomGroups        sets.String
		GroupCapacityInMB map[string]int
	}
	type args struct {
		ctx context.Context
		mon *monitor.DomainsMon
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[int]*resource.MBGroupIncomingStat
		wantErr bool
	}{
		{
			name: "happy path of no dynamic capacity",
			fields: fields{
				domains: domain.Domains{
					0: {
						ID:           0,
						CCDs:         sets.NewInt(0, 1),
						CapacityInMB: 10_000,
					},
				},
			},
			args: args{
				ctx: context.TODO(),
				mon: &monitor.DomainsMon{
					Incoming: map[int]monitor.GroupMonStat{
						0: {
							"shared-60": map[int]monitor.MBStat{
								1: {TotalMB: 2_200},
							},
							"shared-50": map[int]monitor.MBStat{
								0: {TotalMB: 5_500},
								1: {TotalMB: 1_100},
							},
						},
					},
				},
			},
			want: map[int]*resource.MBGroupIncomingStat{
				0: {
					CapacityInMB: 10_000,
					FreeInMB:     10_000 - 2_200 - 6_600,
					GroupSorted: []sets.String{
						sets.NewString("shared-60"),
						sets.NewString("shared-50"),
					},
					GroupTotalUses: map[string]int{
						"shared-60": 2_200,
						"shared-50": 5_500 + 1_100,
					},
					GroupLimits: map[string]int{
						"shared-60": 10_000,
						"shared-50": 10_000 - 2_200,
					},
					ResourceState: resource.State("fit"),
				},
			},
			wantErr: false,
		},
		{
			name: "dynamic capacity in force",
			fields: fields{
				domains: domain.Domains{
					0: {
						ID:           0,
						CCDs:         sets.NewInt(0, 1),
						CapacityInMB: 10_000,
					},
				},
				GroupCapacityInMB: map[string]int{
					"shared-60": 6_000,
				},
			},
			args: args{
				ctx: context.TODO(),
				mon: &monitor.DomainsMon{
					Incoming: map[int]monitor.GroupMonStat{
						0: {
							"shared-60": map[int]monitor.MBStat{
								1: {TotalMB: 2_200},
							},
							"shared-50": map[int]monitor.MBStat{
								0: {TotalMB: 5_500},
								1: {TotalMB: 1_100},
							},
						},
					},
				},
			},
			want: map[int]*resource.MBGroupIncomingStat{
				0: {
					CapacityInMB: 6_000,
					FreeInMB:     0,
					GroupSorted: []sets.String{
						sets.NewString("shared-60"),
						sets.NewString("shared-50"),
					},
					GroupTotalUses: map[string]int{
						"shared-60": 2_200,
						"shared-50": 5_500 + 1_100,
					},
					GroupLimits: map[string]int{
						"shared-60": 6_000,
						"shared-50": 6_000 - 2_200,
					},
					ResourceState: resource.State("underStress"),
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &domainAdvisor{
				domains:           tt.fields.domains,
				XDomGroups:        tt.fields.XDomGroups,
				GroupCapacityInMB: tt.fields.GroupCapacityInMB,
			}
			got, err := d.calcIncomingDomainStats(tt.args.ctx, tt.args.mon)
			if (err != nil) != tt.wantErr {
				t.Errorf("calcIncomingDomainStats() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calcIncomingDomainStats() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_domainAdvisor_GetPlan(t *testing.T) {
	t.Parallel()
	type fields struct {
		domains           domain.Domains
		XDomGroups        sets.String
		GroupCapacityInMB map[string]int
		quotaStrategy     quota.Decider
		flower            sankey.DomainFlower
	}
	type args struct {
		ctx        context.Context
		domainsMon *monitor.DomainsMon
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *plan.MBPlan
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				domains: domain.Domains{
					0: {
						ID:           0,
						CCDs:         nil,
						CapacityInMB: 30_000,
					},
					1: {
						ID:           1,
						CCDs:         nil,
						CapacityInMB: 30_000,
					},
				},
				XDomGroups:        nil,
				GroupCapacityInMB: nil,
				quotaStrategy:     quota.New(),
				flower:            sankey.New(),
			},
			args: args{
				ctx: context.TODO(),
				domainsMon: &monitor.DomainsMon{
					Incoming: map[int]monitor.GroupMonStat{
						0: {
							"shared-60": map[int]monitor.MBStat{
								0: {
									LocalMB:  10_000,
									RemoteMB: 2_000,
									TotalMB:  12_000,
								},
							},
							"shared-50": map[int]monitor.MBStat{
								1: {
									LocalMB:  11_000,
									RemoteMB: 2_500,
									TotalMB:  13_500,
								},
							},
						},
						1: {
							"shared-60": map[int]monitor.MBStat{
								0: {
									LocalMB:  10_000,
									RemoteMB: 2_000,
									TotalMB:  12_000,
								},
							},
							"shared-50": map[int]monitor.MBStat{
								1: {
									LocalMB:  11_000,
									RemoteMB: 2_500,
									TotalMB:  13_500,
								},
							},
						},
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &domainAdvisor{
				domains:           tt.fields.domains,
				XDomGroups:        tt.fields.XDomGroups,
				GroupCapacityInMB: tt.fields.GroupCapacityInMB,
				quotaStrategy:     tt.fields.quotaStrategy,
				flower:            tt.fields.flower,
				emitter:           &metrics.DummyMetrics{},
			}
			got, err := d.GetPlan(tt.args.ctx, tt.args.domainsMon)
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
