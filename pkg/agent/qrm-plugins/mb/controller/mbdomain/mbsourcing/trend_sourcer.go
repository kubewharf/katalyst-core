package mbsourcing

type trendSourcer struct {
}

func (t trendSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	rho := []float64{getLocalRatio(domainTargets[0]), getLocalRatio(domainTargets[1])}
	deltaY := []float64{
		float64(domainTargets[0].TargetIncoming - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].TargetIncoming - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	trendY := []float64{
		deltaY[0] / float64((domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		deltaY[1] / float64((domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	trendX := []float64{
		rho[0]*trendY[0] + (1-rho[0])*trendY[1],
		rho[1]*trendY[1] + (1-rho[1])*trendY[0],
	}

	return []int{
		domainTargets[0].MBSource + int(trendX[0]*float64(domainTargets[0].MBSource)),
		domainTargets[1].MBSource + int(trendX[1]*float64(domainTargets[1].MBSource)),
	}
}

func NewTrendSourcer() Sourcer {
	panic("has flaw; not in use yet")
	return &trendSourcer{}
}
