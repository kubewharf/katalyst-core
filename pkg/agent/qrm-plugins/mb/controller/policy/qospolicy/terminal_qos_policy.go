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

package qospolicy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const saturationThreshold = 8_000

type terminalQoSPolicy struct {
	ccdMBMin        int
	throttlePlanner strategy.LowPrioPlanner
	easePlanner     strategy.LowPrioPlanner
}

func (t terminalQoSPolicy) GetPlan(totalMB int, mbQoSGroups, globalMBQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc {
	general.InfofV(6, "mbm: +++++ terminal qos planner called, istopmost: %v", isTopMost)
	if isTopMost {
		return t.getTopMostPlan(totalMB, mbQoSGroups)
	}

	return t.getLeafPlan(totalMB, mbQoSGroups, globalMBQoSGroups)
}

func (t terminalQoSPolicy) getTopMostPlan(totalMB int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	return t.getFixedPlan(config.CCDMBMax, mbQoSGroups)
}

func (t terminalQoSPolicy) getFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[qosgroup.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, _ := range group.CCDs {
			mbPlan.Plan[qos][ccd] = fixed
		}
	}
	return mbPlan
}

// getLeafPlan actually cope with the low-priority qos groups (needing throttle with mb usage) only
func (t terminalQoSPolicy) getLeafPlan(totalMB int, mbQoSGroups, globalMBQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	// point of view of the receiver makes more sense for MB usage
	totalUsage := getReceiverMBUsage(mbQoSGroups, globalMBQoSGroups)
	general.InfofV(6, "mbm: +++++ (recv) total mb usage: %d, (sender) detail mb: %v", totalUsage, mbQoSGroups)

	if strategy.IsResourceUnderPressure(totalMB, totalUsage) {
		return t.throttlePlanner.GetPlan(totalMB, mbQoSGroups)
	}
	if strategy.IsResourceAtEase(totalMB, totalUsage) {
		return t.easePlanner.GetPlan(totalMB, mbQoSGroups)
	}

	// neither under pressure nor at ease, everything seems fine
	return nil
}

func getReceiverMBUsage(hostQoSMBGroup, globalQoSMBGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) int {
	result := 0
	for qos, group := range hostQoSMBGroup {
		for _, mb := range group.CCDMB {
			result += mb.LocalTotalMB
		}

		globalGroup := globalQoSMBGroups[qos] // it must exists
		for ccd, mb := range globalGroup.CCDMB {
			if _, ok := group.CCDMB[ccd]; ok { // one of the host ccds
				continue
			}
			result += mb.TotalMB - mb.LocalTotalMB
		}

	}
	return result
}

func (t terminalQoSPolicy) getProportionalPlan(ratio float64, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[qosgroup.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, mb := range group.CCDMB {
			newMB := int(ratio * float64(mb.TotalMB))
			if newMB > config.CCDMBMax {
				newMB = config.CCDMBMax
			}
			if newMB < t.ccdMBMin {
				newMB = t.ccdMBMin
			}
			mbPlan.Plan[qos][ccd] = newMB
		}
	}
	return mbPlan
}

func NewTerminalQoSPolicy(ccdMBMin int, throttleType, easeType strategy.LowPrioPlannerType) QoSMBPolicy {
	ccdGroupPlanner := strategy.NewCCDGroupPlanner(ccdMBMin, config.CCDMBMax)
	policy := terminalQoSPolicy{
		ccdMBMin:        ccdMBMin,
		throttlePlanner: strategy.New(throttleType, ccdGroupPlanner),
		easePlanner:     strategy.New(easeType, ccdGroupPlanner),
	}
	general.Infof("mbm: created terminal policy with throttle planner: %v, ease planner %v",
		policy.throttlePlanner.Name(),
		policy.easePlanner.Name())

	return &policy
}
