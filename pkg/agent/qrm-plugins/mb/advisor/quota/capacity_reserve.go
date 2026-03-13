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

import (
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// ratioCapacityReserver throttle the groups by ensuring to set aside the reversed capacity
type ratioCapacityReserver struct {
	minReserveRatio int
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

func (r ratioCapacityReserver) GetGroupQuotas(groupLimits *resource.MBGroupIncomingStat) resource.GroupSettings {
	// 1. not to disrupt high-priority groups unless it has to;
	// 2. to throttle the groups having relatively low priority and accumulated to resource stress.
	quotas := resource.GroupSettings{}

	bufferNeeded := groupLimits.CapacityInMB*r.minReserveRatio/100 - groupLimits.FreeInMB
	for i := len(groupLimits.GroupSorted) - 1; i >= 0; i-- {
		if bufferNeeded <= 0 {
			for group := range groupLimits.GroupSorted[i] {
				quotas[group] = groupLimits.GroupLimits[group]
			}
			continue
		}

		// reserve the needed buffer from this group, as much as it can
		available, err := getAvailableOfEquivGroups(groupLimits.GroupSorted[i], groupLimits)
		if err != nil {
			general.Warningf("[mbm] failed to get available mb of group %d: %v", i, err)
			continue
		}
		available, bufferNeeded = reserveFrom(available, bufferNeeded)
		for group := range groupLimits.GroupSorted[i] {
			quotas[group] = available
		}
	}

	return quotas
}

func getAvailableOfEquivGroups(equivGroups sets.String, groupLimits *resource.MBGroupIncomingStat) (int, error) {
	// all elements of equivGroups have identical GroupLimits value; suffice to get any
	oneGroup, err := resource.GetGroupRepresentative(equivGroups)
	if err != nil {
		return 0, errors.Wrap(err, fmt.Sprintf("failed to get representative of equivalent groups %v", equivGroups))
	}

	available := groupLimits.GroupLimits[oneGroup]
	return available, nil
}

var _ Decider = &ratioCapacityReserver{}
