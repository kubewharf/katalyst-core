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

package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	. "github.com/bytedance/mockey"
	"github.com/moby/sys/mountinfo"
	"github.com/prometheus/procfs"
	"github.com/safchain/ethtool"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/vishvananda/netns"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	procm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
	sysm "github.com/kubewharf/katalyst-core/pkg/util/sysfs/manager"
)

// MockFileInfo is a mock implementation of os.FileInfo for testing purposes.
type MockFileInfo struct {
	name    string
	isDir   bool
	size    int64
	mode    os.FileMode
	modTime time.Time
	sys     interface{}
}

func (m MockFileInfo) Name() string       { return m.name }
func (m MockFileInfo) IsDir() bool        { return m.isDir }
func (m MockFileInfo) Size() int64        { return m.size }
func (m MockFileInfo) Mode() os.FileMode  { return m.mode }
func (m MockFileInfo) ModTime() time.Time { return m.modTime }
func (m MockFileInfo) Sys() interface{}   { return m.sys }

func Test_Network(t *testing.T) {
	testDoNetNS(t)
	testGetNSNetworkHardwareTopology(t)
	testGetExtraNetworkInfo(t)
}

func testGetExtraNetworkInfo(t *testing.T) {
	PatchConvey("Test GetExtraNetworkInfo", t, func() {
		// Mock dependencies and setup
		conf := &global.MachineInfoConfiguration{}

		PatchConvey("Test NetMultipleNS is false", func() {
			conf.NetMultipleNS = false
			PatchConvey("Test getNSNetworkHardwareTopology success", func() {
				expectedNics := []InterfaceInfo{{Name: "eth0", IfIndex: 1, Speed: 1000, NumaNode: 0, Enable: true}}
				Mock(getNSNetworkHardwareTopology).Return(expectedNics, nil).Build()
				actual, err := GetExtraNetworkInfo(conf)
				So(actual, ShouldNotBeNil)
				So(err, ShouldBeNil)
				So(actual.Interface, ShouldResemble, expectedNics)
			})
			PatchConvey("Test getNSNetworkHardwareTopology failed", func() {
				Mock(getNSNetworkHardwareTopology).Return(nil, errors.New("failed to get network topology")).Build()
				actual, err := GetExtraNetworkInfo(conf)
				So(actual, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})

		PatchConvey("Test NetMultipleNS is true", func() {
			conf.NetMultipleNS = true
			PatchConvey("Test NetNSDirAbsPath is empty", func() {
				conf.NetNSDirAbsPath = ""
				actual, err := GetExtraNetworkInfo(conf)
				So(actual, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})

			PatchConvey("Test ReadDir failed", func() {
				conf.NetNSDirAbsPath = "/valid/path"
				Mock(ioutil.ReadDir).Return(nil, errors.New("failed to read directory")).Build()
				actual, err := GetExtraNetworkInfo(conf)
				So(actual, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})

			PatchConvey("Test ReadDir success", func() {
				conf.NetNSDirAbsPath = "/valid/path"
				Mock(ioutil.ReadDir).Return([]os.FileInfo{
					MockFileInfo{name: "ns1", isDir: true},
					MockFileInfo{name: "ns2", isDir: true},
				}, nil).Build()
				PatchConvey("Test getNSNetworkHardwareTopology for all namespaces success", func() {
					expectedNics := []InterfaceInfo{
						{Name: "eth0", IfIndex: 1, Speed: 1000, NumaNode: 0, Enable: true},
						{Name: "eth1", IfIndex: 2, Speed: 1000, NumaNode: 1, Enable: true},
					}
					Mock(getNSNetworkHardwareTopology).Return(expectedNics, nil).Build()
					actual, err := GetExtraNetworkInfo(conf)
					So(actual, ShouldNotBeNil)
					So(err, ShouldBeNil)
					So(len(actual.Interface), ShouldEqual, len(expectedNics)) // Since we have two namespaces
				})
				PatchConvey("Test getNSNetworkHardwareTopology for some namespaces failed", func() {
					Mock(getNSNetworkHardwareTopology).Return(nil, errors.New("failed to get network topology")).Build()
					actual, err := GetExtraNetworkInfo(conf)
					So(actual, ShouldNotBeNil)
					So(err, ShouldBeNil)
					So(len(actual.Interface), ShouldEqual, 0) // Since all getNSNetworkHardwareTopology calls failed
				})
			})
		})
	})
}

func testGetNSNetworkHardwareTopology(t *testing.T) {
	nsName := "test-ns"
	netNSDirAbsPath := "/path/to/netns"

	PatchConvey("Test DoNetNS failed", t, func() {
		Mock(DoNetNS).Return(errors.New("DoNetNS failed")).Build()
		actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
		So(actual, ShouldBeNil)
		So(err, ShouldNotBeNil)
	})

	PatchConvey("Test DoNetNS success", t, func() {
		Mock(DoNetNS).To(func(nsName, netNSDirAbsPath string, cb func(sysFsDir string) error) error {
			return cb("")
		}).Build()
		PatchConvey("Test os.ReadDir failed", func() {
			Mock(os.ReadDir).Return(nil, errors.New("ReadDir failed")).Build()
			actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
			So(actual, ShouldBeNil)
			So(err, ShouldNotBeNil)
		})

		PatchConvey("Test os.ReadDir success", func() {
			Mock(os.ReadDir).Return([]os.DirEntry{
				fs.FileInfoToDirEntry(MockFileInfo{name: "eth0"}),
			}, nil).Build()
			PatchConvey("Test getInterfaceAddr failed", func() {
				Mock(getInterfaceAddr).Return(nil, errors.New("getInterfaceAddr failed")).Build()
				actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
				So(actual, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})

			PatchConvey("Test getInterfaceAddr success", func() {
				Mock(getInterfaceAddr).Return(map[string]*IfaceAddr{
					"eth0": {},
				}, nil).Build()
				PatchConvey("Test filepath.EvalSymlinks failed", func() {
					Mock(filepath.EvalSymlinks).Return("", errors.New("EvalSymlinks failed")).Build()
					actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
					So(actual, ShouldBeNil)
					So(err, ShouldBeNil)
				})

				PatchConvey("Test filepath.EvalSymlinks success", func() {
					Mock(filepath.EvalSymlinks).Return("/path/to/device", nil).Build()
					PatchConvey("Test NIC is not PCI and not in bondingNICs", func() {
						Mock(getBondingNetworkInterfaces).Return(sets.String{}).Build()
						actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
						So(actual, ShouldBeEmpty)
						So(err, ShouldBeNil)
					})

					PatchConvey("Test NIC is PCI or in bondingNICs", func() {
						Mock(getBondingNetworkInterfaces).Return(sets.NewString("eth0")).Build()
						PatchConvey("Test getInterfaceIndex and getInterfaceAttr", func() {
							// Mock getInterfaceIndex and getInterfaceAttr to modify the InterfaceInfo object
							Mock(getInterfaceIndex).Build()
							Mock(getInterfaceAttr).Build()
							actual, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
							So(actual, ShouldNotBeEmpty)
							So(err, ShouldBeNil)
						})
					})
				})
			})
		})
	})
}

// testDoNetNS tests the DoNetNS function with various scenarios using table-driven tests.
func testDoNetNS(t *testing.T) {
	// Define test cases
	testCases := []struct {
		name                   string
		nsName                 string
		netNSDirAbsPath        string
		mockSetup              func()
		expectError            bool
		expectedErrorSubstring string
		cb                     func(sysFsDir string) error
	}{
		{
			name:            "Default namespace",
			nsName:          DefaultNICNamespace,
			netNSDirAbsPath: "/test/path",
			mockSetup:       func() {},
			expectError:     false,
			cb: func(sysFsDir string) error {
				So(sysFsDir, ShouldEqual, sysFSDirNormal)
				return nil
			},
		},
		{
			name:            "Custom namespace - Success case",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, nil).Build()
				Mock(mountinfo.Mounted).Return(false, nil).Build()
				Mock(syscall.Mount).Return(nil).Build()
				Mock(syscall.Unmount).Return(nil).Build()
			},
			expectError: false,
			cb: func(sysFsDir string) error {
				So(sysFsDir, ShouldEqual, sysFSDirNetNSTmp)
				return nil
			},
		},
		{
			name:            "Custom namespace - Get error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(0), fmt.Errorf("get error")).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "get error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - GetFromPath error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(0), fmt.Errorf("get from path error")).Build()
				Mock(netns.Set).Return(nil).Build() // Mock Set to handle the defer call
			},
			expectError:            true,
			expectedErrorSubstring: "get from path error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - Set error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).To(func(ns netns.NsHandle) (err error) {
					if ns == netns.NsHandle(1) { // Mock the Set back to original NS
						return nil
					} else {
						return fmt.Errorf("set error")
					}
				}).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "set error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - Stat error (not ErrNotExist)",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, fmt.Errorf("stat error")).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "failed to stat /tmp/net_ns_sysfs, err stat error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - MkdirAll error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
				Mock(os.MkdirAll).Return(fmt.Errorf("mkdir error")).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "failed to create /tmp/net_ns_sysfs, err mkdir error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - Mounted check error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, nil).Build()
				Mock(mountinfo.Mounted).Return(false, fmt.Errorf("mounted check error")).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "check mounted dir: /tmp/net_ns_sysfs failed with error: mounted check error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - Mount error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, nil).Build()
				Mock(mountinfo.Mounted).Return(false, nil).Build()
				Mock(syscall.Mount).Return(fmt.Errorf("mount error")).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "failed to mount sysfs at /tmp/net_ns_sysfs, err mount error",
			cb:                     func(sysFsDir string) error { return nil },
		},
		{
			name:            "Custom namespace - Callback error",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, nil).Build()
				Mock(mountinfo.Mounted).Return(false, nil).Build()
				Mock(syscall.Mount).Return(nil).Build()
				Mock(syscall.Unmount).Return(nil).Build()
			},
			expectError:            true,
			expectedErrorSubstring: "callback error",
			cb: func(sysFsDir string) error {
				return fmt.Errorf("callback error")
			},
		},
		{
			name:            "Custom namespace - SysFs already mounted",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, nil).Build()
				Mock(mountinfo.Mounted).Return(true, nil).Build()
				// syscall.Mount should not be called
				Mock(syscall.Unmount).Return(nil).Build()
			},
			expectError: false,
			cb: func(sysFsDir string) error {
				So(sysFsDir, ShouldEqual, sysFSDirNetNSTmp)
				return nil
			},
		},
		{
			name:            "Custom namespace - SysFs does not exist, MkdirAll succeeds",
			nsName:          "custom-ns",
			netNSDirAbsPath: "/test/path",
			mockSetup: func() {
				Mock(runtime.LockOSThread).Build()
				Mock(runtime.UnlockOSThread).Build()
				Mock(netns.Get).Return(netns.NsHandle(1), nil).Build()
				Mock((*netns.NsHandle).Close).Return(nil).Build()
				Mock(netns.GetFromPath).Return(netns.NsHandle(2), nil).Build()
				Mock(netns.Set).Return(nil).Build()
				Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
				Mock(os.MkdirAll).Return(nil).Build()
				Mock(mountinfo.Mounted).Return(false, nil).Build()
				Mock(syscall.Mount).Return(nil).Build()
				Mock(syscall.Unmount).Return(nil).Build()
			},
			expectError: false,
			cb: func(sysFsDir string) error {
				So(sysFsDir, ShouldEqual, sysFSDirNetNSTmp)
				return nil
			},
		},
	}

	// Run test cases
	for _, tc := range testCases {
		PatchConvey(tc.name, t, func() {
			// Arrange
			tc.mockSetup()

			// Act
			err := DoNetNS(tc.nsName, tc.netNSDirAbsPath, tc.cb)

			// Assert
			if tc.expectError {
				So(err, ShouldNotBeNil)
				if tc.expectedErrorSubstring != "" {
					So(err.Error(), ShouldContainSubstring, tc.expectedErrorSubstring)
				}
			} else {
				So(err, ShouldBeNil)
			}
		})
	}
}

