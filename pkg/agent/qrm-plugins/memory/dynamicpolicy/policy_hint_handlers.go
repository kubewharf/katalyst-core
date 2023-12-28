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

package dynamicpolicy

import (
	"context"
	"fmt"
	"sort"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	affinityutil "github.com/kubewharf/katalyst-core/pkg/util/affinity"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

func (p *DynamicPolicy) sharedCoresHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
		map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceMemory): nil, // indicates that there is no numa preference
		})
}

func (p *DynamicPolicy) reclaimedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	return p.sharedCoresHintHandler(ctx, req)
}

func (p *DynamicPolicy) dedicatedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("dedicatedCoresHintHandler got nil req")
	}

	switch req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] {
	case apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable:
		return p.dedicatedCoresWithNUMABindingHintHandler(ctx, req)
	default:
		return p.dedicatedCoresWithoutNUMABindingHintHandler(ctx, req)
	}
}

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingHintHandler(_ context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceMemory): nil,
			})
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	resourcesMachineState := p.state.GetMachineState()
	var hints map[string]*pluginapi.ListOfTopologyHints

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		hints = regenerateHints(uint64(reqInt), allocationInfo)

		// regenerateHints failed, and we need to clear container record and re-calculate.
		if hints == nil {
			podResourceEntries := p.state.GetPodResourceEntries()
			for _, podEntries := range podResourceEntries {
				delete(podEntries[req.PodUid], req.ContainerName)
				if len(podEntries[req.PodUid]) == 0 {
					delete(podEntries, req.PodUid)
				}
			}

			var err error
			resourcesMachineState, err = state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
			if err != nil {
				general.Errorf("pod: %s/%s, container: %s GenerateMachineStateFromPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			}
		}
	}

	// if hints exists in extra state-file, prefer to use them
	if hints == nil {
		availableNUMAs := resourcesMachineState[v1.ResourceMemory].GetNUMANodesWithoutNUMABindingPods()

		var extraErr error
		hints, extraErr = util.GetHintsFromExtraStateFile(req.PodName, string(v1.ResourceMemory),
			p.extraStateFileAbsPath, availableNUMAs)
		if extraErr != nil {
			general.Infof("pod: %s/%s, container: %s GetHintsFromExtraStateFile failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, extraErr)
		}
	}

	// otherwise, calculate hint for container without allocated memory
	if hints == nil {
		var calculateErr error
		// calculate hint for container without allocated memory
		hints, calculateErr = p.calculateHints(uint64(reqInt), resourcesMachineState, req.Annotations)
		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	// filter hints by mircotopology affinity and anti-affinity
	if len(hints) > 0 {
		hints, err = p.filterHintsByAffinityAndAntiAffinity(req, hints)
		if err != nil {
			return nil, fmt.Errorf("filterHintsByAffinityAndAntiAffinity failed with error: %v", err)
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceMemory), hints)
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingHintHandler(_ context.Context,
	_ *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// calculateHints is a helper function to calculate the topology hints
// with the given container requests.
func (p *DynamicPolicy) calculateHints(reqInt uint64, resourcesMachineState state.NUMANodeResourcesMap,
	reqAnnotations map[string]string) (map[string]*pluginapi.ListOfTopologyHints, error) {

	machineState := resourcesMachineState[v1.ResourceMemory]

	if len(machineState) == 0 {
		return nil, fmt.Errorf("calculateHints with empty machineState")
	}

	numaNodes := make([]int, 0, len(machineState))
	for numaNode := range machineState {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	hints := map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceMemory): {
			Hints: []*pluginapi.TopologyHint{},
		},
	}

	bytesPerNUMA, err := machineState.BytesPerNUMA()
	if err != nil {
		return nil, fmt.Errorf("getBytesPerNUMAFromMachineState failed with error: %v", err)
	}

	minNUMAsCountNeeded, _, err := util.GetNUMANodesCountToFitMemoryReq(reqInt, bytesPerNUMA, len(machineState))
	if err != nil {
		return nil, fmt.Errorf("GetNUMANodesCountToFitMemoryReq failed with error: %v", err)
	}

	// because it's hard to control memory allocation accurately,
	// we only support numa_binding but not exclusive container with request smaller than 1 NUMA
	if qosutil.AnnotationsIndicateNUMABinding(reqAnnotations) &&
		!qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations) &&
		minNUMAsCountNeeded > 1 {
		return nil, fmt.Errorf("NUMA not exclusive binding container has request larger than 1 NUMA")
	}

	numaPerSocket, err := p.topology.NUMAsPerSocket()
	if err != nil {
		return nil, fmt.Errorf("NUMAsPerSocket failed with error: %v", err)
	}

	bitmask.IterateBitMasks(numaNodes, func(mask bitmask.BitMask) {
		maskCount := mask.Count()
		if maskCount < minNUMAsCountNeeded {
			return
		} else if qosutil.AnnotationsIndicateNUMABinding(reqAnnotations) &&
			!qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations) &&
			maskCount > 1 {
			// because it's hard to control memory allocation accurately,
			// we only support numa_binding but not exclusive container with request smaller than 1 NUMA
			return
		}

		maskBits := mask.GetBits()
		numaCountNeeded := mask.Count()

		var freeBytesInMask uint64 = 0
		for _, nodeID := range maskBits {
			if machineState[nodeID] == nil {
				general.Warningf("NUMA: %d has nil state", nodeID)
				return
			} else if qosutil.AnnotationsIndicateNUMAExclusive(reqAnnotations) && machineState[nodeID].Allocated > 0 {
				general.Warningf("numa_exclusive container skip mask: %s with NUMA: %d allocated: %d",
					mask.String(), nodeID, machineState[nodeID].Allocated)
				return
			}

			freeBytesInMask += machineState[nodeID].Free
		}

		if freeBytesInMask < reqInt {
			general.InfofV(4, "free bytes: %d in mask are smaller than request bytes: %d", freeBytesInMask, reqInt)
			return
		}

		crossSockets, err := machine.CheckNUMACrossSockets(maskBits, p.topology)
		if err != nil {
			general.Errorf("CheckNUMACrossSockets failed with error: %v", err)
			return
		} else if numaCountNeeded <= numaPerSocket && crossSockets {
			general.InfofV(4, "needed: %d; min-needed: %d; NUMAs: %v cross sockets with numaPerSocket: %d",
				numaCountNeeded, minNUMAsCountNeeded, maskBits, numaPerSocket)
			return
		}

		hints[string(v1.ResourceMemory)].Hints = append(hints[string(v1.ResourceMemory)].Hints, &pluginapi.TopologyHint{
			Nodes:     machine.MaskToUInt64Array(mask),
			Preferred: len(maskBits) == minNUMAsCountNeeded,
		})
	})

	return hints, nil
}

func (p *DynamicPolicy) filterHintsByAffinityAndAntiAffinity(
	req *pluginapi.ResourceRequest,
	hints map[string]*pluginapi.ListOfTopologyHints,
) (map[string]*pluginapi.ListOfTopologyHints, error) {
	// 1. check if req need affinity filter
	podAffinity, err := affinityutil.UnmarshalAffinity(req.Annotations)
	if err != nil {
		return nil, err
	}
	if podAffinity.Affinity == nil && podAffinity.AntiAffinity == nil && !p.qosConfig.HasAffinityLabels(req.Labels) {
		return hints, nil
	}
	// There is no need to do numa level inter-pod affinity selction if exclusive = true
	if req.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaExclusive] ==
		apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable {
		if podAffinity.Affinity != nil {
			return nil, fmt.Errorf("can not find required affinity numa nodes while exclusive is enabled")
		}
		return hints, nil
	}

	// 2. Get affinityInfo of all numa nodes and the pod to be binded
	numaAffinityInfoList, err := p.getNumaNodesAffinityInfo()
	if err != nil {
		return nil, err
	}

	// 3. filter hints
	state, err := affinityutil.GetPodAffinityInfo(req, podAffinity, numaAffinityInfoList, p.topology)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return hints, nil
	}

	hints = affinityutil.PodAffinityFilter(state, hints)
	return hints, nil
}

