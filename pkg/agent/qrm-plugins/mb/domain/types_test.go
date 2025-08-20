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
				ccdAlienMBLimit: 5_000,
			},
			want: &Domain{
				id:                 1,
				ccds:               sets.NewInt(4, 5, 6, 7),
				maxAlienIncomingMB: 5_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NewDomain(tt.args.id, tt.args.ccds, tt.args.ccdAlienMBLimit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomain() = %v, want %v", got, tt.want)
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
						id:   0,
						ccds: sets.NewInt(0, 7),
					},
					{
						id:   1,
						ccds: sets.NewInt(4, 7),
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
						id:   0,
						ccds: sets.NewInt(0, 1),
					},
					{
						id:   1,
						ccds: sets.NewInt(2, 3),
					},
				},
			},
			want: Domains{
				0: {
					id:   0,
					ccds: sets.NewInt(0, 1),
				},
				1: {
					id:   1,
					ccds: sets.NewInt(2, 3),
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
					id:   0,
					ccds: sets.NewInt(0, 1, 2, 3),
				},
				1: {
					id:   1,
					ccds: sets.NewInt(4, 5, 6, 7),
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
		info        *machine.KatalystMachineInfo
		maxRemoteMB int
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
			name: "happy path of real numa",
			args: args{
				info: &machine.KatalystMachineInfo{
					CPUTopology: nil,
					DieTopology: &machine.DieTopology{
						NUMAToDie: map[int]sets.Int{
							0: sets.NewInt(0, 1, 16, 17),
							1: sets.NewInt(2, 3, 18, 19),
						},
					},
					ExtraTopologyInfo: &machine.ExtraTopologyInfo{
						SiblingNumaInfo: &machine.SiblingNumaInfo{
							SiblingNumaMap: map[int]sets.Int{
								0: {},
								1: {},
							},
							SiblingNumaMBWAllocatable: 90_000,
						},
					},
				},
				maxRemoteMB: 12_000,
			},
			want: Domains{
				0: &Domain{
					id:                 0,
					ccds:               sets.NewInt(0, 1, 16, 17),
					maxAlienIncomingMB: 12000,
				},
				1: &Domain{
					id:                 1,
					ccds:               sets.NewInt(2, 3, 18, 19),
					maxAlienIncomingMB: 12000,
				},
			},
			wantErr: false,
		},
		{
			name: "happy path of fake numa",
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
				maxRemoteMB: 12_000,
			},
			want: Domains{
				0: &Domain{
					id:                 0,
					ccds:               sets.NewInt(0, 1, 2, 3, 4, 5, 6, 7, 16, 17, 18, 19, 20, 21, 22, 23),
					maxAlienIncomingMB: 12000,
				},
				1: &Domain{
					id:                 1,
					ccds:               sets.NewInt(8, 9, 10, 11, 12, 13, 14, 15, 24, 25, 26, 27, 28, 29, 30, 31),
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
			got, err := NewDomainsByMachineInfo(tt.args.info, tt.args.maxRemoteMB)
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

func Test_identifyDomainByNumas(t *testing.T) {
	t.Parallel()
	type args struct {
		numaMap map[int]sets.Int
	}
	tests := []struct {
		name    string
		args    args
		want    map[int]sets.Int
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				numaMap: map[int]sets.Int{
					0: {},
					1: {},
				},
			},
			want: map[int]sets.Int{
				0: sets.NewInt(0),
				1: sets.NewInt(1),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := identifyDomainByNumas(tt.args.numaMap)
			if (err != nil) != tt.wantErr {
				t.Errorf("identifyDomainByNumas() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("identifyDomainByNumas() got = %v, want %v", got, tt.want)
			}
		})
	}
}
