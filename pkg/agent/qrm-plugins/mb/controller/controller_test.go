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

package controller

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

func TestController_getDedicatedNodes(t *testing.T) {
	t.Parallel()
	type fields struct {
		domainManager *mbdomain.MBDomainManager
		CurrQoSCCDMB  map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		want   sets.Int
	}{
		{
			name: "happy path",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{
							2: {
								TotalMB: 1_234,
							},
							3: {
								TotalMB: 1_333,
							},
						},
					},
				},
			},
			want: sets.Int{1: sets.Empty{}},
		},
		{
			name: "happy path of 0 traffic",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{
							6: {
								TotalMB: 0,
							},
						},
					},
				},
			},
			want: sets.Int{3: sets.Empty{}},
		},
		{
			name: "happy path of no dedicated qos",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					CCDNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
				},
				CurrQoSCCDMB: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					qosgroup.QoSGroupDedicated: {
						CCDMB: map[int]*stat.MBData{},
					},
				},
			},
			want: sets.Int{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Controller{
				DomainManager: tt.fields.domainManager,
				MBStat: &MBStatKeeper{
					currQoSCCDMB: tt.fields.CurrQoSCCDMB,
				},
			}
			if got := c.guessDedicatedNodesByCheckingActiveMBStat(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("guessDedicatedNodesByCheckingActiveMBStat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_globalMBPolicy_adjustSocketCCDMB(t *testing.T) {
	t.Parallel()
	type fields struct {
		domainManager *mbdomain.MBDomainManager
	}
	type args struct {
		mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}{
		{
			name: "0 admission 0 incubation",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID:        0,
							NumaNodes: []int{0, 1, 2, 3},
							NodeCCDs: map[int][]int{
								0: []int{0, 1},
								1: []int{2, 3},
								2: []int{4, 5},
								3: []int{6, 7},
							},
							PreemptyNodes: sets.Int{},
							MBQuota:       0,
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDMB: map[int]*stat.MBData{
						6: {TotalMB: 6_000, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
			},
		},
		{
			name: "having incubation",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID: 0,
							CCDIncubateds: map[int]time.Time{
								6: time.Now().Add(time.Minute),
							},
							NumaNodes: []int{0, 1, 2, 3},
							NodeCCDs: map[int][]int{
								0: []int{0, 1},
								1: []int{2, 3},
								2: []int{4, 5},
								3: []int{6, 7},
							},
							PreemptyNodes: sets.Int{},
							MBQuota:       0,
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						6: {TotalMB: 17_500, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
			},
		},
		{
			name: "having admission",
			fields: fields{
				domainManager: &mbdomain.MBDomainManager{
					Domains: map[int]*mbdomain.MBDomain{
						0: {
							ID: 0,
							CCDIncubateds: map[int]time.Time{
								2: time.Now().Add(time.Minute),
								3: time.Now().Add(time.Minute),
							},
							PreemptyNodes: sets.Int{1: sets.Empty{}},
						},
					},
				},
			},
			args: args{
				mbQoSGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"dedicated": {
						CCDs: sets.Int{6: sets.Empty{}, 7: sets.Empty{}},
						CCDMB: map[int]*stat.MBData{
							6: {TotalMB: 6_000, LocalTotalMB: 1_006},
							7: {TotalMB: 7_000, LocalTotalMB: 1_007},
						},
					},
				},
			},
			want: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
				"dedicated": {
					CCDs: sets.Int{2: sets.Empty{}, 3: sets.Empty{}, 6: sets.Empty{}, 7: sets.Empty{}},
					CCDMB: map[int]*stat.MBData{
						2: {TotalMB: 17_500},
						3: {TotalMB: 17_500},
						6: {TotalMB: 6_000, LocalTotalMB: 1_006},
						7: {TotalMB: 7_000, LocalTotalMB: 1_007},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &Controller{
				DomainManager: tt.fields.domainManager,
			}
			if got := g.adjustSocketCCDMBWithIncubates(tt.args.mbQoSGroups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("adjustSocketCCDMBWithIncubates() = %v, want %v", got, tt.want)
			}
		})
	}
}
