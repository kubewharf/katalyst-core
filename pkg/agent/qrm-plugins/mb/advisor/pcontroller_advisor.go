/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package advisor

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	defaultCCDLimitKp = 0.5
)

type pControllerAdvisor struct {
	pCtrl              pController
	ccdMinMB, ccdMaxMB int

	inner *domainAdvisor

	// ccdMaxTargetMB is the targeted max of mb usages of all resctrl groups in any of their ccds
	ccdMaxTargetMB int
	// ccdCapMB is the effective cap ensuring the max of ccd usages around ccdMaxTargetMB
	ccdCapMB int
}

func (p *pControllerAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	result, err := p.inner.GetPlan(ctx, domainsMon)
	if err != nil {
		return nil, err
	}

	// recalibrate ccd cap
	// we may need defer cap adjustment a bit for resctrl to stabilize itself
	p.ccdCapMB = p.getCapUpdate(domainsMon)
	// apply new cap to ccd mb plan
	result = applyPlanCCDBoundsChecks(result, p.ccdMinMB, p.ccdCapMB)

	return result, nil
}

func (p *pControllerAdvisor) getCapUpdate(mon *monitor.DomainStats) int {
	maxObservedMB := p.maxObservedCCDMB(mon.Outgoings)
	if maxObservedMB == 0 {
		return p.ccdCapMB
	}

	delta := p.pCtrl.update(maxObservedMB)
	proposedCCDUpdate := p.ccdCapMB + delta
	proposedCCDUpdate = clampMB(proposedCCDUpdate, p.ccdMinMB, p.ccdMaxMB)

	if klog.V(6).Enabled() {
		general.InfofV(6, "[mbm] [pctrl] maxObserved=%d, cap delta=%d, capMB: %d -> %d",
			maxObservedMB, delta, p.ccdCapMB, proposedCCDUpdate)
	}

	return proposedCCDUpdate
}

func (p *pControllerAdvisor) maxObservedCCDMB(outgoings map[int]monitor.GroupMBStats) int {
	max := 0
	for _, groupMBStats := range outgoings {
		for _, groupMB := range groupMBStats {
			for _, mb := range groupMB {
				if max < mb.TotalMB {
					max = mb.TotalMB
				}
			}
		}
	}
	return max
}

func clampMB(value, min, max int) int {
	if min > 0 && value < min {
		return min
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

func NewPControllerAdvisor(Kp float64,
	target, minValue, maxValue int,
	inner Advisor,
) Advisor {
	if Kp <= 0 {
		Kp = defaultCCDLimitKp
	}

	return &pControllerAdvisor{
		pCtrl: pController{
			kp:     Kp,
			target: target,
		},
		inner:          inner.(*domainAdvisor),
		ccdMinMB:       minValue,
		ccdMaxMB:       maxValue,
		ccdMaxTargetMB: target,
		ccdCapMB:       maxValue, // initial cap is the upper bound, in line with inner advisor
	}
}

type pController struct {
	kp     float64
	target int
}

func (p *pController) update(measurement int) int {
	gap := float64(p.target - measurement)
	return int(p.kp * gap)
}
