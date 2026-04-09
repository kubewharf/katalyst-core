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
	"sort"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	// metricNameNoDeviceTopologyAffinity is the metric name to record the number of instances when devices have no topology affinity
	metricNameNoDeviceTopologyAffinity = "no_device_topology_affinity"
)

// affinityGroup is a group of devices that have affinity to each other.
// It is uniquely identified by an id.
type affinityGroup struct {
	id                 string
	unallocatedDevices sets.String
	totalDevicesNum    int
}

// Bind binds the sorted devices to the allocation context by searching for the devices that have affinity to each other.
func (s *DeviceAffinityStrategy) Bind(
	ctx *allocate.AllocationContext, sortedDevices []string,
) (*allocate.AllocationResult, error) {
	general.InfoS("device affinity strategy binding called",
		"available devices", sortedDevices)

	valid, errMsg := strategies.IsBindingContextValid(ctx, sortedDevices)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf(errMsg)
	}

	devicesToAllocate := int(ctx.DeviceReq.DeviceRequest)
	reusableDevicesSet := sets.NewString(ctx.DeviceReq.ReusableDevices...)

	requiredDeviceAffinity := ctx.GPUQRMPluginConfig.RequiredDeviceAffinity

	// All devices that are passed into the strategy are unallocated devices
	unallocatedDevicesSet := sets.NewString(sortedDevices...)

	// Get affinity groups ordered from the highest priority to the lowest priority.
	affinityLevels := ctx.DeviceTopology.GroupDeviceAffinity()

	// If there is no topology affinity, fallback to generic canonical strategy
	if len(affinityLevels) == 0 {
		// return error if device affinity is required but no topology affinity is found
		if requiredDeviceAffinity {
			return &allocate.AllocationResult{
				Success:      false,
				ErrorMessage: fmt.Sprintf("no topology affinity found but device affinity is required"),
			}, fmt.Errorf("no topology affinity found but device affinity is required")
		}

		general.Warningf("no topology affinity found, fallback to canonical strategy")
		tags := metrics.ConvertMapToTags(map[string]string{
			"podNamespace":  ctx.ResourceReq.PodNamespace,
			"podName":       ctx.ResourceReq.PodName,
			"containerName": ctx.ResourceReq.ContainerName,
		})

		_ = ctx.Emitter.StoreInt64(metricNameNoDeviceTopologyAffinity, 1, metrics.MetricTypeNameRaw, tags...)
		return s.CanonicalStrategy.Bind(ctx, sortedDevices)
	}

	// Get affinity groups organized by priority level and trimmed to the lowest priority we need to consider.
	affinityGroupsByPriority := s.getAffinityGroupsSortedByPriority(affinityLevels, unallocatedDevicesSet, devicesToAllocate, requiredDeviceAffinity)

	// Allocate reusable devices first
	allocatedDevices, err := s.allocateCandidateDevices(affinityGroupsByPriority,
		reusableDevicesSet.Intersection(unallocatedDevicesSet), sets.NewString(), devicesToAllocate, requiredDeviceAffinity)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to allocate reusable devices: %v", err),
		}, fmt.Errorf("failed to allocate reusable devices: %v", err)
	}

	if len(allocatedDevices) == devicesToAllocate {
		return &allocate.AllocationResult{
			Success:          true,
			AllocatedDevices: allocatedDevices.UnsortedList(),
		}, nil
	}

	// Next, allocate left available devices
	availableDevices := unallocatedDevicesSet.Difference(allocatedDevices)
	allocatedDevices, err = s.allocateCandidateDevices(affinityGroupsByPriority,
		availableDevices, allocatedDevices, devicesToAllocate, requiredDeviceAffinity)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to allocate available devices with affinity: %v", err),
		}, fmt.Errorf("failed to allocate available devices with affinity: %v", err)
	}

	// Return result once we have allocated all the devices
	if len(allocatedDevices) == devicesToAllocate {
		return &allocate.AllocationResult{
			Success:          true,
			AllocatedDevices: allocatedDevices.UnsortedList(),
		}, nil
	}

	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: fmt.Sprintf("not enough devices to allocate: need %d, have %d", devicesToAllocate, len(allocatedDevices)),
	}, fmt.Errorf("not enough devices to allocate: need %d, have %d", devicesToAllocate, len(allocatedDevices))
}

