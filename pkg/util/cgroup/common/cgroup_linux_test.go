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
	. "github.com/smartystreets/goconvey/convey"

	"github.com/bytedance/mockey"
	"github.com/google/cadvisor/container/libcontainer"
	"github.com/opencontainers/runc/libcontainer/configs"
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

func TestApplyCgroupConfigs(t *testing.T) {
	t.Parallel()

	cgroupPath := "test/path"
	resources := &CgroupResources{
		CpuQuota:  100000,
		CpuPeriod: 200000,
	}

	mockey.PatchConvey("When all external calls succeed", t, func() {
		// Arrange
		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(mockSubsystems, nil).Build()

		mockMgr := &mockManager{
			SetFunc: func(r *configs.Resources) error {
				// Check if resources are correctly passed
				So(r.CpuQuota, ShouldEqual, resources.CpuQuota)
				So(r.CpuPeriod, ShouldEqual, resources.CpuPeriod)
				return nil
			},
		}
		mockey.Mock(libcontainer.NewCgroupManager).Return(mockMgr, nil).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, resources)

		// Assert
		So(err, ShouldBeNil)
	})

	mockey.PatchConvey("When GetCgroupSubsystems fails", t, func() {
		// Arrange
		expectedErr := fmt.Errorf("GetCgroupSubsystems failed")
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(nil, expectedErr).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, resources)

		// Assert
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, expectedErr.Error())
	})

	mockey.PatchConvey("When NewCgroupManager fails", t, func() {
		// Arrange
		mockSubsystems := map[string]string{
			"cpu": "/sys/fs/cgroup/cpu",
		}
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(mockSubsystems, nil).Build()

		expectedErr := fmt.Errorf("NewCgroupManager failed")
		mockey.Mock(libcontainer.NewCgroupManager).Return(nil, expectedErr).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, resources)

		// Assert
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, expectedErr.Error())
	})

	mockey.PatchConvey("When manager.Set fails", t, func() {
		// Arrange
		mockSubsystems := map[string]string{
			"cpu": "/sys/fs/cgroup/cpu",
		}
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(mockSubsystems, nil).Build()

		expectedSetErr := fmt.Errorf("manager.Set failed")
		mockMgr := &mockManager{
			SetFunc: func(r *configs.Resources) error {
				return expectedSetErr
			},
		}
		mockey.Mock(libcontainer.NewCgroupManager).Return(mockMgr, nil).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, resources)

		// Assert
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, expectedSetErr.Error())
	})

	mockey.PatchConvey("When input resources is nil", t, func() {
		// Arrange
		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(mockSubsystems, nil).Build()

		mockMgr := &mockManager{
			SetFunc: func(r *configs.Resources) error {
				// toConfigsResources will return nil if input resources is nil
				// and manager.Set(nil) should not cause error in this mock
				So(r, ShouldBeNil)
				return nil
			},
		}
		mockey.Mock(libcontainer.NewCgroupManager).Return(mockMgr, nil).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, nil) // Pass nil resources

		// Assert
		So(err, ShouldBeNil)
	})

	mockey.PatchConvey("When path.Join is called", t, func() {
		// Arrange
		mockSubsystems := map[string]string{
			"cpu":    "/sys/fs/cgroup/cpu",
			"memory": "/sys/fs/cgroup/memory",
		}
		mockey.Mock(libcontainer.GetCgroupSubsystems).Return(mockSubsystems, nil).Build()

		var joinedPaths []string
		mockey.Mock(path.Join).To(func(elem ...string) string {
			// Capture the arguments passed to path.Join
			// The original path.Join is complex to fully replicate,
			// so we just check if it's called with expected first arg (subsystem path)
			// and the cgroupPath.
			if len(elem) == 2 {
				joinedPaths = append(joinedPaths, elem[0]+"/"+elem[1])
				return elem[0] + "/" + elem[1]
			}
			return "mocked/path/join/result"
		}).Build()

		mockMgr := &mockManager{
			SetFunc: func(r *configs.Resources) error {
				return nil
			},
		}
		mockey.Mock(libcontainer.NewCgroupManager).Return(mockMgr, nil).Build()

		// Act
		err := ApplyCgroupConfigs(cgroupPath, resources)

		// Assert
		So(err, ShouldBeNil)
		So(len(joinedPaths), ShouldEqual, len(mockSubsystems))
		for _, subPath := range mockSubsystems {
			found := false
			for _, jp := range joinedPaths {
				if jp == subPath+"/"+cgroupPath {
					found = true
					break
				}
			}
			So(found, ShouldBeTrue)
		}
	})
}
