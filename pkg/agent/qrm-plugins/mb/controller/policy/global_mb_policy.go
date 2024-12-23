package policy

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/quotasourcing"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type globalMBPolicy struct {
	domainManager *mbdomain.MBDomainManager

	lock             sync.Mutex
	domainLeafQuotas map[int]int
	//	leafQoSMBPolicy qospolicy.QoSMBPolicy

	sourcer         quotasourcing.Sourcer
	throttler       strategy.LowPrioPlanner
	easer           strategy.LowPrioPlanner
	ccdGroupPlanner *strategy.CCDGroupPlanner
}

func (g *globalMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	// this relies on the beforehand ProcessGlobalQoSCCDMB(...), which had processed taking into account all the domains
	leafQuota, ok := g.domainLeafQuotas[domain.ID]
	if !ok {
		panic(fmt.Sprintf("missing well prepared plan for domain %d", domain.ID))
	}

	// no high qos in any domains; trivial - no constraint on all CCDs
	allLeaves := leafQuota == -1
	if allLeaves {
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
	// todo: reserve for socket pods in admission

	// no high priority traffic, no constraint on leaves
	if !hasHighQoMB(mbQoSGroups) {
		g.setLeafNoLimit()
		return
	}

	// calculate the leaf mb targets of all domains
	// figure out the leaf quotas by taking into account of cross-domain impacts
	leafCCDMBs := g.getLeafMBTargets(mbQoSGroups)
	leafQuotas := g.sourcer.AttributeMBToSources(leafCCDMBs)
	g.setLeafQuotas(leafQuotas)
}

func (g *globalMBPolicy) sumLeafDomainMB(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) map[int]int {
	ccdMBs := make(map[int]int)
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qos == "shared-30" {
			for ccd, mb := range ccdmb.CCDMB {
				ccdMBs[ccd] += mb.TotalMB
			}
		}
	}
	return g.sumupToDomain(ccdMBs)
}

func (g *globalMBPolicy) sumLeafDomainMBLocal(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) map[int]int {
	ccdMBs := make(map[int]int)
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qos == "shared-30" {
			for ccd, mb := range ccdmb.CCDMB {
				ccdMBs[ccd] += mb.LocalTotalMB
			}
		}
	}
	return g.sumupToDomain(ccdMBs)
}

func (g *globalMBPolicy) sumupToDomain(ccdValues map[int]int) map[int]int {
	domainValues := make(map[int]int)
	for ccd, value := range ccdValues {
		domain, err := g.domainManager.IdentifyDomainByCCD(ccd)
		if err != nil {
			panic("unexpected ccd - not in any domain")
		}
		domainValues[domain] += value
	}
	return domainValues
}

func (g *globalMBPolicy) sumHighQoSMB(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) map[int]int {
	ccdMBs := make(map[int]int)
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qos == "shared-30" {
			continue
		}
		for ccd, mb := range ccdmb.CCDMB {
			ccdMBs[ccd] += mb.TotalMB
		}
	}
	return g.sumupToDomain(ccdMBs)
}

func (g *globalMBPolicy) getLeafMBTargets(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) []quotasourcing.DomainMB {
	highQoSDomainMBs := g.sumHighQoSMB(mbQoSGroups)
	leafDomainMBs := g.sumLeafDomainMB(mbQoSGroups)
	leafLocalDomainMBs := g.sumLeafDomainMBLocal(mbQoSGroups)

	desiredDomainLeafTargets := g.calcDomainLeafTargets(highQoSDomainMBs, leafDomainMBs)

	result := make([]quotasourcing.DomainMB, len(g.domainManager.Domains))
	for i := range result {
		result[i] = quotasourcing.DomainMB{
			Target:         desiredDomainLeafTargets[i],
			MBSource:       leafDomainMBs[i],
			MBSourceRemote: leafDomainMBs[i] - leafLocalDomainMBs[i],
		}
	}
	return result
}

func (g *globalMBPolicy) calcDomainLeafTargets(hiQoSDomainMBs, leafQoSDomainMBs map[int]int) map[int]int {
	result := make(map[int]int)
	for domain := range g.domainManager.Domains {
		result[domain] = g.calcDomainLeafTarget(hiQoSDomainMBs[domain], leafQoSDomainMBs[domain])
	}
	return result
}

func (g *globalMBPolicy) calcDomainLeafTarget(hiQoSMB, leafMB int) int {
	// if not under pressure (too much available mb), get to-ease-to
	// if under pressure, get to-throttle-to
	// in middle way, noop
	totalUsage := hiQoSMB + leafMB
	capacityForLeaf := 122_000 - hiQoSMB

	if strategy.IsResourceUnderPressure(122_000, totalUsage) {
		return g.throttler.GetQuota(capacityForLeaf, leafMB)
	}
	if strategy.IsResourceAtEase(122_000, totalUsage) {
		return g.easer.GetQuota(capacityForLeaf, leafMB)
	}

	// neither under pressure nor at ease, everything seems fine
	return leafMB
}

func (g *globalMBPolicy) setLeafQuotas(leafQuotas []int) {
	g.lock.Lock()
	defer g.lock.Unlock()
	for domain, leafQuota := range leafQuotas {
		g.domainLeafQuotas[domain] = leafQuota
	}
}

func (g *globalMBPolicy) setLeafNoLimit() {
	g.lock.Lock()
	defer g.lock.Unlock()
	for domain := range g.domainLeafQuotas {
		g.domainLeafQuotas[domain] = -1
	}
}

func hasHighQoMB(mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) bool {
	// there may exist random mb traffic in small amount, which is zombie
	const zombieMB = 100 // 100 MB (0.1 GB)
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qos == qosgroup.QoSGroupDedicated || qos == "shared-50" {
			for _, mb := range ccdmb.CCDMB {
				if mb.TotalMB > zombieMB {
					return true
				}
			}
		}
	}
	return false
}

func NewGlobalMBPolicy(ccdMBMin int, domainManager *mbdomain.MBDomainManager, throttleType, easeType strategy.LowPrioPlannerType,
) (DomainMBPolicy, error) {
	domainLeafQuotas := make(map[int]int)
	for domain := range domainManager.Domains {
		domainLeafQuotas[domain] = -1
	}

	ccdPlanner := strategy.NewCCDGroupPlanner(ccdMBMin, 35_000)

	return &globalMBPolicy{
		sourcer:          &quotasourcing.CrossSourcer{},
		throttler:        strategy.New(throttleType, ccdPlanner),
		easer:            strategy.New(easeType, ccdPlanner),
		domainManager:    domainManager,
		domainLeafQuotas: domainLeafQuotas,
		ccdGroupPlanner:  ccdPlanner,
	}, nil
}