func Test_getInterfaceAttr(t *testing.T) {
	PatchConvey("TestGetInterfaceAttr", t, func() {
		nicPath := "/sys/class/net/eth0"

		Mock(general.Errorf).Return().Build()
		PatchConvey("Scenario 1: All files exist and are valid, the enable file exists and is 1", func() {
			// Arrange: prepare test data and mocks
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}

			Mock(general.IsPathExists).Return(true).Build()
			Mock(general.ReadFileIntoInt).To(func(filepath string) (int, error) {
				switch filepath {
				case path.Join(nicPath, netFileNameNUMANode):
					return 1, nil
				case path.Join(nicPath, netFileNameEnable):
					return netEnable, nil // netEnable = 1
				case path.Join(nicPath, netFileNameSpeed):
					return 10000, nil
				}
				return 0, errors.New("unexpected file read in test")
			}).Build()

			// Act: execute the function under test
			getInterfaceAttr(info, nicPath)

			// Assert: verify the results
			So(info.NumaNode, ShouldEqual, 1)
			So(info.Enable, ShouldBeTrue)
			So(info.Speed, ShouldEqual, 10000)
		})

		PatchConvey("Scenario 2: The enable file does not exist, but the operator is up", func() {
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}

			Mock(general.IsPathExists).Return(false).Build()
			Mock(general.ReadFileIntoLines).Return([]string{netUP}, nil).Build() // netUP = "up"
			Mock(general.ReadFileIntoInt).To(func(filepath string) (int, error) {
				switch filepath {
				case path.Join(nicPath, netFileNameNUMANode):
					return 0, nil
				case path.Join(nicPath, netFileNameSpeed):
					return 25000, nil
				}
				return 0, errors.New("unexpected file read in test")
			}).Build()

			getInterfaceAttr(info, nicPath)
			So(info.NumaNode, ShouldEqual, 0)
			So(info.Enable, ShouldBeTrue)
			So(info.Speed, ShouldEqual, 25000)
		})

		PatchConvey("Scenario 3: The enable file does not exist, and the operator state is not up", func() {
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}

			Mock(general.IsPathExists).Return(false).Build()
			Mock(general.ReadFileIntoLines).Return([]string{"down"}, nil).Build()
			Mock(general.ReadFileIntoInt).To(func(filepath string) (int, error) {
				switch filepath {
				case path.Join(nicPath, netFileNameNUMANode):
					return 1, nil
				case path.Join(nicPath, netFileNameSpeed):
					return 40000, nil
				}
				return 0, errors.New("unexpected file read in test")
			}).Build()

			getInterfaceAttr(info, nicPath)

			So(info.NumaNode, ShouldEqual, 1)
			So(info.Enable, ShouldBeFalse)
			So(info.Speed, ShouldEqual, 40000)
		})

		PatchConvey("Scenario 4: Various errors occurred while reading the file", func() {
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}
			readErr := errors.New("generic read error")

			Mock(general.ReadFileIntoInt).Return(0, readErr).Build()
			Mock(general.IsPathExists).Return(false).Build()
			Mock(general.ReadFileIntoLines).Return(nil, readErr).Build()

			getInterfaceAttr(info, nicPath)

			So(info.NumaNode, ShouldEqual, -1)
			So(info.Enable, ShouldBeFalse)
			So(info.Speed, ShouldEqual, -1)
		})

		PatchConvey("Scenario 5: The value of NUMA node is -1 (invalid value)", func() {
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}

			Mock(general.IsPathExists).Return(true).Build()
			Mock(general.ReadFileIntoInt).To(func(filepath string) (int, error) {
				switch filepath {
				case path.Join(nicPath, netFileNameNUMANode):
					return -1, nil // invalid numa node value
				case path.Join(nicPath, netFileNameEnable):
					return netEnable, nil
				case path.Join(nicPath, netFileNameSpeed):
					return 10000, nil
				}
				return 0, errors.New("unexpected file read in test")
			}).Build()

			getInterfaceAttr(info, nicPath)

			So(info.NumaNode, ShouldEqual, -1)
			So(info.Enable, ShouldBeTrue)
			So(info.Speed, ShouldEqual, 10000)
		})

		PatchConvey("Scenario 6: The enable file exists but the value is 0 (disabled)", func() {
			info := &InterfaceInfo{Name: "eth0", NetNSInfo: NetNSInfo{NSName: "default"}}

			Mock(general.IsPathExists).Return(true).Build()
			Mock(general.ReadFileIntoInt).To(func(filepath string) (int, error) {
				switch filepath {
				case path.Join(nicPath, netFileNameNUMANode):
					return 1, nil
				case path.Join(nicPath, netFileNameEnable):
					return 0, nil
				case path.Join(nicPath, netFileNameSpeed):
					return 10000, nil
				}
				return 0, errors.New("unexpected file read in test")
			}).Build()

			getInterfaceAttr(info, nicPath)

			So(info.NumaNode, ShouldEqual, 1)
			So(info.Enable, ShouldBeFalse)
			So(info.Speed, ShouldEqual, 10000)
		})
	})
}

func TestSetIrqAffinityCPUs(t *testing.T) {
	PatchConvey("TestSetIrqAffinityCPUs", t, func() {
		PatchConvey("Scenario 1: When the irq number is invalid, an error should be returned", func() {
			invalidIrq := 0
			cpus := []int64{1, 2}

			err := SetIrqAffinityCPUs(invalidIrq, cpus)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("invalid irq: %d", invalidIrq))
		})

		PatchConvey("Scenario 2: When the cpus list is empty, an error should be returned", func() {
			irq := 10
			var cpus []int64

			err := SetIrqAffinityCPUs(irq, cpus)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "empty cpus")
		})

		PatchConvey("Scenario 3: When the underlying ApplyProcInterrupts call fails, the wrapped error should be returned.", func() {
			mockErr := errors.New("procfs apply error")
			Mock(procm.ApplyProcInterrupts).Return(mockErr).Build()

			irq := 10
			cpus := []int64{1, 2}

			err := SetIrqAffinityCPUs(irq, cpus)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to apply proc interrupts, err %v", mockErr))
		})

		PatchConvey("Scenario 4: Successfully set interrupt affinity (hybrid CPU list)", func() {
			irq := 20
			cpus := []int64{0, 1, 3, 5, 6}
			expectedCpusetString := "0-1,3,5-6"

			mock := Mock(procm.ApplyProcInterrupts).To(func(actualIrq int, actualCpuset string) error {
				So(actualIrq, ShouldEqual, irq)
				So(actualCpuset, ShouldEqual, expectedCpusetString)
				return nil
			}).Build()

			err := SetIrqAffinityCPUs(irq, cpus)

			So(err, ShouldBeNil)
			So(mock.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario 5: Successfully set interrupt affinity (non-continuous CPU list)", func() {
			irq := 30
			cpus := []int64{1, 3, 5}
			expectedCpusetString := "1,3,5"

			mock := Mock(procm.ApplyProcInterrupts).To(func(actualIrq int, actualCpuset string) error {
				So(actualIrq, ShouldEqual, irq)
				So(actualCpuset, ShouldEqual, expectedCpusetString)
				return nil
			}).Build()

			err := SetIrqAffinityCPUs(irq, cpus)

			So(err, ShouldBeNil)
			So(mock.MockTimes(), ShouldEqual, 1)
		})
	})
}

func TestSetIrqAffinity(t *testing.T) {
	PatchConvey("TestSetIrqAffinity", t, func() {
		PatchConvey("Scenario: irq number is invalid", func() {
			invalidIrq := 0
			validCpu := int64(4)

			err := SetIrqAffinity(invalidIrq, validCpu)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("invalid irq: %d", invalidIrq))
		})

		PatchConvey("Scenario: CPU number is invalid", func() {
			validIrq := 10
			invalidCpu := int64(-1)

			err := SetIrqAffinity(validIrq, invalidCpu)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("invalid cpu: %d", invalidCpu))
		})

		PatchConvey("Scenario: The CPU number is too large and the addition to the cpuset fails", func() {
			validIrq := 10
			largeCpu := int64(math.MaxInt64)

			err := SetIrqAffinity(validIrq, largeCpu)

			So(err, ShouldNotBeNil)
		})

		PatchConvey("Scenario: ApplyProcInterrupts call failed", func() {
			validIrq := 10
			validCpu := int64(4)
			mockErr := errors.New("failed to write to smp_affinity")
			mock := Mock(procm.ApplyProcInterrupts).Return(mockErr).Build()

			err := SetIrqAffinity(validIrq, validCpu)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to apply proc interrupts, err %v", mockErr))
			So(mock.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario: Successfully set irq affinity", func() {
			validIrq := 10
			validCpu := int64(4)
			expectedCpusetStr := "4"

			var capturedIrq int
			var capturedCpuset string
			mock := Mock(procm.ApplyProcInterrupts).To(func(irqNumber int, cpuset string) error {
				capturedIrq = irqNumber
				capturedCpuset = cpuset
				return nil
			}).Build()

			err := SetIrqAffinity(validIrq, validCpu)

			So(err, ShouldBeNil)
			So(mock.MockTimes(), ShouldEqual, 1)
			So(capturedIrq, ShouldEqual, validIrq)
			So(capturedCpuset, ShouldEqual, expectedCpusetStr)
		})
	})
}

func Test_GetIrqAffinityCPUs(t *testing.T) {
	PatchConvey("Test GetIrqAffinityCPUs", t, func() {
		PatchConvey("Scene 1: The irq number is invalid", func() {
			invalidIrq := 0

			cpuList, err := GetIrqAffinityCPUs(invalidIrq)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("invalid irq: %d", invalidIrq))
			So(cpuList, ShouldBeNil)
		})

		PatchConvey("Scenario 2: The irq directory does not exist", func() {
			irq := 123

			Mock(os.Stat).To(func(name string) (os.FileInfo, error) {
				return nil, os.ErrNotExist
			}).Build()

			cpuList, err := GetIrqAffinityCPUs(irq)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("%d is not exist", irq))
			So(cpuList, ShouldBeNil)
		})

		PatchConvey("Scenario 3: Failed to parse smp_affinity_list file", func() {
			irq := 456
			irqProcDir := fmt.Sprintf("%s/%d", IrqRootPath, irq)
			smpAffinityListPath := path.Join(irqProcDir, "smp_affinity_list")
			expectedErr := errors.New("read file error")

			Mock(os.Stat).Return(nil, nil).Build()
			Mock(general.ParseLinuxListFormatFromFile).Return(nil, expectedErr).Build()

			cpuList, err := GetIrqAffinityCPUs(irq)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to ParseLinuxListFormatFromFile(%s), err %v", smpAffinityListPath, expectedErr))
			So(cpuList, ShouldBeNil)
		})

		PatchConvey("Scenario 4: Successfully obtaining the CPU affinity list", func() {
			irq := 789
			expectedCPUs := []int64{0, 1, 2, 3}

			Mock(os.Stat).Return(nil, nil).Build()
			Mock(general.ParseLinuxListFormatFromFile).Return(expectedCPUs, nil).Build()

			cpuList, err := GetIrqAffinityCPUs(irq)

			So(err, ShouldBeNil)
			So(cpuList, ShouldResemble, expectedCPUs)
		})
	})
}

