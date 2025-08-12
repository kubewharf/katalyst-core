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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/klauspost/cpuid/v2"
	"github.com/pkg/errors"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func TestGetCoreNumReservedForReclaim(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	tests := []struct {
		name             string
		numReservedCores int
		numNumaNodes     int
		want             map[int]int
	}{
		{
			name:             "reserve 4",
			numReservedCores: 4,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 2",
			numReservedCores: 2,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 3",
			numReservedCores: 3,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 0",
			numReservedCores: 0,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 1",
			numReservedCores: 1,
			numNumaNodes:     4,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 8",
			numReservedCores: 8,
			numNumaNodes:     4,
			want:             map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := GetCoreNumReservedForReclaim(tt.numReservedCores, tt.numNumaNodes)
			assert.Equal(t, tt.want, r)
		})
	}
}

func Test_GetCPUPackageID(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test GetCPUPackageID", t, func() {
		mockey.PatchConvey("scenario：Successfully obtained the cpu package id", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(0)
			expectedPackageID := 1

			// Mock os.Stat，the simulation directory exists
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			// Mock os.ReadFile，simulate the file content successfully read
			mockey.Mock(os.ReadFile).Return([]byte("1\n"), nil).Build()
			// Mock strconv.Atoi，simulate successful conversion of string to an integer
			mockey.Mock(strconv.Atoi).Return(expectedPackageID, nil).Build()

			// Act: call the function to be tested
			pkgID, err := GetCPUPackageID(cpuID)

			// Assert: assertion results are in line with expectations
			So(err, ShouldBeNil)
			So(pkgID, ShouldEqual, expectedPackageID)
		})

		mockey.PatchConvey("scenario 2: Topology directory does not exist", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(1)
			// Mock os.Stat，simulation returns a "file does not exist" error
			mockey.Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
			// Mock os.IsNotExist，make it return true for os.ErrNotExist
			mockey.Mock(os.IsNotExist).Return(true).Build()

			// Act: call the function to be tested
			pkgID, err := GetCPUPackageID(cpuID)

			// Assert: asserts that a specific error and -1 are returned
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "is not exist")
			So(pkgID, ShouldEqual, -1)
		})

		mockey.PatchConvey("Scenario 3: Failed to read the physical_package_id file", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(2)
			readErr := errors.New("permission denied")

			// Mock os.Stat，simulate directory existence
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			// Mock os.ReadFile，an error occurred while simulating reading a file
			mockey.Mock(os.ReadFile).Return(nil, readErr).Build()

			// Act: call the function to be tested
			pkgID, err := GetCPUPackageID(cpuID)

			// Assert: assert returns a file read error and -1
			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, readErr)
			So(pkgID, ShouldEqual, -1)
		})

		mockey.PatchConvey("Scenario 4: Invalid file content causes Atoi conversion to fail", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(3)
			atoiErr := errors.New("invalid syntax")

			// Mock os.Stat，simulate directory existence
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			// Mock os.ReadFile，analog reads of non-digital content
			mockey.Mock(os.ReadFile).Return([]byte("invalid-id\n"), nil).Build()
			// Mock strconv.Atoi，analog conversion failed
			mockey.Mock(strconv.Atoi).Return(0, atoiErr).Build()

			// Act: call the function to be tested
			pkgID, err := GetCPUPackageID(cpuID)

			// Assert: assert returns conversion error and -1
			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, atoiErr)
			So(pkgID, ShouldEqual, 0)
		})
	})
}

func Test_GetSocketCount(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test the GetSocketCount function", t, func() {
		mockey.PatchConvey("Successful scenario - There are 2 sockets in the system", func() {
			// Arrange: prepare the test environment and mock
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "cpu0", isDir: true},
				&mockDirEntry{entryName: "cpu1", isDir: true},
				&mockDirEntry{entryName: "cpu2", isDir: true},
				&mockDirEntry{entryName: "cpu3", isDir: true},
				&mockDirEntry{entryName: "not_a_cpu", isDir: true},
				&mockDirEntry{entryName: "a_file", isDir: false},
			}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Simulation os.Stat always succeeds, indicating that the topology directory exists
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			// Simulate os.ReadFile to return different socket ids according to the path
			// cpu0 and cpu1 belong to socket 0
			// cpu2 and cpu3 belong to socket 1
			mockey.Mock(os.ReadFile).To(func(path string) ([]byte, error) {
				if strings.Contains(path, filepath.Join("cpu0", "topology")) || strings.Contains(path, filepath.Join("cpu1", "topology")) {
					return []byte("0\n"), nil
				}
				if strings.Contains(path, filepath.Join("cpu2", "topology")) || strings.Contains(path, filepath.Join("cpu3", "topology")) {
					return []byte("1\n"), nil
				}
				return nil, fmt.Errorf("unexpected file read: %s", path)
			}).Build()

			// Act: execute the function under test
			count, err := GetSocketCount()

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 2)
		})

		// Scenario 2: Failed to read the /sys/devices/system/cpu directory
		mockey.PatchConvey("Failure scenario - failed to read the CPU root directory", func() {
			// Arrange: prepare the test environment and mock
			expectedErr := errors.New("permission denied")
			mockey.Mock(os.ReadDir).Return(nil, expectedErr).Build()

			// Act: execute the function under test
			count, err := GetSocketCount()

			// Assert: assertion results
			So(count, ShouldEqual, -1)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		// Scenario 3: Failed to read the physical_package_id file
		mockey.PatchConvey("Failed scenario - Failed to read physical_package_id file", func() {
			// Arrange: prepare the test environment and mock
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "cpu0", isDir: true},
			}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			expectedErr := errors.New("read error")
			mockey.Mock(os.ReadFile).Return(nil, expectedErr).Build()

			// Act: execute the function under test
			count, err := GetSocketCount()

			// Assert: assertion results
			So(count, ShouldEqual, -1)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		// Scenario 4: There is no topology subdirectory in a certain CPU directory
		mockey.PatchConvey("Edge scene - Some CPUs do not have topology directory", func() {
			// Arrange: prepare the test environment and mock
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "cpu0", isDir: true}, // there is topology
				&mockDirEntry{entryName: "cpu1", isDir: true}, // no topology
			}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Simulate os.Stat, return "not exist" to the topology directory of cpu1
			mockey.Mock(os.Stat).To(func(path string) (os.FileInfo, error) {
				if strings.Contains(path, filepath.Join("cpu1", "topology")) {
					return nil, os.ErrNotExist
				}
				return nil, nil
			}).Build()

			// Simulation os.ReadFile will only be called for cpu0
			mockey.Mock(os.ReadFile).Return([]byte("0\n"), nil).Build()

			// Act: execute the function under test
			count, err := GetSocketCount()

			// Assert: assertion results，cpu1 is skipped, the result should be 1
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 1)
		})

		// Scene 5 there is no directory starting with cpu
		mockey.PatchConvey("Edge scene - Directory without cpu prefix", func() {
			// Arrange: prepare the test environment and mock
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "node0", isDir: true},
				&mockDirEntry{entryName: "some_file", isDir: false},
			}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Act: execute the function under test
			count, err := GetSocketCount()

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)
		})
	})
}

