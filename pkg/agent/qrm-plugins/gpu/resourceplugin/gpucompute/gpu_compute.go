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

package gpucompute

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

type GPUComputePlugin struct {
	sync.Mutex
	*baseplugin.BasePlugin
}

// NewGPUComputePlugin creates and returns a new GPU compute resource plugin
func NewGPUComputePlugin(base *baseplugin.BasePlugin) resourceplugin.ResourcePlugin {
	// string(consts.ResourceGPUMemory) is the key used for state management in the QRM framework,
	// while GPUDeviceNames are the actual resource names used to fetch the device topologies.
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory),
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, float64(base.Conf.GPUMemoryAllocatablePerGPU.Value())))
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceMilliGPU),
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, float64(base.Conf.MilliGPUAllocatablePerGPU.Value())))
	return &GPUComputePlugin{
		BasePlugin: base,
	}
}

// ResourceName returns the name of the main resource this plugin manages
// TODO: use config for main resource
func (p *GPUComputePlugin) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

// GetTopologyHints returns topology hints for a given resource request
func (p *GPUComputePlugin) GetTopologyHints(ctx context.Context, req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.Conf.QoSConfiguration, req, p.PodAnnotationKeptKeys, p.PodLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	// Get all resource quantities from the request
	pluginResources := p.getAllPluginResources()
	allowedResources := sets.NewString()
	for _, r := range pluginResources {
		allowedResources.Insert(string(r))
	}
	_, floatQuantity, err := util.GetQuantityMapFromResourceReq(req, allowedResources)
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
		hints, err = regenerateGPUComputeHints(allocationInfoMap, false)
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
			}
			podResourceEntries := p.GetState().GetPodResourceEntries()
			// Refresh resourceMachineState after regenerating
			resourceMachineState, err = p.GenerateMachineStateFromPodEntries(podResourceEntries)
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
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
func (p *GPUComputePlugin) getGPUTopology(gpuNames sets.String) (*machine.DeviceTopology, error) {
	switch {
	case gpuNames.Len() == 1:
		deviceName := gpuNames.List()[0]
		return p.DeviceTopologyRegistry.GetDeviceTopology(deviceName)
	case gpuNames.Len() == 0 && len(p.Conf.GPUDeviceNames) > 0:
		topology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
		if err != nil {
			return nil, err
		}
		return topology, nil
	default:
		return nil, fmt.Errorf("pod requests multiple or no gpu types: %v", gpuNames.List())
	}
}

