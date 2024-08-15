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

// CPURegulator gets raw cpu requirement data from policy and generates real cpu requirement
// for a certain region with fine-grained strategies to be robust
type CPURegulator struct {
	types.ResourceEssentials

	RegulatorOptions

	// latestControlKnobValue is the latest updated cpu requirement value
	latestControlKnobValue types.ControlKnobValue

	// latestRampDownTime is the latest ramp down timestamp
	latestRampDownTime time.Time
}

type RegulatorOptions struct {
	// MaxRampUpStep is the max cpu cores can be increased during each cpu requirement update
	MaxRampUpStep int

	// MaxRampDownStep is the max cpu cores can be decreased during each cpu requirement update
	MaxRampDownStep int

	// MinRampDownPeriod is the min time gap between two consecutive cpu requirement ramp down
	MinRampDownPeriod time.Duration
}

// NewCPURegulator returns a cpu regulator instance with immutable parameters
func NewCPURegulator(essentials types.ResourceEssentials, options RegulatorOptions) Regulator {
	c := &CPURegulator{
		ResourceEssentials: essentials,
		RegulatorOptions:   options,
		latestRampDownTime: time.Now().Add(-options.MinRampDownPeriod),
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
}

// GetRequirement returns the latest regulated cpu requirement
func (c *CPURegulator) GetRequirement() int {
	return int(c.latestControlKnobValue.Value)
}

func (c *CPURegulator) slowdown(cpuRequirement int) int {
	now := time.Now()

	general.InfoS("slowdown info", "cpuRequirement", cpuRequirement, "latestCPURequirement", c.latestControlKnobValue.Value, "latestRampDownTime", c.latestRampDownTime, "minRampDownPeriod", c.MinRampDownPeriod)

	// Restrict ramp down frequency
	if cpuRequirement < int(c.latestControlKnobValue.Value) && now.Before(c.latestRampDownTime.Add(c.MinRampDownPeriod)) {
		return int(c.latestControlKnobValue.Value)
	}

	// Restrict ramp up and down step
	if cpuRequirement-int(c.latestControlKnobValue.Value) > c.MaxRampUpStep {
		cpuRequirement = int(c.latestControlKnobValue.Value) + c.MaxRampUpStep
	} else if int(c.latestControlKnobValue.Value)-cpuRequirement > c.MaxRampDownStep {
		cpuRequirement = int(c.latestControlKnobValue.Value) - c.MaxRampDownStep
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
