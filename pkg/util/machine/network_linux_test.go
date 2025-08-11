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
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	. "github.com/bytedance/mockey"
	"github.com/moby/sys/mountinfo"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/vishvananda/netns"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
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
	t.Parallel()
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
	mockLock.Lock()
	defer mockLock.Unlock()
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
