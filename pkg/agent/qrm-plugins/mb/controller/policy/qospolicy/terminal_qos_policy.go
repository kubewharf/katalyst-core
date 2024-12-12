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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

const saturationThreshold = 8_000

type terminalQoSPolicy struct {
	ccdMBMin int
}

func (t terminalQoSPolicy) GetPlan(totalMB int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc {
	if isTopMost {
		return t.getTopMostPlan(totalMB, mbQoSGroups)
	}

	return t.getLeafPlan(totalMB, mbQoSGroups)
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

func (t terminalQoSPolicy) getLeafPlan(totalMB int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	// when there is little mb left after fulfilling this leaf tier (almost saturation),
	// the leaf tier has to be suppressed hard to the mininum, in the fastest way to give away
	// mb room to high prio tasks likely in need
	totalUsage := monitor.SumMB(mbQoSGroups)
	if totalMB-totalUsage <= saturationThreshold {
		return t.getFixedPlan(t.ccdMBMin, mbQoSGroups)
	}

	// distribute total among all proportionally
	ratio := float64(totalMB) / float64(totalUsage)
	return t.getProportionalPlan(ratio, mbQoSGroups)
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

func NewTerminalQoSPolicy(ccdMBMin int) QoSMBPolicy {
	return &terminalQoSPolicy{
		ccdMBMin: ccdMBMin,
	}
}
