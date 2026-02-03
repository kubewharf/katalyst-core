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
	domainStats := d.combinedDomainStats(domainsMon)
	mbPlan, err := d.inner.GetPlan(ctx, domainStats)
	if err != nil {
		return nil, err
	}
	splitPlan := d.splitPlan(mbPlan, domainsMon)
	return splitPlan, nil
}

func (d *EnhancedAdvisor) combinedDomainStats(domainsMon *monitor.DomainStats) *monitor.DomainStats {
	domainStats := &monitor.DomainStats{
		Incomings:            make(map[int]monitor.DomainMonStat),
		Outgoings:            make(map[int]monitor.DomainMonStat),
		OutgoingGroupSumStat: make(map[string][]monitor.MBInfo),
	}
	for id, domainMon := range domainsMon.Incomings {
		domainStats.Incomings[id] = preProcessGroupInfo(domainMon)
	}
	for id, domainMon := range domainsMon.Outgoings {
		domainStats.Outgoings[id] = preProcessGroupInfo(domainMon)
	}
	domainStats.OutgoingGroupSumStat = preProcessGroupSumStat(domainsMon.OutgoingGroupSumStat)
	return domainStats
}

func (d *EnhancedAdvisor) splitPlan(mbPlan *plan.MBPlan, domainsMon *monitor.DomainStats) *plan.MBPlan {
	for groupKey, ccdPlan := range mbPlan.MBGroups {
		if !strings.Contains(groupKey, "combined-") {
			continue
		}
		for ccd, quota := range ccdPlan {
			for _, stats := range domainsMon.Incomings {
				for group, stat := range stats {
					for id := range stat {
						if id == ccd {
							if mbPlan.MBGroups[group] == nil {
								mbPlan.MBGroups[group] = make(plan.GroupCCDPlan)
							}
							mbPlan.MBGroups[group][ccd] = quota
							break
						}
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
