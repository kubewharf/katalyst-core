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
	PolicyName                    string
	NetClass                      NetClassOptions
	PodLevelNetClassAnnoKey       string
	PodLevelNetAttributesAnnoKeys string
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
		PolicyName:                    "dynamic",
		PodLevelNetClassAnnoKey:       consts.PodAnnotationNetClassKey,
		PodLevelNetAttributesAnnoKeys: "",
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
	fs.StringVar(&o.PodLevelNetClassAnnoKey, "network-resource-plugin-net-class-annotation-key",
		o.PodLevelNetClassAnnoKey, "The annotation key of pod-level net class")
	fs.StringVar(&o.PodLevelNetAttributesAnnoKeys, "network-resource-plugin-net-attributes-keys",
		o.PodLevelNetAttributesAnnoKeys, "The annotation keys of pod-level network attributes, separated by commas")
}

func (o *NetworkOptions) ApplyTo(conf *qrmconfig.NetworkQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.NetClass.ReclaimedCores = o.NetClass.ReclaimedCores
	conf.NetClass.SharedCores = o.NetClass.SharedCores
	conf.NetClass.DedicatedCores = o.NetClass.DedicatedCores
	conf.NetClass.SystemCores = o.NetClass.SystemCores
	conf.PodLevelNetClassAnnoKey = o.PodLevelNetClassAnnoKey
	conf.PodLevelNetAttributesAnnoKeys = o.PodLevelNetAttributesAnnoKeys

	return nil
}
