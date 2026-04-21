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
	"context"
	"fmt"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	deviceplugin "k8s.io/kubelet/pkg/apis/deviceplugin/v1alpha"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/resourceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/manager"
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

// NewGPUMemPlugin creates and returns a new GPU memory resource plugin
func NewGPUMemPlugin(base *baseplugin.BasePlugin) resourceplugin.ResourcePlugin {
	// string(consts.ResourceGPUMemory) is the key used for state management in the QRM framework,
	// while GPUDeviceNames are the actual resource names used to fetch the device topologies.
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory),
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, float64(base.Conf.GPUMemoryAllocatablePerGPU.Value())))
	return &GPUMemPlugin{
		BasePlugin: base,
	}
}

// ResourceName returns the name of the main resource this plugin manages
func (p *GPUMemPlugin) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

// GetTopologyHints returns topology hints for a given resource request
func (p *GPUMemPlugin) GetTopologyHints(ctx context.Context, req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, req, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	// Get all resource quantities from the request
	_, floatQuantity, err := util.GetQuantityMapFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("GetQuantityMapFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.Annotations,
		"resourceQuantities", floatQuantity)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
			}
			_ = p.Emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	var hints map[string]*pluginapi.ListOfTopologyHints
	gpuState := p.GetState().GetMachineState()[gpuconsts.GPUDeviceType]
	resourceMachineState := p.GetState().GetMachineState()

	// Collect plugin's resources first
	pluginResources := p.getAllPluginResources()
	// Collect allocationInfo only for plugin's resources in the request
	allocationInfoMap := make(map[v1.ResourceName]*state.AllocationInfo)
	// Also, collect which of plugin's resources are requested
	requestedPluginResources := make(map[v1.ResourceName]bool)
	for _, resName := range pluginResources {
		if _, ok := req.ResourceRequests[string(resName)]; ok {
			requestedPluginResources[resName] = true
			ai := p.GetState().GetAllocationInfo(resName, req.PodUid, req.ContainerName)
			if ai != nil {
				allocationInfoMap[resName] = ai
			}
		}
	}

	if len(allocationInfoMap) > 0 {
		// Check if all of requested plugin resources have allocation info
		if len(allocationInfoMap) != len(requestedPluginResources) {
			return nil, fmt.Errorf("allocation info length mismatch: expected %d, got %d", len(requestedPluginResources), len(allocationInfoMap))
		}

		// Regenerate hints for all resources
		hints, err = regenerateGPUMemoryHints(allocationInfoMap, false)
		if err != nil {
			return nil, err
		}

		// If regenerateHints failed for any resource, clear all container records and re-calculate
		if hints == nil {
			for resName := range allocationInfoMap {
				podEntries := p.GetState().GetPodEntries(resName)
				delete(podEntries[req.PodUid], req.ContainerName)
				if len(podEntries[req.PodUid]) == 0 {
					delete(podEntries, req.PodUid)
				}

				var generateErr error
				_, generateErr = p.GenerateResourceStateFromPodEntries(string(resName), podEntries)
				if generateErr != nil {
					general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed for resource %s with error: %v",
						req.PodNamespace, req.PodName, req.ContainerName, resName, generateErr)
					return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed for resource %s with error: %v", resName, generateErr)
				}
			}
			// Refresh resourceMachineState after regenerating
			resourceMachineState = p.GetState().GetMachineState()
		}
	}

	gpuCount, gpuNames, err := gpuutil.GetGPUCount(req, p.Conf.GPUDeviceNames)
	if err != nil {
		general.Errorf("getGPUCount failed from req %v with error: %v", req, err)
		return nil, fmt.Errorf("getGPUCount failed with error: %v", err)
	}

	general.Infof("gpuCount: %f, gpuNames: %v", gpuCount, gpuNames.List())

	// otherwise, calculate hint for container without allocated memory
	if hints == nil {
		var calculateErr error
		// calculate hint for container without allocated cpus
		hints, calculateErr = p.calculateHints(floatQuantity, gpuCount, gpuNames, resourceMachineState, gpuState, req)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	// Pack the hints response with all resources in the hints map
	return util.PackResourceHintsResponse(req, p.ResourceName(), hints)
}

