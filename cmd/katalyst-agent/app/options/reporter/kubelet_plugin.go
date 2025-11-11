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
	v1 "k8s.io/api/core/v1"
	cliflag "k8s.io/component-base/cli/flag"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type KubeletPluginOptions struct {
	PodResourcesServerEndpoints    []string
	KubeletResourcePluginPaths     []string
	KubeletResourcePluginStateFile string
	EnableReportTopologyPolicy     bool
	ResourceNameToZoneTypeMap      map[string]string
	NeedValidationResources        []string
	NeedAggregateReportingDevices  []string
	EnablePodResourcesFilter       bool
	EnableReportCPUFlags           bool
	EnableReportL3CacheGroup       bool
	EnableReportThreadTopology     bool
}

func NewKubeletPluginOptions() *KubeletPluginOptions {
	return &KubeletPluginOptions{
		PodResourcesServerEndpoints: []string{
			pluginapi.KubeletSocket,
		},
		KubeletResourcePluginPaths: []string{
			pluginapi.ResourcePluginPath,
		},
		KubeletResourcePluginStateFile: consts.KubeletQoSResourceManagerCheckpoint,
		EnableReportTopologyPolicy:     false,
		ResourceNameToZoneTypeMap:      make(map[string]string),
		NeedValidationResources: []string{
			string(v1.ResourceCPU),
			string(v1.ResourceMemory),
		},
		NeedAggregateReportingDevices: []string{},
		EnablePodResourcesFilter:      true,
	}
}

func (o *KubeletPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("reporter-kubelet")

	fs.StringSliceVar(&o.PodResourcesServerEndpoints, "pod-resources-server-endpoint", o.PodResourcesServerEndpoints,
		"the endpoint of pod resource api server")
	fs.StringSliceVar(&o.KubeletResourcePluginPaths, "kubelet-resource-plugin-path", o.KubeletResourcePluginPaths,
		"the path of kubelet resource plugin")
	fs.StringVar(&o.KubeletResourcePluginStateFile, "kubelet-resource-plugin-state-file", o.KubeletResourcePluginStateFile,
		"the state file of kubelet resource plugin")
	fs.BoolVar(&o.EnableReportTopologyPolicy, "enable-report-topology-policy", o.EnableReportTopologyPolicy,
		"whether to report topology policy")
	fs.StringToStringVar(&o.ResourceNameToZoneTypeMap, "resource-name-to-zone-type-map", o.ResourceNameToZoneTypeMap,
		"a map that stores the mapping relationship between resource names to zone types in KCNR (e.g. nvidia.com/gpu=GPU,...)")
	fs.StringSliceVar(&o.NeedValidationResources, "need-validation-resources", o.NeedValidationResources,
		"resources need to be validated")
	fs.StringSliceVar(&o.NeedAggregateReportingDevices, "need-aggregate-reporting-devices", o.NeedAggregateReportingDevices,
		"devices need to be aggregated reporting")
	fs.BoolVar(&o.EnablePodResourcesFilter, "enable-pod-resources-filter", o.EnablePodResourcesFilter,
		"whether to filter pod resources response")
	fs.BoolVar(&o.EnableReportCPUFlags, "enable-report-cpu-flags", o.EnableReportCPUFlags,
		"whether to report cpu flags")
	fs.BoolVar(&o.EnableReportL3CacheGroup, "enable-report-l3-cache-group", o.EnableReportL3CacheGroup,
		"whether to report l3 cache group")
	fs.BoolVar(&o.EnableReportThreadTopology, "enable-report-thread-topology", o.EnableReportThreadTopology,
		"whether to report thread topology")
}

func (o *KubeletPluginOptions) ApplyTo(c *reporter.KubeletPluginConfiguration) error {
	c.PodResourcesServerEndpoints = o.PodResourcesServerEndpoints
	c.KubeletResourcePluginPaths = o.KubeletResourcePluginPaths
	c.KubeletResourcePluginStateFile = o.KubeletResourcePluginStateFile
	c.EnableReportTopologyPolicy = o.EnableReportTopologyPolicy
	c.ResourceNameToZoneTypeMap = o.ResourceNameToZoneTypeMap
	c.NeedValidationResources = o.NeedValidationResources
	c.NeedAggregateReportingDevices = o.NeedAggregateReportingDevices
	c.EnablePodResourcesFilter = o.EnablePodResourcesFilter
	c.EnableReportCPUFlags = o.EnableReportCPUFlags
	c.EnableReportL3CacheGroup = o.EnableReportL3CacheGroup
	c.EnableReportThreadTopology = o.EnableReportThreadTopology

	return nil
}
