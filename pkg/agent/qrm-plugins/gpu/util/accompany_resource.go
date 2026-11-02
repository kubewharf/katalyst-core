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
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// affinityGroup contains accompany devices that share the same target-device
// candidates in one selected affinity dimension.
type affinityGroup struct {
	accompanyResourceIDs []string
	targetResourceIDs    machine.DeviceIDs
}

// ResolveAffinityDeviceByAccompanyResource takes the machine-wide topology mapping
// from accompany-resource devices to target-resource devices. Accompany devices
// sharing the same highest-priority affinity group are distributed evenly across
// its target devices, while each accompany device resolves to one target device.
func ResolveAffinityDeviceByAccompanyResource(
	affinityByAccompanyResource map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
) map[string]string {
	priorities := append([]string(nil), priorityDimensions...)
	if len(priorities) == 0 {
		// NUMA preserves the existing fallback when no dimensions are reported.
		priorities = []string{machine.DimensionNuma}
	}

	groups := groupAccompanyResourcesByAffinity(affinityByAccompanyResource, priorities)
	result := make(map[string]string, len(affinityByAccompanyResource))
	for _, group := range groups {
		assignAffinityGroup(result, group)
	}
	return result
}

// groupAccompanyResourcesByAffinity groups accompany devices by their
// highest-priority affinity dimension and target-device candidates.
func groupAccompanyResourcesByAffinity(
	affinityByAccompanyResource map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
) map[string]*affinityGroup {
	groups := make(map[string]*affinityGroup)
	for accompanyResourceID, affinityByDimension := range affinityByAccompanyResource {
		dimension, targetResourceIDs := getHighestPriorityAffinity(
			affinityByDimension,
			priorityDimensions,
		)
		if len(targetResourceIDs) == 0 {
			continue
		}

		groupKey := dimension + "\x00" + strings.Join(targetResourceIDs, "\x00")
		group, ok := groups[groupKey]
		if !ok {
			group = &affinityGroup{
				targetResourceIDs: targetResourceIDs,
			}
			groups[groupKey] = group
		}
		group.accompanyResourceIDs = append(group.accompanyResourceIDs, accompanyResourceID)
	}
	return groups
}

// getHighestPriorityAffinity returns the first non-empty affinity dimension
// and a sorted copy of its target-device IDs.
func getHighestPriorityAffinity(
	affinityByDimension map[string]machine.DeviceIDs,
	priorityDimensions []string,
) (string, machine.DeviceIDs) {
	for _, dimension := range priorityDimensions {
		targetResourceIDs := append(machine.DeviceIDs(nil), affinityByDimension[dimension]...)
		if len(targetResourceIDs) == 0 {
			continue
		}

		sort.Strings(targetResourceIDs)
		return dimension, targetResourceIDs
	}
	return "", nil
}

// assignAffinityGroup distributes sorted accompany devices as evenly as
// possible across the group's sorted target devices.
// For example, if the group has 5 accompany devices and 3 target devices,
// the result will be:
//   - 2 accompany devices to target device 0
//   - 2 accompany devices to target device 1
//   - 1 accompany device to target device 2
func assignAffinityGroup(result map[string]string, group *affinityGroup) {
	sort.Strings(group.accompanyResourceIDs)

	baseSize := len(group.accompanyResourceIDs) / len(group.targetResourceIDs)
	remainder := len(group.accompanyResourceIDs) % len(group.targetResourceIDs)
	offset := 0
	for i, targetResourceID := range group.targetResourceIDs {
		groupSize := baseSize
		if i < remainder {
			groupSize++
		}

		for _, accompanyResourceID := range group.accompanyResourceIDs[offset : offset+groupSize] {
			result[accompanyResourceID] = targetResourceID
		}
		offset += groupSize
	}
}

// IsSharedDeviceAllocationRequested reports whether the annotation requests
// shared allocation for the device being allocated. A missing key returns false.
func IsSharedDeviceAllocationRequested(annotations map[string]string, annotationKey string) bool {
	if annotationKey == "" {
		return false
	}
	value, ok := annotations[annotationKey]
	return ok && strings.EqualFold(strings.TrimSpace(value), "true")
}
