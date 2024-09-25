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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type weightedQoSMBPolicy struct{}

func (w *weightedQoSMBPolicy) GetPlan(totalMB int, qosGroupMBs map[task.QoSLevel]*monitor.MBQoSGroup, isTopTier bool) *plan.MBAlloc {
	if isTopTier {
		return w.getTopLevelPlan(totalMB, qosGroupMBs)
	}

	return w.getProportionalPlan(totalMB, qosGroupMBs)
}

func (w *weightedQoSMBPolicy) getProportionalPlan(totalMB int, qosGroupMBs map[task.QoSLevel]*monitor.MBQoSGroup) *plan.MBAlloc {
	totalUsage := monitor.SumMB(qosGroupMBs)

	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSLevel]map[int]int)}
	for qos, groupMB := range qosGroupMBs {
		for ccd, mb := range groupMB.CCDMB {
			if _, ok := mbPlan.Plan[qos]; !ok {
				mbPlan.Plan[qos] = make(map[int]int)
			}
			mbPlan.Plan[qos][ccd] = int(float64(totalMB) / float64(totalUsage) * float64(mb))
		}
	}

	return mbPlan
}

func (w *weightedQoSMBPolicy) getTopLevelPlan(totalMB int, qosGroups map[task.QoSLevel]*monitor.MBQoSGroup) *plan.MBAlloc {
	// don't set throttling at all for top level QoS group's CCDs; instead allow more than the totalMB
	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSLevel]map[int]int)}
	for qos, group := range qosGroups {
		for ccd, mb := range group.CCDMB {
			if mb > 0 {
				if _, ok := mbPlan.Plan[qos]; !ok {
					mbPlan.Plan[qos] = make(map[int]int)
				}
				mbPlan.Plan[qos][ccd] = mbdomain.MaxMBPerCCD
			}
		}
	}

	return mbPlan
}

func NewWeightedQoSMBPolicy() QoSMBPolicy {
	return &weightedQoSMBPolicy{}
}