// Get affinityInfo of all numa nodes
func (p *DynamicPolicy) getNumaNodesAffinityInfo() ([]affinityutil.NumaInfo, error) {
	numaResourceMap := p.state.GetMachineState()
	var numaNodesInfo []affinityutil.NumaInfo

	for i := 0; i < p.topology.NumNUMANodes; i++ {
		var numaNodeInfo affinityutil.NumaInfo
		numaNodeInfo.NumaID = i
		numaNodeInfo.Labels = make(map[string][]string)
		cpuSet := p.topology.CPUDetails.SocketsInNUMANodes(i)
		if cpuSet.Size() == 0 {
			return nil, fmt.Errorf("failed to find the associated socket ID for the specified numanode: %d, cpuDetails: %v", i, p.topology.CPUDetails)
		}
		numaNodeInfo.SocketID = cpuSet.ToSliceInt()[0]

		numaState := numaResourceMap[v1.ResourceMemory][i]
		for _, containerEntries := range numaState.PodEntries {
			for _, allocationInfo := range containerEntries {
				if allocationInfo.Annotations[apiconsts.PodAnnotationQoSLevelKey] != apiconsts.PodAnnotationQoSLevelDedicatedCores {
					continue
				}
				labels := p.qosConfig.FilterAffinityLabels(allocationInfo.Labels)
				numaNodeInfo.Labels = affinityutil.MergeNumaInfoMap(labels, numaNodeInfo.Labels)
				if allocationInfo.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaExclusive] == apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable {
					numaNodeInfo.Exclusive = true
				} else {
					numaNodeInfo.Exclusive = false
				}

				if affinityutil.IsPodAntiAffinity(allocationInfo.Annotations) {
					podAffinity, err := affinityutil.UnmarshalAffinity(allocationInfo.Annotations)
					if err != nil {
						return nil, fmt.Errorf("unmarshalAffinity failed")
					}
					if podAffinity.AntiAffinity.Required != nil {
						numaNodeInfo.AntiAffinityRequiredSelectors = append(numaNodeInfo.AntiAffinityRequiredSelectors,
							podAffinity.AntiAffinity.Required...)
					}
				}
				break
			}
		}

		numaNodesInfo = append(numaNodesInfo, numaNodeInfo)
	}

	return numaNodesInfo, nil
}

