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
	"io/ioutil"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	netPathClass  = "/sys/class/net/"
	netPathDevice = "/sys/devices/pci"
)

const (
	netNameSpeed    = "/speed"
	netNameNUMANode = "/device/numa_node"
	netNameEnable   = "/device/enable"
)

type ExtraNetworkInfo struct {
	// Interface info list of all network interface.
	Interface []InterfaceInfo
}

type InterfaceInfo struct {
	// Iface name of this interface.
	Iface string
	// Speed of this interface.
	Speed int
	// NumaNode numa node of this interface belongs to.
	NumaNode int
	// Enable whether enable this interface.
	Enable bool
	// Addr address of this interface, which includes ipv4 and ipv6.
	Addr *IfaceAddr
}

type IfaceAddr struct {
	IPV4 []*net.IP
	IPV6 []*net.IP
}

// GetExtraNetworkInfo get network info from /sys/class/net and system function net.Interfaces.
func GetExtraNetworkInfo() (*ExtraNetworkInfo, error) {
	networkInfo := &ExtraNetworkInfo{}

	// read all device name from networkInfoPath, where name of each directory is a device
	dirs, err := ioutil.ReadDir(netPathClass)
	if err != nil {
		return nil, err
	}

	// get all interface addr, include both ipv4 and ipv6
	interfaceAddrMap, err := getInterfaceAddr()
	if err != nil {
		return nil, err
	}

	// combine all network interface info
	for _, dir := range dirs {
		interfaceName := dir.Name()
		devPath := netPathClass + interfaceName

		realpath, err := filepath.EvalSymlinks(devPath)
		if err == nil {
			// Only PCI Network Card
			if strings.HasPrefix(realpath, netPathDevice) {
				nw := InterfaceInfo{
					Iface:    interfaceName,
					Speed:    simpleReadInt(netPathClass + interfaceName + netNameSpeed),
					NumaNode: simpleReadInt(netPathClass + interfaceName + netNameNUMANode),
				}

				// check this interface whether is enabled
				if simpleReadInt(netPathClass+interfaceName+netNameEnable) == 1 {
					nw.Enable = true
				} else {
					nw.Enable = false
				}

				if interfaceAddr, exist := interfaceAddrMap[interfaceName]; exist {
					nw.Addr = interfaceAddr
				}

				networkInfo.Interface = append(networkInfo.Interface, nw)
			}
		}
	}

	return networkInfo, nil
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

// simpleReadInt read content of file which has only one int value.
func simpleReadInt(file string) int {
	body, err := ioutil.ReadFile(filepath.Clean(file))
	if err != nil {
		return -1
	}

	i, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return -2
	}

	return i
}
