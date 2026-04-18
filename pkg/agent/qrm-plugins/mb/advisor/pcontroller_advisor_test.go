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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func TestGetPlan_CapAppliedToPlan(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
					1: monitor.MBInfo{TotalMB: 30000},
				},
			},
		},
	}

	newCap := p.getCapUpdate(mon)

	innerPlan := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"shared-50": {0: 40000, 1: 40000},
		},
	}

	result := applyPlanCCDBoundsChecks(innerPlan, p.ccdMinMB, newCap)

	for group, ccdMBs := range result.MBGroups {
		for ccd, mb := range ccdMBs {
			if mb > newCap {
				t.Errorf("group=%s ccd=%d mb=%d exceeds newCap=%d", group, ccd, mb, newCap)
			}
		}
	}

	if newCap >= p.ccdMaxMB {
		t.Errorf("newCap should be lowered when observed exceeds target, got %d", newCap)
	}
}

func TestGetPlan_CapDoesNotGoBelowMin(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 100000},
				},
			},
		},
	}

	newCap := p.getCapUpdate(mon)

	innerPlan := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"shared-50": {0: 40000},
		},
	}

	result := applyPlanCCDBoundsChecks(innerPlan, p.ccdMinMB, newCap)

	if result.MBGroups["shared-50"][0] < p.ccdMinMB {
		t.Errorf("CCD MB should not go below ccdMinMB=%d, got %d", p.ccdMinMB, result.MBGroups["shared-50"][0])
	}
}

func TestGetPlan_CapRaisesWhenObservedBelowTarget(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       16000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 15000},
				},
			},
		},
	}

	newCap := p.getCapUpdate(mon)

	if newCap <= 16000 {
		t.Errorf("newCap should increase when observed < target, got %d", newCap)
	}
}

func TestGetPlan_NoObservationKeepsCap(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       30000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{},
	}

	newCap := p.getCapUpdate(mon)

	if newCap != 30000 {
		t.Errorf("newCap should stay unchanged when no observation, got %d", newCap)
	}
}

func TestGetPlan_MultipleGroupsAllCapped(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
				},
				"shared-70": {
					0: monitor.MBInfo{TotalMB: 30000},
				},
			},
		},
	}

	newCap := p.getCapUpdate(mon)

	innerPlan := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"shared-50": {0: 40000, 1: 40000},
			"shared-70": {0: 40000, 1: 40000},
		},
	}

	result := applyPlanCCDBoundsChecks(innerPlan, p.ccdMinMB, newCap)

	for group, ccdMBs := range result.MBGroups {
		for ccd, mb := range ccdMBs {
			if mb > newCap {
				t.Errorf("group=%s ccd=%d mb=%d exceeds newCap=%d", group, ccd, mb, newCap)
			}
		}
	}
}

func TestGetPlan_CapLowersAcrossMultipleRounds(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
				},
			},
		},
	}

	prevCap := p.ccdCapMB
	for i := 0; i < 5; i++ {
		p.ccdCapMB = p.getCapUpdate(mon)
		if p.ccdCapMB > prevCap {
			t.Errorf("round %d: cap should decrease when observed > target, got %d > %d", i, p.ccdCapMB, prevCap)
		}
		prevCap = p.ccdCapMB
	}

	if p.ccdCapMB >= 40000 {
		t.Errorf("cap should have decreased from initial value after 5 rounds, got %d", p.ccdCapMB)
	}
}

func TestGetPlan_ObservedEqualsTargetCapStays(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB:       4000,
		ccdMaxMB:       40000,
		inner:          &domainAdvisor{},
		ccdMaxTargetMB: 24000,
		ccdCapMB:       40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 24000},
				},
			},
		},
	}

	newCap := p.getCapUpdate(mon)

	if newCap != 40000 {
		t.Errorf("when observed == target, cap should stay at ccdMaxMB, got %d", newCap)
	}
}