// getGPUTopology returns the appropriate GPU device topology based on the requested device names
func (p *GPUMemPlugin) getGPUTopology(gpuNames sets.String) (*machine.DeviceTopology, error) {
	switch {
	case gpuNames.Len() == 1:
		deviceName := gpuNames.List()[0]
		return p.DeviceTopologyRegistry.GetDeviceTopology(deviceName)
	case gpuNames.Len() == 0 && len(p.Conf.GPUDeviceNames) > 0:
		topologiesMap, err := p.DeviceTopologyRegistry.GetDeviceTopologies(p.Conf.GPUDeviceNames)
		if err != nil {
			return nil, err
		}
		topology := machine.PickLatestDeviceTopology(topologiesMap)
		if topology == nil {
			return nil, fmt.Errorf("no latest device topology available")
		}
		return topology, nil
	default:
		return nil, fmt.Errorf("pod requests multiple or no gpu types: %v", gpuNames.List())
	}
}

// calculateHints calculates topology hints for the given resource request
func (p *GPUMemPlugin) calculateHints(
	resourceQuantities map[v1.ResourceName]float64, gpuReq float64, gpuNames sets.String,
	resourceMachineState state.AllocationResourcesMap, gpuState state.AllocationMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	gpuTopology, err := p.getGPUTopology(gpuNames)
	if err != nil {
		return nil, err
	}

	// Check if milligpu is requested and handle gpuReq
	if hasMilligpuRequest(req) && gpuReq != 0 {
		return nil, fmt.Errorf("milligpu requested but gpuReq is not 0")
	}

	general.Infof("resourceQuantities: %v, gpuReq: %f", resourceQuantities, gpuReq)

	spreading := p.Conf.FractionalGPUPrefersSpreading

	numaToAvailableGPUCount := make(map[int]float64)
	resourceToNumaToAllocatedGPUResource := make(map[v1.ResourceName]map[int]float64)
	pluginResources := p.getAllPluginResources()

	// Precompute requested plugin resources once instead of checking each time
	requestedResNames := make([]v1.ResourceName, 0)
	for _, resName := range pluginResources {
		if _, requested := resourceQuantities[resName]; requested {
			requestedResNames = append(requestedResNames, resName)
		}
	}

	// Collect all unique numa nodes and initialize resourceToNumaToAllocatedGPUResource
	allNumaNodes := sets.NewInt()
	for _, info := range gpuTopology.Devices {
		if info.Health != deviceplugin.Healthy {
			continue
		}
		for _, numaNode := range info.GetNUMANodes() {
			allNumaNodes.Insert(numaNode)
		}
	}
	for _, resName := range requestedResNames {
		resourceToNumaToAllocatedGPUResource[resName] = make(map[int]float64)
		for _, numaNode := range allNumaNodes.List() {
			resourceToNumaToAllocatedGPUResource[resName][numaNode] = 0
		}
	}

	// Single loop over GPUs to: sum allocations, check available GPUs
	for gpuID, info := range gpuTopology.Devices {
		if info.Health != deviceplugin.Healthy {
			continue
		}

		// Step 1: sum allocations for each resource on this GPU's numa nodes
		for _, resName := range requestedResNames {
			var allocated float64
			if resMachineState, ok := resourceMachineState[resName]; ok {
				allocated = resMachineState[gpuID].GetQuantityAllocated()
			}
			for _, numaNode := range info.GetNUMANodes() {
				resourceToNumaToAllocatedGPUResource[resName][numaNode] += allocated
			}
		}

		// Step 2: check if this GPU is available
		gpuAllocated := gpuState[gpuID].GetQuantityAllocatedWithFilter(func(ai *state.AllocationInfo) bool {
			return gpuNames.Has(ai.DeviceName)
		})
		if gpuAllocated > 0 {
			continue // skip already allocated
		}

		// Step 3: check if all requested resources fit on this GPU
		deviceFitsAllResources := true
		for _, resName := range requestedResNames {
			reqQuantity := resourceQuantities[resName]
			allocatablePerGPU := p.getResourceAllocatableQuantity(string(resName))
			if allocatablePerGPU.IsZero() {
				deviceFitsAllResources = false
				break
			}
			perGPUReqQuantity := reqQuantity
			if gpuReq > 0 {
				perGPUReqQuantity = reqQuantity / gpuReq
			}
			if perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				return nil, fmt.Errorf("per GPU requested quantity %f for resource %s exceeds allocatable %d per GPU", perGPUReqQuantity, resName, allocatablePerGPU.Value())
			}
			var allocated float64
			if resMachineState, ok := resourceMachineState[resName]; ok {
				allocated = resMachineState[gpuID].GetQuantityAllocated()
			}
			if allocated+perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				deviceFitsAllResources = false
				break
			}
		}

		if deviceFitsAllResources {
			for _, numaNode := range info.GetNUMANodes() {
				numaToAvailableGPUCount[numaNode] += 1
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

	// prefer numa nodes with appropriate allocated gpu resources
	p.preferGPUAllocatedHints(availableNumaHints, resourceToNumaToAllocatedGPUResource, spreading)

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need to manage multi-resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("got no available gpu memory hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, gpuutil.ErrNoAvailableGPUMemoryHints
	}

	// Create hints map with all requested resources that are plugin's resources
	hints := make(map[string]*pluginapi.ListOfTopologyHints)
	for _, resName := range pluginResources {
		if _, requested := resourceQuantities[resName]; requested {
			hints[string(resName)] = &pluginapi.ListOfTopologyHints{
				Hints: availableNumaHints,
			}
		}
	}

	return hints, nil
}

// preferGPUAllocatedHints updates the preferred topology hints to prefer hints that include numa nodes
// that are the most (for packing) or least (for spreading) allocated across all resources. If all resources
// agree on a single best numa node, only hints including that node are marked as preferred; otherwise, no
// changes are made to hint preferences.
func (p *GPUMemPlugin) preferGPUAllocatedHints(
	hints []*pluginapi.TopologyHint, resourceToNumaToAllocatedGPUResource map[v1.ResourceName]map[int]float64, spreading bool,
) {
	if len(hints) == 0 {
		return
	}

	hasAllocations := false
outer:
	for _, numaToAllocated := range resourceToNumaToAllocatedGPUResource {
		for _, allocated := range numaToAllocated {
			if allocated > 0 {
				hasAllocations = true
				break outer
			}
		}
	}
	if !hasAllocations {
		return
	}

	resourceToBestNuma := make(map[v1.ResourceName]int)
	for resName, numaToAllocated := range resourceToNumaToAllocatedGPUResource {
		if len(numaToAllocated) == 0 {
			continue
		}

		// Get first numa node as initial best
		var bestNuma int
		var bestValue float64
		for numa, allocated := range numaToAllocated {
			bestNuma = numa
			bestValue = allocated
			break
		}

		// Iterate rest of numa nodes to find best
		for numa, allocated := range numaToAllocated {
			if spreading {
				if allocated < bestValue {
					bestNuma = numa
					bestValue = allocated
				}
			} else {
				if allocated > bestValue {
					bestNuma = numa
					bestValue = allocated
				}
			}
		}

		resourceToBestNuma[resName] = bestNuma
	}

	if len(resourceToBestNuma) == 0 {
		return
	}

	// Check if all resources have the same best numa node
	var commonBestNuma int
	for _, numa := range resourceToBestNuma {
		commonBestNuma = numa
		break
	}

	for _, numa := range resourceToBestNuma {
		if numa != commonBestNuma {
			return
		}
	}

	// All resources agree on the same best numa node
	for _, hint := range hints {
		hint.Preferred = false
		for _, nodeID := range hint.Nodes {
			if int(nodeID) == commonBestNuma {
				hint.Preferred = true
				break
			}
		}
	}
}

// generateTopologyAwareQuantities generates topology‑aware quantities for a device
func generateTopologyAwareQuantities(deviceID string, numaNodes []int, quantity float64) []*pluginapi.TopologyAwareQuantity {
	if len(numaNodes) > 0 {
		perNuma := quantity / float64(len(numaNodes))
		list := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))
		for _, nodeID := range numaNodes {
			if nodeID < 0 {
				nodeID = 0
			}
			list = append(list, &pluginapi.TopologyAwareQuantity{
				Node:          uint64(nodeID),
				ResourceValue: perNuma,
				Name:          deviceID,
				Type:          string(v1alpha1.TopologyTypeGPU),
				Annotations: map[string]string{
					consts.ResourceAnnotationKeyResourceIdentifier: "",
				},
			})
		}
		return list
	}
	return []*pluginapi.TopologyAwareQuantity{
		{
			ResourceValue: quantity,
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		},
	}
}

