package quotasourcing

import "fmt"

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
		if result != -1 { // target constrain exists; need to identify the constraint sources for domain i (dimension?)
			// domain i has constraint rule: ri * qi + (1-rj) * qj <= ti
			// consider 3 possible candidates:
			//   orthogonal point, left end point, and right end point

			j := (i + 1) % 2 // stands if there are 2 domains only
			if orthoHost, orthOther, err := getOrthogonalPoint(domainTargets[i], domainTargets[j]); err == nil {
				orthPoint := make([]int, 3)
				orthPoint[j] = orthOther
				orthPoint[i] = orthoHost
				orthPoint[2] = i // of domain i
				candidates = append(candidates, orthPoint)
			}

			if hostQuota, remoteQuote, err := getRightEndpoint(domainTargets[i], domainTargets[j]); err == nil {
				point := make([]int, 3)
				point[j] = remoteQuote
				point[i] = hostQuota
				point[2] = i // of domain i
				candidates = append(candidates, point)
			}

			if hostQuota, remoteQuote, err := getLeftEndpoint(domainTargets[i], domainTargets[j]); err == nil {
				point := make([]int, 3)
				point[j] = remoteQuote
				point[i] = hostQuota
				point[2] = i // of domain i
				candidates = append(candidates, point)
			}
		}
	}

	if len(candidates) == 0 {
		return results
	}

	// the meeting point, if exists, is also a candidate need to consider
	if hostQuota, otherQuote, err := getCrossPoint(domainTargets[0], domainTargets[1]); err == nil {
		candidates = append(candidates, []int{hostQuota, otherQuote, 0})
	}

	return locateFittest(candidates, float64(domainTargets[0].MBSource), float64(domainTargets[1].MBSource),
		domainTargets)
}

func locateFittest(candidates [][]int, x, y float64, domainTargets []DomainMB) []int {
	result := make([]int, 2)
	if len(candidates) == 1 {
		result[0] = candidates[0][0]
		result[1] = candidates[0][1]
		return result
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
		if domainTargets[jDomain].Target >= 0 && jValue > float64(domainTargets[jDomain].Target) {
			continue
		}
		if minDist == -1 || euclidDistance(float64(candidate[0]), x, float64(candidate[1]), y) < minDist {
			result[0] = candidate[0]
			result[1] = candidate[1]
			minDist = euclidDistance(float64(candidate[0]), x, float64(candidate[1]), y)
		}
	}

	return result
}

// getLocalRatio gets the ratio of local mb out of total for the given mb domain
// local mb is the mb amount accessing the memory in the domain (e.g. cpu socket);
// remote mb is amount accessing memory to other domain (e.g. the other cpu socket).
// both local and remote mb is originated from same domain, but the destination is different.
func getLocalRatio(data DomainMB) float64 {
	if data.MBSource == 0 {
		return 0.0
	}
	if data.MBSourceRemote >= data.MBSource {
		return 0.0
	}
	return 1 - float64(data.MBSourceRemote)/float64(data.MBSource)
}

func getOrthogonalPoint(hostDomain DomainMB, remoteDomain DomainMB) (hostQuota, remoteQuota int, err error) {
	hostLocalRatio := getLocalRatio(hostDomain)
	remoteLocalRatio := getLocalRatio(remoteDomain)
	if hostLocalRatio == 0 { //all host originated is accessing other domain;
		if remoteLocalRatio == 1.0 {
			return 0, 0, fmt.Errorf("no way to have some host mb amount")
		}
		hostQuota = hostDomain.MBSource // keep current value for orthogonal line
		remoteQuota = int(float64(hostDomain.Target) / (1 - remoteLocalRatio))
		return hostQuota, remoteQuota, nil
	}

	a := (remoteLocalRatio - 1) / hostLocalRatio
	b := float64(hostDomain.Target) / hostLocalRatio
	originX := float64(remoteDomain.MBSource)
	originY := float64(remoteDomain.MBSource)
	remoteQuota, hostQuota = orthogonalPoint(originX, originY, a, b)
	return hostQuota, remoteQuota, nil
}

func getRightEndpoint(hostDomain DomainMB, remoteDomain DomainMB) (hostQuota, remoteQuota int, err error) {
	hostLocalRatio := getLocalRatio(hostDomain)
	remoteLocalRatio := getLocalRatio(remoteDomain)
	if hostLocalRatio == 0 && remoteLocalRatio == 1.0 {
		return -1, -1, fmt.Errorf("no way to  have mb amount")
	}

	if hostLocalRatio == 0 {
		remoteQuota = int(float64(hostDomain.Target) / (1 - remoteLocalRatio))
		if remoteQuota < minValue {
			remoteQuota = minValue
		}
		return hostDomain.MBSource, remoteQuota, nil
	}

	remoteQuota = minValue
	hostQuota = int((float64(hostDomain.Target) - (1-remoteLocalRatio)*float64(remoteQuota)) / hostLocalRatio)
	return hostQuota, remoteQuota, nil
}

func getLeftEndpoint(hostDomain DomainMB, remoteDomain DomainMB) (hostQuota, remoteQuota int, err error) {
	hostLocalRatio := getLocalRatio(hostDomain)
	remoteLocalRatio := getLocalRatio(remoteDomain)
	if hostLocalRatio == 0 && remoteLocalRatio == 1.0 {
		return -1, -1, fmt.Errorf("no way to  have mb amount")
	}

	if remoteLocalRatio == 1.0 {
		hostQuota = int(float64(hostDomain.Target) / hostLocalRatio)
		if hostQuota < minValue {
			hostQuota = minValue
		}
		return hostQuota, remoteDomain.MBSource, nil
	}

	hostQuota = minValue
	remoteQuota = int((float64(hostDomain.Target) - hostLocalRatio*float64(hostQuota)) / (1 - remoteLocalRatio))
	return hostQuota, remoteQuota, nil

}

func getCrossPoint(hostDomain, otherDomain DomainMB) (hostQuota, otherQuote int, err error) {
	if hostDomain.Target < 0 || otherDomain.Target < 0 {
		return -1, -1, fmt.Errorf("no cross point for target %d, %d", hostDomain.Target, otherDomain.Target)
	}

	hostLocalRatio := getLocalRatio(hostDomain)
	remoteLocalRatio := getLocalRatio(otherDomain)
	a0, b0, c0 := hostLocalRatio, 1-remoteLocalRatio, float64(hostDomain.Target)
	a1, b1, c1 := 1-hostLocalRatio, remoteLocalRatio, float64(otherDomain.Target)
	if !hasValidMeetingPoint(a0, b0, c0, a1, b1, c1) {
		return -1, -1, fmt.Errorf("no cross point for duplicated(parallel) lines")
	}

	hostQuo, otherQuo, err := getMeetingPoint(hostLocalRatio, remoteLocalRatio, float64(hostDomain.Target), float64(otherDomain.Target))
	return int(hostQuo), int(otherQuo), err
}

var _ Sourcer = &CrossSourcer{}
