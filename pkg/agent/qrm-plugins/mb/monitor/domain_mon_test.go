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
		statOutgoing GroupMonStat
		ccdToDomain  map[int]int
		xdGroups     sets.String
	}
	tests := []struct {
		name    string
		args    args
		want    *DomainsMon
		wantErr bool
	}{
		{
			name: "simple case of one domain",
			args: args{
				statOutgoing: GroupMonStat{
					"dedicated": {
						0: {
							LocalMB:  1,
							RemoteMB: 2,
							TotalMB:  3,
						},
					},
				},
				ccdToDomain: map[int]int{0: 0},
				xdGroups:    nil,
			},
			want: &DomainsMon{
				Incoming: map[int]GroupMonStat{
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
				Outgoing: map[int]GroupMonStat{
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
			},
			wantErr: false,
		},
		{
			name: "happy path of all x-doms",
			args: args{
				statOutgoing: GroupMonStat{
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
			want: &DomainsMon{
				Outgoing: map[int]GroupMonStat{
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
				Incoming: map[int]GroupMonStat{
					0: {
						"shared-30": {
							0: {
								LocalMB:  8_000,
								RemoteMB: 842,
								TotalMB:  8_842,
							},
							1: {
								LocalMB:  20_000,
								RemoteMB: 2_105,
								TotalMB:  22_105,
							},
						},
						"shared-50": {
							0: {
								LocalMB:  10_000,
								RemoteMB: 1_052,
								TotalMB:  11_052,
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
			},
			wantErr: false,
		},
		{
			name: "happy path with shared-50 to cross-domain",
			args: args{
				statOutgoing: GroupMonStat{
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
			want: &DomainsMon{
				Outgoing: map[int]GroupMonStat{
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
				Incoming: map[int]GroupMonStat{
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
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewDomainsMon(tt.args.statOutgoing, tt.args.ccdToDomain, tt.args.xdGroups)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDomainsMon() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomainsMon() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainsMon_GetGroupedDomainSummary(t *testing.T) {
	t.Parallel()
	type fields struct {
		Incoming map[int]GroupMonStat
		Outgoing map[int]GroupMonStat
	}
	tests := []struct {
		name   string
		fields fields
		want   map[string][]MBStat
	}{
		{
			name: "happy path",
			fields: fields{
				Outgoing: map[int]GroupMonStat{
					0: {
						"shared-60": map[int]MBStat{
							0: {LocalMB: 8_000, RemoteMB: 2_000, TotalMB: 10_000},
						},
					},
					1: {
						"shared-60": map[int]MBStat{
							8: {LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
						},
					},
				},
			},
			want: map[string][]MBStat{
				"shared-60": {
					{LocalMB: 8_000, RemoteMB: 2_000, TotalMB: 10_000},
					{LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
				},
			},
		},
		{
			name: "happy path of unbalanced domains",
			fields: fields{
				Outgoing: map[int]GroupMonStat{
					0: {
						"shared-60": map[int]MBStat{
							0: {LocalMB: 8_000, RemoteMB: 2000, TotalMB: 10_000},
						},
					},
					1: {
						"shared-60": map[int]MBStat{
							8: {LocalMB: 4_000, RemoteMB: 2_000, TotalMB: 6_000},
							9: {LocalMB: 2_000, RemoteMB: 0, TotalMB: 2_000},
						},
						"dedicated": map[int]MBStat{
							8: {LocalMB: 11_000, RemoteMB: 1_000, TotalMB: 12_000},
						},
					},
				},
			},
			want: map[string][]MBStat{
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
			d := &DomainsMon{
				Incoming: tt.fields.Incoming,
				Outgoing: tt.fields.Outgoing,
			}
			if got := d.GetGroupedDomainOutgoingSummary(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetGroupedDomainOutgoingSummary() = %v, want %v", got, tt.want)
			}
		})
	}
}
