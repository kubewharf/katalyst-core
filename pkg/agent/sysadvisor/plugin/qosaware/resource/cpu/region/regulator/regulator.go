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

package regulator

import (
	"math"
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Regulator gets raw requirement data from policy and generates real requirement
// for a certain region with fine-grained strategies to be robust
type Regulator interface {
	// SetEssentials updates some essential parameters to restrict requirement
	SetEssentials(essentials types.ResourceEssentials)

	// SetLatestControlKnobValue overwrites the latest regulated requirement
	SetLatestControlKnobValue(controlKnobValue types.ControlKnobValue)

	// Regulate runs an episode of regulation to restrict raw requirement and store the result
	// as the latest requirement value
	Regulate(controlKnobValue types.ControlKnobValue)

	// GetRequirement returns the latest regulated requirement
	GetRequirement() int

	GetReason() string
}

// DummyRegulator always get requirement without regulate
type DummyRegulator struct {
	latestControlKnobValue types.ControlKnobValue
}

func NewDummyRegulator() Regulator {
	return &DummyRegulator{}
}

var _ Regulator = &DummyRegulator{}

func (d *DummyRegulator) SetEssentials(_ types.ResourceEssentials) {
}

func (d *DummyRegulator) SetLatestControlKnobValue(controlKnobValue types.ControlKnobValue) {
	d.latestControlKnobValue = controlKnobValue
}

func (d *DummyRegulator) Regulate(controlKnobValue types.ControlKnobValue) {
	d.SetLatestControlKnobValue(controlKnobValue)
}

func (d *DummyRegulator) GetRequirement() int {
	return int(d.latestControlKnobValue.Value)
}

func (d *DummyRegulator) GetReason() string {
	return d.latestControlKnobValue.Reason
}

// CPURegulator gets raw cpu requirement data from policy and generates real cpu requirement
// for a certain region with fine-grained strategies to be robust
type CPURegulator struct {
	types.ResourceEssentials

	// maxRampUpStep is the max cpu cores can be increased during each cpu requirement update
	maxRampUpStep int

	// maxRampDownStep is the max cpu cores can be decreased during each cpu requirement update
	maxRampDownStep int

	// minRampDownPeriod is the min time gap between two consecutive cpu requirement ramp down
	minRampDownPeriod time.Duration

	// latestControlKnobValue is the latest updated cpu requirement value
	latestControlKnobValue types.ControlKnobValue

	// latestRampDownTime is the latest ramp down timestamp
	latestRampDownTime time.Time
}

// NewCPURegulator returns a cpu regulator instance with immutable parameters
func NewCPURegulator() Regulator {
	c := &CPURegulator{
		maxRampUpStep:      types.MaxRampUpStep,
		maxRampDownStep:    types.MaxRampDownStep,
		minRampDownPeriod:  types.MinRampDownPeriod,
		latestRampDownTime: time.Now().Add(-types.MinRampDownPeriod),
	}
	return c
}

// SetEssentials updates some essential parameters to restrict cpu requirement
func (c *CPURegulator) SetEssentials(essentials types.ResourceEssentials) {
	c.ResourceEssentials = essentials
}

// SetLatestControlKnobValue overwrites the latest regulated cpu requirement
func (c *CPURegulator) SetLatestControlKnobValue(controlKnobValue types.ControlKnobValue) {
	c.latestControlKnobValue = controlKnobValue
}

// Regulate runs an episode of cpu regulation to restrict raw cpu requirement and store the result
// as the latest cpu requirement value
func (c *CPURegulator) Regulate(controlKnobValue types.ControlKnobValue) {
	cpuRequirement := controlKnobValue.Value
	cpuRequirementReserved := cpuRequirement + c.ReservedForAllocate
	cpuRequirementRound := c.round(cpuRequirementReserved)
	cpuRequirementSlowdown := c.slowdown(cpuRequirementRound)
	cpuRequirementClamp := c.clamp(cpuRequirementSlowdown)

	klog.Infof("[qosaware-cpu] cpu requirement by policy: %.2f, with reserve: %.2f, after round: %d, after slowdown: %d, after clamp: %d",
		cpuRequirement, cpuRequirementReserved, cpuRequirementRound, cpuRequirementSlowdown, cpuRequirementClamp)

	if cpuRequirementClamp != int(c.latestControlKnobValue.Value) {
		c.latestControlKnobValue.Value = float64(cpuRequirementClamp)
		c.latestRampDownTime = time.Now()
	}
	c.latestControlKnobValue.Reason = controlKnobValue.Reason
}

// GetRequirement returns the latest regulated cpu requirement
func (c *CPURegulator) GetRequirement() int {
	return int(c.latestControlKnobValue.Value)
}

func (c *CPURegulator) GetReason() string {
	return c.latestControlKnobValue.Reason
}

func (c *CPURegulator) slowdown(cpuRequirement int) int {
	now := time.Now()

	general.InfoS("slowdown info", "cpuRequirement", cpuRequirement, "latestCPURequirement", c.latestControlKnobValue.Value, "latestRampDownTime", c.latestRampDownTime, "minRampDownPeriod", c.minRampDownPeriod)

	// Restrict ramp down frequency
	if cpuRequirement < int(c.latestControlKnobValue.Value) && now.Before(c.latestRampDownTime.Add(c.minRampDownPeriod)) {
		return int(c.latestControlKnobValue.Value)
	}

	// Restrict ramp up and down step
	if cpuRequirement-int(c.latestControlKnobValue.Value) > c.maxRampUpStep {
		cpuRequirement = int(c.latestControlKnobValue.Value) + c.maxRampUpStep
	} else if int(c.latestControlKnobValue.Value)-cpuRequirement > c.maxRampDownStep {
		cpuRequirement = int(c.latestControlKnobValue.Value) - c.maxRampDownStep
	}

	return cpuRequirement
}

func (c *CPURegulator) round(cpuRequirement float64) int {
	// Never share cores between latency-critical pods and best-effort pods
	// so make latency-critical pods require at least a core's-worth of CPUs.
	// This rule can be broken by clamp.
	cpuRequirementRounded := int(math.Ceil(cpuRequirement))
	if cpuRequirementRounded%2 == 1 {
		cpuRequirementRounded += 1
	}
	return cpuRequirementRounded
}

func (c *CPURegulator) clamp(cpuRequirement int) int {
	if cpuRequirement < int(c.ResourceLowerBound) {
		return int(c.ResourceLowerBound)
	} else if cpuRequirement > int(c.ResourceUpperBound) {
		return int(c.ResourceUpperBound)
	} else {
		return cpuRequirement
	}
}