// getAffinityGroupsSortedByPriority forms affinity groups sorted from the highest priority to the lowest priority.
// When device affinity is required, it only keeps levels up to the first one that can satisfy the request.
func (s *DeviceAffinityStrategy) getAffinityGroupsSortedByPriority(
	affinityMap [][]machine.DeviceIDs, unallocatedDevicesSet sets.String, deviceReq int, requiredDeviceAffinity bool,
) [][]affinityGroup {
	affinityGroupsSortedByPriority := make([][]affinityGroup, 0, len(affinityMap))
	for _, affinityDevices := range affinityMap {
		affinityGroups := s.getAffinityGroups(affinityDevices, unallocatedDevicesSet)
		affinityGroupsSortedByPriority = append(affinityGroupsSortedByPriority, affinityGroups)

		if !requiredDeviceAffinity {
			continue
		}

		for _, group := range affinityGroups {
			if deviceReq <= group.totalDevicesNum {
				return affinityGroupsSortedByPriority
			}
		}
	}

	return affinityGroupsSortedByPriority
}

// getAffinityGroups forms a list of affinityGroup with unallocated devices.
func (s *DeviceAffinityStrategy) getAffinityGroups(
	affinityDevices []machine.DeviceIDs, unallocatedDevicesSet sets.String,
) []affinityGroup {
	affinityGroups := make([]affinityGroup, 0, len(affinityDevices))

	// Calculate the number of unallocated devices for each affinity group
	for _, devices := range affinityDevices {
		unallocatedDevices := sets.NewString()
		for _, device := range devices {
			if unallocatedDevicesSet.Has(device) {
				unallocatedDevices.Insert(device)
			}
		}
		affinityGroups = append(affinityGroups, affinityGroup{
			unallocatedDevices: unallocatedDevices,
			id:                 uuid.NewString(),
			totalDevicesNum:    len(devices),
		})
	}

	return affinityGroups
}

// allocateCandidateDevices optimally allocates GPU devices based on affinity priorities.
// This method implements a sophisticated allocation strategy that:
// 1. Prioritizes device groups with higher affinity levels
// 2. Minimizes fragmentation by selecting devices with strong mutual affinity
// 3. Balances between fulfilling exact requirements and maintaining optimal groupings
//
// Parameters:
//   - affinityGroupsByPriority: Device groups ordered from highest priority to lowest priority
//   - candidateDevicesSet: Set of available devices that can be allocated
//   - devicesToAllocate: Total number of devices that need to be allocated
//   - allocatedDevices: Set of devices that have already been allocated in previous iterations
//
// Returns:
//   - sets.String: The complete set of allocated devices after this allocation round
//   - error: Any error encountered during the allocation process
func (s *DeviceAffinityStrategy) allocateCandidateDevices(
	affinityGroupsByPriority [][]affinityGroup,
	candidateDevicesSet, allocatedDevices sets.String,
	devicesToAllocate int,
	requiredDeviceAffinity bool,
) (sets.String, error) {
	// Early termination conditions
	if len(allocatedDevices) == devicesToAllocate || len(candidateDevicesSet) == 0 {
		return allocatedDevices, nil
	}

	// Calculate remaining devices needed
	remainingDevicesToAllocate := devicesToAllocate - len(allocatedDevices)

	// Fast path: If we need all remaining candidates, allocate them all.
	// With RequiredDeviceAffinity, don't blindly union candidates across affinity groups.
	if !requiredDeviceAffinity && remainingDevicesToAllocate >= len(candidateDevicesSet) {
		allocatedDevices = allocatedDevices.Union(candidateDevicesSet)
		return allocatedDevices, nil
	}

	// Process affinity groups from highest to lowest priority
	for priority, affinityGroups := range affinityGroupsByPriority {
		if len(affinityGroups) == 0 {
			continue
		}

		// Prepare group information for evaluation
		groupInfos := s.prepareGroupInfos(affinityGroups, candidateDevicesSet, allocatedDevices)
		if len(groupInfos) == 0 {
			continue
		}

		// Sort groups by allocation suitability
		s.sortGroupsByPriority(groupInfos, remainingDevicesToAllocate)

		// Try to allocate from the best matching groups
		if result, fullyAllocated := s.tryAllocateFromGroups(
			groupInfos, remainingDevicesToAllocate, allocatedDevices, devicesToAllocate,
		); fullyAllocated {
			return result, nil
		}

		// For the lowest considered priority, use more flexible allocation strategies.
		// With RequiredDeviceAffinity, do not mix devices across different affinity groups.
		if priority == len(affinityGroupsByPriority)-1 {
			if requiredDeviceAffinity {
				return allocatedDevices, nil
			}
			return s.handleLowestPriorityAllocation(
				groupInfos, affinityGroupsByPriority, candidateDevicesSet,
				devicesToAllocate, allocatedDevices, remainingDevicesToAllocate, requiredDeviceAffinity,
			)
		}
	}

	return allocatedDevices, nil
}

