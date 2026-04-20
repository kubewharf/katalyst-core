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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func TestGetPlan_PerGroupCapApplied(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl:    pController{kp: 0.5, target: 20000},
				ccdCapMB: 40000,
			},
			"shared-50": {
				pCtrl:    pController{kp: 0.5, target: 24000},
				ccdCapMB: 40000,
			},
		},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"dedicated": {
					0: monitor.MBInfo{TotalMB: 35000},
					1: monitor.MBInfo{TotalMB: 30000},
				},
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 30000},
					1: monitor.MBInfo{TotalMB: 25000},
				},
			},
		},
	}

	result := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"dedicated": {0: 40000, 1: 40000},
			"shared-50": {0: 40000, 1: 40000},
			"shared-70": {0: 40000, 1: 40000},
		},
	}
	for group, state := range p.groupStates {
		maxObservedMB := p.maxObservedCCDMBForGroup(mon.Outgoings, group)
		state.ccdCapMB = p.getGroupCapUpdate(state, maxObservedMB)
		ccdMBs, ok := result.MBGroups[group]
		if !ok {
			continue
		}
		applyGroupCCDBoundsChecks(ccdMBs, p.ccdMinMB, state.ccdCapMB)
	}

	dedicatedCap := p.groupStates["dedicated"].ccdCapMB
	for ccd, mb := range result.MBGroups["dedicated"] {
		if mb > dedicatedCap {
			t.Errorf("dedicated ccd=%d mb=%d exceeds cap %d", ccd, mb, dedicatedCap)
		}
	}

	shared50Cap := p.groupStates["shared-50"].ccdCapMB
	for ccd, mb := range result.MBGroups["shared-50"] {
		if mb > shared50Cap {
			t.Errorf("shared-50 ccd=%d mb=%d exceeds cap %d", ccd, mb, shared50Cap)
		}
	}

	if result.MBGroups["shared-70"][0] != 40000 {
		t.Errorf("shared-70 (not in groupStates) should be unchanged, got %d", result.MBGroups["shared-70"][0])
	}
}

func TestGetPlan_PerGroupIndependentConvergence(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl:    pController{kp: 0.5, target: 20000},
				ccdCapMB: 40000,
			},
			"shared": {
				pCtrl:    pController{kp: 0.5, target: 24000},
				ccdCapMB: 40000,
			},
		},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"dedicated": {
					0: monitor.MBInfo{TotalMB: 35000},
				},
				"shared": {
					0: monitor.MBInfo{TotalMB: 25000},
				},
			},
		},
	}

	prevDedicatedCap := p.groupStates["dedicated"].ccdCapMB
	prevSharedCap := p.groupStates["shared"].ccdCapMB

	for i := 0; i < 5; i++ {
		for group, state := range p.groupStates {
			maxObservedMB := p.maxObservedCCDMBForGroup(mon.Outgoings, group)
			state.ccdCapMB = p.getGroupCapUpdate(state, maxObservedMB)
		}
	}

	if p.groupStates["dedicated"].ccdCapMB >= prevDedicatedCap {
		t.Errorf("dedicated cap should decrease (observed=35000 > target=20000), got %d", p.groupStates["dedicated"].ccdCapMB)
	}
	if p.groupStates["shared"].ccdCapMB >= prevSharedCap {
		t.Errorf("shared cap should decrease (observed=25000 > target=24000), got %d", p.groupStates["shared"].ccdCapMB)
	}
}

func TestGetPlan_PerGroupCapRaisesWhenBelowTarget(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl:    pController{kp: 0.5, target: 20000},
				ccdCapMB: 16000,
			},
		},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"dedicated": {
					0: monitor.MBInfo{TotalMB: 15000},
				},
			},
		},
	}

	state := p.groupStates["dedicated"]
	maxObservedMB := p.maxObservedCCDMBForGroup(mon.Outgoings, "dedicated")
	newCap := p.getGroupCapUpdate(state, maxObservedMB)

	if newCap <= 16000 {
		t.Errorf("cap should increase when observed(15000) < target(20000), got %d", newCap)
	}
}

func TestGetPlan_PerGroupNoObservationKeepsCap(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl:    pController{kp: 0.5, target: 20000},
				ccdCapMB: 30000,
			},
		},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{},
	}

	state := p.groupStates["dedicated"]
	newCap := p.getGroupCapUpdate(state, p.maxObservedCCDMBForGroup(mon.Outgoings, "dedicated"))

	if newCap != 30000 {
		t.Errorf("cap should stay unchanged when no observation, got %d", newCap)
	}
}

func TestGetPlan_PerGroupClampToLowerBound(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl:    pController{kp: 0.5, target: 20000},
				ccdCapMB: 40000,
			},
		},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"dedicated": {
					0: monitor.MBInfo{TotalMB: 100000},
				},
			},
		},
	}

	state := p.groupStates["dedicated"]
	maxObservedMB := p.maxObservedCCDMBForGroup(mon.Outgoings, "dedicated")
	newCap := p.getGroupCapUpdate(state, maxObservedMB)

	if newCap < p.ccdMinMB {
		t.Errorf("cap should not go below ccdMinMB=%d, got %d", p.ccdMinMB, newCap)
	}
}