// GetTopologyAwareResources returns topology-aware resources for a given pod/container
func (p *GPUMemPlugin) GetTopologyAwareResources(ctx context.Context, podUID, containerName string) (map[string]*pluginapi.GetTopologyAwareResourcesResponse, error) {
	general.InfofV(4, "called")

	// Get all resources: main + extra
	pluginResources := p.getAllPluginResources()
	resources := make([]string, 0, len(pluginResources))
	for _, r := range pluginResources {
		resources = append(resources, string(r))
	}

	result := make(map[string]*pluginapi.GetTopologyAwareResourcesResponse)

	for _, resourceName := range resources {
		// Get allocation info for this resource
		allocationInfo := p.GetState().GetAllocationInfo(v1.ResourceName(resourceName), podUID, containerName)

		if allocationInfo == nil {
			continue
		}

		topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(allocationInfo.TopologyAwareAllocations))
		for deviceID, alloc := range allocationInfo.TopologyAwareAllocations {
			list := generateTopologyAwareQuantities(deviceID, alloc.NUMANodes, alloc.Quantity)
			topologyAwareQuantityList = append(topologyAwareQuantityList, list...)
		}

		// Create response for this resource
		result[resourceName] = &pluginapi.GetTopologyAwareResourcesResponse{
			PodUid:       podUID,
			PodName:      allocationInfo.PodName,
			PodNamespace: allocationInfo.PodNamespace,
			ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
				ContainerName: containerName,
				AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
					resourceName: {
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
	}

	// If no resources were added, return nil
	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

// getResourceAllocatableQuantity returns the allocatable quantity for a given resource name
func (p *GPUMemPlugin) getResourceAllocatableQuantity(resourceName string) resource.Quantity {
	switch resourceName {
	case string(consts.ResourceGPUMemory):
		return p.Conf.GPUMemoryAllocatablePerGPU
	case string(consts.ResourceMilliGPU):
		return p.Conf.MilliGPUAllocatablePerGPU
	default:
		return resource.Quantity{}
	}
}

// GetTopologyAwareAllocatableResources returns topology-aware allocatable resources
func (p *GPUMemPlugin) GetTopologyAwareAllocatableResources(ctx context.Context) (map[string]*pluginapi.AllocatableTopologyAwareResource, error) {
	general.InfofV(4, "called")

	p.Lock()
	defer p.Unlock()

	gpuTopology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	if err != nil {
		return nil, err
	}

	// Get all resources to return: main resource + extra resources
	pluginResources := p.getAllPluginResources()
	resources := make([]string, 0, len(pluginResources))
	for _, r := range pluginResources {
		resources = append(resources, string(r))
	}

	// Generate allocatable resources for each resource
	allocatableResources := make(map[string]*pluginapi.AllocatableTopologyAwareResource)
	for _, resourceName := range resources {
		allocatableQuantity := p.getResourceAllocatableQuantity(resourceName)
		if allocatableQuantity.IsZero() {
			continue
		}

		topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(gpuTopology.Devices))
		topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(gpuTopology.Devices))
		var aggregatedAllocatableQuantity, aggregatedCapacityQuantity float64

		allocPerGPU := float64(allocatableQuantity.Value())
		for deviceID, deviceInfo := range gpuTopology.Devices {
			aggregatedAllocatableQuantity += allocPerGPU
			aggregatedCapacityQuantity += allocPerGPU
			list := generateTopologyAwareQuantities(deviceID, deviceInfo.NumaNodes, allocPerGPU)
			topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, list...)
			topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, list...)
		}

		allocatableResources[resourceName] = &pluginapi.AllocatableTopologyAwareResource{
			IsNodeResource:                       true,
			IsScalarResource:                     true,
			AggregatedAllocatableQuantity:        aggregatedAllocatableQuantity,
			TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
			AggregatedCapacityQuantity:           aggregatedCapacityQuantity,
			TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
		}
	}

	return allocatableResources, nil
}

