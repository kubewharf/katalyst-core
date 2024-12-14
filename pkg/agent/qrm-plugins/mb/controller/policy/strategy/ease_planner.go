package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type fullEasePlanner struct {
	ccdGroupPlanner *CCDGroupPlanner
}

func (t fullEasePlanner) Name() string {
	return "full ease planner"
}

func (t fullEasePlanner) GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	allocatable := capacity - easeThreshold
	if allocatable <= 0 {
		return nil
	}

	// distribute total among all proportionally
	totalUsage := monitor.SumMB(mbQoSGroups)
	ratio := float64(allocatable) / float64(totalUsage)
	return t.ccdGroupPlanner.getProportionalPlan(ratio, mbQoSGroups)
}

func newFullEasePlanner(planner *CCDGroupPlanner) LowPrioPlanner {
	return &fullEasePlanner{
		ccdGroupPlanner: planner,
	}
}

type halfEasePlanner struct {
	innerPlanner fullEasePlanner
}

func (s halfEasePlanner) Name() string {
	return "half ease planner"
}

func (s halfEasePlanner) GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	totalUsage := monitor.SumMB(mbQoSGroups)
	// step ease planner eases 1/2 newly allocatable only at ease step
	constraintCapacity := (capacity + easeThreshold + totalUsage) / 2
	return s.innerPlanner.GetPlan(constraintCapacity, mbQoSGroups)
}

func newHalfEasePlanner(planner *CCDGroupPlanner) LowPrioPlanner {
	return &halfEasePlanner{
		innerPlanner: fullEasePlanner{ccdGroupPlanner: planner},
	}
}
