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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

type domainAdvisor struct {
	domains domain.Domains

	// XDomGroups are the qos control groups that allow memory access across domains
	XDomGroups sets.String
	// GroupCapacityInMB specifies domain capacity demanded by control groups other than the base capacity
	GroupCapacityInMB map[string]int

	quotaStrategy quota.Quota
}

func (d *domainAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainsMon) (*plan.MBPlan, error) {
	domLimits, err := d.calcIncomingDomainLimits(ctx, domainsMon)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get plan")
	}

	// based on mb limits and resource state, decide incoming quotas
	domQuotas := d.getIncomingDomainQuotas(ctx, domLimits)
	_ = domQuotas

	// based on incoming quotas, decide what the outgoing settings are

	panic("implement me")
}

func (d *domainAdvisor) getIncomingDomainQuotas(ctx context.Context, domLimits map[int]*resource.MBGroupLimits) map[int]resource.GroupSettings {
	domQuotas := map[int]resource.GroupSettings{}
	for dom, limits := range domLimits {
		domQuotas[dom] = d.quotaStrategy.GetGroupQuotas(limits)
	}
	return domQuotas
}

func (d *domainAdvisor) calcIncomingDomainLimits(ctx context.Context, mon *monitor.DomainsMon) (map[int]*resource.MBGroupLimits, error) {
	domainQuotas := make(map[int]*resource.MBGroupLimits)
	var err error
	for domID, incomingStats := range mon.Incoming {
		domainQuotas[domID], err = d.calcIncomingGroupLimits(domID, incomingStats)
		if err != nil {
			return nil, errors.Wrap(err, "failed to calc domain quotas")
		}
	}
	return domainQuotas, nil
}

func distributeCapacityToGroups(capacity int, incomingStats monitor.GroupMonStat) *resource.MBGroupLimits {
	result := &resource.MBGroupLimits{
		CapacityInMB: capacity,
		GroupLimits:  map[string]int{},
	}

	// from hi to lo to distribute limits, stripping usage out of the determined capacity
	balance := capacity
	groups := maps.Keys(incomingStats)
	sortedGroups := getSortedGroups(groups)
	for _, groups := range sortedGroups {
		used := 0
		for group := range groups {
			used += incomingStats[group].Sum()
		}

		// all equivalent groups shared the same quota
		for group := range groups {
			result.GroupLimits[group] = balance
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

func (d *domainAdvisor) calcIncomingGroupLimits(domID int, incomingStats monitor.GroupMonStat) (*resource.MBGroupLimits, error) {
	capacity, err := d.getCapacity(domID, incomingStats)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calc domain capacity")
	}

	groupCapacities := distributeCapacityToGroups(capacity, incomingStats)
	return groupCapacities, nil
}

func (d *domainAdvisor) getCapacity(domID int, incomingStats monitor.GroupMonStat) (int, error) {
	domInfo, ok := d.domains[domID]
	if !ok {
		return 0, fmt.Errorf("invalid domain %d", domID)
	}

	// check for dynamic mb capacity setting is applicable
	baseCapacity := domInfo.CapacityInMB
	return d.getEffectiveCapacity(baseCapacity, incomingStats), nil
}

func (d *domainAdvisor) getEffectiveCapacity(base int, incomingStats monitor.GroupMonStat) int {
	min := base
	// check the min dynamic capacity required by pre-defined groups, if the specific groups do have active MB traffics
	for group, groupStat := range incomingStats {
		if !groupStat.HasTraffic() {
			continue
		}
		if groupCapacity, ok := d.GroupCapacityInMB[group]; ok {
			if groupCapacity < min {
				min = groupCapacity
			}
		}
	}
	return min
}

func New(XDomGroups []string, groupCapacity map[string]int) Advisor {
	return &domainAdvisor{
		XDomGroups:        sets.NewString(XDomGroups...),
		GroupCapacityInMB: groupCapacity,
		quotaStrategy:     nil,
	}
}
