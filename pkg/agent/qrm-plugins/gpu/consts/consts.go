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

package consts

import (
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
)

type AllocatedResource struct {
	ResourceName string
	PodName      string
	PodNamespace string
	*pluginapi.TopologyAwareResource
}

type AllocatableResource struct {
	ResourceName string
	*pluginapi.AllocatableTopologyAwareResource
}

const (
	GPUDeviceType  = "gpu_device"
	RDMADeviceType = "rdma_device"

	// GPUResourcePluginPolicyNameStatic is the policy name of static gpu resource plugin
	GPUResourcePluginPolicyNameStatic = string(consts.ResourcePluginPolicyNameStatic)

	GPUPluginDynamicPolicyName = qrm.QRMPluginNameGPU + "_" + GPUResourcePluginPolicyNameStatic
	ClearResidualState         = GPUPluginDynamicPolicyName + "_clear_residual_state"

	GPUMemPluginName = "gpu_mem_resource_plugin"

	StateCheckPeriod          = 30 * time.Second
	StateCheckTolerationTimes = 3
	MaxResidualTime           = 5 * time.Minute
)
