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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

// cpuCalculator gets raw cpu requirement data from policy and generates real cpu requirement
// for a certain region with fine-grained strategies to be robust
type cpuCalculator struct {
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

// newCPUCalculator returns a cpu calculator instance with parameters and the specific policy
func newCPUCalculator(conf *config.Configuration, metaCache *metacache.MetaCache, maxRampUpStep float64,
	maxRampDownStep float64, minRampDownPeriod time.Duration) *cpuCalculator {
	c := &cpuCalculator{
		maxRampUpStep:      maxRampUpStep,
		maxRampDownStep:    maxRampDownStep,
		minRampDownPeriod:  minRampDownPeriod,
		latestRampDownTime: time.Now(),
	}
	return c
}

func (c *cpuCalculator) setMinCPURequirement(requirement int) {
	c.minCPURequirement = requirement
}

func (c *cpuCalculator) setMaxCPURequirement(requirement int) {
	c.maxCPURequirement = requirement
}

func (c *cpuCalculator) setTotalCPURequirement(requirement int) {
	c.totalCPURequirement = requirement
}

func (c *cpuCalculator) setLastestCPURequirement(requirement int) {
	c.latestCPURequirement = requirement
}

func (c *cpuCalculator) setReservedForAllocate(value int) {
	c.reservedForAllocate = value
}

// update runs a calculation episode
func (c *cpuCalculator) update(cpuRequirementRaw float64) {
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

// getCPURequirement returns the latest cpu requirement value
func (c *cpuCalculator) getCPURequirement() int {
	return c.latestCPURequirement
}

// getCPURequirementReclaimed returns the latest cpu requirement value can be reclaimed
func (c *cpuCalculator) getCPURequirementReclaimed() int {
	return c.totalCPURequirement - c.latestCPURequirement
}

func (c *cpuCalculator) slowdown(cpuRequirement float64) float64 {
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

func (c *cpuCalculator) round(cpuRequirement float64) int {
	// Never share cores between latency-critical pods and best-effort pods
	// so make latency-critical pods require at least a core's-worth of CPUs.
	// This rule can be broken by clamp.
	cpuRequirementRounded := int(math.Ceil(cpuRequirement))
	if cpuRequirementRounded%2 == 1 {
		cpuRequirementRounded += 1
	}
	return cpuRequirementRounded
}

func (c *cpuCalculator) clamp(cpuRequirement int) int {
	if cpuRequirement < c.minCPURequirement {
		return c.minCPURequirement
	} else if c.minCPURequirement < c.maxCPURequirement && cpuRequirement > c.maxCPURequirement {
		return c.maxCPURequirement
	} else {
		return cpuRequirement
	}
}
