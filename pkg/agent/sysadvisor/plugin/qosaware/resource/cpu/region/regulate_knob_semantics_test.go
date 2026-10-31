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

package region

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

// newTestPolicyResult builds a provisionPolicyResult wired with a CPURegulator
// that has no HT alignment and generous bounds, so tests can isolate the
// first-cycle vs steady-state effectiveControlKnob semantics.
func newTestPolicyResult(msg string) *provisionPolicyResult {
	essentials := types.ResourceEssentials{
		ResourceLowerBound: 0,
		ResourceUpperBound: 1000,
	}
	opts := regulator.RegulatorOptions{
		MaxRampUpStep:     2,
		MaxRampDownStep:   2,
		MinRampDownPeriod: 0, // no time-based restriction in policyResult tests
		NeedHTAligned:     func() bool { return false },
		CPUsPerCore:       func() int { return 1 },
	}
	return newProvisionPolicyResult(essentials, opts, msg)
}

// knobValue reads the regulated value for the NonReclaimedCPURequirement knob as a float64.
func knobValue(t *testing.T, pr *provisionPolicyResult) float64 {
	t.Helper()
	out := pr.getControlKnob()
	item, ok := out[configapi.ControlKnobNonReclaimedCPURequirement]
	require.True(t, ok, "expected control knob in result")
	return item.Value
}

// TestProvisionPolicyResultFirstCycleNoEffectiveKnobVerifiesInput pins the
// first-cycle semantics: when a policy result is new (no prior regulated value)
// and effectiveControlKnob is nil, the regulator applies no ramp limiting and
// the output equals the clamped input. This is the "bootstrap" path.
func TestProvisionPolicyResultFirstCycleNoEffectiveKnobVerifiesInput(t *testing.T) {
	t.Parallel()

	pr := newTestPolicyResult("first-cycle")
	current := types.ControlKnob{
		configapi.ControlKnobNonReclaimedCPURequirement: {Value: 10},
	}
	pr.regulateControlKnob(current, nil)

	require.Equal(t, float64(10), knobValue(t, pr))
}

// TestProvisionPolicyResultSteadyStateEffectiveKnobLimitsRampUp verifies the
// steady-state semantics: once the regulator has a prior value (from a previous
// cycle's effectiveControlKnob), passing a new current value that jumps more
// than MaxRampUpStep gets clamped to the step limit. This is the load-bearing
// slowdown behavior that regulateProvisionControlKnob wires for the first valid
// policy every cycle.
func TestProvisionPolicyResultSteadyStateEffectiveKnobLimitsRampUp(t *testing.T) {
	t.Parallel()

	pr := newTestPolicyResult("steady-ramp-up")
	knob := configapi.ControlKnobNonReclaimedCPURequirement

	// Cycle 1: seed at 10 with effective=10.
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 10}},
		types.ControlKnob{knob: {Value: 10}},
	)
	require.Equal(t, float64(10), knobValue(t, pr))

	// Cycle 2: policy asks for 20, but MaxRampUpStep=2 and effective=10.
	// Slowdown must clamp to 10+2=12, not jump to 20.
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 20}},
		types.ControlKnob{knob: {Value: 10}},
	)
	require.Equal(t, float64(12), knobValue(t, pr),
		"steady-state effective knob must ramp-limit a 10-CPU jump to step=2")
}

// TestProvisionPolicyResultSteadyStateNilEffectiveKnobNoRampLimit verifies the
// secondary-policy semantics: non-first-valid policies get effectiveControlKnob=nil
// on steady-state cycles, so their regulator applies no ramp limiting and the
// output follows the raw policy value directly. This prevents a secondary policy
// from being throttled by the first policy's history.
func TestProvisionPolicyResultSteadyStateNilEffectiveKnobNoRampLimit(t *testing.T) {
	t.Parallel()

	pr := newTestPolicyResult("secondary-policy")
	knob := configapi.ControlKnobNonReclaimedCPURequirement

	// Cycle 1: seed at 10 (first cycle, effective may be non-nil or nil).
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 10}},
		types.ControlKnob{knob: {Value: 10}},
	)
	require.Equal(t, float64(10), knobValue(t, pr))

	// Cycle 2: this is a secondary policy, so regulateProvisionControlKnob
	// passes nil effectiveControlKnob. The regulator must NOT ramp-limit: a
	// jump to 20 lands at 20, not 12.
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 20}},
		nil,
	)
	require.Equal(t, float64(20), knobValue(t, pr),
		"nil effective knob skips ramp limiting even after prior history")
}

