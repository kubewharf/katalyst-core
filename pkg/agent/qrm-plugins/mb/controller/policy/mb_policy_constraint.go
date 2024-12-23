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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/qospolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

// constraintDomainMBPolicy implements soft-constraint mb policy
type constraintDomainMBPolicy struct {
	qosMBPolicy qospolicy.QoSMBPolicy

	lock sync.RWMutex
	qos  map[qosgroup.QoSGroup]*monitor.MBQoSGroup
}

func (c *constraintDomainMBPolicy) ProcessGlobalQoSCCDMB(qos map[qosgroup.QoSGroup]*monitor.MBQoSGroup) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.qos = qos
}

func (c *constraintDomainMBPolicy) getQosMBGroups() map[qosgroup.QoSGroup]*monitor.MBQoSGroup {
	c.lock.RLock()
	defer c.lock.RUnlock()
	qos := syntax.DeepCopy(c.qos)
	if result, ok := qos.(map[qosgroup.QoSGroup]*monitor.MBQoSGroup); ok {
		return result
	}
	return nil
}

func (c *constraintDomainMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	qos := c.getQosMBGroups()
	return c.qosMBPolicy.GetPlan(totalMB, currQoSMB, qos, true)
}

func newConstraintDomainMBPolicy(qosMBPolicy qospolicy.QoSMBPolicy) DomainMBPolicy {
	return &constraintDomainMBPolicy{
		qosMBPolicy: qosMBPolicy,
	}
}

//func NewDefaultConstraintDomainMBPolicy(ccdMBMin int) DomainMBPolicy {
//	// combination of extreme throttling + half easing seems to make sense for scenarios of burst high qos loads; and
//	// other combinations may make more sense
//	return NewConstraintDomainMBPolicy(ccdMBMin, strategy.ExtremeThrottle, strategy.HalfEase)
//}

func NewConstraintDomainMBPolicy(ccdMBMin int, throttleType, easeType strategy.LowPrioPlannerType) DomainMBPolicy {
	qosMBPolicy := qospolicy.BuildHiPrioDetectedQoSMBPolicy(ccdMBMin, throttleType, easeType)
	return newConstraintDomainMBPolicy(qosMBPolicy)
}
