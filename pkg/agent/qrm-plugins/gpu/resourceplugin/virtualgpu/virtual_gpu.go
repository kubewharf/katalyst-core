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

package virtualgpu

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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

type VirtualGPUPlugin struct {
	sync.Mutex
	*baseplugin.BasePlugin
}

// NewVirtualGPUPlugin creates and returns a new GPU compute resource plugin
func NewVirtualGPUPlugin(base *baseplugin.BasePlugin) resourceplugin.ResourcePlugin {
	// string(consts.ResourceGPUMemory) is the key used for state management in the QRM framework,
	// while GPUDeviceNames are the actual resource names used to fetch the device topologies.
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceGPUMemory),
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, float64(base.Conf.GPUMemoryAllocatablePerGPU.Value())))
	base.DefaultResourceStateGeneratorRegistry.RegisterResourceStateGenerator(string(consts.ResourceMilliGPU),
		state.NewGenericDefaultResourceStateGenerator(base.Conf.GPUDeviceNames, base.DeviceTopologyRegistry, float64(base.Conf.MilliGPUAllocatablePerGPU.Value())))
	return &VirtualGPUPlugin{
		BasePlugin: base,
	}
}

// ResourceName returns the name of the main resource this plugin manages
// TODO: use config for main resource
func (p *VirtualGPUPlugin) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

