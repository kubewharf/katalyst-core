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
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// affinityGroup is a group of devices that have affinity to each other.
// It is uniquely identified by an id.
type affinityGroup struct {
	id                 string
	unallocatedDevices []string
}

// possibleAllocation refers to information about a certain affinity group, which includes the number of unallocated devices in the group,
// and candidateDevices refer to a set of devices that we can potentially allocate in an affinity group.
type possibleAllocation struct {
	unallocatedSize  int
	candidateDevices []string
}

// allocationByIntersectionResult is the result of allocating devices by maximizing intersection size of possible allocations
// with an affinity group.
type allocationByIntersectionResult struct {
	finished bool
	err      error
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

	// All devices that are passed into the strategy are unallocated devices
	unallocatedDevicesSet := sets.NewString(sortedDevices...)

	// Get a map of affinity groups that is grouped by priority
	affinityMap := ctx.DeviceTopology.GroupDeviceAffinity()
	affinityGroupByPriority := s.getAffinityGroupsByPriority(affinityMap, unallocatedDevicesSet)

	idToAffinityGroupMap := s.getAffinityGroupById(affinityGroupByPriority)

	// Allocate reusable devices first
	allocatedDevices, err := s.allocateAvailableDevicesWithAffinity(sets.NewString(), reusableDevicesSet, devicesToAllocate, idToAffinityGroupMap, affinityGroupByPriority)
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

	// Next, allocate available devices
	if allocatedDevices, err = s.allocateAvailableDevicesWithAffinity(allocatedDevices, unallocatedDevicesSet, devicesToAllocate, idToAffinityGroupMap, affinityGroupByPriority); err != nil {
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

// getAffinityGroupsByPriority forms a map of affinityGroup by priority.
func (s *DeviceAffinityStrategy) getAffinityGroupsByPriority(
	affinityMap map[machine.AffinityPriority][]machine.DeviceIDs, unallocatedDevicesSet sets.String,
) map[machine.AffinityPriority][]affinityGroup {
	affinityGroupsMap := make(map[machine.AffinityPriority][]affinityGroup)
	for priority, affinityDevices := range affinityMap {
		affinityGroupsMap[priority] = s.getAffinityGroups(affinityDevices, unallocatedDevicesSet)
	}

	return affinityGroupsMap
}

// getAffinityGroups forms a list of affinityGroup with unallocated devices.
func (s *DeviceAffinityStrategy) getAffinityGroups(
	affinityDevices []machine.DeviceIDs, unallocatedDevicesSet sets.String,
) []affinityGroup {
	affinityGroups := make([]affinityGroup, 0, len(affinityDevices))

	// Calculate the number of unallocated devices for each affinity group
	for _, devices := range affinityDevices {
		unallocatedDevices := make(machine.DeviceIDs, 0)
		for _, device := range devices {
			if unallocatedDevicesSet.Has(device) {
				unallocatedDevices = append(unallocatedDevices, device)
			}
		}
		affinityGroups = append(affinityGroups, affinityGroup{
			unallocatedDevices: unallocatedDevices,
			id:                 uuid.NewString(),
		})
	}

	return affinityGroups
}

func (s *DeviceAffinityStrategy) getAffinityGroupById(affinityGroupByPriority map[machine.AffinityPriority][]affinityGroup) map[string]affinityGroup {
	idToAffinityGroupMap := make(map[string]affinityGroup)
	for _, groups := range affinityGroupByPriority {
		for _, group := range groups {
			idToAffinityGroupMap[group.id] = group
		}
	}
	return idToAffinityGroupMap
}

// allocateCandidateDevices finds the best allocation given some candidate devices by finding those devices with the best affinity to each other.
func (s *DeviceAffinityStrategy) allocateCandidateDevices(
	candidateDevicesSet sets.String, devicesToAllocate int,
	affinityGroupsByPriority map[machine.AffinityPriority][]affinityGroup, allocatedDevices sets.String,
) (sets.String, error) {
	// Retrieve all unallocated devices by getting the intersection of reusable devices and unallocated devices.
	// Devices that are already allocated should be excluded.
	availableDevicesSet := candidateDevicesSet.Difference(allocatedDevices)

	// If the available reusable devices is less than or equal to request, we need to allocate all of them
	remainingQuantity := devicesToAllocate - len(allocatedDevices)
	if availableDevicesSet.Len() <= remainingQuantity {
		allocatedDevices = availableDevicesSet
		return allocatedDevices, nil
	}

	for allocatedDevices.Len() < devicesToAllocate {
		// Otherwise, we need to allocate these devices by their affinity
		for priority := 0; priority < len(affinityGroupsByPriority); priority++ {
			groups, ok := affinityGroupsByPriority[machine.AffinityPriority(priority)]
			if !ok {
				return nil, fmt.Errorf("affinity priority %v not found", priority)
			}

			intersectionToPossibleAllocationsMap := s.getIntersectionToPossibleAllocations(groups, availableDevicesSet, allocatedDevices)

			allocateByIntersectionRes := s.allocateByIntersection(intersectionToPossibleAllocationsMap, allocatedDevices, devicesToAllocate, priority == len(affinityGroupsByPriority)-1)
			if allocateByIntersectionRes.err != nil {
				return nil, allocateByIntersectionRes.err
			}

			if allocateByIntersectionRes.finished {
				return allocatedDevices, nil
			}
		}
	}

	return allocatedDevices, nil
}

// allocateAvailableDevicesWithAffinity allocates devices from a set of available devices by trying to find the best device affinity
// to some already allocated reusable devices. Then, if there are still devices needed to be allocated, we continue to find the
// most optimal allocation for them.
func (s *DeviceAffinityStrategy) allocateAvailableDevicesWithAffinity(
	allocatedDevices, availableDevices sets.String, devicesToAllocate int,
	idToAffinityGroupMap map[string]affinityGroup,
	affinityGroupsByPriority map[machine.AffinityPriority][]affinityGroup,
) (sets.String, error) {
	// Find all the affinity group ids that the allocated devices belong to and their respective priorities
	allocatedAffinityGroupIds := s.findAllAffinityGroupIdsByPriority(allocatedDevices.UnsortedList(), affinityGroupsByPriority)

	// From the highest priority to the lowest priority, get the group IDs of the devices that are already allocated
	// and try to allocate from those groups.
	for priority := 0; priority < len(allocatedAffinityGroupIds); priority++ {
		groupIDs, ok := allocatedAffinityGroupIds[machine.AffinityPriority(priority)]
		if !ok {
			return nil, fmt.Errorf("unallocated affinity group ids in priority level %v not found", priority)
		}

		intersectionToPossibleAllocationsMap := make(map[int][]possibleAllocation)

		for groupID := range groupIDs {
			// Get affinity group
			group, ok := idToAffinityGroupMap[groupID]
			if !ok {
				return nil, fmt.Errorf("affinity group %v not found", groupID)
			}

			deviceIntersection := sets.NewString(group.unallocatedDevices...).Intersection(availableDevices)
			if _, ok = intersectionToPossibleAllocationsMap[deviceIntersection.Len()]; !ok {
				intersectionToPossibleAllocationsMap[deviceIntersection.Len()] = make([]possibleAllocation, 0)
			}

			intersectionToPossibleAllocationsMap[deviceIntersection.Len()] = append(intersectionToPossibleAllocationsMap[deviceIntersection.Len()], possibleAllocation{
				// The number of unallocated devices in the group is retrieved by taking a difference between
				// the unallocated devices in the group and the already allocated devices
				unallocatedSize:  sets.NewString(group.unallocatedDevices...).Difference(allocatedDevices).Len(),
				candidateDevices: deviceIntersection.UnsortedList(),
			})
		}

		allocateByIntersectionRes := s.allocateByIntersection(intersectionToPossibleAllocationsMap, allocatedDevices, devicesToAllocate, priority == len(allocatedAffinityGroupIds)-1)
		if allocateByIntersectionRes.err != nil {
			return nil, allocateByIntersectionRes.err
		}

		if allocateByIntersectionRes.finished {
			return allocatedDevices, nil
		}
	}

	// After allocating devices that have affinity to the allocated devices, find an optimal allocation for the rest of the available devices
	allocatedDevices, err := s.allocateCandidateDevices(availableDevices, devicesToAllocate, affinityGroupsByPriority, allocatedDevices)
	if err != nil {
		return nil, err
	}

	return allocatedDevices, nil
}

// mergePossibleAllocationsAndSort merges the possible allocations by their unallocated size and sorts them in ascending order of their unallocated size.
func (s *DeviceAffinityStrategy) mergePossibleAllocationsAndSort(possibleAllocations []possibleAllocation) []possibleAllocation {
	merged := make(map[int][]string)
	for _, alloc := range possibleAllocations {
		if _, ok := merged[alloc.unallocatedSize]; !ok {
			merged[alloc.unallocatedSize] = make([]string, 0)
		}
		merged[alloc.unallocatedSize] = append(merged[alloc.unallocatedSize], alloc.candidateDevices...)
	}

	mergedAllocations := make([]possibleAllocation, 0, len(merged))
	for unallocatedSize, intersected := range merged {
		mergedAllocations = append(mergedAllocations, possibleAllocation{
			unallocatedSize:  unallocatedSize,
			candidateDevices: intersected,
		})
	}

	// Sort possible allocations by their unallocated size in ascending order
	// To support bin-packing, we prioritize allocation of devices in groups that have other allocated devices.
	sort.Slice(mergedAllocations, func(i, j int) bool {
		return mergedAllocations[i].unallocatedSize < mergedAllocations[j].unallocatedSize
	})

	return mergedAllocations
}

func (s *DeviceAffinityStrategy) getIntersectionToPossibleAllocations(
	groups []affinityGroup, availableDevicesSet, allocatedDevices sets.String,
) map[int][]possibleAllocation {
	intersectionToPossibleAllocationsMap := make(map[int][]possibleAllocation)
	for _, group := range groups {
		// Find intersection of affinity group and the available reusable devices
		deviceIntersection := getDeviceIntersection(group.unallocatedDevices, availableDevicesSet)
		// Ignore groups with no intersection as there is no affinity to the group
		if len(deviceIntersection) == 0 {
			continue
		}
		if _, ok := intersectionToPossibleAllocationsMap[len(deviceIntersection)]; !ok {
			intersectionToPossibleAllocationsMap[len(deviceIntersection)] = make([]possibleAllocation, 0)
		}
		intersectionToPossibleAllocationsMap[len(deviceIntersection)] = append(intersectionToPossibleAllocationsMap[len(deviceIntersection)], possibleAllocation{
			// The number of unallocated devices in the group is retrieved by taking a difference between
			// the unallocated devices in the group and the already allocated devices
			unallocatedSize:  sets.NewString(group.unallocatedDevices...).Difference(allocatedDevices).Len(),
			candidateDevices: deviceIntersection,
		})
	}

	return intersectionToPossibleAllocationsMap
}

func getDeviceIntersection(unallocatedDevices []string, availableDevices sets.String) []string {
	deviceIntersection := make([]string, 0)
	for _, device := range unallocatedDevices {
		if availableDevices.Has(device) {
			deviceIntersection = append(deviceIntersection, device)
		}
	}
	return deviceIntersection
}

// allocateByIntersection allocates devices by the following algorithm
//  1. Sort the intersection sizes of possible allocations in descending order, we want to allocate devices with larger intersection size with an affinity group.
//  2. For each intersection size, merge and sort the possible allocations by their unallocated size in ascending order, this is to maximize
//     bin-packing (try to fill up an affinity group that is already allocated with other devices).
//  3. For each intersection size, allocate devices in the order of the sorted possible allocations.
//  4. If a possible allocation has a number of intersected devices larger than the devices needed for allocation, we go to the next priority and try to find an allocation from there.
//  5. If we are currently at the last affinity priority level, we go through the other possible allocations (that are in sorted ascending order of number of unallocated devices)
//     to fill up the remaining devices.
func (s *DeviceAffinityStrategy) allocateByIntersection(
	intersectionToPossibleAllocationsMap map[int][]possibleAllocation, allocatedDevices sets.String,
	devicesToAllocate int, isLastPriority bool,
) allocationByIntersectionResult {
	// Sort the intersection sizes of possible allocations in descending order because we want to process the larger intersections first.
	// A larger intersection means that we are able to find more devices that have an affinity with an affinity group.
	intersectionSizes := make([]int, 0, len(intersectionToPossibleAllocationsMap))
	for intersectionSize := range intersectionToPossibleAllocationsMap {
		intersectionSizes = append(intersectionSizes, intersectionSize)
	}

	sort.Slice(intersectionSizes, func(i, j int) bool {
		return intersectionSizes[i] > intersectionSizes[j]
	})

	// If there is an intersection size that is larger than or equal to the number of devices needed for allocation,
	// find the smallest intersection size. This is so that we try to reduce fragmentation as much as possible.
	// For example, if we have 1 device to allocate, and we have intersectionSizes of 2 and 1, we want to allocate to the group with
	// intersection of size 1, as this means that we are able to successfully do bin-packing (fill up an affinity group that
	// that is already allocated with other devices)
	start := 0
	for i, intersectionSize := range intersectionSizes {
		if intersectionSize <= devicesToAllocate {
			start = i
			break
		}
	}

	klog.Infof("intersection to possible allocations map: %v", intersectionToPossibleAllocationsMap)

	for i := start; i < len(intersectionSizes); i++ {
		intersectionSize := intersectionSizes[i]
		possibleAllocations, ok := intersectionToPossibleAllocationsMap[intersectionSize]
		if !ok {
			return allocationByIntersectionResult{
				finished: false,
				err:      fmt.Errorf("possible reusable devices of intersection size %v not found", intersectionSize),
			}
		}

		mergedPossibleAllocations := s.mergePossibleAllocationsAndSort(possibleAllocations)

		klog.Infof("possible allocations: %v", mergedPossibleAllocations)

		for _, possibleAlloc := range mergedPossibleAllocations {
			// If devices of possible allocation size is larger than the devices needed, and it is not the last priority level,
			// go to the next priority and try to allocate
			remainingToAllocate := devicesToAllocate - allocatedDevices.Len()
			if !isLastPriority && len(possibleAlloc.candidateDevices) > remainingToAllocate {
				return allocationByIntersectionResult{
					finished: false,
					err:      nil,
				}
			}

			for _, device := range possibleAlloc.candidateDevices {
				allocatedDevices.Insert(device)
				if allocatedDevices.Len() == devicesToAllocate {
					return allocationByIntersectionResult{
						finished: true,
						err:      nil,
					}
				}
			}
		}
	}

	return allocationByIntersectionResult{
		finished: false,
		err:      nil,
	}
}

// findAllAffinityGroupIdsByPriority finds the affinity group ids of the allocated devices by affinity priority level.
func (s *DeviceAffinityStrategy) findAllAffinityGroupIdsByPriority(
	allocatedDevices []string, affinityMap map[machine.AffinityPriority][]affinityGroup,
) map[machine.AffinityPriority]sets.String {
	affinityGroupIds := make(map[machine.AffinityPriority]sets.String)
	for _, device := range allocatedDevices {
		for priority, groups := range affinityMap {
			for _, group := range groups {
				if sets.NewString(group.unallocatedDevices...).Has(device) {
					if _, ok := affinityGroupIds[priority]; !ok {
						affinityGroupIds[priority] = sets.NewString()
					}
					affinityGroupIds[priority].Insert(group.id)
				}
			}
		}
	}
	return affinityGroupIds
}
