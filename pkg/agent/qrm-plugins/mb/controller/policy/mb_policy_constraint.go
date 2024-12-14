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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

// constraintDomainMBPolicy implements soft-constraint mb policy
type constraintDomainMBPolicy struct {
	qosMBPolicy qospolicy.QoSMBPolicy
}

func (c constraintDomainMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	return c.qosMBPolicy.GetPlan(totalMB, currQoSMB, true)
}

func newConstraintDomainMBPolicy(qosMBPolicy qospolicy.QoSMBPolicy) DomainMBPolicy {
	return &constraintDomainMBPolicy{
		qosMBPolicy: qosMBPolicy,
	}
}

func NewDefaultConstraintDomainMBPolicy(ccdMBMin int) DomainMBPolicy {
	// combination of extreme throttling + half easing seems to make sense for scenarios of burst high qos loads; and
	// other combinations may make more sense
	qosMBPolicy := qospolicy.BuildHiPrioDetectedQoSMBPolicy(ccdMBMin, strategy.ExtremeThrottle, strategy.HalfEase)
	return newConstraintDomainMBPolicy(qosMBPolicy)
}
