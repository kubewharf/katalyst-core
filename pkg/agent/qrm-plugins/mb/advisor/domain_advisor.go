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
	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/quota"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/sankey"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type domainAdvisor struct {
	emitter metrics.MetricEmitter

	domains domain.Domains

	// XDomGroups are the qos control groups that allow memory access across domains
	XDomGroups sets.String
	// GroupCapacityInMB specifies domain capacity demanded by control groups other than the base capacity
	GroupCapacityInMB map[string]int

	quotaStrategy quota.Decider
	flower        sankey.DomainFlower
}

func (d *domainAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainsMon) (*plan.MBPlan, error) {
	// based on mb resource usage, decide incoming limits
	domainStats, err := d.calcIncomingDomainStats(ctx, domainsMon)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}
	d.emitDomIncomingStatMetrics(domainStats)

	// based on mb limits, decide incoming quotas (i.e. targets) - by setting aside some buffer
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

	// todo: based on the theoretic outgoing targets, decide the outgoing MB to set
	_ = groupedDomainOutgoingTargets

	return nil, nil
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

func distributeCapacityToGroups(capacity int, incomingStats monitor.GroupMonStat) *resource.MBGroupIncomingStat {
	result := &resource.MBGroupIncomingStat{
		CapacityInMB:   capacity,
		GroupTotalUses: map[string]int{},
		GroupLimits:    map[string]int{},
	}

	// from hi to lo to distribute limits, stripping usage out of the determined capacity
	balance := capacity
	groups := maps.Keys(incomingStats)
	sortedGroups := sortGroups(groups)
	for _, groups := range sortedGroups {
		// all equivalent groups shared the same available balance
		for group := range groups {
			result.GroupLimits[group] = balance
		}

		used := 0
		for group := range groups {
			groupUse := incomingStats[group].SumStat().TotalMB
			result.GroupTotalUses[group] = groupUse
			used += groupUse
		}

		if balance < used {
			balance = 0
			continue
		}

		balance -= used
	}

	result.ResourceState = resource.GetResourceState(capacity, balance)
	result.FreeInMB = balance
	result.GroupSorted = sortedGroups

	return result
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
func (d *domainAdvisor) getEffectiveCapacity(domID int, incomingStats monitor.GroupMonStat) (int, error) {
	domInfo, ok := d.domains[domID]
	if !ok {
		return 0, fmt.Errorf("invalid domain %d", domID)
	}

	baseCapacity := domInfo.CapacityInMB
	return getMinEffectiveCapacity(baseCapacity, d.GroupCapacityInMB, incomingStats), nil
}

func getMinEffectiveCapacity(base int, dynaCaps map[string]int, incomingStats monitor.GroupMonStat) int {
	min := base

	// identify the min dynamic capacity required by pre-defined groups, if the specific groups do have active MB traffics
	for group, groupStat := range incomingStats {
		if !groupStat.HasTraffic() {
			continue
		}

		if groupCapacity, ok := dynaCaps[group]; ok {
			if groupCapacity < min {
				min = groupCapacity
			}
		}
	}

	return min
}

func (d *domainAdvisor) deriveOutgoingTargets(domainsMon *monitor.DomainsMon, incomingTargets map[string][]int,
) (map[string][]int, error) {
	// for each group: based on incoming targets, decide what the outgoing targets are
	result := make(map[string][]int)
	for group, domSums := range domainsMon.GetGroupedDomainSummary() {
		incomingTargets, ok := incomingTargets[group]
		if !ok {
			return nil, fmt.Errorf("unable to get incoming targets of group %s", group)
		}

		if d.XDomGroups.Has(group) {
			localRatio := make([]float64, len(domSums))
			for i, domSum := range domSums {
				localRatio[i] = float64(domSum.LocalMB) / float64(domSum.TotalMB)
			}
			outgoingTargets, err := d.flower.InvertFlow(localRatio, incomingTargets)
			if err == nil {
				result[group] = outgoingTargets
				continue
			}
		}

		result[group] = incomingTargets
	}

	return result, nil
}

func New(XDomGroups []string, groupCapacity map[string]int) Advisor {
	return &domainAdvisor{
		XDomGroups:        sets.NewString(XDomGroups...),
		GroupCapacityInMB: groupCapacity,
		quotaStrategy:     nil,
	}
}
