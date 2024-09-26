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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
)

// QoSMBPolicy abstracts planning to distribute given MB to various QoS groups
type QoSMBPolicy interface {
	GetPlan(totalMB int, mbQoSGroups map[task.QoSLevel]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc
}

// BuildFullyChainedQoSPolicy builds up the full chain of {dedicated, shared_50, system} -> {shared_30}
func BuildFullyChainedQoSPolicy() QoSMBPolicy {
	return NewChainedQoSMBPolicy(
		map[task.QoSLevel]struct{}{
			"dedicated": {},
			"shared_50": {},
			"system":    {},
		},
		NewTerminalQoSPolicy(),
		NewTerminalQoSPolicy(),
	)
}

func BuildHiPrioDetectedQoSMBPolicy() QoSMBPolicy {
	//--[if any dedicated|shared_50 pod exist]:    {dedicated, shared_50, system} -> {shared_30}
	//        \ or ---------------------------:    {system, shared_30}

	// to build up {dedicated, shared_50, system} -> {shared_30}
	policyEither := BuildFullyChainedQoSPolicy()

	// to build up {system, shared_30}
	policyOr := NewTerminalQoSPolicy()

	// todo: check by pods instead of mb traffic
	// isTopMost arg is ignored as always true being the root branching in POC scenario
	anyDedicatedShared50PodExist := func(mbQoSGroups map[task.QoSLevel]*monitor.MBQoSGroup, _ bool) bool {
		mbTraffic := 0
		if shared50, ok := mbQoSGroups["shared_50"]; ok {
			mbTraffic += util.SumCCDMB(shared50.CCDMB)
		}
		if dedicated, ok := mbQoSGroups["dedicated"]; ok {
			mbTraffic += util.SumCCDMB(dedicated.CCDMB)
		}
		return mbTraffic > 0
	}
	policyBranched := NewValveQoSMBPolicy(anyDedicatedShared50PodExist, policyEither, policyOr)
	return policyBranched
}
