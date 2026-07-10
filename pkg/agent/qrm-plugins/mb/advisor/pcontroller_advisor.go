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
	"sync"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// pControllerAdvisor is an advisor which restricts given groups' ccd mb to specified upper bound
// based on P-Controler (Proportional Controller) of closed-looped automation system to compare output feedback
// against a target value and adjust inputs in predefined ratio Kp to achieve a desired state
type pControllerAdvisor struct {
	ccdMinMB, ccdMaxMB int
	inner              Advisor

	groupStates         map[string]*groupPCtrlState
	ccdLimitSuppression map[int]map[string]map[int]string
	mu                  sync.RWMutex
}

func (p *pControllerAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	result, err := p.inner.GetPlan(ctx, domainsMon)
	if err != nil {
		return nil, err
	}

	for group, state := range p.groupStates {
		p.restrictGroupCCDCap(group, state, domainsMon, result)
	}

	// update suppression status
	suppression := computeCCDLimitSuppression(domainsMon, p.groupStates, p.ccdMaxMB)
	p.accumulateCCDLimitSuppression(suppression)

	return result, nil
}

// fetchLastSuppressedCCDs returns the accumulated suppression snapshot and clears it for the next collection.
func (p *pControllerAdvisor) fetchLastSuppressedCCDs() map[int]map[string]map[int]string {
	p.mu.Lock()
	defer p.mu.Unlock()

	lastSuppression := p.ccdLimitSuppression
	p.ccdLimitSuppression = nil
	return lastSuppression
}

// GetSuppressedCCDs returns CCDs suppressed by the P-controller limit together with inner advisor suppressions.
func (p *pControllerAdvisor) GetSuppressedCCDs() []SuppressedCCD {
	innerSuppressed := p.inner.GetSuppressedCCDs()
	lastSuppression := p.fetchLastSuppressedCCDs()
	result := buildSuppressedCCDs(lastSuppression, innerSuppressed,
		len(lastSuppression)+len(innerSuppressed),
		// len(lastSuppression) is a lower bound (top-level domain count in 3-level nested map);
		// computing the exact count requires a full traversal which defeats the purpose of the hint
	)

	return result
}

// accumulateCCDLimitSuppression merges the latest CCD limit suppressions into the advisor state.
func (p *pControllerAdvisor) accumulateCCDLimitSuppression(suppression map[int]map[string]map[int]string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for domID, groupCCDTypes := range suppression {
		for group, ccdTypes := range groupCCDTypes {
			for ccdID, suppressionType := range ccdTypes {
				addQuadruplet(&p.ccdLimitSuppression, domID, group, ccdID, suppressionType)
			}
		}
	}
}

// restrictGroupCCDCap updates a group's CCD cap from observed traffic and applies it to the generated plan.
func (p *pControllerAdvisor) restrictGroupCCDCap(group string, groupState *groupPCtrlState,
	domainsMon *monitor.DomainStats, plan *plan.MBPlan,
) {
	maxObservedMB := p.maxObservedCCDMBForGroup(domainsMon.Outgoings, group)
	groupState.updateCCDCap(p.getGroupCapUpdate(groupState, maxObservedMB))

	if klog.V(6).Enabled() {
		general.Infof("[mbm] [pController] group=%s maxObserved=%d target=%d cap=%d",
			group, maxObservedMB, groupState.pCtrl.target, groupState.ccdCapMB)
	}

	ccdMBs, ok := plan.MBGroups[group]
	if !ok {
		// fine to have no plan for the group
		return
	}
	applyGroupCCDBoundsChecks(ccdMBs, p.ccdMinMB, groupState.ccdCapMB)
}

// getGroupCapUpdate calculates the next CCD cap for a group using its controller output and global bounds.
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

// computeCCDLimitSuppression records CCDs whose usage remains above target while their cap is constrained.
func computeCCDLimitSuppression(domainsMon *monitor.DomainStats, groupStates map[string]*groupPCtrlState, ccdMaxMB int) map[int]map[string]map[int]string {
	var result map[int]map[string]map[int]string

	for group, state := range groupStates {
		target := state.pCtrl.target
		if target <= 0 || state.ccdCapMB >= ccdMaxMB {
			continue
		}

		for domID, domStats := range domainsMon.Outgoings {
			ccdStats, ok := domStats[group]
			if !ok {
				continue
			}
			for ccd, mbInfo := range ccdStats {
				if mbInfo.TotalMB >= target {
					addQuadruplet(&result, domID, group, ccd, suppressionTypeCCDLimit)
				}
			}
		}
	}

	return result
}

// applyGroupCCDBoundsChecks clamps every CCD MB value in a group plan to the provided bounds.
func applyGroupCCDBoundsChecks(ccdMBs plan.GroupCCDPlan, lower, upper int) {
	for ccd, mb := range ccdMBs {
		ccdMBs[ccd] = clampMB(mb, lower, upper)
	}
}

// clampMB restricts an MB value to the configured lower and upper bounds when they are enabled.
func clampMB(value, min, max int) int {
	// caller ensures min <= max
	if min > 0 && value < min {
		return min
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// NewPControllerAdvisor creates an advisor that bounds selected groups using proportional control feedback.
func NewPControllerAdvisor(Kp float64,
	minValue, maxValue int,
	groupTargets map[string]int,
	inner Advisor,
) Advisor {
	groupStates := make(map[string]*groupPCtrlState, len(groupTargets))
	for group, target := range groupTargets {
		groupStates[group] = newGroupPCtrlState(Kp, target, maxValue)
	}

	return &pControllerAdvisor{
		ccdMinMB:    minValue,
		ccdMaxMB:    maxValue,
		inner:       inner,
		groupStates: groupStates,
	}
}

type pController struct {
	kp     float64
	target int
}

// update returns the proportional control delta for the given measurement.
func (p *pController) update(measurement int) int {
	gap := float64(p.target - measurement)
	return int(p.kp * gap)
}