func Test_GetNodeCount(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test the GetNodeCount function", t, func() {
		mockey.PatchConvey("Scenario 1: Successfully obtained the number of nodes", func() {
			// Arrange: Prepare simulation data, including valid node directory, invalid directory and files
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "node0", isDir: true},
				&mockDirEntry{entryName: "node1", isDir: true},
				&mockDirEntry{entryName: "cpu", isDir: true},        // NonNodeDirectory
				&mockDirEntry{entryName: "some_file", isDir: false}, // File
				&mockDirEntry{entryName: "nodexyz", isDir: true},    // InvalidNodeDirectoryName
			}
			// Mock os.ReadDir function to return the simulated data we prepared
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Act: call the function to be tested
			count, err := GetNodeCount()

			// Assert: assertion results are as expected
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 2)
		})

		mockey.PatchConvey("Scenario 2: Failed to read the directory", func() {
			// Arrange: Prepare a simulation error
			mockErr := errors.New("permission denied")
			// Mock os.ReadDir function, causing it to return an error
			mockey.Mock(os.ReadDir).Return(nil, mockErr).Build()

			// Act: call the function to be tested
			count, err := GetNodeCount()

			// Assert: The assert function returns an error and -1
			So(err, ShouldNotBeNil)
			So(count, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "permission denied")
		})

		mockey.PatchConvey("Scenario 3: There is no node in the directory", func() {
			// Arrange: Prepare simulation data that does not contain node directory
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "cpu0", isDir: true},
				&mockDirEntry{entryName: "memory0", isDir: true},
				&mockDirEntry{entryName: "a_file", isDir: false},
			}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Act: call the function to be tested
			count, err := GetNodeCount()

			// Assert: Assert the number of nodes is 0
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)
		})

		mockey.PatchConvey("Scene 4: The directory is empty", func() {
			// Arrange: Prepare an empty directory entry list
			mockEntries := []os.DirEntry{}
			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// Act: call the function to be tested
			count, err := GetNodeCount()

			// Assert the number of nodes is 0
			So(err, ShouldBeNil)
			So(count, ShouldEqual, 0)
		})
	})
}

func TestGetNumaPackageID(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("TestGetNumaPackageID", t, func() {
		mockey.PatchConvey("Scenario 1: The cpulist file does not exist", func() {
			// Arrange: Simulate os.Stat return file without error
			mockey.Mock(os.Stat).Return(nil, os.ErrNotExist).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: Assert returns the expected error and ID
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "not exists")
		})

		mockey.PatchConvey("Scenario 2: Failed to parse cpulist file", func() {
			// Arrange: Simulating os.Stat successfully, but ParseLinuxListFormatFromFile fails
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockErr := errors.New("parse error")
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return(nil, mockErr).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: Assert returns the expected error and ID
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "failed to ParseLinuxListFormatFromFile")
		})

		mockey.PatchConvey("Scene 3: cpulist is not empty, the package id is successfully obtained", func() {
			// Arrange: Simulate and parse the CPU list and successfully obtain the package id
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{4}, nil).Build()
			mockey.Mock(GetCPUPackageID).To(func(cpuID int64) (int, error) {
				So(cpuID, ShouldEqual, 4)
				return 1, nil
			}).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: The assert returns package id successfully
			So(err, ShouldBeNil)
			So(id, ShouldEqual, 1)
		})

		mockey.PatchConvey("Scenario 4: cpulist is not empty, and the package id is failed", func() {
			// Arrange: Simulate parsing the CPU list, but fails to get the package id
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{4}, nil).Build()
			mockErr := errors.New("get package id error")
			mockey.Mock(GetCPUPackageID).Return(-1, mockErr).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: assert that the expected error and ID are returned
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "failed to GetCPUPackageID")
		})

		mockey.PatchConvey("Scenario 5: cpulist is empty, failed to get the socket number", func() {
			// Arrange: Simulate parsing an empty CPU list and failing to get the number of sockets
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{}, nil).Build()
			mockErr := errors.New("get socket count error")
			mockey.Mock(GetSocketCount).Return(-1, mockErr).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: assert that the expected error and ID are returned
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "failed to GetSocketCount")
		})

		mockey.PatchConvey("Scenario 6: cpulist is empty, socket number is 0", func() {
			// Arrange: Simulate to parse empty CPU list, and the number of sockets is 0
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{}, nil).Build()
			mockey.Mock(GetSocketCount).Return(0, nil).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: The error of the assertion returns socket count is 0
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldEqual, "socket count is 0")
		})

		mockey.PatchConvey("Scene 7: cpulist is empty, socket number is 1", func() {
			// Arrange: Simulate to parse empty CPU list, and the number of sockets is 1
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{}, nil).Build()
			mockey.Mock(GetSocketCount).Return(1, nil).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: returns 0 if the assertion succeeds
			So(err, ShouldBeNil)
			So(id, ShouldEqual, 0)
		})

		mockey.PatchConvey("Scenario 8: cpulist is empty, the number of sockets is greater than 1, and the number of nodes is failed", func() {
			// Arrange: Simulate parsing an empty CPU list, the number of sockets is greater than 1, but the number of nodes is failed
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{}, nil).Build()
			mockey.Mock(GetSocketCount).Return(2, nil).Build()
			mockErr := errors.New("get node count error")
			mockey.Mock(GetNodeCount).Return(-1, mockErr).Build()

			// Act: call the function to be tested
			id, err := GetNumaPackageID(0)

			// Assert: assert that the expected error and ID are returned
			So(err, ShouldNotBeNil)
			So(id, ShouldEqual, -1)
			So(err.Error(), ShouldContainSubstring, "failed to GetNodeCount")
		})

		mockey.PatchConvey("Scenario 9: cpulist is empty, the number of sockets is greater than 1, the package id is successfully calculated", func() {
			// Arrange: Simulate and parse the empty CPU list, the number of sockets is greater than 1, and the calculation is successful
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{}, nil).Build()
			mockey.Mock(GetSocketCount).Return(2, nil).Build()
			mockey.Mock(GetNodeCount).Return(4, nil).Build()

			// Act: call the function to be tested，nodeCountPerSocket = 4 / 2 = 2, id = 3 / 2 = 1
			id, err := GetNumaPackageID(3)

			So(err, ShouldBeNil)
			So(id, ShouldEqual, 1)
		})
	})
}

