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
					CPUTopology: &machine.CPUTopology{
						CPUDetails: map[int]machine.CPUTopoInfo{
							0: {NUMANodeID: 0, SocketID: 0, CoreID: 0, L3CacheID: 0},
							1: {NUMANodeID: 0, SocketID: 0, CoreID: 1, L3CacheID: 1},
							2: {NUMANodeID: 0, SocketID: 0, CoreID: 2, L3CacheID: 16},
							3: {NUMANodeID: 0, SocketID: 0, CoreID: 3, L3CacheID: 17},
							4: {NUMANodeID: 1, SocketID: 0, CoreID: 4, L3CacheID: 2},
							5: {NUMANodeID: 1, SocketID: 0, CoreID: 5, L3CacheID: 3},
							6: {NUMANodeID: 1, SocketID: 0, CoreID: 6, L3CacheID: 18},
							7: {NUMANodeID: 1, SocketID: 0, CoreID: 7, L3CacheID: 19},
						},
					},
					ExtraTopologyInfo: &machine.ExtraTopologyInfo{
						SiblingNumaInfo: &machine.SiblingNumaInfo{
							SiblingNumaMap: map[int]sets.Int{
								0: {},
								1: {},
							},
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
					CPUTopology: &machine.CPUTopology{
						CPUDetails: map[int]machine.CPUTopoInfo{
							0:  {NUMANodeID: 0, SocketID: 0, CoreID: 0, L3CacheID: 0},
							1:  {NUMANodeID: 0, SocketID: 0, CoreID: 1, L3CacheID: 1},
							2:  {NUMANodeID: 1, SocketID: 0, CoreID: 2, L3CacheID: 2},
							3:  {NUMANodeID: 1, SocketID: 0, CoreID: 3, L3CacheID: 3},
							4:  {NUMANodeID: 2, SocketID: 0, CoreID: 4, L3CacheID: 4},
							5:  {NUMANodeID: 2, SocketID: 0, CoreID: 5, L3CacheID: 5},
							6:  {NUMANodeID: 3, SocketID: 0, CoreID: 6, L3CacheID: 6},
							7:  {NUMANodeID: 3, SocketID: 0, CoreID: 7, L3CacheID: 7},
							8:  {NUMANodeID: 4, SocketID: 0, CoreID: 8, L3CacheID: 8},
							9:  {NUMANodeID: 4, SocketID: 0, CoreID: 9, L3CacheID: 9},
							10: {NUMANodeID: 5, SocketID: 0, CoreID: 10, L3CacheID: 10},
							11: {NUMANodeID: 5, SocketID: 0, CoreID: 11, L3CacheID: 11},
							12: {NUMANodeID: 6, SocketID: 0, CoreID: 12, L3CacheID: 12},
							13: {NUMANodeID: 6, SocketID: 0, CoreID: 13, L3CacheID: 13},
							14: {NUMANodeID: 7, SocketID: 0, CoreID: 14, L3CacheID: 14},
							15: {NUMANodeID: 7, SocketID: 0, CoreID: 15, L3CacheID: 15},
							16: {NUMANodeID: 0, SocketID: 1, CoreID: 16, L3CacheID: 16},
							17: {NUMANodeID: 0, SocketID: 1, CoreID: 17, L3CacheID: 17},
							18: {NUMANodeID: 1, SocketID: 1, CoreID: 18, L3CacheID: 18},
							19: {NUMANodeID: 1, SocketID: 1, CoreID: 19, L3CacheID: 19},
							20: {NUMANodeID: 2, SocketID: 1, CoreID: 20, L3CacheID: 20},
							21: {NUMANodeID: 2, SocketID: 1, CoreID: 21, L3CacheID: 21},
							22: {NUMANodeID: 3, SocketID: 1, CoreID: 22, L3CacheID: 22},
							23: {NUMANodeID: 3, SocketID: 1, CoreID: 23, L3CacheID: 23},
							24: {NUMANodeID: 4, SocketID: 1, CoreID: 24, L3CacheID: 24},
							25: {NUMANodeID: 4, SocketID: 1, CoreID: 25, L3CacheID: 25},
							26: {NUMANodeID: 5, SocketID: 1, CoreID: 26, L3CacheID: 26},
							27: {NUMANodeID: 5, SocketID: 1, CoreID: 27, L3CacheID: 27},
							28: {NUMANodeID: 6, SocketID: 1, CoreID: 28, L3CacheID: 28},
							29: {NUMANodeID: 6, SocketID: 1, CoreID: 29, L3CacheID: 29},
							30: {NUMANodeID: 7, SocketID: 1, CoreID: 30, L3CacheID: 30},
							31: {NUMANodeID: 7, SocketID: 1, CoreID: 31, L3CacheID: 31},
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

func Test_identifyNumaFellowships(t *testing.T) {
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
			got, err := identifyNumaFellowships(tt.args.numaMap)
			if (err != nil) != tt.wantErr {
				t.Errorf("identifyNumaFellowships() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("identifyNumaFellowships() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getL3CacheIDsByNUMAs(t *testing.T) {
	t.Parallel()
	type args struct {
		numas      sets.Int
		cpuDetails machine.CPUDetails
	}
	tests := []struct {
		name    string
		args    args
		want    sets.Int
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				numas: sets.NewInt(2, 3),
				cpuDetails: map[int]machine.CPUTopoInfo{
					0: {
						NUMANodeID: 0,
						SocketID:   0,
						CoreID:     0,
						L3CacheID:  0,
					},
					48: {
						NUMANodeID: 2,
						SocketID:   0,
						CoreID:     48,
						L3CacheID:  3,
					},
					49: {
						NUMANodeID: 2,
						SocketID:   0,
						CoreID:     49,
						L3CacheID:  3,
					},
					72: {
						NUMANodeID: 3,
						SocketID:   0,
						CoreID:     72,
						L3CacheID:  1,
					},
					73: {
						NUMANodeID: 3,
						SocketID:   0,
						CoreID:     73,
						L3CacheID:  1,
					},
				},
			},
			want:    sets.NewInt(1, 3),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getL3CacheIDsByNUMAs(tt.args.numas, tt.args.cpuDetails)
			if (err != nil) != tt.wantErr {
				t.Errorf("getL3CacheIDsByNUMAs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getL3CacheIDsByNUMAs() got = %v, want %v", got, tt.want)
			}
		})
	}
}
