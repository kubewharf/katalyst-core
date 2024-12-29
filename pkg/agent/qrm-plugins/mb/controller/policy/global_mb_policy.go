package policy

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/quotasourcing"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const zombieMBDefault = 100 // 100 MB (0.1 GB)

var qosLeaves = sets.String{"shared-30": sets.Empty{}}

type globalMBPolicy struct {
	domainManager *mbdomain.MBDomainManager

	lock             sync.RWMutex
	domainLeafQuotas map[int]int
	//	leafQoSMBPolicy qospolicy.QoSMBPolicy

	sourcer         quotasourcing.Sourcer
	throttler       strategy.LowPrioPlanner
	easer           strategy.LowPrioPlanner
	ccdGroupPlanner *strategy.CCDGroupPlanner
	zombieMB        int
}

func (g *globalMBPolicy) getLeafQuotas(domain int) (int, error) {
	g.lock.RLock()
	defer g.lock.RUnlock()
	if leafQuota, ok := g.domainLeafQuotas[domain]; ok {
		return leafQuota, nil
	}

	return -1, fmt.Errorf("unknown domain %d", domain)
}

func (g *globalMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	// this relies on the beforehand ProcessGlobalQoSCCDMB(...), which had processed taking into account all the domains
	leafQuota, err := g.getLeafQuotas(domain.ID)
	if err != nil {
		panic(fmt.Sprintf("missing well prepared plan: %v", err))
	}

	// no high qos in any domains; trivial - no constraint on all CCDs
	allLeaves := leafQuota == -1
	if allLeaves {
		return g.ccdGroupPlanner.GetFixedPlan(35_000, currQoSMB)
	}

	// split into higher qos groups, and lowest leaf group ("shared-30")
	hiQoSGroups := make(map[qosgroup.QoSGroup]*stat.MBQoSGroup)
	for qos, mbQoSGroup := range currQoSMB {
		if qos == "shared-30" {
			continue
		}
		hiQoSGroups[qos] = mbQoSGroup
	}
	leafQoSGroup := map[qosgroup.QoSGroup]*stat.MBQoSGroup{
		"shared-30": currQoSMB["shared-30"],
	}

	// to generate mb plan for higher priority groups (usually at least system)
	hiPlans := g.ccdGroupPlanner.GetFixedPlan(35_000, hiQoSGroups)

	// to generate mb plan for leaf (lowest priority) group
	// distribute total among all proportionally
	leafUsage := stat.SumMB(leafQoSGroup)
	ratio := float64(leafQuota) / float64(leafUsage)
	leafPlan := g.ccdGroupPlanner.GetProportionalPlan(ratio, leafQoSGroup)

	return plan.Merge(hiPlans, leafPlan)
}

func (g *globalMBPolicy) ProcessGlobalQoSCCDMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
	// reserve for socket pods in admission or incubation
	mbQoSGroups = g.adjustSocketCCDMB(mbQoSGroups)

	// no high priority traffic, no constraint on leaves
	if !g.hasHighQoSMB(mbQoSGroups) {
		general.InfofV(6, "mbm: policy: no significant high qos traffic; no constraint on low priority")
		g.setLeafNoLimit()
		return
	}

	// get the domain summary MB
	highQoSMBGroups, lowQoSMBGroups := g.split(mbQoSGroups)
	highMBStat := g.domainManager.SumQoSMBByDomainSender(highQoSMBGroups)
	leafMBStat := g.domainManager.SumQoSMBByDomainRecipient(lowQoSMBGroups)

	// calculate the leaf mb targets of all domains
	leafPolicyArgs := make([]quotasourcing.DomainMB, len(g.domainManager.Domains))
	for domain := range g.domainManager.Domains {
		highMBTotal, highMBTotalLocal := 0, 0
		for _, mbStat := range highMBStat[domain] {
			highMBTotal += mbStat.Total
			highMBTotalLocal += mbStat.Local
		}
		leafMBTotal, leafMBTotalLocal := 0, 0
		for _, mbStat := range leafMBStat[domain] {
			leafMBTotal += mbStat.Total
			leafMBTotalLocal += mbStat.Local
		}

		leafTarget := g.calcDomainLeafTarget(highMBTotal, leafMBTotal)
		general.InfofV(6, "mbm: policy: summary: domain %d: (high-qos usage: %d, shared-30 usage: %d, local %d, target %d)", domain, highMBTotal, leafMBTotal, leafMBTotalLocal, leafTarget)
		leafPolicyArgs[domain] = quotasourcing.DomainMB{
			Target:         leafTarget,
			MBSource:       leafMBTotal,
			MBSourceRemote: leafMBTotal - leafMBTotalLocal,
		}
	}

	// figure out the leaf quotas by taking into account of cross-domain impacts
	leafQuotas := g.sourcer.AttributeMBToSources(leafPolicyArgs)
	general.InfofV(6, "mbm: policy: domain quotas: %v", leafQuotas)
	g.setLeafQuotas(leafQuotas)
}

