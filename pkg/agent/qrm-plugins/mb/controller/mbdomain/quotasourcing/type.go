package quotasourcing

import "github.com/kubewharf/katalyst-core/pkg/util/general"

type DomainMB struct {
	Target               int // the desired recipient (incoming) mb to have; -1 means no constraint at all - maximum mb it can get
	TargetOutgoingRemote int // the upper limit outgoing remote MB target (applicable if the other domain has high QoS traffic)
	MBSource             int // the current mb totaling by all contributors (e.g. low prio CCDs)
	MBSourceRemote       int // the current mb accessing other domains' memory
}

type Sourcer interface {
	// AttributeMBToSources to source-attribute the quota of each domain
	// e.g., for below scenario
	//     domain 0: target 50, source-local 75, source-remote 25
	//     domain 1: target 60, source-local 60, source-remote 30
	// we would like to decide what the quota set to domain 0 and domain 1, so that
	// the desired target would be end up with (50, 60)
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

func New(sourcerType string) Sourcer {
	general.Infof("mbm: mb sourcer type: %s", sourcerType)
	switch sourcerType {
	case "category":
		return NewCategorySourcer()
	case "majorfactor":
		return NewMajorfactorSourcer()
	default:
		panic("not supported sourcer type")
	}
}
