//go:build linux
// +build linux

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

package v1

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *manager
	}{
		{
			name: "test new manager",
			want: &manager{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NewManager(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewManager() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_ApplyMemory(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		data          *common.MemoryData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply memory with LimitInBytes",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.MemoryData{
					LimitInBytes: 1234,
				},
			},
			wantErr: true,
		},
		{
			name: "test apply memory with SoftLimitInBytes",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.MemoryData{
					SoftLimitInBytes: 2234,
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := &manager{}
			if err := m.ApplyMemory(tt.args.absCgroupPath, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyMemory() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_GetMemory(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    *common.MemoryStats
		wantErr bool
	}{
		{
			name: "test get memory",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetMemory(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetMemory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetMemory() = %v, want %v", got, tt.want)
			}

			_, _, err = GetMemoryStatsFromStatFile(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMemoryStatsFromStatFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}
