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

package gpumemory

import (
	"fmt"
	"math"
	"sort"
	"sync"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/resourceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	manager "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type GPUMemPlugin struct {
	sync.Mutex
	*baseplugin.BasePlugin
}

func NewGPUMemPlugin(base *baseplugin.BasePlugin) resourceplugin.ResourcePlugin {
	return &GPUMemPlugin{
		BasePlugin: base,
	}
}

func (p *GPUMemPlugin) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

func (p *GPUMemPlugin) GetTopologyHints(req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.QosConfig, req, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	_, gpuMemory, err := util.GetQuantityFromResourceRequests(req.ResourceRequests, p.ResourceName(), false)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	gpuCount, gpuNames, err := gpuutil.GetGPUCount(req, p.GPUDeviceNames)
	if err != nil {
		general.Errorf("getGPUCount failed from req %v with error: %v", req, err)
		return nil, fmt.Errorf("getGPUCount failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.Annotations,
		"gpuMemory", gpuMemory,
		"gpuNames", gpuNames.List(),
		"gpuCount", gpuCount)

	p.Lock()
	defer func() {
		if err := p.State.StoreState(); err != nil {
			general.ErrorS(err, "store state failed", "podName", req.PodName, "containerName", req.ContainerName)
		}
		p.Unlock()
		if err != nil {
			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
			}
			_ = p.Emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	var hints map[string]*pluginapi.ListOfTopologyHints
	machineState := p.State.GetMachineState()[consts.ResourceGPUMemory]
	allocationInfo := p.State.GetAllocationInfo(consts.ResourceGPUMemory, req.PodUid, req.ContainerName)

	if allocationInfo != nil {
		hints = state.RegenerateGPUMemoryHints(allocationInfo, false)

		// regenerateHints failed. need to clear container record and re-calculate.
		if hints == nil {
			podEntries := p.State.GetPodEntries(consts.ResourceGPUMemory)
			delete(podEntries[req.PodUid], req.ContainerName)
			if len(podEntries[req.PodUid]) == 0 {
				delete(podEntries, req.PodUid)
			}

			var err error
			machineState, err = state.GenerateResourceStateFromPodEntries(podEntries, p.DeviceTopologyRegistry, consts.ResourceGPUMemory)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	}

	// otherwise, calculate hint for container without allocated memory
	if hints == nil {
		var calculateErr error
		// calculate hint for container without allocated cpus
		hints, calculateErr = p.calculateHints(gpuMemory, gpuCount, machineState, req)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	return util.PackResourceHintsResponse(req, p.ResourceName(), hints)
}

func (p *GPUMemPlugin) calculateHints(
	gpuMemory float64, gpuReq float64, machineState state.AllocationMap, req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	gpuTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
	if err != nil {
		return nil, err
	}

	if !numaTopologyReady {
		return nil, fmt.Errorf("numa topology is not ready")
	}

	perGPUMemory := gpuMemory / gpuReq
	general.Infof("gpuMemory: %f, gpuReq: %f, perGPUMemory: %f", gpuMemory, gpuReq, perGPUMemory)

	numaToAvailableGPUCount := make(map[int]float64)
	numaToMostAllocatedGPUMemory := make(map[int]float64)
	for gpuID, s := range machineState {
		if s == nil {
			continue
		}

		allocated := s.GetQuantityAllocated()
		if allocated+perGPUMemory <= float64(p.GPUMemoryAllocatablePerGPU.Value()) {
			info, ok := gpuTopology.Devices[gpuID]
			if !ok {
				return nil, fmt.Errorf("gpu %s not found in gpuTopology", gpuID)
			}

			for _, numaNode := range info.GetNUMANode() {
				numaToAvailableGPUCount[numaNode] += 1
				numaToMostAllocatedGPUMemory[numaNode] = math.Max(allocated, numaToMostAllocatedGPUMemory[numaNode])
			}
		}
	}

	numaNodes := make([]int, 0, p.MetaServer.NumNUMANodes)
	for numaNode := range p.MetaServer.NUMAToCPUs {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	minNUMAsCountNeeded, _, err := gpuutil.GetNUMANodesCountToFitGPUReq(gpuReq, p.MetaServer.CPUTopology, gpuTopology)
	if err != nil {
		return nil, err
	}

	numaCountPerSocket, err := p.MetaServer.NUMAsPerSocket()
	if err != nil {
		return nil, fmt.Errorf("NUMAsPerSocket failed with error: %v", err)
	}

	numaBound := len(numaNodes)
	if numaBound > machine.LargeNUMAsPoint {
		// [TODO]: to discuss refine minNUMAsCountNeeded+1
		numaBound = minNUMAsCountNeeded + 1
	}

	var availableNumaHints []*pluginapi.TopologyHint
	machine.IterateBitMasks(numaNodes, numaBound, func(mask machine.BitMask) {
		maskCount := mask.Count()
		if maskCount < minNUMAsCountNeeded {
			return
		}

		maskBits := mask.GetBits()
		numaCountNeeded := mask.Count()

		allAvailableGPUsCountInMask := float64(0)
		for _, nodeID := range maskBits {
			allAvailableGPUsCountInMask += numaToAvailableGPUCount[nodeID]
		}

		if allAvailableGPUsCountInMask < gpuReq {
			return
		}

		crossSockets, err := machine.CheckNUMACrossSockets(maskBits, p.MetaServer.CPUTopology)
		if err != nil {
			return
		} else if numaCountNeeded <= numaCountPerSocket && crossSockets {
			return
		}

		preferred := maskCount == minNUMAsCountNeeded
		availableNumaHints = append(availableNumaHints, &pluginapi.TopologyHint{
			Nodes:     machine.MaskToUInt64Array(mask),
			Preferred: preferred,
		})
	})

	// prefer numa nodes with most allocated gpu memory
	p.preferGPUMemoryMostAllocatedHints(availableNumaHints, numaToMostAllocatedGPUMemory)

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need to manage multi-resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("got no available gpu memory hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, gpuutil.ErrNoAvailableGPUMemoryHints
	}

	return map[string]*pluginapi.ListOfTopologyHints{
		p.ResourceName(): {
			Hints: availableNumaHints,
		},
	}, nil
}

func (p *GPUMemPlugin) preferGPUMemoryMostAllocatedHints(
	hints []*pluginapi.TopologyHint, numaToMostAllocatedGPUMemory map[int]float64,
) {
	hintGPUMemoryMostAllocated := make(map[int]float64)
	for index, hint := range hints {
		if !hint.Preferred {
			continue
		}

		gpuMemoryMostAllocated := float64(0)
		for _, nodeID := range hint.Nodes {
			gpuMemoryMostAllocated = math.Max(gpuMemoryMostAllocated, numaToMostAllocatedGPUMemory[int(nodeID)])
		}
		hintGPUMemoryMostAllocated[index] = gpuMemoryMostAllocated
	}

	mostAllocatedHintIndex := -1
	for index, hint := range hints {
		if !hint.Preferred {
			continue
		}

		if mostAllocatedHintIndex == -1 || hintGPUMemoryMostAllocated[index] > hintGPUMemoryMostAllocated[mostAllocatedHintIndex] {
			mostAllocatedHintIndex = index
		}
	}

	if mostAllocatedHintIndex < 0 {
		return
	}

	for index, hint := range hints {
		if !hint.Preferred || mostAllocatedHintIndex == index {
			continue
		}
		hint.Preferred = false
	}
}

func (p *GPUMemPlugin) GetTopologyAwareResources(podUID, containerName string) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	general.InfofV(4, "called")

	allocationInfo := p.State.GetAllocationInfo(consts.ResourceGPUMemory, podUID, containerName)
	if allocationInfo == nil {
		return nil, nil
	}

	topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(allocationInfo.TopologyAwareAllocations))
	for deviceID, alloc := range allocationInfo.TopologyAwareAllocations {
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: alloc.Quantity,
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
	}

	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       podUID,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: containerName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				p.ResourceName(): {
					IsNodeResource:                    true,
					IsScalarResource:                  true,
					AggregatedQuantity:                allocationInfo.AllocatedAllocation.Quantity,
					OriginalAggregatedQuantity:        allocationInfo.AllocatedAllocation.Quantity,
					TopologyAwareQuantityList:         topologyAwareQuantityList,
					OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
				},
			},
		},
	}

	return resp, nil
}

