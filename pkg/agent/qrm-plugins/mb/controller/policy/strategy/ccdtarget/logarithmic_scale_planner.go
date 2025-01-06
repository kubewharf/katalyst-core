package ccdtarget

import (
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor/stat"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

type logarithmicScalePlanner struct {
	linearPlanner CCDMBPlanner
}

func (l *logarithmicScalePlanner) GetPlan(target int, mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) *plan.MBAlloc {
	weights := logarithmicScale(mbQoSGroups)
	return l.linearPlanner.GetPlan(target, weights)
}

func logarithmicScale(mbQoSGroups map[qosgroup.QoSGroup]*stat.MBQoSGroup) map[qosgroup.QoSGroup]*stat.MBQoSGroup {
	result := make(map[qosgroup.QoSGroup]*stat.MBQoSGroup)
	for qos, mbGroup := range mbQoSGroups {
		result[qos] = &stat.MBQoSGroup{
			CCDs:  mbGroup.CCDs,
			CCDMB: getLogarithmicScaledCCDMB(mbGroup.CCDMB),
		}
	}
	return result
}

func getLogarithmicScaledCCDMB(ccdMB map[int]*stat.MBData) map[int]*stat.MBData {
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

func newLogarithmicScalePlanner(min, max int) CCDMBPlanner {
	return &logarithmicScalePlanner{
		linearPlanner: newCCDGroupPlanner(min, max),
	}
}
