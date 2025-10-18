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

package gpu

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const GPUCustomDevicePluginName = "gpu-custom-device-plugin"

type GPUDevicePlugin struct {
	*baseplugin.BasePlugin
	deviceNames []string
}

func NewGPUDevicePlugin(base *baseplugin.BasePlugin) customdeviceplugin.CustomDevicePlugin {
	gpuTopologyProvider := machine.NewDeviceTopologyProvider(base.GPUDeviceNames)
	base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.GPUDeviceType, gpuTopologyProvider)
	base.RegisterDeviceNameToType(base.GPUDeviceNames, gpuconsts.GPUDeviceType)

	return &GPUDevicePlugin{
		BasePlugin:  base,
		deviceNames: base.GPUDeviceNames,
	}
}

func (p *GPUDevicePlugin) DeviceNames() []string {
	return p.deviceNames
}

func (p *GPUDevicePlugin) UpdateAllocatableAssociatedDevices(request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return p.UpdateAllocatableAssociatedDevicesByDeviceType(request, gpuconsts.GPUDeviceType)
}

func (p *GPUDevicePlugin) GetAssociatedDeviceTopologyHints(_ *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (p *GPUDevicePlugin) AllocateAssociatedDevice(
	resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest, _ string,
) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.QosConfig, resReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			resReq.PodNamespace, resReq.PodName, resReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	general.InfoS("called",
		"podNamespace", resReq.PodNamespace,
		"podName", resReq.PodName,
		"containerName", resReq.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", resReq.Annotations,
		"resourceRequests", resReq.ResourceRequests,
		"deviceName", deviceReq.DeviceName,
		"resourceHint", resReq.Hint,
		"deviceHint", deviceReq.Hint,
		"availableDevices", deviceReq.AvailableDevices,
		"reusableDevices", deviceReq.ReusableDevices,
		"deviceRequest", deviceReq.DeviceRequest,
	)

	memoryAllocationInfo := p.State.GetAllocationInfo(consts.ResourceGPUMemory, resReq.PodUid, resReq.ContainerName)
	// GPU memory should have been allocated at this stage.
	// We anticipate that gpu devices have also been allocated, so we can directly use the allocated devices from the gpu memory state.
	if memoryAllocationInfo == nil || memoryAllocationInfo.TopologyAwareAllocations == nil {
		return nil, fmt.Errorf("gpu memory allocation info is nil, which is unexpected")
	}

	allocatedDevices := make([]string, 0, len(memoryAllocationInfo.TopologyAwareAllocations))
	for gpuID := range memoryAllocationInfo.TopologyAwareAllocations {
		allocatedDevices = append(allocatedDevices, gpuID)
	}

	gpuTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
	if err != nil {
		general.Warningf("failed to get gpu topology: %w", err)
		return nil, fmt.Errorf("failed to get gpu topology: %w", err)
	}

	if !numaTopologyReady {
		general.Warningf("numa topology is not ready")
		return nil, fmt.Errorf("numa topology is not ready")
	}

	// Save gpu device allocations in state
	gpuDeviceTopologyAwareAllocations := make(map[string]state.Allocation)
	for _, deviceID := range allocatedDevices {
		info, ok := gpuTopology.Devices[deviceID]
		if !ok {
			return nil, fmt.Errorf("failed to get gpu info for device: %s", deviceID)
		}

		gpuDeviceTopologyAwareAllocations[deviceID] = state.Allocation{
			Quantity:  1,
			NUMANodes: info.NumaNodes,
		}
	}

	gpuDeviceAllocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resReq, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.Allocation{
			Quantity:  float64(len(allocatedDevices)),
			NUMANodes: memoryAllocationInfo.AllocatedAllocation.NUMANodes,
		},
	}
	gpuDeviceAllocationInfo.TopologyAwareAllocations = gpuDeviceTopologyAwareAllocations

	p.State.SetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName, gpuDeviceAllocationInfo, false)
	gpuDeviceMachineState, err := state.GenerateMachineStateFromPodEntries(p.State.GetPodResourceEntries(), p.DeviceTopologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to generate gpu device machine state from pod entries: %v", err)
	}
	p.State.SetMachineState(gpuDeviceMachineState, true)

	general.InfoS("allocated gpu devices",
		"podNamespace", resReq.PodNamespace,
		"podName", resReq.PodName,
		"containerName", resReq.ContainerName,
		"qosLevel", qosLevel,
		"allocatedDevices", allocatedDevices)

	return &pluginapi.AssociatedDeviceAllocationResponse{
		AllocationResult: &pluginapi.AssociatedDeviceAllocation{
			AllocatedDevices: allocatedDevices,
		},
	}, nil
}
