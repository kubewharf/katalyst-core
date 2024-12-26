package quotasourcing

import "math"

type formulaSourcer struct{}

func (f formulaSourcer) AttributeMBToSources(domainTargets []DomainMB) []int {
	localRatios := map[int]float64{
		0: getLocalRatio(domainTargets[0]),
		1: getLocalRatio(domainTargets[1]),
	}

	if localRatios[0]+localRatios[1] == 1 {
		sum := math.MaxInt
		for domain, mb := range domainTargets {
			if localRatios[domain] == 0 {
				continue
			}

			value := int(float64(mb.Target) / localRatios[domain])
			if value < sum {
				sum = value
			}
		}

		if sum == math.MaxInt {
			// no limit
			return []int{35_000, 35_000}
		}

		return attributeSumProportions(domainTargets, sum)
	}

	return attributeBasedOnSolution(domainTargets)
}

// corner case to calc x0 + x1 <= sum
func attributeSumProportions(domainTargets []DomainMB, sum int) []int {
	current := domainTargets[0].MBSource + domainTargets[1].MBSource
	ratio := float64(sum) / float64(current)
	return []int{
		int(float64(domainTargets[0].MBSource) * ratio),
		int(float64(domainTargets[1].MBSource) * ratio),
	}
}

func attributeBasedOnSolution(domainTargets []DomainMB) []int {
	rho := []float64{getLocalRatio(domainTargets[0]), getLocalRatio(domainTargets[1])}
	y := []float64{float64(domainTargets[0].Target), float64(domainTargets[1].Target)}

	x0 := (rho[1]*y[0] - (1-rho[1])*y[1]) / (rho[0] + rho[1] - 1)
	x1 := (rho[0]*y[1] - (1-rho[0])*y[0]) / (rho[0] + rho[1] - 1)
	return []int{int(x0), int(x1)}
}

func NewFormulaSourcer() Sourcer {
	panic("has flaw; not already")
	return &formulaSourcer{}
}
