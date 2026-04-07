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
	"k8s.io/klog/v2"

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

// uniqPriorityAdvisor is an priorityAdvisor which can only work with resctrl groups of distinct priorities;
// due to this limitation, it should not be used directly.
type uniqPriorityAdvisor struct {
	emitter metrics.MetricEmitter

	domains domain.Domains

	defaultDomainCapacity int
	ccdMinMB              int
	ccdMaxMB              int
	capPercent            int

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

func (a *uniqPriorityAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	a.emitStatsMtrics(domainsMon)

	// identify mb incoming usage etc since the capacity applies to incoming traffic
	domIncomingInfo, err := a.calcIncomingDomainStats(ctx, domainsMon)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}
	if klog.V(6).Enabled() {
		domTotalMBs := getDomainTotalMBs(domIncomingInfo)
		general.InfofV(6, "[mbm] [priorityAdvisor] domains incoming total: %v", domTotalMBs)
	}
	a.emitDomIncomingStatSummaryMetrics(domIncomingInfo)

	// based on mb incoming usage info, decide incoming quotas (i.e. targets)
	domIncomingQuotas := a.getIncomingDomainQuotas(ctx, domIncomingInfo)
	groupedDomIncomingTargets := domIncomingQuotas.GetGroupedDomainSetting()
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] group-domain incoming targets: %s",
			stringify(groupedDomIncomingTargets))
	}
	a.emitIncomingTargets(groupedDomIncomingTargets)

	// for each group, based on incoming targets, decide what the outgoing targets are
	var groupedDomOutgoingTargets map[string][]int
	groupedDomOutgoingTargets, err = a.deriveOutgoingTargets(ctx, domainsMon.OutgoingGroupSumStat, groupedDomIncomingTargets)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] group-domain outgoing targets: %s",
			stringify(groupedDomOutgoingTargets))
	}
	a.emitOutgoingTargets(groupedDomOutgoingTargets)

	// leverage the current observed outgoing stats (and implicit previous outgoing mb)
	// to adjust th outgoing mb hopeful to reach the desired target
	groupedDomOutgoings := domainsMon.OutgoingGroupSumStat
	groupedDomainOutgoingQuotas := a.adjust(ctx, groupedDomOutgoingTargets, groupedDomOutgoings, a.capPercent)
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] group-domain outgoing quotas adjusted: %s",
			stringify(groupedDomainOutgoingQuotas))
	}
	a.emitAdjustedOutgoingTargets(groupedDomainOutgoingQuotas)

	// split outgoing mb to ccd level
	groupedCCDOutgoingQuotas := a.distributeToCCDs(ctx, groupedDomainOutgoingQuotas, domainsMon.Outgoings)
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] group-ccd outgoing quotas: %v", groupedCCDOutgoingQuotas)
	}
	rawPlan := convertToPlan(groupedCCDOutgoingQuotas)
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] raw plan: %s", rawPlan)
	}
	a.emitRawPlan(rawPlan)

	// finalize plan with never-throttle groups and ccb mb checks
	checkedPlan := applyPlanCCDBoundsChecks(rawPlan, a.ccdMinMB, a.ccdMaxMB)
	updatePlan := maskPlanWithNoThrottles(checkedPlan, a.groupNeverThrottles, a.getNoThrottleMB())
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] mb plan update: %s", updatePlan)
	}
	a.emitUpdatePlan(updatePlan)

	return updatePlan, nil
}

func (a *uniqPriorityAdvisor) getNoThrottleMB() int {
	if a.ccdMaxMB > 0 {
		return a.ccdMaxMB
	}

	return a.defaultDomainCapacity
}

func (a *uniqPriorityAdvisor) adjust(_ context.Context,
	groupedSettings map[string][]int, observed map[string][]monitor.MBInfo, capPercent int,
) map[string][]int {
	result := map[string][]int{}
	activeGroups := sets.String{}
	for group, values := range groupedSettings {
		currents := getGroupOutgoingTotals(group, observed)
		if _, ok := a.adjusters[group]; !ok {
			a.adjusters[group] = adjuster.New(capPercent)
		}
		result[group] = a.adjusters[group].AdjustOutgoingTargets(values, currents)
		activeGroups.Insert(group)
	}

	// clean up to avoid memory leak
	if len(groupedSettings) > 0 {
		for group := range a.adjusters {
			if activeGroups.Has(group) {
				continue
			}
			delete(a.adjusters, group)
		}
	}

	return result
}

func (a *uniqPriorityAdvisor) getIncomingDomainQuotas(_ context.Context, domIncomingInfo map[int]*resource.MBGroupIncomingStat,
) resource.DomQuotas {
	domQuotas := map[int]resource.GroupSettings{}
	for dom, incomingInfo := range domIncomingInfo {
		domQuotas[dom] = a.quotaStrategy.GetGroupQuotas(incomingInfo)
	}
	return domQuotas
}

