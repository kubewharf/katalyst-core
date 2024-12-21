package quotasourcing

type DomainMB struct {
	Target         int // the desired mb to have; -1 means no constraint at all - maximum mb it can get
	MBSource       int // the current mb totaling by all contributors (e.g. low prio CCDs)
	MBSourceRemote int // the current mb accessing other domains' memory
}

type Sourcer interface {
	// AttributeMBToSources to source-attribute the quota of each domain
	// e.g., for below scenario
	//     domain 0: target 50, source-local 75, source-remote 25
	//     domain 1: target 60, source-local 60, source-remote 30
	// we would like to decide what the quota set to domain 0 and domain 1, so that
	// the desired target would be 50, 60
	AttributeMBToSources(domainTargets []DomainMB) []int
}

// SiloSourcer takes domain as silo having no impact to/from others
// it is mere for comparison purpose
type SiloSourcer struct{}

func (s SiloSourcer) AttributeMBToSources(domainTargets []DomainMB) []int {
	// to disregard impacts from/to others
	results := make([]int, len(domainTargets))
	for i, domainTarget := range domainTargets {
		results[i] = domainTarget.Target
	}
	return results
}

var _ Sourcer = &SiloSourcer{}
