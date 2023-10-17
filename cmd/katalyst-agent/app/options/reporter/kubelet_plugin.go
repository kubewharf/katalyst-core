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

package reporter

import (
	cliflag "k8s.io/component-base/cli/flag"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
)

type KubeletPluginOptions struct {
	PodResourcesServerEndpoints []string
	KubeletResourcePluginPaths  []string
	EnableReportTopologyPolicy  bool
	EnableReportRDMATopology    bool
}

func NewKubeletPluginOptions() *KubeletPluginOptions {
	return &KubeletPluginOptions{
		PodResourcesServerEndpoints: []string{
			pluginapi.KubeletSocket,
		},
		KubeletResourcePluginPaths: []string{
			pluginapi.ResourcePluginPath,
		},
		EnableReportTopologyPolicy: false,
		EnableReportRDMATopology:   false,
	}
}

func (o *KubeletPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("reporter-kubelet")

	fs.StringSliceVar(&o.PodResourcesServerEndpoints, "pod-resources-server-endpoint", o.PodResourcesServerEndpoints,
		"the endpoint of pod resource api server")
	fs.StringSliceVar(&o.KubeletResourcePluginPaths, "kubelet-resource-plugin-path", o.KubeletResourcePluginPaths,
		"the path of kubelet resource plugin")
	fs.BoolVar(&o.EnableReportTopologyPolicy, "enable-report-topology-policy", o.EnableReportTopologyPolicy,
		"whether to report topology policy")
	fs.BoolVar(&o.EnableReportRDMATopology, "enable-report-rdma-topology", false, "enable report rdma topology, default false")

}

func (o *KubeletPluginOptions) ApplyTo(c *reporter.KubeletPluginConfiguration) error {
	c.PodResourcesServerEndpoints = o.PodResourcesServerEndpoints
	c.KubeletResourcePluginPaths = o.KubeletResourcePluginPaths
	c.EnableReportTopologyPolicy = o.EnableReportTopologyPolicy
	c.EnableReportRDMATopology = o.EnableReportRDMATopology

	return nil
}