func Test_setNicRxQueueRPS(t *testing.T) {
	PatchConvey("Test setNicRxQueueRPS", t, func() {
		nicInfo := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				NetNSInfo: NetNSInfo{
					NSName: "test-ns",
				},
				Name: "eth0",
			},
		}
		queue := 1
		rpsConf := "f"

		PatchConvey("Scenario 1: Set RPS successfully", func() {
			Mock(netnsEnter).Return(&netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}, nil).Build()

			mockExit := Mock((*netnsSwitchContext).netnsExit).Return().Build()
			Mock(sysm.SetNicRxQueueRPS).Return(nil).Build()

			err := setNicRxQueueRPS(nicInfo, queue, rpsConf)

			So(err, ShouldBeNil)
			So(mockExit.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario 2: The queue number is invalid", func() {
			invalidQueue := -1

			err := setNicRxQueueRPS(nicInfo, invalidQueue, rpsConf)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("invalid rx queue %d", invalidQueue))
		})

		PatchConvey("Scene 3: netnsEnter failed", func() {
			mockErr := errors.New("failed to enter netns")
			Mock(netnsEnter).Return(nil, mockErr).Build()

			err := setNicRxQueueRPS(nicInfo, queue, rpsConf)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
		})

		PatchConvey("Scenario 4: SetNicRxQueueRPS failed", func() {
			mockErr := errors.New("failed to set rps")
			Mock(sysm.SetNicRxQueueRPS).Return(mockErr).Build()
			Mock(netnsEnter).Return(&netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}, nil).Build()

			mockExit := Mock((*netnsSwitchContext).netnsExit).Return().Build()

			err := setNicRxQueueRPS(nicInfo, queue, rpsConf)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(mockExit.MockTimes(), ShouldEqual, 1) // 验证defer的netnsExit仍然被调用
		})
	})
}

func TestSetNicRxQueueRPS(t *testing.T) {
	PatchConvey("Test SetNicRxQueueRPS", t, func() {
		nic := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				Name: "eth0",
			},
		}
		queue := 0
		destCpus := []int64{1, 2, 33}

		PatchConvey("Scenario 1: RPS is successfully set", func() {
			mockedBitmap := "00000002,00000006"
			Mock(general.ConvertIntSliceToBitmapString).Return(mockedBitmap, nil).Build()
			mockSetRPS := Mock(setNicRxQueueRPS).To(func(n *NicBasicInfo, q int, rpsConf string) error {
				So(n, ShouldResemble, nic)
				So(q, ShouldEqual, queue)
				So(rpsConf, ShouldEqual, mockedBitmap)
				return nil
			}).Build()

			err := SetNicRxQueueRPS(nic, queue, destCpus)

			So(err, ShouldBeNil)
			So(mockSetRPS.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario 2: The underlying RPS failed", func() {
			expectedErr := errors.New("failed to set rps")
			Mock(general.ConvertIntSliceToBitmapString).Return("some-bitmap", nil).Build()
			Mock(setNicRxQueueRPS).Return(expectedErr).Build()

			err := SetNicRxQueueRPS(nic, queue, destCpus)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, expectedErr)
		})

		PatchConvey("Scenario 3: CPU list conversion bitmap failed", func() {
			Mock(general.ConvertIntSliceToBitmapString).Return("", errors.New("conversion error")).Build()
			mockSetRPS := Mock(setNicRxQueueRPS).To(func(_ *NicBasicInfo, _ int, rpsConf string) error {
				So(rpsConf, ShouldBeEmpty)
				return nil
			}).Build()

			err := SetNicRxQueueRPS(nic, queue, destCpus)

			So(err, ShouldBeNil)
			So(mockSetRPS.MockTimes(), ShouldEqual, 1)
		})
	})
}

func Test_ClearNicRxQueueRPS(t *testing.T) {
	PatchConvey("Test ClearNicRxQueueRPS", t, func() {
		nic := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				Name: "eth0",
				NetNSInfo: NetNSInfo{
					NSName: "test-ns",
				},
			},
		}
		queue := 1

		PatchConvey("Scenario 1: The RPS configuration was successfully cleared", func() {
			mock := Mock(setNicRxQueueRPS).Return(nil).Build()

			err := ClearNicRxQueueRPS(nic, queue)

			So(err, ShouldBeNil)
			So(mock.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario 2: Internal call to setNicRxQueueRPS fails", func() {
			expectedErr := errors.New("internal error from setNicRxQueueRPS")
			Mock(setNicRxQueueRPS).Return(expectedErr).Build()

			// Act: execute the function under test
			err := ClearNicRxQueueRPS(nic, queue)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, expectedErr)
		})
	})
}

func Test_IsZeroBitmap(t *testing.T) {
	PatchConvey("Test the IsZeroBitmap function", t, func() {
		PatchConvey("Scenario 1: When all fields of the bitmap string are 0, true should be returned", func() {
			bitmapStr := "0,00,0,000"
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 2: When the bitmap string contains a non-zero value, false should be returned", func() {
			bitmapStr := "0,0,F,0"
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 3: When the bitmap string contains invalid hexadecimal characters, false should be returned", func() {
			bitmapStr := "0,G,0"
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 4: When the input is an empty string, false should be returned", func() {
			bitmapStr := ""
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 5: When the input only contains delimiters, false should be returned", func() {
			bitmapStr := ",,"
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 6: When the input is a single 0, it should return true", func() {
			bitmapStr := "0"
			result := IsZeroBitmap(bitmapStr)

			So(result, ShouldBeTrue)
		})
	})
}

func Test_ComparesHexBitmapStrings(t *testing.T) {
	PatchConvey("Test ComparesHexBitmapStrings", t, func() {
		PatchConvey("Scenario 1: Two strings are exactly equal", func() {
			a := "a,b,c,123"
			b := "a,b,c,123"

			result := ComparesHexBitmapStrings(a, b)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 2: Two strings are not equal in common", func() {
			a := "a,b,c"
			b := "a,b,d"

			result := ComparesHexBitmapStrings(a, b)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 3: a longer, but the excess is 0, logically equal", func() {
			a := "0,0,0,a,b,c"
			b := "a,b,c"

			result := ComparesHexBitmapStrings(a, b)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 4: b is longer, but the excess is 0, logically equal", func() {
			// Arrange
			a := "a,b,c"
			b := "0,0,a,b,c"

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert
			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 5: a longer, but the excess contains non-zero values, which are logically unequal", func() {
			// Arrange
			a := "1,0,a,b,c"
			b := "a,b,c"

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert
			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 6: b is longer, but the excess contains non-zero values, which are logically unequal", func() {
			// Arrange
			a := "a,b,c"
			b := "0,1,a,b,c"

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert
			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 7: a longer, but the excess contains invalid hexadecimal characters", func() {
			// Arrange: 'g' is not a valid hex character.
			a := "g,a,b,c"
			b := "a,b,c"

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert: Should return false due to parsing error.
			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 8: Both strings are empty", func() {
			// Arrange
			a := ""
			b := ""

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert
			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 9: One string is empty, the other is not empty", func() {
			// Arrange
			a := "1,2,3"
			b := ""

			// Act
			result := ComparesHexBitmapStrings(a, b)

			// Assert
			So(result, ShouldBeFalse)
		})
	})
}

func TestGetNicRxQueueRpsConf(t *testing.T) {
	nic := &NicBasicInfo{
		InterfaceInfo: InterfaceInfo{
			Name: "eth0",
			NetNSInfo: NetNSInfo{
				NSName: "test-ns",
			},
		},
	}
	queue := 0

	PatchConvey("TestGetNicRxQueueRpsConf", t, func() {
		PatchConvey("Scenario 1: netnsEnter call failed", func() {
			mockErr := errors.New("netns enter failed")
			Mock(netnsEnter).Return(nil, mockErr).Build()

			rps, err := GetNicRxQueueRpsConf(nic, queue)

			So(rps, ShouldBeEmpty)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "netns enter failed")
		})

		PatchConvey("Scenario 2: GetNicRxQueueRPS call failed", func() {
			mockNsc := &netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			mockExit := Mock((*netnsSwitchContext).netnsExit).Return().Build()
			mockErr := errors.New("sysfs read failed")
			Mock(sysm.GetNicRxQueueRPS).Return("", mockErr).Build()

			rps, err := GetNicRxQueueRpsConf(nic, queue)

			So(rps, ShouldBeEmpty)
			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, mockErr)
			So(mockExit.MockTimes(), ShouldEqual, 1)
		})

		PatchConvey("Scenario 3: Successfully obtaining RPS configuration", func() {
			mockNsc := &netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			mockExit := Mock((*netnsSwitchContext).netnsExit).Return().Build()
			expectedRps := "f"
			mockGetRPS := Mock(sysm.GetNicRxQueueRPS).Return(expectedRps, nil).Build()

			rps, err := GetNicRxQueueRpsConf(nic, queue)

			So(err, ShouldBeNil)
			So(rps, ShouldEqual, expectedRps)
			So(mockExit.MockTimes(), ShouldEqual, 1)
			So(mockGetRPS.MockTimes(), ShouldEqual, 1)
		})
	})
}

func TestGetNicRxQueuesRpsConf(t *testing.T) {
	PatchConvey("TestGetNicRxQueuesRpsConf", t, func() {
		nicInfo := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				NetNSInfo: NetNSInfo{
					NSName: "test-ns",
				},
				Name: "eth0",
			},
			QueueNum: 2,
		}

		PatchConvey("Scenario 1: Successfully obtaining RPS configuration", func() {
			Mock(netnsEnter).Return(&netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}, nil).Build()
			Mock((*netnsSwitchContext).netnsExit).Return().Build()
			Mock(sysm.GetNicRxQueueRPS).To(func(sysPath, nic string, queue int) (string, error) {
				return fmt.Sprintf("conf-for-queue-%d", queue), nil
			}).Build()

			conf, err := GetNicRxQueuesRpsConf(nicInfo)

			So(err, ShouldBeNil)
			So(conf, ShouldNotBeNil)
			So(len(conf), ShouldEqual, 2)
			So(conf[0], ShouldEqual, "conf-for-queue-0")
			So(conf[1], ShouldEqual, "conf-for-queue-1")
		})

		PatchConvey("Scenario 2: The queue number is 0 and causes failure", func() {
			invalidNicInfo := &NicBasicInfo{
				QueueNum: 0,
			}

			conf, err := GetNicRxQueuesRpsConf(invalidNicInfo)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "invalid queue number 0")
			So(conf, ShouldBeNil)
		})

		PatchConvey("Scenario 3: Failed to enter the network namespace", func() {
			mockErr := errors.New("failed to enter netns")
			Mock(netnsEnter).Return(nil, mockErr).Build()

			conf, err := GetNicRxQueuesRpsConf(nicInfo)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(conf, ShouldBeNil)
		})

		PatchConvey("Scenario 4: Failed to obtain RPS configuration", func() {
			mockErr := errors.New("failed to read rps_cpus")
			Mock(netnsEnter).Return(&netnsSwitchContext{
				sysMountDir: "/tmp/sys",
			}, nil).Build()
			exitMock := Mock((*netnsSwitchContext).netnsExit).Return().Build()
			Mock(sysm.GetNicRxQueueRPS).Return("", mockErr).Build()

			conf, err := GetNicRxQueuesRpsConf(nicInfo)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, mockErr)
			So(conf, ShouldBeNil)
			So(exitMock.MockTimes(), ShouldEqual, 1)
		})
	})
}

