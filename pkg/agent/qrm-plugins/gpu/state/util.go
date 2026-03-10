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

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GenerateMachineState returns an empty AllocationResourcesMap for all resource names.
func GenerateMachineState(
	defaultMachineStateGenerators *DefaultResourceStateGeneratorRegistry,
) (AllocationResourcesMap, error) {
	if defaultMachineStateGenerators == nil {
		return nil, fmt.Errorf("cannot generate machine state from nil defaultMachineStateGenerators")
	}

	allocationResourcesMap := make(AllocationResourcesMap)
	for resourceName, generator := range defaultMachineStateGenerators.GetGenerators() {
		allocationMap, err := generator.GenerateDefaultResourceState()
		if err != nil {
			return nil, fmt.Errorf("GenerateDefaultResourceState for resource %s failed with error: %v", resourceName, err)
		}
		allocationResourcesMap[v1.ResourceName(resourceName)] = allocationMap
	}

	return allocationResourcesMap, nil
}

// GenerateMachineStateFromPodEntries returns AllocationResourcesMap for allocated resources based on
// resource name along with existed pod resource entries.
func GenerateMachineStateFromPodEntries(
	podResourceEntries PodResourceEntries,
	defaultMachineStateGenerators *DefaultResourceStateGeneratorRegistry,
) (AllocationResourcesMap, error) {
	if defaultMachineStateGenerators == nil {
		return nil, fmt.Errorf("cannot generate machine state from nil resourceStateGeneratorRegistry")
	}

	machineState := make(AllocationResourcesMap)
	for resourceName, podEntries := range podResourceEntries {
		generator, ok := defaultMachineStateGenerators.GetGenerator(string(resourceName))
		if !ok {
			return nil, fmt.Errorf("GetGenerator for resource %s failed", resourceName)
		}

		allocationMap, err := GenerateResourceStateFromPodEntries(podEntries, generator)
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceStateFromPodEntries for resource %s failed with error: %v", resourceName, err)
		}
		machineState[resourceName] = allocationMap
	}

	return machineState, nil
}

// GenerateResourceStateFromPodEntries returns an AllocationMap of a certain resource based on pod entries
func GenerateResourceStateFromPodEntries(
	podEntries PodEntries,
	generator DefaultResourceStateGenerator,
) (AllocationMap, error) {
	machineState, err := generator.GenerateDefaultResourceState()
	if err != nil {
		return nil, fmt.Errorf("GenerateDefaultResourceState failed with error: %v", err)
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

type genericDefaultResourceStateGenerator struct {
	resourceName     string
	topologyRegistry *machine.DeviceTopologyRegistry
}

func NewGenericDefaultResourceStateGenerator(
	resourceName string,
	topologyRegistry *machine.DeviceTopologyRegistry,
) DefaultResourceStateGenerator {
	return &genericDefaultResourceStateGenerator{resourceName: resourceName, topologyRegistry: topologyRegistry}
}

// GenerateDefaultResourceState return a default resource state by topology
func (g *genericDefaultResourceStateGenerator) GenerateDefaultResourceState() (AllocationMap, error) {
	if g == nil {
		return nil, fmt.Errorf("nil DefaultResourceStateGenerator")
	}

	if g.topologyRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	topology, _, err := g.topologyRegistry.GetDeviceTopology(g.resourceName)
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
