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
	"net"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	DefaultNICNamespace = ""

	IPVersionV4 = 4
	IPVersionV6 = 6
)

type ExtraNetworkInfo struct {
	// Interface info list of all network interface.
	Interface []InterfaceInfo
}

func (e ExtraNetworkInfo) GetAllocatableNICs(conf *global.MachineInfoConfiguration) []InterfaceInfo {
	// it is incorrect to reserve bandwidth on those disabled NICs.
	// we only count active NICs as available network devices and allocate bandwidth on them
	filteredNICs := make([]InterfaceInfo, 0, len(e.Interface))
	for _, nic := range e.Interface {
		if nic.NSName != DefaultNICNamespace && !general.IsNameEnabled(nic.NSName, nil, conf.NetAllocatableNS) {
			general.Infof("skip allocatable nic: %s with namespace: %s", nic.Iface, nic.NSName)
			continue
		}

		if !nic.Enable {
			general.Warningf("nic: %s isn't enabled", nic.Iface)
			continue
		} else if nic.Addr == nil || (len(nic.Addr.IPV4) == 0 && len(nic.Addr.IPV6) == 0) {
			general.Warningf("nic: %s doesn't have IP address", nic.Iface)
			continue
		}

		filteredNICs = append(filteredNICs, nic)
	}

	if len(filteredNICs) == 0 {
		general.InfoS("nic list returned by filterNICsByAvailability is empty")
	}

	return filteredNICs
}

type InterfaceInfo struct {
	// Iface name of this interface.
	Iface string
	// IfIndex is an index of network interface.
	IfIndex int
	// Speed of this interface.
	Speed int
	// NumaNode numa node of this interface belongs to.
	NumaNode int
	// Enable whether enable this interface.
	Enable bool
	// Addr address of this interface, which includes ipv4 and ipv6.
	Addr *IfaceAddr

	// NSName indicates the namespace for this interface
	NSName string
	// NSAbsolutePath indicates the namespace path for this interface
	NSAbsolutePath string
}

type IfaceAddr struct {
	IPV4 []*net.IP
	IPV6 []*net.IP
}

func (addr *IfaceAddr) GetNICIPs(ipVersion int) []string {
	if addr == nil {
		return nil
	}

	res := sets.NewString()

	var targetIPs []net.IP
	switch ipVersion {
	case IPVersionV4:
		for _, ip := range addr.IPV4 {
			if ip != nil {
				targetIPs = append(targetIPs, *ip)
			}
		}
	case IPVersionV6:
		for _, ip := range addr.IPV6 {
			if ip != nil {
				targetIPs = append(targetIPs, *ip)
			}
		}
	}

	for _, ip := range targetIPs {
		res.Insert(ip.String())
	}

	return res.List()
}

func GetInterfaceAddr(iface net.Interface) (*IfaceAddr, error) {
	// if the interface is down or a loopback interface, we just skip it
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback > 0 {
		return nil, fmt.Errorf("if the interface is down or a loopback interface")
	}

	address, err := iface.Addrs()
	if err != nil {
		return nil, err
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
		return ia, nil
	}
	return nil, fmt.Errorf("interface %v has no IP addresses", iface.Name)
}
