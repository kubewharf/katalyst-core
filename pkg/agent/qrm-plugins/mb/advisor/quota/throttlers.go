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

package quota

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"

// throttler throttle the groups by ensuring to set aside the reversed capacity
type throttler struct {
	reservationRatio int
}

func reserveFrom(available int, toReserve int) (left int, moreToReserve int) {
	if available < toReserve {
		left = 0
		moreToReserve = toReserve - available
		return
	}

	left = available - toReserve
	moreToReserve = 0
	return
}

func (t throttler) GetGroupQuotas(groupLimits *resource.MBGroupLimits) resource.GroupSettings {
	// 1. not to disrupt high-priority groups unless it has to;
	// 2. to throttle the groups having relatively low priority and accumulated to resource stress.
	quotas := resource.GroupSettings{}

	bufferNeeded := groupLimits.CapacityInMB*t.reservationRatio/100 - groupLimits.FreeInMB
	for i := len(groupLimits.GroupSorted) - 1; i >= 0; i-- {
		if bufferNeeded <= 0 {
			for group := range groupLimits.GroupSorted[i] {
				quotas[group] = groupLimits.GroupLimits[group]
			}
			continue
		}

		// reserve the needed buffer from this group, as much as it can
		oneGroup := resource.GetOne(groupLimits.GroupSorted[i])
		available := groupLimits.GroupLimits[oneGroup]
		available, bufferNeeded = reserveFrom(available, bufferNeeded)
		for group := range groupLimits.GroupSorted[i] {
			quotas[group] = available
		}
	}

	return quotas
}

var _ Quota = &throttler{}
