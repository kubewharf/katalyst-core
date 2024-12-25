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

package mbdomain

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNewMBDomainManager(t *testing.T) {
	t.Parallel()

	type args struct {
		dieTopology *machine.DieTopology
	}
	tests := []struct {
		name string
		args args
		want *MBDomainManager
	}{
		{
			name: "happy path",
			args: args{
				dieTopology: &machine.DieTopology{
					Packages:       1,
					NUMAsInPackage: map[int][]int{0: {0, 1, 2, 3}},
					DiesInNuma: map[int]sets.Int{
						0: {0: sets.Empty{}, 1: sets.Empty{}},
						1: {2: sets.Empty{}, 3: sets.Empty{}},
						2: {4: sets.Empty{}, 5: sets.Empty{}},
						3: {6: sets.Empty{}, 7: sets.Empty{}},
					},
				},
			},
			want: &MBDomainManager{
				Domains: map[int]*MBDomain{
					0: {
						ID:        0,
						NumaNodes: []int{0, 1, 2, 3},
						CCDNode: map[int]int{
							0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3,
						},
						NodeCCDs: map[int][]int{
							0: {0, 1},
							1: {2, 3},
							2: {4, 5},
							3: {6, 7},
						},
						CCDs:               []int{0, 1, 2, 3, 4, 5, 6, 7},
						PreemptyNodes:      make(sets.Int),
						ccdIncubated:       IncubatedCCDs{},
						incubationInterval: time.Second * 1,
						MBQuota:            120_000_000,
					},
				},
				nodeCCDs: map[int]sets.Int{
					0: {0: sets.Empty{}, 1: sets.Empty{}},
					1: {2: sets.Empty{}, 3: sets.Empty{}},
					2: {4: sets.Empty{}, 5: sets.Empty{}},
					3: {6: sets.Empty{}, 7: sets.Empty{}},
				},
				CCDNode: map[int]int{
					0: 0,
					1: 0,
					2: 1,
					3: 1,
					4: 2,
					5: 2,
					6: 3,
					7: 3,
				},
				CCDDomain: map[int]int{
					0: 0,
					1: 0,
					2: 0,
					3: 0,
					4: 0,
					5: 0,
					6: 0,
					7: 0,
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NewMBDomainManager(tt.args.dieTopology, time.Second*1, 120_000_000)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMBDomainManager_PreemptNodes(t *testing.T) {
	t.Parallel()
	type fields struct {
		Domains map[int]*MBDomain
	}
	type args struct {
		nodes []int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "happy path of change",
			fields: fields{
				Domains: map[int]*MBDomain{
					0: {
						ID: 0,
						NodeCCDs: map[int][]int{
							0: {0, 1},
							1: {2, 3},
							2: {4, 5},
							3: {6, 7},
						},
						PreemptyNodes: sets.Int{},
					},
				},
			},
			args: args{
				nodes: []int{2},
			},
			want: true,
		},
		{
			name: "happy path of no change",
			fields: fields{
				Domains: map[int]*MBDomain{
					0: {
						ID: 0,
						NodeCCDs: map[int][]int{
							0: {0, 1},
							1: {2, 3},
							2: {4, 5},
							3: {6, 7},
						},
						PreemptyNodes: sets.Int{2: sets.Empty{}},
					},
				},
			},
			args: args{
				nodes: []int{2},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := MBDomainManager{
				Domains: tt.fields.Domains,
			}
			assert.Equalf(t, tt.want, m.PreemptNodes(tt.args.nodes), "PreemptNodes(%v)", tt.args.nodes)
		})
	}
}

func TestMBDomainManager_GetNode(t *testing.T) {
	t.Parallel()
	type fields struct {
		Domains  map[int]*MBDomain
		nodeCCDs map[int]sets.Int
		CcdNode  map[int]int
	}
	type args struct {
		ccd int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			fields: fields{
				CcdNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3, 8: 4, 9: 4, 10: 5, 11: 5},
			},
			args: args{
				ccd: 7,
			},
			want:    3,
			wantErr: assert.NoError,
		},
		{
			name: "not found error",
			fields: fields{
				CcdNode: map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3, 8: 4, 9: 4, 10: 5, 11: 5},
			},
			args: args{
				ccd: 20,
			},
			want:    -1,
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := MBDomainManager{
				Domains:  tt.fields.Domains,
				nodeCCDs: tt.fields.nodeCCDs,
				CCDNode:  tt.fields.CcdNode,
			}
			got, err := m.GetNode(tt.args.ccd)
			if !tt.wantErr(t, err, fmt.Sprintf("GetNode(%v)", tt.args.ccd)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetNode(%v)", tt.args.ccd)
		})
	}
}

func Test_genCCDNode(t *testing.T) {
	t.Parallel()
	type args struct {
		nodeCCDs map[int]sets.Int
	}
	tests := []struct {
		name string
		args args
		want map[int]int
	}{
		{
			name: "happy path",
			args: args{
				nodeCCDs: map[int]sets.Int{
					1: {2: sets.Empty{}, 3: sets.Empty{}},
					6: {13: sets.Empty{}},
				},
			},
			want: map[int]int{
				2:  1,
				3:  1,
				13: 6,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, genCCDNode(tt.args.nodeCCDs), "genCCDNode(%v)", tt.args.nodeCCDs)
		})
	}
}