func (p *GPUMemPlugin) GetExtraResources() []string {
	return []string{string(consts.ResourceMilliGPU)}
}

// getAllPluginResources returns all resources managed by this plugin
func (p *GPUMemPlugin) getAllPluginResources() []v1.ResourceName {
	resources := []v1.ResourceName{v1.ResourceName(p.ResourceName())}
	for _, extra := range p.GetExtraResources() {
		resources = append(resources, v1.ResourceName(extra))
	}
	return resources
}

// hasMilligpuRequest checks if the given resource request contains a request for ResourceMilliGPU
func hasMilligpuRequest(resourceReq *pluginapi.ResourceRequest) bool {
	_, exists := resourceReq.ResourceRequests[string(consts.ResourceMilliGPU)]
	return exists
}

// RegisterExtraResourceStateGenerator registers a state generator for the given extra resource
func (p *GPUMemPlugin) RegisterExtraResourceStateGenerator(extraResource string) error {
	// Only register extra resources that this plugin handles
	if extraResource == string(consts.ResourceMilliGPU) {
		p.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(extraResource,
			state.NewGenericDefaultResourceStateGenerator(p.Conf.GPUDeviceNames, p.DeviceTopologyRegistry, float64(p.Conf.MilliGPUAllocatablePerGPU.Value())))
		return nil
	}
	return fmt.Errorf("extra resource %s not supported by %s plugin", extraResource, p.ResourceName())
}