func Test_GetCPUOnlineStatus(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test the GetCPUOnlineStatus function", t, func() {
		// Scenario 1: Test the special situation when cpuID is 0
		mockey.PatchConvey("Scenario 1: When cpuID is 0, true should be returned directly", func() {
			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(0)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(online, ShouldBeTrue)
		})

		// Scenario 2: Test the situation when the CPU directory does not exist
		mockey.PatchConvey("Scenario 2: When the CPU directory does not exist, an error should be returned", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(99)
			cpuName := fmt.Sprintf("cpu%d", cpuID)
			cpuPath := filepath.Join(cpuSysDir, cpuName)

			// Mock os.Stat，Make it return os.ErrNotExist when checking the CPU directory
			mockey.Mock(os.Stat).To(func(name string) (os.FileInfo, error) {
				if name == cpuPath {
					return nil, os.ErrNotExist
				}
				// For other path calls, return success to avoid affecting other logic
				return nil, nil
			}).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(online, ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("cpu %d not exists", cpuID))
		})

		// Scenario 3: Test the default behavior when the online file does not exist
		mockey.PatchConvey("Scenario 3: When the online file does not exist, it should return true by default.", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(1)
			cpuName := fmt.Sprintf("cpu%d", cpuID)
			cpuPath := filepath.Join(cpuSysDir, cpuName)
			cpuOnlineFile := filepath.Join(cpuPath, "online")

			// Mock os.Stat，Make it successful when checking the CPU directory, but return os.ErrNotExist when checking the online file
			mockey.Mock(os.Stat).To(func(name string) (os.FileInfo, error) {
				if name == cpuPath {
					return nil, nil // the cpu directory exists
				}
				if name == cpuOnlineFile {
					return nil, os.ErrNotExist // the online file does not exist
				}
				return nil, nil
			}).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(online, ShouldBeTrue)
		})

		// Scenario 4: Test when the content of the online file is "1" (online)
		mockey.PatchConvey("Scenario 4: When the content of the online file is '1', the CPU should be in the online state", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(2)
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(os.ReadFile).Return([]byte("1\n"), nil).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(online, ShouldBeTrue)
		})

		// Scenario 5: Test when the content of the online file is "0" (offline)
		mockey.PatchConvey("Scenario 5: When the content of the online file is '0', the CPU should be offline", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(3)
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(os.ReadFile).Return([]byte("0\n"), nil).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(online, ShouldBeFalse)
		})

		// Scenario 6: Testing failure to read online files
		mockey.PatchConvey("Scenario 6: When reading the online file fails, an error should be returned", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(4)
			expectedErr := errors.New("mock read file error")
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(os.ReadFile).Return(nil, expectedErr).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(online, ShouldBeFalse)
			So(err, ShouldEqual, expectedErr)
		})

		// Scenario 7: Testing the case where the content of the online file is non-number
		mockey.PatchConvey("Scenario 7: When the content of the online file is non-number, a parsing error should be returned.", func() {
			// Arrange: prepare test data and mock
			cpuID := int64(5)
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(os.ReadFile).Return([]byte("invalid-data\n"), nil).Build()

			// Act: call the function to be tested
			online, err := GetCPUOnlineStatus(cpuID)

			// Assert: assertion results
			So(online, ShouldBeFalse)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "invalid syntax") // strconv.Atoi返回的错误
		})
	})
}

func Test_getLLCDomain(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	const fakeCPUListFile = "/fake/path/to/cpu_list"
	mockey.PatchConvey("Test getLLCDomain", t, func() {
		mockey.PatchConvey("Scenario 1: Successfully obtaining LLC domain information", func() {
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			// Mock general.ParseLinuxListFormatFromFile
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(filePath string) ([]int64, error) {
				switch filePath {
				case fakeCPUListFile:
					// The main CPU list file returns one CPU for each of the two physical cores.
					return []int64{0, 2}, nil
				case filepath.Join(cpuSysDir, "cpu0/topology/thread_siblings_list"):
					// the brother cpu of physical core 0
					return []int64{0, 1}, nil
				case filepath.Join(cpuSysDir, "cpu2/topology/thread_siblings_list"):
					// the brother cpu of physical core 2
					return []int64{2, 3}, nil
				default:
					return nil, fmt.Errorf("unexpected file path: %s", filePath)
				}
			}).Build()

			// Act: execute the function under test
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(llcDomain, ShouldNotBeNil)
			So(len(llcDomain.PhyCores), ShouldEqual, 2)
			expectedDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1}},
					{CPUs: []int64{2, 3}},
				},
			}
			So(llcDomain, ShouldResemble, expectedDomain)
		})

		mockey.PatchConvey("Scenario 2: The main CPU list file parsing failed", func() {
			mockErr := errors.New("read cpu list file failed")
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return(nil, mockErr).Build()

			// Act: execute the function under test
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
		})

		mockey.PatchConvey("Scenario 3: The topology file of the physical core does not exist", func() {
			mockey.Mock(os.Stat).To(func(name string) (os.FileInfo, error) {
				if name == filepath.Join(cpuSysDir, "cpu2/topology/thread_siblings_list") {
					return nil, os.ErrNotExist // the simulation file does not exist
				}
				return nil, nil
			}).Build()

			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(filePath string) ([]int64, error) {
				switch filePath {
				case fakeCPUListFile:
					return []int64{0, 2}, nil
				case filepath.Join(cpuSysDir, "cpu0/topology/thread_siblings_list"):
					return []int64{0, 1}, nil
				default:
					// The resolution of cpu2 should not be called because os.Stat will fail
					return nil, fmt.Errorf("unexpected file path: %s", filePath)
				}
			}).Build()

			// Act: execute the function under test
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			// Assert: assertion results, should contain only the information of a physical core
			So(err, ShouldBeNil)
			So(llcDomain, ShouldNotBeNil)
			So(len(llcDomain.PhyCores), ShouldEqual, 1)
			expectedDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1}},
				},
			}
			So(llcDomain, ShouldResemble, expectedDomain)
		})

		mockey.PatchConvey("Scenario 4: Physical core CPU list file parsing failed", func() {
			// Arrange
			mockErr := errors.New("read thread_siblings_list failed")
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(filePath string) ([]int64, error) {
				if filePath == fakeCPUListFile {
					return []int64{0}, nil
				}
				return nil, mockErr
			}).Build()

			// Act
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			// Assert
			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
		})

		mockey.PatchConvey("Scenario 5: The physical core CPU list is empty", func() {
			// Arrange
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(filePath string) ([]int64, error) {
				if filePath == fakeCPUListFile {
					return []int64{0}, nil
				}
				return []int64{}, nil
			}).Build()

			// Act
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			// Assert
			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "has 0 cpu")
		})

		mockey.PatchConvey("Scenario 6: The main CPU list file content is empty", func() {
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return(nil, nil).Build()

			// Act
			llcDomain, err := getLLCDomain(fakeCPUListFile)

			// Assert
			So(err, ShouldBeNil)
			So(llcDomain, ShouldNotBeNil)
			So(llcDomain.PhyCores, ShouldBeEmpty)
		})
	})
}

