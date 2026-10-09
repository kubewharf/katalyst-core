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
	"path/filepath"
	"testing"

	"github.com/klauspost/cpuid/v2"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"
)

func TestGetCoreNumReservedForReclaim(t *testing.T) {
	tests := []struct {
		name             string
		numReservedCores int
		numNumaNodes     int
		cpusPerCore      int
		want             map[int]int
	}{
		{
			name:             "reserve 4",
			numReservedCores: 4,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 2",
			numReservedCores: 2,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 3",
			numReservedCores: 3,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 0",
			numReservedCores: 0,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 1",
			numReservedCores: 1,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 1, 1: 1, 2: 1, 3: 1},
		},
		{
			name:             "reserve 8",
			numReservedCores: 8,
			numNumaNodes:     4,
			cpusPerCore:      1,
			want:             map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
		{
			// SMT2: a per-NUMA magnitude of 1 CPU rounds UP to a complete core
			// (2 CPUs) so no reserve seeds a half core.
			name:             "smt2 rounds per numa up to core",
			numReservedCores: 4,
			numNumaNodes:     4,
			cpusPerCore:      2,
			want:             map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
		{
			// SMT2: distribute complete cores until the aggregate reaches 12.
			name:             "smt2 preserves dense even split",
			numReservedCores: 12,
			numNumaNodes:     4,
			cpusPerCore:      2,
			want:             map[int]int{0: 4, 1: 4, 2: 4, 3: 4},
		},
		{
			// The legacy dense API divides first and intentionally drops the
			// global remainder instead of redistributing it across low IDs.
			name:             "smt2 preserves dense remainder semantics",
			numReservedCores: 10,
			numNumaNodes:     4,
			cpusPerCore:      2,
			want:             map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := GetCoreNumReservedForReclaim(tt.numReservedCores, tt.numNumaNodes, tt.cpusPerCore)
			assert.Equal(t, tt.want, r)
		})
	}
}

