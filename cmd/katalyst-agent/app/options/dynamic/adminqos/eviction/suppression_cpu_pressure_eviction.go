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
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

const (
	defaultEnableSuppressionUsageEviction         = false
	defaultSuppressionUsageSyncPeriod             = 15
	defaultSuppressionUsageMetricRingSize         = 10
	defaultSuppressionUsageThresholdMetPercentage = 0.8
	defaultSoftSuppressionRateThreshold           = 5
	defaultSoftCPUUsageThreshold                  = 0.8
	defaultHardSuppressionRateThreshold           = 10
	defaultHardCPUUsageThreshold                  = 0.01
	defaultPodCPUUsageEvictionThreshold           = 0
	defaultSuppressionUsageGracePeriod            = -1
)

// SuppressionUsageCPUPressureEvictionOptions holds the command-line options
// of the suppression usage cpu pressure eviction channel.
type SuppressionUsageCPUPressureEvictionOptions struct {
	EnableSuppressionUsageEviction bool
	SyncPeriod                     int64
	MetricRingSize                 int
	ThresholdMetPercentage         float64
	SoftSuppressionRateThreshold   float64
	SoftCPUUsageThreshold          float64
	HardSuppressionRateThreshold   float64
	HardCPUUsageThreshold          float64
	PodCPUUsageEvictionThreshold   float64
	GracePeriod                    int64
}

// NewSuppressionUsageCPUPressureEvictionOptions creates
// SuppressionUsageCPUPressureEvictionOptions with default values.
func NewSuppressionUsageCPUPressureEvictionOptions() SuppressionUsageCPUPressureEvictionOptions {
	return SuppressionUsageCPUPressureEvictionOptions{
		EnableSuppressionUsageEviction: defaultEnableSuppressionUsageEviction,
		SyncPeriod:                     defaultSuppressionUsageSyncPeriod,
		MetricRingSize:                 defaultSuppressionUsageMetricRingSize,
		ThresholdMetPercentage:         defaultSuppressionUsageThresholdMetPercentage,
		SoftSuppressionRateThreshold:   defaultSoftSuppressionRateThreshold,
		SoftCPUUsageThreshold:          defaultSoftCPUUsageThreshold,
		HardSuppressionRateThreshold:   defaultHardSuppressionRateThreshold,
		HardCPUUsageThreshold:          defaultHardCPUUsageThreshold,
		PodCPUUsageEvictionThreshold:   defaultPodCPUUsageEvictionThreshold,
		GracePeriod:                    defaultSuppressionUsageGracePeriod,
	}
}

// AddFlags adds the suppression usage cpu pressure eviction flags.
func (o *SuppressionUsageCPUPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("suppression-usage-cpu-pressure-eviction")

	fs.BoolVar(&o.EnableSuppressionUsageEviction, "suppression-usage-cpu-pressure-eviction-enable", o.EnableSuppressionUsageEviction,
		"Enable cpu suppression usage eviction, which evicts reclaimed pods by considering both the suppression rate and the actual CPU usage")
	fs.Int64Var(&o.SyncPeriod, "suppression-usage-cpu-pressure-eviction-sync-period", o.SyncPeriod,
		"The sync period (in seconds) for cpu suppression usage eviction")
	fs.IntVar(&o.MetricRingSize, "suppression-usage-cpu-pressure-eviction-metric-ring-size", o.MetricRingSize,
		"The size of the metric ring for cpu suppression usage eviction")
	fs.Float64Var(&o.ThresholdMetPercentage, "suppression-usage-cpu-pressure-eviction-threshold-met-percentage", o.ThresholdMetPercentage,
		"The percentage of samples in the metric ring over the threshold to trigger cpu suppression usage eviction")
	fs.Float64Var(&o.SoftSuppressionRateThreshold, "suppression-usage-cpu-pressure-eviction-soft-suppression-rate-threshold", o.SoftSuppressionRateThreshold,
		"The soft threshold of the pool suppression rate")
	fs.Float64Var(&o.SoftCPUUsageThreshold, "suppression-usage-cpu-pressure-eviction-soft-cpu-usage-threshold", o.SoftCPUUsageThreshold,
		"The soft threshold of the pool CPU usage ratio")
	fs.Float64Var(&o.HardSuppressionRateThreshold, "suppression-usage-cpu-pressure-eviction-hard-suppression-rate-threshold", o.HardSuppressionRateThreshold,
		"The hard threshold of the pool suppression rate")
	fs.Float64Var(&o.HardCPUUsageThreshold, "suppression-usage-cpu-pressure-eviction-hard-cpu-usage-threshold", o.HardCPUUsageThreshold,
		"The hard threshold of the pool CPU usage ratio")
	fs.Float64Var(&o.PodCPUUsageEvictionThreshold, "suppression-usage-cpu-pressure-eviction-pod-cpu-usage-eviction-threshold", o.PodCPUUsageEvictionThreshold,
		"The minimum actual CPU usage (in cores) a pod must consume to be considered for eviction")
	fs.Int64Var(&o.GracePeriod, "suppression-usage-cpu-pressure-eviction-grace-period", o.GracePeriod,
		"The grace period (in seconds) after a pod starts before it can be considered for eviction due to suppression usage")
}

// ApplyTo applies the options to the config, validating the numeric fields
// that the strategy relies on.
func (o *SuppressionUsageCPUPressureEvictionOptions) ApplyTo(c *eviction.SuppressionUsageCPUPressureEvictionConfiguration) error {
	if o.SyncPeriod <= 0 || o.MetricRingSize <= 0 || o.ThresholdMetPercentage <= 0 || o.ThresholdMetPercentage > 1 {
		return fmt.Errorf("invalid suppression usage cpu pressure eviction options: syncPeriod=%d metricRingSize=%d thresholdMetPercentage=%v",
			o.SyncPeriod, o.MetricRingSize, o.ThresholdMetPercentage)
	}
	c.EnableSuppressionUsageEviction = o.EnableSuppressionUsageEviction
	c.SyncPeriod = o.SyncPeriod
	c.MetricRingSize = o.MetricRingSize
	c.ThresholdMetPercentage = o.ThresholdMetPercentage
	c.SoftSuppressionRateThreshold = o.SoftSuppressionRateThreshold
	c.SoftCPUUsageThreshold = o.SoftCPUUsageThreshold
	c.HardSuppressionRateThreshold = o.HardSuppressionRateThreshold
	c.HardCPUUsageThreshold = o.HardCPUUsageThreshold
	c.PodCPUUsageEvictionThreshold = o.PodCPUUsageEvictionThreshold
	c.GracePeriod = o.GracePeriod

	return nil
}
