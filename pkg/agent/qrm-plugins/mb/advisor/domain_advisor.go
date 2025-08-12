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
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/adjuster"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/distributor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/quota"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/sankey"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type domainAdvisor struct {
	emitter metrics.MetricEmitter

	domains domain.Domains

	defaultDomainCapacity int
	ccdMinMB              int
	ccdMaxMB              int

	// xDomGroups are the qos control groups that allow memory access across domains
	xDomGroups sets.String
	// groupNeverThrottles are  the groups not to throttle regardless of resource pressure status
	groupNeverThrottles sets.String
	// groupCapacityInMB specifies domain capacity demanded by control groups other than the base capacity
	groupCapacityInMB map[string]int

	quotaStrategy quota.Decider
	flower        sankey.DomainFlower
	adjusters     map[string]adjuster.Adjuster
	ccdDistribute distributor.Distributor
}

func (d *domainAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainsMon) (*plan.MBPlan, error) {
	// based on mb resource outgoing usage, decide incoming usage
	domainStats, err := d.calcIncomingDomainStats(ctx, domainsMon)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}
	d.emitDomIncomingStatMetrics(domainStats)

	// based on mb incoming usage, decide incoming quotas (i.e. targets) - by setting aside some buffer
	domQuotas := d.getIncomingDomainQuotas(ctx, domainStats)
	groupedDomIncomingTargets := resource.GetGroupedDomainSetting(domQuotas)
	d.emitIncomingTargets(groupedDomIncomingTargets)

	// for each group, based on incoming targets, decide what the outgoing targets are
	var groupedDomainOutgoingTargets map[string][]int
	groupedDomainOutgoingTargets, err = d.deriveOutgoingTargets(domainsMon, groupedDomIncomingTargets)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}
	d.emitOutgoingTargets(groupedDomainOutgoingTargets)

	groupedDomOutgoings := domainsMon.GetGroupedDomainOutgoingSummary()
	groupedDomainOutgoingQuotas := d.adjust(groupedDomainOutgoingTargets, groupedDomOutgoings)
	d.emitAdjustedOutgoingTargets(groupedDomainOutgoingQuotas)

	groupedCCDOutgoingQuotas := d.distribute(groupedDomainOutgoingQuotas, domainsMon.Outgoing)
	rawPlan := convertToPlan(groupedCCDOutgoingQuotas)
	d.emitRawPlan(rawPlan)

	// finalize plan in line with never-throttle groups and ccb mb checks
	checkedPlan := applyPlanCCDChecks(rawPlan, d.ccdMinMB, d.ccdMaxMB)
	updatePlan := maskPlanWithNoThrottles(checkedPlan, d.groupNeverThrottles, d.getNoThrottleMB())
	d.emitUpdatePlan(updatePlan)

	return updatePlan, nil
}

func (d *domainAdvisor) getNoThrottleMB() int {
	if d.ccdMaxMB > 0 {
		return d.ccdMaxMB
	}

	return d.defaultDomainCapacity
}

func (d *domainAdvisor) adjust(groupedSettings map[string][]int, observed map[string][]monitor.MBStat) map[string][]int {
	result := map[string][]int{}
	activeGroups := sets.String{}
	for group, values := range groupedSettings {
		currents := getGroupOutgoingTotals(group, observed)
		if _, ok := d.adjusters[group]; !ok {
			d.adjusters[group] = adjuster.New()
		}
		result[group] = d.adjusters[group].AdjustOutgoingTargets(values, currents)
		activeGroups.Insert(group)
	}

	// clean up to avoid memory leak
	if len(groupedSettings) > 0 {
		for group := range d.adjusters {
			if activeGroups.Has(group) {
				continue
			}
			delete(d.adjusters, group)
		}
	}

	return result
}

func (d *domainAdvisor) getIncomingDomainQuotas(ctx context.Context, domLimits map[int]*resource.MBGroupIncomingStat) map[int]resource.GroupSettings {
	domQuotas := map[int]resource.GroupSettings{}
	for dom, limits := range domLimits {
		domQuotas[dom] = d.quotaStrategy.GetGroupQuotas(limits)
	}
	return domQuotas
}

