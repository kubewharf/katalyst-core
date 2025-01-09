package mbsourcing

import (
	"math"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"
)

// categorySourcer categorizes into 4 cases based on positive/negative of domain incoming changes
// and assumes the directions of domain outgoing traffic change accordingly
type categorySourcer struct{}

func (c categorySourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
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

	deltaX := c.sourceOutgoingQuota(rho, deltaY)

	result := []int{
		domainTargets[0].MBSource + deltaX[0],
		domainTargets[1].MBSource + deltaX[1],
	}

	return c.adjustResult(result, rho, target)
}

func (c categorySourcer) sourceOutgoingQuota(rho []float64, deltaY []float64) []int {
	// both to ease
	deltaX := []int{0, 0}
	if deltaY[0] >= 0 && deltaY[1] >= 0 {
		return processEaseEase(rho, deltaY)
	} else if deltaY[0] <= 0 && deltaY[1] <= 0 {
		return processThrottleThrottle(rho, deltaY)
	} else if deltaY[0] >= 0 && deltaY[1] <= 0 {
		deltaX = processEaseThrottle(rho, deltaY)
	} else {
		deltaXReversed := []int{0, 0}
		deltaXReversed = processEaseThrottle([]float64{rho[1], rho[0]}, []float64{deltaY[1], deltaY[0]})
		deltaX = []int{deltaXReversed[1], deltaXReversed[0]}
	}

	return deltaX
}

func processEaseEase(rho []float64, deltaY []float64) (deltaX []int) {
	// if all is to ease, the geometry representation of one domain is like below
	//  |
	//  |\
	//  |.\
	//  |..\
	//  L...\_______
	// the solution is get meeting point (should it be a valid one), otherwise
	// locate a point at the inner line
	crossPoint, err := locateCrossPoint(rho, deltaY)
	if err == nil && isAllPositive(crossPoint) {
		deltaX = crossPoint
	} else {
		// locate the inner line
		x0, y0 := getLineEnds(rho[0], 1-rho[1], deltaY[0])
		x1, y1 := getLineEnds(1-rho[0], rho[1], deltaY[1])
		if x0 < x1 || y0 < y1 {
			// inner line is line 0
			deltaX = pickAlongLine(x0, y0)
		} else {
			// inner line is line 1
			deltaX = pickAlongLine(x1, y1)
		}
	}

	return deltaX
}

func processThrottleThrottle(rho []float64, deltaY []float64) (deltaX []int) {
	// if all is to throttle, the geometry representation of one domain is like below
	//  |..........
	//  |\.........
	//  | \........
	//  |  \.......
	//  L __\......
	// the solution is get meeting point (should it be a valid one), otherwise
	// locate a point at the outer line
	// i.e. similar to both easing, except that outer line should be chosen
	crossPoint, err := locateCrossPoint(rho, []float64{-deltaY[0], -deltaY[1]})
	if err == nil && isAllPositive(crossPoint) {
		deltaX = []int{-crossPoint[0], -crossPoint[1]}
	} else {
		// locate the outer line
		x0, y0 := getLineEnds(rho[0], 1-rho[1], deltaY[0])
		x1, y1 := getLineEnds(1-rho[0], rho[1], deltaY[1])
		if x0 < x1 || y0 < y1 {
			// outer line is line 1
			deltaX = pickAlongLine(x1, y1)
		} else {
			// outer line is line 0
			deltaX = pickAlongLine(x0, y0)
		}

		deltaX = []int{-deltaX[0], -deltaX[1]}
	}

	return deltaX
}

