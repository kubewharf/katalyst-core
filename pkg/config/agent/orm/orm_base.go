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

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type GenericORMConfiguration struct {
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

func NewGenericORMConfiguration() *GenericORMConfiguration {
	return &GenericORMConfiguration{
		ORMWorkMode:                     consts.WorkModeBypass,
		ORMReconcilePeriod:              time.Second * 5,
		ORMResourceNamesMap:             map[string]string{},
		ORMPodNotifyChanLen:             10,
		TopologyPolicyName:              "none",
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
