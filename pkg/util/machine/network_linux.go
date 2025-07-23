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
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/moby/sys/mountinfo"
	"github.com/vishvananda/netns"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	sysFSDirNormal   = "/sys"
	sysFSDirNetNSTmp = "/tmp/net_ns_sysfs"
)

const (
	nicPathNameDeviceFormatPCI = "devices/pci"
	nicPathNAMEBaseDir         = "class/net"
	bondingMasterPath          = "bonding_masters"
)

const (
	netFileNameSpeed    = "speed"
	netFileNameNUMANode = "device/numa_node"
	netFileNameEnable   = "device/enable"
	nicFileNameIfindex  = "ifindex"
	netOperstate        = "operstate"
	netUP               = "up"
	netEnable           = 1
)

const UnknownNumaNode = -1

// GetExtraNetworkInfo collects network interface information from /sys/class/net and net.Interfaces.
// When multiple network namespaces are enabled, it executes in each namespace to gather NIC information.
// Returns ExtraNetworkInfo containing all discovered network interfaces and their attributes.
func GetExtraNetworkInfo(conf *global.MachineInfoConfiguration) (*ExtraNetworkInfo, error) {
	networkInfo := &ExtraNetworkInfo{}

	nsList := []string{DefaultNICNamespace}
	if conf.NetMultipleNS {
		if conf.NetNSDirAbsPath == "" {
			return nil, fmt.Errorf("GetNetworkInterfaces got nil netNSDirAbsPath")
		}

		if dirs, err := ioutil.ReadDir(conf.NetNSDirAbsPath); err != nil {
			return nil, err
		} else {
			for _, dir := range dirs {
				if !dir.IsDir() {
					nsList = append(nsList, dir.Name())
				}
			}
		}

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	general.Infof("namespace list: %v", nsList)

	for _, ns := range nsList {
		nicsInNs, err := getNSNetworkHardwareTopology(ns, conf.NetNSDirAbsPath)
		if err != nil {
			// if multiple ns is disabled, we should block agent start
			// todo: to discuss if block agent start when any ns is parsed failed
			if !conf.NetMultipleNS {
				return nil, fmt.Errorf("failed to get network topology: %v", err)
			}

			general.Errorf("get network topology for ns %s failed: %v", ns, err)
			continue
		}

		networkInfo.Interface = append(networkInfo.Interface, nicsInNs...)
	}
	return networkInfo, nil
}

// DoNetNS executes a callback function within the specified network namespace.
// For the default namespace, runs callback in current namespace without modification.
// For other namespaces:
// 1. Locks OS thread to maintain namespace context
// 2. Creates temporary sysfs mount to isolate from host
// 3. Executes callback with isolated sysfs path
// 4. Cleans up mount and restores original namespace
func DoNetNS(nsName, netNSDirAbsPath string, cb func(sysFsDir, nsAbsPath string) error) error {
	// if nsName is defaulted, the callback function will be run in the current network namespace.
	// So skip the whole function, just call cb().
	// cb() needs a sysFsDir as arg but ignored, give it a fake one.
	var nsAbsPath string
	sysFsDir := sysFSDirNormal
	if nsName == DefaultNICNamespace {
		return cb(sysFsDir, nsAbsPath)
	}
	nsAbsPath = path.Join(netNSDirAbsPath, nsName)

	// if nsName is not defaulted, we should exec into the new network namespace.
	// So we need to mount sysfs to /tmp/net_ns_sysfs to avoid contaminating the host sysfs directory.
	// create the target directory if it doesn't exist
	sysFsDir = sysFSDirNetNSTmp

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// save the current network namespace
	originNS, err := netns.Get()
	if err != nil {
		return err
	}
	defer func() {
		// switch back to the original namespace
		if err := netns.Set(originNS); err != nil {
			general.Fatalf("failed set originNS: %v", err)
		}
		_ = originNS.Close()
	}()

	// exec into the new network namespace
	newNS, err := netns.GetFromPath(nsAbsPath)
	if err != nil {
		return fmt.Errorf("get handle from net ns path: %s failed with error: %v", nsAbsPath, err)
	}
	defer func() { _ = newNS.Close() }()

	if err = netns.Set(newNS); err != nil {
		return fmt.Errorf("set newNS: %s failed with error: %v", nsAbsPath, err)
	}

	// mount sysfs to /tmp/net_ns_sysfs to avoid contaminating the host sysfs directory
	// create the target directory if it doesn't exist
	if _, err := os.Stat(sysFsDir); err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(sysFsDir, os.FileMode(0o755)); err != nil {
				return fmt.Errorf("make dir: %s failed with error: %v", sysFsDir, err)
			}
		} else {
			return fmt.Errorf("check dir: %s failed with error: %v", sysFsDir, err)
		}
	}

	if mounted, err := mountinfo.Mounted(sysFsDir); err != nil {
		return fmt.Errorf("check mounted dir: %s failed with error: %v", sysFsDir, err)
	} else if !mounted {
		if err := syscall.Mount("sysfs", sysFsDir, "sysfs", 0, ""); err != nil {
			return fmt.Errorf("mount sysfs to %s failed with error: %v", sysFsDir, err)
		}
	}

	// the sysfs needs to be remounted before switching network namespace back
	defer func() {
		if err := syscall.Unmount(sysFsDir, 0); err != nil {
			general.Fatalf("unmount sysfs: %s failed with error: %v", sysFsDir, err)
		}
	}()

	return cb(sysFsDir, nsAbsPath)
}

