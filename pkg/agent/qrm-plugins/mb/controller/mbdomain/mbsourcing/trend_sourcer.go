package mbsourcing

import (
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
)

type trendfactorSourcer struct {
}

func getTrend(i int, domainTargets []DomainMBTargetSource) float64 {
	j := mbdomain.GetAlienDomainID(i)
	currentRecipientUsage := domainTargets[i].MBSource - domainTargets[i].MBSourceRemote + domainTargets[j].MBSourceRemote
	if currentRecipientUsage <= 0 {
		return math.MaxFloat64
	}

	delta := domainTargets[i].TargetIncoming - currentRecipientUsage
	return toFixedPoint2(float64(delta) / float64(currentRecipientUsage))
}

func (m trendfactorSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	rho := []float64{getLocalRatio(domainTargets[0]), getLocalRatio(domainTargets[1])}
	deltaY := []float64{
		float64(domainTargets[0].TargetIncoming - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].TargetIncoming - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	trendY := []float64{
		getTrend(0, domainTargets),
		getTrend(1, domainTargets),
	}

	majorTrendX := []float64{
		getMajorTrend(rho[0], trendY[0], trendY[1]),
		getMajorTrend(rho[1], trendY[1], trendY[0]),
	}

	// both to ease
	if trendY[0] >= 0 && trendY[1] >= 0 {
		change := calcEaseEaseBase(rho, majorTrendX, deltaY)
		return []int{
			int(float64(domainTargets[0].MBSource) + majorTrendX[0]*float64(change)),
			int(float64(domainTargets[1].MBSource) + majorTrendX[1]*float64(change)),
		}
	}

	// both to throttle
	if trendY[0] <= 0 && trendY[1] <= 0 {
		change := calcThrottleThrottleBase(rho, majorTrendX, deltaY)
		return []int{
			int(float64(domainTargets[0].MBSource) + majorTrendX[0]*float64(change)),
			int(float64(domainTargets[1].MBSource) + majorTrendX[1]*float64(change)),
		}
	}

	// one to throttle, one to ease - split into 4 cases
	if rho[0] >= 0.5 && rho[1] >= 0.5 { // both major local
		if deltaY[0] >= 0 {
			deltaX0 := deltaY[0] / rho[0]
			deltaX1 := -(-deltaY[1] + (1-rho[0])*deltaY[0]/rho[0]) / rho[1]
			return []int{
				int(float64(domainTargets[0].MBSource) + deltaX0),
				int(float64(domainTargets[1].MBSource) + deltaX1),
			}
		} else {
			deltaX1 := deltaY[1] / rho[1]
			deltaX0 := -(-deltaY[0] + (1-rho[1])*deltaY[1]/rho[1]) / rho[0]
			return []int{
				int(float64(domainTargets[0].MBSource) + deltaX0),
				int(float64(domainTargets[1].MBSource) + deltaX1),
			}
		}
	}
	if rho[0] <= 0.5 && rho[1] <= 0.5 { // both major local
		if deltaY[0] >= 0 {
			deltaX1 := deltaY[0] / (1 - rho[1])
			deltaX0 := -(-deltaY[1] + rho[1]*deltaY[0]/(1-rho[1])) / (1 - rho[0])
			return []int{
				int(float64(domainTargets[0].MBSource) + deltaX0),
				int(float64(domainTargets[1].MBSource) + deltaX1),
			}
		} else {
			deltaX0 := deltaY[1] / (1 - rho[0])
			deltaX1 := -(-deltaY[0] + rho[0]*deltaY[1]/(1-rho[0])) / (1 - rho[1])
			return []int{
				int(float64(domainTargets[0].MBSource) + deltaX0),
				int(float64(domainTargets[1].MBSource) + deltaX1),
			}
		}
	}

	result := []int{}
	if rho[0] >= 0.5 && rho[1] <= 0.5 { // one major local, the other major remote
		if deltaY[0] >= 0 { // 0 to ease
			if deltaY[0] >= -deltaY[1] { // more is easing 0
				// assuming whoever contributes more to 0 to have positive delta
				if rho[0] >= (1 - rho[1]) { // 0 positive
					deltaX0 := deltaY[0] / rho[0]
					deltaX1 := -(-deltaY[1] + (1-rho[0])*deltaY[0]/rho[0]) / rho[1]
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				} else { // 1 positive
					deltaX1 := deltaY[0] / (1 - rho[0])
					deltaX0 := -(-deltaY[1] + rho[1]*deltaY[0]/(1-rho[1])) / (1 - rho[0])
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				}
			} else { // more is throttling 1
				// assuming whoever contributes to 1 to have negative delta
				if (1 - rho[0]) >= rho[1] { // 0 negative
					deltaX1 := deltaY[0] / (1 - rho[0])
					deltaX0 := -(-deltaY[1] + rho[1]*deltaY[0]/(1-rho[1])) / (1 - rho[0])
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				} else { // 1 negative
					deltaX0 := deltaY[0] / rho[0]
					deltaX1 := -(-deltaY[1] + (1-rho[0])*deltaY[0]/rho[0]) / rho[1]
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				}
			}
		} else { // 0 to throttle, 1 to ease
			if -deltaY[0] >= deltaY[1] { // more is to throttle 0
				// assuming whoever contributes more to 0 to have negative delta
				if rho[0] >= (1 - rho[1]) { // 0 negative
					deltaX1 := deltaY[1] / rho[1]
					deltaX0 := -(-deltaY[0] + (1-rho[1])*deltaY[1]/rho[1]) / rho[0]
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				} else { // 1 negative
					deltaX0 := deltaY[1] / (1 - rho[0])
					deltaX1 := -(-deltaY[0] + rho[0]*deltaY[1]/(1-rho[0])) / (1 - rho[1])
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				}
			} else { // more is easing 1
				// assuming whoever contributes more to 1 to have positive delta
				if (1 - rho[0]) >= rho[1] { // 0 contributing more, hence positive
					deltaX0 := deltaY[1] / (1 - rho[0])
					deltaX1 := -(-deltaY[0] + rho[0]*deltaY[1]/(1-rho[0])) / (1 - rho[1])
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				} else { // 1 positive
					deltaX1 := deltaY[1] / rho[1]
					deltaX0 := -(-deltaY[0] + (1-rho[1])*deltaY[1]/rho[1]) / rho[0]
					result = []int{
						int(float64(domainTargets[0].MBSource) + deltaX0),
						int(float64(domainTargets[1].MBSource) + deltaX1),
					}
				}
			}
		}
	}

	// adjust if negative
	if result[0] < 0 {
		return []int{
			0,
			int(math.Min(float64(domainTargets[0].TargetIncoming)/(1-rho[1]), float64(domainTargets[1].TargetIncoming)/rho[1])),
		}
	}
	if result[1] < 0 {
		return []int{
			int(math.Min(float64(domainTargets[0].TargetIncoming)/rho[0], float64(domainTargets[1].TargetIncoming)/(1-rho[0]))),
			0,
		}
	}

	return result
}

func calcThrottleThrottleBase(rho []float64, majorTrendX []float64, deltaY []float64) int {
	change := float64(1)
	if denom := rho[0]*majorTrendX[0] + (1-rho[1])*majorTrendX[1]; denom != 0 {
		change = deltaY[0] / denom
	}
	if denom := (1-rho[0])*majorTrendX[0] + rho[1]*majorTrendX[1]; denom != 0 {
		change2 := deltaY[1] / denom
		if change < change2 {
			change = change2
		}
	}
	return int(change)
}

func calcEaseEaseBase(rho []float64, majorTrendX []float64, deltaY []float64) int {
	change := math.MaxFloat64
	if denom := rho[0]*majorTrendX[0] + (1-rho[1])*majorTrendX[1]; denom != 0 {
		change = deltaY[0] / denom
	}
	if denom := (1-rho[0])*majorTrendX[0] + rho[1]*majorTrendX[1]; denom != 0 {
		change2 := deltaY[1] / denom
		if change > change2 {
			change = change2
		}
	}
	return int(change)
}

func getMajorTrend(localRatio float64, hostYTrend, alienYTrend float64) float64 {
	if localRatio > 0.75 {
		return hostYTrend
	}
	if localRatio < 0.25 {
		return alienYTrend
	}

	return toFixedPoint2(localRatio*hostYTrend + (1-localRatio)*alienYTrend)
}

func newTrendSourcer() Sourcer {
	return &trendfactorSourcer{}
}