// Allocate allocates GPU memory resources to a container based on the given request
func (p *GPUMemPlugin) Allocate(
	ctx context.Context, resourceReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest,
) (*pluginapi.ResourceAllocationResponse, error) {
	quantity, exists := resourceReq.ResourceRequests[p.ResourceName()]
	if !exists || quantity == 0 {
		general.InfoS("No GPU memory annotation detected and no GPU memory requested, returning empty response",
			"podNamespace", resourceReq.PodNamespace,
			"podName", resourceReq.PodName,
			"containerName", resourceReq.ContainerName)
		return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, resourceReq, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			resourceReq.PodNamespace, resourceReq.PodName, resourceReq.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	_, gpuMemory, err := util.GetQuantityFromResourceRequests(resourceReq.ResourceRequests, p.ResourceName(), nil)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", resourceReq.PodNamespace,
		"podName", resourceReq.PodName,
		"containerName", resourceReq.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", resourceReq.Annotations,
		"gpuMemory", gpuMemory,
		"deviceReq", deviceReq.String())

	p.Lock()
	defer func() {
		if err := p.GetState().StoreState(); err != nil {
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
		allocationInfoMap := map[string]*state.AllocationInfo{
			p.ResourceName(): {},
		}
		return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName())
	}

	allocationInfo := p.GetState().GetAllocationInfo(consts.ResourceGPUMemory, resourceReq.PodUid, resourceReq.ContainerName)
	if allocationInfo != nil {
		allocationInfoMap := map[string]*state.AllocationInfo{
			p.ResourceName(): allocationInfo,
		}
		resp, packErr := p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName())
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				resourceReq.PodNamespace, resourceReq.PodName, resourceReq.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %w", packErr)
		}
		return resp, nil
	}

	if deviceReq == nil {
		if !hasMilligpuRequest(resourceReq) {
			general.InfoS("Nil device request, returning empty response",
				"podNamespace", resourceReq.PodNamespace,
				"podName", resourceReq.PodName,
				"containerName", resourceReq.ContainerName)
			// if deviceReq is nil and no milligpu requested, return empty response to re-allocate after device allocation
			return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
		}
		// deviceReq is nil but we have milligpu requested, use allocateWithoutGPUDevices
		return p.allocateWithoutGPUDevices(ctx, resourceReq, gpuMemory, qosLevel)
	}

	// Check if we have both device request and milligpu request
	if hasMilligpuRequest(resourceReq) {
		return nil, fmt.Errorf("milligpu requested together with GPU device request")
	}

	general.Infof("deviceReq: %v", deviceReq.String())

	// Use allocateWithGPUDevices for when we have deviceReq
	return p.allocateWithGPUDevices(ctx, resourceReq, deviceReq, gpuMemory, qosLevel)
}

