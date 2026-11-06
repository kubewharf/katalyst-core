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

package common

import (
	"fmt"
	"path"
	"reflect"
	"testing"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/stretchr/testify/require"
)

func TestParseCgroupNumaValue(t *testing.T) {
	t.Parallel()

	type args struct {
		content string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[int]uint64
		wantErr bool
	}{
		{
			name: "cgroupv1 format",
			args: args{
				content: `total=7587426 N0=92184 N1=21339 N2=104047 N3=7374122
file=70686 N0=5353 N1=3096 N2=12817 N3=51844
anon=7516740 N0=86831 N1=18243 N2=91230 N3=7322278
unevictable=0 N0=0 N1=0 N2=0 N3=0`,
			},
			want: map[string]map[int]uint64{
				"total": {
					0: 92184,
					1: 21339,
					2: 104047,
					3: 7374122,
				},
				"file": {
					0: 5353,
					1: 3096,
					2: 12817,
					3: 51844,
				},
				"anon": {
					0: 86831,
					1: 18243,
					2: 91230,
					3: 7322278,
				},
				"unevictable": {
					0: 0,
					1: 0,
					2: 0,
					3: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "cgroupv2 format",
			args: args{
				content: `anon N0=1629990912 N1=65225723904
file N0=1892352 N1=37441536
unevictable N0=0 N1=0`,
			},
			want: map[string]map[int]uint64{
				"anon": {
					0: 1629990912,
					1: 65225723904,
				},
				"file": {
					0: 1892352,
					1: 37441536,
				},
				"unevictable": {
					0: 0,
					1: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "wrong separator",
			args: args{
				content: `anon N0:1629990912 N1:65225723904
file N0:1892352 N1:37441536
unevictable N0:0 N1:0`,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCgroupNumaValue(tt.args.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCgroupNumaValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCgroupNumaValue() = %v, want %v", got, tt.want)
				return
			}
		})
	}
}

// MockManager is a mock implementation of libcontainer.Manager
// We need to define this because libcontainer.Manager is an interface
// and we need to mock its Set method.

// Define a mock Manager that implements the libcontainer.Manager interface
type mockManager struct {
	cgroups.Manager // Embed the interface to satisfy it implicitly
	SetFunc         func(r *configs.Resources) error
}

// Set calls the mock SetFunc
func (m *mockManager) Set(r *configs.Resources) error {
	if m.SetFunc != nil {
		return m.SetFunc(r)
	}
	return nil
}

// GetPaths is a dummy implementation for the mockManager
func (m *mockManager) GetPaths() map[string]string {
	return nil
}

// GetStats is a dummy implementation for the mockManager
func (m *mockManager) GetStats() (*cgroups.Stats, error) {
	return nil, nil
}

// Freeze is a dummy implementation for the mockManager
func (m *mockManager) Freeze(state configs.FreezerState) error {
	return nil
}

// Destroy is a dummy implementation for the mockManager
func (m *mockManager) Destroy() error {
	return nil
}

// GetPids is a dummy implementation for the mockManager
func (m *mockManager) GetPids() ([]int, error) {
	return nil, nil
}

// GetAllPids is a dummy implementation for the mockManager
func (m *mockManager) GetAllPids() ([]int, error) {
	return nil, nil
}

// TestApplyCgroupConfigs exercises the unexported applyCgroupConfigs core with
// injected fakes for subsystem discovery and manager construction. No global
// function patches are used, so the test is safe to run in parallel with other
// package tests.
func TestApplyCgroupConfigs(t *testing.T) {
	t.Parallel()

	cgroupPath := "test/path"
	resources := &CgroupResources{
		CpuQuota:  100000,
		CpuPeriod: 200000,
	}

	t.Run("when all external calls succeed", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockMgr := &mockManager{SetFunc: func(r *configs.Resources) error {
			// Check if resources are correctly passed through.
			as.Equal(resources.CpuQuota, r.CpuQuota)
			as.Equal(resources.CpuPeriod, r.CpuPeriod)
			return nil
		}}

		err := applyCgroupConfigs(cgroupPath, resources,
			func() (map[string]string, error) { return mockSubsystems, nil },
			func(string, map[string]string) (cgroups.Manager, error) { return mockMgr, nil },
		)
		as.NoError(err)
	})

	t.Run("when GetCgroupSubsystems fails", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		expectedErr := fmt.Errorf("GetCgroupSubsystems failed")
		err := applyCgroupConfigs(cgroupPath, resources,
			func() (map[string]string, error) { return nil, expectedErr },
			func(string, map[string]string) (cgroups.Manager, error) { return nil, nil },
		)
		as.Error(err)
		as.Contains(err.Error(), expectedErr.Error())
	})

	t.Run("when NewCgroupManager fails", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		mockSubsystems := map[string]string{"cpu": "/sys/fs/cgroup/cpu"}
		expectedErr := fmt.Errorf("NewCgroupManager failed")
		err := applyCgroupConfigs(cgroupPath, resources,
			func() (map[string]string, error) { return mockSubsystems, nil },
			func(string, map[string]string) (cgroups.Manager, error) { return nil, expectedErr },
		)
		as.Error(err)
		as.Contains(err.Error(), expectedErr.Error())
	})

	t.Run("when manager.Set fails", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		mockSubsystems := map[string]string{"cpu": "/sys/fs/cgroup/cpu"}
		expectedSetErr := fmt.Errorf("manager.Set failed")
		mockMgr := &mockManager{SetFunc: func(*configs.Resources) error { return expectedSetErr }}

		err := applyCgroupConfigs(cgroupPath, resources,
			func() (map[string]string, error) { return mockSubsystems, nil },
			func(string, map[string]string) (cgroups.Manager, error) { return mockMgr, nil },
		)
		as.Error(err)
		as.Contains(err.Error(), expectedSetErr.Error())
	})

	t.Run("when input resources is nil", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockMgr := &mockManager{SetFunc: func(r *configs.Resources) error {
			// toConfigsResources returns nil when input resources is nil,
			// so manager.Set must receive nil.
			as.Nil(r)
			return nil
		}}

		err := applyCgroupConfigs(cgroupPath, nil,
			func() (map[string]string, error) { return mockSubsystems, nil },
			func(string, map[string]string) (cgroups.Manager, error) { return mockMgr, nil },
		)
		as.NoError(err)
	})

	t.Run("per-subsystem paths are joined with cgroupPath", func(t *testing.T) {
		t.Parallel()
		as := require.New(t)

		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockMgr := &mockManager{SetFunc: func(*configs.Resources) error { return nil }}

		var gotPaths map[string]string
		err := applyCgroupConfigs(cgroupPath, resources,
			func() (map[string]string, error) { return mockSubsystems, nil },
			func(_ string, paths map[string]string) (cgroups.Manager, error) {
				gotPaths = paths
				return mockMgr, nil
			},
		)
		as.NoError(err)
		as.Len(gotPaths, len(mockSubsystems))
		for name, subPath := range mockSubsystems {
			as.Equal(path.Join(subPath, cgroupPath), gotPaths[name])
		}
	})
}