// TestProvisionPolicyResultCleanupStaleKnobs verifies that control knobs no
// longer present in the current input have their regulators dropped, so a knob
// the policy stopped emitting does not linger in the output.
func TestProvisionPolicyResultCleanupStaleKnobs(t *testing.T) {
	t.Parallel()

	pr := newTestPolicyResult("cleanup")
	knobA := configapi.ControlKnobNonReclaimedCPURequirement

	pr.regulateControlKnob(
		types.ControlKnob{knobA: {Value: 5}},
		nil,
	)
	require.Contains(t, pr.getControlKnob(), knobA)

	// Next cycle: policy emits no knobs at all. The regulator for knobA must
	// be cleaned up.
	pr.regulateControlKnob(types.ControlKnob{}, nil)
	require.Empty(t, pr.getControlKnob())
}

// TestProvisionPolicyResultSetEssentialsPropagates verifies that setEssentials
// updates the bounds on existing regulators, which is what steady-state cycles
// call before regulateControlKnob so the clamp uses the current bounds.
func TestProvisionPolicyResultSetEssentialsPropagates(t *testing.T) {
	t.Parallel()

	pr := newTestPolicyResult("essentials")
	knob := configapi.ControlKnobNonReclaimedCPURequirement

	// Seed at 10.
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 10}},
		nil,
	)
	require.Equal(t, float64(10), knobValue(t, pr))

	// Tighten the upper bound to 7 via setEssentials. The next regulate must
	// clamp to 7, proving the propagated essentials took effect.
	pr.setEssentials(types.ResourceEssentials{
		ResourceLowerBound: 0,
		ResourceUpperBound: 7,
	})
	pr.regulateControlKnob(
		types.ControlKnob{knob: {Value: 50}},
		nil,
	)
	require.Equal(t, float64(7), knobValue(t, pr),
		"setEssentials must update the clamp bound on existing regulators")
}

// TestCPURegulatorSlowdownRampDownPeriod pins the time-based ramp-down guard:
// after one ramp-down sets latestRampDownTime, a further decrease within
// MinRampDownPeriod is held at the effective value. This is the other input that
// regulateProvisionControlKnob feeds via the non-nil effectiveControlKnob.
func TestCPURegulatorSlowdownRampDownPeriod(t *testing.T) {
	t.Parallel()

	reg := &regulator.CPURegulator{
		ResourceEssentials: types.ResourceEssentials{ResourceLowerBound: 0, ResourceUpperBound: 100},
		RegulatorOptions: regulator.RegulatorOptions{
			MaxRampUpStep:     100, // big enough to not step-limit
			MaxRampDownStep:   100,
			MinRampDownPeriod: time.Hour, // long: the period must NOT have elapsed
			NeedHTAligned:     func() bool { return false },
			CPUsPerCore:       func() int { return 1 },
		},
	}

	// First regulate at 20 (ramp up from 0): latestRampDownTime is NOT touched
	// because 20 > 0 is an increase.
	reg.Regulate(types.ControlKnobItem{Value: 20}, nil)
	require.Equal(t, 20, reg.GetRequirement())

	// Ramp down to 15: this sets latestRampDownTime=now.
	eff15 := types.ControlKnobItem{Value: 15}
	reg.Regulate(types.ControlKnobItem{Value: 15}, &eff15)
	require.Equal(t, 15, reg.GetRequirement())

	// Immediately ask for 10 (another decrease). Because MinRampDownPeriod has
	// not elapsed since the 20->15 ramp-down, slowdown must hold at effective=15.
	reg.Regulate(types.ControlKnobItem{Value: 10}, &eff15)
	require.Equal(t, 15, reg.GetRequirement(),
		"ramp-down within MinRampDownPeriod must be held at the effective value")
}