// getNSNetworkHardwareTopology discovers network interfaces and their hardware attributes
// within a specified network namespace. Handles both regular and bonded interfaces.
// Returns slice of InterfaceInfo containing NIC details like speed, NUMA node, etc.
func getNSNetworkHardwareTopology(nsName, netNSDirAbsPath string) ([]InterfaceInfo, error) {
	var nics []InterfaceInfo

	err := DoNetNS(nsName, netNSDirAbsPath, func(sysFsDir, nsAbsPath string) error {
		nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
		nicDirs, err := os.ReadDir(nicsBaseDirPath)
		if err != nil {
			return err
		}

		bondingNICs := getBondingNetworkInterfaces(path.Join(nicsBaseDirPath, bondingMasterPath))

		nicsAddrMap, err := getInterfaceAddr()
		if err != nil {
			return err
		}

		for _, nicDir := range nicDirs {
			nicName := nicDir.Name()
			nicPath := path.Join(nicsBaseDirPath, nicName)

			devPath, err := filepath.EvalSymlinks(nicPath)
			if err != nil {
				general.Warningf("eval sym link: %s failed with error: %v", nicPath, err)
				continue
			}

			// only return PCI NIC
			if !strings.Contains(devPath, nicPathNameDeviceFormatPCI) && !bondingNICs.Has(nicName) {
				general.Warningf("skip nic: %s with devPath: %s which isn't pci device", nicName, devPath)
				continue
			}

			nic := InterfaceInfo{
				Iface:          nicName,
				NSName:         nsName,
				NSAbsolutePath: nsAbsPath,
			}
			if nicAddr, exist := nicsAddrMap[nicName]; exist {
				nic.Addr = nicAddr
			}

			getInterfaceIndex(nicsBaseDirPath, &nic)
			getInterfaceAttr(&nic, nicPath)

			general.Infof("discover nic: %#v", nic)
			nics = append(nics, nic)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return nics, nil
}

func getInterfaceIndex(nicsBaseDirPath string, info *InterfaceInfo) {
	if info == nil {
		return
	}

	nicName := info.Iface
	iface, err := net.InterfaceByName(nicName)
	if err != nil {
		general.Warningf("failed to InterfaceByName(%s), err %v", nicName, err)
		ifIndex, err := getIfIndexFromSys(nicsBaseDirPath, nicName)
		if err != nil {
			general.Errorf("failed to get %s interface index from sys: %v", nicName, err)
			return
		}
		info.IfIndex = ifIndex
		return
	}
	info.IfIndex = iface.Index
}

// getInterfaceAttr parses key information from system files
func getInterfaceAttr(info *InterfaceInfo, nicPath string) {
	if nicNUMANode, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameNUMANode)); err != nil {
		general.Errorf("ns %v name %v, read NUMA node failed with error: %v. Suppose it's associated with NUMA node 0", info.NSName, info.Iface, err)
		// some net device files are missed on VMs (e.g. "device/numanode")
		info.NumaNode = -1
	} else {
		info.NumaNode = nicNUMANode
		if info.NumaNode == -1 {
			// the "device/numanode" file is filled with -1 on some VMs (e.g. byte-vm), we should return 0 instead
			general.Errorf("Invalid NUMA node %v for interface %v. Suppose it's associated with NUMA node 0", info.NumaNode, info.Iface)
		}
	}

	if general.IsPathExists(path.Join(nicPath, netFileNameEnable)) {
		if nicEnabledStatus, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameEnable)); err != nil {
			general.Errorf("ns %v name %v, read enable status failed with error: %v", info.NSName, info.Iface, err)
			info.Enable = false
		} else {
			info.Enable = nicEnabledStatus == netEnable
		}
	} else {
		// some VMs do not have "device/enable" file under nicPath, we can read "operstate" for nic status instead
		if nicUPStatus, err := general.ReadFileIntoLines(path.Join(nicPath, netOperstate)); err != nil || len(nicUPStatus) == 0 {
			general.Errorf("ns %v name %v, read operstate failed with error: %v", info.NSName, info.Iface, err)
			info.Enable = false
		} else {
			info.Enable = nicUPStatus[0] == netUP
		}
	}

	if nicSpeed, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameSpeed)); err != nil {
		general.Errorf("ns %v name %v, read speed failed with error: %v", info.NSName, info.Iface, err)
		info.Speed = -1
	} else {
		info.Speed = nicSpeed
	}
}

func getIfIndexFromSys(nicsBaseDirPath, nicName string) (int, error) {
	ifIndexPath := path.Join(nicsBaseDirPath, nicName, nicFileNameIfindex)
	b, err := os.ReadFile(ifIndexPath)
	if err != nil {
		return 0, err
	}

	ifindex, err := strconv.Atoi(strings.TrimSpace(strings.TrimRight(string(b), "\n")))
	if err != nil {
		return 0, err
	}

	return ifindex, nil
}

// getInterfaceAddr get interface address which is map of interface name to
// its interface address which includes both ipv6 and ipv4 address.
func getInterfaceAddr() (map[string]*IfaceAddr, error) {
	var err error

	ias := make(map[string]*IfaceAddr)

	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, i := range interfaces {
		addr, err := GetInterfaceAddr(i)
		if err != nil {
			general.Warningf("failed to get interface addr from %v, err %v", i, err)
			continue
		}

		if addr != nil {
			ias[i.Name] = addr
		}
	}

	return ias, nil
}

func getBondingNetworkInterfaces(bondingMasterPath string) sets.String {
	bondingNICs := sets.String{}
	lines, err := general.ReadFileIntoLines(bondingMasterPath)
	if err != nil {
		return bondingNICs
	}
	for _, line := range lines {
		nics := strings.Split(line, " ")
		for _, nic := range nics {
			bondingNICs.Insert(nic)
		}
	}
	return bondingNICs
}
