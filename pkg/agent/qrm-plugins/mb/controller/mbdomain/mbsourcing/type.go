package mbsourcing

import "github.com/kubewharf/katalyst-core/pkg/util/general"

type DomainMBTargetSource struct {
	TargetIncoming      int // the desired recipient (incoming) mb to have; -1 means no constraint at all - maximum mb it can get
	MBSource            int // the current mb totaling by all contributors (e.g. low prio CCDs)
	MBSourceRemote      int // the current mb accessing other domains' memory
	MBSourceRemoteLimit int // the upper limit outgoing remote MB target (applicable if the other domain has high QoS traffic)
}

type Sourcer interface {
	// AttributeMBToSources to source-attribute the quota of each domain
	// e.g., for below scenario
	//     domain 0: target 50, source-local 75, source-remote 25
	//     domain 1: target 60, source-local 60, source-remote 30
	// we would like to decide what the quota set to domain 0 and domain 1, so that
	// the desired target would be end up with (50, 60)
	AttributeIncomingMBToSources(domainTargets []DomainMBTargetSource) []int
}

func New(sourcerType string) Sourcer {
	general.Infof("mbm: mb sourcer type: %s", sourcerType)
	switch sourcerType {
	case "silo":
		return &SiloSourcer{}
	case "category":
		return NewCategorySourcer()
	case "crbs":
		return NewCategoryRemoteBoundSourcer()
	case "majorfactor":
		return NewMajorfactorSourcer()
	case "adaptive-crbs":
		return newFeedbackSourcer(NewCategoryRemoteBoundSourcer())
	case "adaptive-category":
		return newFeedbackSourcer(NewCategorySourcer())
	default:
		panic("not supported sourcer type")
	}
}
