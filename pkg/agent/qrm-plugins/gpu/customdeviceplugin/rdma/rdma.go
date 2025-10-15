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
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
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
	rdmaTopologyProvider := machine.NewDeviceTopologyProvider(base.RDMADeviceNames)
	base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(gpuconsts.RDMADeviceType, rdmaTopologyProvider)

	return &RDMADevicePlugin{
		BasePlugin:  base,
		deviceNames: base.RDMADeviceNames,
	}
}

func (p *RDMADevicePlugin) DeviceNames() []string {
	return p.deviceNames
}

func (p *RDMADevicePlugin) UpdateAllocatableAssociatedDevices(request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return p.UpdateAllocatableAssociatedDevicesByDeviceType(request, gpuconsts.RDMADeviceType)
}

func (p *RDMADevicePlugin) GetAssociatedDeviceTopologyHints(_ *pluginapi.AssociatedDeviceRequest) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

func (p *RDMADevicePlugin) AllocateAssociatedDevice(
	resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest,
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

	// Check if there is state for the device name
	rdmaAllocationInfo := p.State.GetAllocationInfo(gpuconsts.RDMADeviceType, resReq.PodUid, resReq.ContainerName)
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

	// Find out the GPU devices that are allocated to the container and allocate RDMA devices that correspond to the numa nodes of GPU device
	gpuAllocationInfo := p.State.GetAllocationInfo(v1.ResourceName(resReq.ResourceName), resReq.PodUid, resReq.ContainerName)
	if gpuAllocationInfo == nil || gpuAllocationInfo.TopologyAwareAllocations == nil {
		err = fmt.Errorf("get allocation info of the resource %s for pod %s/%s, container: %s failed with error: %v",
			resReq.ResourceName, resReq.PodNamespace, resReq.PodName, resReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	rdmaTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.RDMADeviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu device topology: %v", err)
	}
	if !numaTopologyReady {
		return nil, fmt.Errorf("gpu device topology is not ready")
	}

	// For every gpu that is allocated to the container, find out the rdma devices that have affinity to the same
	// numa nodes as the gpu and allocate them
	allocatedRdmaDevices := sets.NewString()
	gpuToRdmaAffinityMap, err := p.DeviceTopologyRegistry.GetDeviceAffinity(gpuconsts.GPUDeviceType, gpuconsts.RDMADeviceType)
	if err != nil {
		general.Warningf("failed to get gpu to rdma affinity map: %v", err)
		return nil, err
	}

	hintNodes, err := machine.NewCPUSetUint64(deviceReq.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, err
	}

	for gpuID := range gpuAllocationInfo.TopologyAwareAllocations {
		rdmaDevices, ok := gpuToRdmaAffinityMap[gpuID]
		if !ok {
			return nil, fmt.Errorf("failed to find rdma devices with gpu id %s", gpuID)
		}

		for _, rdmaID := range rdmaDevices {
			// Only allocate when the rdma device is a subset of hint nodes
			isNumaNodeAffinity, err := p.DeviceTopologyRegistry.IsNumaNodeAffinity(gpuconsts.RDMADeviceType, rdmaID, hintNodes)
			if err != nil {
				return nil, fmt.Errorf("failed to check numa node affinity of rdma device %s with error: %v", rdmaID, err)
			}
			if isNumaNodeAffinity {
				allocatedRdmaDevices.Insert(rdmaID)
			}
		}
	}

	// Modify rdma state
	topologyAwareAllocations := make(map[string]state.Allocation)
	allocatedRdmaDevicesRes := allocatedRdmaDevices.List()
	for _, deviceID := range allocatedRdmaDevicesRes {
		info, ok := rdmaTopology.Devices[deviceID]
		if !ok {
			return nil, fmt.Errorf("failed to get rdma info for device %s", deviceID)
		}

		topologyAwareAllocations[deviceID] = state.Allocation{
			Quantity:  1,
			NUMANodes: info.GetNUMANode(),
		}
	}

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resReq, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.Allocation{
			Quantity:  1,
			NUMANodes: hintNodes.ToSliceInt(),
		},
	}

	allocationInfo.TopologyAwareAllocations = topologyAwareAllocations
	p.State.SetAllocationInfo(gpuconsts.RDMADeviceType, resReq.PodUid, resReq.ContainerName, allocationInfo, false)
	machineState, err := state.GenerateMachineStateFromPodEntries(p.State.GetPodResourceEntries(), p.DeviceTopologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to generate machine state from pod entries: %v", err)
	}

	p.State.SetMachineState(machineState, true)

	general.InfoS("allocated rdma devices",
		"podNamespace", resReq.PodNamespace,
		"podName", resReq.PodName,
		"containerName", resReq.ContainerName,
		"qosLevel", qosLevel,
		"allocatedDevices", allocatedRdmaDevicesRes)

	return &pluginapi.AssociatedDeviceAllocationResponse{
		AllocationResult: &pluginapi.AssociatedDeviceAllocation{
			AllocatedDevices: allocatedRdmaDevicesRes,
		},
	}, nil
}
