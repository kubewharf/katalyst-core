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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	libcgroups "github.com/opencontainers/runc/libcontainer/cgroups"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestMain(m *testing.M) {
	oldTestMode := libcgroups.TestMode
	libcgroups.TestMode = true
	code := m.Run()
	libcgroups.TestMode = oldTestMode
	os.Exit(code)
}

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

func Test_manager_GetHierarchicalNumaMemoryUsage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "total=6 N0=2 N1=4\nhierarchical_total=9 N0=3 N1=6\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.numa_stat"), []byte(content), 0o644); err != nil {
		t.Fatalf("write memory.numa_stat: %v", err)
	}

	got, err := NewManager().GetHierarchicalNumaMemoryUsage(dir, 4096)
	if err != nil {
		t.Fatalf("GetHierarchicalNumaMemoryUsage() error = %v", err)
	}
	want := map[int]uint64{0: 3 * 4096, 1: 6 * 4096}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetHierarchicalNumaMemoryUsage() = %v, want %v", got, want)
	}
}

func Test_manager_GetHierarchicalNumaMemoryUsageRejectsMissingCounter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.numa_stat"), []byte("total=2 N0=2\n"), 0o644); err != nil {
		t.Fatalf("write memory.numa_stat: %v", err)
	}

	if _, err := NewManager().GetHierarchicalNumaMemoryUsage(dir, 4096); err == nil {
		t.Fatal("GetHierarchicalNumaMemoryUsage() expected error")
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
