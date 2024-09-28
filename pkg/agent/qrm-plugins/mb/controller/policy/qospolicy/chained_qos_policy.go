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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type chainedQosPolicy struct {
	currQoSLevels map[task.QoSGroup]struct{}
	current       QoSMBPolicy
	next          QoSMBPolicy
}

func (p *chainedQosPolicy) splitQoSGroups(groups map[task.QoSGroup]*monitor.MBQoSGroup) (
	curr, others map[task.QoSGroup]*monitor.MBQoSGroup,
) {
	curr = make(map[task.QoSGroup]*monitor.MBQoSGroup)
	others = make(map[task.QoSGroup]*monitor.MBQoSGroup)
	for qos, ccdMB := range groups {
		if _, ok := p.currQoSLevels[qos]; ok {
			curr[qos] = ccdMB
		} else {
			others[qos] = ccdMB
		}
	}
	return
}

func (p *chainedQosPolicy) GetPlan(totalMB int, qosGroups map[task.QoSGroup]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc {
	currGroups, nextGroups := p.splitQoSGroups(qosGroups)
	planCurrTier := p.current.GetPlan(totalMB, currGroups, isTopMost)
	leftMB := totalMB - monitor.SumMB(currGroups)
	planNextTiers := p.next.GetPlan(leftMB, nextGroups, false)
	return plan.Merge(planCurrTier, planNextTiers)
}

func NewChainedQoSMBPolicy(currQoSLevels map[task.QoSGroup]struct{}, current, next QoSMBPolicy) QoSMBPolicy {
	return &chainedQosPolicy{
		currQoSLevels: currQoSLevels,
		current:       current,
		next:          next,
	}
}