// writeNodeSysfs lays out a fake /sys/devices/system/node directory under root
// and returns it. Each entry in names is materialized as either a directory
// (node*) or a plain file, mirroring the real sysfs layout.
func writeNodeSysfs(t *testing.T, root string, names []string) string {
	t.Helper()
	nodeDir := filepath.Join(root, "sys", "devices", "system", "node")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", nodeDir, err)
	}
	for _, name := range names {
		p := filepath.Join(nodeDir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	return nodeDir
}

func Test_GetNodeCount(t *testing.T) {
	t.Parallel()

	t.Run("valid node dirs counted, others skipped", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		nodeDir := writeNodeSysfs(t, root, []string{"node0", "node1", "cpu"})
		// a plain (non-dir) file must be ignored
		if err := os.WriteFile(filepath.Join(nodeDir, "some_file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		// a directory prefixed with node but not numeric must be ignored
		if err := os.MkdirAll(filepath.Join(nodeDir, "nodexyz"), 0o755); err != nil {
			t.Fatal(err)
		}

		count, err := getNodeCount(nodeDir)
		assert.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("missing dir returns error", func(t *testing.T) {
		t.Parallel()
		// a path that does not exist triggers the ReadDir error branch.
		count, err := getNodeCount(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Error(t, err)
		assert.Equal(t, -1, count)
	})

	t.Run("no node directory returns zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		nodeDir := writeNodeSysfs(t, root, []string{"cpu0", "memory0"})
		if err := os.WriteFile(filepath.Join(nodeDir, "a_file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		count, err := getNodeCount(nodeDir)
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("empty directory returns zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		nodeDir := writeNodeSysfs(t, root, nil)

		count, err := getNodeCount(nodeDir)
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func Test_getSocketCPUList(t *testing.T) {
	t.Parallel()

	Convey("Test getSocketCPUList", t, func() {
		Convey("Scenario 1: When the CPU manufacturer is Intel", func() {
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

			cpuList := getSocketCPUList(socket, cpuid.Intel)
			So(cpuList, ShouldResemble, expectedCPUList)
		})

		Convey("Scenario 2: When the CPU manufacturer is AMD", func() {
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

			cpuList := getSocketCPUList(socket, cpuid.AMD)
			So(cpuList, ShouldResemble, expectedCPUList)
		})

		Convey("Scenario 3: When the CPU manufacturer is unknown", func() {
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

		Convey("Scenario 4: When there is no NUMA node in the Socket", func() {
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
	t.Parallel()

	Convey("Test the getAMDSocketPhysicalCores function", t, func() {
		Convey("Scenario 1: Normally, Socket contains multiple NUMA nodes and physical cores", func() {
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

		Convey("Scenario 2: Socket does not contain any NUMA nodes", func() {
			socket := &CPUSocket{
				NumaIDs:  []int{},
				AMDNumas: map[int]*AMDNuma{},
			}

			result := getAMDSocketPhysicalCores(socket)
			So(result, ShouldBeEmpty)
		})

		Convey("Scenario 3: Some NUMA nodes or CCDs are empty", func() {
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

		Convey("Scene 4: Socket's AMDNumas is nil", func() {
			socket := &CPUSocket{
				NumaIDs:  []int{},
				AMDNumas: nil,
			}

			result := getAMDSocketPhysicalCores(socket)
			So(result, ShouldBeEmpty)
		})
	})
}

func Test_getIntelSocketPhysicalCores(t *testing.T) {
	t.Parallel()

	Convey("Test_getIntelSocketPhysicalCores", t, func() {
		Convey("Normal scenario: When the socket contains multiple valid NUMA nodes", func() {
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

			result := getIntelSocketPhysicalCores(socket)
			So(result, ShouldNotBeNil)
			So(result, ShouldResemble, expectedCores)
		})

		Convey("Boundary Scenario: When NumaIDs are empty", func() {
			socket := &CPUSocket{
				NumaIDs: []int{},
				IntelNumas: map[int]*LLCDomain{
					0: {
						PhyCores: []PhyCore{{CPUs: []int64{0, 1}}},
					},
				},
			}

			result := getIntelSocketPhysicalCores(socket)
			So(result, ShouldBeEmpty)
		})

		Convey("Boundary scenario: When the PhyCores of a NUMA node is empty", func() {
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

			result := getIntelSocketPhysicalCores(socket)
			So(result, ShouldResemble, expectedCores)
		})

		Convey("Exception scenario: panic should occur when IntelNumas maps to nil", func() {
			socket := &CPUSocket{
				NumaIDs:    []int{0},
				IntelNumas: nil, // IntelNumas map为nil
			}

			So(func() {
				getIntelSocketPhysicalCores(socket)
			}, ShouldPanic)
		})

		Convey("Exception scenario: panic should occur when NumaID does not exist in IntelNumas", func() {
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

	Convey("Test CPUInfo GetSocketSlice", t, func() {
		Convey("Scenario 1: When Sockets map is empty", func() {
			cpuInfo := &CPUInfo{
				Sockets: make(map[int]*CPUSocket),
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldBeEmpty)
		})

		Convey("Scenario 2: When Sockets map is nil", func() {
			cpuInfo := &CPUInfo{
				Sockets: nil,
			}

			result := cpuInfo.GetSocketSlice()
			So(result, ShouldBeEmpty)
		})

		Convey("Scenario 3: When Sockets map contains an element", func() {
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

		Convey("Scenario 4: When Sockets map contains multiple out-of-order elements", func() {
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

	Convey("Test CPUInfo.GetSocketPhysicalCores", t, func() {
		Convey("Scenario 1: When socketID is negative, nil should be returned", func() {
			cpuInfo := &CPUInfo{
				Sockets: map[int]*CPUSocket{
					0: {},
				},
			}

			cores := cpuInfo.GetSocketPhysicalCores(-1)
			So(cores, ShouldBeNil)
		})

		Convey("Scenario 2: When the socketID exceeds the length of Sockets, nil should be returned", func() {
			cpuInfo := &CPUInfo{
				Sockets: map[int]*CPUSocket{
					0: {},
				},
			}

			cores := cpuInfo.GetSocketPhysicalCores(2)
			So(cores, ShouldBeNil)
		})

		Convey("Scenario 3: When the CPU is AMD, the AMD physical cores are returned", func() {
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
											{CPUs: []int64{0, 1}},
										},
									},
								},
							},
						},
					},
				},
			}
			expectedCores := []PhyCore{{CPUs: []int64{0, 1}}}

			cores := amdCPUInfo.GetSocketPhysicalCores(0)
			So(cores, ShouldResemble, expectedCores)
		})

		Convey("Scenario 4: When the CPU is Intel, the Intel physical cores are returned", func() {
			intelCPUInfo := &CPUInfo{
				CPUVendor: cpuid.Intel,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
						IntelNumas: map[int]*LLCDomain{
							0: {
								PhyCores: []PhyCore{
									{CPUs: []int64{0, 1}},
								},
							},
						},
					},
				},
			}
			expectedCores := []PhyCore{{CPUs: []int64{0, 1}}}

			cores := intelCPUInfo.GetSocketPhysicalCores(0)
			So(cores, ShouldResemble, expectedCores)
		})

		Convey("Scenario 5: When the CPU is another manufacturer, nil should be returned", func() {
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

	Convey("Test CPUInfo GetNodeCPUList", t, func() {
		Convey("Scenario 1: Intel CPU, successfully find the node and return to the CPU list", func() {
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

			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldNotBeNil)
			So(cpuList, ShouldResemble, expectedCPUs)
		})

		Convey("Scenario 2: AMD CPU, successfully finds the node and returns to the CPU list", func() {
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

			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldNotBeNil)
			So(cpuList, ShouldResemble, expectedCPUs)
		})

		Convey("Scenario 3: The node ID does not exist", func() {
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

			cpuList := c.GetNodeCPUList(nonExistentNodeID)
			So(cpuList, ShouldBeNil)
		})

		Convey("Scenario 4: Unsupported CPU architecture", func() {
			c := &CPUInfo{
				CPUVendor: cpuid.VendorUnknown,
				Sockets: map[int]*CPUSocket{
					0: {
						NumaIDs: []int{0},
					},
				},
			}
			nodeID := 0

			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldBeEmpty)
		})

		Convey("Scenario 5: The node exists but does not have a CPU core", func() {
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

			cpuList := c.GetNodeCPUList(nodeID)
			So(cpuList, ShouldBeEmpty)
		})
	})
}

func Test_CPUInfo_GetAMDNumaCCDs(t *testing.T) {
	t.Parallel()

	Convey("Test CPUInfo.GetAMDNumaCCDs", t, func() {
		Convey("Scenario 1: The CPU architecture is not AMD, an error should be returned", func() {
			cpuInfo := &CPUInfo{
				CPUVendor: cpuid.Intel,
			}
			ccds, err := cpuInfo.GetAMDNumaCCDs(0)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "cpu arch is not amd")
			So(ccds, ShouldBeNil)
		})

		Convey("Scenario 2: The CPU is AMD but the specified Node ID cannot be found, an error should be returned", func() {
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

		Convey("Scenario 3: Successfully found Node and return CCDs (single Socket)", func() {
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

		Convey("Scenario 4: Find Node successfully and return CCDs (multiple Sockets)", func() {
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

		Convey("Scenario 5: Node is found but its CCDs list is empty", func() {
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

	Convey("Test GetLLCDomainCPUList", t, func() {
		Convey("Scenario: Normally, including multiple physical cores and out-of-order CPUs", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{3, 1, 5}},
					{CPUs: []int64{4, 2}},
				},
			}
			expected := []int64{1, 2, 3, 4, 5}

			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldResemble, expected)
		})

		Convey("Scenario: LLCDomain does not contain any physical cores", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{},
			}

			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldBeEmpty)
		})

		Convey("Scenario: Some physical cores do not include CPU", func() {
			llcDomain := &LLCDomain{
				PhyCores: []PhyCore{
					{CPUs: []int64{3, 1}},
					{CPUs: []int64{}},
					{CPUs: []int64{2}},
				},
			}
			expected := []int64{1, 2, 3}

			result := GetLLCDomainCPUList(llcDomain)
			So(result, ShouldResemble, expected)
		})

		Convey("Scenario: input is nil", func() {
			var llcDomain *LLCDomain = nil
			So(func() {
				GetLLCDomainCPUList(llcDomain)
			}, ShouldPanic)
		})
	})
}
