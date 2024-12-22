package policy

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type GlobalMBPlanner interface {
	ProcessGlobalQoSCCDMB(qos map[qosgroup.QoSGroup]*monitor.MBQoSGroup)
}

type globalMBPolicy struct {
	domainManager    mbdomain.MBDomainManager
	domainLeafQuotas map[int]int
	//	leafQoSMBPolicy qospolicy.QoSMBPolicy
	ccdGroupPlanner *strategy.CCDGroupPlanner
}

func (g *globalMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	// this relies on the before -hand ProcessGlobalQoSCCDMB(...), which had processed taking into account all the domains
	leafQuota, ok := g.domainLeafQuotas[domain.ID]
	if !ok {
		panic(fmt.Sprintf("missing well prepared plan for domain %d", domain.ID))
	}

	// no high qos in any domains; trivial - no constraint on all CCDs
	allLeavs := leafQuota == -1
	if allLeavs {
		return g.ccdGroupPlanner.GetFixedPlan(35_000, currQoSMB)
	}

	// split into higher qos groups, and lowest leaf group ("shared-30")
	hiQoSGroups := make(map[qosgroup.QoSGroup]*monitor.MBQoSGroup)
	for qos, mbQoSGroup := range currQoSMB {
		if qos == "shared-30" {
			continue
		}
		hiQoSGroups[qos] = mbQoSGroup
	}
	leafQoSGroup := map[qosgroup.QoSGroup]*monitor.MBQoSGroup{
		"shared-30": currQoSMB["shared-30"],
	}

	// to generate mb plan for higher priority groups (usually at least system)
	hiPlans := g.ccdGroupPlanner.GetFixedPlan(35_000, hiQoSGroups)

	// to generate mb plan for leaf (lowest priority) group
	// distribute total among all proportionally
	leafUsage := monitor.SumMB(leafQoSGroup)
	ratio := float64(leafQuota) / float64(leafUsage)
	leafPlan := g.ccdGroupPlanner.GetProportionalPlan(ratio, leafQoSGroup)

	return plan.Merge(hiPlans, leafPlan)
}

func (g *globalMBPolicy) ProcessGlobalQoSCCDMB(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) {
	panic("impl")
}

func NewGlobalMBPolicy() DomainMBPolicy {
	return &globalMBPolicy{}
}
