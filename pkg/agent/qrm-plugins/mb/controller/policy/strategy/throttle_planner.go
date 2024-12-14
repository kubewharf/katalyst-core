package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

// extremeThrottlePlanner implements the extreme throttling by generating the plan
// that set all groups to their min allocation
type extremeThrottlePlanner struct {
	ccdGroupPlanner *CCDGroupPlanner
}

func (e extremeThrottlePlanner) Name() string {
	return "extreme throttle planner"
}

func (e extremeThrottlePlanner) GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	return e.ccdGroupPlanner.getFixedPlan(e.ccdGroupPlanner.ccdMBMin, mbQoSGroups)
}

func NewExtremeThrottlePlanner(ccdPlanner *CCDGroupPlanner) LowPrioPlanner {
	return &extremeThrottlePlanner{
		ccdGroupPlanner: ccdPlanner,
	}
}

// halfThrottlePlanner forces qos groups to yield half of mb in use
type halfThrottlePlanner struct {
	ccdGroupPlanner *CCDGroupPlanner
}

func (h halfThrottlePlanner) Name() string {
	return "half throttle planner"
}

func (h halfThrottlePlanner) GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	totalUsage := monitor.SumMB(mbQoSGroups)

	allocatable := totalUsage / 2
	// summarized low prio qos plans should  not exceeding the ease bar
	if allocatable > capacity-easeThreshold {
		allocatable = capacity - easeThreshold
	}

	// distribute total among all proportionally
	ratio := float64(allocatable) / float64(totalUsage)
	return h.ccdGroupPlanner.getProportionalPlanWithUpperLimit(ratio, mbQoSGroups, capacity-easeThreshold)
}

func NewHalfThrottlePlanner(ccdPlanner *CCDGroupPlanner) LowPrioPlanner {
	return &halfThrottlePlanner{
		ccdGroupPlanner: ccdPlanner,
	}
}
