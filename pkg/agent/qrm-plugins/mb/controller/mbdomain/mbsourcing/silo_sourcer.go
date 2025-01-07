package mbsourcing

// SiloSourcer takes domain as silo having no impact to/from others
type SiloSourcer struct{}

func (s SiloSourcer) AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int {
	// to disregard impacts from/to others
	results := make([]int, len(domainTargets))
	for i, domainTarget := range domainTargets {
		results[i] = domainTarget.TargetIncoming
	}
	return results
}

var _ Sourcer = &SiloSourcer{}
