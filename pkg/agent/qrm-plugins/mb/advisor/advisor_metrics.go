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

package advisor

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	mbconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/policy/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	nameMBMIncomingStat           = "mbm_incoming_stat"
	nameMBMOutgoingStat           = "mbm_outgoing_stat"
	nameMBMCapacity               = "mbm_capacity"
	nameMBMFree                   = "mbm_free"
	nameMBMIncomingTarget         = "mbm_incoming_target"
	nameMBMOutgoingTarget         = "mbm_outgoing_target"
	nameMBMAdjustedOutgoingTarget = "mbm_outgoing_target_adjusted"
	namePlanRaw                   = "mbm_plan_raw"
	namePlanUpdate                = "mbm_plan_update"

	suppressionTypeDomainStress = "domain_stress"
	suppressionTypeCCDLimit     = "ccd_limit"
)

func (a *uniqPriorityAdvisor) emitDomIncomingStatSummaryMetrics(domLimits map[int]*resource.MBGroupIncomingStat) {
	for domID, limit := range domLimits {
		tags := map[string]string{
			"domain": fmt.Sprintf("%d", domID),
			"state":  string(limit.ResourceState),
		}
		emitKV(a.emitter, nameMBMCapacity, int64(limit.CapacityInMB*mbconsts.BytesPerMB), tags)
		emitKV(a.emitter, nameMBMFree, int64(limit.FreeInMB*mbconsts.BytesPerMB), tags)
	}
}

func (a *uniqPriorityAdvisor) emitStatsMtrics(domainsMon *monitor.DomainStats) {
	a.emitOutgoingStats(domainsMon.Outgoings)
	a.emitIncomingStats(domainsMon.Incomings)
}

func (a *uniqPriorityAdvisor) emitIncomingStats(incomings map[int]monitor.DomainMonStat) {
	a.emitStat(incomings, nameMBMIncomingStat)
}

func (a *uniqPriorityAdvisor) emitOutgoingStats(outgoings map[int]monitor.DomainMonStat) {
	a.emitStat(outgoings, nameMBMOutgoingStat)
}

func (a *uniqPriorityAdvisor) emitStat(stats map[int]monitor.DomainMonStat, metricName string) {
	for domId, monStat := range stats {
		for group, ccdMBs := range monStat {
			dom := fmt.Sprintf("%d", domId)
			for ccd, v := range ccdMBs {
				tags := map[string]string{
					"domain": dom,
					"group":  group,
					"ccd":    fmt.Sprintf("%d", ccd),
				}
				// MBInfo stores MB to avoid int32 overflow (60 GB = 60000 MB, within int range),
				// but metrics are emitted in bytes for consistency with other systems.
				emitKV(a.emitter, metricName, int64(v.TotalMB)*mbconsts.BytesPerMB, tags)
			}
		}
	}
}

func (a *uniqPriorityAdvisor) emitIncomingTargets(groupedDomIncomingTargets map[string][]int) {
	emitNamedGroupTargets(a.emitter, nameMBMIncomingTarget, groupedDomIncomingTargets)
}

func (a *uniqPriorityAdvisor) emitOutgoingTargets(groupedDomOutgoingTargets map[string][]int) {
	emitNamedGroupTargets(a.emitter, nameMBMOutgoingTarget, groupedDomOutgoingTargets)
}

func (a *uniqPriorityAdvisor) emitAdjustedOutgoingTargets(groupedDomOutgoingTargets map[string][]int) {
	emitNamedGroupTargets(a.emitter, nameMBMAdjustedOutgoingTarget, groupedDomOutgoingTargets)
}

func (a *uniqPriorityAdvisor) emitRawPlan(plan *plan.MBPlan) {
	a.emitPlanWithMetricName(plan, namePlanRaw)
}

func (a *uniqPriorityAdvisor) emitUpdatePlan(plan *plan.MBPlan) {
	a.emitPlanWithMetricName(plan, namePlanUpdate)
}

func (a *uniqPriorityAdvisor) emitPlanWithMetricName(plan *plan.MBPlan, metricName string) {
	if plan == nil || len(plan.MBGroups) == 0 {
		return
	}

	for group, ccdMBs := range plan.MBGroups {
		for ccd, v := range ccdMBs {
			tags := map[string]string{
				"group": group,
				"ccd":   fmt.Sprintf("%d", ccd),
			}
			emitKV(a.emitter, metricName, int64(v*mbconsts.BytesPerMB), tags)
		}
	}
}

func emitKV(emitter metrics.MetricEmitter, k string, v int64, tags map[string]string) {
	_ = emitter.StoreInt64(k,
		v,
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(tags)...,
	)
}

func emitNamedGroupTargets(emitter metrics.MetricEmitter, name string, groupedDomTargets map[string][]int) {
	for group, domTargets := range groupedDomTargets {
		for domID, v := range domTargets {
			tags := map[string]string{
				"group":  group,
				"domain": fmt.Sprintf("%d", domID),
			}
			emitKV(emitter, name, int64(v*mbconsts.BytesPerMB), tags)
		}
	}
}