// regenerateHints regenerates hints for container that'd already been allocated memory,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func regenerateHints(reqInt uint64, allocationInfo *state.AllocationInfo) map[string]*pluginapi.ListOfTopologyHints {
	hints := map[string]*pluginapi.ListOfTopologyHints{}

	allocatedInt := allocationInfo.AggregatedQuantity
	if allocatedInt < reqInt {
		general.ErrorS(nil, "memory's already allocated with smaller quantity than requested",
			"podUID", allocationInfo.PodUid, "containerName", allocationInfo.ContainerName,
			"requestedResource", reqInt, "allocatedSize", allocatedInt)
		return nil
	}

	allocatedNumaNodes := make([]uint64, 0, len(allocationInfo.TopologyAwareAllocations))
	for numaNode, quantity := range allocationInfo.TopologyAwareAllocations {
		if quantity > 0 {
			allocatedNumaNodes = append(allocatedNumaNodes, uint64(numaNode))
		}
	}

	general.InfoS("regenerating topology hints, memory was already allocated to pod",
		"podNamespace", allocationInfo.PodNamespace,
		"podName", allocationInfo.PodName,
		"containerName", allocationInfo.ContainerName,
		"hint", allocatedNumaNodes)
	hints[string(v1.ResourceMemory)] = &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{
				Nodes:     allocatedNumaNodes,
				Preferred: true,
			},
		},
	}
	return hints
}
