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
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const GPUCustomDevicePluginName = "gpu-custom-device-plugin"

const (
	defaultAccompanyResourceName = string(consts.ResourceGPUMemory)
)

type GPUDevicePlugin struct {
	*baseplugin.BasePlugin
	deviceNames []string
}

func NewGPUDevicePlugin(base *baseplugin.BasePlugin) customdeviceplugin.CustomDevicePlugin {
	gpuTopologyProvider := machine.NewDeviceTopologyProvider(base.Conf.GPUDeviceNames)
	base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.GPUDeviceType, gpuTopologyProvider)
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(gpuconsts.GPUDeviceType,
		state.NewGenericDefaultResourceStateGenerator(gpuconsts.GPUDeviceType, base.DeviceTopologyRegistry))
	base.RegisterDeviceNameToType(base.Conf.GPUDeviceNames, gpuconsts.GPUDeviceType)

	return &GPUDevicePlugin{
		BasePlugin:  base,
		deviceNames: base.Conf.GPUDeviceNames,
	}
}

func (p *GPUDevicePlugin) DefaultAccompanyResourceName() string {
	return defaultAccompanyResourceName
}

func (p *GPUDevicePlugin) DeviceNames() []string {
	return p.deviceNames
}

func (p *GPUDevicePlugin) UpdateAllocatableAssociatedDevices(ctx context.Context, request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return p.UpdateAllocatableAssociatedDevicesByDeviceType(request, gpuconsts.GPUDeviceType)
}

func (p *GPUDevicePlugin) GetAssociatedDeviceTopologyHints(context.Context, *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (p *GPUDevicePlugin) AllocateAssociatedDevice(
	ctx context.Context, resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest, _ string,
) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	qosLevel, err := qrmutil.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, resReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
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

	gpuAllocationInfo := p.State.GetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName)
	if gpuAllocationInfo != nil {
		if gpuAllocationInfo.TopologyAwareAllocations == nil {
			return nil, fmt.Errorf("GPU topology aware allocation info is nil")
		}
		allocatedDevices := make([]string, 0, len(gpuAllocationInfo.TopologyAwareAllocations))
		for gpuID := range gpuAllocationInfo.TopologyAwareAllocations {
			allocatedDevices = append(allocatedDevices, gpuID)
		}
		return &pluginapi.AssociatedDeviceAllocationResponse{
			AllocationResult: &pluginapi.AssociatedDeviceAllocation{
				AllocatedDevices: allocatedDevices,
			},
		}, nil
	}

	var allocatedDevices []string
	memoryAllocationInfo := p.State.GetAllocationInfo(v1.ResourceName(defaultAccompanyResourceName), resReq.PodUid, resReq.ContainerName)
	// GPU memory should have been allocated at this stage.
	// We anticipate that gpu devices have also been allocated, so we can directly use the allocated devices from the gpu memory state.
	if memoryAllocationInfo == nil || memoryAllocationInfo.TopologyAwareAllocations == nil {
		// When GPU memory allocation info is nil, invoke the GPU allocate strategy to perform GPU allocation
		general.InfoS("GPU memory allocation info is nil, invoking GPU allocate strategy",
			"podNamespace", resReq.PodNamespace,
			"podName", resReq.PodName,
			"containerName", resReq.ContainerName)

		// Get GPU topology
		gpuTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
		if err != nil {
			general.Warningf("failed to get gpu topology: %v", err)
			return nil, fmt.Errorf("failed to get gpu topology: %w", err)
		}

		if !numaTopologyReady {
			general.Warningf("numa topology is not ready")
			return nil, fmt.Errorf("numa topology is not ready")
		}

		// Use the strategy framework to allocate GPU devices
		result, err := manager.AllocateGPUUsingStrategy(
			resReq,
			deviceReq,
			gpuTopology,
			p.Conf.GPUQRMPluginConfig,
			p.Emitter,
			p.MetaServer,
			p.State.GetMachineState(),
			qosLevel,
		)
		if err != nil {
			return nil, fmt.Errorf("GPU allocation using strategy failed: %v", err)
		}

		if !result.Success {
			return nil, fmt.Errorf("GPU allocation failed: %s", result.ErrorMessage)
		}

		allocatedDevices = result.AllocatedDevices
	} else {
		// when GPU memory allocation info exists
		for gpuID := range memoryAllocationInfo.TopologyAwareAllocations {
			allocatedDevices = append(allocatedDevices, gpuID)
		}
	}

	gpuTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, fmt.Errorf("failed to get gpu topology: %w", err)
	}

	if !numaTopologyReady {
		general.Warningf("numa topology is not ready")
		return nil, fmt.Errorf("numa topology is not ready")
	}

	// Save gpu device allocations in state
	numaNodes := machine.NewCPUSet()
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
		numaNodes.Add(info.NumaNodes...)
	}

	gpuDeviceAllocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resReq, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.Allocation{
			Quantity:  float64(len(allocatedDevices)),
			NUMANodes: numaNodes.ToSliceInt(),
		},
	}
	gpuDeviceAllocationInfo.TopologyAwareAllocations = gpuDeviceTopologyAwareAllocations

	// TODO：State can be updated using the actual resource name
	p.State.SetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName, gpuDeviceAllocationInfo, false)
	resourceState, err := p.GenerateResourceStateFromPodEntries(gpuconsts.GPUDeviceType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate gpu device state from pod entries: %v", err)
	}
	p.State.SetResourceState(gpuconsts.GPUDeviceType, resourceState, true)

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
