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

package orm

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	ormconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/orm"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

const (
	defaultORMPluginRegistrationDir = "/var/lib/katalyst/resource-plugins"
)

type GenericORMPluginOptions struct {
	ORMPluginRegistrationDir        string
	ORMWorkMode                     consts.WorkMode
	ORMReconcilePeriod              time.Duration
	ORMResourceNamesMap             map[string]string
	ORMPodNotifyChanLen             int
	TopologyPolicyName              string
	NumericAlignResources           []string
	ORMPodResourcesSocket           string
	ORMDevicesProvider              string
	ORMKubeletPodResourcesEndpoints []string
	ORMNRISocketPath                string
	ORMNRIPluginName                string
	ORMNRIPluginIndex               string
	ORMNRIHandleEvents              string
}

func NewGenericORMPluginOptions() *GenericORMPluginOptions {
	return &GenericORMPluginOptions{
		ORMPluginRegistrationDir:        defaultORMPluginRegistrationDir,
		ORMWorkMode:                     consts.WorkModeBypass,
		ORMReconcilePeriod:              time.Second * 5,
		ORMResourceNamesMap:             map[string]string{},
		ORMPodNotifyChanLen:             10,
		TopologyPolicyName:              "",
		NumericAlignResources:           []string{"cpu", "memory"},
		ORMPodResourcesSocket:           "unix:/var/lib/katalyst/pod-resources/kubelet.sock",
		ORMDevicesProvider:              "",
		ORMKubeletPodResourcesEndpoints: []string{"/var/lib/kubelet/pod-resources/kubelet.sock"},
		ORMNRISocketPath:                "/var/run/nri/nri.sock",
		ORMNRIPluginName:                "orm",
		ORMNRIPluginIndex:               "00",
		ORMNRIHandleEvents:              "RunPodSandbox,CreateContainer,UpdateContainer,RemovePodSandbox",
	}
}

func (o *GenericORMPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("orm")

	fs.StringVar(&o.ORMPluginRegistrationDir, "orm-plugin-registration-dir", o.ORMPluginRegistrationDir,
		"The path where the orm plugin manager finds the sockets of resource plugins")
	fs.StringVar((*string)(&o.ORMWorkMode), "orm-work-mode", string(o.ORMWorkMode), "orm work mode, nri or bypass")
	fs.DurationVar(&o.ORMReconcilePeriod, "orm-reconcile-period",
		o.ORMReconcilePeriod, "orm resource reconcile period")
	fs.StringToStringVar(&o.ORMResourceNamesMap, "orm-resource-names-map", o.ORMResourceNamesMap,
		"A set of ResourceName=ResourceQuantity pairs that map resource name during out-of-band Resource Manager allocation period. "+
			"e.g. 'resource.katalyst.kubewharf.io/reclaimed_millicpu=cpu,resource.katalyst.kubewharf.io/reclaimed_memory=memory' "+
			"should be set for that reclaimed_cores pods with resources [resource.katalyst.kubewharf.io/reclaimed_millicpu] and [resource.katalyst.kubewharf.io/reclaimed_memory]"+
			"will also be allocated by [cpu] and [memory] QRM plugins")
	fs.IntVar(&o.ORMPodNotifyChanLen, "orm-pod-notify-chan-len",
		o.ORMPodNotifyChanLen, "length of pod addition and movement notifying channel")
	fs.StringVar(&o.TopologyPolicyName, "topology-policy-name",
		o.TopologyPolicyName, "topology merge policy name used by ORM")
	fs.StringSliceVar(&o.NumericAlignResources, "numeric-align-resources", o.NumericAlignResources,
		"resources which should be aligned in numeric topology policy")
	fs.StringVar(&o.ORMPodResourcesSocket, "orm-pod-resources-socket", o.ORMPodResourcesSocket,
		"socket of ORM pod resource api, default 'unix:/var/lib/katalyst/pod-resources/kubelet.sock'")
	fs.StringVar(&o.ORMDevicesProvider, "orm-devices-provider", o.ORMDevicesProvider,
		"devices provider provides devices resources and allocatable for ORM podResources api")
	fs.StringSliceVar(&o.ORMKubeletPodResourcesEndpoints, "orm-kubelet-pod-resources-endpoints", o.ORMKubeletPodResourcesEndpoints,
		"kubelet podResources endpoints for ORM kubelet devices provider")
	fs.StringVar(&o.ORMNRIPluginName, "orm-nri-plugin-name", o.ORMNRIPluginName, "orm nri plugin name")
	fs.StringVar(&o.ORMNRIPluginIndex, "orm-nri-plugin-index", o.ORMNRIPluginIndex, "orm nri plugin index")
	fs.StringVar(&o.ORMNRIHandleEvents, "orm-nri-handle-events", o.ORMNRIHandleEvents, "orm nri handle events")
}

func (o *GenericORMPluginOptions) ApplyTo(conf *ormconfig.GenericORMConfiguration) error {
	conf.ORMPluginRegistrationDir = o.ORMPluginRegistrationDir
	conf.ORMWorkMode = o.ORMWorkMode
	conf.ORMReconcilePeriod = o.ORMReconcilePeriod
	conf.ORMResourceNamesMap = o.ORMResourceNamesMap
	conf.ORMPodNotifyChanLen = o.ORMPodNotifyChanLen
	conf.TopologyPolicyName = o.TopologyPolicyName
	conf.NumericAlignResources = o.NumericAlignResources
	conf.ORMPodResourcesSocket = o.ORMPodResourcesSocket
	conf.ORMDevicesProvider = o.ORMDevicesProvider
	conf.ORMKubeletPodResourcesEndpoints = o.ORMKubeletPodResourcesEndpoints
	conf.ORMNRISocketPath = o.ORMNRISocketPath
	conf.ORMNRIPluginName = o.ORMNRIPluginName
	conf.ORMNRIPluginIndex = o.ORMNRIPluginIndex
	conf.ORMNRIHandleEvents = o.ORMNRIHandleEvents

	return nil
}
