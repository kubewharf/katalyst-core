package advisor

import (
	"context"
	"errors"
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
	return d.splitPlan(mbPlan, domainsMon)
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

func (d *EnhancedAdvisor) splitPlan(mbPlan *plan.MBPlan, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	for groupKey, ccdPlan := range mbPlan.MBGroups {
		if !strings.Contains(groupKey, "combined-") {
			continue
		}
		maxMap := make(map[int]int)
		for ccd, quota := range ccdPlan {
			for _, stats := range domainsMon.Incomings {
				for group, stat := range stats {
					for id := range stat {
						if id == ccd {
							if mbPlan.MBGroups[group] == nil {
								mbPlan.MBGroups[group] = make(plan.GroupCCDPlan)
							}
							mbPlan.MBGroups[group][ccd] = quota
						}
						if stat[id].TotalMB > maxMap[id] {
							maxMap[id] = stat[id].TotalMB
						}
					}
				}
			}
		}
		delete(mbPlan.MBGroups, groupKey)
		for _, stats := range domainsMon.Incomings {
			for group, stat := range stats {
				for id, mbStat := range stat {
					if mbStat.TotalMB > maxMap[id]/2 && mbStat.TotalMB < maxMap[id] {
						return nil, errors.New("invalid incoming inputs")
					}
					if mbStat.TotalMB <= maxMap[id]/2 {
						delete(mbPlan.MBGroups[group], id)
					}
				}
			}
		}
	}
	return mbPlan, nil
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
