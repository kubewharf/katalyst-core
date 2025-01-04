package ccdtarget

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

type CCDMBPlanner interface {
	GetPlan(target int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc
	GetFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc
}

type CCDGroupPlanner struct {
	CCDMBMin, ccdMBMax int
}

func (c *CCDGroupPlanner) GetPlan(target int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	plan := &plan.MBAlloc{
		Plan: make(map[qosgroup.QoSGroup]map[int]int),
	}

	totalUsed := stat.SumMB(mbQoSGroups)
	for qos, mbGroup := range mbQoSGroups {
		used := stat.SumCCDMB(mbGroup.CCDMB)
		groupTarget := target * used / totalUsed
		plan.Plan[qos] = c.getCCDMBPlan(groupTarget, mbGroup.CCDMB)
	}

	return plan
}

func (c *CCDGroupPlanner) getCCDMBPlan(target int, ccdMB map[int]*stat.MBData) map[int]int {
	used := stat.SumCCDMB(ccdMB)
	ratio := float64(target) / float64(used)
	return c.getProportionalPlan(ratio, ccdMB)
}

func (c *CCDGroupPlanner) getProportionalPlan(ratio float64, ccdMB map[int]*stat.MBData) map[int]int {
	return c.getProportionalPlanWithUpperLimit(ratio, ccdMB, c.ccdMBMax)
}

func (c *CCDGroupPlanner) getProportionalPlanWithUpperLimit(ratio float64, ccdMB map[int]*stat.MBData, upperBound int) map[int]int {
	distributions := make(map[int]int)
	for ccd, mb := range ccdMB {
		newMB := int(ratio * float64(mb.TotalMB))
		if newMB > upperBound {
			newMB = upperBound
		}
		if newMB < c.CCDMBMin {
			newMB = c.CCDMBMin
		}
		distributions[ccd] = newMB
	}
	return distributions
}

func (c *CCDGroupPlanner) GetFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	return getFixedPlan(fixed, mbQoSGroups)
}

func getFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	mbPlan := &plan.MBAlloc{Plan: make(map[qosgroup.QoSGroup]map[int]int)}
	for qos, group := range mbQoSGroups {
		mbPlan.Plan[qos] = make(map[int]int)
		for ccd, _ := range group.CCDs {
			mbPlan.Plan[qos][ccd] = fixed
		}
	}
	return mbPlan
}

func NewCCDGroupPlanner(min, max int) CCDMBPlanner {
	return &CCDGroupPlanner{
		CCDMBMin: min,
		ccdMBMax: max,
	}
}
