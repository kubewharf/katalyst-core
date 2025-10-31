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

type NumaSysCPUPressureEvictionOptions struct {
	EnableEviction bool
	MetricRingSize int
	GracePeriod    int64
	SyncPeriod     int64

	ThresholdMetPercentage                 float64
	NumaCPUUsageSoftThreshold              float64
	NumaCPUUsageHardThreshold              float64
	NUMASysOverTotalUsageSoftThreshold     float64
	NUMASysOverTotalUsageHardThreshold     float64
	NUMASysOverTotalUsageEvictionThreshold float64
}

func NewNumaSysCPUPressureEvictionOptions() NumaSysCPUPressureEvictionOptions {
	return NumaSysCPUPressureEvictionOptions{
		EnableEviction: false,
		MetricRingSize: 4,
		GracePeriod:    60,
		SyncPeriod:     10,

		ThresholdMetPercentage:                 0.7,
		NumaCPUUsageSoftThreshold:              0.4,
		NumaCPUUsageHardThreshold:              0.5,
		NUMASysOverTotalUsageSoftThreshold:     0.4,
		NUMASysOverTotalUsageHardThreshold:     0.5,
		NUMASysOverTotalUsageEvictionThreshold: 0.3,
	}
}

func (o *NumaSysCPUPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("numa-sys-cpu-pressure-eviction")

	fs.BoolVar(&o.EnableEviction, "numa-sys-cpu-pressure-eviction-enable", o.EnableEviction,
		"Enable numa system cpu pressure eviction")
	fs.IntVar(&o.MetricRingSize, "numa-sys-cpu-pressure-eviction-metric-ring-size", o.MetricRingSize,
		"The size of the metric ring for NUMA system CPU pressure")
	fs.Int64Var(&o.GracePeriod, "numa-sys-cpu-pressure-eviction-grace-period", o.GracePeriod,
		"The grace period (in seconds) before evicting pods due to NUMA system CPU pressure")
	fs.Int64Var(&o.SyncPeriod, "numa-sys-cpu-pressure-eviction-sync-period", o.SyncPeriod,
		"The sync period (in seconds) for NUMA system CPU pressure eviction")

	fs.Float64Var(&o.ThresholdMetPercentage, "numa-sys-cpu-pressure-eviction-threshold-met-percentage", o.ThresholdMetPercentage,
		"The percentage of NUMA nodes whose system CPU pressure meets the threshold to trigger eviction")
	fs.Float64Var(&o.NumaCPUUsageSoftThreshold, "numa-sys-cpu-pressure-eviction-numa-cpu-usage-soft-threshold", o.NumaCPUUsageSoftThreshold,
		"The soft threshold of NUMA node system CPU usage ratio")
	fs.Float64Var(&o.NumaCPUUsageHardThreshold, "numa-sys-cpu-pressure-eviction-numa-cpu-usage-hard-threshold", o.NumaCPUUsageHardThreshold,
		"The hard threshold of NUMA node system CPU usage ratio")
	fs.Float64Var(&o.NUMASysOverTotalUsageSoftThreshold, "numa-sys-cpu-pressure-eviction-numa-sys-over-total-usage-soft-threshold", o.NUMASysOverTotalUsageSoftThreshold,
		"The soft threshold of NUMA node system CPU pressure over total system CPU usage ratio")
	fs.Float64Var(&o.NUMASysOverTotalUsageHardThreshold, "numa-sys-cpu-pressure-eviction-numa-sys-over-total-usage-hard-threshold", o.NUMASysOverTotalUsageHardThreshold,
		"The hard threshold of NUMA node system CPU pressure over total system CPU usage ratio")
	fs.Float64Var(&o.NUMASysOverTotalUsageEvictionThreshold, "numa-sys-cpu-pressure-eviction-numa-sys-over-total-usage-eviction-threshold", o.NUMASysOverTotalUsageEvictionThreshold,
		"The eviction threshold of NUMA node system CPU pressure over total system CPU usage ratio")
}

func (o *NumaSysCPUPressureEvictionOptions) ApplyTo(c *eviction.NumaSysCPUPressureEvictionConfiguration) error {
	c.EnableEviction = o.EnableEviction
	c.MetricRingSize = o.MetricRingSize
	c.GracePeriod = o.GracePeriod
	c.SyncPeriod = o.SyncPeriod

	c.ThresholdMetPercentage = o.ThresholdMetPercentage
	c.NumaCPUUsageSoftThreshold = o.NumaCPUUsageSoftThreshold
	c.NumaCPUUsageHardThreshold = o.NumaCPUUsageHardThreshold
	c.NUMASysOverTotalUsageSoftThreshold = o.NUMASysOverTotalUsageSoftThreshold
	c.NUMASysOverTotalUsageHardThreshold = o.NUMASysOverTotalUsageHardThreshold
	c.NUMASysOverTotalUsageEvictionThreshold = o.NUMASysOverTotalUsageEvictionThreshold

	return nil
}
