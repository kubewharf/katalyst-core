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
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const GPUCustomDevicePluginName = "gpu-custom-device-plugin"

const (
	defaultPreAllocateResourceName = string(consts.ResourceGPUMemory)
)

type GPUDevicePlugin struct {
	*baseplugin.BasePlugin
	deviceNames []string
}

func NewGPUDevicePlugin(base *baseplugin.BasePlugin) customdeviceplugin.CustomDevicePlugin {
	for _, deviceName := range base.Conf.GPUDeviceNames {
		gpuTopologyProvider := machine.NewDeviceTopologyProvider()
		base.DeviceTopologyRegistry.RegisterDeviceTopologyProvider(deviceName, gpuTopologyProvider)
	}

	// GPUDeviceType is the key used for GPU state management in the QRM framework,
	// while GPUDeviceNames are the actual resource names used to fetch the GPU device topologies.
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(gpuconsts.GPUDeviceType,
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, 1, true))
	base.RegisterDeviceNames(base.Conf.GPUDeviceNames, gpuconsts.GPUDeviceType)

	return &GPUDevicePlugin{
		BasePlugin:  base,
		deviceNames: base.Conf.GPUDeviceNames,
	}
}

func (p *GPUDevicePlugin) DefaultPreAllocateResourceName() string {
	return defaultPreAllocateResourceName
}

// filterOccupiedDevicesFromRequest filters out devices with existing allocations from
// the provided DeviceRequest's AvailableDevices and ReusableDevices in place.
// A device is considered occupied if the ResourceMilliGPU resource in AllocationResourcesMap
// has len(PodEntries) > 0 for that device.
func filterOccupiedDevicesFromRequest(
	req *pluginapi.DeviceRequest,
	allocationResourcesMap state.AllocationResourcesMap,
) {
	// Helper to filter a single list of devices
	filter := func(devices []string) []string {
		filtered := make([]string, 0, len(devices))
		for _, deviceID := range devices {
			deviceOccupied := false
			for resourceName, allocationMap := range allocationResourcesMap {
				if string(resourceName) == string(consts.ResourceMilliGPU) {
					if allocationState, exists := allocationMap[deviceID]; exists {
						if len(allocationState.PodEntries) > 0 {
							deviceOccupied = true
							break
						}
					}
				}
			}
			if !deviceOccupied {
				filtered = append(filtered, deviceID)
			}
		}
		return filtered
	}
	req.AvailableDevices = filter(req.AvailableDevices)
	req.ReusableDevices = filter(req.ReusableDevices)
}

func (p *GPUDevicePlugin) DeviceNames() []string {
	return p.deviceNames
}

func (p *GPUDevicePlugin) UpdateAllocatableAssociatedDevices(
	ctx context.Context, request *pluginapi.UpdateAllocatableAssociatedDevicesRequest,
) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	return p.BasePlugin.UpdateAllocatableAssociatedDevices(request)
}

