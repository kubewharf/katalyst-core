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

package headroom

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/headroom"
)

const (
	defaultReclaimedCPUTargetCoreUtilization   = 0.6
	defaultReclaimedCPUMaxCoreUtilization      = 0
	defaultReclaimedCPUMaxOversoldRate         = 1.2
	defaultReclaimedCPUMaxHeadroomCapacityRate = 1.
)

type PolicyAdaptiveOptions struct {
	ReclaimedCPUTargetCoreUtilization   float64
	ReclaimedCPUMaxCoreUtilization      float64
	ReclaimedCPUMaxOversoldRate         float64
	ReclaimedCPUMaxHeadroomCapacityRate float64
}

func NewPolicyAdaptiveOptions() *PolicyAdaptiveOptions {
	return &PolicyAdaptiveOptions{
		ReclaimedCPUTargetCoreUtilization:   defaultReclaimedCPUTargetCoreUtilization,
		ReclaimedCPUMaxCoreUtilization:      defaultReclaimedCPUMaxCoreUtilization,
		ReclaimedCPUMaxOversoldRate:         defaultReclaimedCPUMaxOversoldRate,
		ReclaimedCPUMaxHeadroomCapacityRate: defaultReclaimedCPUMaxHeadroomCapacityRate,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *PolicyAdaptiveOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float64Var(&o.ReclaimedCPUTargetCoreUtilization, "cpu-headroom-policy-adaptive-target-core-utilization", o.ReclaimedCPUTargetCoreUtilization,
		"the target core utilization of reclaimed_cpu pool")
	fs.Float64Var(&o.ReclaimedCPUMaxCoreUtilization, "cpu-headroom-policy-adaptive-max-core-utilization", o.ReclaimedCPUMaxCoreUtilization,
		"the maximum core utilization of reclaimed_cores pool, if zero means no upper limit")
	fs.Float64Var(&o.ReclaimedCPUMaxOversoldRate, "cpu-headroom-policy-adaptive-max-oversold-ratio", o.ReclaimedCPUMaxOversoldRate,
		"the maximum oversold ratio of reclaimed_cores cpu reported to actual supply")
	fs.Float64Var(&o.ReclaimedCPUMaxHeadroomCapacityRate, "cpu-headroom-policy-adaptive-max-headroom-capacity-rate", o.ReclaimedCPUMaxHeadroomCapacityRate,
		"the maximum rate of cpu headroom to node cpu capacity, if zero means no upper limit")
}

func (o *PolicyAdaptiveOptions) ApplyTo(c *headroom.PolicyAdaptiveConfiguration) error {
	c.ReclaimedCPUTargetCoreUtilization = o.ReclaimedCPUTargetCoreUtilization
	c.ReclaimedCPUMaxCoreUtilization = o.ReclaimedCPUMaxCoreUtilization
	c.ReclaimedCPUMaxOversoldRate = o.ReclaimedCPUMaxOversoldRate
	c.ReclaimedCPUMaxHeadroomCapacityRate = o.ReclaimedCPUMaxHeadroomCapacityRate
	return nil
}