// allocateWithGPUDevices allocates GPU memory resources when physical GPU devices are requested
func (p *GPUMemPlugin) allocateWithGPUDevices(
	ctx context.Context,
	resourceReq *pluginapi.ResourceRequest,
	deviceReq *pluginapi.DeviceRequest,
	gpuMemory float64,
	qosLevel string,
) (*pluginapi.ResourceAllocationResponse, error) {
	// Get GPU topology using the specific device resource name
	gpuTopology, err := p.DeviceTopologyRegistry.GetDeviceTopology(deviceReq.DeviceName)
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, fmt.Errorf("failed to get gpu topology: %v", err)
	}

	// Use the strategy framework to allocate GPU memory
	result, err := manager.AllocateGPUUsingStrategy(
		resourceReq,
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

	gpuMemoryPerGPU := gpuMemory / float64(deviceReq.DeviceRequest)
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
	p.GetState().SetAllocationInfo(consts.ResourceGPUMemory, resourceReq.PodUid, resourceReq.ContainerName, newAllocation, false)

	machineState, stateErr := p.GenerateResourceStateFromPodEntries(string(consts.ResourceGPUMemory), nil)
	if stateErr != nil {
		general.ErrorS(stateErr, "GenerateResourceStateFromPodEntries failed",
			"podNamespace", resourceReq.PodNamespace,
			"podName", resourceReq.PodName,
			"containerName", resourceReq.ContainerName,
			"gpuMemory", gpuMemory)
		return nil, fmt.Errorf("GenerateResourceStateFromPodEntries failed with error: %v", stateErr)
	}

	// update state cache
	p.GetState().SetResourceState(consts.ResourceGPUMemory, machineState, true)

	allocationInfoMap := map[string]*state.AllocationInfo{
		p.ResourceName(): newAllocation,
	}
	return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName())
}