func TestPController_LowersCapWhenObservedExceedsTarget(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
					1: monitor.MBInfo{TotalMB: 30000},
				},
			},
		},
	}

	newValue := p.getCapUpdate(mon)

	ctrlErr := 24000 - 35000
	delta := int(0.5 * float64(ctrlErr))
	expected := 40000 + delta

	if newValue != expected {
		t.Errorf("newValue = %d, want %d (err=%d, delta=%d)", newValue, expected, ctrlErr, delta)
	}

	if newValue >= p.ccdMaxMB {
		t.Errorf("newValue should be lowered when observed exceeds target, got %d", newValue)
	}
}

func TestPController_RaisesCapWhenObservedBelowTarget(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 20000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 18000},
					1: monitor.MBInfo{TotalMB: 15000},
				},
			},
		},
	}

	newValue := p.getCapUpdate(mon)

	ctrlErr := 24000 - 18000
	delta := int(0.5 * float64(ctrlErr))
	expected := 20000 + delta

	if newValue != expected {
		t.Errorf("newValue = %d, want %d", newValue, expected)
	}

	if newValue <= 20000 {
		t.Errorf("newValue should be raised when observed is below target, got %d", newValue)
	}
}

func TestPController_ClampsToLowerBound(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 100000},
				},
			},
		},
	}

	newValue := p.getCapUpdate(mon)

	if newValue < p.ccdMinMB {
		t.Errorf("newValue should not go below ccdMinMB=%d, got %d", p.ccdMinMB, newValue)
	}
}

func TestPController_ClampsToUpperBound(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 0},
				},
			},
		},
	}

	newValue := p.getCapUpdate(mon)

	if newValue > p.ccdMaxMB {
		t.Errorf("newValue should not exceed ccdMaxMB=%d, got %d", p.ccdMaxMB, newValue)
	}
}

func TestPController_NoUpdateWhenNoObservation(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{},
	}

	newValue := p.getCapUpdate(mon)

	if newValue != 40000 {
		t.Errorf("newValue should stay unchanged when no observation, got %d", newValue)
	}
}

func TestPController_MaxObservedPicksMaxAcrossCCDs(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		inner: &domainAdvisor{},
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
					1: monitor.MBInfo{TotalMB: 10000},
					2: monitor.MBInfo{TotalMB: 5000},
				},
			},
		},
	}

	maxObs := p.maxObservedCCDMB(mon.Outgoings)
	if maxObs != 35000 {
		t.Errorf("maxObservedCCDMB = %d, want 35000 (max across CCDs)", maxObs)
	}
}

func TestPController_DefaultKp(t *testing.T) {
	t.Parallel()

	inner := &domainAdvisor{}
	p := NewPControllerAdvisor(0, 24000, 4000, 40000, inner)
	pctrl := p.(*pControllerAdvisor)

	if pctrl.pCtrl.kp != 0.5 {
		t.Errorf("default Kp should be 0.5 when 0 is provided, got %f", pctrl.pCtrl.kp)
	}
}

func TestPController_Convergence(t *testing.T) {
	t.Parallel()

	p := &pControllerAdvisor{
		pCtrl: pController{
			kp:     0.5,
			target: 24000,
		},
		ccdMinMB: 4000,
		ccdMaxMB: 40000,
		inner:    &domainAdvisor{},
		ccdCapMB: 40000,
	}

	mon := &monitor.DomainStats{
		Outgoings: map[int]monitor.GroupMBStats{
			0: {
				"shared-50": {
					0: monitor.MBInfo{TotalMB: 35000},
				},
			},
		},
	}

	for i := 0; i < 10; i++ {
		p.ccdCapMB = p.getCapUpdate(mon)
	}

	if p.ccdCapMB > p.ccdMinMB && p.ccdCapMB < 40000 {
		t.Logf("after 10 rounds, ccdCapMB=%d (converging toward target)", p.ccdCapMB)
	}

	if p.ccdCapMB < p.ccdMinMB {
		t.Errorf("ccdCapMB should not go below ccdMinMB, got %d", p.ccdCapMB)
	}
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
		{"negative error (observed > target)", 0.5, 24000, 35000, -5500},
		{"positive error (observed < target)", 0.5, 24000, 18000, 3000},
		{"zero error (observed == target)", 0.5, 24000, 24000, 0},
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
