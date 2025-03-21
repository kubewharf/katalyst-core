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

package qrm

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type NetworkOptions struct {
	PolicyName                                      string
	NetClass                                        NetClassOptions
	ReservedBandwidth                               uint32
	EgressCapacityRate                              float32
	IngressCapacityRate                             float32
	SkipNetworkStateCorruption                      bool
	PodLevelNetClassAnnoKey                         string
	PodLevelNetAttributesAnnoKeys                   string
	IPv4ResourceAllocationAnnotationKey             string
	IPv6ResourceAllocationAnnotationKey             string
	NetNSPathResourceAllocationAnnotationKey        string
	NetInterfaceNameResourceAllocationAnnotationKey string
	NetClassIDResourceAllocationAnnotationKey       string
	NetBandwidthResourceAllocationAnnotationKey     string
	NICHealthCheckers                               []string
	EnableNICAllocationReactor                      bool
}

type NetClassOptions struct {
	// ReclaimedCores is the network class id for reclaimed_cores
	ReclaimedCores uint32
	// SharedCores is the network class id for shared_cores
	SharedCores uint32
	// DedicatedCores is the network class id for dedicated_cores
	DedicatedCores uint32
	// SystemCores is the network class id for system_cores
	SystemCores uint32
}

func NewNetworkOptions() *NetworkOptions {
	return &NetworkOptions{
		PolicyName:                                      "static",
		PodLevelNetClassAnnoKey:                         consts.PodAnnotationNetClassKey,
		ReservedBandwidth:                               0,
		EgressCapacityRate:                              0.94,
		IngressCapacityRate:                             0.9,
		SkipNetworkStateCorruption:                      false,
		PodLevelNetAttributesAnnoKeys:                   "",
		IPv4ResourceAllocationAnnotationKey:             "qrm.katalyst.kubewharf.io/inet_addr",
		IPv6ResourceAllocationAnnotationKey:             "qrm.katalyst.kubewharf.io/inet_addr_ipv6",
		NetNSPathResourceAllocationAnnotationKey:        "qrm.katalyst.kubewharf.io/netns_path",
		NetInterfaceNameResourceAllocationAnnotationKey: "qrm.katalyst.kubewharf.io/nic_name",
		NetClassIDResourceAllocationAnnotationKey:       "qrm.katalyst.kubewharf.io/netcls_id",
		NetBandwidthResourceAllocationAnnotationKey:     "qrm.katalyst.kubewharf.io/net_bandwidth",
		EnableNICAllocationReactor:                      true,
		NICHealthCheckers:                               []string{"*"},
	}
}