func Test_getIntelNumaTopo(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	const fakeNodeCPUListFile = "/tmp/node0_cpulist"
	mockey.PatchConvey("Test getIntelNumaTopo", t, func() {
		mockey.PatchConvey("Scene 1: Get the LLC Domain normally", func() {
			// Mock general.ParseLinuxListFormatFromFile
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(file string) ([]int64, error) {
				switch file {
				case fakeNodeCPUListFile:
					return []int64{0, 1, 4, 5}, nil
				case filepath.Join(cpuSysDir, "cpu0/topology/thread_siblings_list"):
					return []int64{0, 1}, nil
				case filepath.Join(cpuSysDir, "cpu4/topology/thread_siblings_list"):
					return []int64{4, 5}, nil
				default:
					return nil, fmt.Errorf("unexpected file path: %s", file)
				}
			}).Build()

			mockey.Mock(os.Stat).Return(nil, nil).Build()

			// Act: call the function to be tested
			llcDomain, err := getIntelNumaTopo(fakeNodeCPUListFile)

			// Assert: assertion results
			So(err, ShouldBeNil)
			So(llcDomain, ShouldNotBeNil)
			expectedDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1}},
					{CPUs: []int64{4, 5}},
				},
			}
			So(llcDomain, ShouldResemble, expectedDomain)
		})

		mockey.PatchConvey("Scenario 2: Top-level CPU list file parsing failed", func() {
			expectedErr := errors.New("failed to parse cpu list file")
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return(nil, expectedErr).Build()

			// Act: call the function to be tested
			llcDomain, err := getIntelNumaTopo(fakeNodeCPUListFile)

			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		mockey.PatchConvey("Scenario 3: The CPU topology file of the physical core does not exist", func() {
			mockey.Mock(os.Stat).To(func(name string) (os.FileInfo, error) {
				if name == filepath.Join(cpuSysDir, "cpu4/topology/thread_siblings_list") {
					return nil, os.ErrNotExist
				}
				return nil, nil
			}).Build()

			// Mock general.ParseLinuxListFormatFromFile
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(file string) ([]int64, error) {
				switch file {
				case fakeNodeCPUListFile:
					return []int64{0, 4}, nil
				case filepath.Join(cpuSysDir, "cpu0/topology/thread_siblings_list"):
					return []int64{0, 1}, nil
				default:
					return nil, fmt.Errorf("unexpected file path: %s", file)
				}
			}).Build()

			// Act: call the function to be tested
			llcDomain, err := getIntelNumaTopo(fakeNodeCPUListFile)

			So(err, ShouldBeNil)
			So(llcDomain, ShouldNotBeNil)
			expectedDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1}},
				},
			}
			So(llcDomain, ShouldResemble, expectedDomain)
		})

		mockey.PatchConvey("Scenario 4: The CPU topology file parsing of the physical core failed", func() {
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			expectedErr := errors.New("failed to parse siblings list")
			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(file string) ([]int64, error) {
				if file == fakeNodeCPUListFile {
					return []int64{0}, nil
				}
				return nil, expectedErr
			}).Build()

			// Act: call the function to be tested
			llcDomain, err := getIntelNumaTopo(fakeNodeCPUListFile)

			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		mockey.PatchConvey("Scenario 5: The content of the CPU topology file of the physical core is empty", func() {
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			mockey.Mock(general.ParseLinuxListFormatFromFile).To(func(file string) ([]int64, error) {
				if file == fakeNodeCPUListFile {
					return []int64{0}, nil
				}
				return []int64{}, nil
			}).Build()

			// Act: call the function to be tested
			llcDomain, err := getIntelNumaTopo(fakeNodeCPUListFile)

			So(llcDomain, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "has 0 cpu")
		})
	})
}

func Test_getAMDNumaTopo(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test getAMDNumaTopo", t, func() {
		mockey.PatchConvey("Scenario: Successfully obtained the AMD NUMA topology", func() {
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{0, 8}, nil).Build()
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			mockLLCDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1, 2, 3}},
					{CPUs: []int64{4, 5, 6, 7}},
				},
			}
			mockey.Mock(getLLCDomain).Return(mockLLCDomain, nil).Build()

			// Act: call the function to be tested
			numa, err := getAMDNumaTopo("dummy/node/cpu/list")

			So(err, ShouldBeNil)
			So(numa, ShouldNotBeNil)
			expectedNuma := &AMDNuma{
				CCDs: []*LLCDomain{mockLLCDomain, mockLLCDomain},
			}
			So(numa, ShouldResemble, expectedNuma)
		})

		mockey.PatchConvey("Scenario: The topology was successfully obtained, but some CPUs have been parsed", func() {
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{0, 1}, nil).Build()
			mockey.Mock(os.Stat).Return(nil, nil).Build()

			llcDomainForCpu0 := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 1}},
				},
			}
			getLLCDomainMock := mockey.Mock(getLLCDomain).To(func(path string) (*LLCDomain, error) {
				So(path, ShouldContainSubstring, "cpu0") // Make sure it is called for cpu0
				return llcDomainForCpu0, nil
			}).Build()

			// Act: call the function to be tested
			numa, err := getAMDNumaTopo("dummy/node/cpu/list")

			So(err, ShouldBeNil)
			So(numa, ShouldNotBeNil)
			expectedNuma := &AMDNuma{
				CCDs: []*LLCDomain{llcDomainForCpu0},
			}
			So(numa, ShouldResemble, expectedNuma)
			So(getLLCDomainMock.MockTimes(), ShouldEqual, 1)
		})

		mockey.PatchConvey("Scenario: Failed to parse nodeCPUListFile", func() {
			mockErr := errors.New("read error")
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return(nil, mockErr).Build()

			// Act: call the function to be tested
			numa, err := getAMDNumaTopo("dummy/node/cpu/list")

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to ParseLinuxListFormatFromFile")
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(numa, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario: getLLCDomain failed", func() {
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{0}, nil).Build()
			mockey.Mock(os.Stat).Return(nil, nil).Build()
			mockErr := errors.New("get llc domain error")
			mockey.Mock(getLLCDomain).Return(nil, mockErr).Build()

			// Act: call the function to be tested
			numa, err := getAMDNumaTopo("dummy/node/cpu/list")

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to getLLCDomain")
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(numa, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario: l3_shared_cpu_list file does not exist", func() {
			mockey.Mock(general.ParseLinuxListFormatFromFile).Return([]int64{0}, nil).Build()
			mockey.Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
			getLLCDomainMock := mockey.Mock(getLLCDomain).Return(nil, nil).Build()

			// Act: call the function to be tested
			numa, err := getAMDNumaTopo("dummy/node/cpu/list")

			So(err, ShouldBeNil)
			So(numa, ShouldNotBeNil)
			So(numa.CCDs, ShouldBeEmpty)
			So(getLLCDomainMock.MockTimes(), ShouldEqual, 0)
		})
	})
}

