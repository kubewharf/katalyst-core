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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type weightedQoSMBPolicy struct {
	isTopLink bool
}

func (w *weightedQoSMBPolicy) SetTopLink() {
	w.isTopLink = true
}

func (w *weightedQoSMBPolicy) GetPlan(totalMB int, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	if w.isTopLink {
		return w.getTopLevelPlan(totalMB, currQoSMB)
	}

	return w.getProportionalPlan(totalMB, currQoSMB)
}

func (w *weightedQoSMBPolicy) getProportionalPlan(totalMB int, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	totalUsage := util.Sum(currQoSMB)

	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSLevel]map[int]int)}
	for qos, ccdMB := range currQoSMB {
		for ccd, mb := range ccdMB {
			if _, ok := mbPlan.Plan[qos]; !ok {
				mbPlan.Plan[qos] = make(map[int]int)
			}
			mbPlan.Plan[qos][ccd] = int(float64(totalMB) / float64(totalUsage) * float64(mb))
		}
	}

	return mbPlan
}

func (w *weightedQoSMBPolicy) getTopLevelPlan(totalMB int, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	// don't set throttling at all for top level QoS CCDs
	mbPlan := &plan.MBAlloc{Plan: make(map[task.QoSLevel]map[int]int)}
	for qos, ccdMB := range currQoSMB {
		for ccd, mb := range ccdMB {
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
