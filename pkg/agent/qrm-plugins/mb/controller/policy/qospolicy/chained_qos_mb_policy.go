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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

type priorityChainedMBPolicy struct {
	isTopLink bool
	qosTier   map[task.QoSLevel]struct{}
	tier      QoSMBPolicy
	next      QoSMBPolicy
}

func (p *priorityChainedMBPolicy) SetTopLink() {
	p.isTopLink = true
	p.tier.SetTopLink()
}

type mapQoSMB = map[task.QoSLevel]map[int]int

func (p *priorityChainedMBPolicy) splitQoS(currQoSMB mapQoSMB) (tierQoS, otherQoS mapQoSMB) {
	tierQoS = make(mapQoSMB)
	otherQoS = make(mapQoSMB)
	for qos, ccdMB := range currQoSMB {
		if _, ok := p.qosTier[qos]; ok {
			tierQoS[qos] = ccdMB
		} else {
			otherQoS[qos] = ccdMB
		}
	}
	return
}

func (p *priorityChainedMBPolicy) getTopPrioPlan(totalMB int, currQoSMB map[task.QoSLevel]map[int]int) (
	planTopTier *plan.MBAlloc, leftMB int, leftQoSMB map[task.QoSLevel]map[int]int,
) {
	topTierQoSMB := make(map[task.QoSLevel]map[int]int)
	topTierQoSMB, leftQoSMB = p.splitQoS(currQoSMB)

	topTierInUse := util.Sum(topTierQoSMB)
	otherMins := config.GetMins(util.GetQoSKeys(leftQoSMB)...)
	if topTierInUse >= totalMB-otherMins {
		// the priority barely meets its needs; it should be always prioritized
		// just take all MB for the top tier, besides the min MB for the left
		// todo: exclude qos that has no active pods at all (indicating by no traffic?)
		leftMB = otherMins
		planTopTier = p.tier.GetPlan(totalMB-leftMB, topTierQoSMB)
		return
	}

	topTireUpperBound := totalMB - otherMins
	// top tier has sufficient room for itself
	// its lounge zone (if applicable) should be honored,
	// unless otherwise exceeding its upper bound, or the left min unable to hold
	topTierToAllocate := topTierInUse + config.GetLounges(util.GetQoSKeys(topTierQoSMB)...)
	if topTireUpperBound < topTierToAllocate {
		topTierToAllocate = topTireUpperBound
	}

	free := totalMB - topTierToAllocate - util.Sum(leftQoSMB)
	// to identify how much free portion is for top tire
	freeTopTierPortion := 0
	if free > 0 && topTireUpperBound > topTierToAllocate {
		totalInUse := util.Sum(currQoSMB)
		freeTopTierPortion = int(float64(free) * float64(topTierInUse) / float64(totalInUse))
		if freeTopTierPortion >= topTireUpperBound-topTierToAllocate {
			freeTopTierPortion = topTireUpperBound - topTierToAllocate
		}
	}
	topTierToAllocate += freeTopTierPortion
	leftMB = totalMB - topTierToAllocate
	planTopTier = p.tier.GetPlan(topTierToAllocate, topTierQoSMB)
	return
}

func (p *priorityChainedMBPolicy) GetPlan(totalMB int, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	planTopTier, leftMB, leftQoSMB := p.getTopPrioPlan(totalMB, currQoSMB)

	var planLeft *plan.MBAlloc
	if p.next != nil && len(leftQoSMB) > 0 {
		// ensure top level of qos process as next if not this one
		if p.isTopLink && util.Sum(planTopTier.Plan) == 0 {
			p.next.SetTopLink()
		}
		planLeft = p.next.GetPlan(leftMB, leftQoSMB)
	}

	return plan.Merge(planTopTier, planLeft)
}

var _ QoSMBPolicy = &priorityChainedMBPolicy{}
