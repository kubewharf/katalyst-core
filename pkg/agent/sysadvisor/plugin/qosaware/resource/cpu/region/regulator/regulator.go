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
)

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

	// latestCPURequirement is the latest updated cpu requirement value
	latestCPURequirement int

	// latestRampDownTime is the latest ramp down timestamp
	latestRampDownTime time.Time
}

// NewCPURegulator returns a cpu regulator instance with immutable parameters
func NewCPURegulator() *CPURegulator {
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

// SetLatestCPURequirement overwrites the latest regulated cpu requirement
func (c *CPURegulator) SetLatestCPURequirement(latestCPURequirement int) {
	c.latestCPURequirement = latestCPURequirement
}

// Regulate runs an episode of cpu regulation to restrict raw cpu requirement and store the result
// as the latest cpu requirement value
func (c *CPURegulator) Regulate(cpuRequirement float64) {
	cpuRequirementReserved := cpuRequirement + c.ReservedForAllocate
	cpuRequirementRound := c.round(cpuRequirementReserved)
	cpuRequirementSlowdown := c.slowdown(cpuRequirementRound)
	cpuRequirementClamp := c.clamp(cpuRequirementSlowdown)

	klog.Infof("[qosaware-cpu] cpu requirement by policy: %.2f, with reserve: %.2f, after round: %d, after slowdown: %d, after clamp: %d",
		cpuRequirement, cpuRequirementReserved, cpuRequirementRound, cpuRequirementSlowdown, cpuRequirementClamp)

	if cpuRequirementClamp != c.latestCPURequirement {
		c.latestCPURequirement = cpuRequirementClamp
		c.latestRampDownTime = time.Now()
	}
}

// GetCPURequirement returns the latest regulated cpu requirement
func (c *CPURegulator) GetCPURequirement() int {
	return c.latestCPURequirement
}

func (c *CPURegulator) slowdown(cpuRequirement int) int {
	now := time.Now()

	// Restrict ramp down frequency
	if cpuRequirement < c.latestCPURequirement && now.Before(c.latestRampDownTime.Add(c.minRampDownPeriod)) {
		return c.latestCPURequirement
	}

	// Restrict ramp up and down step
	if cpuRequirement-c.latestCPURequirement > c.maxRampUpStep {
		cpuRequirement = c.latestCPURequirement + c.maxRampUpStep
	} else if c.latestCPURequirement-cpuRequirement > c.maxRampDownStep {
		cpuRequirement = c.latestCPURequirement - c.maxRampDownStep
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