// prepareGroupInfos processes affinity groups and extracts relevant allocation information.
// This helper method filters out groups with no candidate devices and calculates
// the intersection between group devices and available candidates.
func (s *DeviceAffinityStrategy) prepareGroupInfos(
	affinityGroups []affinityGroup,
	candidateDevicesSet sets.String,
	allocatedDevices sets.String,
) []groupInfo {
	groupInfos := make([]groupInfo, 0, len(affinityGroups))

	for _, group := range affinityGroups {
		// Find devices in this group that are also candidates
		candidates := group.unallocatedDevices.Intersection(candidateDevicesSet)
		if candidates.Len() == 0 {
			continue // Skip groups with no matching candidates
		}

		// Calculate unallocated and allocated device sets for this group
		unallocated := group.unallocatedDevices.Difference(allocatedDevices)
		if unallocated.Len() == 0 {
			continue // Skip groups where all devices are already allocated
		}

		allocated := group.unallocatedDevices.Intersection(allocatedDevices)

		groupInfos = append(groupInfos, groupInfo{
			group:       group,
			candidates:  candidates,
			allocated:   allocated,
			unallocated: unallocated,
		})
	}

	return groupInfos
}

// sortGroupsByPriority sorts affinity groups based on allocation suitability.
// The sorting criteria are:
// 1. Proximity to the exact number of devices needed (closer is better)
// 2. Total unallocated devices (smaller is better to minimize fragmentation)
// 3. Already allocated devices (larger is better to maintain consistency)
func (s *DeviceAffinityStrategy) sortGroupsByPriority(
	groupInfos []groupInfo,
	remainingDevicesToAllocate int,
) {
	sort.Slice(groupInfos, func(i, j int) bool {
		// Calculate absolute difference from needed devices
		diffI := abs(groupInfos[i].candidates.Len() - remainingDevicesToAllocate)
		diffJ := abs(groupInfos[j].candidates.Len() - remainingDevicesToAllocate)

		// Prefer groups closer to the exact number needed
		if diffI != diffJ {
			return diffI < diffJ
		}

		// Prefer groups with fewer unallocated devices to reduce fragmentation
		if groupInfos[i].unallocated.Len() != groupInfos[j].unallocated.Len() {
			return groupInfos[i].unallocated.Len() < groupInfos[j].unallocated.Len()
		}

		// Prefer groups with more already allocated devices for consistency
		if groupInfos[i].allocated.Len() != groupInfos[j].allocated.Len() {
			return groupInfos[i].allocated.Len() > groupInfos[j].allocated.Len()
		}

		return groupInfos[i].group.id < groupInfos[j].group.id
	})
}

