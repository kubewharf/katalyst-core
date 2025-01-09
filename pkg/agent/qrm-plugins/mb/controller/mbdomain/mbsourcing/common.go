package mbsourcing

import "fmt"

// getLocalRatio gets the ratio of local mb out of total for the given mb domain
// local mb is the mb amount accessing the memory in the domain (e.g. cpu socket);
// remote mb is amount accessing memory to other domain (e.g. the other cpu socket).
// both local and remote mb is originated from same domain, but the destination is different.
func getLocalRatio(data DomainMBTargetSource) float64 {
	if data.MBSource == 0 {
		return 1.0
	}
	if data.MBSourceRemote >= data.MBSource {
		return 0.0
	}
	return 1 - float64(data.MBSourceRemote)/float64(data.MBSource)
}

// uint of 0.01
func toFixedPoint2(value float64) float64 {
	const point2 = 100
	return float64(int(value*point2)) / point2
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