func (d *domainAdvisor) calcIncomingDomainStats(ctx context.Context, mon *monitor.DomainsMon) (map[int]*resource.MBGroupIncomingStat, error) {
	domainQuotas := make(map[int]*resource.MBGroupIncomingStat)
	var err error
	for domID, incomingStats := range mon.Incoming {
		domainQuotas[domID], err = d.calcIncomingStat(domID, incomingStats)
		if err != nil {
			return nil, errors.Wrap(err, "failed to calc domain quotas")
		}
	}
	return domainQuotas, nil
}

func (d *domainAdvisor) calcIncomingStat(domID int, incomingStats monitor.GroupMonStat) (*resource.MBGroupIncomingStat, error) {
	capacity, err := d.getEffectiveCapacity(domID, incomingStats)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calc domain capacity")
	}

	groupCapacities := distributeCapacityToGroups(capacity, incomingStats)
	return groupCapacities, nil
}

// getEffectiveCapacity gets the effective memory bandwidth capacity of specified domain, with its given resource usage
func (d *domainAdvisor) getEffectiveCapacity(domID int, domIncomingStats monitor.GroupMonStat) (int, error) {
	if _, ok := d.domains[domID]; !ok {
		return 0, fmt.Errorf("unknown domain %d", domID)
	}

	return getMinEffectiveCapacity(d.defaultDomainCapacity, d.groupCapacityInMB, domIncomingStats), nil
}

func (d *domainAdvisor) deriveOutgoingTargets(domainsMon *monitor.DomainsMon, incomingTargets map[string][]int,
) (map[string][]int, error) {
	// for each group: based on incoming targets, decide what the outgoing targets are
	result := make(map[string][]int)
	for group, domSums := range domainsMon.GetGroupedDomainOutgoingSummary() {
		groupIncomingTargets, ok := incomingTargets[group]
		if !ok {
			general.Infof("[mbm] no need to change group %s resource", group)
			result[group] = nil
			continue
		}

		if d.xDomGroups.Has(group) {
			localRatio := make([]float64, len(domSums))
			for i, domSum := range domSums {
				// limit the excessive remote traffic if applicable
				remoteTarget := d.domains[i].GetAlienMBLimit()
				if remoteTarget > domSum.RemoteMB {
					remoteTarget = domSum.RemoteMB
				}
				localRatio[i] = float64(domSum.LocalMB) / float64(domSum.LocalMB+remoteTarget)
			}
			outgoingTargets, err := d.flower.InvertFlow(localRatio, groupIncomingTargets)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get sourcing outgoing out of desired incoming targets")
			}

			result[group] = outgoingTargets
			continue
		}

		result[group] = groupIncomingTargets
	}

	return result, nil
}

func (d *domainAdvisor) distribute(quotas map[string][]int, outgoingStat map[int]monitor.GroupMonStat) map[string]map[int]int {
	result := map[string]map[int]int{}
	for group, domQuota := range quotas {
		// each domain is treated independently
		for domID, domTotal := range domQuota {
			ccdDistributions := d.domainDistributeGroup(domID, group, domTotal, outgoingStat)
			if len(ccdDistributions) == 0 {
				continue
			}

			if _, ok := result[group]; !ok {
				result[group] = make(map[int]int)
			}
			for ccd, v := range ccdDistributions {
				result[group][ccd] = v
			}
		}
	}
	return result
}

func (d *domainAdvisor) domainDistributeGroup(domID int, group string,
	domTotal int, outgoingStat map[int]monitor.GroupMonStat,
) map[int]int {
	weights := map[int]int{}
	groupStat := outgoingStat[domID][group]
	for ccd, stat := range groupStat {
		weights[ccd] = stat.TotalMB
	}
	domCCDQuotas := d.ccdDistribute.Distribute(domTotal, weights)
	result := map[int]int{}
	for ccd, v := range domCCDQuotas {
		result[ccd] = v
	}
	return result
}

func New(ccdMinMB, ccdMaxMB int, defaultDomainCapacity int,
	XDomGroups []string, groupNeverThrottles []string,
	groupCapacity map[string]int,
) Advisor {
	return &domainAdvisor{
		xDomGroups:            sets.NewString(XDomGroups...),
		groupNeverThrottles:   sets.NewString(groupNeverThrottles...),
		groupCapacityInMB:     groupCapacity,
		quotaStrategy:         quota.New(),
		flower:                sankey.New(),
		adjusters:             map[string]adjuster.Adjuster{},
		ccdDistribute:         distributor.New(ccdMinMB, ccdMaxMB),
		ccdMaxMB:              ccdMaxMB,
		defaultDomainCapacity: defaultDomainCapacity,
	}
}