func (g *globalMBPolicy) adjustSocketCCDMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[qosgroup.QoSGroup]*stat.MBQoSGroup {
	for qos, qosGroup := range mbQoSGroups {
		if qos != "dedicated" {
			continue
		}
		mbQoSGroups[qos] = g.adjustWthAdmissionIncubation(qosGroup)
	}

	return mbQoSGroups
}

func (g *globalMBPolicy) split(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) (highQoSs, leaves map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
	highQoSs = make(map[qosgroup.QoSGroup]*stat.MBQoSGroup)
	leaves = make(map[qosgroup.QoSGroup]*stat.MBQoSGroup)
	for qos, ccdmb := range mbQoSGroups {
		if qosLeaves.Has(string(qos)) {
			leaves[qos] = ccdmb
		} else {
			highQoSs[qos] = ccdmb
		}
	}
	return highQoSs, leaves
}

func (g *globalMBPolicy) sumLeafDomainMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[int]int {
	ccdMBs := make(map[int]int)
	for qos, ccdmb := range mbQoSGroups {
		if qosLeaves.Has(string(qos)) {
			for ccd, mb := range ccdmb.CCDMB {
				ccdMBs[ccd] += mb.TotalMB
			}
		}
	}
	return g.sumupToDomain(ccdMBs)
}

func (g *globalMBPolicy) sumLeafDomainMBLocal(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[int]int {
	ccdMBs := make(map[int]int)
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qosLeaves.Has(string(qos)) {
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

func (g *globalMBPolicy) sumHighQoSMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[int]int {
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

func (g *globalMBPolicy) sumMBTotal(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) int {
	total := 0
	for _, ccdmb := range mbQoSGroups {
		for _, mb := range ccdmb.CCDMB {
			total += mb.TotalMB
		}
	}
	return total
}

func (g *globalMBPolicy) getLeafMBTargets(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) []quotasourcing.DomainMB {
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

func (g *globalMBPolicy) adjustWthAdmissionIncubation(group *stat.MBQoSGroup) *stat.MBQoSGroup {
	incubCCDs := make(sets.Int)
	for _, domain := range g.domainManager.Domains {
		domain.CleanseIncubates()
		for ccd, _ := range domain.CloneIncubates() {
			incubCCDs.Insert(ccd)
		}
	}

	for incubCCD, _ := range incubCCDs {
		group.CCDs.Insert(incubCCD)
		if _, ok := group.CCDMB[incubCCD]; !ok {
			group.CCDMB[incubCCD] = &stat.MBData{}
		}
		if group.CCDMB[incubCCD].TotalMB < config.ReservedPerCCD {
			group.CCDMB[incubCCD].TotalMB = config.ReservedPerCCD
		}
	}

	return group
}

func (g *globalMBPolicy) hasHighQoSMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) bool {
	// there may exist random mb traffic in small amount, which is zombie
	for qos, ccdmb := range mbQoSGroups {
		// system qos is always there; not to count it for this purpose
		if qos == qosgroup.QoSGroupDedicated || qos == "shared-50" {
			for _, mb := range ccdmb.CCDMB {
				if mb.TotalMB > g.zombieMB {
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
		sourcer:          quotasourcing.NewCategorySourcer(),
		throttler:        strategy.New(throttleType, ccdPlanner),
		easer:            strategy.New(easeType, ccdPlanner),
		domainManager:    domainManager,
		domainLeafQuotas: domainLeafQuotas,
		ccdGroupPlanner:  ccdPlanner,
		zombieMB:         zombieMBDefault,
	}, nil
}
