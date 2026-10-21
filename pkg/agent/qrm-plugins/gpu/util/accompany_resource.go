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

// ResolveAffinityDeviceByAccompanyResource takes the machine-wide topology mapping
// from accompany-resource devices to target-resource devices. For each accompany
// device, it selects the first target device with affinity in the highest-priority
// non-empty dimension. Multiple accompany devices may resolve to the same target.
func ResolveAffinityDeviceByAccompanyResource(
	affinityByAccompanyResource map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
) map[string]string {
	priorities := append([]string(nil), priorityDimensions...)
	if len(priorities) == 0 {
		// NUMA preserves the existing fallback when no dimensions are reported.
		priorities = []string{machine.DimensionNuma}
	}

	affinityDeviceByAccompanyResource := make(map[string]string, len(affinityByAccompanyResource))
	for accompanyResourceID, affinityByDimension := range affinityByAccompanyResource {
		for _, dimension := range priorities {
			resourceIDs := append(machine.DeviceIDs(nil), affinityByDimension[dimension]...)
			if len(resourceIDs) == 0 {
				continue
			}
			// Sorting makes affinity device selection deterministic.
			sort.Strings(resourceIDs)
			affinityDeviceByAccompanyResource[accompanyResourceID] = resourceIDs[0]
			break
		}
	}
	return affinityDeviceByAccompanyResource
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
