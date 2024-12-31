package crossdomain

import (
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain/quotasourcing"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const zombieMBDefault = 100 // 100 MB (0.1 GB)

// the leaf layer of QoS that could be throttled with their mb resource quota to give room for high levels of QoS
var qosLeaves = sets.String{"shared-30": sets.Empty{}}

type globalMBPolicy struct {
	domainManager *mbdomain.MBDomainManager

	lock sync.RWMutex
	// quotas of throttle-able leaf QoS layer via sender(outgoing) perspective
	domainLeafOutgoingQuotas map[int]int

	// sourcer is the component that attributes leaf sender quotas
	// based on domain desired recipient targets, and domain sending total usage and local/remote ratio
	sourcer quotasourcing.Sourcer

	// throttler and easer are policy decision makers that decide how much to throttle/ease the recipient traffic (desired recipient target)
	throttler strategy.LowPrioPlanner
	easer     strategy.LowPrioPlanner

	// ccdGroupPlanner distributes ccd mb shares of domain quota based on weighted usage
	ccdGroupPlanner *strategy.CCDGroupPlanner

	// traffic negligible for high QoS traffic
	zombieMB int
}

func (g *globalMBPolicy) GetPlan(totalMB int, domain *mbdomain.MBDomain, currQoSMB map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	// this relies on the beforehand ProcessGlobalQoSCCDMB(...), which had processed taking into account all the domains
	leafOutgoingQuota, err := g.getLeafOutgoingQuotas(domain.ID)
	if err != nil {
		panic(fmt.Sprintf("missing well prepared plan: %v", err))
	}

	// no high qos in any domains; trivial - no constraint on all CCDs
	allLeaves := leafOutgoingQuota == -1
	if allLeaves {
		return g.ccdGroupPlanner.GetFixedPlan(35_000, currQoSMB)
	}

	// splitQoSHighAndLeaf into higher qos groups, and lowest leaf group ("shared-30")
	hiQoSGroups, leafQoSGroup := g.splitQoSHighAndLeaf(currQoSMB)

	// to generate mb plan for higher priority groups (usually at least system)
	hiPlans := g.ccdGroupPlanner.GetFixedPlan(35_000, hiQoSGroups)

	// to generate mb plan for leaf (lowest priority) group
	leafOutgoingAmount := stat.SumMB(leafQoSGroup)
	ratio := float64(leafOutgoingQuota) / float64(leafOutgoingAmount)
	leafPlan := g.ccdGroupPlanner.GetProportionalPlan(ratio, leafQoSGroup)

	return plan.Merge(hiPlans, leafPlan)
}

func (g *globalMBPolicy) ProcessGlobalQoSCCDMB(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
	// reserve for socket pods in admission or incubation
	mbQoSGroups = g.adjustSocketCCDMBWithIncubates(mbQoSGroups)

	// no high priority traffic, no constraint on leaves
	if !g.hasHighQoSMB(mbQoSGroups) {
		general.InfofV(6, "mbm: policy: no significant high qos traffic; no constraint on low priority")
		g.setLeafOutgoingNoLimit()
		return
	}

	highQoSMBGroups, lowQoSMBGroups := g.splitQoSHighAndLeaf(mbQoSGroups)

	// assuming high QoS outgoing is equivalent to incoming
	// todo: use incoming direction ms stat instead
	highOutgoingMBStat := g.domainManager.SumQoSMBByDomainSender(highQoSMBGroups)

	// critical for leaf QoS to have outgoing vs incoming mb stats
	leafOutgoingMBStat := g.domainManager.SumQoSMBByDomainSender(lowQoSMBGroups)
	leafIncomingMBStat := g.domainManager.SumQoSMBByDomainRecipient(lowQoSMBGroups)

	// args for calculation of the leaf outgoing mb quota of all domains
	leafPolicySourceInfo := make([]quotasourcing.DomainMB, len(g.domainManager.Domains))
	for domainID := range g.domainManager.Domains {
		leafRecipientTarget := g.proposeDomainRecipientTarget(domainID, highOutgoingMBStat[domainID], leafIncomingMBStat[domainID])
		leafPolicySourceInfo[domainID] = assemblePolicySourceInfo(leafRecipientTarget, leafOutgoingMBStat[domainID])
	}

	general.InfofV(6, "mbm: policy: source args: %d records: %s", len(leafPolicySourceInfo), stringifyPolicySourceInfo(leafPolicySourceInfo))

	// figure out the leaf sender quotas that satisfies the desired recipient targets by taking into account of cross-domain impact
	leafQuotas := g.sourcer.AttributeMBToSources(leafPolicySourceInfo)
	general.InfofV(6, "mbm: policy: domain outgoing quotas: %v", leafQuotas)
	g.setLeafOutgoingQuotas(leafQuotas)
}

func (g *globalMBPolicy) proposeDomainRecipientTarget(domainID int, highIncomingQoSMBStat, leafIncomingMBStat map[qosgroup.QoSGroup]rmbtype.MBStat) int {
	highMBTotal, _, _ := getTotalLocalRemoteMBStatSummary(highIncomingQoSMBStat)
	leafIncomingMBTotal, leafIncomingMBLocal, leafIncomingMBRemote := getTotalLocalRemoteMBStatSummary(leafIncomingMBStat)
	// figure out the target MB based on recipient values
	leafRecipientTarget := g.calcDomainLeafTarget(highMBTotal, leafIncomingMBTotal)
	general.InfofV(6, "mbm: policy: summary: domain %d: stat - (high-qos usage: %d, (recv)shared-30 usage: %d, from local %d, from remote %d; desired recipient target %d)",
		domainID, highMBTotal, leafIncomingMBTotal, leafIncomingMBLocal, leafIncomingMBRemote, leafRecipientTarget)
	return leafRecipientTarget
}

func assemblePolicySourceInfo(recipientTarget int, outgoingMBStat map[qosgroup.QoSGroup]rmbtype.MBStat) quotasourcing.DomainMB {
	leafOutgoingMBTotal, _, leafOutgoingMBRemote := getTotalLocalRemoteMBStatSummary(outgoingMBStat)
	return quotasourcing.DomainMB{
		Target:         recipientTarget,
		MBSource:       leafOutgoingMBTotal,
		MBSourceRemote: leafOutgoingMBRemote,
	}
}

func getTotalLocalRemoteMBStatSummary(qosMBStat map[qosgroup.QoSGroup]rmbtype.MBStat) (total, local, remote int) {
	for _, mbStat := range qosMBStat {
		total += mbStat.Total
		local += mbStat.Local
	}
	remote = total - local
	return total, local, remote
}

func stringifyPolicySourceInfo(domainSources []quotasourcing.DomainMB) string {
	var sb strings.Builder
	for id, domainMB := range domainSources {
		sb.WriteString(fmt.Sprintf("domain: %d ", id))
		sb.WriteString(fmt.Sprintf("target: %d, sending total: %d, sending to remote: %d", domainMB.Target, domainMB.MBSource, domainMB.MBSourceRemote))
		sb.WriteString("\n")
	}
	return sb.String()
}

func (g *globalMBPolicy) adjustSocketCCDMBWithIncubates(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[qosgroup.QoSGroup]*stat.MBQoSGroup {
	for qos, qosGroup := range mbQoSGroups {
		if qos != "dedicated" {
			continue
		}
		mbQoSGroups[qos] = g.adjustWthAdmissionIncubation(qosGroup)
	}

	return mbQoSGroups
}

func (g *globalMBPolicy) splitQoSHighAndLeaf(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) (highQoSs, leaves map[qosgroup.QoSGroup]*stat.MBQoSGroup) {
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

func (g *globalMBPolicy) getLeafOutgoingQuotas(domain int) (int, error) {
	g.lock.RLock()
	defer g.lock.RUnlock()
	if leafQuota, ok := g.domainLeafOutgoingQuotas[domain]; ok {
		return leafQuota, nil
	}

	return -1, fmt.Errorf("unknown domain %d", domain)
}

func (g *globalMBPolicy) setLeafOutgoingQuotas(leafQuotas []int) {
	g.lock.Lock()
	defer g.lock.Unlock()
	for domain, leafQuota := range leafQuotas {
		g.domainLeafOutgoingQuotas[domain] = leafQuota
	}
}

func (g *globalMBPolicy) setLeafOutgoingNoLimit() {
	g.lock.Lock()
	defer g.lock.Unlock()
	for domain := range g.domainLeafOutgoingQuotas {
		g.domainLeafOutgoingQuotas[domain] = -1
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
	sourcerType string,
) (policy.DomainMBPolicy, error) {
	domainLeafQuotas := make(map[int]int)
	for domain := range domainManager.Domains {
		domainLeafQuotas[domain] = -1
	}

	ccdPlanner := strategy.NewCCDGroupPlanner(ccdMBMin, 35_000)

	return &globalMBPolicy{
		sourcer:                  quotasourcing.New(sourcerType),
		throttler:                strategy.New(throttleType, ccdPlanner),
		easer:                    strategy.New(easeType, ccdPlanner),
		domainManager:            domainManager,
		domainLeafOutgoingQuotas: domainLeafQuotas,
		ccdGroupPlanner:          ccdPlanner,
		zombieMB:                 zombieMBDefault,
	}, nil
}
