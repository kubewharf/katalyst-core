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

package rdma

import (
	"context"
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const RDMACustomDevicePluginName = "rdma-custom-device-plugin"

type RDMADevicePlugin struct {
	*baseplugin.BasePlugin
	deviceNames []string
}

func NewRDMADevicePlugin(base *baseplugin.BasePlugin) customdeviceplugin.CustomDevicePlugin {
	rdmaTopologyProvider := machine.NewDeviceTopologyProvider(base.Conf.RDMADeviceNames)
	base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.RDMADeviceType, rdmaTopologyProvider)
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(gpuconsts.RDMADeviceType,
		state.NewGenericDefaultResourceStateGenerator(gpuconsts.RDMADeviceType, base.DeviceTopologyRegistry))
	base.RegisterDeviceNameToType(base.Conf.RDMADeviceNames, gpuconsts.RDMADeviceType)

	return &RDMADevicePlugin{
		BasePlugin:  base,
		deviceNames: base.Conf.RDMADeviceNames,
	}
}

func (p *RDMADevicePlugin) DefaultPreAllocateResourceName() string {
	return ""
}

func (p *RDMADevicePlugin) DeviceNames() []string {
	return p.deviceNames
}

func (p *RDMADevicePlugin) UpdateAllocatableAssociatedDevices(ctx context.Context, request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return p.UpdateAllocatableAssociatedDevicesByDeviceType(request, gpuconsts.RDMADeviceType)
}

func (p *RDMADevicePlugin) GetAssociatedDeviceTopologyHints(context.Context, *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

// AllocateAssociatedDevice check if rdma is allocated to other containers, make sure they do not share rdma
func (p *RDMADevicePlugin) AllocateAssociatedDevice(
	ctx context.Context, resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest, accompanyResourceName string,
) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, resReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
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

	// Check if there is state for the device name
	rdmaAllocationInfo := p.GetState().GetAllocationInfo(gpuconsts.RDMADeviceType, resReq.PodUid, resReq.ContainerName)
	if rdmaAllocationInfo != nil && rdmaAllocationInfo.TopologyAwareAllocations != nil {
		allocatedDevices := make([]string, 0, len(rdmaAllocationInfo.TopologyAwareAllocations))
		for rdmaID := range rdmaAllocationInfo.TopologyAwareAllocations {
			allocatedDevices = append(allocatedDevices, rdmaID)
		}
		return &pluginapi.AssociatedDeviceAllocationResponse{
			AllocationResult: &pluginapi.AssociatedDeviceAllocation{
				AllocatedDevices: allocatedDevices,
			},
		}, nil
	}

	rdmaTopology, _, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.RDMADeviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu device topology: %v", err)
	}

	hintNodes, err := machine.NewCPUSetUint64(deviceReq.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, err
	}

	accompanyResourceName = p.ResolveResourceName(accompanyResourceName, false)

	// Use strategy framework to allocate RDMA devices
	result, err := manager.AllocateDevicesUsingStrategy(
		resReq,
		deviceReq,
		p.DeviceTopologyRegistry,
		p.Conf.GPUQRMPluginConfig,
		p.Emitter,
		p.MetaServer,
		p.GetState().GetMachineState(),
		qosLevel,
		gpuconsts.RDMADeviceType,
		accompanyResourceName,
	)
	if err != nil {
		return nil, fmt.Errorf("RDMA allocation using strategy failed: %v", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("RDMA allocation failed: %v", result.ErrorMessage)
	}

	allocatedRdmaDevices := result.AllocatedDevices

	// Modify rdma state
	topologyAwareAllocations := make(map[string]state.Allocation)
	for _, deviceID := range allocatedRdmaDevices {
		info, ok := rdmaTopology.Devices[deviceID]
		if !ok {
			return nil, fmt.Errorf("failed to get rdma info for device %s", deviceID)
		}

		topologyAwareAllocations[deviceID] = state.Allocation{
			Quantity:  1,
			NUMANodes: info.GetNUMANodes(),
		}
	}

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resReq, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.Allocation{
			Quantity:  float64(len(allocatedRdmaDevices)),
			NUMANodes: hintNodes.ToSliceInt(),
		},
	}

	allocationInfo.TopologyAwareAllocations = topologyAwareAllocations
	p.GetState().SetAllocationInfo(gpuconsts.RDMADeviceType, resReq.PodUid, resReq.ContainerName, allocationInfo, false)
	resourceState, err := p.GenerateResourceStateFromPodEntries(gpuconsts.RDMADeviceType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rdma device state from pod entries: %v", err)
	}

	p.GetState().SetResourceState(gpuconsts.RDMADeviceType, resourceState, true)

	general.InfoS("allocated rdma devices",
		"podNamespace", resReq.PodNamespace,
		"podName", resReq.PodName,
		"containerName", resReq.ContainerName,
		"qosLevel", qosLevel,
		"allocatedRdmaDevices", allocatedRdmaDevices)

	return &pluginapi.AssociatedDeviceAllocationResponse{
		AllocationResult: &pluginapi.AssociatedDeviceAllocation{
			AllocatedDevices: allocatedRdmaDevices,
		},
	}, nil
}