func TestMaxObservedCCDMBForGroup(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{}

	outgoings := map[int]monitor.GroupMBStats{
		0: {
			"/": {
				0: monitor.MBInfo{TotalMB: 10000},
				1: monitor.MBInfo{TotalMB: 12000},
			},
			"dedicated": {
				0: monitor.MBInfo{TotalMB: 35000},
				1: monitor.MBInfo{TotalMB: 8000},
			},
			"shared-50": {
				0: monitor.MBInfo{TotalMB: 5000},
				1: monitor.MBInfo{TotalMB: 6000},
			},
		},
	}

	tests := []struct {
		group string
		want  int
	}{
		{"dedicated", 35000},
		{"shared-50", 6000},
		{"/", 12000},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		got := p.maxObservedCCDMBForGroup(outgoings, tt.group)
		if got != tt.want {
			t.Errorf("maxObservedCCDMBForGroup(%q) = %d, want %d", tt.group, got, tt.want)
		}
	}
}

func TestApplyGroupCCDBoundsChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    plan.GroupCCDPlan
		minMB    int
		maxMB    int
		expected plan.GroupCCDPlan
	}{
		{
			"within range",
			map[int]int{0: 15000, 1: 20000},
			1000, 30000,
			map[int]int{0: 15000, 1: 20000},
		},
		{
			"below min",
			map[int]int{0: 500, 1: 20000},
			1000, 30000,
			map[int]int{0: 1000, 1: 20000},
		},
		{
			"above max",
			map[int]int{0: 15000, 1: 40000},
			1000, 30000,
			map[int]int{0: 15000, 1: 30000},
		},
		{
			"both bounds",
			map[int]int{0: 500, 1: 40000},
			1000, 30000,
			map[int]int{0: 1000, 1: 30000},
		},
		{
			"min is 0",
			map[int]int{0: 500, 1: 40000},
			0, 30000,
			map[int]int{0: 500, 1: 30000},
		},
		{
			"max is 0",
			map[int]int{0: 500, 1: 40000},
			1000, 0,
			map[int]int{0: 1000, 1: 40000},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputCopy := make(map[int]int, len(tt.input))
			for k, v := range tt.input {
				inputCopy[k] = v
			}
			applyGroupCCDBoundsChecks(inputCopy, tt.minMB, tt.maxMB)
			for k, v := range inputCopy {
				if v != tt.expected[k] {
					t.Errorf("ccd %d = %d, want %d", k, v, tt.expected[k])
				}
			}
		})
	}
}

func TestNewPControllerAdvisor_PerGroup(t *testing.T) {
	t.Parallel()

	inner := &domainAdvisor{}
	groupTargets := map[string]int{
		"dedicated": 20000,
		"shared-50": 24000,
	}

	p := NewPControllerAdvisor(0.3, 4000, 40000, groupTargets, inner)
	pctrl := p.(*pControllerAdvisor)

	if len(pctrl.groupStates) != 2 {
		t.Fatalf("expected 2 group states, got %d", len(pctrl.groupStates))
	}

	if pctrl.groupStates["dedicated"].pCtrl.target != 20000 {
		t.Errorf("dedicated target = %d, want 20000", pctrl.groupStates["dedicated"].pCtrl.target)
	}
	if pctrl.groupStates["shared-50"].pCtrl.target != 24000 {
		t.Errorf("shared-50 target = %d, want 24000", pctrl.groupStates["shared-50"].pCtrl.target)
	}
	if pctrl.groupStates["dedicated"].pCtrl.kp != 0.3 {
		t.Errorf("Kp = %f, want 0.3", pctrl.groupStates["dedicated"].pCtrl.kp)
	}
	if pctrl.groupStates["dedicated"].ccdCapMB != 40000 {
		t.Errorf("initial cap should be maxValue, got %d", pctrl.groupStates["dedicated"].ccdCapMB)
	}
}

func TestNewPControllerAdvisor_DefaultKp(t *testing.T) {
	t.Parallel()

	inner := &domainAdvisor{}
	p := NewPControllerAdvisor(0, 4000, 40000, map[string]int{"g": 24000}, inner)
	pctrl := p.(*pControllerAdvisor)

	if pctrl.groupStates["g"].pCtrl.kp != 0.5 {
		t.Errorf("default Kp should be 0.5, got %f", pctrl.groupStates["g"].pCtrl.kp)
	}
}

func TestNewPControllerAdvisor_ReturnsInnerWhenNotDomainAdvisor(t *testing.T) {
	t.Parallel()

	inner := &stubAdvisor{}
	result := NewPControllerAdvisor(0.5, 4000, 40000, nil, inner)

	if result != inner {
		t.Error("should return inner when it's not a domainAdvisor")
	}
}

type stubAdvisor struct{}

func (s *stubAdvisor) GetPlan(_ context.Context, _ *monitor.DomainStats) (*plan.MBPlan, error) {
	return nil, nil
}

func TestPController_Update(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		kp          float64
		target      int
		measurement int
		want        int
	}{
		{"negative error", 0.5, 24000, 35000, -5500},
		{"positive error", 0.5, 24000, 18000, 3000},
		{"zero error", 0.5, 24000, 24000, 0},
		{"Kp=1.0", 1.0, 24000, 35000, -11000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &pController{kp: tt.kp, target: tt.target}
			if got := p.update(tt.measurement); got != tt.want {
				t.Errorf("pController.update() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClampMB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value int
		min   int
		max   int
		want  int
	}{
		{"within range", 500, 100, 1000, 500},
		{"below min", 50, 100, 1000, 100},
		{"above max", 5000, 100, 1000, 1000},
		{"at min", 100, 100, 1000, 100},
		{"at max", 1000, 100, 1000, 1000},
		{"min is 0", 50, 0, 1000, 50},
		{"max is 0", 5000, 100, 0, 5000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := clampMB(tt.value, tt.min, tt.max); got != tt.want {
				t.Errorf("clampMB() = %d, want %d", got, tt.want)
			}
		})
	}
}
