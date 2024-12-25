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
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/qospolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// preemptDomainMBPolicy implements the admitting pod MB reservation (preemption)
type preemptDomainMBPolicy struct {
	qosMBPolicy qospolicy.QoSMBPolicy

	lock sync.RWMutex
	qos  map[qosgroup.QoSGroup]*stat.MBQoSGroup
}

func (p *preemptDomainMBPolicy) ProcessGlobalQoSCCDMB(qos map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.qos = qos
}

// todo： consider to work on CCDs instead of nodes?
func getReservationPlan(domain *mbdomain.MBDomain, preemptingNodes []int) *plan.MBAlloc {
	ccds := make([]int, 0)
	for _, node := range preemptingNodes {
		ccds = append(ccds, domain.NodeCCDs[node]...)
	}

	ccdMB := make(map[int]int)
	for _, ccd := range ccds {
		ccdMB[ccd] = config.CCDMBMax
	}

	return &plan.MBAlloc{
		Plan: map[qosgroup.QoSGroup]map[int]int{
			qosgroup.QoSGroupDedicated: ccdMB,
		},
	}
}

func (p *preemptDomainMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	preemptingNodes := domain.GetPreemptingNodes()
	mbToReserve := config.ReservedPerNuma * len(preemptingNodes)
	reservationPlan := getReservationPlan(domain, preemptingNodes)
	general.InfofV(6, "mbm: domain %d hard reservation mb plan: %v", domain.ID, reservationPlan)

	mbAllocatable := totalMB - mbToReserve
	if mbAllocatable < 0 {
		mbAllocatable = 0
	}

	// todo: protect q.qos from accidental update race
	allocatablePlan := p.qosMBPolicy.GetPlan(mbAllocatable, currQoSMB, p.qos, true)

	return plan.Merge(reservationPlan, allocatablePlan)
}

func newPreemptDomainMBPolicy(chainedPolicy qospolicy.QoSMBPolicy) DomainMBPolicy {
	return &preemptDomainMBPolicy{
		qosMBPolicy: chainedPolicy,
	}
}

//func NewDefaultPreemptDomainMBPolicy(ccdMBMin int) DomainMBPolicy {
//	return NewPreemptDomainMBPolicy(ccdMBMin, strategy.ExtremeThrottle, strategy.HalfEase)
//}

func NewPreemptDomainMBPolicy(ccdMBMin int, throttleType, easeType strategy.LowPrioPlannerType) DomainMBPolicy {
	// since there is admitting socket pod, the qos policy is {dedicated, shared-50, system} -> {shared-30}
	qosMBPolicy := qospolicy.BuildFullyChainedQoSPolicy(ccdMBMin, throttleType, easeType)
	return newPreemptDomainMBPolicy(qosMBPolicy)
}
