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
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/bitmask"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (p *DynamicPolicy) sharedCoresHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("got nil request")
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): nil, // indicates that there is no numa preference
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

func (p *DynamicPolicy) dedicatedCoresWithNUMABindingHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	// currently, we set cpuset of sidecar to the cpuset of its main container,
	// so there is no numa preference here.
	if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	var hints map[string]*pluginapi.ListOfTopologyHints
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	machineState := p.state.GetMachineState()

	if allocationInfo != nil {
		hints = regenerateHints(allocationInfo, reqInt)

		// regenerateHints failed. need to clear container record and re-calculate.
		if hints == nil {
			podEntries := p.state.GetPodEntries()
			delete(podEntries[req.PodUid], req.ContainerName)
			if len(podEntries[req.PodUid]) == 0 {
				delete(podEntries, req.PodUid)
			}

			var err error
			machineState, err = state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
			if err != nil {
				klog.Errorf("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingHintHandler] pod: %s/%s, container: %s GenerateCPUMachineStateByPodEntries failed with error: %v",
					req.PodNamespace, req.PodName, req.ContainerName, err)
				return nil, fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
			}
		}
	}

	if hints == nil {
		var extraErr error
		hints, extraErr = util.GetHintsFromExtraStateFile(req.PodName, string(v1.ResourceCPU), p.extraStateFileAbsPath)
		if extraErr != nil {
			klog.Infof("[CPUDynamicPolicy.dedicatedCoresWithNUMABindingHintHandler] pod: %s/%s, container: %s GetHintsFromExtraStateFile failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, extraErr)
		}
	}

	if hints == nil {
		var calculateErr error
		// calculate hint for container without allocated cpus
		hints, calculateErr = p.calculateHints(reqInt, machineState)

		if calculateErr != nil {
			return nil, fmt.Errorf("calculateHints failed with error: %v", calculateErr)
		}
	}

	return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), hints)
}

func (p *DynamicPolicy) dedicatedCoresWithoutNUMABindingHintHandler(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	// todo: support dedicated_cores without NUMA binding
	return nil, fmt.Errorf("not support dedicated_cores without NUMA binding")
}

// calculateHints is a helper function to calculate the topology hints
// with the given container requests.
func (p *DynamicPolicy) calculateHints(reqInt int, machineState state.NUMANodeMap) (map[string]*pluginapi.ListOfTopologyHints, error) {
	numaNodes := make([]int, 0, len(machineState))
	for numaNode := range machineState {
		numaNodes = append(numaNodes, numaNode)
	}
	sort.Ints(numaNodes)

	hints := map[string]*pluginapi.ListOfTopologyHints{
		string(v1.ResourceCPU): {
			Hints: []*pluginapi.TopologyHint{},
		},
	}

	minNUMAsCountNeeded, _, err := util.GetNUMANodesCountToFitCPUReq(reqInt, p.machineInfo.CPUTopology)
	if err != nil {
		return nil, fmt.Errorf("GetNUMANodesCountToFitCPUReq failed with error: %v", err)
	}

	numaPerSocket, err := p.machineInfo.NUMAsPerSocket()
	if err != nil {
		return nil, fmt.Errorf("NUMAsPerSocket failed with error: %v", err)
	}

	bitmask.IterateBitMasks(numaNodes, func(mask bitmask.BitMask) {
		if mask.Count() < minNUMAsCountNeeded {
			return
		}

		maskBits := mask.GetBits()
		numaCountNeeded := mask.Count()

		allAvailableCPUsInMask := machine.NewCPUSet()
		for _, nodeID := range maskBits {
			if machineState[nodeID] == nil {
				klog.Warningf("[CPUDynamicPolicy.calculateHints] NUMA: %d has nil state", nodeID)
				return
			}

			allAvailableCPUsInMask = allAvailableCPUsInMask.Union(machineState[nodeID].GetAvailableCPUSet(p.reservedCPUs))
		}

		if allAvailableCPUsInMask.Size() < reqInt {
			klog.V(4).Infof("[CPUDynamicPolicy.calculateHints] available cpuset: %s of size: %d "+
				"excluding NUMA binding pods which is smaller than request: %d",
				allAvailableCPUsInMask.String(), allAvailableCPUsInMask.Size(), reqInt)
			return
		}

		crossSockets, err := machine.CheckNUMACrossSockets(maskBits, p.machineInfo.CPUTopology)
		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.calculateHints] CheckNUMACrossSockets failed with error: %v", err)
			return
		} else if numaCountNeeded <= numaPerSocket && crossSockets {
			klog.V(4).Infof("[CPUDynamicPolicy.calculateHints] needed: %d; min-needed: %d; NUMAs: %v cross sockets with numaPerSocket: %d",
				numaCountNeeded, minNUMAsCountNeeded, maskBits, numaPerSocket)
			return
		}

		hints[string(v1.ResourceCPU)].Hints = append(hints[string(v1.ResourceCPU)].Hints, &pluginapi.TopologyHint{
			Nodes:     util.MaskToUInt64Array(mask),
			Preferred: len(maskBits) == minNUMAsCountNeeded,
		})
	})

	return hints, nil
}

// regenerateHints re-generates for container already allocated cpus
func regenerateHints(allocationInfo *state.AllocationInfo, reqInt int) map[string]*pluginapi.ListOfTopologyHints {
	hints := map[string]*pluginapi.ListOfTopologyHints{}
	if allocationInfo.OriginalAllocationResult.Size() < reqInt {
		klog.ErrorS(nil, "[CPUDynamicPolicy.regenerateHints] cpus already allocated with smaller quantity than requested",
			"podUID", allocationInfo.PodUid,
			"containerName", allocationInfo.ContainerName,
			"requestedResource", reqInt,
			"allocatedSize", allocationInfo.OriginalAllocationResult.Size())

		return nil
	}

	allocatedNumaNodes := make([]uint64, 0, len(allocationInfo.TopologyAwareAssignments))
	for numaNode, cset := range allocationInfo.TopologyAwareAssignments {
		if cset.Size() > 0 {
			allocatedNumaNodes = append(allocatedNumaNodes, uint64(numaNode))
		}
	}

	klog.InfoS("[CPUDynamicPolicy.regenerateHints] regenerating machineInfo hints, cpus was already allocated to pod",
		"podNamespace", allocationInfo.PodNamespace,
		"podName", allocationInfo.PodName,
		"containerName", allocationInfo.ContainerName,
		"hint", allocatedNumaNodes)

	hints[string(v1.ResourceCPU)] = &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{
				Nodes:     allocatedNumaNodes,
				Preferred: true,
			},
		},
	}
	return hints
}