func Test_getSocketCPUList(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test getSocketCPUList", t, func() {
		mockey.Mock(general.SortInt64Slice).To(func(x []int64) {
			sort.Slice(x, func(i, j int) bool {
				return x[i] < x[j]
			})
		}).Build()

		mockey.PatchConvey("Scenario 1: When the CPU manufacturer is Intel", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{
							{CPUs: []int64{3, 1}}, // intentionally use out of order data
							{CPUs: []int64{0, 2}},
						},
					},
					1: {
						PhyCores: []PhyCore{
							{CPUs: []int64{8, 9}},
							{CPUs: []int64{11, 10}},
						},
					},
				},
				AMDNumas: nil,
			}
			expectedCPUList := []int64{0, 1, 2, 3, 8, 9, 10, 11}

			// Act: call the function to be tested
			cpuList := getSocketCPUList(socket, cpuid.Intel)

			So(cpuList, ShouldResemble, expectedCPUList)
		})

		mockey.PatchConvey("Scenario 2: When the CPU manufacturer is AMD", func() {
			// Arrange: Construct the topology of an AMD CPU
			socket := &CPUSocket{
				NumaIDs:    []int{0},
				IntelNumas: nil,
				AMDNumas: map[int]*AMDNuma{
					0: {
						CCDs: []*LLCDomain{
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{5, 4}}, // intentionally use out of order data
									{CPUs: []int64{7, 6}},
								},
							},
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{13, 12}},
									{CPUs: []int64{15, 14}},
								},
							},
						},
					},
				},
			}
			expectedCPUList := []int64{4, 5, 6, 7, 12, 13, 14, 15}

			// Act: call the function to be tested
			cpuList := getSocketCPUList(socket, cpuid.AMD)
			So(cpuList, ShouldResemble, expectedCPUList)
		})

		mockey.PatchConvey("Scenario 3: When the CPU manufacturer is unknown", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{
							{CPUs: []int64{0, 1}},
						},
					},
				},
			}

			cpuList := getSocketCPUList(socket, cpuid.VendorUnknown)
			So(cpuList, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario 4: When there is no NUMA node in the Socket", func() {
			socket := &CPUSocket{
				NumaIDs:    []int{},
				IntelNumas: map[int]*LLCDomain{},
				AMDNumas:   map[int]*AMDNuma{},
			}

			cpuList := getSocketCPUList(socket, cpuid.Intel)
			So(cpuList, ShouldBeEmpty)
		})
	})
}

func Test_GetCPUInfoWithTopo(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test GetCPUInfoWithTopo", t, func() {
		mockey.Mock(os.Stat).Return(nil, nil).Build()
		mockey.PatchConvey("Scenario 1: Successfully obtaining Intel CPU topology", func() {
			// Arrange: Simulate an Intel CPU environment
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.Intel)
			// There is a node0 directory under simulation /sys/devices/system/node
			mockNode0 := &mockDirEntry{entryName: "node0", isDir: true}
			mockey.Mock(os.ReadDir).Return([]fs.DirEntry{mockNode0}, nil).Build()

			mockIntelDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{0, 2}}, // Physical core 0, including logical core 0 and 2
					{CPUs: []int64{1, 3}}, // Physical core 1, containing logical core 1 and 3
				},
			}
			mockey.Mock(getIntelNumaTopo).Return(mockIntelDomain, nil).Build()
			mockey.Mock(GetCPUPackageID).Return(0, nil).Build()
			mockey.Mock(GetCPUOnlineStatus).Return(true, nil).Build()

			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(err, ShouldBeNil)
			So(info, ShouldNotBeNil)
			So(info.CPUVendor, ShouldEqual, cpuid.Intel)
			So(len(info.Sockets), ShouldEqual, 1)
			So(info.Sockets[0], ShouldNotBeNil)
			So(info.Sockets[0].NumaIDs, ShouldResemble, []int{0})
			So(info.SocketCPUs[0], ShouldResemble, []int64{0, 1, 2, 3})
			So(info.CPU2Socket[0], ShouldEqual, 0)
			So(info.CPU2Socket[3], ShouldEqual, 0)
			So(info.CPUOnline[0], ShouldBeTrue)
			So(info.CPUOnline[3], ShouldBeTrue)
		})

		mockey.PatchConvey("Scenario 2: Successfully obtaining AMD CPU topology", func() {
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.AMD)
			// There is a node0 directory under simulation /sys/devices/system/node
			mockNode0 := &mockDirEntry{entryName: "node0", isDir: true}
			mockey.Mock(os.ReadDir).Return([]fs.DirEntry{mockNode0}, nil).Build()

			// Simulate the return result of getAMDNumaTopo, including a CCD
			mockAmdNuma := &AMDNuma{
				CCDs: []*LLCDomain{
					{
						PhyCores: []PhyCore{
							{CPUs: []int64{0, 8}},
							{CPUs: []int64{1, 9}},
						},
					},
				},
			}
			mockey.Mock(getAMDNumaTopo).Return(mockAmdNuma, nil).Build()
			mockey.Mock(GetCPUPackageID).Return(0, nil).Build()
			mockey.Mock(GetCPUOnlineStatus).Return(true, nil).Build()

			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(err, ShouldBeNil)
			So(info, ShouldNotBeNil)
			So(info.CPUVendor, ShouldEqual, cpuid.AMD)
			So(len(info.Sockets), ShouldEqual, 1)
			So(info.Sockets[0], ShouldNotBeNil)
			So(info.Sockets[0].NumaIDs, ShouldResemble, []int{0})
			So(info.SocketCPUs[0], ShouldResemble, []int64{0, 1, 8, 9})
			So(info.CPU2Socket[9], ShouldEqual, 0)
			So(info.CPUOnline[8], ShouldBeTrue)
		})

		mockey.PatchConvey("Scenario 3: CPU manufacturers are not supported", func() {
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.VendorUnknown)
			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(info, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unsupport cpu arch")
		})

		mockey.PatchConvey("Scenario 4: Failed to read the node directory", func() {
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.Intel)
			expectedErr := errors.New("permission denied")
			mockey.Mock(os.ReadDir).Return(nil, expectedErr).Build()

			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(info, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		mockey.PatchConvey("Scenario 5: The offline CPU exists when obtaining the CPU's online status", func() {
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.Intel)
			mockNode0 := &mockDirEntry{entryName: "node0", isDir: true}
			mockey.Mock(os.ReadDir).Return([]fs.DirEntry{mockNode0}, nil).Build()
			mockIntelDomain := &LLCDomain{
				PhyCores: []PhyCore{{CPUs: []int64{0, 1}}},
			}
			mockey.Mock(getIntelNumaTopo).Return(mockIntelDomain, nil).Build()
			mockey.Mock(GetCPUPackageID).Return(0, nil).Build()
			mockey.Mock(GetCPUOnlineStatus).To(func(cpuID int64) (bool, error) {
				if cpuID == 1 {
					return false, nil
				}
				return true, nil
			}).Build()

			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(info, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("offline cpu %d exists in /sys/devices/system/node/nodeX/cpu_list", 1))
		})

		mockey.PatchConvey("Scenario 6: Failed to obtain CPU Package ID", func() {
			mockey.MockValue(&cpuid.CPU.VendorID).To(cpuid.Intel)
			mockNode0 := &mockDirEntry{entryName: "node0", isDir: true}
			mockey.Mock(os.ReadDir).Return([]fs.DirEntry{mockNode0}, nil).Build()
			mockIntelDomain := &LLCDomain{
				PhyCores: []PhyCore{{CPUs: []int64{0}}},
			}
			mockey.Mock(getIntelNumaTopo).Return(mockIntelDomain, nil).Build()

			expectedErr := errors.New("failed to read package id")
			mockey.Mock(GetCPUPackageID).Return(-1, expectedErr).Build()

			// Act: call the function to be tested
			info, err := GetCPUInfoWithTopo()

			So(info, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})
	})
}

