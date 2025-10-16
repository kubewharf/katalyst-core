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

package cpuheadroom

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/reclaimedresource/cpuheadroom"
)

const (
	defaultEnable                         = false
	defaultTargetReclaimedCoreUtilization = 0.6
	defaultMaxReclaimedCoreUtilization    = 0
	defaultMaxOversoldRate                = 1.2
	defaultMaxHeadroomCapacityRate        = 1.
	defaultNonReclaimUtilizationHigh      = 0.7
	defaultNonReclaimUtilizationLow       = 0.6
)

type CPUHeadroomUtilBasedOptions struct {
	Enable                         bool
	TargetReclaimedCoreUtilization float64
	MaxReclaimedCoreUtilization    float64
	MaxOversoldRate                float64
	MaxHeadroomCapacityRate        float64
	NonReclaimUtilizationHigh      float64
	NonReclaimUtilizationLow       float64
}

func NewCPUHeadroomUtilBasedOptions() *CPUHeadroomUtilBasedOptions {
	return &CPUHeadroomUtilBasedOptions{
		Enable:                         defaultEnable,
		TargetReclaimedCoreUtilization: defaultTargetReclaimedCoreUtilization,
		MaxReclaimedCoreUtilization:    defaultMaxReclaimedCoreUtilization,
		MaxOversoldRate:                defaultMaxOversoldRate,
		MaxHeadroomCapacityRate:        defaultMaxHeadroomCapacityRate,
		NonReclaimUtilizationHigh:      defaultNonReclaimUtilizationHigh,
		NonReclaimUtilizationLow:       defaultNonReclaimUtilizationLow,
	}
}

func (o *CPUHeadroomUtilBasedOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Enable, "cpu-headroom-util-based-enable", o.Enable,
		"show whether enable utilization based cpu headroom policy")
	fs.Float64Var(&o.TargetReclaimedCoreUtilization, "cpu-headroom-target-reclaimed-core-utilization", o.TargetReclaimedCoreUtilization,
		"the target reclaimed core utilization to be used for calculating the oversold cpu headroom")
	fs.Float64Var(&o.MaxReclaimedCoreUtilization, "cpu-headroom-max-reclaimed-core-utilization", o.MaxReclaimedCoreUtilization,
		"the max reclaimed core utilization to be used for calculating the oversold cpu headroom, if zero means no limit")
	fs.Float64Var(&o.MaxOversoldRate, "cpu-headroom-max-oversold-rate", o.MaxOversoldRate,
		"the max oversold rate of cpu headroom to the actual size of reclaimed_cores pool")
	fs.Float64Var(&o.MaxHeadroomCapacityRate, "cpu-headroom-max-capacity-rate", o.MaxHeadroomCapacityRate,
		"the max headroom capacity rate of cpu headroom to the total cpu capacity of node")
	fs.Float64Var(&o.NonReclaimUtilizationHigh, "cpu-headroom-non-reclaim-utilization-high", o.NonReclaimUtilizationHigh,
		"the high cpu utilization threshold")
	fs.Float64Var(&o.NonReclaimUtilizationLow, "cpu-headroom-non-reclaim-utilization-low", o.NonReclaimUtilizationLow,
		"the low cpu utilization threshold")
}

func (o *CPUHeadroomUtilBasedOptions) ApplyTo(c *cpuheadroom.CPUUtilBasedConfiguration) error {
	c.Enable = o.Enable
	c.TargetReclaimedCoreUtilization = o.TargetReclaimedCoreUtilization
	c.MaxReclaimedCoreUtilization = o.MaxReclaimedCoreUtilization
	c.MaxOversoldRate = o.MaxOversoldRate
	c.MaxHeadroomCapacityRate = o.MaxHeadroomCapacityRate
	c.NonReclaimUtilizationHigh = o.NonReclaimUtilizationHigh
	c.NonReclaimUtilizationLow = o.NonReclaimUtilizationLow
	return nil
}
