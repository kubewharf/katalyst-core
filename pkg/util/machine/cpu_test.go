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
	"os"
	"sort"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/klauspost/cpuid/v2"
	"github.com/pkg/errors"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func TestGetCoreNumReservedForReclaim(t *testing.T) {
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

func Test_GetNodeCount(t *testing.T) {
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

func Test_getSocketCPUList(t *testing.T) {
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

func Test_getAMDSocketPhysicalCores(t *testing.T) {
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
