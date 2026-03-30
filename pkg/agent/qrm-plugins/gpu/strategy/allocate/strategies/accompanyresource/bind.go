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

package accompanyresource

import (
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Bind tries to allocate devices by maximizing affinity with the accompany resource devices, making sure that it is
// allocated proportionally to the accompany resource.
func (s *AccompanyResourceStrategy) Bind(ctx *allocate.AllocationContext, sortedDevices []string) (*allocate.AllocationResult, error) {
	general.InfoS("accompany device strategy binding called",
		"available devices", sortedDevices)

	valid, errMsg := strategies.IsBindingContextValid(ctx, sortedDevices)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf(errMsg)
	}

	accompanyResourceName := ctx.AccompanyResourceName
	// Use generic canonical strategy to bind devices if there is no accompany resource name
	if accompanyResourceName == "" {
		general.Infof("No accompany resource name specified, using fallback canonical strategy to bind devices")
		return s.CanonicalStrategy.Bind(ctx, sortedDevices)
	}

	// resourceName is the name of the target resource to be allocated
	resourceName := ctx.ResourceName

	// Allocate all the reusable devices first
	allocatedDevices := sets.NewString(ctx.DeviceReq.ReusableDevices...)
	machineState := ctx.MachineState

	// Find the name of accompany resource in state
	accompanyResourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, accompanyResourceName, false)
	if accompanyResourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid accompany resource %s in state", accompanyResourceName),
		}, fmt.Errorf("invalid accompany resource %s in state", accompanyResourceName)
	}

	// Get the allocated accompany resource devices from machine state
	accompanyAllocatedDeviceIDs := machineState.GetAllocatedDeviceIDs(v1.ResourceName(accompanyResourceNameInState), ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName)
	if len(accompanyAllocatedDeviceIDs) == 0 {
		general.Infof("No accompany resource device found for pod %s, container %s, resource %s, falling back to canonical strategy",
			ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName, accompanyResourceNameInState)
		return s.CanonicalStrategy.Bind(ctx, sortedDevices)
	}

	// Find the name of resource in state
	resourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, resourceName, false)
	if resourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid resource %s in state", resourceName),
		}, fmt.Errorf("invalid resource %s in state", resourceName)
	}

	// Get ratio of accompany resource to target device
	accompanyResourceToDeviceRatio := machineState.GetRatioOfAccompanyResourceToTargetResource(accompanyResourceNameInState, resourceNameInState)

	// Find out the number of target devices to be allocated proportionally to the accompany resource devices
	ratio := accompanyResourceToDeviceRatio
	if ratio <= 0 {
		ratio = 1
	}

	devicesNeededFloat := float64(len(accompanyAllocatedDeviceIDs)) / ratio
	devicesToBeAllocated := int(math.Floor(devicesNeededFloat))
	// Should have a minimum of 1 device to be allocated at all times
	if devicesToBeAllocated < 1 {
		devicesToBeAllocated = 1
	}

	if allocatedDevices.Len() >= devicesToBeAllocated {
		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	// Obtain the target devices that have affinity with the accompany resource devices
	affinityDevices, err := ctx.DeviceTopologyRegistry.GetAffinityDevices(accompanyResourceName, resourceName)
	if err != nil {
		general.Warningf("failed to get affinity devices between %s and %s: %v", accompanyResourceName, resourceName, err)
		return nil, fmt.Errorf("failed to get affinity devices between %s and %s: %w", accompanyResourceName, resourceName, err)
	}

	// Get the priority dimensions from the accompany resource's topology
	var priorityDimensions []string
	accompanyTopology, err := ctx.DeviceTopologyRegistry.GetDeviceTopology(accompanyResourceName)
	if err == nil && accompanyTopology != nil {
		priorityDimensions = accompanyTopology.PriorityDimensions
	}

	general.Infof("Get affinity devices from allocated devices: %v", affinityDevices)

	availableDevices := sets.NewString(sortedDevices...)

	// Choose allocation path based on presence of affinity info
	// If there are no affinity devices, simply allocate available devices in best-effort manner.
	if len(affinityDevices) == 0 {
		general.Infof("No affinity devices found, allocating devices using best effort manner")
		return s.allocateWithoutAffinity(ctx, sortedDevices, allocatedDevices, devicesToBeAllocated)
	}

	// maxAllocationPerDevice is the maximum number of target devices that can be allocated to each accompany resource device.
	// This is to uniformly distribute the target devices to each accompany resource device.
	maxAllocationPerDevice := int(math.Max(1/accompanyResourceToDeviceRatio, 1))
	return s.allocateWithAffinity(ctx, accompanyAllocatedDeviceIDs, affinityDevices, priorityDimensions, availableDevices, allocatedDevices, devicesToBeAllocated, maxAllocationPerDevice, accompanyResourceName)
}

