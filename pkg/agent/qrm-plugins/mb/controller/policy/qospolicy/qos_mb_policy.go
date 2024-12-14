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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

// QoSMBPolicy abstracts planning to distribute given MB to various QoS groups
type QoSMBPolicy interface {
	GetPlan(totalMB int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc
}

// BuildFullyChainedQoSPolicy builds up the full chain of {dedicated, shared-50, system} -> {shared-30, reclaimed}
func BuildFullyChainedQoSPolicy(ccdMBMin int, throttleType, easeType strategy.LowPrioPlannerType) QoSMBPolicy {
	return NewChainedQoSMBPolicy(
		map[qosgroup.QoSGroup]struct{}{
			"dedicated": {},
			"shared-50": {},
			"system":    {},
		},
		NewTerminalQoSPolicy(ccdMBMin, throttleType, easeType),
		NewTerminalQoSPolicy(ccdMBMin, throttleType, easeType),
	)
}

func BuildHiPrioDetectedQoSMBPolicy(ccdMBMin int, throttleType, easeType strategy.LowPrioPlannerType) QoSMBPolicy {
	//--[if any dedicated|shared-50 pod exist]:    {dedicated, shared-50, system} -> {shared-30}
	//        \ or ---------------------------:    {system, shared-30}

	// to build up {dedicated, shared-50, system} -> {shared-30}
	policyEither := BuildFullyChainedQoSPolicy(ccdMBMin, throttleType, easeType)

	// to build up {system, shared-30}
	policyOr := NewTerminalQoSPolicy(ccdMBMin, throttleType, easeType)

	// todo: check by pods instead of mb traffic
	// isTopMost arg is ignored as always true being the root branching in POC scenario
	anyDedicatedShared50PodExist := func(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup, _ bool) bool {
		mbTraffic := 0
		if shared50, ok := mbQoSGroups["shared-50"]; ok {
			mbTraffic += monitor.SumCCDMB(shared50.CCDMB)
		}
		if dedicated, ok := mbQoSGroups["dedicated"]; ok {
			mbTraffic += monitor.SumCCDMB(dedicated.CCDMB)
		}
		return mbTraffic > 0
	}
	policyBranched := NewValveQoSMBPolicy(anyDedicatedShared50PodExist, policyEither, policyOr)
	return policyBranched
}
