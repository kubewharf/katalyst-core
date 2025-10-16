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

package preoccupation

import (
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	preOccDeleteTimestampKey = "pre-occ-delete-timestamp"

	preOccAllocationExpireDuration = 10 * time.Minute
)

// PreOccAllocationFilter filters the allocation metadata for pre-occupied resources.
// It returns true if the allocation is for a dedicated NUMA binding, indicating pre-occupied resources.
func PreOccAllocationFilter(meta commonstate.AllocationMeta) bool {
	return meta.CheckDedicatedNUMABinding()
}

// SetPreOccDeleteTimestamp sets the pre-occupied delete timestamp annotation on the allocation metadata.
// It stores the current time in RFC3339 format for future reference.
func SetPreOccDeleteTimestamp(meta *commonstate.AllocationMeta, timestamp time.Time) {
	if meta == nil {
		return
	}

	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}

	if _, ok := meta.Annotations[preOccDeleteTimestampKey]; !ok {
		meta.Annotations[preOccDeleteTimestampKey] = timestamp.Format(time.RFC3339)
	}
}

// PreOccAllocationExpired checks if the pre-occupied allocation is expired.
// If the allocation is numa exclusive, the timestamp is always valid, it will be reclaimed only when
// other numa exclusive pod occupy it
func PreOccAllocationExpired(meta commonstate.AllocationMeta, now time.Time) bool {
	timestampStr := meta.Annotations[preOccDeleteTimestampKey]
	if timestampStr == "" {
		return false
	}

	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		general.Errorf("Error parsing pre-occ-delete-timestamp annotation: %v", err)
		return false
	}

	return now.Sub(timestamp) > preOccAllocationExpireDuration && !meta.CheckDedicatedNUMABindingNUMAExclusive()
}

func IsPreOccupiedContainer(req *pluginapi.ResourceRequest, allocation commonstate.AllocationMeta) bool {
	return req.PodName == allocation.PodName &&
		req.PodNamespace == allocation.PodNamespace &&
		req.ContainerName == allocation.ContainerName
}

func PreOccupationTopologyHints(hints []*pluginapi.TopologyHint, preOccupationNUMAs, withoutPreOccupationNUMAs machine.CPUSet) (bool, error) {
	preferIndexes := sets.NewInt()
	if !preOccupationNUMAs.IsEmpty() {
		for idx, hint := range hints {
			if hint == nil || !hint.Preferred {
				continue
			}

			origin, err := machine.NewCPUSetUint64(hint.Nodes...)
			if err != nil {
				return false, err
			}

			if origin.Equals(preOccupationNUMAs) {
				preferIndexes.Insert(idx)
			}
		}
	}

	if preferIndexes.Len() == 0 && !withoutPreOccupationNUMAs.IsEmpty() {
		for idx, hint := range hints {
			if hint == nil || !hint.Preferred {
				continue
			}

			origin, err := machine.NewCPUSetUint64(hint.Nodes...)
			if err != nil {
				return false, err
			}

			if origin.IsSubsetOf(withoutPreOccupationNUMAs) {
				preferIndexes.Insert(idx)
			}
		}
	}

	if len(preferIndexes) > 0 {
		for idx, hint := range hints {
			if hint == nil || !hint.Preferred {
				continue
			}

			if !preferIndexes.Has(idx) {
				hint.Preferred = false
			}
		}
		return true, nil
	}

	return false, nil
}
