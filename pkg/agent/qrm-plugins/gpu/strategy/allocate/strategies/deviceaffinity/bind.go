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

	allocatedDevices, err := s.allocateCandidateDevices(reusableDevicesSet, unallocatedDevicesSet, devicesToAllocate,
		affinityGroupByPriority, sets.NewString(), false, false)
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
	reusableDevicesSet, totalAvailableDevicesSet sets.String, devicesToAllocate int,
	defaultAffinityGroupsByPriority map[machine.AffinityPriority][]affinityGroup,
	allocatedDevices sets.String, isFindAvailableDeviceAffinity, hasAllocatedReusable bool,
) (sets.String, error) {
	// Retrieve all unallocated devices by getting the intersection of reusable devices and unallocated devices.
	// Devices that are already allocated should be excluded.
	var candidateDevicesSet sets.String

	if !hasAllocatedReusable {
		candidateDevicesSet = reusableDevicesSet
	} else {
		candidateDevicesSet = totalAvailableDevicesSet
	}

	availableDevicesSet := candidateDevicesSet
	// Only subtract out the available devices with the allocated devices if we are trying to allocate from scratch
	if !isFindAvailableDeviceAffinity {
		availableDevicesSet = availableDevicesSet.Difference(allocatedDevices)
	}

	// If the available reusable devices is less than or equal to request, we need to allocate all of them
	remainingQuantity := devicesToAllocate - len(allocatedDevices)
	if availableDevicesSet.Len() <= remainingQuantity {
		allocatedDevices = availableDevicesSet

		// If we are now allocating reusable devices, make another recursive call to allocate available devices
		if !hasAllocatedReusable {
			return s.allocateCandidateDevices(reusableDevicesSet, totalAvailableDevicesSet, devicesToAllocate,
				defaultAffinityGroupsByPriority, allocatedDevices, !isFindAvailableDeviceAffinity, true)
		}
		return allocatedDevices, nil
	}

	var affinityGroupsByPriority map[machine.AffinityPriority][]affinityGroup
	if isFindAvailableDeviceAffinity {
		affinityGroupsByPriority = s.findAllAllocatedAffinityGroupsByPriority(allocatedDevices.UnsortedList(), defaultAffinityGroupsByPriority)
	} else {
		affinityGroupsByPriority = defaultAffinityGroupsByPriority
	}

	for {
		// Otherwise, we need to allocate these devices by their affinity
		for priority := 0; priority < len(affinityGroupsByPriority); priority++ {
			groups, ok := affinityGroupsByPriority[machine.AffinityPriority(priority)]
			if !ok {
				return nil, fmt.Errorf("affinity priority %v not found", priority)
			}

			candidateDeviceSizeToPossibleAllocationsMap := s.getCandidateDeviceSizeToPossibleAllocations(groups, availableDevicesSet, allocatedDevices)

			allocateByIntersectionRes := s.allocateByIntersection(candidateDeviceSizeToPossibleAllocationsMap, allocatedDevices, devicesToAllocate, priority == len(affinityGroupsByPriority)-1)
			if allocateByIntersectionRes.err != nil {
				return nil, allocateByIntersectionRes.err
			}

			if allocateByIntersectionRes.finished {
				return allocatedDevices, nil
			}
		}

		// isFindAvailableDeviceAffinity is true if we are trying to find available devices that have affinity with the already allocated reusable device
		if isFindAvailableDeviceAffinity || allocatedDevices.Len() >= devicesToAllocate {
			break
		}
	}

	return s.allocateCandidateDevices(reusableDevicesSet, totalAvailableDevicesSet, devicesToAllocate,
		defaultAffinityGroupsByPriority, allocatedDevices, !isFindAvailableDeviceAffinity, true)
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

// getCandidateDeviceSizeToPossibleAllocations calculates the candidate devices given an affinity group and available devices by
// taking the intersection of the two groups. Then, it returns a map of size of candidate devices to an array of possible allocations,
// where one possible allocation contains the number of unallocated devices in an affinity group, and the candidate devices calculated earlier.
func (s *DeviceAffinityStrategy) getCandidateDeviceSizeToPossibleAllocations(
	groups []affinityGroup, availableDevicesSet, allocatedDevices sets.String,
) map[int][]possibleAllocation {
	candidateDeviceSizeToPossibleAllocationsMap := make(map[int][]possibleAllocation)
	for _, group := range groups {
		// Find intersection of affinity group and the available reusable devices, these will be the set of allocatable candidate devices in a group
		candidateDevices := getDeviceIntersection(group.unallocatedDevices, availableDevicesSet)
		// Ignore groups with no intersection as there is no affinity to the group
		if len(candidateDevices) == 0 {
			continue
		}
		if _, ok := candidateDeviceSizeToPossibleAllocationsMap[len(candidateDevices)]; !ok {
			candidateDeviceSizeToPossibleAllocationsMap[len(candidateDevices)] = make([]possibleAllocation, 0)
		}
		candidateDeviceSizeToPossibleAllocationsMap[len(candidateDevices)] = append(candidateDeviceSizeToPossibleAllocationsMap[len(candidateDevices)], possibleAllocation{
			// The number of unallocated devices in the group is retrieved by taking a difference between
			// the unallocated devices in the group and the already allocated devices
			unallocatedSize:  sets.NewString(group.unallocatedDevices...).Difference(allocatedDevices).Len(),
			candidateDevices: candidateDevices,
		})
	}

	return candidateDeviceSizeToPossibleAllocationsMap
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
//  1. Sort the candidate devices size of possible allocations in descending order, as we want to prioritize devices with larger candidate devices size with an affinity group.
//  2. For each candidate devices size, merge and sort the possible allocations by their unallocated size in ascending order, this is to maximize
//     bin-packing (try to fill up an affinity group that is already allocated with other devices).
//  3. For each candidate device size, allocate devices in the order of the sorted possible allocations.
//  4. If a possible allocation has a number of candidate devices larger than the devices needed for allocation, we go to the next priority and try to find an allocation from there.
//  5. If we are currently at the last affinity priority level, we go through the other possible allocations (that are in sorted ascending order of number of unallocated devices)
//     to fill up the remaining devices.
func (s *DeviceAffinityStrategy) allocateByIntersection(
	candidateDevicesSizeToPossibleAllocationsMap map[int][]possibleAllocation, allocatedDevices sets.String,
	devicesToAllocate int, isLastPriority bool,
) allocationByIntersectionResult {
	// Sort the intersection sizes of possible allocations in descending order because we want to process the larger intersections first.
	// A larger intersection means that we are able to find more devices that have an affinity with an affinity group.
	candidateDevicesSizes := make([]int, 0, len(candidateDevicesSizeToPossibleAllocationsMap))
	for candidateSize := range candidateDevicesSizeToPossibleAllocationsMap {
		candidateDevicesSizes = append(candidateDevicesSizes, candidateSize)
	}

	sort.Slice(candidateDevicesSizes, func(i, j int) bool {
		return candidateDevicesSizes[i] > candidateDevicesSizes[j]
	})

	// If there is an intersection size that is larger than or equal to the number of devices needed for allocation,
	// find the smallest intersection size. This is so that we try to reduce fragmentation as much as possible.
	// For example, if we have 1 device to allocate, and we have candidateDevicesSizes of 2 and 1, we want to allocate to the group with
	// intersection of size 1, as this means that we are able to successfully do bin-packing (fill up an affinity group that
	// that is already allocated with other devices)
	// Find the starting index: first size <= devicesToAllocate, or last one if none
	start := len(candidateDevicesSizes) - 1
	for i, size := range candidateDevicesSizes {
		if size <= devicesToAllocate {
			start = i
			break
		}
	}

	klog.Infof("intersection to possible allocations map: %v", candidateDevicesSizeToPossibleAllocationsMap)

	for i := start; i < len(candidateDevicesSizes); i++ {
		intersectionSize := candidateDevicesSizes[i]
		possibleAllocations, ok := candidateDevicesSizeToPossibleAllocationsMap[intersectionSize]
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

func (s *DeviceAffinityStrategy) findAllAllocatedAffinityGroupsByPriority(
	allocatedDevices []string, affinityMap map[machine.AffinityPriority][]affinityGroup,
) map[machine.AffinityPriority][]affinityGroup {
	affinityGroupsByPriority := make(map[machine.AffinityPriority][]affinityGroup)
	seenIds := sets.NewString()
	for _, device := range allocatedDevices {
		for priority, groups := range affinityMap {
			for _, group := range groups {
				if sets.NewString(group.unallocatedDevices...).Has(device) {
					if seenIds.Has(group.id) {
						continue
					}

					if _, ok := affinityGroupsByPriority[priority]; !ok {
						affinityGroupsByPriority[priority] = make([]affinityGroup, 0)
					}
					affinityGroupsByPriority[priority] = append(affinityGroupsByPriority[priority], group)
					seenIds.Insert(group.id)
				}
			}
		}
	}
	return affinityGroupsByPriority
}
