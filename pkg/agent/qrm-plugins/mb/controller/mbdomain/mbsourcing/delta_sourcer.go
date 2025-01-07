package mbsourcing

import "math"

const alpha = 0.5

type deltaSourcer struct{}

func (d deltaSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	rho := []float64{getLocalRatio(domainTargets[0]), getLocalRatio(domainTargets[1])}
	delataY := []float64{
		float64(domainTargets[0].TargetIncoming - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].TargetIncoming - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	if rho[0]+rho[1] == 1.0 {
		deltaSum := math.MaxFloat64
		domainOfMinSum := -1
		for domain := range delataY {
			if rho[domain] != 0 {
				value := delataY[domain] / rho[domain]
				if value < deltaSum {
					deltaSum = value
					domainOfMinSum = domain
				}
			}
		}

		return []int{
			domainTargets[0].MBSource + int(0.5*alpha*delataY[domainOfMinSum]/rho[domainOfMinSum]),
			domainTargets[1].MBSource + int(0.5*alpha*delataY[domainOfMinSum]/rho[domainOfMinSum]),
		}
	}

	deltaX0 := alpha * (rho[1]*delataY[0] - (1-rho[1])*delataY[1]) / (rho[0] + rho[1] - 1)
	deltaX1 := alpha * (rho[0]*delataY[1] - (1-rho[0])*delataY[0]) / (rho[0] + rho[1] - 1)
	return []int{
		domainTargets[0].MBSource + int(deltaX0),
		domainTargets[1].MBSource + int(deltaX1),
	}
}

func NewDeltaSourcer() Sourcer {
	panic("has flaw; not already")
	return deltaSourcer{}
}
