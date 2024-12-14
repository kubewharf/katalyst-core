package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type LowPrioPlanner interface {
	GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc
	Name() string
}

type LowPrioPlannerType string

const (
	ExtremeThrottle = LowPrioPlannerType("extreme-throttle")
	HalfThrottle    = LowPrioPlannerType("half-throttle")
	FullEase        = LowPrioPlannerType("full-ease")
	HalfEase        = LowPrioPlannerType("half-ease")
)

func New(typ LowPrioPlannerType, ccdPlanner *CCDGroupPlanner) LowPrioPlanner {
	switch typ {
	case ExtremeThrottle:
		return newExtremeThrottlePlanner(ccdPlanner)
	case HalfThrottle:
		return newHalfThrottlePlanner(ccdPlanner)
	case FullEase:
		return newFullEasePlanner(ccdPlanner)
	case HalfEase:
		return newHalfEasePlanner(ccdPlanner)
	default:
		panic("not implemented yet")
	}
}
