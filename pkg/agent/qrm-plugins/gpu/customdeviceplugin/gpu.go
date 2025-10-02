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

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const GPUCustomDevicePluginName = "gpu-custom-device-plugin"

type GPUDevicePlugin struct {
	*baseplugin.BasePlugin
}

func NewGPUDevicePlugin(base *baseplugin.BasePlugin) CustomDevicePlugin {
	return &GPUDevicePlugin{
		BasePlugin: base,
	}
}

func (p *GPUDevicePlugin) DeviceName() string {
	return "nvidia.com/gpu"
}

func (p *GPUDevicePlugin) UpdateAllocatableAssociatedDevices(request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	gpuTopology := &machine.GPUTopology{
		GPUs: make(map[string]machine.GPUInfo, len(request.Devices)),
	}

	for _, device := range request.Devices {
		var numaNode []int
		if device.Topology != nil {
			numaNode = make([]int, 0, len(device.Topology.Nodes))

			for _, node := range device.Topology.Nodes {
				if node == nil {
					continue
				}
				numaNode = append(numaNode, int(node.ID))
			}
		}

		gpuTopology.GPUs[device.ID] = machine.GPUInfo{
			Health:   device.Health,
			NUMANode: numaNode,
		}
	}

	err := p.GpuTopologyProvider.SetGPUTopology(gpuTopology)
	if err != nil {
		general.Errorf("set gpu topology failed with error: %v", err)
		return nil, fmt.Errorf("set gpu topology failed with error: %v", err)
	}

	general.Infof("got device %s gpuTopology success: %v", request.DeviceName, gpuTopology)

	return &pluginapi.UpdateAllocatableAssociatedDevicesResponse{}, nil
}

func (p *GPUDevicePlugin) GetAssociatedDeviceTopologyHints(_ *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (p *GPUDevicePlugin) AllocateAssociatedDevice(req *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceAllocationResponse, error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.QosConfig, req.ResourceRequest, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.ResourceRequest.PodNamespace, req.ResourceRequest.PodName, req.ResourceRequest.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	general.InfoS("called",
		"podNamespace", req.ResourceRequest.PodNamespace,
		"podName", req.ResourceRequest.PodName,
		"containerName", req.ResourceRequest.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.ResourceRequest.Annotations,
		"resourceRequests", req.ResourceRequest.ResourceRequests,
		"deviceName", req.DeviceRequest.DeviceName,
		"resourceHint", req.ResourceRequest.Hint,
		"deviceHint", req.DeviceRequest.Hint,
		"availableDevices", req.DeviceRequest.AvailableDevices,
		"reusableDevices", req.DeviceRequest.ReusableDevices,
		"deviceRequest", req.DeviceRequest.DeviceRequest,
	)

	allocationInfo := p.State.GetAllocationInfo(req.ResourceRequest.PodUid, req.ResourceRequest.ContainerName)
	if allocationInfo != nil && allocationInfo.TopologyAwareAllocations != nil {
		allocatedDevices := make([]string, 0, len(allocationInfo.TopologyAwareAllocations))
		for gpuID := range allocationInfo.TopologyAwareAllocations {
			allocatedDevices = append(allocatedDevices, gpuID)
		}
		return &pluginapi.AssociatedDeviceAllocationResponse{
			AllocationResult: &pluginapi.AssociatedDeviceAllocation{
				AllocatedDevices: allocatedDevices,
			},
		}, nil
	}

	_, gpuMemoryRequest, err := util.GetQuantityFromResourceRequests(req.ResourceRequest.ResourceRequests, req.ResourceRequest.ResourceName, false)
	if err != nil {
		return nil, err
	}

	// get hint nodes from request
	hintNodes, err := machine.NewCPUSetUint64(req.DeviceRequest.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, err
	}

	gpuTopology, numaTopologyReady, err := p.GpuTopologyProvider.GetGPUTopology()
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, err
	}

	if !numaTopologyReady {
		general.Warningf("numa topology is not ready")
		return nil, fmt.Errorf("numa topology is not ready")
	}

	allocatedDevices, allocatedGPUMemory, err := p.CalculateAssociatedDevices(gpuTopology, gpuMemoryRequest, hintNodes, req)
	if err != nil {
		general.Warningf("failed to allocate associated devices: %v", err)
		return nil, err
	}

	topologyAwareAllocations := make(map[string]state.GPUAllocation)
	for _, device := range allocatedDevices {
		info, ok := gpuTopology.GPUs[device]
		if !ok {
			return nil, fmt.Errorf("failed to get gpu topology for device: %s", device)
		}

		topologyAwareAllocations[device] = state.GPUAllocation{
			GPUMemoryQuantity: allocatedGPUMemory[device],
			NUMANodes:         info.GetNUMANode(),
		}
	}

	if allocationInfo == nil {
		allocationInfo = &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req.ResourceRequest, commonstate.EmptyOwnerPoolName, qosLevel),
			AllocatedAllocation: state.GPUAllocation{
				GPUMemoryQuantity: gpuMemoryRequest,
				NUMANodes:         hintNodes.ToSliceInt(),
			},
		}
	}

	allocationInfo.TopologyAwareAllocations = topologyAwareAllocations
	p.State.SetAllocationInfo(req.ResourceRequest.PodUid, req.ResourceRequest.ContainerName, allocationInfo, false)
	machineState, err := state.GenerateMachineStateFromPodEntries(p.QrmConfig, p.State.GetPodEntries(), p.GpuTopologyProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to generate machine state from pod entries: %v", err)
	}

	p.State.SetMachineState(machineState, true)

	general.InfoS("allocated devices",
		"podNamespace", req.ResourceRequest.PodNamespace,
		"podName", req.ResourceRequest.PodName,
		"containerName", req.ResourceRequest.ContainerName,
		"qosLevel", qosLevel,
		"allocatedDevices", allocatedDevices)

	return &pluginapi.AssociatedDeviceAllocationResponse{
		AllocationResult: &pluginapi.AssociatedDeviceAllocation{
			AllocatedDevices: allocatedDevices,
		},
	}, nil
}