func (p *GPUDevicePlugin) GetAssociatedDeviceTopologyHints(
	_ context.Context, req *pluginapi.AssociatedDeviceRequest,
) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	if req == nil || req.ResourceRequest == nil {
		return nil, fmt.Errorf("GetAssociatedDeviceTopologyHints got invalid request")
	}

	resReq := req.ResourceRequest
	qosLevel, err := qrmutil.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, resReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			resReq.PodNamespace, resReq.PodName, resReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	general.InfoS("GetAssociatedDeviceTopologyHints called",
		"podNamespace", resReq.PodNamespace,
		"podName", resReq.PodName,
		"containerName", resReq.ContainerName,
		"deviceName", req.DeviceName,
		"qosLevel", qosLevel,
	)

	var hints []*pluginapi.TopologyHint

	// 1. Check if GPU device allocation already exists.
	gpuAllocationInfo := p.GetState().GetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName)
	if gpuAllocationInfo != nil && gpuAllocationInfo.TopologyAwareAllocations != nil {
		general.InfoS("generating hints from existing GPU allocation",
			"podNamespace", resReq.PodNamespace,
			"podName", resReq.PodName,
			"containerName", resReq.ContainerName,
			"deviceName", req.DeviceName,
		)
		hints = p.generateHintsFromAllocation(gpuAllocationInfo)
	} else if preAllocateResourceAllocationInfo := p.GetState().GetAllocationInfo(v1.ResourceName(defaultPreAllocateResourceName), resReq.PodUid, resReq.ContainerName); preAllocateResourceAllocationInfo != nil && preAllocateResourceAllocationInfo.TopologyAwareAllocations != nil {
		// 2. Check if pre-allocate resource allocation already exists.
		general.InfoS("generating hints from existing GPU pre-allocate resource allocation",
			"podNamespace", resReq.PodNamespace,
			"podName", resReq.PodName,
			"containerName", resReq.ContainerName,
			"deviceName", req.DeviceName,
		)
		hints = p.generateHintsFromAllocation(preAllocateResourceAllocationInfo)
	} else {
		// 3. No existing allocation: locate the target DeviceRequest, filter out devices already
		// occupied by other containers, and generate hints from the GPU topology.
		targetDeviceReq := gpuutil.FindDeviceRequest(req.DeviceRequest, req.DeviceName)

		if targetDeviceReq == nil {
			return nil, fmt.Errorf("no target device plugin found for target device %s", req.DeviceName)
		}

		// Short circuit with nil hints when the request is 0
		if targetDeviceReq.DeviceRequest == 0 {
			return p.buildAssociatedDeviceHintsResponse(req, nil), nil
		}

		filterOccupiedDevicesFromRequest(targetDeviceReq, p.GetState().GetMachineState())

		gpuTopology, err := p.DeviceTopologyRegistry.GetDeviceTopology(targetDeviceReq.DeviceName)
		if err != nil {
			general.Warningf("failed to get gpu topology: %v", err)
			return nil, fmt.Errorf("failed to get gpu topology: %w", err)
		}

		hints = p.generateDeviceTopologyHints(targetDeviceReq, gpuTopology, resReq, qosLevel)
	}

	if len(hints) == 0 {
		return nil, fmt.Errorf("GetAssociatedDeviceTopologyHints got empty hints")
	}

	return p.buildAssociatedDeviceHintsResponse(req, hints), nil
}

func (p *GPUDevicePlugin) generateHintsFromAllocation(allocationInfo *state.AllocationInfo) []*pluginapi.TopologyHint {
	nodesSet := sets.NewInt()
	for _, alloc := range allocationInfo.TopologyAwareAllocations {
		nodesSet.Insert(alloc.NUMANodes...)
	}

	nodes := make([]uint64, 0, nodesSet.Len())
	for _, node := range nodesSet.List() {
		nodes = append(nodes, uint64(node))
	}

	return []*pluginapi.TopologyHint{
		{
			Nodes:     nodes,
			Preferred: true,
		},
	}
}

// setPreferredHints sets hints to be preferred, prioritizing hints with NUMA nodes that match the selected NUMANodes.
// Otherwise, it sets hints with NUMA nodes that match the minAffinitySize to be preferred.
func setPreferredHints(
	hints []*pluginapi.TopologyHint,
	selectedNUMANodes machine.CPUSet,
	minAffinitySize int,
) {
	if !selectedNUMANodes.IsEmpty() {
		for _, hint := range hints {
			hintNUMANodes, err := machine.NewCPUSetUint64(hint.Nodes...)
			if err == nil && hintNUMANodes.Equals(selectedNUMANodes) {
				hint.Preferred = true
				return
			}
		}
	}

	for _, hint := range hints {
		if len(hint.Nodes) == minAffinitySize {
			hint.Preferred = true
		}
	}
}

