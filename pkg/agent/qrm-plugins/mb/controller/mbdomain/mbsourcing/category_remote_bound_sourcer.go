package mbsourcing

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"

type categoryRemoteBoundSourcer struct {
	cateSourcer categorySourcer
	remoteLimit int
}

func (c categoryRemoteBoundSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
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

	deltaX := c.cateSourcer.sourceOutgoingQuota(rho, deltaY)

	result := []int{
		domainTargets[0].MBSource + deltaX[0],
		domainTargets[1].MBSource + deltaX[1],
	}

	// if one domain has high QoS pods, its alien domain has to have upper bound of remote outgoing traffic
	return c.adjustResult(result, rho, target, []int{domainTargets[0].MBSourceRemoteLimit, domainTargets[1].MBSourceRemoteLimit})
}

func (c categoryRemoteBoundSourcer) adjustResult(result []int, rho []float64, target []int, targetIncomingRemote []int) []int {
	result = c.cateSourcer.adjustResult(result, rho, target)
	for i, quota := range result {
		// to clip down with the remote upper bound
		if (1-rho[i])*float64(quota) > float64(targetIncomingRemote[i]) {
			result[i] = int(float64(targetIncomingRemote[i]) / (1 - rho[i]))
		}
	}
	return result
}

func NewCategoryRemoteBoundSourcer() Sourcer {
	return &categoryRemoteBoundSourcer{
		cateSourcer: categorySourcer{},
		remoteLimit: config.PolicyConfig.MBRemoteLimit,
	}
}
