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

package customdeviceplugin

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
)

type CustomDevicePluginStub struct {
	*baseplugin.BasePlugin
}

func NewCustomDevicePluginStub(base *baseplugin.BasePlugin) CustomDevicePlugin {
	return &CustomDevicePluginStub{
		BasePlugin: base,
	}
}

func (c CustomDevicePluginStub) DeviceNames() []string {
	return []string{"custom-device-plugin-stub"}
}

func (c CustomDevicePluginStub) GetAssociatedDeviceTopologyHints(_ *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (c CustomDevicePluginStub) UpdateAllocatableAssociatedDevices(_ *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return &pluginapi.UpdateAllocatableAssociatedDevicesResponse{}, nil
}

func (c CustomDevicePluginStub) DefaultAccompanyResourceName() string {
	return "resource-plugin-stub"
}

func (c CustomDevicePluginStub) AllocateAssociatedDevice(resReq *pluginapi.ResourceRequest, _ *pluginapi.DeviceRequest, accompanyResourceName string) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	// Simply check if the accompany resource has been allocated
	// If it has been allocated, allocate the associated device
	accompanyResourceAllocation := c.State.GetAllocationInfo(v1.ResourceName(accompanyResourceName), resReq.PodUid, resReq.ContainerName)
	if accompanyResourceAllocation != nil {
		c.State.SetAllocationInfo(v1.ResourceName(accompanyResourceName), resReq.PodUid, resReq.ContainerName, &state.AllocationInfo{}, false)
		return &pluginapi.AssociatedDeviceAllocationResponse{}, nil
	} else {
		return nil, fmt.Errorf("accompany resource %s has not been allocated", accompanyResourceName)
	}
}

type CustomDevicePluginStub2 struct {
	*baseplugin.BasePlugin
}

func NewCustomDevicePluginStub2(base *baseplugin.BasePlugin) CustomDevicePlugin {
	return &CustomDevicePluginStub2{
		BasePlugin: base,
	}
}

func (c CustomDevicePluginStub2) DeviceNames() []string {
	return []string{"custom-device-plugin-stub-2"}
}

func (c CustomDevicePluginStub2) GetAssociatedDeviceTopologyHints(_ *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (c CustomDevicePluginStub2) UpdateAllocatableAssociatedDevices(_ *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return &pluginapi.UpdateAllocatableAssociatedDevicesResponse{}, nil
}

func (c CustomDevicePluginStub2) DefaultAccompanyResourceName() string {
	return "resource-plugin-stub"
}

func (c CustomDevicePluginStub2) AllocateAssociatedDevice(resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest, _ string) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	// Simply allocate the associated device
	c.State.SetAllocationInfo(v1.ResourceName(deviceReq.DeviceName), resReq.PodUid, resReq.ContainerName, &state.AllocationInfo{}, false)
	return &pluginapi.AssociatedDeviceAllocationResponse{}, nil
}