func (p *GPUDevicePlugin) generateDeviceTopologyHints(
	deviceReq *pluginapi.DeviceRequest,
	gpuTopology *machine.DeviceTopology,
	resReq *pluginapi.ResourceRequest,
	qosLevel string,
) []*pluginapi.TopologyHint {
	request := int(deviceReq.DeviceRequest)
	available := sets.NewString(deviceReq.AvailableDevices...)
	reusable := sets.NewString(deviceReq.ReusableDevices...)

	if available.Union(reusable).Len() < request {
		general.Warningf("Unable to generate topology hints: requested number of devices unavailable, request: %d, available: %d",
			request, available.Union(reusable).Len())
		return nil
	}

	// Gather all NUMA nodes that have healthy GPUs
	numaNodesSet := sets.NewInt()
	numaNodesByDevice := make(map[string][]int, len(gpuTopology.Devices))
	for deviceID, dev := range gpuTopology.Devices {
		numaNodes := dev.NumaNodes
		// When there are no numa nodes in a device, fallback to the FallbackNUMANodeID
		if len(numaNodes) == 0 {
			numaNodes = []int{machine.FallbackNUMANodeID}
		}
		numaNodesByDevice[deviceID] = numaNodes

		if dev.Health != pluginapi.Healthy {
			continue
		}

		numaNodesSet.Insert(numaNodes...)
	}
	numaNodes := numaNodesSet.List()

	// minAffinitySize tracks the minimum mask size that satisfies the Strategy
	minAffinitySize := len(numaNodes)
	var hints []*pluginapi.TopologyHint

	selectedDevices := gpuutil.ParseGPUSelection(
		resReq.Annotations,
		p.Conf.GPUQRMPluginConfig.GPUSelectionResultAnnotationKey,
	)
	selectedNUMANodes := gpuTopology.GetDeviceNUMANodes(selectedDevices.UnsortedList()...)

	// Iterate through all combinations of NUMA Nodes and build hints from them.
	machine.IterateBitMasks(numaNodes, len(numaNodes), func(mask machine.BitMask) {
		// Fast Path: Check to see if all of the reusable devices are part of the bitmask.
		numMatching := 0
		for d := range reusable {
			deviceNUMANodes, ok := numaNodesByDevice[d]
			if !ok {
				continue
			}
			if !mask.AnySet(deviceNUMANodes) {
				return
			}
			numMatching++
		}

		// Fast Path: Check to see if enough available devices remain on the
		// current NUMA node combination to satisfy the device request.
		for d := range available {
			deviceNUMANodes, ok := numaNodesByDevice[d]
			if !ok {
				continue
			}
			if mask.AnySet(deviceNUMANodes) {
				numMatching++
			}
		}

		// If they don't, then move onto the next combination.
		if numMatching < request {
			return
		}

		// Slow Path: Use Strategy Framework to verify inter-GPU affinity (NVLink/PCIe etc.)
		bits := mask.GetBits()
		nodes := make([]uint64, len(bits))
		for i, bit := range bits {
			nodes[i] = uint64(bit)
		}

		// Deep copy deviceReq and inject the current NUMA hint
		deviceReqCopy := *deviceReq
		deviceReqCopy.Hint = &pluginapi.TopologyHint{
			Nodes:     nodes,
			Preferred: false,
		}

		result, err := manager.AllocateDevicesUsingStrategy(
			resReq,
			&deviceReqCopy,
			p.DeviceTopologyRegistry,
			p.Conf.GPUQRMPluginConfig,
			p.Emitter,
			p.MetaServer,
			p.GetState().GetMachineState(),
			qosLevel,
			deviceReq.DeviceName,
			"",
			p.GetDeviceNameToTypeMap(),
		)

		if err != nil || !result.Success {
			general.InfoS("mask failed strategy verification",
				"mask", nodes,
				"error", err,
			)
			return
		}

		// Strategy succeeded, this mask is valid. Update minAffinitySize.
		if mask.Count() < minAffinitySize {
			minAffinitySize = mask.Count()
		}

		// First set all hints' preferred to be false
		hints = append(hints, &pluginapi.TopologyHint{
			Nodes:     nodes,
			Preferred: false,
		})
	})

	// Iterate through all of the hints and set preferred to be true
	setPreferredHints(hints, selectedNUMANodes, minAffinitySize)

	return hints
}

