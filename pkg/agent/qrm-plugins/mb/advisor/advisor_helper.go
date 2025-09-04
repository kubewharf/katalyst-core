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
	"fmt"
	"strings"

	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

// getMinEffectiveCapacity identifies the min dynamic capacity required by pre-defined groups,
// if the specific groups have active MB traffics
func getMinEffectiveCapacity(base int, groupCaps map[string]int, incomingStats monitor.GroupMBStats) int {
	min := base
	for group, groupStat := range incomingStats {
		// ignore the  group having no active traffic
		if !groupStat.HasTraffic() {
			continue
		}

		if groupCapacity, ok := groupCaps[group]; ok {
			if groupCapacity < min {
				min = groupCapacity
			}
		}
	}

	return min
}

func applyPlanCCDBoundsChecks(mbPlan *plan.MBPlan, minCCDMB int, maxCCDMB int) *plan.MBPlan {
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

func getGroupOutgoingTotals(group string, outgoings map[string][]monitor.MBInfo) []int {
	groupOutgoins, ok := outgoings[group]
	if !ok {
		groupOutgoins = nil
	}
	results := make([]int, len(groupOutgoins))
	for i := range groupOutgoins {
		results[i] = groupOutgoins[i].TotalMB
	}
	return results
}

func getGroupIncomingInfo(capacity int, incomingStats monitor.GroupMBStats) *resource.MBGroupIncomingStat {
	result := &resource.MBGroupIncomingStat{
		CapacityInMB: capacity,
	}

	result.GroupSorted = sortGroups(maps.Keys(incomingStats))
	result.GroupTotalUses = getUsedTotalByGroup(incomingStats)
	result.FreeInMB, result.GroupLimits = getLimitsByGroupSorted(capacity, result.GroupSorted, result.GroupTotalUses)
	result.ResourceState = resource.GetResourceState(capacity, result.FreeInMB)
	return result
}

func getLimitsByGroupSorted(capacity int, groupSorting []sets.String, groupUsages map[string]int) (int, map[string]int) {
	result := make(map[string]int)

	available := capacity
	// from high to low to deduct used out of current available
	for _, groups := range groupSorting {
		used := 0
		for group := range groups {
			result[group] = available
			used += groupUsages[group]
		}

		available -= used
		if available < 0 {
			available = 0
		}
	}

	return available, result
}

func getUsedTotalByGroup(stats monitor.GroupMBStats) map[string]int {
	result := make(map[string]int)
	for group, stat := range stats {
		result[group] = stat.SumStat().TotalMB
	}
	return result
}

func getDomainTotalMBs(domStats map[int]*resource.MBGroupIncomingStat) []int {
	domainNum := len(domStats)
	result := make([]int, domainNum)
	for i := range result {
		for _, v := range domStats[i].GroupTotalUses {
			result[i] += v
		}
	}
	return result
}

func stringify(groupDomValues map[string][]int) string {
	var sb strings.Builder
	sb.WriteString("{")
	for group, values := range groupDomValues {
		sb.WriteString(group)
		sb.WriteString(": {")
		for dom, v := range values {
			sb.WriteString(fmt.Sprintf("%d=%d,", dom, v))
		}
		sb.WriteString("},")
	}
	sb.WriteString("}")
	return sb.String()
}
