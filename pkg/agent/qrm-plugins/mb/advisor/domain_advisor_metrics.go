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
)

func (d *domainAdvisor) emitDomIncomingStatSummaryMetrics(domLimits map[int]*resource.MBGroupIncomingStat) {
	for domID, limit := range domLimits {
		tags := map[string]string{
			"domain": fmt.Sprintf("%d", domID),
			"state":  string(limit.ResourceState),
		}
		emitKV(d.emitter, nameMBMCapacity, limit.CapacityInMB, tags)
		emitKV(d.emitter, nameMBMFree, limit.FreeInMB, tags)
	}
}

func (d *domainAdvisor) emitStatsMtrics(domainsMon *monitor.DomainStats) {
	d.emitOutgoingStats(domainsMon.Outgoings)
	d.emitIncomingStats(domainsMon.Incomings)
}

func (d *domainAdvisor) emitIncomingStats(incomings map[int]monitor.DomainMonStat) {
	d.emitStat(incomings, nameMBMIncomingStat)
}

func (d *domainAdvisor) emitOutgoingStats(outgoings map[int]monitor.DomainMonStat) {
	d.emitStat(outgoings, nameMBMOutgoingStat)
}

func (d *domainAdvisor) emitStat(stats map[int]monitor.DomainMonStat, metricName string) {
	for domId, monStat := range stats {
		for group, ccdMBs := range monStat {
			dom := fmt.Sprintf("%d", domId)
			for ccd, v := range ccdMBs {
				tags := map[string]string{
					"domain": dom,
					"group":  group,
					"ccd":    fmt.Sprintf("%d", ccd),
				}
				emitKV(d.emitter, metricName, v.TotalMB, tags)
			}
		}
	}
}

func (d *domainAdvisor) emitIncomingTargets(groupedDomIncomingTargets map[string][]int) {
	emitNamedGroupTargets(d.emitter, nameMBMIncomingTarget, groupedDomIncomingTargets)
}

func (d *domainAdvisor) emitOutgoingTargets(groupedDomOutgoingTargets map[string][]int) {
	emitNamedGroupTargets(d.emitter, nameMBMOutgoingTarget, groupedDomOutgoingTargets)
}

func (d *domainAdvisor) emitAdjustedOutgoingTargets(groupedDomOutgoingTargets map[string][]int) {
	emitNamedGroupTargets(d.emitter, nameMBMAdjustedOutgoingTarget, groupedDomOutgoingTargets)
}

func (d *domainAdvisor) emitRawPlan(plan *plan.MBPlan) {
	d.emitPlanWithMetricName(plan, namePlanRaw)
}

func (d *domainAdvisor) emitUpdatePlan(plan *plan.MBPlan) {
	d.emitPlanWithMetricName(plan, namePlanUpdate)
}

func (d *domainAdvisor) emitPlanWithMetricName(plan *plan.MBPlan, metricName string) {
	if plan == nil || len(plan.MBGroups) == 0 {
		return
	}

	for group, ccdMBs := range plan.MBGroups {
		for ccd, v := range ccdMBs {
			tags := map[string]string{
				"group": group,
				"ccd":   fmt.Sprintf("%d", ccd),
			}
			emitKV(d.emitter, metricName, v, tags)
		}
	}
}

func emitKV(emitter metrics.MetricEmitter, k string, v int, tags map[string]string) {
	_ = emitter.StoreInt64(k,
		int64(v),
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
			emitKV(emitter, name, v, tags)
		}
	}
}