// calculateHints calculates topology hints for the given resource request
func (p *GPUComputePlugin) calculateHints(
	resourceQuantities map[v1.ResourceName]float64, gpuReq float64, gpuNames sets.String,
	resourceMachineState state.AllocationResourcesMap, gpuState state.AllocationMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	gpuTopology, err := p.getGPUTopology(gpuNames)
	if err != nil {
		return nil, err
	}

	// Check if milligpu is requested and handle gpuReq
	if hasMilligpuRequest(req) && gpuNames.Len() > 0 {
		return nil, fmt.Errorf("milligpu requested but gpu device is also requested")
	}

	general.Infof("resourceQuantities: %v, gpuReq: %f", resourceQuantities, gpuReq)

	numaToAvailableGPUCount := make(map[int]float64)
	pluginResources := p.getAllPluginResources()

	// Variables to track max allocation per resource and which GPU has that max
	resourceToMaxAllocatedValue := make(map[v1.ResourceName]float64)
	resourceToMaxAllocatedGPUID := make(map[v1.ResourceName]string)
	// Also store GPU info and resAllocatedMap for quick look-up
	gpuIDToInfo := make(map[string]machine.DeviceInfo)
	gpuIDToResAllocatedMap := make(map[string]map[v1.ResourceName]float64)

	// Single loop over GPUs:
	for gpuID, info := range gpuTopology.Devices {
		if info.Health != deviceplugin.Healthy {
			continue
		}

		// Step 1: check if GPU is available (not already allocated)
		gpuAllocated := gpuState[gpuID].GetQuantityAllocatedWithFilter(func(ai *state.AllocationInfo) bool {
			return gpuNames.Has(ai.DeviceName)
		})
		if gpuAllocated > 0 {
			continue // skip already allocated
		}

		// Store this GPU's info for later
		gpuIDToInfo[gpuID] = info

		// Step 2: check if all requested resources fit on this GPU
		deviceFitsAllResources := true
		// Store the allocated values for each resource here so we don't look them up twice!
		resAllocatedMap := make(map[v1.ResourceName]float64)
		for resName := range resourceQuantities {
			reqQuantity := resourceQuantities[resName]
			allocatablePerGPU := p.getResourceAllocatableQuantity(string(resName))
			if allocatablePerGPU.IsZero() {
				deviceFitsAllResources = false
				break
			}

			perGPUReqQuantity := reqQuantity / gpuReq
			if perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				return nil, fmt.Errorf("per GPU requested quantity %f for resource %s exceeds allocatable %d per GPU", perGPUReqQuantity, resName, allocatablePerGPU.Value())
			}
			var allocated float64
			if resMachineState, ok := resourceMachineState[resName]; ok {
				allocated = resMachineState[gpuID].GetQuantityAllocated()
			}
			resAllocatedMap[resName] = allocated
			if allocated+perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
				deviceFitsAllResources = false
				break
			}

			// Update max allocation tracking for this resource
			if allocated > resourceToMaxAllocatedValue[resName] {
				resourceToMaxAllocatedValue[resName] = allocated
				resourceToMaxAllocatedGPUID[resName] = gpuID
			}
		}

		// Store this GPU's resAllocatedMap for later
		gpuIDToResAllocatedMap[gpuID] = resAllocatedMap

		if deviceFitsAllResources {
			// Update numaToAvailableGPUCount
			for _, numaNode := range info.GetNUMANodes() {
				numaToAvailableGPUCount[numaNode] += 1
			}
		}
	}

	// Find common numa node if all resources agree on the same GPU
	commonNUMANode := findCommonNUMANode(resourceQuantities, resourceToMaxAllocatedGPUID, gpuIDToInfo)

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

	// Prefer hints that include common numa node if all resources agree on it
	preferCommonNUMANode(availableNumaHints, commonNUMANode)

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	//       we should resolve this issue if we need to manage multi-resource in one plugin.
	if len(availableNumaHints) == 0 {
		general.Warningf("got no available gpu compute hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, gpuutil.ErrNoAvailableGPUComputeHints
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

// findCommonNUMANode checks if all requested resources agree on the same GPU, and if so returns the numa node of that GPU.
func findCommonNUMANode(resourceQuantities map[v1.ResourceName]float64, resourceToMaxAllocatedGPUID map[v1.ResourceName]string, gpuIDToInfo map[string]machine.DeviceInfo) *int {
	if len(resourceQuantities) == 0 {
		return nil
	}

	var firstResName v1.ResourceName
	for resName := range resourceQuantities {
		firstResName = resName
		break
	}
	commonGPUID := resourceToMaxAllocatedGPUID[firstResName]
	if commonGPUID == "" {
		return nil
	}

	hasCommonGPU := true
	for resName := range resourceQuantities {
		if resName == firstResName {
			continue
		}
		if resourceToMaxAllocatedGPUID[resName] != commonGPUID {
			hasCommonGPU = false
			break
		}
	}

	if hasCommonGPU {
		if commonGPUInfo, exists := gpuIDToInfo[commonGPUID]; exists {
			numaNodes := commonGPUInfo.GetNUMANodes()
			if len(numaNodes) > 0 {
				node := numaNodes[0]
				return &node
			}
		}
	}

	return nil
}

// preferCommonNUMANode updates the preferred topology hints to prefer hints that include the common numa node.
// If there is a common numa node, first marks all hints as NOT preferred, then only hints including that node are marked as preferred; otherwise, no changes are made.
func preferCommonNUMANode(hints []*pluginapi.TopologyHint, commonNUMANode *int) {
	if commonNUMANode == nil || len(hints) == 0 {
		return
	}

	// Step 1: first mark all hints' Preferred as false
	for _, hint := range hints {
		hint.Preferred = false
	}

	// Step 2: then, set Preferred to true only for those hints that include commonNUMANode
	for _, hint := range hints {
		for _, node := range hint.Nodes {
			if int(node) == *commonNUMANode {
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
func (p *GPUComputePlugin) GetTopologyAwareResources(ctx context.Context, podUID, containerName string) (map[string]*pluginapi.GetTopologyAwareResourcesResponse, error) {
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
func (p *GPUComputePlugin) getResourceAllocatableQuantity(resourceName string) resource.Quantity {
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
func (p *GPUComputePlugin) GetTopologyAwareAllocatableResources(ctx context.Context) (map[string]*pluginapi.AllocatableTopologyAwareResource, error) {
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

// TODO: make sure extra resource dont include main resource
func (p *GPUComputePlugin) GetExtraResources() []string {
	return []string{string(consts.ResourceMilliGPU)}
}

// getAllPluginResources returns all resources managed by this plugin
func (p *GPUComputePlugin) getAllPluginResources() []v1.ResourceName {
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

// Allocate allocates GPU compute resources to a container based on the given request
func (p *GPUComputePlugin) Allocate(
	ctx context.Context, resourceReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest,
) (*pluginapi.ResourceAllocationResponse, error) {
	quantity, exists := resourceReq.ResourceRequests[p.ResourceName()]
	if !exists || quantity == 0 {
		general.InfoS("No GPU compute annotation detected and no GPU compute requested, returning empty response",
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

	allowedResources := sets.NewString()
	for _, r := range p.getAllPluginResources() {
		allowedResources.Insert(string(r))
	}
	_, resourceQuantities, err := util.GetQuantityMapFromResourceReq(resourceReq, allowedResources)
	if err != nil {
		return nil, fmt.Errorf("getQuantityMapFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", resourceReq.PodNamespace,
		"podName", resourceReq.PodName,
		"containerName", resourceReq.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", resourceReq.Annotations,
		"resourceQuantities", resourceQuantities,
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
		return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName(), nil)
	}

	allocationInfo := p.GetState().GetAllocationInfo(consts.ResourceGPUMemory, resourceReq.PodUid, resourceReq.ContainerName)
	if allocationInfo != nil {
		allocationInfoMap := map[string]*state.AllocationInfo{
			p.ResourceName(): allocationInfo,
		}
		resp, packErr := p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName(), nil)
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				resourceReq.PodNamespace, resourceReq.PodName, resourceReq.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %w", packErr)
		}
		return resp, nil
	}

	// Check if we have both device request and milligpu request
	if hasMilligpuRequest(resourceReq) && deviceReq != nil {
		return nil, fmt.Errorf("milligpu requested together with GPU device request")
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
		// deviceReq is nil but we have milligpu requested, generate our own device request
		generatedDeviceReq, err := p.generateDeviceRequestForMilliGPU(resourceReq)
		if err != nil {
			return nil, fmt.Errorf("failed to generate device request for milligpu: %w", err)
		}
		deviceReq = generatedDeviceReq
	}

	general.Infof("deviceReq: %v", deviceReq.String())

	// Use allocate for when we have deviceReq
	return p.allocate(ctx, resourceReq, deviceReq, resourceQuantities, qosLevel)
}

// allocate allocates GPU compute resources when physical GPU devices are requested
func (p *GPUComputePlugin) allocate(
	ctx context.Context,
	resourceReq *pluginapi.ResourceRequest,
	deviceReq *pluginapi.DeviceRequest,
	resourceQuantities map[v1.ResourceName]float64,
	qosLevel string,
) (*pluginapi.ResourceAllocationResponse, error) {
	// Get GPU topology using the specific device resource name if present, else fallback to latest topology of allowed devices
	var gpuTopology *machine.DeviceTopology
	var err error
	if deviceReq.DeviceName != "" {
		gpuTopology, err = p.DeviceTopologyRegistry.GetDeviceTopology(deviceReq.DeviceName)
	} else {
		gpuTopology, err = p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	}
	if err != nil {
		general.Warningf("failed to get gpu topology: %v", err)
		return nil, fmt.Errorf("failed to get gpu topology: %v", err)
	}

	// Use the strategy framework to allocate GPU compute
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

	allocationInfoMap := make(map[string]*state.AllocationInfo)

	// Map of resource name to environment variables
	resourceAllocationEnvs := make(map[string]map[string]string)

	for resName, quantity := range resourceQuantities {
		newAllocation := &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(resourceReq, commonstate.EmptyOwnerPoolName, qosLevel),
			AllocatedAllocation: state.Allocation{
				Quantity:  quantity,
				NUMANodes: hintNodes.ToSliceInt(),
			},
			TopologyAwareAllocations: make(map[string]state.Allocation),
		}

		resQuantityPerGPU := quantity / float64(deviceReq.DeviceRequest)
		for _, deviceID := range result.AllocatedDevices {
			info, ok := gpuTopology.Devices[deviceID]
			if !ok {
				return nil, fmt.Errorf("failed to get gpu info for device: %s", deviceID)
			}

			newAllocation.TopologyAwareAllocations[deviceID] = state.Allocation{
				Quantity:  resQuantityPerGPU,
				NUMANodes: info.NumaNodes,
			}
		}

		// Set allocation info in state
		p.GetState().SetAllocationInfo(resName, resourceReq.PodUid, resourceReq.ContainerName, newAllocation, false)

		machineState, stateErr := p.GenerateResourceStateFromPodEntries(string(resName), nil)
		if stateErr != nil {
			general.ErrorS(stateErr, "GenerateResourceStateFromPodEntries failed",
				"podNamespace", resourceReq.PodNamespace,
				"podName", resourceReq.PodName,
				"containerName", resourceReq.ContainerName,
				"resourceQuantity", quantity)
			return nil, fmt.Errorf("GenerateResourceStateFromPodEntries failed with error: %v", stateErr)
		}

		// update state cache
		p.GetState().SetResourceState(resName, machineState, true)
		allocationInfoMap[string(resName)] = newAllocation

		// Calculate the environment variables
		envs := p.calculateEnvVariables(resName, result.AllocatedDevices, resQuantityPerGPU)
		if len(envs) > 0 {
			resourceAllocationEnvs[string(resName)] = envs
		}
	}

	return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName(), resourceAllocationEnvs)
}

// calculateEnvVariables computes the environment variable map for a given GPU resource.
// It calculates the allocated fraction percentage and formats it as "<deviceID>:<fraction>"
// when exactly one device is allocated.
func (p *GPUComputePlugin) calculateEnvVariables(resName v1.ResourceName, allocatedDevices []string, resQuantityPerGPU float64) map[string]string {
	envKey := ""
	if string(resName) == string(consts.ResourceGPUMemory) {
		envKey = p.Conf.GPUMemoryWeightEnvKey
	} else if string(resName) == string(consts.ResourceMilliGPU) {
		envKey = p.Conf.MilliGPUWeightEnvKey
	}

	if envKey != "" && len(allocatedDevices) == 1 {
		allocatableQuantity := p.getResourceAllocatableQuantity(string(resName))
		if !allocatableQuantity.IsZero() {
			fraction := (resQuantityPerGPU / float64(allocatableQuantity.Value())) * 100
			return map[string]string{
				envKey: fmt.Sprintf("%s:%.0f", allocatedDevices[0], fraction),
			}
		}
	}
	return nil
}

func (p *GPUComputePlugin) generateDeviceRequestForMilliGPU(resourceReq *pluginapi.ResourceRequest) (*pluginapi.DeviceRequest, error) {
	// Get topology to find all available healthy devices
	gpuTopology, err := p.getGPUTopology(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get gpu topology: %w", err)
	}

	var allHealthyDevices []string
	for deviceID, info := range gpuTopology.Devices {
		if info.Health == deviceplugin.Healthy {
			allHealthyDevices = append(allHealthyDevices, deviceID)
		}
	}

	// Create a preliminary DeviceRequest
	deviceReq := &pluginapi.DeviceRequest{
		DeviceRequest:    1,
		Hint:             resourceReq.GetHint(),
		AvailableDevices: allHealthyDevices,
	}

	// Filter out occupied devices from AvailableDevices
	allocationResourcesMap := p.GetState().GetMachineState()
	filteredDevices := make([]string, 0, len(deviceReq.AvailableDevices))
	for _, deviceID := range deviceReq.AvailableDevices {
		deviceOccupied := false
		for resourceName, allocationMap := range allocationResourcesMap {
			// Only check gpu_device resource
			if string(resourceName) != gpuconsts.GPUDeviceType {
				continue
			}
			if allocationState, exists := allocationMap[deviceID]; exists {
				if len(allocationState.PodEntries) > 0 {
					deviceOccupied = true
					break
				}
			}
		}
		if !deviceOccupied {
			filteredDevices = append(filteredDevices, deviceID)
		}
	}
	deviceReq.AvailableDevices = filteredDevices

	return deviceReq, nil
}

// regenerateGPUComputeHints regenerates hints for container that'd already been allocated resources,
// and regenerateHints will assemble hints based on already-existed AllocationInfo for each resource,
// without any calculation logics at all
// TODO: change name
func regenerateGPUComputeHints(
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
