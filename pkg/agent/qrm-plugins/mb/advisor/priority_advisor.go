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

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// priorityAdvisor is able to work with resctrl groups with priority;
// one groups' priority can be the same as that of others'.
// It leverages uniqPriorityAdvisor working with groups of distinct priorities only.
// It targets the scenarios where the groups of same priority don't share ccd.
// todo: enhance to handle multiple groups of same priority sharing ccd
type priorityAdvisor struct {
	uniqPriorityAdvisor *uniqPriorityAdvisor
}

// groupInfo stores the mapping of groups and their CCDs for each domain
type groupInfo struct { // DomainGroups maps domain ID -> combined group key -> original group key -> CCD IDs
	DomainGroups map[int]domainGroupMapping
}

// domainGroupMapping maps combined group keys to their original groups
type domainGroupMapping map[string]combinedGroupMapping

// combinedGroupMapping maps original group keys to their CCD IDs
type combinedGroupMapping map[string]ccdSet

// ccdSet represents a set of CCD IDs
type ccdSet = sets.Int

func (a *priorityAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	domainStats, groupInfos, err := a.combinedDomainStats(domainsMon)
	if err != nil {
		return nil, err
	}
	mbPlan, err := a.uniqPriorityAdvisor.GetPlan(ctx, domainStats)
	if err != nil {
		return nil, err
	}
	return a.splitPlan(mbPlan, groupInfos), nil
}

func (a *priorityAdvisor) combinedDomainStats(domainsMon *monitor.DomainStats) (*monitor.DomainStats, *groupInfo, error) {
	domainStats := &monitor.DomainStats{
		Incomings:            make(map[int]monitor.DomainMonStat),
		Outgoings:            make(map[int]monitor.DomainMonStat),
		OutgoingGroupSumStat: make(map[string][]monitor.MBInfo),
	}
	groupInfos := &groupInfo{
		DomainGroups: make(map[int]domainGroupMapping),
	}
	var err error
	for id, domainMon := range domainsMon.Incomings {
		domainStats.Incomings[id], groupInfos.DomainGroups[id], err = preProcessGroupInfo(domainMon)
		if err != nil {
			return nil, nil, err
		}
	}
	for id, domainMon := range domainsMon.Outgoings {
		domainStats.Outgoings[id], _, err = preProcessGroupInfo(domainMon)
		if err != nil {
			return nil, nil, err
		}
	}
	domainStats.OutgoingGroupSumStat = preProcessGroupSumStat(domainsMon.OutgoingGroupSumStat)
	return domainStats, groupInfos, nil
}

func (a *priorityAdvisor) splitPlan(mbPlan *plan.MBPlan, groupInfos *groupInfo) *plan.MBPlan {
	for groupKey, ccdPlan := range mbPlan.MBGroups {
		if !isCombinedGroup(groupKey) {
			continue
		}
		for _, domainGroupInfos := range groupInfos.DomainGroups {
			for group, groupInfo := range domainGroupInfos[groupKey] {
				for ccd := range groupInfo {
					if _, exists := ccdPlan[ccd]; exists {
						if mbPlan.MBGroups[group] == nil {
							mbPlan.MBGroups[group] = make(plan.GroupCCDPlan)
						}
						mbPlan.MBGroups[group][ccd] = ccdPlan[ccd]
					}
				}
			}
		}
		delete(mbPlan.MBGroups, groupKey)
	}
	return mbPlan
}

func New(emitter metrics.MetricEmitter, domains domain.Domains, ccdMinMB, ccdMaxMB int, defaultDomainCapacity int,
	capPercent int, XDomGroups []string, groupNeverThrottles []string,
	groupCapacity map[string]int,
) Advisor {
	return &priorityAdvisor{
		uniqPriorityAdvisor: newUniqPriorityAdvisor(emitter,
			domains, ccdMinMB, ccdMaxMB, defaultDomainCapacity, capPercent,
			XDomGroups, groupNeverThrottles,
			groupCapacity,
		),
	}
}