// allocateWithoutGPUDevices allocates GPU memory resources without a physical GPU device request (TODO)
func (p *GPUMemPlugin) allocateWithoutGPUDevices(
	ctx context.Context,
	resourceReq *pluginapi.ResourceRequest,
	gpuMemory float64,
	qosLevel string,
) (*pluginapi.ResourceAllocationResponse, error) {
	// Step 1: Extract required info
	pluginResources := p.getAllPluginResources()
	_, resourceQuantities, err := util.GetQuantityMapFromResourceReq(resourceReq)
	if err != nil {
		return nil, err
	}

	_, gpuNames, err := gpuutil.GetGPUCount(resourceReq, p.Conf.GPUDeviceNames)
	if err != nil {
		return nil, err
	}

	requestedResNames := make([]v1.ResourceName, 0)
	for _, resName := range pluginResources {
		if _, ok := resourceQuantities[resName]; ok {
			requestedResNames = append(requestedResNames, resName)
		}
	}

	resourceMachineState := p.GetState().GetMachineState()
	gpuTopology, err := p.getGPUTopology(gpuNames)
	if err != nil {
		return nil, err
	}

	// Step 2: Iterate over each GPU to check health, allocation, and resource fit
	// Create resource-to-unallocated-to-GPUs map
	resourceToUnallocatedToGPUs := make(map[v1.ResourceName]map[float64][]string)
	for _, resName := range requestedResNames {
		resourceToUnallocatedToGPUs[resName] = make(map[float64][]string)
	}

	for gpuID, info := range gpuTopology.Devices {
		if info.Health != deviceplugin.Healthy {
			continue
		}

		// Check if the whole GPU is already allocated
		gpuState := resourceMachineState[v1.ResourceName(p.ResourceName())]
		gpuAllocated := gpuState[gpuID].GetQuantityAllocatedWithFilter(func(ai *state.AllocationInfo) bool {
			return gpuNames.Has(ai.DeviceName)
		})
		if gpuAllocated > 0 {
			continue
		}

		// Check if all requested resources fit
		deviceFitsAllResources := true
		resUnallocatedMap := make(map[v1.ResourceName]float64)
		for _, resName := range requestedResNames {
			reqQuantity := resourceQuantities[resName]
			allocatablePerGPU := p.getResourceAllocatableQuantity(string(resName))
			if allocatablePerGPU.IsZero() {
				deviceFitsAllResources = false
				break
			}
			perGPUReqQuantity := reqQuantity
			if perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				return nil, fmt.Errorf("per GPU requested quantity %f for resource %s exceeds allocatable %d per GPU", perGPUReqQuantity, resName, allocatablePerGPU.Value())
			}
			var allocated float64
			if resMachineState, ok := resourceMachineState[resName]; ok {
				allocated = resMachineState[gpuID].GetQuantityAllocated()
			}
			if allocated+perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				deviceFitsAllResources = false
				break
			}
			allocatableQuantity := p.getResourceAllocatableQuantity(string(resName))
			unallocated := float64(allocatableQuantity.Value()) - allocated
			resUnallocatedMap[resName] = unallocated
		}

		if deviceFitsAllResources {
			// Populate resourceToUnallocatedToGPUs map
			for _, resName := range requestedResNames {
				unallocated := resUnallocatedMap[resName]
				resourceToUnallocatedToGPUs[resName][unallocated] = append(resourceToUnallocatedToGPUs[resName][unallocated], gpuID)
			}
		}
	}

	// Step 4: Implement allocation logic
	spreading := p.Conf.FractionalGPUPrefersSpreading

	// Get best GPU IDs for each resource based on spreading policy
	resourceToBestGPUs := make(map[v1.ResourceName][]string)
	for resName, unallocatedToGPUs := range resourceToUnallocatedToGPUs {
		if len(unallocatedToGPUs) == 0 {
			continue
		}

		// Collect all unallocated quantities and sort them
		unallocatedQuantities := make([]float64, 0, len(unallocatedToGPUs))
		for unallocated := range unallocatedToGPUs {
			unallocatedQuantities = append(unallocatedQuantities, unallocated)
		}

		// Sort based on spreading policy
		if spreading {
			// Spreading: sort descending to get most unallocated
			sort.Sort(sort.Reverse(sort.Float64Slice(unallocatedQuantities)))
		} else {
			// Packing: sort ascending to get least unallocated
			sort.Float64s(unallocatedQuantities)
		}

		// Get best GPU IDs (first entry after sorting)
		if len(unallocatedQuantities) > 0 {
			bestUnallocated := unallocatedQuantities[0]
			resourceToBestGPUs[resName] = unallocatedToGPUs[bestUnallocated]
		}
	}

	selectedGPUID := findBestCommonGPU(resourceToBestGPUs)

	// If no GPU ID selected, return empty response
	if selectedGPUID == "" {
		general.InfoS("allocateWithoutGPUDevices: No suitable GPU found, returning empty response")
		return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
	}

	// Get GPU info for selected GPU
	gpuInfo, ok := gpuTopology.Devices[selectedGPUID]
	if !ok {
		general.Errorf("allocateWithoutGPUDevices: Selected GPU %s not found in topology", selectedGPUID)
		return util.CreateEmptyAllocationResponse(resourceReq, p.ResourceName()), nil
	}

	// Update state for each requested resource
	allocationInfoMap := make(map[string]*state.AllocationInfo)
	for _, resName := range requestedResNames {
		// Create a separate allocation for each resource with the correct quantity
		resourceAllocation := &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resourceReq, commonstate.EmptyOwnerPoolName, qosLevel),
			AllocatedAllocation: state.Allocation{
				Quantity:  resourceQuantities[resName],
				NUMANodes: gpuInfo.NumaNodes,
			},
			TopologyAwareAllocations: map[string]state.Allocation{
				selectedGPUID: {
					Quantity:  resourceQuantities[resName],
					NUMANodes: gpuInfo.NumaNodes,
				},
			},
		}
		p.GetState().SetAllocationInfo(resName, resourceReq.PodUid, resourceReq.ContainerName, resourceAllocation, false)
		allocationInfoMap[string(resName)] = resourceAllocation

		// Generate and update resource state for each requested resource
		machineState, stateErr := p.GenerateResourceStateFromPodEntries(string(resName), nil)
		if stateErr != nil {
			general.ErrorS(stateErr, "GenerateResourceStateFromPodEntries failed",
				"podNamespace", resourceReq.PodNamespace,
				"podName", resourceReq.PodName,
				"containerName", resourceReq.ContainerName,
				"resource", resName)
			// Continue even if one resource fails to generate state
			continue
		}

		// Update state cache
		p.GetState().SetResourceState(resName, machineState, true)
	}

	general.InfoS("allocateWithoutGPUDevices: Allocation successful",
		"selectedGPU", selectedGPUID,
		"podName", resourceReq.PodName,
		"containerName", resourceReq.ContainerName)

	return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName())
}

