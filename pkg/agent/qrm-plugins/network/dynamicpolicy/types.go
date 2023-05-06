package dynamicpolicy

import "net"

type NetworkInterfaceAddr struct {
	IPV4 []net.IP
	IPV6 []net.IP
}

type NetworkInterface struct {
	Name               string
	AffinitiveNUMANode int
	Enabled            bool
	Addr               NetworkInterfaceAddr
	NSAbsolutePath     string
	NSName             string
}
