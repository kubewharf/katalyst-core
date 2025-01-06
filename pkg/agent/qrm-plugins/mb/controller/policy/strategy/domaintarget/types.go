package domaintarget

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy/ccdtarget"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type DomainMBAdjuster interface {
	GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc
	GetQuota(capacity, currentUsage int) int
	Name() string
}

type MBAdjusterType string

const (
	ExtremeThrottle = MBAdjusterType("extreme-throttle")
	HalfThrottle    = MBAdjusterType("half-throttle")
	FullEase        = MBAdjusterType("full-ease")
	HalfEase        = MBAdjusterType("half-ease")
	QuarterEase     = MBAdjusterType("quarter-ease")
)

func New(typ MBAdjusterType, ccdPlanner ccdtarget.CCDMBPlanner) DomainMBAdjuster {
	switch typ {
	case ExtremeThrottle:
		return newExtremeThrottlePlanner(ccdPlanner)
	case HalfThrottle:
		return newHalfThrottlePlanner(ccdPlanner)
	case FullEase:
		return newFullEasePlanner(ccdPlanner)
	case HalfEase:
		return newHalfEasePlanner(ccdPlanner)
	case QuarterEase:
		return newQuarterEasePlanner()
	default:
		panic("not implemented yet")
	}
}
