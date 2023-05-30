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
	"net"

	"k8s.io/apimachinery/pkg/util/sets"
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

	// NSName indicates the namespace for this interface
	NSName string
	// NSAbsolutePath indicates the namespace path for this interface
	NSAbsolutePath string
}

type IfaceAddr struct {
	IPV4 []*net.IP
	IPV6 []*net.IP
}

func (nic *InterfaceInfo) GetNICIPs(ipVersion int) []string {
	res := sets.NewString()

	var targetIPs []net.IP
	switch ipVersion {
	case IPVersionV4:
		for _, ip := range nic.Addr.IPV4 {
			if ip != nil {
				targetIPs = append(targetIPs, *ip)
			}
		}
	case IPVersionV6:
		for _, ip := range nic.Addr.IPV6 {
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
