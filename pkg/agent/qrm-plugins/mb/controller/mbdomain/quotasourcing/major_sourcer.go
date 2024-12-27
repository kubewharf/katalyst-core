package quotasourcing

import "math"

type majorfactorSourcer struct {
}

func (m majorfactorSourcer) AttributeMBToSources(domainTargets []DomainMB) []int {
	rho := []float64{getLocalRatio(domainTargets[0]), getLocalRatio(domainTargets[1])}
	deltaY := []float64{
		float64(domainTargets[0].Target - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].Target - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	trendY := []float64{
		toFixedPoint(deltaY[0] / float64((domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote))),
		toFixedPoint(deltaY[1] / float64((domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote))),
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
			int(math.Min(float64(domainTargets[0].Target)/(1-rho[1]), float64(domainTargets[1].Target)/rho[1])),
		}
	}
	if result[1] < 0 {
		return []int{
			int(math.Min(float64(domainTargets[0].Target)/rho[0], float64(domainTargets[1].Target)/(1-rho[0]))),
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

	return toFixedPoint(localRatio*hostYTrend + (1-localRatio)*alienYTrend)
}

func toFixedPoint(value float64) float64 {
	const point3 = 1000
	return float64(int(value*1000)) / 1000
}

func NewMajorfactorSourcer() Sourcer {
	return &majorfactorSourcer{}
}
