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

package test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
)

var (
	osTestOnce    sync.Once
	filerTestOnce sync.Once
)

func init() {
	SetupTestOS()
	SetupTestFiler()
}

func Test_Contains(t *testing.T) {
	t.Parallel()
	list := []int{1, 2, 3}
	if found := utils.Contains(list, 3); !found {
		t.Errorf("expected found, got %v", found)
	}
	if found := utils.Contains(list, 4); found {
		t.Errorf("expected not found, got %v", found)
	}
}

func TestDelta(t *testing.T) {
	t.Parallel()
	type args struct {
		bit int
		new uint64
		old uint64
	}
	tests := []struct {
		name string
		args args
		want uint64
	}{
		{
			name: "happy path 8 bits",
			args: args{
				bit: 8,
				new: 140,
				old: 100,
			},
			want: 40,
		},
		{
			name: "happy path 16 bits",
			args: args{
				bit: 16,
				new: 100,
				old: 140,
			},
			want: 2<<15 - 40 - 1, // 65495,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := utils.Delta(tt.args.bit, tt.args.new, tt.args.old); got != tt.want {
				t.Errorf("Delta() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestL3PMCToLatency(t *testing.T) {
	t.Parallel()
	type args struct {
		count1   uint64
		count2   uint64
		interval uint64
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "conner case 0 divider",
			args: args{
				count1:   1,
				count2:   0,
				interval: 1,
			},
			want: 0,
		},
		{
			name: "happy path",
			args: args{
				count1:   100,
				count2:   2,
				interval: 1,
			},
			want: 800000,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := utils.L3PMCToLatency(tt.args.count1, tt.args.count2, tt.args.interval); got != tt.want {
				t.Errorf("L3PMCToLatency() = %v, want %v", got, tt.want)
			}
		})
	}
}

type filerMock struct{}

func (f filerMock) ReadFileIntoInt(filepath string) (int, error) {
	switch filepath {
	case "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_cur_freq":
		return 101, nil
	default:
		return 0, errors.New("mock test error")
	}
}

func SetupTestFiler() {
	filerTestOnce.Do(func() {
		utils.FilerSingleton = &filerMock{}
	})
}

func TestGetCPUFrequency(t *testing.T) {
	t.Parallel()
	type args struct {
		cpu    int
		vendor string
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "happy path AMD",
			args: args{
				cpu:    0,
				vendor: "AMD",
			},
			want:    101,
			wantErr: false,
		},
		{
			name: "negative path Intel",
			args: args{
				cpu:    1,
				vendor: "Intel",
			},
			want:    -1,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := utils.GetCPUFrequency(tt.args.cpu, tt.args.vendor)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCPUFrequency() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetCPUFrequency() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func SetupTestOS() {
	osTestOnce.Do(func() {
		testOS := &afero.Afero{Fs: afero.NewMemMapFs()}

		// we would like to have below device files exist for testing
		fakeFiles := []struct {
			dir     string
			file    string
			content string
		}{
			{
				dir:     "/sys/devices/system/node/node0/cpu0/cache/index3/",
				file:    "shared_cpu_list",
				content: "0-1\n",
			},
			{
				dir:     "/sys/devices/system/node/node0/cpu1/cache/index3/",
				file:    "shared_cpu_list",
				content: "0-1\n",
			},
			{
				dir:     "/sys/devices/system/node/node1/cpu2/cache/index3/",
				file:    "shared_cpu_list",
				content: "2-3\n",
			},
			{
				dir:     "/sys/devices/system/node/node1/cpu3/cache/index3/",
				file:    "shared_cpu_list",
				content: "2-3\n",
			},
		}

		for _, entry := range fakeFiles {
			_ = testOS.MkdirAll(entry.dir, os.ModePerm)
			_ = testOS.WriteFile(filepath.Join(entry.dir, entry.file), []byte(entry.content), os.ModePerm)
		}

		utils.OSSingleton = testOS
	})
}

func TestGetCCDTopology(t *testing.T) {
	t.Parallel()
	type args struct {
		numNuma int
	}
	tests := []struct {
		name    string
		args    args
		want    map[int][]int
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				numNuma: 2,
			},
			want:    map[int][]int{0: {0, 1}, 1: {2, 3}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := utils.GetCCDTopology(tt.args.numNuma)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCCDTopology() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCCDTopology() got = %v, want %v", got, tt.want)
			}
		})
	}
}