// GetTopologyHints returns topology hints for a given resource request
func (p *VirtualGPUPlugin) GetTopologyHints(ctx context.Context, req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
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
		hints, err = regenerateVirtualGPUHints(allocationInfoMap, false)
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

// resourceEvalContext holds pre-calculated values for evaluating GPU capacities and packing.
// It includes the requested quantity per GPU, the allocatable capacity per GPU,
// and the current allocations for each GPU to avoid duplicate calculations.
type resourceEvalContext struct {
	allocatablePerGPU float64
	perGPUReqQuantity float64
	gpuAllocations    map[string]float64
}

// calculateHints calculates topology hints for the given resource request
func (p *VirtualGPUPlugin) calculateHints(
	resourceQuantities map[v1.ResourceName]float64, gpuReq float64, gpuNames sets.String,
	resourceMachineState state.AllocationResourcesMap, gpuState state.AllocationMap,
	req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	gpuTopology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	if err != nil {
		return nil, fmt.Errorf("GetLatestDeviceTopology failed with error: %v", err)
	}

	// Check if milligpu is requested and handle gpuReq
	if hasMilligpuRequest(req) && gpuReq > 1 {
		return nil, fmt.Errorf("milligpu requested but gpu count is greater than 1")
	}

	general.Infof("resourceQuantities: %v, gpuReq: %f", resourceQuantities, gpuReq)

	// Step 1: Pre-calculate per-resource requirements and check global allocatability
	// This removes the O(N*M) calculation overhead inside the GPU loop.
	resContexts := make(map[v1.ResourceName]resourceEvalContext, len(resourceQuantities))
	for resName, reqQuantity := range resourceQuantities {
		allocatablePerGPU := p.getResourceAllocatableQuantity(resName)

		gpuAllocations := make(map[string]float64)
		if resMachineState, ok := resourceMachineState[resName]; ok {
			for gpuID, state := range resMachineState {
				gpuAllocations[gpuID] = state.GetQuantityAllocated()
			}
		}

		if allocatablePerGPU.IsZero() {
			// If a resource has 0 allocatable, it will fail for all GPUs.
			// We store 0 so it deterministically fails during capacity evaluation.
			resContexts[resName] = resourceEvalContext{
				allocatablePerGPU: 0,
				perGPUReqQuantity: reqQuantity / gpuReq,
				gpuAllocations:    gpuAllocations,
			}
			continue
		}
		perGPUReqQuantity := reqQuantity / gpuReq
		if perGPUReqQuantity > float64(allocatablePerGPU.Value()) {
			return nil, fmt.Errorf("per GPU requested quantity %f for resource %s exceeds allocatable %d per GPU", perGPUReqQuantity, resName, allocatablePerGPU.Value())
		}
		resContexts[resName] = resourceEvalContext{
			allocatablePerGPU: float64(allocatablePerGPU.Value()),
			perGPUReqQuantity: perGPUReqQuantity,
			gpuAllocations:    gpuAllocations,
		}
	}

	// Step 2: Evaluate GPU capacities and gather statistics
	numaToAvailableGPUCount, fittingGPUs, gpuIDToInfo := p.evaluateGPUsCapacity(gpuTopology, gpuState, gpuNames, resContexts)

	// Step 3: Find preferred NUMA nodes (optimal packing or spreading)
	preferredNUMANodes := getPreferredNUMANodes(fittingGPUs, resContexts, gpuIDToInfo, p.Conf.VirtualGPUPrefersSpreading)

	// Step 4: Generate NUMA hints
	availableNumaHints, err := p.generateNUMAHints(gpuReq, gpuTopology, numaToAvailableGPUCount, preferredNUMANodes)
	if err != nil {
		return nil, err
	}

	// NOTE: because grpc is inability to distinguish between an empty array and nil,
	//       we return an error instead of an empty array.
	if len(availableNumaHints) == 0 {
		general.Warningf("got no available gpu compute hints for pod: %s/%s, container: %s",
			req.PodNamespace, req.PodName, req.ContainerName)
		return nil, gpuutil.ErrNoAvailableVirtualGPUHints
	}

	// Step 5: Create hints map with all requested resources that are plugin's resources
	pluginResources := p.getAllPluginResources()
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

// evaluateGPUsCapacity iterates over all GPUs, checks if they can satisfy the resource requirements,
// and returns the count of available GPUs per NUMA node, a list of GPUs that fit the request, and their device info.
func (p *VirtualGPUPlugin) evaluateGPUsCapacity(
	gpuTopology *machine.DeviceTopology,
	gpuState state.AllocationMap,
	gpuNames sets.String,
	resContexts map[v1.ResourceName]resourceEvalContext,
) (map[int]float64, []string, map[string]machine.DeviceInfo) {
	numaToAvailableGPUCount := make(map[int]float64)
	var fittingGPUs []string
	gpuIDToInfo := make(map[string]machine.DeviceInfo)

	// Define filter function outside the loop to prevent closure allocation overhead per GPU
	filterFunc := func(ai *state.AllocationInfo) bool {
		return gpuNames.Has(ai.DeviceName)
	}

	for gpuID, info := range gpuTopology.Devices {
		if info.Health != deviceplugin.Healthy {
			continue
		}

		gpuAllocated := gpuState[gpuID].GetQuantityAllocatedWithFilter(filterFunc)
		if gpuAllocated > 0 {
			continue // skip already allocated
		}

		deviceFitsAllResources := true
		for _, ctx := range resContexts {
			if ctx.allocatablePerGPU == 0 {
				deviceFitsAllResources = false
				break
			}

			allocated := ctx.gpuAllocations[gpuID]

			if allocated+ctx.perGPUReqQuantity > ctx.allocatablePerGPU {
				deviceFitsAllResources = false
				break
			}
		}

		if deviceFitsAllResources {
			fittingGPUs = append(fittingGPUs, gpuID)
			gpuIDToInfo[gpuID] = info
			for _, numaNode := range info.GetNUMANodes() {
				numaToAvailableGPUCount[numaNode] += 1
			}
		}
	}

	return numaToAvailableGPUCount, fittingGPUs, gpuIDToInfo
}

// getPreferredNUMANodes evaluates fitting GPUs to find those that best match the allocation strategy
// (packing or spreading). For packing (prefersSpreading=false), it identifies GPUs with the maximum
// allocated resources. For spreading (prefersSpreading=true), it identifies GPUs with the minimum
// allocated resources.
// It returns a deduplicated list of all NUMA nodes associated with these optimal GPUs.
// This implements a Zero-Copy Memory Optimization by directly reading from resourceMachineState.
func getPreferredNUMANodes(
	fittingGPUs []string,
	resContexts map[v1.ResourceName]resourceEvalContext,
	gpuIDToInfo map[string]machine.DeviceInfo,
	prefersSpreading bool,
) []int {
	if len(fittingGPUs) == 0 || len(resContexts) == 0 {
		return nil
	}

	// 1. Find global optimal (max for packing, min for spreading) allocation for each requested resource among all fitting GPUs
	optimalAlloc := make(map[v1.ResourceName]float64, len(resContexts))
	for resName, ctx := range resContexts {
		var targetAlloc float64
		// Initialize targetAlloc with the first GPU's allocation
		if len(fittingGPUs) > 0 {
			targetAlloc = ctx.gpuAllocations[fittingGPUs[0]]
		}
		for _, gpuID := range fittingGPUs {
			alloc := ctx.gpuAllocations[gpuID]
			if prefersSpreading {
				if alloc < targetAlloc {
					targetAlloc = alloc
				}
			} else {
				if alloc > targetAlloc {
					targetAlloc = alloc
				}
			}
		}
		optimalAlloc[resName] = targetAlloc
	}

	// 2. Identify optimal GPUs that match the target allocation for ALL requested resources
	optimalGPUs := make(map[string]bool)
	for _, gpuID := range fittingGPUs {
		isOptimal := true
		for resName, ctx := range resContexts {
			alloc := ctx.gpuAllocations[gpuID]
			if alloc != optimalAlloc[resName] {
				isOptimal = false
				break
			}
		}
		if isOptimal {
			optimalGPUs[gpuID] = true
		}
	}

	// 3. Collect and deduplicate NUMA nodes from all optimal GPUs
	numaNodesSet := sets.NewInt()
	for gpuID := range optimalGPUs {
		if info, ok := gpuIDToInfo[gpuID]; ok {
			for _, node := range info.GetNUMANodes() {
				numaNodesSet.Insert(node)
			}
		}
	}

	return numaNodesSet.UnsortedList()
}

// generateNUMAHints generates topology hints based on requested GPU count, available GPUs per NUMA,
// and marks hints containing preferred NUMA nodes as preferred.
func (p *VirtualGPUPlugin) generateNUMAHints(
	gpuReq float64,
	gpuTopology *machine.DeviceTopology,
	numaToAvailableGPUCount map[int]float64,
	preferredNUMANodes []int,
) ([]*pluginapi.TopologyHint, error) {
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

	markPreferredNUMANodes(availableNumaHints, preferredNUMANodes)

	return availableNumaHints, nil
}

// markPreferredNUMANodes updates the topology hints based on optimal NUMA nodes.
// It first attempts to find matching hints among those already marked as Preferred (e.g. minimal NUMA count).
// If any matching hints are found in this group, they are kept as Preferred, and all others are marked non-Preferred.
// If no matching hints are found in the already Preferred group, it then checks the non-Preferred hints.
// If matches are found there, they are marked Preferred, and all others non-Preferred.
// If still no matches are found, the original Preferred status of all hints is kept unchanged.
func markPreferredNUMANodes(hints []*pluginapi.TopologyHint, preferredNUMANodes []int) {
	if len(preferredNUMANodes) == 0 || len(hints) == 0 {
		return
	}

	preferredSet := sets.NewInt(preferredNUMANodes...)
	foundInOriginallyPreferred := false
	foundInOriginallyNonPreferred := false

	// First pass: Check if any originally Preferred hint matches the preferred nodes
	for _, hint := range hints {
		if !hint.Preferred {
			continue
		}
		for _, node := range hint.Nodes {
			if preferredSet.Has(int(node)) {
				foundInOriginallyPreferred = true
				break
			}
		}
	}

	// Second pass: If not found in Preferred, check if any non-Preferred hint matches
	if !foundInOriginallyPreferred {
		for _, hint := range hints {
			if hint.Preferred {
				continue
			}
			for _, node := range hint.Nodes {
				if preferredSet.Has(int(node)) {
					foundInOriginallyNonPreferred = true
					break
				}
			}
		}
	}

	// If no matches found at all, we keep the original Preferred markings
	if !foundInOriginallyPreferred && !foundInOriginallyNonPreferred {
		return
	}

	// Apply the new Preferred status based on where we found matches
	for _, hint := range hints {
		isMatch := false
		for _, node := range hint.Nodes {
			if preferredSet.Has(int(node)) {
				isMatch = true
				break
			}
		}

		if foundInOriginallyPreferred {
			// Only matching originally Preferred hints become Preferred
			hint.Preferred = hint.Preferred && isMatch
		} else {
			// Only matching originally non-Preferred hints become Preferred
			hint.Preferred = !hint.Preferred && isMatch
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
func (p *VirtualGPUPlugin) GetTopologyAwareResources(ctx context.Context, podUID, containerName string) (map[string]*pluginapi.GetTopologyAwareResourcesResponse, error) {
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
func (p *VirtualGPUPlugin) getResourceAllocatableQuantity(resourceName v1.ResourceName) resource.Quantity {
	switch resourceName {
	case consts.ResourceGPUMemory:
		return p.Conf.GPUMemoryAllocatablePerGPU
	case consts.ResourceMilliGPU:
		return p.Conf.MilliGPUAllocatablePerGPU
	default:
		return resource.Quantity{}
	}
}

// GetTopologyAwareAllocatableResources returns topology-aware allocatable resources
func (p *VirtualGPUPlugin) GetTopologyAwareAllocatableResources(ctx context.Context) (map[string]*pluginapi.AllocatableTopologyAwareResource, error) {
	general.InfofV(4, "called")

	p.Lock()
	defer p.Unlock()

	gpuTopology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	if err != nil {
		return nil, err
	}

	// Get all resources to return: main resource + extra resources
	pluginResources := p.getAllPluginResources()

	// Generate allocatable resources for each resource
	allocatableResources := make(map[string]*pluginapi.AllocatableTopologyAwareResource)
	for _, resourceName := range pluginResources {
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

		allocatableResources[string(resourceName)] = &pluginapi.AllocatableTopologyAwareResource{
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
func (p *VirtualGPUPlugin) GetExtraResources() []string {
	return []string{string(consts.ResourceMilliGPU)}
}

// getAllPluginResources returns all resources managed by this plugin
func (p *VirtualGPUPlugin) getAllPluginResources() []v1.ResourceName {
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
func (p *VirtualGPUPlugin) Allocate(
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

	// We cannot have milligpu and gpu device request together
	if deviceReq != nil {
		deviceName := p.ResolveResourceName(deviceReq.DeviceName, true)
		if hasMilligpuRequest(resourceReq) && deviceName == gpuconsts.GPUDeviceType {
			return nil, fmt.Errorf("milligpu requested together with GPU device request")
		}
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

	return p.allocate(resourceReq, deviceReq, resourceQuantities, qosLevel)
}

// allocate allocates GPU compute resources when physical GPU devices are requested
func (p *VirtualGPUPlugin) allocate(
	resourceReq *pluginapi.ResourceRequest,
	deviceReq *pluginapi.DeviceRequest,
	resourceQuantities map[v1.ResourceName]float64,
	qosLevel string,
) (*pluginapi.ResourceAllocationResponse, error) {
	gpuTopology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
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

	p.mergeTimesliceAndPolicyEnvs(resourceAllocationEnvs)

	return p.PackAllocationResponse(resourceReq, allocationInfoMap, nil, p.ResourceName(), resourceAllocationEnvs)
}

// mergeTimesliceAndPolicyEnvs merges the timeslice and policy environment variables into the resource allocation environments
func (p *VirtualGPUPlugin) mergeTimesliceAndPolicyEnvs(resourceAllocationEnvs map[string]map[string]string) {
	for _, envs := range resourceAllocationEnvs {
		// Inject timeslice configuration for Virtual GPU isolation if the environment name is configured
		if p.Conf.VirtualGPUTimesliceEnvName != "" {
			envs[p.Conf.VirtualGPUTimesliceEnvName] = strconv.Itoa(p.Conf.VirtualGPUTimesliceEnvValue)
		}
		// Inject compute policy configuration for Virtual GPU isolation if the environment name is configured
		if p.Conf.VirtualGPUComputePolicyEnvName != "" {
			envs[p.Conf.VirtualGPUComputePolicyEnvName] = strconv.Itoa(p.Conf.VirtualGPUComputePolicyEnvValue)
		}
	}
}

// calculateEnvVariables computes the environment variable map for a given GPU resource.
// It calculates the allocated fraction percentage (granularity 1, max 100) and formats it
// as "<deviceID>:<percentage>" when exactly one device is allocated.
func (p *VirtualGPUPlugin) calculateEnvVariables(resName v1.ResourceName, allocatedDevices []string, resQuantityPerGPU float64) map[string]string {
	envName := ""
	if resName == consts.ResourceGPUMemory {
		envName = p.Conf.VirtualGPUMemoryWeightEnvName
	} else if resName == consts.ResourceMilliGPU {
		envName = p.Conf.VirtualGPUComputeWeightEnvName
	}

	if envName != "" && len(allocatedDevices) == 1 {
		allocatableQuantity := p.getResourceAllocatableQuantity(resName)
		if !allocatableQuantity.IsZero() {
			// Calculate the allocated percentage of the GPU resource (minimum 1, max 100)
			fraction := (resQuantityPerGPU / float64(allocatableQuantity.Value())) * 100
			return map[string]string{
				envName: fmt.Sprintf("%s:%.0f", allocatedDevices[0], fraction),
			}
		}
	}
	return nil
}

func (p *VirtualGPUPlugin) generateDeviceRequestForMilliGPU(resourceReq *pluginapi.ResourceRequest) (*pluginapi.DeviceRequest, error) {
	// Get topology to find all available healthy devices
	gpuTopology, err := p.DeviceTopologyRegistry.GetLatestDeviceTopology(p.Conf.GPUDeviceNames)
	if err != nil {
		return nil, fmt.Errorf("GetLatestDeviceTopology failed with error: %v", err)
	}

	var allHealthyDevices []string
	for deviceID, info := range gpuTopology.Devices {
		if info.Health == deviceplugin.Healthy {
			allHealthyDevices = append(allHealthyDevices, deviceID)
		}
	}

	// Create a preliminary DeviceRequest
	// TODO: we currently don't support init containers, so we don't have reusable devices.
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

// regenerateVirtualGPUHints regenerates hints for container that'd already been allocated resources,
// and regenerateHints will assemble hints based on already-existed AllocationInfo for each resource,
// without any calculation logics at all
func regenerateVirtualGPUHints(
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
