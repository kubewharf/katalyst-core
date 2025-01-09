package mbsourcing

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"

type geometryRemoteBoundSourcer struct {
	innerSourcer geometrySourcer
	remoteLimit  int
}

func (c geometryRemoteBoundSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	rho := []float64{
		toFixedPoint2(getLocalRatio(domainTargets[0])),
		toFixedPoint2(getLocalRatio(domainTargets[1])),
	}
	target := []int{
		domainTargets[0].TargetIncoming,
		domainTargets[1].TargetIncoming,
	}
	deltaY := []float64{
		float64(domainTargets[0].TargetIncoming - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].TargetIncoming - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	deltaX := c.innerSourcer.sourceOutgoingQuota(rho, deltaY)

	result := []int{
		domainTargets[0].MBSource + deltaX[0],
		domainTargets[1].MBSource + deltaX[1],
	}

	// if one domain has high QoS pods, its alien domain has to have upper bound of remote outgoing traffic
	return c.adjustResult(result, rho, target, []int{domainTargets[0].MBSourceRemoteLimit, domainTargets[1].MBSourceRemoteLimit})
}

func (c geometryRemoteBoundSourcer) adjustResult(domainOutgoingQuotas []int, rho []float64, targetIncoming []int, targetIncomingRemote []int) []int {
	domainOutgoingQuotas = c.innerSourcer.adjustResult(domainOutgoingQuotas, rho, targetIncoming)
	for domain, sourceQuota := range domainOutgoingQuotas {
		// to clip down with the remote upper bound
		if (1-rho[domain])*float64(sourceQuota) > float64(targetIncomingRemote[domain]) {
			domainOutgoingQuotas[domain] = int(float64(targetIncomingRemote[domain]) / (1 - rho[domain]))
		}
	}
	return domainOutgoingQuotas
}

func newGeometryRemoteBoundSourcer() Sourcer {
	return &geometryRemoteBoundSourcer{
		innerSourcer: geometrySourcer{},
		remoteLimit:  config.PolicyConfig.MBRemoteLimit,
	}
}