func TestMBDomainManager_SumGroupMBByDomainRecipient(t *testing.T) {
	t.Parallel()
	type fields struct {
		CCDDomain map[int]int
	}
	type args struct {
		mbGroup *stat.MBQoSGroup
	}
	tests := []struct {
		name             string
		fields           fields
		args             args
		wantDomainMBStat map[int]*rmbtype.MBStat
	}{
		{
			name: "happy path",
			fields: fields{
				CCDDomain: map[int]int{
					0: 1, 1: 1,
					5: 0, 7: 0,
				},
			},
			args: args{
				mbGroup: &stat.MBQoSGroup{
					CCDMB: map[int]*stat.MBData{
						0: {TotalMB: 10_000, LocalTotalMB: 9_000},
						5: {TotalMB: 11_000, LocalTotalMB: 6_000},
						7: {TotalMB: 13_000, LocalTotalMB: 3_000},
						1: {TotalMB: 8_000, LocalTotalMB: 1_234},
					},
				},
			},
			wantDomainMBStat: map[int]*rmbtype.MBStat{
				1: {Total: 9_000 + 1_234 + 5_000 + 10_000, Local: 9_000 + 1_234},
				0: {Total: 6_000 + 3_000 + 1_000 + (8_000 - 1_234), Local: 6_000 + 3_000},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MBDomainManager{
				CCDDomain: tt.fields.CCDDomain,
			}
			assert.Equalf(t, tt.wantDomainMBStat, m.sumGroupMBByDomainRecipient(tt.args.mbGroup), "sumGroupMBByDomainRecipient(%v)", tt.args.mbGroup)
		})
	}
}

func TestMBDomainManager_SumQoSMBByDomainRecipient(t *testing.T) {
	t.Parallel()
	type fields struct {
		CCDDomain map[int]int
	}
	type args struct {
		qosMBGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup
	}
	tests := []struct {
		name            string
		fields          fields
		args            args
		wantDomainQoSMB map[int]map[qosgroup.QoSGroup]rmbtype.MBStat
	}{
		{
			name: "happy path",
			fields: fields{
				CCDDomain: map[int]int{
					0: 0, 3: 0,
					9: 1, 14: 1,
				},
			},
			args: args{
				qosMBGroups: map[qosgroup.QoSGroup]*stat.MBQoSGroup{
					"shared-30": {
						CCDMB: map[int]*stat.MBData{
							0:  {TotalMB: 5_000, LocalTotalMB: 2_333},
							3:  {TotalMB: 4_000, LocalTotalMB: 1_111},
							9:  {TotalMB: 7_000, LocalTotalMB: 3_000},
							14: {TotalMB: 2_000, LocalTotalMB: 555},
						},
					},
				},
			},
			wantDomainQoSMB: map[int]map[qosgroup.QoSGroup]rmbtype.MBStat{
				0: {
					"shared-30": {
						Total: 2_333 + 1_111 + 4_000 + (2000 - 555),
						Local: 2_333 + 1_111,
					},
				},
				1: {
					"shared-30": {
						Total: 3_000 + 555 + (5_000 - 2_333) + (4_000 - 1_111),
						Local: 3_000 + 555,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MBDomainManager{
				CCDDomain: tt.fields.CCDDomain,
			}
			assert.Equalf(t, tt.wantDomainQoSMB, m.SumQoSMBByDomainRecipient(tt.args.qosMBGroups), "SumQoSMBByDomainRecipient(%v)", tt.args.qosMBGroups)
		})
	}
}