// allocateWithoutAffinity simply allocates available devices in order until the target count is satisfied.
func (s *AccompanyResourceStrategy) allocateWithoutAffinity(ctx *allocate.AllocationContext, sortedDevices []string,
	allocatedDevices sets.String, targetDeviceToBeAllocated int,
) (*allocate.AllocationResult, error) {
	for _, device := range sortedDevices {
		allocatedDevices.Insert(device)
		if allocatedDevices.Len() >= targetDeviceToBeAllocated {
			general.InfoS("Successfully bound devices",
				"podNamespace", ctx.ResourceReq.PodNamespace,
				"podName", ctx.ResourceReq.PodName,
				"containerName", ctx.ResourceReq.ContainerName,
				"allocatedDevices", allocatedDevices.List())

			return &allocate.AllocationResult{AllocatedDevices: allocatedDevices.UnsortedList(), Success: true}, nil
		}
	}

	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: fmt.Sprintf("not enough devices to allocate (no affinity): need %d, have %d", targetDeviceToBeAllocated, len(allocatedDevices)),
	}, fmt.Errorf("not enough devices to allocate (no affinity): need %d, have %d", targetDeviceToBeAllocated, len(allocatedDevices))
}

// allocateWithAffinity tries to allocate devices that have affinity with the allocated accompany devices.
func (s *AccompanyResourceStrategy) allocateWithAffinity(
	ctx *allocate.AllocationContext,
	allocatedAccompanyDeviceIDs []string,
	affinityByDevice map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
	available sets.String,
	selected sets.String,
	devicesToAllocate int,
	perAccompanyLimit int,
	accompanyResourceName string,
) (*allocate.AllocationResult, error) {
	// Iterate each accompany resource device and bind target devices that have affinity with it.
	for _, accompanyID := range allocatedAccompanyDeviceIDs {
		var allocatedForCurrent int
		affinityForDevice, ok := affinityByDevice[accompanyID]
		if !ok {
			return &allocate.AllocationResult{
				Success:      false,
				ErrorMessage: fmt.Sprintf("accompany resource not found %s in affinity devices map, accompanyDeviceId: %s", accompanyResourceName, accompanyID),
			}, fmt.Errorf("accompany resource not found %s in affinity devices map, accompanyDeviceId: %s", accompanyResourceName, accompanyID)
		}

	PriorityLoop:
		for _, dimName := range priorityDimensions {
			deviceIDs, ok := affinityForDevice[dimName]
			if !ok {
				continue
			}
			for _, deviceID := range deviceIDs {
				// Skip devices that are not available or already selected
				if !available.Has(deviceID) || selected.Has(deviceID) {
					continue
				}

				allocatedForCurrent++
				selected.Insert(deviceID)
				if selected.Len() >= devicesToAllocate {
					general.InfoS("Successfully bound devices",
						"podNamespace", ctx.ResourceReq.PodNamespace,
						"podName", ctx.ResourceReq.PodName,
						"containerName", ctx.ResourceReq.ContainerName,
						"allocatedDevices", selected.List())

					return &allocate.AllocationResult{AllocatedDevices: selected.UnsortedList(), Success: true}, nil
				}

				// Move to the next accompany device once the perAccompanyLimit threshold is reached.
				if allocatedForCurrent >= perAccompanyLimit {
					break PriorityLoop
				}
			}
		}
	}

	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: fmt.Sprintf("not enough devices to allocate: need %d, have %d", devicesToAllocate, len(selected)),
	}, fmt.Errorf("not enough devices to allocate: need %d, have %d", devicesToAllocate, len(selected))
}
