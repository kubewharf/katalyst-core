package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type CCDGroupPlanner struct {
	ccdMBMin, ccdMBMax int
}

func (c *CCDGroupPlanner) GetProportionalPlan(ratio float64, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	return c.getProportionalPlanWithUpperLimit(ratio, mbQoSGroups, c.ccdMBMax)
}

func (c *CCDGroupPlanner) getProportionalPlanWithUpperLimit(ratio float64, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup, high int) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[qosgroup.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, mb := range group.CCDMB {
			newMB := int(ratio * float64(mb.TotalMB))
			if newMB > high {
				newMB = high
			}
			if newMB < c.ccdMBMin {
				newMB = c.ccdMBMin
			}
			mbPlan.Plan[qos][ccd] = newMB
		}
	}
	return mbPlan
}

func (c *CCDGroupPlanner) GetFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[qosgroup.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, _ := range group.CCDs {
			mbPlan.Plan[qos][ccd] = fixed
		}
	}
	return mbPlan
}

func NewCCDGroupPlanner(min, max int) *CCDGroupPlanner {
	return &CCDGroupPlanner{
		ccdMBMin: min,
		ccdMBMax: max,
	}
}
