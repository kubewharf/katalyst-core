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
	"strings"
	"testing"

	. "github.com/bytedance/mockey"
	"github.com/safchain/ethtool"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/vishvananda/netlink"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

func TestIsSriovPf(t *testing.T) {
	PatchConvey("TestIsSriovPf", t, func() {
		PatchConvey("is pf", func() {
			Mock(os.Stat).Return(nil, os.ErrNotExist).Build()
			res := isSriovPf("/sys", "eth0")
			So(res, ShouldBeFalse)
		})

		PatchConvey("is not pf", func() {
			Mock(os.Stat).
				When(func(name string) bool { return strings.HasSuffix(name, netFileNamePhysfn) }).Return(nil, os.ErrNotExist).
				When(func(name string) bool { return strings.Contains(name, netFileNameNumVFS) }).Return(nil, nil).
				Build()
			res := isSriovPf("/sys", "eth0")
			So(res, ShouldBeTrue)
		})
	})
}

func TestDetectSriovPFDriver(t *testing.T) {
	PatchConvey("TestDetectSriovPFDriver", t, func() {
		PatchConvey("mlx", func() {
			Mock(ethtool.DriverName).Return(MellanoxPFDriverName, nil).Build()
			driver, err := detectSriovPFDriver("eth0")
			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverMLX)
		})

		PatchConvey("bnx", func() {
			Mock(ethtool.DriverName).Return(BroadComPFDriverName, nil).Build()
			driver, err := detectSriovPFDriver("eth0")
			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverBNX)
		})

		PatchConvey("unknown", func() {
			Mock(ethtool.DriverName).Return("mlx5e_rep", nil).Build()
			driver, err := detectSriovPFDriver("eth0_0")
			So(err, ShouldBeNil)
			So(driver, ShouldEqual, NicDriverUnknown)
		})
	})
}

func TestGetBrcmPfIndex(t *testing.T) {
	deviceID := []byte("0x1750")

	PatchConvey("TestGetBrcmPfIndex", t, func() {
		Mock(os.ReadFile).Return(deviceID, nil).Build()
		Mock(os.ReadDir).Return([]os.DirEntry{
			&mockDirEntry{entryName: "0000:41:00.0", isDir: true},
			&mockDirEntry{entryName: "0000:41:00.1", isDir: true},
			&mockDirEntry{entryName: "0000:c1:00.0", isDir: true},
			&mockDirEntry{entryName: "0000:c1:00.1", isDir: true},
		}, nil).Build()

		index0, err := getBrcmPfIndex("/sys", "0000:41:00.0")
		So(err, ShouldBeNil)
		So(index0, ShouldEqual, 0)

		index1, err := getBrcmPfIndex("/sys", "0000:41:00.1")
		So(err, ShouldBeNil)
		So(index1, ShouldEqual, 1)
	})
}

func TestGetVfRepresenterMap(t *testing.T) {
	PatchConvey("TestGetVfRepresenterMap", t, func() {
		switchID := []byte("eaab570003e1a258")

		PatchConvey("mlx", func() {
			Mock(os.ReadFile).
				When(func(s string) bool { return strings.HasSuffix(s, netDevPhysSwitchID) }).Return(switchID, nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth0_0", netDevPhysPortName)) }).Return([]byte("pf0vf0"), nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth0_1", netDevPhysPortName)) }).Return([]byte("pf0vf1"), nil).
				Build()
			Mock(os.ReadDir).Return([]os.DirEntry{
				&mockDirEntry{entryName: "eth0_0", isDir: true},
				&mockDirEntry{entryName: "eth0_1", isDir: true},
			}, nil).Build()

			res, err := getVfRepresenterMap("/sys", "eth0", "device/net")

			So(err, ShouldBeNil)
			So(res, ShouldResemble, map[int]string{
				0: "eth0_0",
				1: "eth0_1",
			})
		})

		PatchConvey("brcm", func() {
			Mock(os.ReadFile).
				When(func(s string) bool { return strings.HasSuffix(s, netDevPhysSwitchID) }).Return(switchID, nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth0_0", netDevPhysPortName)) }).Return([]byte("pf0vf0"), nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth0_1", netDevPhysPortName)) }).Return([]byte("pf0vf1"), nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth1_0", netDevPhysPortName)) }).Return([]byte("pf1vf0"), nil).
				When(func(s string) bool { return strings.Contains(s, filepath.Join("eth1_1", netDevPhysPortName)) }).Return([]byte("pf1vf1"), nil).
				Build()
			Mock(os.ReadDir).Return([]os.DirEntry{
				&mockDirEntry{entryName: "eth0_0", isDir: true},
				&mockDirEntry{entryName: "eth0_1", isDir: true},
				&mockDirEntry{entryName: "eth1_0", isDir: true},
				&mockDirEntry{entryName: "eth1_1", isDir: true},
			}, nil).Build()

			res, err := getVfRepresenterMap("/sys", "eth0", "subsystem", brcmVfRepresenterFilter(0))

			So(err, ShouldBeNil)
			So(res, ShouldResemble, map[int]string{
				0: "eth0_0",
				1: "eth0_1",
			})
		})
	})
}