func TestCollectCpuStats(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test CollectCpuStats", t, func() {
		mockey.PatchConvey("Scenario 1: Normal situation, successfully parsing CPU statistics information", func() {
			// Arrange: Prepare simulation data and mock
			// Simulate /proc/stat file content
			// - cpu0: Contains all fields
			// - cpu1: Missing guest and guestNice fields
			// - cpu2: The steel, guest, guestNice field is missing
			// - cpu: Aggregation rows should be ignored
			// - other_line: irrelevant to the line, should be ignored
			mockLines := []string{
				"cpu0 100 10 200 300 40 50 60 70 80 90",
				"cpu1 101 11 201 301 41 51 61 71",
				"cpu2 102 12 202 302 42 52 62",
				"cpu  1000 100 2000 3000 400 500 600 700 800 900",
				"other_line 123 456",
			}
			mockey.Mock(general.ReadLines).Return(mockLines, nil).Build()

			// Act: call the function to be tested
			stats, err := CollectCpuStats()

			So(err, ShouldBeNil)
			So(stats, ShouldNotBeNil)

			expectedStats := map[int64]*CPUStat{
				0: {User: 100, Nice: 10, System: 200, Idle: 300, Iowait: 40, Irq: 50, Softirq: 60, Steal: 70, Guest: 80, GuestNice: 90},
				1: {User: 101, Nice: 11, System: 201, Idle: 301, Iowait: 41, Irq: 51, Softirq: 61, Steal: 71, Guest: 0, GuestNice: 0},
				2: {User: 102, Nice: 12, System: 202, Idle: 302, Iowait: 42, Irq: 52, Softirq: 62, Steal: 0, Guest: 0, GuestNice: 0},
			}
			So(stats, ShouldResemble, expectedStats)
		})

		mockey.PatchConvey("Scenario 2: When reading /proc/stat file failed", func() {
			mockErr := errors.New("failed to read file")
			mockey.Mock(general.ReadLines).Return(nil, mockErr).Build()

			// Act: call the function to be tested
			stats, err := CollectCpuStats()

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, mockErr)
			So(stats, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 3: When the content of /proc/stat file contains various format errors", func() {
			mockLines := []string{
				"cpu0 100 10 200 300 40 50 60 70 80 90", // ok
				"cpu_invalid 1 2 3 4 5 6 7",             // invalid cpu id
				"cpu1 1 2 3",                            // insufficient number of columns
				"cpu2 1 not_a_number 3 4 5 6 7",         // statistical value is not a numeric
				"cpu3 103 13 203 303 43 53 63",          // another normal line
			}
			mockey.Mock(general.ReadLines).Return(mockLines, nil).Build()
			// Act: call the function to be tested
			stats, err := CollectCpuStats()

			So(err, ShouldBeNil)

			expectedStats := map[int64]*CPUStat{
				0: {User: 100, Nice: 10, System: 200, Idle: 300, Iowait: 40, Irq: 50, Softirq: 60, Steal: 70, Guest: 80, GuestNice: 90},
				3: {User: 103, Nice: 13, System: 203, Idle: 303, Iowait: 43, Irq: 53, Softirq: 63, Steal: 0, Guest: 0, GuestNice: 0},
			}
			So(stats, ShouldResemble, expectedStats)
		})

		mockey.PatchConvey("Scenario 4: When the file content is empty", func() {
			mockLines := []string{}
			mockey.Mock(general.ReadLines).Return(mockLines, nil).Build()

			// Act: call the function to be tested
			stats, err := CollectCpuStats()
			So(err, ShouldBeNil)
			So(stats, ShouldNotBeNil)
			So(stats, ShouldBeEmpty)
		})
	})
}

func Test_getAMDSocketPhysicalCores(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test the getAMDSocketPhysicalCores function", t, func() {
		mockey.PatchConvey("Scenario 1: Normally, Socket contains multiple NUMA nodes and physical cores", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1},
				AMDNumas: map[int]*AMDNuma{
					0: {
						CCDs: []*LLCDomain{
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{0, 1}},
									{CPUs: []int64{2, 3}},
								},
							},
						},
					},
					1: {
						CCDs: []*LLCDomain{
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{4, 5}},
								},
							},
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{6, 7}},
								},
							},
						},
					},
				},
			}
			expectedCores := []PhyCore{
				{CPUs: []int64{0, 1}},
				{CPUs: []int64{2, 3}},
				{CPUs: []int64{4, 5}},
				{CPUs: []int64{6, 7}},
			}

			result := getAMDSocketPhysicalCores(socket)
			So(result, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Scenario 2: Socket does not contain any NUMA nodes", func() {
			socket := &CPUSocket{
				NumaIDs:  []int{},
				AMDNumas: map[int]*AMDNuma{},
			}

			result := getAMDSocketPhysicalCores(socket)
			// Assert: assertion results are empty slices
			So(result, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario 3: Some NUMA nodes or CCDs are empty", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1, 2},
				AMDNumas: map[int]*AMDNuma{
					0: {
						CCDs: []*LLCDomain{
							{
								PhyCores: []PhyCore{
									{CPUs: []int64{10, 11}},
								},
							},
						},
					},
					1: {
						CCDs: []*LLCDomain{
							{
								PhyCores: []PhyCore{},
							},
						},
					},
					2: {
						CCDs: []*LLCDomain{},
					},
				},
			}
			expectedCores := []PhyCore{
				{CPUs: []int64{10, 11}},
			}

			result := getAMDSocketPhysicalCores(socket)
			So(result, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Scene 4: Socket's AMDNumas is nil", func() {
			socket := &CPUSocket{
				NumaIDs:  []int{},
				AMDNumas: nil,
			}

			result := getAMDSocketPhysicalCores(socket)
			// Assert: assertion results are empty slices
			So(result, ShouldBeEmpty)
		})
	})
}

