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

// QoSMBPolicy abstracts planning to distribute given MB to various QoS groups
type QoSMBPolicy interface {
	GetPlan(totalMB int, mbQoSGroups map[task.QoSLevel]*monitor.MBQoSGroup, isTopMost bool) *plan.MBAlloc
}

func BuildDefaultChainedQoSMBPolicy() QoSMBPolicy {
	// bottom up construction - reclaimed --> { system | shared } --> dedicated
	policyReclaimedLink := NewChainedQoSMBPolicy(
		map[task.QoSLevel]struct{}{task.QoSLevelReclaimedCores: {}},
		NewWeightedQoSMBPolicy(),
		nil)
	policySystemSharedLink := NewChainedQoSMBPolicy(
		map[task.QoSLevel]struct{}{
			task.QoSLevelSharedCores: {},
			task.QoSLevelSystemCores: {},
		},
		NewWeightedQoSMBPolicy(),
		policyReclaimedLink)
	policyDecidatedLink := NewChainedQoSMBPolicy(
		map[task.QoSLevel]struct{}{
			task.QoSLevelDedicatedCores: {},
		},
		NewWeightedQoSMBPolicy(),
		policySystemSharedLink)
	return policyDecidatedLink
}
