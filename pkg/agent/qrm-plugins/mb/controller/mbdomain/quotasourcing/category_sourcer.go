package quotasourcing

import (
	"fmt"
	"math"
)

const maxMB = 35_000

type categorySourcer struct{}

func (c categorySourcer) AttributeMBToSources(domainTargets []DomainMB) []int {
	rho := []float64{
		toFixedPoint2(getLocalRatio(domainTargets[0])),
		toFixedPoint2(getLocalRatio(domainTargets[1])),
	}
	target := []int{
		domainTargets[0].Target,
		domainTargets[1].Target,
	}
	deltaY := []float64{
		float64(domainTargets[0].Target - (domainTargets[0].MBSource - domainTargets[0].MBSourceRemote + domainTargets[1].MBSourceRemote)),
		float64(domainTargets[1].Target - (domainTargets[1].MBSource - domainTargets[1].MBSourceRemote + domainTargets[0].MBSourceRemote)),
	}

	// both to ease
	deltaX := []int{0, 0}
	if deltaY[0] >= 0 && deltaY[1] >= 0 {
		// general approach is
		// get meeting point if  all is positive, otherwise
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
	} else if deltaY[0] <= 0 && deltaY[1] <= 0 {
		// similar to both easing, except that outer line should be chosen
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
				deltaX = []int{-deltaX[0], -deltaX[1]}
			} else {
				// outer line is line 0
				deltaX = pickAlongLine(x0, y0)
				deltaX = []int{-deltaX[0], -deltaX[1]}
			}
		}
	} else if deltaY[0] >= 0 && deltaY[1] <= 0 {
		deltaX = processEaseThrottl(rho, deltaY)
	} else {
		deltaXReversed := []int{0, 0}
		deltaXReversed = processEaseThrottl([]float64{rho[1], rho[0]}, []float64{deltaY[1], deltaY[0]})
		deltaX = []int{deltaXReversed[1], deltaXReversed[0]}
	}

	result := []int{
		domainTargets[0].MBSource + deltaX[0],
		domainTargets[1].MBSource + deltaX[1],
	}

	return adjustResult(result, rho, target)
}

// mixed case: 0 to ease, 1 to throttle
func processEaseThrottl(rho []float64, deltaY []float64) (deltaX []int) {
	// has to be one positive the other negative
	foundResult := false
	// try domain 0 negative
	crossPoint, err := locateCrossPointForm2(rho, []float64{deltaY[0], -deltaY[1]})
	if err == nil && isAllPositive(crossPoint) {
		deltaX = []int{-crossPoint[0], crossPoint[1]}
		foundResult = true
	} else {
		//x0, y0 := getLineEnds(-rho[0], 1-rho[1], deltaY[0])
		x1, _ := getLineEnds(-(1 - rho[0]), rho[1], -deltaY[0])
		if x1 > 0 {
			deltaX = []int{-x1, 0}
			foundResult = true
		}
	}

	// try domain 0 as positive
	if !foundResult {
		crossPoint2, err2 := locateCrossPointForm3(rho, []float64{deltaY[0], -deltaY[1]})
		if err2 == nil && isAllPositive(crossPoint2) {
			deltaX = []int{crossPoint2[0], -crossPoint2[1]}
		} else {
			if rho[1] != 0 {
				deltaX = []int{0, -int(deltaY[1] / rho[1])}
			}
		}
	}

	return deltaX
}

func pickAlongLine(x, y int) []int {
	if x == math.MaxInt {
		return []int{maxMB * 8, y}
	} else if y == math.MaxInt {
		return []int{x, maxMB * 8}
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
		x = int(c / b)
	}
	return x, y
}

// line r0 * x0 + (1-r1) * x1 = y0, and
//      (1-r0) * x0 + r1 * x1 = y1, given all xi,yi >=0
func locateCrossPoint(rho []float64, y []float64) ([]int, error) {
	if rho[0]+rho[1] == 1 {
		return nil, fmt.Errorf("parallel lines having no cross point")
	}

	x0 := (rho[1]*y[0] - (1-rho[1])*y[1]) / (rho[0] + rho[1] - 1)
	x1 := (rho[0]*y[1] - (1-rho[0])*y[0]) / (rho[0] + rho[1] - 1)
	return []int{int(x0), int(x1)}, nil
}

// line -r0 * x0 + (1-r1) * x1 = y0, and
//      -(1-r0) * x0 + r1 * x1 = -y1, given all xi,yi >=0
func locateCrossPointForm2(rho []float64, y []float64) ([]int, error) {
	if rho[0]+rho[1] == 1 {
		return nil, fmt.Errorf("parallel lines having no cross point")
	}

	x0 := (rho[1]*y[0] + (1-rho[1])*y[1]) / (1 - rho[0] - rho[1])
	var x1 float64
	if rho[1] != 1.0 {
		x1 = (rho[0]*x0 + y[0]) / (1 - rho[1])
	} else {
		x1 = (1-rho[0])*x0 - y[1]
	}
	return []int{int(x0), int(x1)}, nil
}

// line r0 * x0 - (1-r1) * x1 = y0, and
//      (1-r0) * x0 - r1 * x1 = -y1, given all xi,yi >=0
func locateCrossPointForm3(rho []float64, y []float64) ([]int, error) {
	if rho[0]+rho[1] == 1 {
		return nil, fmt.Errorf("parallel lines having no cross point")
	}

	x0 := (-rho[1]*y[0] - (1-rho[1])*y[1]) / (1 - rho[0] - rho[1])
	var x1 float64
	if rho[1] != 1.0 {
		x1 = (rho[0]*x0 - y[0]) / (1 - rho[1])
	} else {
		x1 = (1-rho[0])*x0 + y[1]
	}
	return []int{int(x0), int(x1)}, nil
}

func isAllPositive(values []int) bool {
	for _, v := range values {
		if v < 0 {
			return false
		}
	}
	return true
}

//func isAllBelowMax(values []int) bool {
//	for _, v := range values {
//		if v > maxMB {
//			return false
//		}
//	}
//	return true
//}

func adjustResult(result []int, rho []float64, target []int) []int {
	if !isAllPositive(result) {
		if result[0] < 0 {
			return []int{0, calcMinFormula(1-rho[1], target[0], target[1])}
		} else {
			return []int{calcMinFormula(rho[0], target[0], target[1]), 0}
		}
	}
	return result
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

// uint of 0.01
func toFixedPoint2(value float64) float64 {
	const point2 = 100
	return float64(int(value*point2)) / point2
}

func NewCategorySourcer() Sourcer {
	return &categorySourcer{}
}
