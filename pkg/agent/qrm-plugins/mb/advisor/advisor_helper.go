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

package advisor

import (
	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func getMinEffectiveCapacity(base int, dynaCaps map[string]int, incomingStats monitor.GroupMonStat) int {
	min := base

	// identify the min dynamic capacity required by pre-defined groups, if the specific groups do have active MB traffics
	for group, groupStat := range incomingStats {
		if !groupStat.HasTraffic() {
			continue
		}

		if groupCapacity, ok := dynaCaps[group]; ok {
			if groupCapacity < min {
				min = groupCapacity
			}
		}
	}

	return min
}

func applyPlanCCDChecks(mbPlan *plan.MBPlan, minCCDMB int, maxCCDMB int) *plan.MBPlan {
	if mbPlan == nil || len(mbPlan.MBGroups) == 0 {
		return mbPlan
	}

	if minCCDMB > 0 {
		for group, ccdMBs := range mbPlan.MBGroups {
			for ccd, mb := range ccdMBs {
				if mb < minCCDMB {
					mbPlan.MBGroups[group][ccd] = minCCDMB
				}
			}
		}
	}

	if maxCCDMB > 0 {
		for group, ccdMBs := range mbPlan.MBGroups {
			for ccd, mb := range ccdMBs {
				if mb > maxCCDMB {
					mbPlan.MBGroups[group][ccd] = maxCCDMB
				}
			}
		}
	}

	return mbPlan
}

func maskPlanWithNoThrottles(updatePlan *plan.MBPlan, groupNoThrottles sets.String, noThrottleCCDMB int) *plan.MBPlan {
	if updatePlan == nil || noThrottleCCDMB == 0 {
		return updatePlan
	}

	result := updatePlan
	// never-throttle groups are of exceptional significance; should be allowed to use mb resource freely whatsoever
	for notToThrottle := range groupNoThrottles {
		if _, ok := result.MBGroups[notToThrottle]; !ok {
			continue
		}
		noThrottles := map[int]int{}
		for ccd := range result.MBGroups[notToThrottle] {
			noThrottles[ccd] = noThrottleCCDMB
		}
		result.MBGroups[notToThrottle] = noThrottles
	}
	return result
}

func convertToPlan(quotas map[string]map[int]int) *plan.MBPlan {
	updatePlan := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{},
	}
	for group, ccdQuotas := range quotas {
		if len(ccdQuotas) == 0 {
			continue
		}
		updatePlan.MBGroups[group] = ccdQuotas
	}
	return updatePlan
}

func getGroupOutgoingTotals(group string, outgoings map[string][]monitor.MBStat) []int {
	groupOutgoins, ok := outgoings[group]
	if !ok {
		groupOutgoins = nil
	}
	results := make([]int, len(groupOutgoins))
	for i, v := range groupOutgoins {
		results[i] = v.TotalMB
	}
	return results
}

func distributeCapacityToGroups(capacity int, incomingStats monitor.GroupMonStat) *resource.MBGroupIncomingStat {
	result := &resource.MBGroupIncomingStat{
		CapacityInMB:   capacity,
		GroupTotalUses: map[string]int{},
		GroupLimits:    map[string]int{},
	}

	// from hi to lo to distribute limits, stripping usage out of the determined capacity
	balance := capacity
	groups := maps.Keys(incomingStats)
	sortedGroups := sortGroups(groups)
	for _, groups := range sortedGroups {
		// all equivalent groups shared the same available balance
		for group := range groups {
			result.GroupLimits[group] = balance
		}

		used := 0
		for group := range groups {
			groupUse := incomingStats[group].SumStat().TotalMB
			result.GroupTotalUses[group] = groupUse
			used += groupUse
		}

		if balance < used {
			balance = 0
			continue
		}

		balance -= used
	}

	result.ResourceState = resource.GetResourceState(capacity, balance)
	result.FreeInMB = balance
	result.GroupSorted = sortedGroups

	return result
}
