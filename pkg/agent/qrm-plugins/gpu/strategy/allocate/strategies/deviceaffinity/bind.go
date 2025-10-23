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

package deviceaffinity

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/strings/slices"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Bind binds the sorted devices to the allocation context by searching for the devices that have affinity to each other.
func (s *DeviceAffinityStrategy) Bind(
	ctx *allocate.AllocationContext, sortedDevices []string,
) (*allocate.AllocationResult, error) {
	valid, errMsg := strategies.IsBindingContextValid(ctx, sortedDevices)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf(errMsg)
	}

	devicesToAllocate := int(ctx.DeviceReq.DeviceRequest)

	// When we bind devices, we need to make sure that the devices have affinity to each other
	allocatedDevices := sets.NewString()
	allocateDevices := func(devices ...string) (bool, error) {
		for _, firstDevice := range devices {
			if devicesToAllocate == allocatedDevices.Len() {
				return true, nil
			}
			if allocatedDevices.Has(firstDevice) {
				continue
			}
			// Get all the devices that have affinity to the device
			affinityMap, err := ctx.DeviceTopology.GetDeviceAffinityMap(firstDevice)
			if err != nil {
				return false, err
			}

			// Loop from the highest priority to the lowest priority
			for priority := 0; priority < len(affinityMap); priority++ {
				// Check if the affinity device is in the list of devices to allocate
				affinityPriorityDevices, ok := affinityMap[machine.AffinityPriority(priority)]
				if !ok {
					return false, fmt.Errorf("affinity priority %d not found", priority)
				}
				for _, device := range affinityPriorityDevices {
					if slices.Contains(devices, device) {
						allocatedDevices.Insert(device)
					}
				}
			}
		}
		return false, nil
	}

	// Allocate reusable devices first
	finishedAllocation, err := allocateDevices(ctx.DeviceReq.ReusableDevices...)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to allocate reusable devices: %v", err),
		}, fmt.Errorf("failed to allocate reusable devices: %v", err)
	}

	if finishedAllocation {
		return &allocate.AllocationResult{
			Success:          true,
			AllocatedDevices: allocatedDevices.UnsortedList(),
		}, nil
	}

	// Then try to bind devices from sorted list
	finishedAllocation, err = allocateDevices(sortedDevices...)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to allocate sorted devices: %v", err),
		}, fmt.Errorf("failed to allocate sorted devices: %v", err)
	}

	if finishedAllocation {
		general.InfoS("Successfully bound devices",
			"podNamespace", ctx.ResourceReq.PodNamespace,
			"podName", ctx.ResourceReq.PodName,
			"containerName", ctx.ResourceReq.ContainerName,
			"allocatedDevices", allocatedDevices.List())

		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: fmt.Sprintf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices)),
	}, fmt.Errorf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices))
}
