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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type terminalQoSPolicy struct{}

func (t terminalQoSPolicy) GetPlan(totalMB int, mbQoSGroups map[task.QoSGroup]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc {
	if isTopMost {
		return getTopMostPlan(totalMB, mbQoSGroups)
	}

	return getLeafPlan(totalMB, mbQoSGroups)
}

func getTopMostPlan(totalMB int, mbQoSGroups map[task.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	return getFixedPlan(config.CCDMBMax, mbQoSGroups)
}

func getFixedPlan(fixed int, mbQoSGroups map[task.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, _ := range group.CCDs {
			mbPlan.Plan[qos][ccd] = fixed
		}
	}
	return mbPlan
}

func getLeafPlan(totalMB int, mbQoSGroups map[task.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	// distribute total among all proportionally
	totalUsage := monitor.SumMB(mbQoSGroups)
	if totalUsage == 0 {
		return getFixedPlan(config.CCDMBMin, mbQoSGroups)
	}

	ratio := float64(totalMB) / float64(totalUsage)
	return getProportionalPlan(ratio, mbQoSGroups)
}

func getProportionalPlan(ratio float64, mbQoSGroups map[task.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, mb := range group.CCDMB {
			newMB := int(ratio * float64(mb.ReadsMB+mb.WritesMB))
			if newMB > config.CCDMBMax {
				newMB = config.CCDMBMax
			}
			if newMB < config.CCDMBMin {
				newMB = config.CCDMBMin
			}
			mbPlan.Plan[qos][ccd] = newMB
		}
	}
	return mbPlan
}

func NewTerminalQoSPolicy() QoSMBPolicy {
	return &terminalQoSPolicy{}
}
