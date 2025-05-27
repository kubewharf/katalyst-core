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

package eviction

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type NumaCPUPressureEvictionOptions struct {
	EnableEviction         bool
	ThresholdMetPercentage float64
	MetricRingSize         int
	GracePeriod            int64
	ThresholdExpandFactor  float64
}

func NewNumaCPUPressureEvictionOptions() NumaCPUPressureEvictionOptions {
	return NumaCPUPressureEvictionOptions{
		EnableEviction:         false,
		ThresholdMetPercentage: 0.7,
		MetricRingSize:         4,
		GracePeriod:            60,
		ThresholdExpandFactor:  1.1,
	}
}

func (o *NumaCPUPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("numa-cpu-pressure-eviction")

	fs.BoolVar(&o.EnableEviction, "numa-cpu-pressure-eviction-enable", o.EnableEviction,
		"Enable numa cpu pressure eviction")
	fs.Float64Var(&o.ThresholdMetPercentage, "numa-cpu-pressure-eviction-threshold-met-percentage", o.ThresholdMetPercentage,
		"The percentage of NUMA nodes whose CPU pressure meets the threshold to trigger eviction")
	fs.IntVar(&o.MetricRingSize, "numa-cpu-pressure-eviction-metric-ring-size", o.MetricRingSize,
		"The size of the metric ring for NUMA CPU pressure")
	fs.Int64Var(&o.GracePeriod, "numa-cpu-pressure-eviction-grace-period", o.GracePeriod,
		"The grace period (in seconds) before evicting pods due to NUMA CPU pressure")
	fs.Float64Var(&o.ThresholdExpandFactor, "numa-cpu-pressure-eviction-threshold-expand-factor", o.ThresholdExpandFactor,
		"The factor by which to expand the NUMA CPU pressure threshold")
}

func (o *NumaCPUPressureEvictionOptions) ApplyTo(c *eviction.NumaCPUPressureEvictionConfiguration) error {
	c.EnableEviction = o.EnableEviction
	c.ThresholdMetPercentage = o.ThresholdMetPercentage
	c.MetricRingSize = o.MetricRingSize
	c.GracePeriod = o.GracePeriod
	c.ThresholdExpandFactor = o.ThresholdExpandFactor
	return nil
}
