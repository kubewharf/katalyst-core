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
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/moby/sys/mountinfo"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/vishvananda/netns"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func testDoNetNS() {
	Convey("Test DoNetNS", func() {
		Convey("non-default namespace", func() {
			mockey.PatchConvey("should handle mount/unmount correctly", func() {
				mockMkdirAll := mockey.Mock(os.MkdirAll).Return(nil).Build()
				mockMount := mockey.Mock(syscall.Mount).Return(nil).Build()
				mockUnmount := mockey.Mock(syscall.Unmount).Return(nil).Build()
				mockGetFromPath := mockey.Mock(netns.GetFromPath).Return(netns.NsHandle(1), nil).Build()
				mockSet := mockey.Mock(netns.Set).Return(nil).Build()
				mockMounted := mockey.Mock(mountinfo.Mounted).Return(true, nil).Build()

				defer func() {
					mockMkdirAll.UnPatch()
					mockMount.UnPatch()
					mockUnmount.UnPatch()
					mockGetFromPath.UnPatch()
					mockSet.UnPatch()
					mockMounted.UnPatch()
				}()

				err := DoNetNS("test-ns", "/test/path", func(sysFsDir, nsAbsPath string) error {
					return nil
				})
				So(err, ShouldBeNil)
			})
		})

		Convey("error cases", func() {
			mockey.PatchConvey("should handle mount error", func() {
				mockMkdirAll := mockey.Mock(os.MkdirAll).Return(nil).Build()
				mockMount := mockey.Mock(syscall.Mount).Return(fmt.Errorf("mount error")).Build()
				mockGetFromPath := mockey.Mock(netns.GetFromPath).Return(netns.NsHandle(1), nil).Build()
				mockSet := mockey.Mock(netns.Set).Return(nil).Build()

				defer func() {
					mockMkdirAll.UnPatch()
					mockMount.UnPatch()
					mockGetFromPath.UnPatch()
					mockSet.UnPatch()
				}()

				err := DoNetNS("test-ns", "/test/path", func(sysFsDir, nsAbsPath string) error {
					return nil
				})
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func testGetNSNetworkHardwareTopology() {
	Convey("Test getNSNetworkHardwareTopology", func() {
		Convey("single namespace case", func() {
			mockey.PatchConvey("should return network interfaces for single namespace", func() {
				mockDoNetNS := mockey.Mock(DoNetNS).Return(nil).Build()
				mockReadDir := mockey.Mock(os.ReadDir).Return([]os.DirEntry{}, nil).Build()
				mockEvalSymlinks := mockey.Mock(filepath.EvalSymlinks).Return("", nil).Build()
				mockGetInterfaceAddr := mockey.Mock(getInterfaceAddr).Return(map[string]*IfaceAddr{}, nil).Build()

				defer func() {
					mockDoNetNS.UnPatch()
					mockReadDir.UnPatch()
					mockEvalSymlinks.UnPatch()
					mockGetInterfaceAddr.UnPatch()
				}()

				result, err := getNSNetworkHardwareTopology("test-ns", "/test/path")
				So(err, ShouldBeNil)
				So(result, ShouldNotBeNil)
			})
		})

		Convey("error cases", func() {
			mockey.PatchConvey("should handle DoNetNS error", func() {
				mockDoNetNS := mockey.Mock(DoNetNS).Return(fmt.Errorf("test error")).Build()
				defer mockDoNetNS.UnPatch()

				_, err := getNSNetworkHardwareTopology("test-ns", "/test/path")
				So(err, ShouldNotBeNil)
			})

			mockey.PatchConvey("should handle ReadDir error", func() {
				mockDoNetNS := mockey.Mock(DoNetNS).Return(nil).Build()
				mockReadDir := mockey.Mock(os.ReadDir).Return(nil, fmt.Errorf("test error")).Build()
				defer func() {
					mockDoNetNS.UnPatch()
					mockReadDir.UnPatch()
				}()

				_, err := getNSNetworkHardwareTopology("test-ns", "/test/path")
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func testGetInterfaceAttr() {
	Convey("Test getInterfaceAttr", func() {
		mockey.PatchConvey("should get interface attributes successfully", func() {
			mockReadFileIntoInt := mockey.Mock(general.ReadFileIntoInt).Return(1, nil).Build()
			mockIsPathExists := mockey.Mock(general.IsPathExists).Return(true).Build()
			mockReadFileIntoLines := mockey.Mock(general.ReadFileIntoLines).Return([]string{"up"}, nil).Build()
			defer func() {
				mockReadFileIntoInt.UnPatch()
				mockIsPathExists.UnPatch()
				mockReadFileIntoLines.UnPatch()
			}()

			info := &InterfaceInfo{Iface: "eth0"}
			getInterfaceAttr(info, "/sys/class/net/eth0")
			So(info.NumaNode, ShouldEqual, 1)
			So(info.Enable, ShouldBeTrue)
			So(info.Speed, ShouldEqual, 1)
		})

		mockey.PatchConvey("should handle read file errors", func() {
			mockReadFileIntoInt := mockey.Mock(general.ReadFileIntoInt).Return(-1, fmt.Errorf("test error")).Build()
			mockIsPathExists := mockey.Mock(general.IsPathExists).Return(false).Build()
			mockReadFileIntoLines := mockey.Mock(general.ReadFileIntoLines).Return(nil, fmt.Errorf("test error")).Build()
			defer func() {
				mockReadFileIntoInt.UnPatch()
				mockIsPathExists.UnPatch()
				mockReadFileIntoLines.UnPatch()
			}()

			info := &InterfaceInfo{Iface: "eth0"}
			getInterfaceAttr(info, "/sys/class/net/eth0")
			So(info.NumaNode, ShouldEqual, -1)
			So(info.Enable, ShouldBeFalse)
			So(info.Speed, ShouldEqual, -1)
		})
	})
}

func TestGetExtraNetworkInfo(t *testing.T) {
	t.Parallel()
	Convey("Test GetExtraNetworkInfo", t, func() {
		testGetNSNetworkHardwareTopology()
		testDoNetNS()
		testGetInterfaceAttr()

		Convey("default case", func() {
			mockey.PatchConvey("should return empty network info when config is nil", func() {
				result, err := GetExtraNetworkInfo(nil)
				So(err, ShouldBeNil)
				So(result, ShouldNotBeNil)
				So(len(result.Interface), ShouldEqual, 0)
			})
		})

		Convey("single namespace case", func() {
			mockey.PatchConvey("should return network info from default namespace", func() {
				mockGetNSNetworkHardwareTopology := mockey.Mock(getNSNetworkHardwareTopology).Return([]InterfaceInfo{{
					Iface:    "eth0",
					NumaNode: 0,
					Enable:   true,
				}}, nil).Build()
				defer mockGetNSNetworkHardwareTopology.UnPatch()

				conf := &global.MachineInfoConfiguration{
					NetMultipleNS: false,
				}

				result, err := GetExtraNetworkInfo(conf)
				So(err, ShouldBeNil)
				So(result, ShouldNotBeNil)
				So(len(result.Interface), ShouldEqual, 1)
			})
		})

		Convey("multiple namespace case", func() {
			mockey.PatchConvey("should return network info from multiple namespaces", func() {
				mockGetNSNetworkHardwareTopology := mockey.Mock(getNSNetworkHardwareTopology).Return([]InterfaceInfo{{
					Iface:    "eth0",
					NumaNode: 0,
					Enable:   true,
				}}, nil).Build()
				defer mockGetNSNetworkHardwareTopology.UnPatch()

				conf := &global.MachineInfoConfiguration{
					NetMultipleNS:   true,
					NetNSDirAbsPath: "/test/path",
				}

				result, err := GetExtraNetworkInfo(conf)
				So(err, ShouldBeNil)
				So(result, ShouldNotBeNil)
				So(len(result.Interface), ShouldEqual, 1)
			})
		})

		Convey("error cases", func() {
			mockey.PatchConvey("should return error when getNSNetworkHardwareTopology fails", func() {
				mockGetNSNetworkHardwareTopology := mockey.Mock(getNSNetworkHardwareTopology).Return(nil, fmt.Errorf("test error")).Build()
				defer mockGetNSNetworkHardwareTopology.UnPatch()

				conf := &global.MachineInfoConfiguration{
					NetMultipleNS: false,
				}

				result, err := GetExtraNetworkInfo(conf)
				So(err, ShouldNotBeNil)
				So(result, ShouldBeNil)
			})
		})
	})
}
