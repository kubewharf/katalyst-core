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

package resourceplugin

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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	GPUMemPluginName = "gpu_mem_resource_plugin"
)

type GPUMemPlugin struct {
	sync.Mutex
	*baseplugin.BasePlugin
}

func NewGPUMemPlugin(base *baseplugin.BasePlugin) ResourcePlugin {
	return &GPUMemPlugin{
		BasePlugin: base,
	}
}

func (p *GPUMemPlugin) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

func (p *GPUMemPlugin) GetTopologyHints(req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	existReallocAnno, isReallocation := util.IsReallocation(req.Annotations)

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

	gpuCount, gpuNames, err := p.GetGPUCount(req)
	if err != nil {
		general.Errorf("getGPUCount failed from req %v with error: %v", req, err)
		return nil, fmt.Errorf("getGPUCount failed with error: %v", err)
	}

	if gpuCount == 0 {
		general.Errorf("getGPUCount failed from req %v with error: no GPU resources found", req)
		return nil, fmt.Errorf("no GPU resources found")
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
			if existReallocAnno {
				metricTags = append(metricTags, metrics.MetricTag{Key: "reallocation", Val: isReallocation})
			}
			_ = p.Emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	var hints map[string]*pluginapi.ListOfTopologyHints
	machineState := p.State.GetMachineState()
	allocationInfo := p.State.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = gpuutil.RegenerateGPUMemoryHints(allocationInfo, false)

		// regenerateHints failed. need to clear container record and re-calculate.
		if hints == nil {
			podEntries := p.State.GetPodEntries()
			delete(podEntries[req.PodUid], req.ContainerName)
			if len(podEntries[req.PodUid]) == 0 {
				delete(podEntries, req.PodUid)
			}

			var err error
			machineState, err = state.GenerateMachineStateFromPodEntries(p.QrmConfig, p.State.GetPodEntries(), p.GpuTopologyProvider)
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
	gpuMemory float64, gpuReq float64, machineState state.GPUMap, req *pluginapi.ResourceRequest,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	gpuTopology, numaTopologyReady, err := p.GpuTopologyProvider.GetGPUTopology()
	if err != nil {
		return nil, err
	}

	if !numaTopologyReady {
		return nil, fmt.Errorf("numa topology is ready")
	}

	perGPUMemory := gpuMemory / gpuReq
	general.Infof("gpuMemory: %f, gpuReq: %f, perGPUMemory: %f", gpuMemory, gpuReq, perGPUMemory)

	numaToAvailableGPUCount := make(map[int]float64)
	numaToMostAllocatedGPUMemory := make(map[int]float64)
	for gpuID, s := range machineState {
		if s == nil {
			continue
		}

		if s.GetGPUMemoryAllocated()+perGPUMemory <= s.GetGPUMemoryAllocatable() {
			info, ok := gpuTopology.GPUs[gpuID]
			if !ok {
				return nil, fmt.Errorf("gpu %s not found in gpuTopology", gpuID)
			}

			for _, numaNode := range info.GetNUMANode() {
				numaToAvailableGPUCount[numaNode] += 1
				numaToMostAllocatedGPUMemory[numaNode] = math.Max(s.GetGPUMemoryAllocated(), numaToMostAllocatedGPUMemory[numaNode])
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

func (p *GPUMemPlugin) GetTopologyAwareResources(allocationInfo *state.AllocationInfo) *gpuconsts.AllocatedResource {
	general.InfofV(4, "called")

	topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(allocationInfo.TopologyAwareAllocations))
	for deviceID, alloc := range allocationInfo.TopologyAwareAllocations {
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: alloc.GPUMemoryQuantity,
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
	}

	resp := &gpuconsts.AllocatedResource{
		ResourceName: p.ResourceName(),
		TopologyAwareResource: &pluginapi.TopologyAwareResource{
			IsNodeResource:                    true,
			IsScalarResource:                  true,
			AggregatedQuantity:                allocationInfo.AllocatedAllocation.GPUMemoryQuantity,
			OriginalAggregatedQuantity:        allocationInfo.AllocatedAllocation.GPUMemoryQuantity,
			TopologyAwareQuantityList:         topologyAwareQuantityList,
			OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
		},
	}

	return resp
}

func (p *GPUMemPlugin) GetTopologyAwareAllocatableResources(machineState state.GPUMap) *gpuconsts.AllocatableResource {
	general.InfofV(4, "called")

	p.Lock()
	defer p.Unlock()

	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity float64
	for deviceID, gpuState := range machineState {
		aggregatedAllocatableQuantity += gpuState.GetGPUMemoryAllocatable()
		aggregatedCapacityQuantity += gpuState.GetGPUMemoryAllocatable()
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: gpuState.GetGPUMemoryAllocatable(),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: gpuState.GetGPUMemoryAllocatable(),
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
	}
}

func (p *GPUMemPlugin) Allocate(req *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	existReallocAnno, isReallocation := util.IsReallocation(req.Annotations)

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

	gpuCount, gpuNames, err := p.GetGPUCount(req)
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
			if existReallocAnno {
				metricTags = append(metricTags, metrics.MetricTag{Key: "reallocation", Val: isReallocation})
			}
			_ = p.Emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	emptyResponse := &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   p.ResourceName(),
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}

	// currently, not to deal with init containers
	if req.ContainerType == pluginapi.ContainerType_INIT {
		return emptyResponse, nil
	} else if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		// not to deal with sidecars, and return a trivial allocationResult to avoid re-allocating
		return p.PackAllocationResponse(req, &state.AllocationInfo{}, nil, p.ResourceName())
	}

	allocationInfo := p.State.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		resp, packErr := p.PackAllocationResponse(req, allocationInfo, nil, p.ResourceName())
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %v", packErr)
		}
		return resp, nil
	}

	// get hint nodes from request
	hintNodes, err := machine.NewCPUSetUint64(req.GetHint().GetNodes()...)
	if err != nil {
		general.Warningf("failed to get hint nodes: %v", err)
		return nil, fmt.Errorf("failed to get hint nodes: %v", err)
	}

	newAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.GPUAllocation{
			GPUMemoryQuantity: gpuMemory,
			NUMANodes:         hintNodes.ToSliceInt(),
		},
	}

	p.State.SetAllocationInfo(req.PodUid, req.ContainerName, newAllocation, false)

	machineState, stateErr := state.GenerateMachineStateFromPodEntries(p.QrmConfig, p.State.GetPodEntries(), p.GpuTopologyProvider)
	if stateErr != nil {
		general.ErrorS(stateErr, "GenerateMachineStateFromPodEntries failed",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"gpuMemory", gpuMemory)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", stateErr)
	}

	// update state cache
	p.State.SetMachineState(machineState, true)

	return p.PackAllocationResponse(req, newAllocation, nil, p.ResourceName())
}
