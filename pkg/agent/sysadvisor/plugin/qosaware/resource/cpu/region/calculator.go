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
	"math"
	"time"

	"k8s.io/klog/v2"
)

// CPUCalculator gets raw cpu requirement data from policy and generates real cpu requirement
// for a certain region with fine-grained strategies to be robust
type CPUCalculator struct {
	// minCPURequirement is the min cpu requirement value
	minCPURequirement int

	// maxCPURequirement is the max cpu requirement value
	maxCPURequirement int

	// totalCPURequirement is all available cpu resource value
	totalCPURequirement int

	// ReservedForAllocate is the reserved cpu resource value for this region
	reservedForAllocate int

	// maxRampUpStep is the max cpu cores can be increased during each cpu requirement update
	maxRampUpStep float64

	// maxRampDownStep is the max cpu cores can be decreased during each cpu requirement update
	maxRampDownStep float64

	// minRampDownPeriod is the min time gap between two consecutive cpu requirement ramp down
	minRampDownPeriod time.Duration

	// latestCPURequirement is the latest updated cpu requirement value
	latestCPURequirement int

	// latestRampDownTime is the lastest ramp down timestamp
	latestRampDownTime time.Time
}

// NewCPUCalculator returns a cpu calculator instance with parameters and the specific policy
func NewCPUCalculator(maxRampUpStep float64,
	maxRampDownStep float64, minRampDownPeriod time.Duration) *CPUCalculator {
	c := &CPUCalculator{
		maxRampUpStep:      maxRampUpStep,
		maxRampDownStep:    maxRampDownStep,
		minRampDownPeriod:  minRampDownPeriod,
		latestRampDownTime: time.Now(),
	}
	return c
}

func (c *CPUCalculator) SetupCPURequirement(min, max, total, reserved int) {
	c.minCPURequirement = min
	c.maxCPURequirement = max
	c.totalCPURequirement = total
	c.reservedForAllocate = reserved
}

func (c *CPUCalculator) SetLastestCPURequirement(requirement int) {
	c.latestCPURequirement = requirement
}

// RegulateRequirement runs a calculation episode
func (c *CPUCalculator) RegulateRequirement(cpuRequirementRaw float64) {
	cpuRequirement := cpuRequirementRaw + float64(c.reservedForAllocate)
	cpuRequirement = c.slowdown(cpuRequirement)
	cpuRequirementInt := c.round(cpuRequirement)
	cpuRequirementInt = c.clamp(cpuRequirementInt)

	klog.Infof("[qosaware-cpu] cpu requirement by policy: %.2f, after post process: %v, added reserved: %v", cpuRequirementRaw, cpuRequirementInt, c.reservedForAllocate)

	if cpuRequirementInt != c.latestCPURequirement {
		c.latestCPURequirement = cpuRequirementInt
		c.latestRampDownTime = time.Now()
	}
}

// GetCPURequirement returns the latest cpu requirement value
func (c *CPUCalculator) GetCPURequirement() int {
	return c.latestCPURequirement
}

// GetCPURequirementReclaimed returns the latest cpu requirement value can be reclaimed
func (c *CPUCalculator) GetCPURequirementReclaimed() int {
	return c.totalCPURequirement - c.latestCPURequirement
}

func (c *CPUCalculator) slowdown(cpuRequirement float64) float64 {
	now := time.Now()
	latestCPURequirement := float64(c.latestCPURequirement)

	// Restrict ramp down period
	if cpuRequirement < latestCPURequirement && now.Before(c.latestRampDownTime.Add(c.minRampDownPeriod)) {
		cpuRequirement = latestCPURequirement
	}

	// Restrict ramp up and down step
	if cpuRequirement-latestCPURequirement > c.maxRampUpStep {
		cpuRequirement = latestCPURequirement + c.maxRampUpStep
	} else if latestCPURequirement-cpuRequirement > c.maxRampDownStep {
		cpuRequirement = latestCPURequirement - c.maxRampDownStep
	}

	return cpuRequirement
}

func (c *CPUCalculator) round(cpuRequirement float64) int {
	// Never share cores between latency-critical pods and best-effort pods
	// so make latency-critical pods require at least a core's-worth of CPUs.
	// This rule can be broken by clamp.
	cpuRequirementRounded := int(math.Ceil(cpuRequirement))
	if cpuRequirementRounded%2 == 1 {
		cpuRequirementRounded += 1
	}
	return cpuRequirementRounded
}

func (c *CPUCalculator) clamp(cpuRequirement int) int {
	if cpuRequirement < c.minCPURequirement {
		return c.minCPURequirement
	} else if c.minCPURequirement < c.maxCPURequirement && cpuRequirement > c.maxCPURequirement {
		return c.maxCPURequirement
	} else {
		return cpuRequirement
	}
}