func (a *uniqPriorityAdvisor) calcIncomingDomainStats(ctx context.Context, mon *monitor.DomainStats,
) (map[int]*resource.MBGroupIncomingStat, error) {
	incomingInfoOfDomains := make(map[int]*resource.MBGroupIncomingStat)
	var err error
	for domID, incomingStats := range mon.Incomings {
		incomingInfoOfDomains[domID], err = a.calcIncomingStat(domID, incomingStats)
		if err != nil {
			return nil, errors.Wrap(err, "failed to calc domain quotas")
		}
	}
	return incomingInfoOfDomains, nil
}

func (a *uniqPriorityAdvisor) calcIncomingStat(domID int, incomingStats monitor.GroupMBStats) (*resource.MBGroupIncomingStat, error) {
	capacity, err := a.getEffectiveCapacity(domID, incomingStats)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calc domain capacity")
	}

	groupIncomingInfo := getGroupIncomingInfo(capacity, incomingStats)
	return groupIncomingInfo, nil
}

// getEffectiveCapacity gets the effective memory bandwidth capacity of specified domain, with its given resource usage
func (a *uniqPriorityAdvisor) getEffectiveCapacity(domID int, domIncomingStats monitor.GroupMBStats) (int, error) {
	if _, ok := a.domains[domID]; !ok {
		return 0, fmt.Errorf("unknown domain %d", domID)
	}

	return getMinEffectiveCapacity(a.defaultDomainCapacity, a.groupCapacityInMB, domIncomingStats), nil
}

func (a *uniqPriorityAdvisor) deriveOutgoingTargets(_ context.Context,
	outgoingGroupSumStat map[string][]monitor.MBInfo, incomingTargets map[string][]int,
) (map[string][]int, error) {
	// for each group: based on incoming targets, decide what the outgoing targets are
	result := make(map[string][]int)
	for group, domSums := range outgoingGroupSumStat {
		groupIncomingTargets, ok := incomingTargets[group]
		if !ok {
			general.Infof("[mbm] no need to change group %s resource", group)
			result[group] = nil
			continue
		}

		if a.xDomGroups.Has(group) {
			localRatio := make([]float64, len(domSums))
			for i, domSum := range domSums {
				// limit the excessive remote traffic if applicable
				remoteTarget := a.domains[i].GetAlienMBLimit()
				if remoteTarget > domSum.RemoteMB {
					remoteTarget = domSum.RemoteMB
				}
				localRatio[i] = float64(domSum.LocalMB) / float64(domSum.LocalMB+remoteTarget)
			}
			outgoingTargets, err := a.flower.InvertFlow(localRatio, groupIncomingTargets)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get sourcing outgoing out of desired incoming targets")
			}

			result[group] = outgoingTargets
		} else {
			result[group] = groupIncomingTargets
		}
	}

	return result, nil
}

func (a *uniqPriorityAdvisor) distributeToCCDs(_ context.Context,
	quotas map[string][]int, outgoingStat map[int]monitor.GroupMBStats,
) map[string]map[int]int {
	result := map[string]map[int]int{}
	for group, domQuota := range quotas {
		// each domain is treated independently
		for domID, domTotal := range domQuota {
			ccdDistributions := a.domainDistributeGroup(domID, group, domTotal, outgoingStat)
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

func (a *uniqPriorityAdvisor) domainDistributeGroup(domID int, group string,
	domTotal int, outgoingStat map[int]monitor.GroupMBStats,
) map[int]int {
	weights := map[int]int{}
	groupStat := outgoingStat[domID][group]
	for ccd, stat := range groupStat {
		weights[ccd] = stat.TotalMB
	}
	domCCDQuotas := a.ccdDistribute.Distribute(domTotal, weights)
	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [priorityAdvisor] domain %d, group %s, total %v, weights %v, distribute to ccd quotas: %v",
			domID, group, domTotal, weights, domCCDQuotas)
	}
	result := map[int]int{}
	for ccd, v := range domCCDQuotas {
		result[ccd] = v
	}
	return result
}

func newUniqPriorityAdvisor(emitter metrics.MetricEmitter, domains domain.Domains, ccdMinMB, ccdMaxMB int, defaultDomainCapacity int,
	capPercent int, XDomGroups []string, groupNeverThrottles []string,
	groupCapacity map[string]int,
) *uniqPriorityAdvisor {
	// do not throttle built-in "/" anytime
	notThrottles := sets.NewString("/")
	notThrottles.Insert(groupNeverThrottles...)

	return &uniqPriorityAdvisor{
		emitter:               emitter,
		domains:               domains,
		xDomGroups:            sets.NewString(XDomGroups...),
		groupNeverThrottles:   notThrottles,
		defaultDomainCapacity: defaultDomainCapacity,
		groupCapacityInMB:     groupCapacity,
		quotaStrategy:         quota.New(),
		flower:                sankey.New(),
		adjusters:             map[string]adjuster.Adjuster{},
		ccdDistribute:         distributor.New(ccdMinMB, ccdMaxMB),
		ccdMaxMB:              ccdMaxMB,
		capPercent:            capPercent,
	}
}
