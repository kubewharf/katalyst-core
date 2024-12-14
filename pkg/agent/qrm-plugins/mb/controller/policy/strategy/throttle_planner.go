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

func (t extremeThrottlePlanner) GetPlan(capacity int, mbQoSGroups map[qosgroup.QoSGroup]*monitor.MBQoSGroup) *plan.MBAlloc {
	return t.ccdGroupPlanner.getFixedPlan(t.ccdGroupPlanner.ccdMBMin, mbQoSGroups)
}

func NewExtremeThrottlePlanner(ccdPlanner *CCDGroupPlanner) LowPrioPlanner {
	return &extremeThrottlePlanner{
		ccdGroupPlanner: ccdPlanner,
	}
}
