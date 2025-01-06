package ccdtarget

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type CCDMBDistributor interface {
	GetPlan(target int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc
	GetFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc
}

type CCDMBDistributorType string

const (
	LinearCCDMBDistributor      = CCDMBDistributorType("linear-distributor")
	LogarithmicScaleDistributor = CCDMBDistributorType("logarithm-distributor")
)

func New(plannerType CCDMBDistributorType, min, max int) CCDMBDistributor {
	general.Infof("mbm: ccd mb distributor type %s", plannerType)
	switch plannerType {
	case LinearCCDMBDistributor:
		return newLinearDistributor(min, max)
	case LogarithmicScaleDistributor:
		return newLogarithmicScalePlanner(min, max)
	default:
		panic("unrecognized ccd planner type")
	}
}
