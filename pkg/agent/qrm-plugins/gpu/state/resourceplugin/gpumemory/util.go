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

	"github.com/kubewharf/katalyst-api/pkg/consts"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

func GenerateMachineState(
	conf *qrm.QRMPluginsConfiguration, topologyRegistry *machine.DeviceTopologyRegistry,
) (GPUMap, error) {
	if topologyRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	gpuTopology, _, err := topologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceName)
	if err != nil {
		return nil, fmt.Errorf("gpu topology provider failed with error: %v", err)
	}

	gpuMap := make(GPUMap)
	for deviceID := range gpuTopology.Devices {
		gpuMap[deviceID] = &GPUMemoryState{
			GPUMemoryAllocatable: float64(conf.GPUQRMPluginConfig.GPUMemoryAllocatablePerGPU.Value()),
			GPUMemoryAllocated:   0,
		}
	}

	return gpuMap, nil
}

// GenerateMachineStateFromPodEntries returns GPUMap for gpu memory based on
// machine info along with existed pod entries
func GenerateMachineStateFromPodEntries(
	conf *qrm.QRMPluginsConfiguration,
	podEntries PodEntries,
	topologyRegistry *machine.DeviceTopologyRegistry,
) (GPUMap, error) {
	machineState, err := GenerateMachineState(conf, topologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	for gpuID, gpuState := range machineState {
		var gpuMemoryAllocated float64
		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					gpuAllocation, ok := allocationInfo.TopologyAwareAllocations[gpuID]
					if !ok {
						continue
					}
					alloc := allocationInfo.Clone()
					alloc.AllocatedAllocation = gpuAllocation.Clone()
					alloc.TopologyAwareAllocations = map[string]GPUAllocation{gpuID: gpuAllocation}
					gpuMemoryAllocated += gpuAllocation.GPUMemoryQuantity
					gpuState.SetAllocationInfo(podUID, containerName, alloc)
				}
			}
		}
		gpuState.GPUMemoryAllocated = gpuMemoryAllocated

		generalLog := general.LoggerWithPrefix("GenerateMachineStateFromPodEntries", general.LoggingPKGFull)
		if gpuState.GPUMemoryAllocatable < gpuState.GPUMemoryAllocated {
			generalLog.Warningf("invalid allocated GPU memory: %f on GPU: %s"+
				" with allocatable GPU memory size: %f", gpuState.GPUMemoryAllocated, gpuID, gpuState.GPUMemoryAllocatable)
		}
		machineState[gpuID] = gpuState
	}

	return machineState, nil
}

// RegenerateGPUMemoryHints regenerates hints for container that'd already been allocated gpu memory,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func RegenerateGPUMemoryHints(
	allocationInfo *AllocationInfo, regenerate bool,
) map[string]*pluginapi.ListOfTopologyHints {
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
