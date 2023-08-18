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
	"strings"
	"syscall"

	"github.com/vishvananda/netns"

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
)

const (
	netFileNameSpeed    = "speed"
	netFileNameNUMANode = "device/numa_node"
	netFileNameEnable   = "device/enable"
	netEnable           = 1
)

// GetExtraNetworkInfo get network info from /sys/class/net and system function net.Interfaces.
// if multiple network namespace is enabled, we should exec into all namespaces and parse nics for them.
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

// getNSNetworkHardwareTopology set given network namespaces and get nics inside if needed
func getNSNetworkHardwareTopology(nsName, netNSDirAbsPath string) ([]InterfaceInfo, error) {
	var nics []InterfaceInfo

	nsAbsPath := ""
	sysFsDir := sysFSDirNormal

	if nsName != DefaultNICNamespace {
		nsAbsPath = path.Join(netNSDirAbsPath, nsName)
		sysFsDir = sysFSDirNetNSTmp

		// save the current network namespace
		originNS, _ := netns.Get()
		defer func() {
			// switch back to the original namespace
			if err := netns.Set(originNS); err != nil {
				general.Fatalf("failed to unmount sys fs: %v", err)
			}
			_ = originNS.Close()
		}()

		// exec into the new network namespace
		newNS, err := netns.GetFromPath(nsAbsPath)
		if err != nil {
			return nil, fmt.Errorf("get handle from net ns path: %s failed with error: %v", nsAbsPath, err)
		}
		defer func() { _ = newNS.Close() }()

		if err = netns.Set(newNS); err != nil {
			return nil, fmt.Errorf("set newNS: %s failed with error: %v", nsAbsPath, err)
		}

		// create the target directory if it doesn't exist
		if _, err := os.Stat(sysFSDirNetNSTmp); err != nil {
			if os.IsNotExist(err) {
				if err := os.MkdirAll(sysFSDirNetNSTmp, os.FileMode(0755)); err != nil {
					return nil, fmt.Errorf("make dir: %s failed with error: %v", sysFSDirNetNSTmp, err)
				}
			} else {
				return nil, fmt.Errorf("check dir: %s failed with error: %v", sysFSDirNetNSTmp, err)
			}
		}

		if err := syscall.Mount("sysfs", sysFSDirNetNSTmp, "sysfs", 0, ""); err != nil {
			return nil, fmt.Errorf("mount sysfs to %s failed with error: %v", sysFSDirNetNSTmp, err)
		}

		// the sysfs needs to be remounted before switching network namespace back
		defer func() {
			if err := syscall.Unmount(sysFSDirNetNSTmp, 0); err != nil {
				general.Fatalf("unmount sysfs: %s failed with error: %v", sysFSDirNetNSTmp, err)
			}
		}()
	}

	nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
	nicDirs, err := ioutil.ReadDir(nicsBaseDirPath)
	if err != nil {
		return nil, err
	}

	nicsAddrMap, err := getInterfaceAddr()
	if err != nil {
		return nil, err
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
		if !strings.Contains(devPath, nicPathNameDeviceFormatPCI) {
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
		getInterfaceAttr(&nic, nicPath)

		general.Infof("discover nic: %#v", nic)
		nics = append(nics, nic)
	}

	return nics, nil
}

// getInterfaceAttr parses key information from system files
func getInterfaceAttr(info *InterfaceInfo, nicPath string) {
	if nicNUMANode, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameNUMANode)); err != nil {
		general.Errorf("ns %v name %v, read NUMA node failed with error: %v. Suppose it's associated with NUMA node 0", info.NSName, info.Iface, err)
		info.NumaNode = 0
	} else {
		info.NumaNode = nicNUMANode
	}

	if nicEnabledStatus, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameEnable)); err != nil {
		general.Errorf("ns %v name %v, read enable status failed with error: %v", info.NSName, info.Iface, err)
		info.Enable = false
	} else {
		info.Enable = nicEnabledStatus == netEnable
	}

	if nicSpeed, err := general.ReadFileIntoInt(path.Join(nicPath, netFileNameSpeed)); err != nil {
		general.Errorf("ns %v name %v, read speed failed with error: %v", info.NSName, info.Iface, err)
		info.Speed = -1
	} else {
		info.Speed = nicSpeed
	}
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
		// if the interface is down or a loopback interface, we just skip it
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback > 0 {
			continue
		}

		address, err := i.Addrs()
		if err != nil {
			continue
		}

		if len(address) > 0 {
			ia := &IfaceAddr{}

			for _, addr := range address {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				default:
					continue
				}

				// filter out ips that are not global uni-cast
				if !ip.IsGlobalUnicast() {
					continue
				}

				if ip.To4() != nil {
					ia.IPV4 = append(ia.IPV4, &ip)
				} else {
					ia.IPV6 = append(ia.IPV6, &ip)
				}
			}

			ias[i.Name] = ia
		}
	}

	return ias, nil
}
