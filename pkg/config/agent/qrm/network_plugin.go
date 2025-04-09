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

// NetworkQRMPluginConfig is the config of network QRM plugin
type NetworkQRMPluginConfig struct {
	// PolicyName is used to switch between several strategies
	PolicyName string
	NetClass   NetClassConfig
	// Reserved network bandwidth in unit of Mbps for business-critical jobs (e.g. online services).
	// In phase 1, we only support the reservation for business-critical jobs. The system component reservation might be added later.
	// Also, we do not differentiate the egress and ingress reservation for now. That is, the reserved bandwidth on egress and ingress is supposed to be same
	ReservedBandwidth uint32
	// The ratio of available capacity to NIC line speed. For example, a 25Gbps NIC's max bandwidth is around 23.5Gbps.
	// Please note, the ingress rate throttling may need additional virtual device like ifb, which results in lower capacity than egress
	EgressCapacityRate  float32
	IngressCapacityRate float32
	// skip network state corruption and it will be used after updating state properties
	SkipNetworkStateCorruption                      bool
	PodLevelNetClassAnnoKey                         string
	PodLevelNetAttributesAnnoKeys                   string
	IPv4ResourceAllocationAnnotationKey             string
	IPv6ResourceAllocationAnnotationKey             string
	NetNSPathResourceAllocationAnnotationKey        string
	NetInterfaceNameResourceAllocationAnnotationKey string
	NetClassIDResourceAllocationAnnotationKey       string
	NetBandwidthResourceAllocationAnnotationKey     string

	// EnableNICAllocationReactor: enable the nic allocation reactor for pods already have nic allocated by runtime
	EnableNICAllocationReactor bool
	// NICHealthCheckers is the list of enabled NIC health checkers
	NICHealthCheckers []string
}

type NetClassConfig struct {
	// ReclaimedCores is the network class id for reclaimed_cores
	ReclaimedCores uint32
	// SharedCores is the network class id for shared_cores
	SharedCores uint32
	// DedicatedCores is the network class id for dedicated_cores
	DedicatedCores uint32
	// SystemCores is the network class id for system_cores
	SystemCores uint32
}

// NewNetworkQRMPluginConfig returns a NetworkQRMPluginConfig
func NewNetworkQRMPluginConfig() *NetworkQRMPluginConfig {
	return &NetworkQRMPluginConfig{}
}
