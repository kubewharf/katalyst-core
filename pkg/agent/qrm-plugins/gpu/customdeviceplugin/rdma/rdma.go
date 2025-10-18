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
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
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
	base.RegisterDeviceNameToType(base.RDMADeviceNames, gpuconsts.RDMADeviceType)

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

// TODO：accompany resource name == ""
// check if rdma is allocated to other containers, make sure they do not share rdma
func (p *RDMADevicePlugin) AllocateAssociatedDevice(
	resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest, accompanyResourceName string,
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
	p.Lock()
	defer p.Unlock()

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

	rdmaTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.RDMADeviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu device topology: %v", err)
	}
	if !numaTopologyReady {
		return nil, fmt.Errorf("gpu device topology is not ready")
	}

	hintNodes, err := machine.NewCPUSetUint64(deviceReq.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, err
	}

	var allocatedRdmaDevices []string

	// No accompany resource name
	if accompanyResourceName == "" {
		allocatedRdmaDevices, err = p.allocateWithNoAccompanyResource(deviceReq, rdmaTopology, hintNodes)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate with no accompany resource: %v", err)
		}
	} else {
		allocatedRdmaDevices, err = p.allocateWithAccompanyResource(deviceReq, resReq, accompanyResourceName)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate with accompany resource: %v", err)
		}
	}

	// Modify rdma state
	topologyAwareAllocations := make(map[string]state.Allocation)
	for _, deviceID := range allocatedRdmaDevices {
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
		"allocatedRdmaDevices", allocatedRdmaDevices)

	return &pluginapi.AssociatedDeviceAllocationResponse{
		AllocationResult: &pluginapi.AssociatedDeviceAllocation{
			AllocatedDevices: allocatedRdmaDevices,
		},
	}, nil
}

// allocateWithNoAccompanyResource allocates the rdma devices by best effort basis on the by making sure that
// it fits the hint nodes.
func (p *RDMADevicePlugin) allocateWithNoAccompanyResource(
	deviceReq *pluginapi.DeviceRequest, rdmaTopology *machine.DeviceTopology, hintNodes machine.CPUSet,
) ([]string, error) {
	reqQuantity := deviceReq.GetDeviceRequest()

	machineState, ok := p.State.GetMachineState()[gpuconsts.RDMADeviceType]
	if !ok {
		return nil, fmt.Errorf("no machine state for resource %s", gpuconsts.RDMADeviceType)
	}

	allocatedDevices := sets.NewString()
	allocateDevices := func(devices ...string) bool {
		for _, device := range devices {
			allocatedDevices.Insert(device)
			if allocatedDevices.Len() >= int(reqQuantity) {
				return true
			}
		}
		return false
	}

	availableDevices := deviceReq.GetAvailableDevices()
	reusableDevices := deviceReq.GetReusableDevices()

	// allocate reusable devices first
	allocated := allocateDevices(reusableDevices...)
	if allocated {
		return allocatedDevices.UnsortedList(), nil
	}

	for _, device := range availableDevices {
		if !gpuutil.IsNUMAAffinityDevice(device, rdmaTopology, hintNodes) {
			continue
		}

		if !machineState.IsRequestSatisfied(device, 1, 1) {
			general.Infof("available numa affinity rdma %s is already allocated", device)
			continue
		}

		if allocateDevices(device) {
			return allocatedDevices.UnsortedList(), nil
		}
	}

	return nil, fmt.Errorf("not enough available RDMAs found in rdmaTopology, number of needed RDMAs: %d, availableDevices len: %d, allocatedDevices len: %d", reqQuantity, len(availableDevices), len(allocatedDevices))
}

// TODO: first allocate to reusable devices, then allocate to available devices proportionally based on numa affinity
func (p *RDMADevicePlugin) allocateWithAccompanyResource(
	deviceReq *pluginapi.DeviceRequest, resReq *pluginapi.ResourceRequest, accompanyResourceName string,
) ([]string, error) {
	var err error

	// Find out the accompany devices that are allocated to the container and allocate RDMA devices that correspond to the numa nodes of accompany device
	accompanyDeviceType, err := p.GetResourceTypeFromDeviceName(accompanyResourceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get device type for accompany resource %s: %v", accompanyResourceName, err)
	}

	accompanyAllocationInfo := p.State.GetAllocationInfo(v1.ResourceName(accompanyDeviceType), resReq.PodUid, resReq.ContainerName)
	if accompanyAllocationInfo == nil || accompanyAllocationInfo.TopologyAwareAllocations == nil {
		err = fmt.Errorf("get allocation info of the resource %s for pod %s/%s, container: %s failed with error: %v",
			resReq.ResourceName, resReq.PodNamespace, resReq.PodName, resReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	// Get ratio of accompany resource to target device
	accompanyResourceToTargetDeviceRatio := p.State.GetMachineState().GetRatioOfAccompanyResourceToTargetResource(accompanyResourceName, deviceReq.DeviceName)

	// Allocate target device according to ratio of accompany resource to target device
	podResourceEntries := p.State.GetPodResourceEntries()
	totalAllocated, accompanyResourceIds := podResourceEntries.GetTotalAllocatedResourceOfContainer(v1.ResourceName(accompanyResourceName), resReq.PodUid, resReq.ContainerName)

	rdmaToBeAllocated := int(math.Ceil(float64(totalAllocated) * accompanyResourceToTargetDeviceRatio))

	// For every gpu that is allocated to the container, find out the rdma devices that have affinity to the same
	// numa nodes as the gpu and allocate them
	accompanyResourceToRdmaAffinityMap, err := p.DeviceTopologyRegistry.GetDeviceAffinity(accompanyResourceName, deviceReq.DeviceName)
	if err != nil {
		general.Warningf("failed to get gpu to rdma affinity map: %v", err)
		return nil, err
	}

	machineState := p.State.GetMachineState()[v1.ResourceName(gpuconsts.RDMADeviceType)]

	allocatedDevices := sets.NewString()
	allocateDevices := func(devices ...string) bool {
		for _, device := range devices {
			allocatedDevices.Insert(device)
			if allocatedDevices.Len() >= rdmaToBeAllocated {
				return true
			}
		}
		return false
	}

	for accompanyResourceId := range accompanyResourceIds {
		rdmaDevices, ok := accompanyResourceToRdmaAffinityMap[accompanyResourceId]
		if !ok {
			general.Warningf("failed to get rdma device with accompany device id: %s", accompanyResourceId)
			continue
		}

		// Iterate through the rdma devices and check if they are already allocated
		for _, rdmaDevice := range rdmaDevices {
			if !machineState.IsRequestSatisfied(rdmaDevice, 1, 1) {
				continue
			}

			if allocateDevices(rdmaDevice) {
				return allocatedDevices.UnsortedList(), nil
			}
		}
	}

	// Did not find enough available rdma devices to allocate, return the devices that are already allocated
	return allocatedDevices.UnsortedList(), nil
}
