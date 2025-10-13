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

package state

import (
	"fmt"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

// GenerateMachineState returns an empty AllocationResourcesMap for all resource names.
func GenerateMachineState(topologyRegistry *machine.DeviceTopologyRegistry) (AllocationResourcesMap, error) {
	if topologyRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	deviceNameToProvider := topologyRegistry.DeviceNameToProvider
	allocationResourcesMap := make(AllocationResourcesMap)

	for resourceName := range deviceNameToProvider {
		allocationMap, err := GenerateResourceState(topologyRegistry, v1.ResourceName(resourceName))
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceState for resource %s failed with error: %v", resourceName, err)
		}
		allocationResourcesMap[v1.ResourceName(resourceName)] = allocationMap
	}

	return allocationResourcesMap, nil
}

// GenerateMachineStateFromPodEntries returns AllocationResourcesMap for allocated resources based on
// resource name along with existed pod resource entries.
func GenerateMachineStateFromPodEntries(
	podResourceEntries PodResourceEntries,
	topologyRegistry *machine.DeviceTopologyRegistry,
) (AllocationResourcesMap, error) {
	machineState, err := GenerateMachineState(topologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	for resourceName, podEntries := range podResourceEntries {
		allocationMap, err := GenerateResourceStateFromPodEntries(podEntries, topologyRegistry, resourceName)
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceStateFromPodEntries for resource %s failed with error: %v", resourceName, err)
		}
		machineState[resourceName] = allocationMap
	}

	return machineState, nil
}

// GenerateResourceStateFromPodEntries returns an AllocationMap of a certain resource based on pod entries
func GenerateResourceStateFromPodEntries(
	podEntries PodEntries, topologyRegistry *machine.DeviceTopologyRegistry, resourceName v1.ResourceName,
) (AllocationMap, error) {
	machineState, err := GenerateResourceState(topologyRegistry, resourceName)
	if err != nil {
		return nil, fmt.Errorf("GenerateResourceState failed with error: %v", err)
	}

	for deviceID, allocationState := range machineState {
		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil {
					allocation, ok := allocationInfo.TopologyAwareAllocations[deviceID]
					if !ok {
						continue
					}
					alloc := allocationInfo.Clone()
					alloc.AllocatedAllocation = allocation.Clone()
					alloc.TopologyAwareAllocations = map[string]Allocation{deviceID: allocation}
					allocationState.SetAllocationInfo(podUID, containerName, alloc)
				}
			}
		}
		machineState[deviceID] = allocationState
	}

	return machineState, nil
}

// GenerateResourceState returns an empty AllocationMap for a certain resource.
func GenerateResourceState(
	topologyProviderRegistry *machine.DeviceTopologyRegistry, resourceName v1.ResourceName,
) (AllocationMap, error) {
	if topologyProviderRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	topology, _, err := topologyProviderRegistry.GetDeviceTopology(string(resourceName))
	if err != nil {
		return nil, fmt.Errorf("topology provider registry failed with error: %v", err)
	}

	resourceState := make(AllocationMap)
	for deviceID := range topology.Devices {
		resourceState[deviceID] = &AllocationState{
			PodEntries: make(PodEntries),
		}
	}
	return resourceState, nil
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
