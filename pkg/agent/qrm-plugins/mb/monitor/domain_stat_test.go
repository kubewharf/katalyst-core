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

package monitor

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

func TestNewDomainsMon(t *testing.T) {
	t.Parallel()
	type args struct {
		statOutgoing GroupMBStats
		ccdToDomain  map[int]int
		xdGroups     sets.String
	}
	tests := []struct {
		name    string
		args    args
		want    *DomainStats
		wantErr bool
	}{
		{
			name: "simple case of one domain",
			args: args{
				statOutgoing: GroupMBStats{
					"dedicated": {
						0: {
							LocalMB:  1,
							RemoteMB: 2,
							TotalMB:  3,
						},
					},
				},
				ccdToDomain: map[int]int{0: 0},
			},
			want: &DomainStats{
				Incomings: map[int]GroupMBStats{
					0: {
						"dedicated": {
							0: {
								LocalMB:  1,
								RemoteMB: 2,
								TotalMB:  3,
							},
						},
					},
				},
				Outgoings: map[int]GroupMBStats{
					0: {
						"dedicated": {
							0: {
								LocalMB:  1,
								RemoteMB: 2,
								TotalMB:  3,
							},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]MBInfo{
					"dedicated": {
						0: {
							LocalMB:  1,
							RemoteMB: 2,
							TotalMB:  3,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "happy path of all x-doms",
			args: args{
				statOutgoing: GroupMBStats{
					"shared-50": {
						0: {
							LocalMB:  10_000,
							RemoteMB: 5_000,
							TotalMB:  15_000,
						},
						2: {
							LocalMB:  12_000,
							RemoteMB: 4_000,
							TotalMB:  16_000,
						},
					},
					"shared-30": {
						0: {
							LocalMB:  8_000,
							RemoteMB: 2_000,
							TotalMB:  10_000,
						},
						1: {
							LocalMB:  20_000,
							RemoteMB: 5_000,
							TotalMB:  25_000,
						},
					},
				},
				ccdToDomain: map[int]int{
					0: 0, 1: 0,
					2: 1, 3: 1,
				},
				xdGroups: sets.NewString("shared-50", "shared-30", "any"),
			},
			want: &DomainStats{
				Incomings: map[int]GroupMBStats{
					0: {
						"shared-30": {
							0: {
								LocalMB:  8_000,
								RemoteMB: 666,
								TotalMB:  8_666,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 1_666,
								TotalMB:  21_666,
							},
						},
						"shared-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 1_666,
								TotalMB:  11_666,
							},
						},
					},
					1: {
						"shared-50": {
							2: {
								LocalMB:  12_000,
								RemoteMB: 12_000,
								TotalMB:  24_000,
							},
						},
					},
				},
				Outgoings: map[int]GroupMBStats{
					0: {
						"shared-30": {
							0: {
								LocalMB:  8_000,
								RemoteMB: 2_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 5_000,
								TotalMB:  25_000,
							},
						},
						"shared-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 5_000,
								TotalMB:  15_000,
							},
						},
					},
					1: {
						"shared-50": {
							2: {
								LocalMB:  12_000,
								RemoteMB: 4_000,
								TotalMB:  16_000,
							},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]MBInfo{
					"shared-50": {
						0: {
							LocalMB:  10_000,
							RemoteMB: 5_000,
							TotalMB:  15_000,
						},
						1: {
							LocalMB:  12_000,
							RemoteMB: 4_000,
							TotalMB:  16_000,
						},
					},
					"shared-30": {
						0: {
							LocalMB:  28_000,
							RemoteMB: 7_000,
							TotalMB:  35_000,
						},
						1: {},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "happy path with shared-50 to cross-domain",
			args: args{
				statOutgoing: GroupMBStats{
					"shared-50": {
						0: {
							LocalMB:  10_000,
							RemoteMB: 5_000,
							TotalMB:  15_000,
						},
						2: {
							LocalMB:  12_000,
							RemoteMB: 4_000,
							TotalMB:  16_000,
						},
					},
					"shared-30": {
						0: {
							LocalMB:  8_000,
							RemoteMB: 2_000,
							TotalMB:  10_000,
						},
						1: {
							LocalMB:  20_000,
							RemoteMB: 5_000,
							TotalMB:  25_000,
						},
					},
				},
				ccdToDomain: map[int]int{
					0: 0, 1: 0,
					2: 1, 3: 1,
				},
				xdGroups: sets.NewString("shared-50"),
			},
			want: &DomainStats{
				Incomings: map[int]GroupMBStats{
					0: {
						"shared-30": {
							0: {
								LocalMB:  8_000,
								RemoteMB: 2_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 5_000,
								TotalMB:  25_000,
							},
						},
						"shared-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 4_000,
								TotalMB:  14_000,
							},
						},
					},
					1: {
						"shared-50": {
							2: {
								LocalMB:  12_000,
								RemoteMB: 5_000,
								TotalMB:  17_000,
							},
						},
					},
				},
				Outgoings: map[int]GroupMBStats{
					0: {
						"shared-30": {
							0: {
								LocalMB:  8_000,
								RemoteMB: 2_000,
								TotalMB:  10_000,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 5_000,
								TotalMB:  25_000,
							},
						},
						"shared-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 5_000,
								TotalMB:  15_000,
							},
						},
					},
					1: {
						"shared-50": {
							2: {
								LocalMB:  12_000,
								RemoteMB: 4_000,
								TotalMB:  16_000,
							},
						},
					},
				},
				OutgoingGroupSumStat: map[string][]MBInfo{
					"shared-50": {
						0: {
							LocalMB:  10_000,
							RemoteMB: 5_000,
							TotalMB:  15_000,
						},
						1: {
							LocalMB:  12_000,
							RemoteMB: 4_000,
							TotalMB:  16_000,
						},
					},
					"shared-30": {
						0: {
							LocalMB:  28_000,
							RemoteMB: 7_000,
							TotalMB:  35_000,
						},
						1: {},
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
			got, err := NewDomainStats(tt.args.statOutgoing, tt.args.ccdToDomain, tt.args.xdGroups)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDomainStats() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomainStats() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainsMon_SumOutgoingByGroup(t *testing.T) {
	t.Parallel()
	type fields struct {
		Incoming map[int]GroupMBStats
		Outgoing map[int]GroupMBStats
	}
	tests := []struct {
		name   string
		fields fields
		want   map[string][]MBInfo
	}{
		{
			name: "happy path",
			fields: fields{
				Outgoing: map[int]GroupMBStats{
					0: {
						"shared-60": map[int]MBInfo{
							0: {LocalMB: 8_000, RemoteMB: 2_000, TotalMB: 10_000},
						},
					},
					1: {
						"shared-60": map[int]MBInfo{
							8: {LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
						},
					},
				},
			},
			want: map[string][]MBInfo{
				"shared-60": {
					{LocalMB: 8_000, RemoteMB: 2_000, TotalMB: 10_000},
					{LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
				},
			},
		},
		{
			name: "happy path of unbalanced domains",
			fields: fields{
				Outgoing: map[int]GroupMBStats{
					0: {
						"shared-60": map[int]MBInfo{
							0: {LocalMB: 8_000, RemoteMB: 2000, TotalMB: 10_000},
						},
					},
					1: {
						"shared-60": map[int]MBInfo{
							8: {LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
							9: {LocalMB: 2_000, RemoteMB: 0, TotalMB: 2_000},
						},
						"dedicated": map[int]MBInfo{
							8: {LocalMB: 11_000, RemoteMB: 1_000, TotalMB: 12_000},
						},
					},
				},
			},
			want: map[string][]MBInfo{
				"dedicated": {
					{},
					{LocalMB: 11_000, RemoteMB: 1_000, TotalMB: 12_000},
				},
				"shared-60": {
					{LocalMB: 8_000, RemoteMB: 2_000, TotalMB: 10_000},
					{LocalMB: 6_000, RemoteMB: 2_000, TotalMB: 8_000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &DomainStats{
				Incomings:            tt.fields.Incoming,
				Outgoings:            tt.fields.Outgoing,
				OutgoingGroupSumStat: map[string][]MBInfo{},
			}
			d.sumOutgoingByGroup()
			if got := d.OutgoingGroupSumStat; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGroupedDomainOutgoingSummary() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainStats_String(t *testing.T) {
	t.Parallel()
	type fields struct {
		Incomings            map[int]DomainMonStat
		Outgoings            map[int]DomainMonStat
		OutgoingGroupSumStat map[string][]MBInfo
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "happy path",
			fields: fields{
				Incomings: map[int]DomainMonStat{
					3: {
						"shared-50": map[int]MBInfo{
							0: {
								LocalMB:  4,
								RemoteMB: 5,
								TotalMB:  9,
							},
						},
					},
					4: {
						"/": map[int]MBInfo{
							2: {
								LocalMB:  10,
								RemoteMB: 20,
								TotalMB:  30,
							},
						},
					},
				},
			},
			want: `[DomainStats]
Incomings:{
  3:{"shared-50":{0:{l:4,r:5,t:9},},},
  4:{"/":{2:{l:10,r:20,t:30},},},
}
Outgoings:{
}
OutgoingGroupSumStat:{}
`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &DomainStats{
				Incomings:            tt.fields.Incomings,
				Outgoings:            tt.fields.Outgoings,
				OutgoingGroupSumStat: tt.fields.OutgoingGroupSumStat,
			}
			if got := d.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}