func (p *GPUMemPlugin) GetTopologyAwareAllocatableResources() (*gpuconsts.AllocatableResource, error) {
	general.InfofV(4, "called")

	p.Lock()
	defer p.Unlock()

	machineState := p.State.GetMachineState()[consts.ResourceGPUMemory]

	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity float64
	for deviceID := range machineState {
		aggregatedAllocatableQuantity += float64(p.GPUMemoryAllocatablePerGPU.Value())
		aggregatedCapacityQuantity += float64(p.GPUMemoryAllocatablePerGPU.Value())
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(p.GPUMemoryAllocatablePerGPU.Value()),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(p.GPUMemoryAllocatablePerGPU.Value()),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
	}

	return &gpuconsts.AllocatableResource{
		ResourceName: p.ResourceName(),
		AllocatableTopologyAwareResource: &pluginapi.AllocatableTopologyAwareResource{
			IsNodeResource:                       true,
			IsScalarResource:                     true,
			AggregatedAllocatableQuantity:        aggregatedAllocatableQuantity,
			TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
			AggregatedCapacityQuantity:           aggregatedCapacityQuantity,
			TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
		},
	}, nil
}

func (p *GPUMemPlugin) Allocate(
	resourceReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest,
) (*pluginapi.ResourceAllocationResponse, error) {
	_, exists := resourceReq.Annotations[p.ResourceName()]
	if !exists {
		general.InfoS("No GPU memory annotation detected and no GPU memory requested, returning empty response",
			"podNamespace", resourceReq.PodNamespace,
			"podName", resourceReq.PodName,
			"containerName", resourceReq.ContainerName)
		return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.QosConfig, resourceReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			resourceReq.PodNamespace, resourceReq.PodName, resourceReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	_, gpuMemory, err := util.GetQuantityFromResourceRequests(resourceReq.ResourceRequests, p.ResourceName(), false)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	gpuCount, gpuNames, err := gpuutil.GetGPUCount(resourceReq, p.GPUDeviceNames)
	if err != nil {
		general.Errorf("getGPUCount failed from req %v with error: %v", resourceReq, err)
		return nil, fmt.Errorf("getGPUCount failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", resourceReq.PodNamespace,
		"podName", resourceReq.PodName,
		"containerName", resourceReq.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", resourceReq.Annotations,
		"gpuMemory", gpuMemory,
		"gpuNames", gpuNames.List(),
		"gpuCount", gpuCount)

	p.Lock()
	defer func() {
		if err := p.State.StoreState(); err != nil {
			general.ErrorS(err, "store state failed", "podName", resourceReq.PodName, "containerName", resourceReq.ContainerName)
		}
		p.Unlock()
		if err != nil {
			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
			}
			_ = p.Emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	// currently, not to deal with init containers
	if resourceReq.ContainerType == pluginapi.ContainerType_INIT {
		return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
	} else if resourceReq.ContainerType == pluginapi.ContainerType_SIDECAR {
		// not to deal with sidecars, and return a trivial allocationResult to avoid re-allocating
		return p.PackAllocationResponse(resourceReq, &state.AllocationInfo{}, nil, p.ResourceName())
	}

	allocationInfo := p.State.GetAllocationInfo(consts.ResourceGPUMemory, resourceReq.PodUid, resourceReq.ContainerName)
	if allocationInfo != nil {
		resp, packErr := p.PackAllocationResponse(resourceReq, allocationInfo, nil, p.ResourceName())
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				resourceReq.PodNamespace, resourceReq.PodName, resourceReq.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %w", packErr)
		}
		return resp, nil
	}

	// Get GPU topology
	gpuTopology, numaTopologyReady, err := p.DeviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, fmt.Errorf("failed to get gpu topology: %v", err)
	}

	if !numaTopologyReady {
		general.Warningf("numa topology is not ready")
		return nil, fmt.Errorf("numa topology is not ready")
	}

	// Use the strategy framework to allocate GPU memory
	// TODO: What happens when deviceReq is nil?
	result, err := manager.AllocateGPUUsingStrategy(
		resourceReq,
		deviceReq,
		gpuTopology,
		p.GPUQRMPluginConfig,
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

	// get hint nodes from request
	hintNodes, err := machine.NewCPUSetUint64(resourceReq.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, fmt.Errorf("failed to get hint nodes: %w", err)
	}

	newAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resourceReq, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.Allocation{
			Quantity:  gpuMemory,
			NUMANodes: hintNodes.ToSliceInt(),
		},
		TopologyAwareAllocations: make(map[string]state.Allocation),
	}

	gpuMemoryPerGPU := gpuMemory / gpuCount
	for _, deviceID := range result.AllocatedDevices {
		info, ok := gpuTopology.Devices[deviceID]
		if !ok {
			return nil, fmt.Errorf("failed to get gpu info for device: %s", deviceID)
		}

		newAllocation.TopologyAwareAllocations[deviceID] = state.Allocation{
			Quantity:  gpuMemoryPerGPU,
			NUMANodes: info.NumaNodes,
		}
	}

	// Set allocation info in state
	p.State.SetAllocationInfo(consts.ResourceGPUMemory, resourceReq.PodUid, resourceReq.ContainerName, newAllocation, false)

	machineState, stateErr := state.GenerateMachineStateFromPodEntries(p.State.GetPodResourceEntries(), p.DeviceTopologyRegistry)
	if stateErr != nil {
		general.ErrorS(stateErr, "GenerateMachineStateFromPodEntries failed",
			"podNamespace", resourceReq.PodNamespace,
			"podName", resourceReq.PodName,
			"containerName", resourceReq.ContainerName,
			"gpuMemory", gpuMemory)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", stateErr)
	}

	// update state cache
	p.State.SetMachineState(machineState, true)

	return p.PackAllocationResponse(resourceReq, newAllocation, nil, p.ResourceName())
}
