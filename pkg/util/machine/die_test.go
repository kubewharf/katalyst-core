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

package machine

import (
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"
)

func Test_getCPU(t *testing.T) {
	t.Parallel()
	dummyFs := afero.NewMemMapFs()
	dummyFs.MkdirAll("/sys/devices/system/cpu/cpu10/driver", 0777)
	dummyFs.MkdirAll("/sys/devices/system/cpu/cpu10/node2", 0777)
	afero.WriteFile(dummyFs, "/sys/devices/system/cpu/cpu10/cache/index3/id", []byte("4"), 0444)

	type args struct {
		id int
	}
	tests := []struct {
		name string
		args args
		want *cpuDev
	}{
		{
			name: "happy path",
			args: args{
				id: 10,
			},
			want: &cpuDev{
				id:       10,
				numaNode: 2,
				ccd:      4,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, _ := getCPU(dummyFs, tt.args.id); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getCPU() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getCPUs(t *testing.T) {
	t.Parallel()
	dummyFs := afero.NewMemMapFs()
	dummyFs.MkdirAll("/sys/devices/system/cpu/cpu0/node5", 0777)
	afero.WriteFile(dummyFs, "/sys/devices/system/cpu/cpu0/cache/index3/id", []byte("20\n"), 0444)
	dummyFs.MkdirAll("/sys/devices/system/cpu/cpu1/node6", 0777)
	afero.WriteFile(dummyFs, "/sys/devices/system/cpu/cpu1/cache/index3/id", []byte("22"), 0444)

	type args struct {
		fs afero.Fs
	}
	tests := []struct {
		name    string
		args    args
		want    []*cpuDev
		wantErr bool
	}{
		{
			name: "happy path of 2 cpus",
			args: args{
				fs: dummyFs,
			},
			want: []*cpuDev{
				{
					id:       0,
					numaNode: 5,
					ccd:      20,
				},
				{
					id:       1,
					numaNode: 6,
					ccd:      22,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getCPUs(tt.args.fs)
			if (err != nil) != tt.wantErr {
				t.Errorf("getCPUs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getCPUs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getCPUsInDie(t *testing.T) {
	t.Parallel()
	type args struct {
		cpus []*cpuDev
	}
	tests := []struct {
		name string
		args args
		want map[int][]int
	}{
		{
			name: "happy path of 4 cpu 2 die",
			args: args{
				cpus: []*cpuDev{
					{id: 192, ccd: 32},
					{id: 193, ccd: 33},
					{id: 78, ccd: 32},
					{id: 79, ccd: 33},
				},
			},
			want: map[int][]int{
				32: {192, 78},
				33: {193, 79},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getCPUsInDie(tt.args.cpus); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getCPUsInDie() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getDiesInNuma(t *testing.T) {
	t.Parallel()
	type args struct {
		cpus []*cpuDev
	}
	tests := []struct {
		name string
		args args
		want map[int]sets.Int
	}{
		{
			name: "happy path of 2 die 1 numa",
			args: args{
				cpus: []*cpuDev{
					{id: 3, ccd: 15, numaNode: 7},
					{id: 4, ccd: 14, numaNode: 7},
					{id: 5, ccd: 14, numaNode: 7},
				},
			},
			want: map[int]sets.Int{
				7: {15: sets.Empty{}, 14: sets.Empty{}},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getDiesInNuma(tt.args.cpus); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getDiesInNuma() = %v, want %v", got, tt.want)
			}
		})
	}
}
