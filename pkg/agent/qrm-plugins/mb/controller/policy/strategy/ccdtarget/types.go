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

type CCDMBPlannerType string

const (
	LinearCCDMBPlanner      = CCDMBPlannerType("linear-ccd-planner")
	LogarithmicScalePlanner = CCDMBPlannerType("logarithm-ccd-planner")
)

func New(plannerType CCDMBPlannerType, min, max int) CCDMBPlanner {
	switch plannerType {
	case LinearCCDMBPlanner:
		return newCCDGroupPlanner(min, max)
	case LogarithmicScalePlanner:
		return newLogarithmicScalePlanner(min, max)
	default:
		panic("unrecognized ccd planner type")
	}
}
