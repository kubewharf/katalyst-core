package quotasourcing

import (
	"fmt"
	"math"
)

// assuming 0 <= a0, a1, b0, b1 <= 1.0
// the meeting point has to be within the first phase of x-y dimension
func hasValidMeetingPoint(a0, b0, c0, a1, b1, c1 float64) bool {
	x0, y0, err0 := locateAxisPoint(a0, b0, c0)
	x1, y1, err1 := locateAxisPoint(a1, b1, c1)
	if err0 != nil || err1 != nil {
		return false
	}

	if x0 == math.MaxFloat64 && x1 == math.MaxFloat64 {
		return y0 == y1
	}
	if y0 == math.MaxFloat64 && y1 == math.MaxFloat64 {
		return x0 == x1
	}

	if x0 <= x1 && y0 >= y1 {
		return true
	}
	if x0 >= x1 && y0 <= y1 {
		return true
	}
	return false
}

// line: ax + by = c
func locateAxisPoint(a, b, c float64) (x, y float64, err error) {
	x, y = math.MaxFloat64, math.MaxFloat64
	if a == 0 && b == 0 && c != 0 {
		return x, y, fmt.Errorf("math unsolvable")
	}

	if a != 0 {
		x = c / a
	}
	if b != 0 {
		y = c / b
	}
	return x, y, nil
}

// locate the meeting point of
// 2 lines: r0 X + (1-r1)Y = T0
//          (1-r0)X + r1 Y = T1
func getMeetingPoint(r0, r1, t0, t1 float64) (x, y float64, err error) {
	if r0+r1 == 1.0 {
		return 0, 0, fmt.Errorf("2 lines not crossing")
	}

	return (t1 - r1*(t0+t1)) / (1.0 - r0 - r1), (t0 - r0*(t0+t1)) / (1.0 - r0 - r1), nil
}

// orthogonalPoint locates the (x,y) to which draw a line from origin (originX, originY)
// orthogonal to the line y=ax+b
func orthogonalPoint(originX, originY, a, b float64) (x, y int) {
	demoninator := a*a + 1
	xNumarator := originX + a*(originY-b)
	yNumerator := a*(originX+a*originY) + b
	x = int(xNumarator / demoninator)
	y = int(yNumerator / demoninator)
	return x, y
}

func euclidDistance(x0, y0, x1, y1 float64) int {
	return int((x0-x1)*(x0-x1) + (y0-y1)*(y0-y1))
}