// tryAllocateFromGroups attempts to allocate devices from the prioritized groups.
// It first tries to find an exact match, then falls back to partial allocations.
func (s *DeviceAffinityStrategy) tryAllocateFromGroups(
	groupInfos []groupInfo,
	remainingDevicesToAllocate int,
	allocatedDevices sets.String,
	devicesToAllocate int,
) (sets.String, bool) {
	// Try to find groups that can exactly satisfy the remaining requirement
	for _, group := range groupInfos {
		// Check if this group can satisfy the exact remaining requirement and
		// ensure affinity allocation if there are already allocated devices
		if remainingDevicesToAllocate <= group.candidates.Len() &&
			!(allocatedDevices.Len() > 0 && group.allocated.Len() <= 0) {

			// Add all candidate devices from this group
			for _, device := range group.candidates.List() {
				allocatedDevices.Insert(device)
				if len(allocatedDevices) == devicesToAllocate {
					return allocatedDevices, true // Fully allocated
				}
			}
			return allocatedDevices, true
		}
	}

	return allocatedDevices, false // Not fully allocated
}

// handleLowestPriorityAllocation implements flexible allocation strategies for the lowest priority.
// This method is more permissive in its allocation strategy to ensure device requirements are met.
func (s *DeviceAffinityStrategy) handleLowestPriorityAllocation(
	groupInfos []groupInfo,
	affinityGroupsByPriority [][]affinityGroup,
	candidateDevicesSet sets.String,
	devicesToAllocate int,
	allocatedDevices sets.String,
	remainingDevicesToAllocate int,
	requiredDeviceAffinity bool,
) (sets.String, error) {
	// First try to allocate entire groups that fit within the remaining requirement and
	// ensure affinity allocation if there are already allocated devices
	for _, group := range groupInfos {
		if remainingDevicesToAllocate >= group.candidates.Len() &&
			!(allocatedDevices.Len() > 0 && group.allocated.Len() <= 0) {

			// Allocate all devices from this group
			allocatedDevices = allocatedDevices.Union(group.candidates)

			// Recursively allocate the remaining devices
			return s.allocateCandidateDevices(
				affinityGroupsByPriority,
				candidateDevicesSet.Difference(group.candidates),
				allocatedDevices,
				devicesToAllocate,
				requiredDeviceAffinity,
			)
		}
	}

	// If no exact matches, try partial allocations from larger groups
	for _, group := range groupInfos {
		// Check if this group can contribute to the remaining requirement
		if remainingDevicesToAllocate >= group.candidates.Len() {
			// Allocate all devices from this group and continue
			allocatedDevices = allocatedDevices.Union(group.candidates)

			return s.allocateCandidateDevices(
				affinityGroupsByPriority,
				candidateDevicesSet.Difference(group.candidates),
				allocatedDevices,
				devicesToAllocate,
				requiredDeviceAffinity,
			)
		} else {
			// Recursively allocate a subset of devices from this group
			devices, err := s.allocateCandidateDevices(
				affinityGroupsByPriority,
				group.candidates,
				group.allocated,
				remainingDevicesToAllocate,
				requiredDeviceAffinity,
			)
			if err != nil {
				return nil, err
			}

			return allocatedDevices.Union(devices), nil
		}
	}

	return allocatedDevices, nil
}

// abs returns the absolute value of an integer.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// groupInfo contains pre-calculated information about an affinity group
// to optimize the allocation process by avoiding repeated calculations.
type groupInfo struct {
	group       affinityGroup
	candidates  sets.String // Devices in this group that are also candidates
	allocated   sets.String // Devices in this group that are already allocated
	unallocated sets.String // Devices in this group that are not yet allocated
}
