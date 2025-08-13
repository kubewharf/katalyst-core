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
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"
)

func Test_procFsCPUInfoGetter_Get(t *testing.T) {
	t.Parallel()

	dummyFs := afero.NewMemMapFs()
	_ = dummyFs.MkdirAll("/sys/devices/system/cpu/cpu10/driver", 0o777)
	_ = dummyFs.MkdirAll("/sys/devices/system/cpu/cpu10/node2", 0o777)
	_ = afero.WriteFile(dummyFs, "/sys/devices/system/cpu/cpu10/cache/index3/id", []byte("4"), 0o444)

	type fields struct {
		fs afero.Fs
	}
	type args struct {
		cpuID int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *cpuInfo
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			fields: fields{
				fs: dummyFs,
			},
			args: args{
				cpuID: 10,
			},
			want: &cpuInfo{
				cpuID:     10,
				nodeID:    2,
				l3CacheID: 4,
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := procFsCPUInfoGetter{
				fs: tt.fields.fs,
			}
			got, err := p.Get(tt.args.cpuID)
			if !tt.wantErr(t, err, fmt.Sprintf("Get(%v)", tt.args.cpuID)) {
				return
			}
			assert.Equalf(t, tt.want, got, "Get(%v)", tt.args.cpuID)
		})
	}
}

type mockCPUInfoGetter struct {
	mock.Mock
}

func (m *mockCPUInfoGetter) Get(cpuID int) (*cpuInfo, error) {
	args := m.Called(cpuID)
	return args.Get(0).(*cpuInfo), args.Error(1)
}

func TestGetDieTopology(t *testing.T) {
	t.Parallel()

	dummyGetter := new(mockCPUInfoGetter)
	dummyGetter.On("Get", 0).Return(&cpuInfo{cpuID: 0, nodeID: 0, l3CacheID: 0}, nil)
	dummyGetter.On("Get", 1).Return(&cpuInfo{cpuID: 0, nodeID: 0, l3CacheID: 4}, nil)
	dummyGetter.On("Get", 2).Return(&cpuInfo{cpuID: 0, nodeID: 1, l3CacheID: 8}, nil)
	dummyGetter.On("Get", 3).Return(&cpuInfo{cpuID: 0, nodeID: 1, l3CacheID: 12}, nil)

	type args struct {
		infoGetter cpuInfoGetter
		numCPU     int
	}
	tests := []struct {
		name    string
		args    args
		want    *DieTopology
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			args: args{
				infoGetter: dummyGetter,
				numCPU:     4,
			},
			want: &DieTopology{
				numaToDie: map[int]sets.Int{0: sets.NewInt(0, 4), 1: sets.NewInt(8, 12)},
				dieToNuma: map[int]int{0: 0, 4: 0, 8: 1, 12: 1},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getDieTopology(tt.args.infoGetter, tt.args.numCPU)
			if !tt.wantErr(t, err, fmt.Sprintf("getDieTopology(%v, %v)", tt.args.infoGetter, tt.args.numCPU)) {
				return
			}
			assert.Equalf(t, tt.want, got, "getDieTopology(%v, %v)", tt.args.infoGetter, tt.args.numCPU)
		})
	}
}
