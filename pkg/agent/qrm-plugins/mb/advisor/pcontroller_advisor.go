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
	defaultCCDCapKp = 0.5
)

type pControllerAdvisor struct {
	ccdMinMB, ccdMaxMB int
	inner              *domainAdvisor

	groupStates map[string]*groupPCtrlState
}

type groupPCtrlState struct {
	pCtrl    pController
	ccdCapMB int
}

func (p *pControllerAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	result, err := p.inner.GetPlan(ctx, domainsMon)
	if err != nil {
		return nil, err
	}

	for group, state := range p.groupStates {
		maxObservedMB := p.maxObservedCCDMBForGroup(domainsMon.Outgoings, group)
		state.ccdCapMB = p.getGroupCapUpdate(state, maxObservedMB)

		ccdMBs, ok := result.MBGroups[group]
		if !ok {
			continue
		}
		applyGroupCCDBoundsChecks(ccdMBs, p.ccdMinMB, state.ccdCapMB)

		if klog.V(6).Enabled() {
			general.InfofV(6, "[mbm] [pctrl] group=%s maxObserved=%d target=%d cap=%d",
				group, maxObservedMB, state.pCtrl.target, state.ccdCapMB)
		}
	}

	return result, nil
}

func (p *pControllerAdvisor) getGroupCapUpdate(state *groupPCtrlState, maxObservedMB int) int {
	if maxObservedMB == 0 {
		return state.ccdCapMB
	}

	delta := state.pCtrl.update(maxObservedMB)
	newCap := state.ccdCapMB + delta
	return clampMB(newCap, p.ccdMinMB, p.ccdMaxMB)
}

func (p *pControllerAdvisor) maxObservedCCDMBForGroup(outgoings map[int]monitor.GroupMBStats, group string) int {
	max := 0
	for _, groupStats := range outgoings {
		ccdStats, ok := groupStats[group]
		if !ok {
			continue
		}
		for _, mbInfo := range ccdStats {
			if mbInfo.TotalMB > max {
				max = mbInfo.TotalMB
			}
		}
	}
	return max
}

func applyGroupCCDBoundsChecks(ccdMBs plan.GroupCCDPlan, minMB, maxMB int) {
	if minMB > 0 {
		for ccd, mb := range ccdMBs {
			if mb < minMB {
				ccdMBs[ccd] = minMB
			}
		}
	}
	if maxMB > 0 {
		for ccd, mb := range ccdMBs {
			if mb > maxMB {
				ccdMBs[ccd] = maxMB
			}
		}
	}
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
	minValue, maxValue int,
	groupTargets map[string]int,
	inner Advisor,
) Advisor {
	if Kp <= 0 {
		Kp = defaultCCDCapKp
	}

	domAdvisor, ok := inner.(*domainAdvisor)
	if !ok {
		return inner
	}

	groupStates := make(map[string]*groupPCtrlState, len(groupTargets))
	for group, target := range groupTargets {
		groupStates[group] = &groupPCtrlState{
			pCtrl: pController{
				kp:     Kp,
				target: target,
			},
			ccdCapMB: maxValue,
		}
	}

	return &pControllerAdvisor{
		ccdMinMB:    minValue,
		ccdMaxMB:    maxValue,
		inner:       domAdvisor,
		groupStates: groupStates,
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