// regenerateGPUMemoryHints regenerates hints for container that'd already been allocated resources,
// and regenerateHints will assemble hints based on already-existed AllocationInfo for each resource,
// without any calculation logics at all
func regenerateGPUMemoryHints(
	allocationInfoMap map[v1.ResourceName]*state.AllocationInfo, regenerate bool,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	if len(allocationInfoMap) == 0 {
		general.Errorf("RegenerateHints got empty allocationInfoMap")
		return nil, nil
	}

	hints := map[string]*pluginapi.ListOfTopologyHints{}
	if regenerate {
		// Use first allocationInfo for logging
		var firstAI *state.AllocationInfo
		for _, ai := range allocationInfoMap {
			firstAI = ai
			break
		}
		if firstAI != nil {
			general.ErrorS(nil, "need to regenerate hints",
				"podNamespace", firstAI.PodNamespace,
				"podName", firstAI.PodName,
				"podUID", firstAI.PodUid,
				"containerName", firstAI.ContainerName)
		}
		return nil, nil
	}

	// Generate hints for each resource in the map
	for resName, allocationInfo := range allocationInfoMap {
		if allocationInfo == nil {
			general.Errorf("RegenerateHints got nil allocationInfo for resource %s", resName)
			continue
		}

		allocatedNumaNodes := machine.NewCPUSet(allocationInfo.AllocatedAllocation.NUMANodes...)

		general.InfoS("regenerating machineInfo hints, resource was already allocated to pod",
			"resource", resName,
			"podNamespace", allocationInfo.PodNamespace,
			"podName", allocationInfo.PodName,
			"containerName", allocationInfo.ContainerName,
			"hint", allocatedNumaNodes)
		hints[string(resName)] = &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     allocatedNumaNodes.ToSliceUInt64(),
					Preferred: true,
				},
			},
		}
	}

	return hints, nil
}

// findBestCommonGPU selects a single GPU ID that is common to all resources' best GPU lists.
// If no common GPU exists, it picks the first GPU from the first resource's best list.
// Returns empty string if no best GPUs are available for any resource.
//
// Parameters:
//   - resourceToBestGPUs: map from resource name to a slice of best GPU IDs for that resource.
//     The best GPU IDs are sorted in order of preference for the given resource.
//
// Returns:
//   - selectedGPUID: the best GPU ID to use for allocation, or empty string if no suitable GPU found.
func findBestCommonGPU(resourceToBestGPUs map[v1.ResourceName][]string) string {
	if len(resourceToBestGPUs) == 0 {
		return ""
	}

	// Extract the first resource's best GPUs as the starting candidate set
	var firstResourceGPUs []string
	for _, gpus := range resourceToBestGPUs {
		firstResourceGPUs = gpus
		break
	}
	if len(firstResourceGPUs) == 0 {
		return ""
	}

	// Find the intersection of all resources' best GPU lists
	gpuSet := sets.NewString(firstResourceGPUs...)
	for _, gpus := range resourceToBestGPUs {
		gpuSet = gpuSet.Intersection(sets.NewString(gpus...))
		if gpuSet.Len() == 0 {
			break
		}
	}

	// If there's a common GPU, pick the first one (since lists are sorted by preference)
	if gpuSet.Len() > 0 {
		return gpuSet.List()[0]
	}

	// No common GPU found; fall back to the first GPU from the first resource's list
	return firstResourceGPUs[0]
}