func Test_getIntelSocketPhysicalCores(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test_getIntelSocketPhysicalCores", t, func() {
		mockey.PatchConvey("Normal scenario: When the socket contains multiple valid NUMA nodes", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{
							{CPUs: []int64{0, 1}},
							{CPUs: []int64{2, 3}},
						},
					},
					1: {
						PhyCores: []PhyCore{
							{CPUs: []int64{4, 5}},
						},
					},
				},
			}
			expectedCores := []PhyCore{
				{CPUs: []int64{0, 1}},
				{CPUs: []int64{2, 3}},
				{CPUs: []int64{4, 5}},
			}

			// Act: call the function to be tested
			result := getIntelSocketPhysicalCores(socket)

			So(result, ShouldNotBeNil)
			So(result, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Boundary Scenario: When NumaIDs are empty", func() {
			socket := &CPUSocket{
				NumaIDs: []int{},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{{CPUs: []int64{0, 1}}},
					},
				},
			}

			// Act: call the function to be tested
			result := getIntelSocketPhysicalCores(socket)

			So(result, ShouldBeEmpty)
		})

		mockey.PatchConvey("Boundary scenario: When the PhyCores of a NUMA node is empty", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{
							{CPUs: []int64{0, 1}},
						},
					},
					1: {
						PhyCores: nil,
					},
				},
			}
			expectedCores := []PhyCore{
				{CPUs: []int64{0, 1}},
			}

			// Act: call the function to be tested
			result := getIntelSocketPhysicalCores(socket)

			So(result, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Exception scenario: panic should occur when IntelNumas maps to nil", func() {
			socket := &CPUSocket{
				NumaIDs:    []int{0},
				IntelNumas: nil, // IntelNumas map为nil
			}

			So(func() {
				getIntelSocketPhysicalCores(socket)
			}, ShouldPanic)
		})

		mockey.PatchConvey("Exception scenario: panic should occur when NumaID does not exist in IntelNumas", func() {
			socket := &CPUSocket{
				NumaIDs: []int{0, 1},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{{CPUs: []int64{0, 1}}},
					},
				},
			}

			So(func() {
				getIntelSocketPhysicalCores(socket)
			}, ShouldPanic)
		})
	})
}

func Test_CPUInfo_GetSocketSlice(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test CPUInfo GetSocketSlice", t, func() {
		mockey.PatchConvey("Scenario 1: When Sockets map is empty", func() {
			cpuInfo := &CPUInfo{
				Sockets: make(map[int]*CPUSocket),
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario 2: When Sockets map is nil", func() {
			cpuInfo := &CPUInfo{
				Sockets: nil,
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario 3: When Sockets map contains an element", func() {
			cpuInfo := &CPUInfo{
				Sockets: map[int]*CPUSocket{
					1: {},
				},
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldNotBeEmpty)
			So(len(result), ShouldEqual, 1)
			So(result, ShouldResemble, []int{1})
		})

		mockey.PatchConvey("Scenario 4: When Sockets map contains multiple out-of-order elements", func() {
			cpuInfo := &CPUInfo{
				Sockets: map[int]*CPUSocket{
					2: {},
					0: {},
					3: {},
					1: {},
				},
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldNotBeEmpty)
			So(len(result), ShouldEqual, 4)
			So(result, ShouldResemble, []int{0, 1, 2, 3})
		})
	})
}

func Test_CPUInfo_GetSocketPhysicalCores(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test CPUInfo.GetSocketPhysicalCores", t, func() {
		cpuInfo := &CPUInfo{
			Sockets: map[int]*CPUSocket{
				0: {}, // Socket 0
			},
		}

		mockey.PatchConvey("Scenario 1: When socketID is negative, nil should be returned", func() {
			cores := cpuInfo.GetSocketPhysicalCores(-1)
			So(cores, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 2: When the socketID exceeds the length of Sockets, nil should be returned", func() {
			cores := cpuInfo.GetSocketPhysicalCores(2)
			So(cores, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 3: When the CPU is AMD, getAMDSocketPhysicalCores should be called and its result should be returned.", func() {
			// Arrange: prepare test data and mock
			amdCPUInfo := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						AMDNumas: map[int]*AMDNuma{
							0: {
								CCDs: []*LLCDomain{
									{
										PhyCores: []PhyCore{
											{CPUs: []int64{0, 1}}, // Physical core 0, including logical core 0 and 1
										},
									},
								},
							},
						},
					},
				},
			}
			expectedCores := []PhyCore{{CPUs: []int64{0, 1}}}
			mockey.Mock(getAMDSocketPhysicalCores).Return(expectedCores).Build()

			cores := amdCPUInfo.GetSocketPhysicalCores(0)
			So(cores, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Scenario 4: When the CPU is Intel, getIntelSocketPhysicalCores should be called and its result should be returned.", func() {
			// Arrange: prepare test data and mock
			intelCPUInfo := &CPUInfo{
				CPUVendor: cpuid.Intel,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						IntelNumas: map[int]*LLCDomain{
							0: {
								PhyCores: []PhyCore{
									{CPUs: []int64{0, 1}}, // Physical core 0, including logical core 0 and 1
								},
							},
						},
					},
				},
			}
			expectedCores := []PhyCore{{CPUs: []int64{0, 1}}}
			mockey.Mock(getIntelSocketPhysicalCores).Return(expectedCores).Build()

			cores := intelCPUInfo.GetSocketPhysicalCores(0)
			So(cores, ShouldResemble, expectedCores)
		})

		mockey.PatchConvey("Scenario 5: When the CPU is another manufacturer, nil should be returned", func() {
			otherCPUInfo := &CPUInfo{
				CPUVendor: cpuid.VendorUnknown,
				Sockets: map[int]*CPUSocket{
					0: {},
				},
			}

			cores := otherCPUInfo.GetSocketPhysicalCores(0)
			So(cores, ShouldBeNil)
		})
	})
}

