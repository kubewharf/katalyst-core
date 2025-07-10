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
		statIncoming GroupMonStat
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
			name: "happy path of all x-doms",
			args: args{
				statIncoming: GroupMonStat{
					mon: map[string]GroupCCDMB{
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
				},
				ccdToDomain: map[int]int{
					0: 0, 1: 0,
					2: 1, 3: 1,
				},
				xdGroups: sets.NewString("shared-50", "shared-30", "any"),
			},
			want: &DomainsMon{
				Incoming: map[int]GroupMonStat{
					0: {
						mon: map[string]GroupCCDMB{
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
					},
					1: {
						mon: map[string]GroupCCDMB{
							"shared-50": {
								2: {
									LocalMB:  12_000,
									RemoteMB: 4_000,
									TotalMB:  16_000,
								},
							},
						},
					},
				},
				Outgoing: map[int]GroupMonStat{
					0: {
						mon: map[string]GroupCCDMB{
							"shared-30": {
								0: {
									LocalMB:  8_000,
									RemoteMB: 840,
									TotalMB:  8_840,
								},
								1: {
									LocalMB:  20_000,
									RemoteMB: 2_080,
									TotalMB:  22_080,
								},
							},
							"shared-50": {
								0: {
									LocalMB:  10_000,
									RemoteMB: 1_040,
									TotalMB:  11_040,
								},
							},
						},
					},
					1: {
						mon: map[string]GroupCCDMB{
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
			},
			wantErr: false,
		},
		{
			name: "happy path with shared-50 to cross-domain",
			args: args{
				statIncoming: GroupMonStat{
					mon: map[string]GroupCCDMB{
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
				},
				ccdToDomain: map[int]int{
					0: 0, 1: 0,
					2: 1, 3: 1,
				},
				xdGroups: sets.NewString("shared-50"),
			},
			want: &DomainsMon{
				Incoming: map[int]GroupMonStat{
					0: {
						mon: map[string]GroupCCDMB{
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
					},
					1: {
						mon: map[string]GroupCCDMB{
							"shared-50": {
								2: {
									LocalMB:  12_000,
									RemoteMB: 4_000,
									TotalMB:  16_000,
								},
							},
						},
					},
				},
				Outgoing: map[int]GroupMonStat{
					0: {
						mon: map[string]GroupCCDMB{
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
					},
					1: {
						mon: map[string]GroupCCDMB{
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
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewDomainsMon(tt.args.statIncoming, tt.args.ccdToDomain, tt.args.xdGroups)
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
