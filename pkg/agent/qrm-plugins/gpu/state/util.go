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

	"github.com/kubewharf/katalyst-core/pkg/util/general"
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
		if !generator.MustInitDefaultResourceState() {
			general.Infof("skipping generator for resource %s as we do not need to init default resource state", resourceName)
			continue
		}

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
	previous AllocationResourcesMap,
) (AllocationResourcesMap, error) {
	if defaultMachineStateGenerators == nil {
		return nil, fmt.Errorf("cannot generate machine state from nil resourceStateGeneratorRegistry")
	}

	machineState := make(AllocationResourcesMap)
	for resourceName, podEntries := range podResourceEntries {
		generator, ok := defaultMachineStateGenerators.GetGenerator(string(resourceName))
		if !ok {
			general.Warningf("cannot find generator for resource %s", resourceName)
			continue
		}

		allocationMap, err := GenerateResourceStateFromPodEntries(podEntries, generator, previous[resourceName])
		if err != nil {
			return nil, fmt.Errorf("GenerateResourceStateFromPodEntries for resource %s failed with error: %v", resourceName, err)
		}
		machineState[resourceName] = allocationMap
	}

	// fill in the rest of the resources with default state
	for resourceName, generator := range defaultMachineStateGenerators.GetGenerators() {
		if _, ok := machineState[v1.ResourceName(resourceName)]; ok {
			continue
		}

		// Skip generators that opt out of default resource state initialization
		if !generator.MustInitDefaultResourceState() {
			general.Infof("skipping generator for resource %s as we do not need to init default resource state", resourceName)
			continue
		}

		allocationMap, err := generator.GenerateDefaultResourceState()
		if err != nil {
			return nil, fmt.Errorf("GenerateDefaultResourceState for resource %s failed with error: %v", resourceName, err)
		}
		machineState[v1.ResourceName(resourceName)] = allocationMap
	}

	return machineState, nil
}

// GenerateResourceStateFromPodEntries rebuilds a resource from current topology and pod entries.
// Missing devices remain while referenced by pod entries; previous supplies their capacity only.
func GenerateResourceStateFromPodEntries(
	podEntries PodEntries,
	generator DefaultResourceStateGenerator,
	previous AllocationMap,
) (AllocationMap, error) {
	devices, err := generator.GenerateDefaultResourceState()
	if err != nil {
		return nil, fmt.Errorf("GenerateDefaultResourceState failed with error: %w", err)
	}
	if devices == nil {
		devices = make(AllocationMap)
	}

	for podUID, containers := range podEntries {
		for containerName, info := range containers {
			if containerName == "" || info == nil {
				continue
			}
			for id, allocation := range info.TopologyAwareAllocations {
				device := devices[id]
				if device == nil {
					old := previous[id]
					if old == nil {
						general.Warningf("device %s allocated to pod %s container %s is absent from current topology and previous state; skipping device", id, podUID, containerName)
						continue
					}
					device = &AllocationState{Allocatable: old.Allocatable}
					devices[id] = device
				}

				perDevice := info.Clone()
				perDevice.AllocatedAllocation = allocation.Clone()
				perDevice.TopologyAwareAllocations = map[string]Allocation{id: allocation}
				device.SetAllocationInfo(podUID, containerName, perDevice)
			}
		}
	}
	return devices, nil
}

type genericDefaultResourceStateGenerator struct {
	deviceNames                  []string
	topologyRegistry             *machine.DeviceTopologyRegistry
	allocatable                  float64
	mustInitDefaultResourceState bool
}

func NewGenericDefaultResourceStateGenerator(
	deviceNames []string,
	topologyRegistry *machine.DeviceTopologyRegistry,
	allocatable float64,
	mustInitDefaultResourceState bool,
) DefaultResourceStateGenerator {
	return &genericDefaultResourceStateGenerator{
		deviceNames:                  deviceNames,
		topologyRegistry:             topologyRegistry,
		allocatable:                  allocatable,
		mustInitDefaultResourceState: mustInitDefaultResourceState,
	}
}

// MustInitDefaultResourceState returns true if we need to initialize the default resource state at the start
func (g *genericDefaultResourceStateGenerator) MustInitDefaultResourceState() bool {
	return g.mustInitDefaultResourceState
}

// GenerateDefaultResourceState return a default resource state by topology
func (g *genericDefaultResourceStateGenerator) GenerateDefaultResourceState() (AllocationMap, error) {
	if g == nil {
		return nil, fmt.Errorf("nil DefaultResourceStateGenerator")
	}

	if g.topologyRegistry == nil {
		return nil, fmt.Errorf("topology provider registry must not be nil")
	}

	// We pick the latest topology from multiple device names to generate a single unified state
	latestTopology, _, err := g.topologyRegistry.GetLatestDeviceTopology(g.deviceNames)
	if err != nil {
		return nil, fmt.Errorf("topology provider registry failed to get latest topology: %v", err)
	}

	resourceState := make(AllocationMap)
	for deviceID := range latestTopology.Devices {
		resourceState[deviceID] = &AllocationState{
			PodEntries:  make(PodEntries),
			Allocatable: g.allocatable,
		}
	}

	return resourceState, nil
}
