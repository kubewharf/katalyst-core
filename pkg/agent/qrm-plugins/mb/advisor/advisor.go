package advisor

import (
	"context"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type EnhancedAdvisor struct {
	inner domainAdvisor
}

func (d *EnhancedAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	domainStats, groupInfos, err := d.combinedDomainStats(domainsMon)
	if err != nil {
		return nil, err
	}
	mbPlan, err := d.inner.GetPlan(ctx, domainStats)
	if err != nil {
		return nil, err
	}
	return d.splitPlan(mbPlan, groupInfos), nil
}

func (d *EnhancedAdvisor) combinedDomainStats(domainsMon *monitor.DomainStats) (*monitor.DomainStats, *monitor.GroupInfo, error) {
	domainStats := &monitor.DomainStats{
		Incomings:            make(map[int]monitor.DomainMonStat),
		Outgoings:            make(map[int]monitor.DomainMonStat),
		OutgoingGroupSumStat: make(map[string][]monitor.MBInfo),
	}
	groupInfos := &monitor.GroupInfo{
		DomainGroups: make(map[int]monitor.DomainGroupMapping),
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

func (d *EnhancedAdvisor) splitPlan(mbPlan *plan.MBPlan, groupInfos *monitor.GroupInfo) *plan.MBPlan {
	for groupKey, ccdPlan := range mbPlan.MBGroups {
		if !strings.Contains(groupKey, "combined-") {
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

func NewEnhancedAdvisor(emitter metrics.MetricEmitter, domains domain.Domains, ccdMinMB, ccdMaxMB int, defaultDomainCapacity int,
	capPercent int, XDomGroups []string, groupNeverThrottles []string,
	groupCapacity map[string]int,
) Advisor {
	return NewDomainAdvisor(emitter, domains,
		ccdMaxMB, ccdMaxMB,
		defaultDomainCapacity, capPercent,
		XDomGroups, groupNeverThrottles,
		groupCapacity)
}
