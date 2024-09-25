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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type priorityChainedMBPolicy struct {
	currQoSLevels map[task.QoSLevel]struct{}
	current       QoSMBPolicy
	next          QoSMBPolicy
}

func (p *priorityChainedMBPolicy) splitQoSGroups(groups map[task.QoSLevel]*monitor.MBQoSGroup) (
	top, others map[task.QoSLevel]*monitor.MBQoSGroup,
) {
	top = make(map[task.QoSLevel]*monitor.MBQoSGroup)
	others = make(map[task.QoSLevel]*monitor.MBQoSGroup)
	for qos, ccdMB := range groups {
		if _, ok := p.currQoSLevels[qos]; ok {
			top[qos] = ccdMB
		} else {
			others[qos] = ccdMB
		}
	}
	return
}

func (p *priorityChainedMBPolicy) processCurrentTier(isTopTier bool, totalMB int, qosGroups map[task.QoSLevel]*monitor.MBQoSGroup) (
	planTopTier *plan.MBAlloc, leftMB int, nextGroups map[task.QoSLevel]*monitor.MBQoSGroup,
) {
	topTierGroups := make(map[task.QoSLevel]*monitor.MBQoSGroup)
	topTierGroups, nextGroups = p.splitQoSGroups(qosGroups)

	topTierInUse := monitor.SumMB(topTierGroups)
	otherMins := config.GetMins(monitor.GetQoSKeys(nextGroups)...)
	if topTierInUse >= totalMB-otherMins {
		// the priority barely meets its needs; it should be always prioritized
		// just take all MB for the top current, besides the min MB for the left
		// todo: exclude qos that has no active pods at all (indicating by no traffic?)
		leftMB = otherMins
		planTopTier = p.current.GetPlan(totalMB-leftMB, topTierGroups, isTopTier)
		return
	}

	topTireUpperBound := totalMB - otherMins
	// top current has sufficient room for itself
	// its lounge zone (if applicable) should be honored,
	// unless otherwise exceeding its upper bound, or the left min unable to hold
	topTierToAllocate := topTierInUse + config.GetLounges(monitor.GetQoSKeys(topTierGroups)...)
	if topTireUpperBound < topTierToAllocate {
		topTierToAllocate = topTireUpperBound
	}

	free := totalMB - topTierToAllocate - monitor.SumMB(nextGroups)
	// to identify how much free portion is for top tire
	freeTopTierPortion := 0
	if free > 0 && topTireUpperBound > topTierToAllocate {
		totalInUse := monitor.SumMB(qosGroups)
		freeTopTierPortion = int(float64(free) * float64(topTierInUse) / float64(totalInUse))
		if freeTopTierPortion >= topTireUpperBound-topTierToAllocate {
			freeTopTierPortion = topTireUpperBound - topTierToAllocate
		}
	}
	topTierToAllocate += freeTopTierPortion
	leftMB = totalMB - topTierToAllocate
	planTopTier = p.current.GetPlan(topTierToAllocate, topTierGroups, isTopTier)
	return
}

func (p *priorityChainedMBPolicy) GetPlan(totalMB int, qosGroups map[task.QoSLevel]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc {
	planCurrentTier, leftMB, leftMBGroups := p.processCurrentTier(isTopMost, totalMB, qosGroups)

	var planLeft *plan.MBAlloc
	if p.next != nil && len(leftMBGroups) > 0 {
		// ensure top level of qos process as next if not this one
		nextIsTopMose := isTopMost && util.Sum(planCurrentTier.Plan) == 0
		//if p.isEffectiveTopLink && util.Sum(planCurrentTier.Plan) == 0 {
		//	p.next.SetTopLink()
		//}
		planLeft = p.next.GetPlan(leftMB, leftMBGroups, nextIsTopMose)
	}

	return plan.Merge(planCurrentTier, planLeft)
}

func NewChainedQoSMBPolicy(currQoSLevels map[task.QoSLevel]struct{}, current, next QoSMBPolicy) QoSMBPolicy {
	return &priorityChainedMBPolicy{
		currQoSLevels: currQoSLevels,
		current:       current,
		next:          next,
	}
}