func TestGetSriovVFList(t *testing.T) {
	PatchConvey("TestGetSriovVFList", t, func() {
		Mock(DoNetNS).Return(nil).Build()

		Mock(isSriovPf).To(func(sysFsDir string, ifName string) bool { return ifName == "eth0" || ifName == "eth1" }).Build()

		Mock(ethtool.DriverName).To(func(ifName string) (string, error) {
			switch ifName {
			case "eth0", "eth1":
				return MellanoxPFDriverName, nil
			case "eth0_0":
				return "mlx5e_rep", nil
			}
			return "", nil
		}).Build()

		Mock(os.ReadDir).Return([]os.DirEntry{
			&mockDirEntry{entryName: "eth0_0", isDir: true},
			&mockDirEntry{entryName: "eth0_1", isDir: true},
		}, nil).Build()

		Mock(getVfRepresenterMap).To(func(sysFsDir string, pfName string, devicePath string, filters ...vfRepresenterFilter) (map[int]string, error) {
			result := map[int]string{}
			for i := 0; i < 3; i++ {
				result[i] = fmt.Sprintf("%s_%d", pfName, i)
			}
			return result, nil
		}).Build()

		Mock(getVfLinkMap).To(func(pfName string) (map[int]netlink.VfInfo, error) {
			result := map[int]netlink.VfInfo{}
			for i := 0; i < 3; i++ {
				var trust uint32
				if i >= 2 {
					trust = 1
				}
				result[i] = netlink.VfInfo{
					Trust: trust,
				}
			}
			return result, nil
		}).Build()

		Mock(getVfPCIMap).To(func(sysFsDir string, pfPCIAddr string) (map[int]string, error) {
			pciPrefix := strings.Split(pfPCIAddr, ".")[0]
			result := map[int]string{}
			for i := 0; i < 3; i++ {
				result[i] = fmt.Sprintf("%s.%d", pciPrefix, i+1)
			}
			return result, nil
		}).Build()

		vfList, err := GetSriovVFList(&global.MachineInfoConfiguration{
			NetMultipleNS:    true,
			NetAllocatableNS: []string{"ns2"},
			NetNSDirAbsPath:  "/var/run/netns/",
		}, []InterfaceInfo{
			{Name: "eth0", PCIAddr: "0000:41:00.0", NumaNode: 0},
			{Name: "eth0_0", PCIAddr: "0000:41:00.0", NumaNode: 0},
			{Name: "eth1", NetNSInfo: NetNSInfo{NSName: "ns2"}, PCIAddr: "0000:c1:00.0", NumaNode: 2},
			{Name: "ethxx", NetNSInfo: NetNSInfo{NSName: "cni-4b21e8c8-ab8b-213d-caa4-33a509190c21"}, PCIAddr: "0000:c1:00.0"},
		})

		So(err, ShouldBeNil)
		So(vfList, ShouldResemble, []SriovVFInfo{
			{
				PFInfo: InterfaceInfo{
					Name:     "eth0",
					PCIAddr:  "0000:41:00.0",
					NumaNode: 0,
				},
				Index:   0,
				PCIAddr: "0000:41:00.1",
				RepName: "eth0_0",
			},
			{
				PFInfo: InterfaceInfo{
					Name:     "eth0",
					PCIAddr:  "0000:41:00.0",
					NumaNode: 0,
				},
				Index:   1,
				PCIAddr: "0000:41:00.2",
				RepName: "eth0_1",
			},
			{
				PFInfo: InterfaceInfo{
					NetNSInfo: NetNSInfo{NSName: "ns2"},
					Name:      "eth1",
					PCIAddr:   "0000:c1:00.0",
					NumaNode:  2,
				},
				Index:   0,
				PCIAddr: "0000:c1:00.1",
				RepName: "eth1_0",
			},
			{
				PFInfo: InterfaceInfo{
					NetNSInfo: NetNSInfo{NSName: "ns2"},
					Name:      "eth1",
					PCIAddr:   "0000:c1:00.0",
					NumaNode:  2,
				},
				Index:   1,
				PCIAddr: "0000:c1:00.2",
				RepName: "eth1_1",
			},
		})
	})
}

func TestGetVFName(t *testing.T) {
	PatchConvey("TestGetVFName", t, func() {
		Mock(os.Stat).Return(nil, nil).Build()
		Mock(os.ReadDir).Return([]os.DirEntry{&mockDirEntry{entryName: "enp65s0v0", isDir: true}}, nil).Build()

		res, err := GetVFName("/sys", "0000:41:00.1")

		So(err, ShouldBeNil)
		So(res, ShouldEqual, "enp65s0v0")
	})
}

func TestGetVfIBDevices(t *testing.T) {
	PatchConvey("TestGetVfIBDevices", t, func() {
		ibVerbs := "uverbs47"
		ibMad := "umad46"

		Mock(os.ReadDir).
			When(func(s string) bool { return strings.Contains(s, netFileNameIBVerbs) }).
			Return([]os.DirEntry{&mockDirEntry{entryName: ibVerbs, isDir: true}}, nil).
			When(func(s string) bool { return strings.Contains(s, netFileNameIBMad) }).
			Return([]os.DirEntry{&mockDirEntry{entryName: ibMad, isDir: true}}, nil).
			Build()

		res, err := GetVfIBDevices("/sys", "eth0")

		So(err, ShouldBeNil)
		So(res, ShouldResemble, []string{
			ibVerbs,
			ibMad,
		})
	})
}
