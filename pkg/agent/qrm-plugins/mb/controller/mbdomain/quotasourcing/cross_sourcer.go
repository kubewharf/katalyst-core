package quotasourcing

// todo: var config
const minValue = 4_000

type CrossSourcer struct{}

func (c CrossSourcer) AttributeMBToSources(domainTargets []DomainMB) []int {
	if len(domainTargets) != 2 {
		panic("only 2 domains supported for now")
	}

	results := make([]int, len(domainTargets))
	for i, domainTarget := range domainTargets {
		if domainTarget.Target == -1 {
			results[i] = -1 // no constraint for this domain
		}
	}

	candidates := make([][]int, 0)
	for i, result := range results {
		if result == 0 { // target constrain exists; need to identify the constraint sources for domain i (dimension?)
			j := (i + 1) % 2 // stands if there are 2 domains only
			// todo: treat very tiny ratio to 0?
			iLocalRatio := getLocalRatio(domainTargets[i])
			jLocalRatio := getLocalRatio(domainTargets[j])
			if iLocalRatio == 0 {
				panic("to impl")
			}

			a := (jLocalRatio - 1) / iLocalRatio
			b := float64(domainTargets[i].Target) / iLocalRatio
			originX := float64(domainTargets[j].MBSource)
			originY := float64(domainTargets[i].MBSource)
			x, y := orthogonalPoint(originX, originY, a, b)
			if isValid(x, y, minValue) {
				candidate := make([]int, 3)
				candidate[j] = x
				candidate[i] = y
				candidate[2] = i // of domain i
				candidates = append(candidates, candidate)
				continue
			}

			// the 2 end points are (min, a*min + b), ( (min - b)/a, min)
			// locate the one with smaller distance to (originX, originY)
			distLeft := euclidDistance(minValue, a*minValue+b, originX, originY)
			distRight := euclidDistance((minValue-b)/a, minValue, originX, originY)
			if distLeft <= distRight {
				candidate := make([]int, 3)
				candidate[j] = minValue
				candidate[i] = int(a*minValue + b)
				candidate[2] = i // of domain i
				candidates = append(candidates, candidate)
			} else {
				candidate := make([]int, 3)
				candidate[j] = int((minValue - b) / a)
				candidate[i] = minValue
				candidate[2] = i // of domain i
				candidates = append(candidates, candidate)
			}
		}
	}

	if len(candidates) == 0 {
		return results
	}

	// locate the meeting point if exist
	//	panic("impl")

	return locateFittest(candidates, nil,
		float64(domainTargets[0].MBSource), float64(domainTargets[1].MBSource),
		domainTargets)
}

func locateMeetingPoint(domainTargets []DomainMB) []int {
	panic("impl")
}

func locateFittest(candidates [][]int, meetingPoint []int, x, y float64, domainTargets []DomainMB) []int {
	result := make([]int, 2)
	if len(candidates) == 1 {
		result[0] = candidates[0][0]
		result[1] = candidates[0][1]
		return result
	}

	if len(candidates) > 2 {
		panic("not support more than 2 domains")
	}

	// todo: find a better sentinal than -1
	minDist := -1
	for _, candidate := range candidates {
		iDomain := candidate[2]
		jDomain := (iDomain + 1) % 2 // stands if there are 2 domains only
		// keeps i if i satisfies the constraint of j
		iLocalRatio := getLocalRatio(domainTargets[iDomain])
		jLocalRatio := getLocalRatio(domainTargets[jDomain])
		jValue := (1-iLocalRatio)*float64(candidate[0]) + jLocalRatio*float64(candidate[1])
		if jValue > float64(domainTargets[jDomain].Target) {
			continue
		}
		if minDist == -1 || euclidDistance(float64(candidate[0]), x, float64(candidate[1]), y) < minDist {
			result[0] = candidate[0]
			result[1] = candidate[1]
			minDist = euclidDistance(float64(candidate[0]), x, float64(candidate[1]), y)
		}
	}

	if len(meetingPoint) == 2 {
		if minDist == -1 || euclidDistance(float64(meetingPoint[0]), x, float64(meetingPoint[1]), y) < minDist {
			result[0] = meetingPoint[0]
			result[1] = meetingPoint[1]
		}
	}

	return result
}

func getLocalRatio(data DomainMB) float64 {
	if data.MBSource == 0 {
		return 0.0
	}
	if data.MBSourceRemote >= data.MBSource {
		return 0.0
	}
	return 1 - float64(data.MBSourceRemote)/float64(data.MBSource)
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

func isValid(x, y int, min int) bool {
	return x >= min && y >= min
}

func euclidDistance(x0, y0, x1, y1 float64) int {
	return int((x0-x1)*(x0-x1) + (y0-y1)*(y0-y1))
}

var _ Sourcer = &CrossSourcer{}