func (p *GPUDevicePlugin) buildAssociatedDeviceHintsResponse(
	req *pluginapi.AssociatedDeviceRequest,
	hints []*pluginapi.TopologyHint,
) *pluginapi.AssociatedDeviceHintsResponse {
	resReq := req.ResourceRequest
	var deviceHints *pluginapi.ListOfTopologyHints
	if hints != nil {
		deviceHints = &pluginapi.ListOfTopologyHints{Hints: hints}
	}
	return &pluginapi.AssociatedDeviceHintsResponse{
		PodUid:         resReq.PodUid,
		PodNamespace:   resReq.PodNamespace,
		PodName:        resReq.PodName,
		ContainerName:  resReq.ContainerName,
		ContainerType:  resReq.ContainerType,
		ContainerIndex: resReq.ContainerIndex,
		PodRole:        resReq.PodRole,
		PodType:        resReq.PodType,
		DeviceName:     req.DeviceName,
		DeviceHints:    deviceHints,
		Labels:         resReq.Labels,
		Annotations:    resReq.Annotations,
	}
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

	gpuAllocationInfo := p.GetState().GetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName)
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
	preAllocateResourceAllocationInfo := p.GetState().GetAllocationInfo(v1.ResourceName(defaultPreAllocateResourceName), resReq.PodUid, resReq.ContainerName)
	// GPU pre-allocate resource should have been allocated at this stage.
	// We anticipate that gpu devices have also been allocated, so we can directly use the allocated devices from the gpu pre-allocate resource state.
	if preAllocateResourceAllocationInfo == nil || preAllocateResourceAllocationInfo.TopologyAwareAllocations == nil {
		// When GPU pre-allocate resource allocation info is nil, invoke the GPU allocate strategy to perform GPU allocation
		general.InfoS("GPU pre-allocate resource allocation info is nil, invoking GPU allocate strategy",
			"podNamespace", resReq.PodNamespace,
			"podName", resReq.PodName,
			"containerName", resReq.ContainerName)

		// Filter out devices that already have allocations in other resources
		filterOccupiedDevicesFromRequest(deviceReq, p.GetState().GetMachineState())

		// Use the strategy framework to allocate GPU devices
		result, err := manager.AllocateDevicesUsingStrategy(
			resReq,
			deviceReq,
			p.DeviceTopologyRegistry,
			p.Conf.GPUQRMPluginConfig,
			p.Emitter,
			p.MetaServer,
			p.GetState().GetMachineState(),
			qosLevel,
			deviceReq.DeviceName,
			"",
			p.GetDeviceNameToTypeMap(),
		)
		if err != nil {
			return nil, fmt.Errorf("GPU allocation using strategy failed: %v", err)
		}

		if !result.Success {
			return nil, fmt.Errorf("GPU allocation failed: %s", result.ErrorMessage)
		}

		allocatedDevices = result.AllocatedDevices
	} else {
		// when GPU pre-allocate resource allocation info exists
		for gpuID := range preAllocateResourceAllocationInfo.TopologyAwareAllocations {
			allocatedDevices = append(allocatedDevices, gpuID)
		}
	}

	gpuTopology, err := p.DeviceTopologyRegistry.GetDeviceTopology(deviceReq.DeviceName)
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, fmt.Errorf("failed to get gpu topology: %w", err)
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
		DeviceName:     deviceReq.DeviceName,
		AllocatedAllocation: state.Allocation{
			Quantity:  float64(len(allocatedDevices)),
			NUMANodes: numaNodes.ToSliceInt(),
		},
	}
	gpuDeviceAllocationInfo.TopologyAwareAllocations = gpuDeviceTopologyAwareAllocations

	p.GetState().SetAllocationInfo(gpuconsts.GPUDeviceType, resReq.PodUid, resReq.ContainerName, gpuDeviceAllocationInfo, false)
	resourceState, err := p.GenerateResourceStateFromPodEntries(gpuconsts.GPUDeviceType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate gpu device state from pod entries: %v", err)
	}
	p.GetState().SetResourceState(gpuconsts.GPUDeviceType, resourceState, true)

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
