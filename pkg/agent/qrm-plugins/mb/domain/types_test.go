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

package domain

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestNewDomain(t *testing.T) {
	t.Parallel()
	type args struct {
		id              int
		ccds            sets.Int
		capacity        int
		ccdMax          int
		ccdMin          int
		ccdAlienMBLimit int
	}
	tests := []struct {
		name string
		args args
		want *Domain
	}{
		{
			name: "happy path",
			args: args{
				id:              1,
				ccds:            sets.NewInt(4, 5, 6, 7),
				capacity:        60_000,
				ccdMax:          35_000,
				ccdMin:          4_000,
				ccdAlienMBLimit: 5_000,
			},
			want: &Domain{
				ID:                 1,
				CCDs:               sets.NewInt(4, 5, 6, 7),
				defaultCapacityMB:  60_000,
				maxAlienIncomingMB: 5_000,
				maxMBPerCCD:        35_000,
				minMBPerCCD:        4_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := newDomain(tt.args.id, tt.args.ccds, tt.args.capacity,
				tt.args.ccdMin, tt.args.ccdMax, tt.args.ccdAlienMBLimit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newDomain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewDomains(t *testing.T) {
	t.Parallel()
	type args struct {
		domains []*Domain
	}
	tests := []struct {
		name    string
		args    args
		want    Domains
		wantErr bool
	}{
		{
			name: "overlapping ccds not allowed",
			args: args{
				domains: []*Domain{
					{
						ID:   0,
						CCDs: sets.NewInt(0, 7),
					},
					{
						ID:   1,
						CCDs: sets.NewInt(4, 7),
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "happy path",
			args: args{
				domains: []*Domain{
					{
						ID:   0,
						CCDs: sets.NewInt(0, 1),
					},
					{
						ID:   1,
						CCDs: sets.NewInt(2, 3),
					},
				},
			},
			want: Domains{
				0: {
					ID:   0,
					CCDs: sets.NewInt(0, 1),
				},
				1: {
					ID:   1,
					CCDs: sets.NewInt(2, 3),
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewDomains(tt.args.domains...)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDomains() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomains() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomains_GetCCDMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    Domains
		want map[int]int
	}{
		{
			name: "happy path",
			d: Domains{
				0: {
					ID:   0,
					CCDs: sets.NewInt(0, 1, 2, 3),
				},
				1: {
					ID:   1,
					CCDs: sets.NewInt(4, 5, 6, 7),
				},
			},
			want: map[int]int{
				0: 0, 1: 0, 2: 0, 3: 0,
				4: 1, 5: 1, 6: 1, 7: 1,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.d.GetCCDMapping(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCCDMapping() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewDomainsByMachineInfo(t *testing.T) {
	t.Parallel()
	type args struct {
		info            *machine.KatalystMachineInfo
		ccdMinMB        int
		ccdMaxMB        int
		maxRemoteMB     int
		defaultDomainMB int
	}
	tests := []struct {
		name    string
		args    args
		want    Domains
		wantErr bool
	}{
		{
			name: "nil negative",
			args: args{
				info: nil,
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "happy path",
			args: args{
				info: &machine.KatalystMachineInfo{
					CPUTopology: nil,
					DieTopology: &machine.DieTopology{
						NUMAToDie: map[int]sets.Int{
							0: sets.NewInt(0, 1, 16, 17),
							1: sets.NewInt(2, 3, 18, 19),
							2: sets.NewInt(4, 5, 20, 21),
							3: sets.NewInt(6, 7, 22, 23),
							4: sets.NewInt(8, 9, 24, 25),
							5: sets.NewInt(10, 11, 26, 27),
							6: sets.NewInt(12, 13, 28, 29),
							7: sets.NewInt(14, 15, 30, 31),
						},
					},
					ExtraTopologyInfo: &machine.ExtraTopologyInfo{
						SiblingNumaInfo: &machine.SiblingNumaInfo{
							SiblingNumaMap: map[int]sets.Int{
								0: sets.NewInt(1, 2, 3),
								1: sets.NewInt(0, 2, 3),
								2: sets.NewInt(0, 1, 3),
								3: sets.NewInt(0, 1, 2),
								4: sets.NewInt(5, 6, 7),
								5: sets.NewInt(4, 6, 7),
								6: sets.NewInt(4, 5, 7),
								7: sets.NewInt(4, 5, 6),
							},
							SiblingNumaMBWAllocatable: 90_000,
						},
					},
				},
				ccdMinMB:        1_000,
				ccdMaxMB:        35_000,
				maxRemoteMB:     12_000,
				defaultDomainMB: 90_000,
			},
			want: Domains{
				0: &Domain{
					ID:                 0,
					CCDs:               sets.NewInt(0, 1, 2, 3, 4, 5, 6, 7, 16, 17, 18, 19, 20, 21, 22, 23),
					defaultCapacityMB:  90_000,
					minMBPerCCD:        1_000,
					maxMBPerCCD:        35_000,
					maxAlienIncomingMB: 12000,
				},
				1: &Domain{
					ID:                 1,
					CCDs:               sets.NewInt(8, 9, 10, 11, 12, 13, 14, 15, 24, 25, 26, 27, 28, 29, 30, 31),
					defaultCapacityMB:  90_000,
					minMBPerCCD:        1_000,
					maxMBPerCCD:        35_000,
					maxAlienIncomingMB: 12000,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewDomainsByMachineInfo(tt.args.info,
				tt.args.defaultDomainMB, tt.args.ccdMinMB, tt.args.ccdMaxMB, tt.args.maxRemoteMB)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDomainsByMachineInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomainsByMachineInfo() got = %v, want %v", got, tt.want)
			}
		})
	}
}
