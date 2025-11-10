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

package resourceplugin

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
)

type ResourcePluginStub struct {
	*baseplugin.BasePlugin
}

func NewResourcePluginStub(base *baseplugin.BasePlugin) ResourcePlugin {
	return &ResourcePluginStub{BasePlugin: base}
}

func (r ResourcePluginStub) ResourceName() string {
	return "resource-plugin-stub"
}

func (r ResourcePluginStub) GetTopologyHints(_ *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return &pluginapi.ResourceHintsResponse{}, nil
}

func (r ResourcePluginStub) GetTopologyAwareResources(podUID, containerName string) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	// Simply returns a fixed response if the podUID and containerName is found in the state, otherwise return an error
	allocationInfo := r.State.GetAllocationInfo(v1.ResourceName(r.ResourceName()), podUID, containerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("allocationInfo is nil")
	}

	// Simply returns a fixed response
	return &pluginapi.GetTopologyAwareResourcesResponse{
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				r.ResourceName(): {},
			},
		},
	}, nil
}

func (r ResourcePluginStub) GetTopologyAwareAllocatableResources() (*gpuconsts.AllocatableResource, error) {
	// Simply return a fixed response
	return &gpuconsts.AllocatableResource{
		ResourceName: r.ResourceName(),
	}, nil
}

func (r ResourcePluginStub) Allocate(resourceReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	// Simply save resource request and device request in state
	if resourceReq.ResourceName == r.ResourceName() {
		r.State.SetAllocationInfo(v1.ResourceName(r.ResourceName()), resourceReq.PodUid, resourceReq.ContainerName, &state.AllocationInfo{}, false)
	}

	if deviceReq != nil {
		r.State.SetAllocationInfo(v1.ResourceName(deviceReq.DeviceName), resourceReq.PodUid, resourceReq.ContainerName, &state.AllocationInfo{}, false)
	}

	return &pluginapi.ResourceAllocationResponse{}, nil
}
