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

	// GPUDeviceType is the key used for state management in the QRM framework,
	// while GPUDeviceNames are the actual resource names used to fetch the device topologies.
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(gpuconsts.GPUDeviceType,
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, 1))
	base.RegisterDeviceNames(base.Conf.GPUDeviceNames, gpuconsts.GPUDeviceType)

	return &GPUDevicePlugin{
		BasePlugin:  base,
		deviceNames: base.Conf.GPUDeviceNames,
	}
}

func (p *GPUDevicePlugin) DefaultPreAllocateResourceName() string {
	return defaultPreAllocateResourceName
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

	// Find the target device request
	var targetDeviceReq *pluginapi.DeviceRequest
	for _, deviceRequest := range req.DeviceRequest {
		if deviceRequest.DeviceName == req.DeviceName {
			targetDeviceReq = deviceRequest
			break
		}
	}

	if targetDeviceReq == nil {
		return nil, fmt.Errorf("no target device plugin found for target device %s", req.DeviceName)
	}

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
		// 3. Fallback to generating hints from the available devices and the GPU topology.
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

	// Gather all NUMA nodes that have GPUs
	numaNodesSet := sets.NewInt()
	for _, dev := range gpuTopology.Devices {
		numaNodesSet.Insert(dev.NumaNodes...)
	}
	numaNodes := numaNodesSet.List()

	// minAffinitySize tracks the minimum mask size that satisfies the Strategy
	minAffinitySize := len(numaNodes)
	var hints []*pluginapi.TopologyHint

	// Iterate through all combinations of NUMA Nodes and build hints from them.
	machine.IterateBitMasks(numaNodes, len(numaNodes), func(mask machine.BitMask) {
		// Fast Path: Check to see if all of the reusable devices are part of the bitmask.
		numMatching := 0
		for d := range reusable {
			dev, ok := gpuTopology.Devices[d]
			if !ok || len(dev.NumaNodes) == 0 {
				continue
			}
			if !mask.AnySet(dev.NumaNodes) {
				return
			}
			numMatching++
		}

		// Fast Path: Check to see if enough available devices remain on the
		// current NUMA node combination to satisfy the device request.
		for d := range available {
			dev, ok := gpuTopology.Devices[d]
			if !ok || len(dev.NumaNodes) == 0 {
				continue
			}
			if mask.AnySet(dev.NumaNodes) {
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

		result, err := manager.AllocateGPUUsingStrategy(
			resReq,
			&deviceReqCopy,
			gpuTopology,
			p.Conf.GPUQRMPluginConfig,
			p.Emitter,
			p.MetaServer,
			p.GetState().GetMachineState(),
			qosLevel,
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

		// Add it to the list of hints. We set all hint preferences to 'false' on the first pass through.
		hints = append(hints, &pluginapi.TopologyHint{
			Nodes:     nodes,
			Preferred: false,
		})
	})

	// Loop back through all hints and update the 'Preferred' field based on
	// counting the number of bits sets in the affinity mask and comparing it
	// to the minAffinity. Only those with an equal number of bits set will be
	// considered preferred.
	for i := range hints {
		if len(hints[i].Nodes) == minAffinitySize {
			hints[i].Preferred = true
		}
	}

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

		// Get GPU topology using the specific device resource name
		gpuTopology, err := p.DeviceTopologyRegistry.GetDeviceTopology(deviceReq.DeviceName)
		if err != nil {
			general.Warningf("failed to get gpu topology: %v", err)
			return nil, fmt.Errorf("failed to get gpu topology: %w", err)
		}

		// Use the strategy framework to allocate GPU devices
		result, err := manager.AllocateGPUUsingStrategy(
			resReq,
			deviceReq,
			gpuTopology,
			p.Conf.GPUQRMPluginConfig,
			p.Emitter,
			p.MetaServer,
			p.GetState().GetMachineState(),
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