func Test_CPUInfo_GetNodeCPUList(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test CPUInfo GetNodeCPUList", t, func() {
		mockey.Mock(general.SortInt64Slice).To(func(x []int64) {
			sort.Slice(x, func(i, j int) bool {
				return x[i] < x[j]
			})
		}).Build()

		mockey.PatchConvey("Scenario 1: Intel CPU, successfully find the node and return to the CPU list", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.Intel,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						IntelNumas: map[int]*LLCDomain{
							0: {
								PhyCores: []PhyCore{
									{CPUs: []int64{1, 5}}, // CPU core1
									{CPUs: []int64{0, 4}}, // CPU core0
								},
							},
						},
					},
					1: {
						NumaIDs: []int{1},
						IntelNumas: map[int]*LLCDomain{
							1: {
								PhyCores: []PhyCore{
									{CPUs: []int64{2, 6}},
									{CPUs: []int64{3, 7}},
								},
							},
						},
					},
				},
			}
			nodeID := 0
			expectedCPUs := []int64{0, 1, 4, 5}

			// Act: call the function to be tested
			cpuList := c.GetNodeCPUList(nodeID)

			// Assert: assertion results
			So(cpuList, ShouldNotBeNil)
			So(cpuList, ShouldResemble, expectedCPUs)
		})

		mockey.PatchConvey("Scenario 2: AMD CPU, successfully finds the node and returns to the CPU list", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						AMDNumas: map[int]*AMDNuma{
							0: {
								CCDs: []*LLCDomain{
									{
										PhyCores: []PhyCore{
											{CPUs: []int64{1, 9}},
											{CPUs: []int64{0, 8}},
										},
									},
									{
										PhyCores: []PhyCore{
											{CPUs: []int64{3, 11}},
											{CPUs: []int64{2, 10}},
										},
									},
								},
							},
						},
					},
				},
			}
			nodeID := 0
			expectedCPUs := []int64{0, 1, 2, 3, 8, 9, 10, 11}

			// Act: call the function to be tested
			cpuList := c.GetNodeCPUList(nodeID)

			// Assert: assertion results
			So(cpuList, ShouldNotBeNil)
			So(cpuList, ShouldResemble, expectedCPUs)
		})

		mockey.PatchConvey("Scenario 3: The node ID does not exist", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.Intel,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						IntelNumas: map[int]*LLCDomain{
							0: {
								PhyCores: []PhyCore{
									{CPUs: []int64{0, 4}},
								},
							},
						},
					},
				},
			}
			nonExistentNodeID := 99

			// Act: call the function to be tested
			cpuList := c.GetNodeCPUList(nonExistentNodeID)

			// Assert: assertion results为nil
			So(cpuList, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 4: Unsupported CPU architecture", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.VendorUnknown,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
					},
				},
			}
			nodeID := 0

			// Act: call the function to be tested
			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario 5: The node exists but does not have a CPU core", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.Intel,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						IntelNumas: map[int]*LLCDomain{
							0: {
								PhyCores: []PhyCore{},
							},
						},
					},
				},
			}
			nodeID := 0

			// Act: call the function to be tested
			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldBeEmpty)
		})
	})
}

func Test_CPUInfo_GetAMDNumaCCDs(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test CPUInfo.GetAMDNumaCCDs", t, func() {
		mockey.PatchConvey("Scenario 1: The CPU architecture is not AMD, an error should be returned", func() {
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.Intel,
			}
			ccds, err := cpuInfo.GetAMDNumaCCDs(0)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "cpu arch is not amd")
			So(ccds, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 2: The CPU is AMD but the specified Node ID cannot be found, an error should be returned", func() {
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						AMDNumas: map[int]*AMDNuma{
							0: {
								CCDs: []*LLCDomain{},
							},
						},
					},
				},
			}
			targetNodeID := 1
			ccds, err := cpuInfo.GetAMDNumaCCDs(targetNodeID)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to find node %d", targetNodeID))
			So(ccds, ShouldBeNil)
		})

		mockey.PatchConvey("Scenario 3: Successfully found Node and return CCDs (single Socket)", func() {
			expectedCCDs := []*LLCDomain{
				{PhyCores: []PhyCore{{CPUs: []int64{0, 1, 2, 3}}}},
				{PhyCores: []PhyCore{{CPUs: []int64{4, 5, 6, 7}}}},
			}
			targetNodeID := 0
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{targetNodeID},
						AMDNumas: map[int]*AMDNuma{
							targetNodeID: {
								CCDs: expectedCCDs,
							},
						},
					},
				},
			}

			ccds, err := cpuInfo.GetAMDNumaCCDs(targetNodeID)
			So(err, ShouldBeNil)
			So(ccds, ShouldNotBeNil)
			So(ccds, ShouldResemble, expectedCCDs)
		})

		mockey.PatchConvey("Scenario 4: Find Node successfully and return CCDs (multiple Sockets)", func() {
			expectedCCDs := []*LLCDomain{
				{PhyCores: []PhyCore{{CPUs: []int64{16, 17, 18, 19}}}},
			}
			targetNodeID := 2
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0, 1},
						AMDNumas: map[int]*AMDNuma{
							0: {CCDs: []*LLCDomain{}},
							1: {CCDs: []*LLCDomain{}},
						},
					},
					1: {
						NumaIDs: []int{targetNodeID},
						AMDNumas: map[int]*AMDNuma{
							targetNodeID: {
								CCDs: expectedCCDs,
							},
						},
					},
				},
			}

			ccds, err := cpuInfo.GetAMDNumaCCDs(targetNodeID)
			So(err, ShouldBeNil)
			So(ccds, ShouldNotBeNil)
			So(ccds, ShouldResemble, expectedCCDs)
		})

		mockey.PatchConvey("Scenario 5: Node is found but its CCDs list is empty", func() {
			targetNodeID := 0
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.AMD,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{targetNodeID},
						AMDNumas: map[int]*AMDNuma{
							targetNodeID: {
								CCDs: []*LLCDomain{},
							},
						},
					},
				},
			}

			ccds, err := cpuInfo.GetAMDNumaCCDs(targetNodeID)
			So(err, ShouldBeNil)
			So(ccds, ShouldBeEmpty)
		})
	})
}

func Test_GetLLCDomainCPUList(t *testing.T) {
	t.Parallel()
	networkMockLock.Lock()
	defer networkMockLock.Unlock()

	mockey.PatchConvey("Test GetLLCDomainCPUList", t, func() {
		mockey.Mock(general.SortInt64Slice).To(func(x []int64) {
			sort.Slice(x, func(i, j int) bool {
				return x[i] < x[j]
			})
		}).Build()

		mockey.PatchConvey("Scenario: Normally, including multiple physical cores and out-of-order CPUs", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{3, 1, 5}},
					{CPUs: []int64{4, 2}},
				},
			}
			expected := []int64{1, 2, 3, 4, 5}

			// Act: call the function to be tested
			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldResemble, expected)
		})

		mockey.PatchConvey("Scenario: LLCDomain does not contain any physical cores", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{},
			}

			// Act: call the function to be tested
			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldBeEmpty)
		})

		mockey.PatchConvey("Scenario: Some physical cores do not include CPU", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{3, 1}},
					{CPUs: []int64{}},
					{CPUs: []int64{2}},
				},
			}
			expected := []int64{1, 2, 3}

			// Act: call the function to be tested
			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldResemble, expected)
		})

		mockey.PatchConvey("Scenario: input is nil", func() {
			var llcDomain *LLCDomain = nil
			So(func() {
				GetLLCDomainCPUList(llcDomain)
			}, ShouldPanic)
		})
	})
}