func Test_GetNicQueue2IrqWithQueueFilter(t *testing.T) {
	nicInfo := &NicBasicInfo{
		InterfaceInfo: InterfaceInfo{
			Name:    "eth0",
			IfIndex: 1,
			PCIAddr: "0000:01:00.0",
		},
	}

	PatchConvey("Test GetNicQueue2IrqWithQueueFilter", t, func() {
		PatchConvey("Scenario 1: Successfully obtain the universal NIC queue and IRQ mapping", func() {
			interruptsContent := `
			           CPU0       CPU1
			 28:         10         20   IO-APIC-fasteoi   eth0-rx-0
			 29:         30         40   IO-APIC-fasteoi   eth0-tx-1
			 30:          0          0   IO-APIC-fasteoi   other-device
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()

			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, "eth0", "-")

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			expectedMap := map[int]int{0: 28, 1: 29}
			So(result, ShouldResemble, expectedMap)
		})

		PatchConvey("Scenario 2: The Mellanox NIC queue and IRQ mapping are successfully obtained", func() {
			interruptsContent := `
			           CPU0       CPU1
			105:        100        200   IR-PCI-MSI-edge   eth0-0
			106:        300        400   IR-PCI-MSI-edge   eth0-1
			107:        500        600   IR-PCI-MSI-edge   other-device
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()

			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, nicInfo.Name, "-")

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			expectedMap := map[int]int{0: 105, 1: 106}
			So(result, ShouldResemble, expectedMap)
		})

		PatchConvey("Scenario 3: Failed to read the interrupts file", func() {
			mockErr := errors.New("read file error")
			Mock(os.ReadFile).Return(nil, mockErr).Build()

			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, "eth0", "-")

			So(result, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to ReadFile(%s), err %s", InterruptsFile, mockErr))
		})

		PatchConvey("Scenario 4: The resolved queue number is greater than or equal to the total number of queues", func() {
			interruptsContent := `
			           CPU0       CPU1
			 28:         10         20   IO-APIC-fasteoi   eth0-rx-0
			 29:         30         40   IO-APIC-fasteoi   eth0-tx-99
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()

			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, "eth0", "-")

			So(result, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("%s: %d: %s queue %d greater-equal queue count %d", nicInfo.NSName, nicInfo.IfIndex, nicInfo.Name, 99, 2))
		})

		PatchConvey("Scenario 5: Failed to resolve the interrupt number (IRQ).", func() {
			interruptsContent := `
			           CPU0       CPU1
			 28:         10         20   IO-APIC-fasteoi   eth0-rx-0
			bad:         30         40   IO-APIC-fasteoi   eth0-tx-1
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()
			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, "eth0", "-")

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			expectedMap := map[int]int{0: 28}
			So(result, ShouldResemble, expectedMap)
		})

		PatchConvey("Scenario 6: The same queue is mapped to multiple break numbers", func() {
			interruptsContent := `
			           CPU0       CPU1
			 28:         10         20   IO-APIC-fasteoi   eth0-rx-0
			 29:         30         40   IO-APIC-fasteoi   eth0-tx-0
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()
			result, err := GetNicQueue2IrqWithQueueFilter(nicInfo, "eth0", "-")

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			expectedMap := map[int]int{0: 28}
			So(result, ShouldResemble, expectedMap)
		})

		PatchConvey("Scenario 7: Filtering using nicInfo.Irqs", func() {
			nicInfoWithIrqs := &NicBasicInfo{
				InterfaceInfo: InterfaceInfo{Name: "eth0"},
				Irqs:          []int{28},
			}
			interruptsContent := `
			           CPU0       CPU1
			 28:         10         20   IO-APIC-fasteoi   eth0-rx-0
			 29:         30         40   IO-APIC-fasteoi   eth0-tx-1
			`
			Mock(os.ReadFile).Return([]byte(interruptsContent), nil).Build()

			result, err := GetNicQueue2IrqWithQueueFilter(nicInfoWithIrqs, "eth0", "-")

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			expectedMap := map[int]int{0: 28}
			So(result, ShouldResemble, expectedMap)
		})
	})
}

func Test_GetNicQueue2Irq(t *testing.T) {
	PatchConvey("Test GetNicQueue2Irq", t, func() {
		PatchConvey("Scenario 1: Virtio network card", func() {
			nicInfo := &NicBasicInfo{
				IsVirtioNetDev: true,
				VirtioNetName:  "virtio-net-p0",
				InterfaceInfo: InterfaceInfo{
					IfIndex: 1,
					Name:    "eth0",
				},
			}

			PatchConvey("Successfully get input and output queues", func() {
				mockInputQueue := map[int]int{0: 100, 1: 101}
				mockOutputQueue := map[int]int{0: 200, 1: 201}
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "virtio-net-p0-input" {
						return mockInputQueue, nil
					}
					if queueFilter == "virtio-net-p0-output" {
						return mockOutputQueue, nil
					}
					return nil, fmt.Errorf("unexpected filter: %s", queueFilter)
				}).Build()

				q2i, txq2i, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldBeNil)
				So(q2i, ShouldResemble, mockInputQueue)
				So(txq2i, ShouldResemble, mockOutputQueue)
			})

			PatchConvey("Gets input queue returns an error", func() {
				mockErr := errors.New("input queue error")
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "virtio-net-p0-input" {
						return nil, mockErr
					}
					return nil, nil
				}).Build()

				_, _, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, mockErr.Error())
			})

			PatchConvey("Error is returned when getting the output queue", func() {
				mockInputQueue := map[int]int{0: 100, 1: 101}
				mockErr := errors.New("output queue error")
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "virtio-net-p0-input" {
						return mockInputQueue, nil
					}
					if queueFilter == "virtio-net-p0-output" {
						return nil, mockErr
					}
					return nil, nil
				}).Build()

				_, _, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, mockErr.Error())
			})

			PatchConvey("No matching input queue found (returns empty map)", func() {
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "virtio-net-p0-input" {
						return make(map[int]int), nil
					}
					return nil, nil
				}).Build()

				_, _, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "failed to find matched input queue")
			})
		})

		PatchConvey("Scenario 2: Non-Virtio NICs", func() {
			nicInfo := &NicBasicInfo{
				IsVirtioNetDev: false,
				InterfaceInfo: InterfaceInfo{
					IfIndex: 2,
					Name:    "eth_bond0",
					PCIAddr: "0000:81:00.0",
				},
			}
			expectedTxQueue := make(map[int]int)

			PatchConvey("Successfully matched by the NIC name", func() {
				mockQueue := map[int]int{0: 10, 1: 11}
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "eth_bond0" {
						return mockQueue, nil
					}
					return make(map[int]int), nil
				}).Build()

				q2i, txq2i, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldBeNil)
				So(q2i, ShouldResemble, mockQueue)
				So(txq2i, ShouldResemble, expectedTxQueue)
			})

			PatchConvey("Successfully matched via PCI address", func() {
				mockQueue := map[int]int{0: 20, 1: 21}
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "eth_bond0" {
						return make(map[int]int), nil
					}
					if queueFilter == "0000:81:00.0" {
						return mockQueue, nil
					}
					return make(map[int]int), nil
				}).Build()

				q2i, txq2i, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldBeNil)
				So(q2i, ShouldResemble, mockQueue)
				So(txq2i, ShouldResemble, expectedTxQueue)
			})

			PatchConvey("The split NIC name is successfully matched", func() {
				mockQueue := map[int]int{0: 30, 1: 31}
				Mock(GetNicQueue2IrqWithQueueFilter).To(func(_ *NicBasicInfo, queueFilter string, _ string) (map[int]int, error) {
					if queueFilter == "eth_bond0" {
						return make(map[int]int), nil
					}
					if queueFilter == "0000:81:00.0" {
						return make(map[int]int), nil
					}
					if queueFilter == "eth" {
						return mockQueue, nil
					}
					return make(map[int]int), nil
				}).Build()

				q2i, txq2i, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldBeNil)
				So(q2i, ShouldResemble, mockQueue)
				So(txq2i, ShouldResemble, expectedTxQueue)
			})

			PatchConvey("None of the filters matched", func() {
				Mock(GetNicQueue2IrqWithQueueFilter).Return(make(map[int]int), nil).Build()

				_, _, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "failed to find matched queue")
			})

			PatchConvey("The filter returns an error", func() {
				mockErr := errors.New("filter error")
				Mock(GetNicQueue2IrqWithQueueFilter).Return(nil, mockErr).Build()

				_, _, err := GetNicQueue2Irq(nicInfo)

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, mockErr.Error())
			})
		})
	})
}

func Test_GetIrqsAffinityCPUs(t *testing.T) {
	PatchConvey("Test GetIrqsAffinityCPUs", t, func() {
		PatchConvey("Scenario 1: Successfully obtain CPU affinity for multiple interrupts", func() {
			// Arrange: prepare test data and mocks
			irqs := []int{10, 20}
			expectedCPUsFor10 := []int64{0, 1}
			expectedCPUsFor20 := []int64{2, 3}
			expectedResult := map[int][]int64{
				10: expectedCPUsFor10,
				20: expectedCPUsFor20,
			}

			Mock(GetIrqAffinityCPUs).To(func(irq int) ([]int64, error) {
				if irq == 10 {
					return expectedCPUsFor10, nil
				}
				if irq == 20 {
					return expectedCPUsFor20, nil
				}
				return nil, fmt.Errorf("unexpected irq: %d", irq)
			}).Build()

			result, err := GetIrqsAffinityCPUs(irqs)

			So(err, ShouldBeNil)
			So(result, ShouldResemble, expectedResult)
		})

		PatchConvey("Scenario 2: An error occurs when obtaining the interrupted CPU affinity", func() {
			// Arrange: prepare test data and mocks
			irqs := []int{10, 20}
			mockErr := errors.New("mock error on irq 20")

			Mock(GetIrqAffinityCPUs).To(func(irq int) ([]int64, error) {
				if irq == 10 {
					return []int64{0, 1}, nil
				}
				if irq == 20 {
					return nil, mockErr
				}
				return nil, nil
			}).Build()

			result, err := GetIrqsAffinityCPUs(irqs)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to GetIrqAffinityCPUs(20)")
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(result, ShouldBeNil)
		})

		PatchConvey("Scenario 3: The entered interrupt list is empty", func() {
			irqs := []int{}

			result, err := GetIrqsAffinityCPUs(irqs)

			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			So(result, ShouldBeEmpty)
		})
	})
}

func Test_TidyUpNicIrqsAffinityCPUs(t *testing.T) {
	PatchConvey("Test TidyUpNicIrqsAffinityCPUs", t, func() {
		PatchConvey("Success Scenario - Distribute IRQ evenly across available CPUs", func() {
			irq2CPUs := map[int][]int64{
				10: {0, 1}, // IRQ 10
				11: {0, 1}, // IRQ 11
				12: {0, 1}, // IRQ 12
			}
			mockSetAffinity := Mock(SetIrqAffinity).Return(nil).Build()

			result, err := TidyUpNicIrqsAffinityCPUs(irq2CPUs)

			// Assert:
			// irq 10 -> cpu 0 (counts: {0:1})
			// irq 11 -> cpu 1 (counts: {0:1, 1:1})
			// irq 12 -> cpu 0 (counts: {0:2, 1:1})
			expected := map[int]int64{
				10: 0,
				11: 1,
				12: 0,
			}
			So(err, ShouldBeNil)
			So(result, ShouldResemble, expected)
			So(mockSetAffinity.MockTimes(), ShouldEqual, 3)
		})

		PatchConvey("Success Scenario - Prioritize the CPU with the lowest load", func() {
			irq2CPUs := map[int][]int64{
				21: {0, 1},
				20: {0, 1},
				22: {1},
			}
			Mock(SetIrqAffinity).Return(nil).Build()

			result, err := TidyUpNicIrqsAffinityCPUs(irq2CPUs)

			// Assert:
			// irq 20 -> cpu 0 (counts: {0:1})
			// irq 21 -> cpu 1 (counts: {0:1, 1:1})
			// irq 22 -> cpu 1 (counts: {0:1, 1:2})
			expected := map[int]int64{
				20: 0,
				21: 1,
				22: 1,
			}
			So(err, ShouldBeNil)
			So(result, ShouldResemble, expected)
		})

		PatchConvey("Failure Scenario - SetIrqAffinity returns an error", func() {
			irq2CPUs := map[int][]int64{
				10: {0, 1},
			}
			mockErr := errors.New("set affinity failed")
			Mock(SetIrqAffinity).Return(mockErr).Build()

			result, err := TidyUpNicIrqsAffinityCPUs(irq2CPUs)

			So(result, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to SetIrqAffinity(10, 0), err: set affinity failed")
		})

		PatchConvey("Edge Scene - The input map is empty", func() {
			irq2CPUs := map[int][]int64{}
			mockSetAffinity := Mock(SetIrqAffinity).Return(nil).Build()

			result, err := TidyUpNicIrqsAffinityCPUs(irq2CPUs)

			So(err, ShouldBeNil)
			So(result, ShouldBeEmpty)
			So(mockSetAffinity.MockTimes(), ShouldEqual, 0)
		})

		PatchConvey("Failure Scenario - IRQ has no CPU available", func() {
			irq2CPUs := map[int][]int64{
				10: {0},
				11: {},
			}
			Mock(SetIrqAffinity).To(func(irq int, cpu int64) error {
				if cpu < 0 {
					return fmt.Errorf("invalid cpu: %d", cpu)
				}
				return nil
			}).Build()

			result, err := TidyUpNicIrqsAffinityCPUs(irq2CPUs)

			So(result, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to SetIrqAffinity(11, -1)")
			So(err.Error(), ShouldContainSubstring, "invalid cpu: -1")
		})
	})
}

func Test_getNicDriver(t *testing.T) {
	PatchConvey("Test getNicDriver", t, func() {
		dummyNicSysPath := "/sys/class/net/eth0"
		PatchConvey("Scenario 1: When reading a symlink fails, an unknown driver and error should be returned", func() {
			expectedErr := errors.New("readlink error")
			Mock(os.Readlink).Return("", expectedErr).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to read symlink")
			So(driver, ShouldEqual, NicDriverUnknown)
		})

		PatchConvey("Scenario 2: When the driver is of type MLX, the NicDriverMLX should be returned correctly", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/mlx5_core", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverMLX)
		})

		PatchConvey("Scenario 3: When the driver is BNX type, NicDriverBNX should be returned correctly", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/bnxt_en", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverBNX)
		})

		PatchConvey("Scenario 4: When the driver is of type VirtioNet, NicDriverVirtioNet should be returned correctly", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/virtio_net", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverVirtioNet)
		})

		PatchConvey("Scenario 5: When the driver is of type I40E, the NicDriverI40E should be returned correctly", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/i40e", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverI40E)
		})

		PatchConvey("Scenario 6: When the driver is of type IXGBE, NicDriverIXGBE should be returned correctly", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/ixgbe", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverIXGBE)
		})

		PatchConvey("Scenario 7: When the driver is of an unrecognized type, an unknown driver should be returned", func() {
			Mock(os.Readlink).Return("../../bus/pci/drivers/some_other_driver", nil).Build()

			driver, err := getNicDriver(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverUnknown)
		})
	})
}

type mockDirEntry struct {
	fs.DirEntry
	entryName string
	isDir     bool
	typ       fs.FileMode
}

// Name return the mock entry name
func (m *mockDirEntry) Name() string {
	return m.entryName
}

// IsDir return if the entry is a directory
func (m *mockDirEntry) IsDir() bool {
	return m.isDir
}

// Type return the mock entry type
func (m *mockDirEntry) Type() fs.FileMode {
	return m.typ
}

// Info return the mock entry info
func (m *mockDirEntry) Info() (fs.FileInfo, error) {
	return nil, nil
}

func Test_GetNicIrqs(t *testing.T) {
	PatchConvey("Test GetNicIrqs", t, func() {
		dummyPath := "/sys/class/net/eth0"
		PatchConvey("Scenario 1: All IRQs are successfully obtained and resolved", func() {
			mockEntries := []fs.DirEntry{
				&mockDirEntry{entryName: "120"},
				&mockDirEntry{entryName: "122"},
				&mockDirEntry{entryName: "121"},
			}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()

			irqs, err := GetNicIrqs(dummyPath)

			So(err, ShouldBeNil)
			So(irqs, ShouldNotBeNil)
			So(irqs, ShouldResemble, []int{120, 121, 122})
		})

		PatchConvey("Scenario 2: Failed to read the directory", func() {
			expectedErr := errors.New("permission denied")
			Mock(os.ReadDir).Return(nil, expectedErr).Build()

			irqs, err := GetNicIrqs(dummyPath)

			So(err, ShouldNotBeNil)
			So(irqs, ShouldBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to ReadDir")
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
		})

		PatchConvey("Scenario 3: Some file names cannot be converted to integers", func() {
			mockEntries := []fs.DirEntry{
				&mockDirEntry{entryName: "130"},
				&mockDirEntry{entryName: "not-a-number"},
				&mockDirEntry{entryName: "132"},
				&mockDirEntry{entryName: "another-invalid"},
			}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()

			irqs, err := GetNicIrqs(dummyPath)

			So(err, ShouldBeNil)
			So(irqs, ShouldNotBeNil)
			So(irqs, ShouldResemble, []int{130, 132})
		})

		PatchConvey("Scenario 4: The directory is empty", func() {
			mockEntries := []fs.DirEntry{}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()

			irqs, err := GetNicIrqs(dummyPath)

			So(err, ShouldBeNil)
			So(irqs, ShouldBeEmpty)
		})
	})
}

func Test_IsPCIDevice(t *testing.T) {
	PatchConvey("Test IsPCIDevice", t, func() {
		PatchConvey("When EvalSymlinks fails", func() {
			testPath := "/sys/class/net/eth0"
			expectedErr := errors.New("mock symlink error")

			Mock(filepath.EvalSymlinks).Return("", expectedErr).Build()
			result := IsPCIDevice(testPath)

			So(result, ShouldBeFalse)
		})

		PatchConvey("When the path is a PCI device", func() {
			testPath := "/sys/class/net/eth0"
			realPath := "/sys/devices/pci0000:00/0000:00:1f.6/net/eth0"

			Mock(filepath.EvalSymlinks).Return(realPath, nil).Build()

			result := IsPCIDevice(testPath)

			So(result, ShouldBeTrue)
		})

		PatchConvey("When the path is not a PCI device", func() {
			testPath := "/sys/class/net/usb0"
			realPath := "/sys/devices/platform/soc/some/usb/net/usb0"

			Mock(filepath.EvalSymlinks).Return(realPath, nil).Build()

			// Act: execute the function under test
			result := IsPCIDevice(testPath)

			So(result, ShouldBeFalse)
		})
	})
}

func Test_GetNicPCIAddr(t *testing.T) {
	PatchConvey("Test GetNicPCIAddr", t, func() {
		nicSysPath := "/sys/class/net/eth0"
		PatchConvey("Scenario 1: EvalSymlinks fails", func() {
			mockErr := errors.New("filesystem error")
			Mock(filepath.EvalSymlinks).Return("", mockErr).Build()

			pciAddr, err := GetNicPCIAddr(nicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(pciAddr, ShouldBeEmpty)
		})

		PatchConvey("Scenario 2: It's a VirtioNet device", func() {
			devRealPath := "/sys/devices/pci0000:00/0000:00:03.0"
			Mock(filepath.EvalSymlinks).Return(devRealPath, nil).Build()
			Mock(IsVirtioNetDevice).Return(true).Build()
			pciAddr, err := GetNicPCIAddr(nicSysPath)

			expectedPCIAddr := filepath.Base(filepath.Dir(devRealPath))
			So(err, ShouldBeNil)
			So(pciAddr, ShouldEqual, expectedPCIAddr)
		})

		PatchConvey("Scenario 3: Not a VirtioNet device", func() {
			devRealPath := "/sys/devices/pci0000:00/0000:00:04.0"
			Mock(filepath.EvalSymlinks).Return(devRealPath, nil).Build()
			Mock(IsVirtioNetDevice).Return(false).Build()

			pciAddr, err := GetNicPCIAddr(nicSysPath)

			expectedPCIAddr := filepath.Base(devRealPath)
			So(err, ShouldBeNil)
			So(pciAddr, ShouldEqual, expectedPCIAddr)
		})
	})
}

func Test_IsVirtioNetDevice(t *testing.T) {
	PatchConvey("Test IsVirtioNetDevice", t, func() {
		PatchConvey("Scenario: It is a virtio-net device", func() {
			Mock(filepath.EvalSymlinks).Return("/sys/bus/pci/drivers/virtio_net", nil).Build()

			result := IsVirtioNetDevice("/sys/class/net/eth0")

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario: Not a virtio-net device", func() {
			Mock(filepath.EvalSymlinks).Return("/sys/bus/pci/drivers/another_driver", nil).Build()

			result := IsVirtioNetDevice("/sys/class/net/eth1")

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario: EvalSymlinks returns an error", func() {
			mockErr := errors.New("filesystem error")
			Mock(filepath.EvalSymlinks).Return("", mockErr).Build()
			result := IsVirtioNetDevice("/sys/class/net/eth2")

			So(result, ShouldBeFalse)
		})
	})
}

func Test_GetNicVirtioName(t *testing.T) {
	PatchConvey("Test GetNicVirtioName", t, func() {
		nicSysPath := "/sys/class/net/eth0"
		nicDevSysPath := filepath.Join(nicSysPath, "device")
		PatchConvey("When EvalSymlinks succeeds, the correct virtio name should be returned", func() {
			expectedVirtioName := "virtio0"
			mockRealPath := fmt.Sprintf("/sys/devices/pci0000:00/0000:00:03.0/%s", expectedVirtioName)
			Mock(filepath.EvalSymlinks).Return(mockRealPath, nil).Build()

			// Act: execute the function under test
			virtioName, err := GetNicVirtioName(nicSysPath)

			So(err, ShouldBeNil)
			So(virtioName, ShouldEqual, expectedVirtioName)
		})

		PatchConvey("When EvalSymlinks fail, an error should be returned", func() {
			mockError := errors.New("symlink does not exist")
			Mock(filepath.EvalSymlinks).Return("", mockError).Build()

			virtioName, err := GetNicVirtioName(nicSysPath)

			So(err, ShouldNotBeNil)
			So(virtioName, ShouldBeEmpty)
			expectedErrMsg := fmt.Sprintf("failed to EvalSymlinks(%s), err %v", nicDevSysPath, mockError)
			So(err.Error(), ShouldEqual, expectedErrMsg)
		})
	})
}

func Test_GetNicNumaNode(t *testing.T) {
	nicSysPath := "/sys/class/net/eth0"

	PatchConvey("Test GetNicNumaNode", t, func() {
		PatchConvey("Success Scenario: Find the numa_node file under the direct path", func() {
			Mock(os.Stat).Return(nil, nil).Build()
			Mock(os.ReadFile).Return([]byte("1\n"), nil).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldBeNil)
			So(numa, ShouldEqual, 1)
		})

		PatchConvey("Success Scenario: Locate numa_node file via a symlinked path", func() {
			directNumaPath := filepath.Join(nicSysPath, "device/numa_node")
			symlinkDevPath := "/sys/devices/pci/00/01"
			symlinkNumaPath := filepath.Join(filepath.Dir(symlinkDevPath), "numa_node")

			Mock(os.Stat).To(func(path string) (os.FileInfo, error) {
				if path == directNumaPath {
					return nil, os.ErrNotExist
				}
				if path == symlinkNumaPath {
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected Stat call with path: %s", path)
			}).Build()

			Mock(filepath.EvalSymlinks).Return(symlinkDevPath, nil).Build()
			Mock(os.ReadFile).Return([]byte("0\n"), nil).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldBeNil)
			So(numa, ShouldEqual, 0)
		})

		PatchConvey("Failure Scenario: The first OS. Stat call failed with a non-'Not Exists' error", func() {
			expectedErr := errors.New("permission denied")
			Mock(os.Stat).Return(nil, expectedErr).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(numa, ShouldEqual, UnknownNumaNode)
		})

		PatchConvey("Failure scenario: EvalSymlinks call failed", func() {
			expectedErr := errors.New("failed to eval symlinks")
			Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
			Mock(filepath.EvalSymlinks).Return("", expectedErr).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(numa, ShouldEqual, UnknownNumaNode)
		})

		PatchConvey("Failure scenario: OS. ReadFile call failed", func() {
			expectedErr := errors.New("read file error")
			Mock(os.Stat).Return(nil, nil).Build()
			Mock(os.ReadFile).Return(nil, expectedErr).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(numa, ShouldEqual, UnknownNumaNode)
		})

		PatchConvey("Failure scenario: The file content is not numeric", func() {
			Mock(os.Stat).Return(nil, nil).Build()
			Mock(os.ReadFile).Return([]byte("not-a-number"), nil).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to Atoi")
			So(numa, ShouldEqual, UnknownNumaNode)
		})

		PatchConvey("Special Scenario: The file content is negative", func() {
			Mock(os.Stat).Return(nil, nil).Build()
			Mock(os.ReadFile).Return([]byte("-1\n"), nil).Build()

			numa, err := GetNicNumaNode(nicSysPath)

			So(err, ShouldBeNil)
			So(numa, ShouldEqual, UnknownNumaNode)
		})
	})
}

func Test_GetIfIndex(t *testing.T) {
	PatchConvey("Test GetIfIndex", t, func() {
		nicName := "eth0"
		nicSysPath := "/sys/class/net/eth0"
		PatchConvey("Scenario 1: net. InterfaceByName call succeeds", func() {
			expectedIndex := 5
			Mock(net.InterfaceByName).Return(&net.Interface{Index: expectedIndex}, nil).Build()

			index, err := GetIfIndex(nicName, nicSysPath)

			So(err, ShouldBeNil)
			So(index, ShouldEqual, expectedIndex)
		})

		PatchConvey("Scenario 2: net. The InterfaceByName call fails, but the GetIfIndexFromSys call succeeds", func() {
			mockErr := errors.New("interface not found")
			Mock(net.InterfaceByName).Return(nil, mockErr).Build()
			expectedIndex := 10
			Mock(GetIfIndexFromSys).Return(expectedIndex, nil).Build()

			index, err := GetIfIndex(nicName, nicSysPath)

			So(err, ShouldBeNil)
			So(index, ShouldEqual, expectedIndex)
		})

		PatchConvey("Scenario 3: net. Both InterfaceByName and GetIfIndexFromSys calls failed", func() {
			mockErrNet := errors.New("interface not found")
			Mock(net.InterfaceByName).Return(nil, mockErrNet).Build()

			mockErrSys := errors.New("failed to read from sys")
			Mock(GetIfIndexFromSys).Return(0, mockErrSys).Build()

			index, err := GetIfIndex(nicName, nicSysPath)

			So(index, ShouldEqual, -1)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to GetIfIndexFromSys(%s), err %v", nicSysPath, mockErrSys))
		})
	})
}

func Test_GetIfIndexFromSys(t *testing.T) {
	PatchConvey("Test GetIfIndexFromSys", t, func() {
		dummyNicSysPath := "/sys/class/net/eth0"

		PatchConvey("Success Scenario - When the file exists and the content is significant figures", func() {
			mockContent := []byte(" 42 \n")
			Mock(os.ReadFile).Return(mockContent, nil).Build()

			// Act: execute the function under test
			ifindex, err := GetIfIndexFromSys(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(ifindex, ShouldEqual, 42)
		})

		PatchConvey("Failure Scenario - When reading a file fails", func() {
			expectedErr := errors.New("permission denied")
			Mock(os.ReadFile).Return(nil, expectedErr).Build()

			// Act: execute the function under test
			ifindex, err := GetIfIndexFromSys(dummyNicSysPath)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, expectedErr)
			So(ifindex, ShouldEqual, 0)
		})

		PatchConvey("Failure Scenario - When the file content is not a number", func() {
			mockContent := []byte("not-a-number")
			Mock(os.ReadFile).Return(mockContent, nil).Build()

			// Act: execute the function under test
			ifindex, err := GetIfIndexFromSys(dummyNicSysPath)

			So(err, ShouldNotBeNil)
			So(ifindex, ShouldEqual, 0)
		})
	})
}

// mockFileInfo is a mock implementation of os.FileInfo for testing
// It embeds the os.FileInfo interface and overrides the IsDir method to return a controllable value
type mockFileInfo struct {
	os.FileInfo
	isDir bool
}

func (m *mockFileInfo) IsDir() bool {
	return m.isDir
}

func Test_IsBondingNetDevice(t *testing.T) {
	PatchConvey("Test IsBondingNetDevice", t, func() {
		nicSysPath := "/sys/class/net/test_nic"
		PatchConvey("Scenario 1: The bonding path exists and is a directory, and it should return true", func() {
			Mock(os.Stat).Return(&mockFileInfo{isDir: true}, nil).Build()

			// Act: execute the function under test
			result := IsBondingNetDevice(nicSysPath)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 2: The bonding path exists but is not a directory, and it should return false", func() {
			Mock(os.Stat).Return(&mockFileInfo{isDir: false}, nil).Build()

			result := IsBondingNetDevice(nicSysPath)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 3: OS. The Stat call returns an error (if the path does not exist), it should return false", func() {
			Mock(os.Stat).Return(nil, errors.New("path not found")).Build()

			result := IsBondingNetDevice(nicSysPath)

			So(result, ShouldBeFalse)
		})
	})
}

func Test_IsNetDevLinkUp(t *testing.T) {
	PatchConvey("Test IsNetDevLinkUp", t, func() {
		dummyNicSysPath := "/sys/class/net/eth0"
		PatchConvey("Scenario 1: When the operstate file content is 'up', it should return true", func() {
			Mock(os.ReadFile).Return([]byte("up\n"), nil).Build()
			result := IsNetDevLinkUp(dummyNicSysPath)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 2: When the operstate file content is 'down', false should be returned", func() {
			Mock(os.ReadFile).Return([]byte("down"), nil).Build()

			result := IsNetDevLinkUp(dummyNicSysPath)

			So(result, ShouldBeFalse)
		})

		PatchConvey("Scenario 3: When the operstate file content is 'up' with extra spaces and line breaks, true should be returned", func() {
			Mock(os.ReadFile).Return([]byte("  up  \n\n"), nil).Build()

			result := IsNetDevLinkUp(dummyNicSysPath)

			So(result, ShouldBeTrue)
		})

		PatchConvey("Scenario 4: When reading the operstate file fails, false should be returned and a warning log should be logged", func() {
			readErr := errors.New("permission denied")
			Mock(os.ReadFile).Return(nil, readErr).Build()

			result := IsNetDevLinkUp(dummyNicSysPath)

			So(result, ShouldBeFalse)
		})
	})
}

func Test_ListBondNetDevSlaves(t *testing.T) {
	PatchConvey("Test ListBondNetDevSlaves", t, func() {
		dummyNicSysPath := "/sys/class/net/bond0"
		PatchConvey("Successfully obtained the slaves list", func() {
			Mock(IsBondingNetDevice).Return(true).Build()
			Mock(os.ReadFile).Return([]byte("eth0 eth1\n"), nil).Build()

			// Act: execute the function under test
			slaves, err := ListBondNetDevSlaves(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(slaves, ShouldResemble, []string{"eth0", "eth1"})
		})

		PatchConvey("Non-bonding equipment", func() {
			Mock(IsBondingNetDevice).Return(false).Build()

			// Act: execute the function under test
			slaves, err := ListBondNetDevSlaves(dummyNicSysPath)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("%s is not bonding netdev", dummyNicSysPath))
			So(slaves, ShouldBeNil)
		})

		PatchConvey("Failed to read slaves file", func() {
			Mock(IsBondingNetDevice).Return(true).Build()
			readErr := errors.New("permission denied")
			Mock(os.ReadFile).Return(nil, readErr).Build()

			// Act: execute the function under test
			slaves, err := ListBondNetDevSlaves(dummyNicSysPath)

			So(err, ShouldNotBeNil)
			bondSlavesPath := filepath.Join(dummyNicSysPath, "bonding/slaves")
			expectedErr := fmt.Sprintf("failed to ReadFile(%s), err %v", bondSlavesPath, readErr)
			So(err.Error(), ShouldEqual, expectedErr)
			So(slaves, ShouldBeNil)
		})

		PatchConvey("SLAVES FILE IS EMPTY", func() {
			Mock(IsBondingNetDevice).Return(true).Build()
			Mock(os.ReadFile).Return([]byte(""), nil).Build()

			// Act: execute the function under test
			slaves, err := ListBondNetDevSlaves(dummyNicSysPath)

			So(err, ShouldBeNil)
			So(slaves, ShouldBeEmpty)
		})
	})
}

func TestListNetNS(t *testing.T) {
	PatchConvey("Test ListNetNS", t, func() {
		testNetNSDir := "/var/run/test_netns"
		PatchConvey("Scenario 1: Successfully obtain a list of network namespaces", func() {
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return([]os.DirEntry{
				&mockDirEntry{entryName: "ns1"},
				&mockDirEntry{entryName: "ns2"},
			}, nil).Build()
			Mock(general.GetFileInode).To(func(path string) (uint64, error) {
				if path == filepath.Join(testNetNSDir, "ns1") {
					return 67890, nil
				}
				if path == filepath.Join(testNetNSDir, "ns2") {
					return 67891, nil
				}
				return 0, errors.New("unexpected file path")
			}).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldBeNil)
			So(len(nsList), ShouldEqual, 3)
			So(nsList, ShouldResemble, []NetNSInfo{
				{NSName: "", NSInode: 12345, NSAbsDir: testNetNSDir},
				{NSName: "ns1", NSInode: 67890, NSAbsDir: testNetNSDir},
				{NSName: "ns2", NSInode: 67891, NSAbsDir: testNetNSDir},
			})
		})

		PatchConvey("Scenario 2: Use the default path when netNSDir is empty", func() {
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return([]os.DirEntry{}, nil).Build()

			nsList, err := ListNetNS("")

			So(err, ShouldBeNil)
			So(len(nsList), ShouldEqual, 1)
			So(nsList[0].NSAbsDir, ShouldEqual, DefaultNetNSDir)
		})

		PatchConvey("Scenario 3: Failing to obtain the host network namespace inode", func() {
			mockErr := errors.New("failed to get host inode")
			Mock(general.GetProcessNameSpaceInode).Return(uint64(0), mockErr).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(nsList, ShouldBeNil)
		})

		PatchConvey("Scenario 4: The network namespace directory does not exist", func() {
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return(nil, os.ErrNotExist).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldBeNil)
			So(len(nsList), ShouldEqual, 1)
			So(nsList[0].NSInode, ShouldEqual, 12345)
		})

		PatchConvey("Scenario 5: Failing to read the network namespace directory", func() {
			mockErr := errors.New("failed to read directory")
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return(nil, mockErr).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, mockErr)
			So(nsList, ShouldBeNil)
		})

		PatchConvey("Scenario 6: Failed to get file inodes", func() {
			mockErr := errors.New("failed to get file inode")
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return([]os.DirEntry{
				&mockDirEntry{entryName: "ns1", isDir: false},
			}, nil).Build()
			Mock(general.GetFileInode).Return(uint64(0), mockErr).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, mockErr.Error())
			So(nsList, ShouldBeNil)
		})

		PatchConvey("Scenario 7: Contains subdirectories in a directory should be ignored", func() {
			Mock(general.GetProcessNameSpaceInode).Return(uint64(12345), nil).Build()
			Mock(os.ReadDir).Return([]os.DirEntry{
				&mockDirEntry{entryName: "ns1", isDir: false},
				&mockDirEntry{entryName: "a_directory", isDir: true},
				&mockDirEntry{entryName: "ns2", isDir: false},
			}, nil).Build()
			Mock(general.GetFileInode).To(func(path string) (uint64, error) {
				if path == filepath.Join(testNetNSDir, "ns1") {
					return 67890, nil
				}
				if path == filepath.Join(testNetNSDir, "ns2") {
					return 67891, nil
				}
				return 0, errors.New("unexpected file path")
			}).Build()

			nsList, err := ListNetNS(testNetNSDir)

			So(err, ShouldBeNil)
			So(len(nsList), ShouldEqual, 3)
			So(nsList, ShouldResemble, []NetNSInfo{
				{NSName: "", NSInode: 12345, NSAbsDir: testNetNSDir},
				{NSName: "ns1", NSInode: 67890, NSAbsDir: testNetNSDir},
				{NSName: "ns2", NSInode: 67891, NSAbsDir: testNetNSDir},
			})
		})
	})
}

func Test_ListActiveUplinkNicsFromNetNS(t *testing.T) {
	PatchConvey("Test ListActiveUplinkNicsFromNetNS", t, func() {
		netnsInfo := NetNSInfo{NSName: "test_ns"}
		mockNsc := &netnsSwitchContext{sysMountDir: "/tmp/mock_sys"}
		Mock((*netnsSwitchContext).netnsExit).Return().Build()
		Mock(klog.Warningf).To(func(format string, args ...interface{}) {}).Build()

		PatchConvey("Scenario: Successfully obtain the information of a normal PCI NIC card", func() {
			// --- Arrange ---
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return([]string{"eth0"}, nil).Build()
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "eth0"},
			}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()
			Mock(IsPCIDevice).Return(true).Build()
			Mock(IsNetDevLinkUp).Return(true).Build()
			Mock(IsBondingNetDevice).Return(false).Build()
			Mock(GetIfIndex).Return(1, nil).Build()
			Mock(GetNicPCIAddr).Return("0000:01:00.0", nil).Build()
			Mock(getNicDriver).Return(NicDriverMLX, nil).Build()
			Mock(GetNicNumaNode).Return(0, nil).Build()
			Mock(IsVirtioNetDevice).Return(false).Build()
			Mock(GetNicIrqs).Return([]int{10, 11}, nil).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldBeNil)
			So(nics, ShouldNotBeNil)
			So(len(nics), ShouldEqual, 1)
			So(nics[0].Name, ShouldEqual, "eth0")
			So(nics[0].IfIndex, ShouldEqual, 1)
			So(nics[0].PCIAddr, ShouldEqual, "0000:01:00.0")
			So(nics[0].Driver, ShouldEqual, NicDriverMLX)
			So(nics[0].NumaNode, ShouldEqual, 0)
			So(nics[0].IsVirtioNetDev, ShouldBeFalse)
			So(nics[0].Irqs, ShouldResemble, []int{10, 11})
		})

		PatchConvey("Scenario: Successfully obtain the information of the active slave NIC of a bonding NIC", func() {
			// --- Arrange ---
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return([]string{"bond0"}, nil).Build()
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "bond0"},
				&mockDirEntry{entryName: "eth1"},
			}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()

			Mock(IsBondingNetDevice).When(func(path string) bool { return strings.HasSuffix(path, "bond0") }).Return(true).Build()
			Mock(ListBondNetDevSlaves).Return([]string{"eth1"}, nil).Build()

			Mock(IsPCIDevice).When(func(path string) bool { return strings.HasSuffix(path, "eth1") }).Return(true).Build()
			Mock(IsNetDevLinkUp).Return(true).Build()

			Mock(GetIfIndex).Return(2, nil).Build()
			Mock(GetNicPCIAddr).Return("0000:02:00.0", nil).Build()
			Mock(getNicDriver).Return(NicDriverI40E, nil).Build()
			Mock(GetNicNumaNode).Return(1, nil).Build()
			Mock(IsVirtioNetDevice).Return(false).Build()
			Mock(GetNicIrqs).Return([]int{20, 21}, nil).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldBeNil)
			So(len(nics), ShouldEqual, 1)
			So(nics[0].Name, ShouldEqual, "eth1")
			So(nics[0].PCIAddr, ShouldEqual, "0000:02:00.0")
			So(nics[0].Driver, ShouldEqual, NicDriverI40E)
		})

		PatchConvey("Scenario: Successfully obtain a Virtio NIC information", func() {
			// --- Arrange ---
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return([]string{"eth-virtio"}, nil).Build()
			mockEntries := []os.DirEntry{
				&mockDirEntry{entryName: "eth-virtio"},
			}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()
			Mock(IsPCIDevice).Return(true).Build()
			Mock(IsNetDevLinkUp).Return(true).Build()
			Mock(IsBondingNetDevice).Return(false).Build()
			Mock(GetIfIndex).Return(3, nil).Build()
			Mock(GetNicPCIAddr).Return("virtio0", nil).Build()
			Mock(getNicDriver).Return(NicDriverVirtioNet, nil).Build()
			Mock(GetNicNumaNode).Return(0, nil).Build()
			Mock(IsVirtioNetDevice).Return(true).Build()
			Mock(GetNicVirtioName).Return("virtio-net-pci-0000:00:05.0", nil).Build()
			Mock(GetNicIrqs).Return(nil, errors.New("no msi_irqs for virtio")).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldBeNil)
			So(len(nics), ShouldEqual, 1)
			So(nics[0].Name, ShouldEqual, "eth-virtio")
			So(nics[0].IsVirtioNetDev, ShouldBeTrue)
			So(nics[0].VirtioNetName, ShouldEqual, "virtio-net-pci-0000:00:05.0")
			So(nics[0].Irqs, ShouldBeNil)
		})

		PatchConvey("Scenario: Failed to enter netns", func() {
			// --- Arrange ---
			expectedErr := errors.New("netns enter failed")
			Mock(netnsEnter).Return(nil, expectedErr).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario: Failed to obtain a list of interfaces with IPs", func() {
			// --- Arrange ---
			expectedErr := errors.New("list interfaces failed")
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return(nil, expectedErr).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario: Failed to get network card details", func() {
			// --- Arrange ---
			expectedErr := errors.New("get pci addr failed")
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return([]string{"eth0"}, nil).Build()
			mockEntries := []os.DirEntry{&mockDirEntry{entryName: "eth0"}}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()
			Mock(IsPCIDevice).Return(true).Build()
			Mock(IsNetDevLinkUp).Return(true).Build()
			Mock(IsBondingNetDevice).Return(false).Build()
			Mock(GetIfIndex).Return(1, nil).Build()
			Mock(GetNicPCIAddr).Return("", expectedErr).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, expectedErr.Error())
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario: No active Uplink network card found", func() {
			// --- Arrange ---
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(ListInterfacesWithIPAddr).Return([]string{"eth0"}, nil).Build()
			mockEntries := []os.DirEntry{&mockDirEntry{entryName: "eth0"}}
			Mock(os.ReadDir).Return(mockEntries, nil).Build()
			Mock(IsPCIDevice).Return(true).Build()
			Mock(IsNetDevLinkUp).Return(false).Build()
			Mock(IsBondingNetDevice).Return(false).Build()

			// --- Act ---
			nics, err := ListActiveUplinkNicsFromNetNS(netnsInfo)

			// --- Assert ---
			So(err, ShouldBeNil)
			So(nics, ShouldBeEmpty)
		})
	})
}

func Test_ListActiveUplinkNics(t *testing.T) {
	PatchConvey("Test ListActiveUplinkNics", t, func() {
		const mockNetNSDir = "/var/run/netns"

		PatchConvey("Scenario 1: Successfully obtain the information of active NICs in all network namespaces", func() {
			mockNetNSList := []NetNSInfo{
				{NSName: "default", NSInode: 1001},
				{NSName: "ns1", NSInode: 1002},
			}
			mockNicsFromDefaultNS := []*NicBasicInfo{
				{InterfaceInfo: InterfaceInfo{Name: "eth0", IfIndex: 1}},
			}
			mockNicsFromNs1 := []*NicBasicInfo{
				{InterfaceInfo: InterfaceInfo{Name: "eth1", IfIndex: 2}},
			}
			mockQueue2Irq := map[int]int{0: 100, 1: 101}
			mockTxQueue2Irq := map[int]int{0: 200, 1: 201}
			expectedIrq2Queue := map[int]int{100: 0, 101: 1}
			expectedTxIrq2Queue := map[int]int{100: 0, 101: 1}

			Mock(ListNetNS).Return(mockNetNSList, nil).Build()
			Mock(ListActiveUplinkNicsFromNetNS).
				To(func(ns NetNSInfo) ([]*NicBasicInfo, error) {
					if ns.NSName == "default" {
						return mockNicsFromDefaultNS, nil
					}
					if ns.NSName == "ns1" {
						return mockNicsFromNs1, nil
					}
					return nil, nil
				}).Build()
			Mock(GetNicQueue2Irq).Return(mockQueue2Irq, mockTxQueue2Irq, nil).Build()

			nics, err := ListActiveUplinkNics(mockNetNSDir)

			So(err, ShouldBeNil)
			So(nics, ShouldNotBeNil)
			So(len(nics), ShouldEqual, 2)

			So(nics[0].Name, ShouldEqual, "eth0")
			So(nics[0].QueueNum, ShouldEqual, len(mockQueue2Irq))
			So(nics[0].Queue2Irq, ShouldResemble, mockQueue2Irq)
			So(nics[0].Irq2Queue, ShouldResemble, expectedIrq2Queue)
			So(nics[0].TxQueue2Irq, ShouldResemble, mockTxQueue2Irq)
			So(nics[0].TxIrq2Queue, ShouldResemble, expectedTxIrq2Queue)

			So(nics[1].Name, ShouldEqual, "eth1")
			So(nics[1].QueueNum, ShouldEqual, len(mockQueue2Irq))
			So(nics[1].Queue2Irq, ShouldResemble, mockQueue2Irq)
			So(nics[1].Irq2Queue, ShouldResemble, expectedIrq2Queue)
			So(nics[1].TxQueue2Irq, ShouldResemble, mockTxQueue2Irq)
			So(nics[1].TxIrq2Queue, ShouldResemble, expectedTxIrq2Queue)
		})

		PatchConvey("Scenario 2: When a ListNetNS call fails, an error should be returned", func() {
			mockErr := errors.New("failed to list netns")
			Mock(ListNetNS).Return(nil, mockErr).Build()

			nics, err := ListActiveUplinkNics(mockNetNSDir)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to ListNetNS, err %v", mockErr))
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario 3: When the ListActiveUplinkNicsFromNetNS call fails, an error should be returned", func() {
			mockNetNSList := []NetNSInfo{{NSName: "default", NSInode: 1001}}
			mockErr := errors.New("failed to list nics from netns")
			Mock(ListNetNS).Return(mockNetNSList, nil).Build()
			Mock(ListActiveUplinkNicsFromNetNS).Return(nil, mockErr).Build()

			nics, err := ListActiveUplinkNics(mockNetNSDir)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to ListActiveUplinkNicsFromNetNS(%s), err %v", mockNetNSList[0].NSName, mockErr))
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario 4: When the GetNicQueue2Irq call fails, an error should be returned", func() {
			mockNetNSList := []NetNSInfo{{NSName: "default", NSInode: 1001}}
			mockNicsFromNS := []*NicBasicInfo{{InterfaceInfo: InterfaceInfo{Name: "eth0", IfIndex: 1}}}
			mockErr := errors.New("failed to get queue to irq mapping")

			Mock(ListNetNS).Return(mockNetNSList, nil).Build()
			Mock(ListActiveUplinkNicsFromNetNS).Return(mockNicsFromNS, nil).Build()
			Mock(GetNicQueue2Irq).Return(nil, nil, mockErr).Build()

			nics, err := ListActiveUplinkNics(mockNetNSDir)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("failed to GetNicQueue2Irq for %d: %s, err %v", mockNicsFromNS[0].IfIndex, mockNicsFromNS[0].Name, mockErr))
			So(nics, ShouldBeNil)
		})

		PatchConvey("Scenario 5: When no active NIC is found, an empty list and nil error should be returned", func() {
			mockNetNSList := []NetNSInfo{{NSName: "default", NSInode: 1001}}
			Mock(ListNetNS).Return(mockNetNSList, nil).Build()
			Mock(ListActiveUplinkNicsFromNetNS).Return([]*NicBasicInfo{}, nil).Build()

			nics, err := ListActiveUplinkNics(mockNetNSDir)

			So(err, ShouldBeNil)
			So(nics, ShouldBeEmpty)
		})
	})
}

func Test_ListInterfacesWithIPAddr(t *testing.T) {
	PatchConvey("Test ListInterfacesWithIPAddr", t, func() {
		PatchConvey("Scenario 1: When net. When the Interfaces call fails, an error should be returned", func() {
			mockErr := errors.New("failed to get interfaces")
			Mock(net.Interfaces).Return(nil, mockErr).Build()

			intfs, err := ListInterfacesWithIPAddr()

			So(err, ShouldBeError, mockErr)
			So(intfs, ShouldBeNil)
		})

		PatchConvey("Scenario 2: When there are interfaces with multiple states, only the interface names that meet the conditions should be returned", func() {
			localIP := net.ParseIP("169.254.1.1")
			_, globalAddr, _ := net.ParseCIDR("10.194.18.162/26")
			localAddr := &net.IPAddr{IP: localIP}

			mockInterfaces := []net.Interface{
				{Name: "eth0", Flags: net.FlagUp},
				{Name: "eth1", Flags: 0},
				{Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
				{Name: "eth2", Flags: net.FlagUp},
				{Name: "eth3", Flags: net.FlagUp},
				{Name: "eth4", Flags: net.FlagUp},
				{Name: "veth0", Flags: net.FlagUp},
			}

			Mock(net.Interfaces).Return(mockInterfaces, nil).Build()

			Mock((*net.Interface).Addrs).To(func(i *net.Interface) ([]net.Addr, error) {
				switch i.Name {
				case "eth0":
					return []net.Addr{globalAddr}, nil
				case "eth2":
					return nil, errors.New("failed to get addrs")
				case "eth3":
					return []net.Addr{}, nil
				case "eth4":
					return []net.Addr{localAddr}, nil
				case "veth0":
					return []net.Addr{localAddr, globalAddr}, nil
				default:
					return []net.Addr{}, nil
				}
			}).Build()

			intfs, err := ListInterfacesWithIPAddr()

			So(err, ShouldBeNil)
			So(intfs, ShouldResemble, []string{"eth0", "veth0"})
		})

		PatchConvey("Scenario 3: When all interfaces do not have global unicast IPs, empty slices should be returned", func() {
			_, localAddr, _ := net.ParseCIDR("192.168.1.100/24")

			mockInterfaces := []net.Interface{
				{Name: "eth0", Flags: net.FlagUp},
				{Name: "eth1", Flags: net.FlagUp},
			}

			Mock(net.Interfaces).Return(mockInterfaces, nil).Build()

			Mock((*net.Interface).Addrs).Return([]net.Addr{localAddr}, nil).Build()

			Mock((net.IP).IsGlobalUnicast).To(func(ip net.IP) bool {
				return false
			}).Build()

			intfs, err := ListInterfacesWithIPAddr()

			So(err, ShouldBeNil)
			So(intfs, ShouldBeEmpty)
		})

		PatchConvey("Scenario 4: When there is no network interface, an empty slice should be returned", func() {
			Mock(net.Interfaces).Return([]net.Interface{}, nil).Build()

			intfs, err := ListInterfacesWithIPAddr()

			So(err, ShouldBeNil)
			So(intfs, ShouldBeEmpty)
		})
	})
}

func newTestNicBasicInfo(driver NicDriver) *NicBasicInfo {
	return &NicBasicInfo{
		InterfaceInfo: InterfaceInfo{
			NetNSInfo: NetNSInfo{
				NSName: "test-ns",
			},
			Name: "eth0",
		},
		Driver:   driver,
		QueueNum: 4,
	}
}

func Test_GetNicRxQueuePackets(t *testing.T) {
	PatchConvey("Test GetNicRxQueuePackets", t, func() {
		mockEthtool := &ethtool.Ethtool{}
		mockNetnsCtx := &netnsSwitchContext{}
		errNetns := errors.New("netns enter failed")
		errEthtoolNew := errors.New("ethtool new failed")
		errEthtoolStats := errors.New("ethtool stats failed")

		PatchConvey("When the driver type is not supported", func() {
			nic := newTestNicBasicInfo(NicDriverUnknown)

			result, err := GetNicRxQueuePackets(nic)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, fmt.Sprintf("unknow driver: %s", NicDriverUnknown))
			So(result, ShouldBeNil)
		})

		PatchConvey("When entering a network namespace fails", func() {
			Mock(netnsEnter).Return(nil, errNetns).Build()
			nic := newTestNicBasicInfo(NicDriverMLX)

			result, err := GetNicRxQueuePackets(nic)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, errNetns.Error())
			So(result, ShouldBeNil)
		})

		PatchConvey("When creating ethtool handles fails", func() {
			Mock(netnsEnter).Return(mockNetnsCtx, nil).Build()
			Mock((*netnsSwitchContext).netnsExit).Return().Build()
			Mock(ethtool.NewEthtool).Return(nil, errEthtoolNew).Build()
			nic := newTestNicBasicInfo(NicDriverMLX)

			result, err := GetNicRxQueuePackets(nic)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, errEthtoolNew)
			So(result, ShouldBeNil)
		})

		PatchConvey("When getting ethtool statistics fails", func() {
			Mock(netnsEnter).Return(mockNetnsCtx, nil).Build()
			Mock((*netnsSwitchContext).netnsExit).Return().Build()
			Mock(ethtool.NewEthtool).Return(mockEthtool, nil).Build()
			Mock((*ethtool.Ethtool).Close).Return().Build()
			Mock((*ethtool.Ethtool).Stats).Return(nil, errEthtoolStats).Build()
			nic := newTestNicBasicInfo(NicDriverMLX)

			result, err := GetNicRxQueuePackets(nic)

			So(err, ShouldNotBeNil)
			So(err, ShouldEqual, errEthtoolStats)
			So(result, ShouldBeNil)
		})

		driverTestCases := []struct {
			driver      NicDriver
			stats       map[string]uint64
			expected    map[int]uint64
			description string
		}{
			{
				driver: NicDriverMLX,
				stats: map[string]uint64{
					"rx0_packets":    100,
					"rx1_packets":    200,
					"tx0_packets":    999,
					"rx_invalid_key": 123,
				},
				expected:    map[int]uint64{0: 100, 1: 200},
				description: "Successfully parsing MLX-powered statistics",
			},
			{
				driver: NicDriverBNX,
				stats: map[string]uint64{
					"[0]: rx_ucast_packets": 101,
					"[2]: rx_ucast_packets": 202,
					"[1]: tx_ucast_packets": 888,
				},
				expected:    map[int]uint64{0: 101, 2: 202},
				description: "Successfully parsing BNX-driven statistics",
			},
			{
				driver: NicDriverVirtioNet,
				stats: map[string]uint64{
					"rx_queue_0_packets": 103,
					"rx_queue_3_packets": 204,
					"rx_queue_5_packets": 999,
				},
				expected:    map[int]uint64{0: 103, 3: 204},
				description: "Successfully parsed VirtioNet-driven statistics",
			},
			{
				driver: NicDriverI40E,
				stats: map[string]uint64{
					"rx-0.packets": 105,
					"rx-1.packets": 206,
					"rx-a.packets": 777,
				},
				expected:    map[int]uint64{0: 105, 1: 206},
				description: "Successfully parsed the statistics of the I40E driver",
			},
			{
				driver: NicDriverIXGBE,
				stats: map[string]uint64{
					"rx_queue_0_packets": 107,
					"rx_queue_1_packets": 208,
				},
				expected:    map[int]uint64{0: 107, 1: 208},
				description: "Successfully parsed IXGBE-driven statistics",
			},
		}

		for _, tc := range driverTestCases {
			currentTC := tc
			PatchConvey(currentTC.description, func() {
				Mock(netnsEnter).Return(mockNetnsCtx, nil).Build()
				Mock((*netnsSwitchContext).netnsExit).Return().Build()
				Mock(ethtool.NewEthtool).Return(mockEthtool, nil).Build()
				Mock((*ethtool.Ethtool).Close).Return().Build()
				Mock((*ethtool.Ethtool).Stats).Return(currentTC.stats, nil).Build()
				nic := newTestNicBasicInfo(currentTC.driver)

				result, err := GetNicRxQueuePackets(nic)

				So(err, ShouldBeNil)
				So(result, ShouldNotBeNil)
				So(result, ShouldResemble, currentTC.expected)
			})
		}
	})
}

func Test_GetNetDevRxPackets(t *testing.T) {
	PatchConvey("Test GetNetDevRxPackets", t, func() {
		nic := &NicBasicInfo{
			InterfaceInfo: InterfaceInfo{
				Name: "eth0",
				NetNSInfo: NetNSInfo{
					NSName: "test-ns",
				},
			},
		}
		mockNsc := &netnsSwitchContext{
			sysMountDir: "/tmp",
		}

		Mock((*netnsSwitchContext).netnsExit).To(func(_ *netnsSwitchContext) {}).Build()
		Mock(klog.Warningf).To(func(format string, args ...interface{}) {}).Build()
		Mock(klog.Errorf).To(func(format string, args ...interface{}) {}).Build()

		PatchConvey("Scenario 1: Successfully fetch data from the /sys/class/net file", func() {
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(os.Stat).Return(nil, nil).Build() // os.Stat 成功
			Mock(general.ReadUint64FromFile).Return(uint64(12345), nil).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldBeNil)
			So(packets, ShouldEqual, 12345)
		})

		PatchConvey("Scenario 2: Fail from /sys file, fall back to /proc/net/dev to successfully retrieve", func() {
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(os.Stat).Return(nil, errors.New("stat error")).Build() // os.Stat 失败

			mockNetDevs := map[string]procfs.NetDevLine{
				"eth0": {
					Name:      "eth0",
					RxPackets: 54321,
				},
				"lo": {
					Name:      "lo",
					RxPackets: 999,
				},
			}
			Mock(procm.GetNetDev).Return(mockNetDevs, nil).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldBeNil)
			So(packets, ShouldEqual, 54321)
		})

		PatchConvey("Scenario 3: netnsEnter fails", func() {
			expectedErr := errors.New("netns enter failed")
			Mock(netnsEnter).Return(nil, expectedErr).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "netns enter failed")
			So(packets, ShouldEqual, 0)
		})

		PatchConvey("Scenario 4: GetNetDev fails when falling back to /proc/net/dev", func() {
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(os.Stat).Return(nil, errors.New("stat error")).Build()
			expectedErr := errors.New("get net dev failed")
			Mock(procm.GetNetDev).Return(nil, expectedErr).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "get net dev failed")
			So(packets, ShouldEqual, 0)
		})

		PatchConvey("Scenario 5: When falling back to /proc/net/dev, the corresponding NIC cannot be found", func() {
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(os.Stat).Return(nil, errors.New("stat error")).Build()

			mockNetDevs := map[string]procfs.NetDevLine{
				"lo": {
					Name:      "lo",
					RxPackets: 999,
				},
			}
			Mock(procm.GetNetDev).Return(mockNetDevs, nil).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to find eth0")
			So(packets, ShouldEqual, 0)
		})

		PatchConvey("Scenario 6: Failed to read file contents from /sys, and fell back to /proc/net/dev to successfully obtain it", func() {
			Mock(netnsEnter).Return(mockNsc, nil).Build()
			Mock(os.Stat).Return(nil, nil).Build()
			Mock(general.ReadUint64FromFile).Return(uint64(0), errors.New("read file error")).Build()

			mockNetDevs := map[string]procfs.NetDevLine{
				"eth0": {
					Name:      "eth0",
					RxPackets: 67890,
				},
			}
			Mock(procm.GetNetDev).Return(mockNetDevs, nil).Build()

			packets, err := GetNetDevRxPackets(nic)

			So(err, ShouldBeNil)
			So(packets, ShouldEqual, 67890)
		})
	})
}
