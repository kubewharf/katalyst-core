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

package util

import (
	"fmt"
	"math"

	pkgerrors "github.com/pkg/errors"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var ErrNoAvailableGPUMemoryHints = pkgerrors.New("no available gpu memory hints")

// RegenerateGPUMemoryHints regenerates hints for container that'd already been allocated gpu memory,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func RegenerateGPUMemoryHints(allocationInfo *state.AllocationInfo, regenerate bool) map[string]*pluginapi.ListOfTopologyHints {
	if allocationInfo == nil {
		general.Errorf("RegenerateHints got nil allocationInfo")
		return nil
	}

	hints := map[string]*pluginapi.ListOfTopologyHints{}
	if regenerate {
		general.ErrorS(nil, "need to regenerate hints",
			"podNamespace", allocationInfo.PodNamespace,
			"podName", allocationInfo.PodName,
			"podUID", allocationInfo.PodUid,
			"containerName", allocationInfo.ContainerName)

		return nil
	}

	allocatedNumaNodes := machine.NewCPUSet(allocationInfo.AllocatedAllocation.NUMANodes...)

	general.InfoS("regenerating machineInfo hints, gpu memory was already allocated to pod",
		"podNamespace", allocationInfo.PodNamespace,
		"podName", allocationInfo.PodName,
		"containerName", allocationInfo.ContainerName,
		"hint", allocatedNumaNodes)
	hints[string(consts.ResourceGPUMemory)] = &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{
				Nodes:     allocatedNumaNodes.ToSliceUInt64(),
				Preferred: true,
			},
		},
	}
	return hints
}

func GetNUMANodesCountToFitGPUReq(gpuReq float64, cpuTopology *machine.CPUTopology, gpuTopology *machine.GPUTopology) (int, int, error) {
	if gpuTopology == nil {
		return 0, 0, fmt.Errorf("GetNUMANodesCountToFitGPUReq got nil gpuTopology")
	}

	numaCount := cpuTopology.NumNUMANodes
	if numaCount == 0 {
		return 0, 0, fmt.Errorf("there is no NUMA in cpuTopology")
	}

	if len(gpuTopology.GPUs)%numaCount != 0 {
		general.Warningf("GPUs count %d cannot be evenly divisible by NUMA count %d", len(gpuTopology.GPUs), numaCount)
	}

	gpusPerNUMA := (len(gpuTopology.GPUs) + numaCount - 1) / numaCount
	numaCountNeeded := int(math.Ceil(gpuReq / float64(gpusPerNUMA)))
	if numaCountNeeded == 0 {
		numaCountNeeded = 1
	}
	if numaCountNeeded > numaCount {
		return 0, 0, fmt.Errorf("invalid gpu req: %.3f in topology with NUMAs count: %d and GPUs count: %d", gpuReq, numaCount, len(gpuTopology.GPUs))
	}

	gpusCountNeededPerNUMA := int(math.Ceil(gpuReq / float64(numaCountNeeded)))
	return numaCountNeeded, gpusCountNeededPerNUMA, nil
}
