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

package policy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/qospolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// preemptDomainMBPolicy implements the admitting pod MB reservation (preemption)
type preemptDomainMBPolicy struct {
	qosMBPolicy qospolicy.QoSMBPolicy
}

func getReservationPlan(domain *mbdomain.MBDomain, total int, preemptingNodes []int) *plan.MBAlloc {
	if len(preemptingNodes) == 0 {
		return nil
	}

	ccds := make([]int, 0)
	for _, node := range preemptingNodes {
		ccds = append(ccds, domain.NodeCCDs[node]...)
	}

	ccdAverage := total / len(ccds)
	ccdMB := make(map[int]int)
	for _, ccd := range ccds {
		ccdMB[ccd] = ccdAverage
	}

	return &plan.MBAlloc{
		Plan: map[task.QoSLevel]map[int]int{
			task.QoSLevelDedicatedCores: ccdMB,
		},
	}
}

func (p preemptDomainMBPolicy) GetPlan(domain *mbdomain.MBDomain, currQoSMB map[task.QoSLevel]map[int]int) *plan.MBAlloc {
	preemptingNodes := domain.GetPreemptingNodes()
	mbToServe := mbdomain.ReservedPerNuma * len(preemptingNodes)
	reservationPlan := getReservationPlan(domain, mbToServe, preemptingNodes)
	general.InfofV(6, "mbm: domain %d hard reservation mb plan: %v", domain.ID, reservationPlan)

	mbAllocatable := mbdomain.DomainTotalMB - mbToServe
	allocatablePlan := p.qosMBPolicy.GetPlan(mbAllocatable, currQoSMB)

	return plan.Merge(reservationPlan, allocatablePlan)
}

func NewPreemptPolicy(chainedPolicy qospolicy.QoSMBPolicy) DomainMBPolicy {
	return &preemptDomainMBPolicy{
		qosMBPolicy: chainedPolicy,
	}
}
