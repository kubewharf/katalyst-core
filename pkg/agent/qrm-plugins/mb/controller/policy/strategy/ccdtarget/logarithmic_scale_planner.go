package ccdtarget

import (
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

type logarithmicScalePlanner struct {
	linearPlanner CCDMBPlanner
}

func (l *logarithmicScalePlanner) GetPlan(total int, ccdMB map[int]*stat.MBData) map[int]int {
	weights := getWeightedCCDMB(ccdMB)
	return l.linearPlanner.GetPlan(total, weights)
}

func getWeightedCCDMB(ccdMB map[int]*stat.MBData) map[int]*stat.MBData {
	clone := syntax.DeepCopy(ccdMB).(map[int]*stat.MBData)
	for ccd, mb := range ccdMB {
		if mb.TotalMB == 0 {
			clone[ccd].TotalMB = 0
			continue
		}
		clone[ccd].TotalMB = int(math.Log2(float64(mb.TotalMB)))
	}
	return clone
}

func (l *logarithmicScalePlanner) GetFixedPlan(fixed int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	return getFixedPlan(fixed, mbQoSGroups)
}

func NewLogarithmicScalePlanner(min, max int) CCDMBPlanner {
	return &logarithmicScalePlanner{
		linearPlanner: NewCCDGroupPlanner(min, max),
	}
}