// mixed case: 0 to ease, 1 to throttle
// caller guarantee that deltaY[0] >=0 and deltaY[1] <= 0
func processEaseThrottle(rho []float64, deltaY []float64) (deltaX []int) {
	// deltaX has to be one positive the other negative

	// first try domain 0 negative
	if deltaX, err := processEaseThrottleWithThrottleEase(rho, deltaY); err == nil {
		return deltaX
	}

	// try domain 0 as positive, if first try is not good enough
	if deltaX, err := processEaseThrottleWithEaseThrottle(rho, deltaY); err == nil {
		return deltaX
	}

	// in theory no solution for rho[0] == 1 && rho[1] == 0, as the formula (1-rho[0]*x0 + rho[1]*x1 <= -y1 (if y1 != 0 ) no way to be true
	// in practice, lets make a compromise of being at conservative (safer) side
	deltaX = []int{0, -config.PolicyConfig.DomainMBMax}
	return deltaX
}

func processEaseThrottleWithThrottleEase(rho []float64, deltaY []float64) (deltaX []int, err error) {
	// geometry representation of one domain is like below
	//  |    /....
	//  |   /.....
	//  |  /......
	//  L_/.......
	// the solution is get meeting point (should it be a valid one), otherwise
	// locate a point at the valid area
	crossPoint, err := locateCrossPointForm2(rho, []float64{deltaY[0], -deltaY[1]})
	if err == nil && isAllPositive(crossPoint) {
		deltaX = []int{-crossPoint[0], crossPoint[1]}
		return deltaX, nil
	} else {
		x1, _ := getLineEnds(-(1 - rho[0]), rho[1], -deltaY[1])
		if x1 > 0 && x1 != math.MaxInt {
			deltaX = []int{-x1, 0}
			return deltaX, nil
		}
		return nil, errors.New("unable to get valid solution")
	}
}

func processEaseThrottleWithEaseThrottle(rho []float64, deltaY []float64) (deltaX []int, err error) {
	// geometry representation of one domain is like below
	//  |..../
	//  |.../
	//  |../
	//  L./_____
	// the solution is get meeting point (should it be a valid one), otherwise
	// locate a point at the valid area
	// the solution is get meeting point (should it be a valid one), otherwise
	// locate a point at the valid area
	crossPoint2, err2 := locateCrossPointForm3(rho, []float64{deltaY[0], -deltaY[1]})
	if err2 == nil && isAllPositive(crossPoint2) {
		deltaX = []int{crossPoint2[0], -crossPoint2[1]}
		return deltaX, nil
	} else {
		if rho[1] != 0 {
			deltaX = []int{0, -int(-deltaY[1] / rho[1])}
			return deltaX, nil
		}
		return nil, errors.New("unable to get valid solution")
	}
}

// pickAlongLine picks the middle point for a normal line from (x,0) to (0,y), given x>=0, y>=0
func pickAlongLine(x, y int) []int {
	if x == math.MaxInt {
		return []int{config.PolicyConfig.DomainMBMax, y}
	} else if y == math.MaxInt {
		return []int{x, config.PolicyConfig.DomainMBMax}
	}
	return []int{x / 2, y / 2}
}

// line a * x + b * y = c
func getLineEnds(a, b, c float64) (x, y int) {
	x = math.MaxInt
	if a != 0 {
		x = int(c / a)
	}
	y = math.MaxInt
	if b != 0 {
		y = int(c / b)
	}
	return x, y
}

func (c categorySourcer) adjustResult(domainOutgoingQuotas []int, rho []float64, targetIncoming []int) []int {
	if !isAllPositive(domainOutgoingQuotas) {
		if domainOutgoingQuotas[0] < 0 {
			return []int{0, calcMinFormula(1-rho[1], targetIncoming[0], targetIncoming[1])}
		} else {
			return []int{calcMinFormula(rho[0], targetIncoming[0], targetIncoming[1]), 0}
		}
	}
	return domainOutgoingQuotas
}

// min{ y0/r, y1/(1-r) }
func calcMinFormula(rho float64, y0, y1 int) int {
	rho = toFixedPoint2(rho)
	result := math.MaxInt
	if rho > 0 {
		value := int(float64(y0) / rho)
		if value < result {
			result = value
		}
	}
	if rho < 1.0 {
		value := int(float64(y1) / (1 - rho))
		if value < result {
			result = value
		}
	}
	return result
}

func NewCategorySourcer() Sourcer {
	return &categorySourcer{}
}