func (o *NetworkOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("network_resource_plugin")

	fs.StringVar(&o.PolicyName, "network-resource-plugin-policy",
		o.PolicyName, "The policy network resource plugin should use")
	fs.Uint32Var(&o.NetClass.ReclaimedCores, "network-resource-plugin-class-id-reclaimed-cores",
		o.NetClass.ReclaimedCores, "net class id for reclaimed_cores")
	fs.Uint32Var(&o.NetClass.SharedCores, "network-resource-plugin-class-id-shared-cores",
		o.NetClass.SharedCores, "net class id for shared_cores")
	fs.Uint32Var(&o.NetClass.DedicatedCores, "network-resource-plugin-class-id-dedicated-cores",
		o.NetClass.DedicatedCores, "net class id for dedicated_cores")
	fs.Uint32Var(&o.NetClass.SystemCores, "network-resource-plugin-class-id-system-cores",
		o.NetClass.SystemCores, "net class id for system_cores")
	fs.Uint32Var(&o.ReservedBandwidth, "network-resource-plugin-reserved-bandwidth",
		o.ReservedBandwidth, "reserved bandwidth for business-critical jobs")
	fs.Float32Var(&o.EgressCapacityRate, "network-resource-plugin-egress-capacity-rate",
		o.EgressCapacityRate, "ratio of available egress capacity to egress line speed")
	fs.Float32Var(&o.IngressCapacityRate, "network-resource-plugin-ingress-capacity-rate",
		o.IngressCapacityRate, "ratio of available ingress capacity to ingress line speed")
	fs.BoolVar(&o.SkipNetworkStateCorruption, "skip-network-state-corruption",
		o.SkipNetworkStateCorruption, "if set true, we will skip network state corruption")
	fs.StringVar(&o.PodLevelNetClassAnnoKey, "network-resource-plugin-net-class-annotation-key",
		o.PodLevelNetClassAnnoKey, "The annotation key of pod-level net class")
	fs.StringVar(&o.PodLevelNetAttributesAnnoKeys, "network-resource-plugin-net-attributes-keys",
		o.PodLevelNetAttributesAnnoKeys, "The annotation keys of pod-level network attributes, separated by commas")
	fs.StringVar(&o.IPv4ResourceAllocationAnnotationKey, "network-resource-plugin-ipv4-allocation-anno-key",
		o.IPv4ResourceAllocationAnnotationKey, "The annotation key of allocated ipv4 address for the container, which is ready by runtime")
	fs.StringVar(&o.IPv6ResourceAllocationAnnotationKey, "network-resource-plugin-ipv6-allocation-anno-key",
		o.IPv6ResourceAllocationAnnotationKey, "The annotation key of allocated ipv6 address for the container, which is ready by runtime")
	fs.StringVar(&o.NetNSPathResourceAllocationAnnotationKey, "network-resource-plugin-ns-path-allocation-anno-key",
		o.NetNSPathResourceAllocationAnnotationKey, "The annotation key of allocated ns path for the container, which is ready by runtime")
	fs.StringVar(&o.NetInterfaceNameResourceAllocationAnnotationKey, "network-resource-plugin-nic-name-allocation-anno-key",
		o.NetInterfaceNameResourceAllocationAnnotationKey, "The annotation key of allocated nic name the container, which is ready by runtime")
	fs.StringVar(&o.NetClassIDResourceAllocationAnnotationKey, "network-resource-plugin-class-id-allocation-anno-key",
		o.NetClassIDResourceAllocationAnnotationKey, "The annotation key of allocated netcls id for the container, which is ready by runtime")
	fs.StringVar(&o.NetBandwidthResourceAllocationAnnotationKey, "network-resource-plugin-bandwidth-allocation-anno-key",
		o.NetBandwidthResourceAllocationAnnotationKey, "The annotation key of allocated bandwidth for the container, which is ready by runtime")
	fs.BoolVar(&o.EnableNICAllocationReactor, "enable-network-resource-plugin-nic-allocation-reactor",
		o.EnableNICAllocationReactor, "enable network allocation reactor, default is true")
	fs.StringSliceVar(&o.NICHealthCheckers, "network-resource-plugin-nic-health-checkers",
		o.NICHealthCheckers, "list of nic health checkers, '*' run all on-by-default checkers,"+
			"'ip' run checker 'ip', '-ip' not run checker 'ip'")
}

func (o *NetworkOptions) ApplyTo(conf *qrmconfig.NetworkQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.NetClass.ReclaimedCores = o.NetClass.ReclaimedCores
	conf.NetClass.SharedCores = o.NetClass.SharedCores
	conf.NetClass.DedicatedCores = o.NetClass.DedicatedCores
	conf.NetClass.SystemCores = o.NetClass.SystemCores
	conf.ReservedBandwidth = o.ReservedBandwidth
	conf.EgressCapacityRate = o.EgressCapacityRate
	conf.IngressCapacityRate = o.IngressCapacityRate
	conf.SkipNetworkStateCorruption = o.SkipNetworkStateCorruption
	conf.PodLevelNetClassAnnoKey = o.PodLevelNetClassAnnoKey
	conf.PodLevelNetAttributesAnnoKeys = o.PodLevelNetAttributesAnnoKeys
	conf.IPv4ResourceAllocationAnnotationKey = o.IPv4ResourceAllocationAnnotationKey
	conf.IPv6ResourceAllocationAnnotationKey = o.IPv6ResourceAllocationAnnotationKey
	conf.NetNSPathResourceAllocationAnnotationKey = o.NetNSPathResourceAllocationAnnotationKey
	conf.NetInterfaceNameResourceAllocationAnnotationKey = o.NetInterfaceNameResourceAllocationAnnotationKey
	conf.NetClassIDResourceAllocationAnnotationKey = o.NetClassIDResourceAllocationAnnotationKey
	conf.NetBandwidthResourceAllocationAnnotationKey = o.NetBandwidthResourceAllocationAnnotationKey
	conf.EnableNICAllocationReactor = o.EnableNICAllocationReactor
	conf.NICHealthCheckers = o.NICHealthCheckers

	return nil
}
