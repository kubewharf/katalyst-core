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
	domainStats, subGroups := d.combinedDomainStats(domainsMon)
	mbPlan, err := d.inner.GetPlan(ctx, domainStats)
	if err != nil {
		return nil, err
	}
	splitPlan := d.splitPlan(mbPlan, subGroups, domainsMon)
	return splitPlan, nil
}

func (d *EnhancedAdvisor) combinedDomainStats(domainsMon *monitor.DomainStats) (*monitor.DomainStats, map[string][]string) {
	domainStats := &monitor.DomainStats{
		Incomings:            make(map[int]monitor.DomainMonStat),
		Outgoings:            make(map[int]monitor.DomainMonStat),
		OutgoingGroupSumStat: make(map[string][]monitor.MBInfo),
	}
	subGroups := make(map[string][]string)
	for id, domainMon := range domainsMon.Incomings {
		domainStats.Incomings[id], subGroups = preProcessGroupInfo(domainMon)
	}
	for id, domainMon := range domainsMon.Outgoings {
		domainStats.Outgoings[id], subGroups = preProcessGroupInfo(domainMon)
	}
	domainStats.OutgoingGroupSumStat = preProcessGroupSumStat(domainsMon.OutgoingGroupSumStat)
	return domainStats, subGroups
}

func (d *EnhancedAdvisor) splitPlan(mbPlan *plan.MBPlan, subGroups map[string][]string, domainsMon *monitor.DomainStats) *plan.MBPlan {
	for groupKey, ccdPlan := range mbPlan.MBGroups {
		if !strings.Contains(groupKey, "combined-") {
			continue
		}
		for _, stats := range domainsMon.Incomings {
			subGroupMap := make(map[string][]int)
			sumMB := 0
			for group, stat := range stats {
				for _, subGroup := range subGroups[groupKey] {
					maxMB := 0
					for id, mbInfo := range stat {
						sumMB += mbInfo.TotalMB
						if group == subGroup {
							if mbInfo.TotalMB > maxMB {
								subGroupMap[group][0] = id
							}
							subGroupMap[group][1] += mbInfo.TotalMB
						}
					}
				}
			}
			for _, subGroup := range subGroups[groupKey] {
				ccd := subGroupMap[subGroup][0]
				mb := subGroupMap[subGroup][1]
				mbPlan.MBGroups[subGroup][ccd] = mb / sumMB * ccdPlan[0]
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
