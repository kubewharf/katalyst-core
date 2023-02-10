// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package eviction

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
)

const (
	defaultEnableCPUPressureEviction                = false
	defaultLoadUpperBoundRatio                      = 1.8
	defaultLoadThresholdMetPercentage               = 0.8
	defaultCPUPressureEvictionPodGracePeriodSeconds = -1
	defaultMetricRingSize                           = 10
	defaultCPUPressureEvictionSyncPeriod            = 30 * time.Second
	defaultCPUPressureEvictionColdPeriod            = 300 * time.Second
	defaultMaxCPUSuppressionToleranceRate           = 5
)

// CPUPressureEvictionPluginOptions is the options of CPUPressureEvictionPlugin
type CPUPressureEvictionPluginOptions struct {
	EnableCPUPressureEviction                bool
	LoadUpperBoundRatio                      float64
	LoadThresholdMetPercentage               float64
	CPUPressureEvictionPodGracePeriodSeconds int64
	MetricRingSize                           int
	CPUPressureEvictionSyncPeriod            time.Duration
	CPUPressureEvictionColdPeriod            time.Duration
	MaxCPUSuppressionToleranceRate           float64
}

// NewCPUPressureEvictionPluginOptions returns a new CPUPressureEvictionPluginOptions
func NewCPUPressureEvictionPluginOptions() *CPUPressureEvictionPluginOptions {
	return &CPUPressureEvictionPluginOptions{
		EnableCPUPressureEviction:                defaultEnableCPUPressureEviction,
		LoadUpperBoundRatio:                      defaultLoadUpperBoundRatio,
		LoadThresholdMetPercentage:               defaultLoadThresholdMetPercentage,
		CPUPressureEvictionPodGracePeriodSeconds: defaultCPUPressureEvictionPodGracePeriodSeconds,
		MetricRingSize:                           defaultMetricRingSize,
		CPUPressureEvictionSyncPeriod:            defaultCPUPressureEvictionSyncPeriod,
		CPUPressureEvictionColdPeriod:            defaultCPUPressureEvictionColdPeriod,
		MaxCPUSuppressionToleranceRate:           defaultMaxCPUSuppressionToleranceRate,
	}
}

// AddFlags parses the flags to CPUPressureEvictionPluginOptions
func (o *CPUPressureEvictionPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-cpu-pressure")

	fs.BoolVar(&o.EnableCPUPressureEviction, "enable-cpu-pressure-eviction",
		o.EnableCPUPressureEviction, "set true to enable cpu pressure eviction plugin")
	fs.Float64Var(&o.LoadUpperBoundRatio, "cpu-pressure-eviction-load-upper-bound-ratio",
		o.LoadUpperBoundRatio,
		"multiply the target cpuset pool size by this ration to get the load upper bound. "+
			"if the load of the target cpuset pool is greater than the load upper bound repeatedly, the eviction will be triggered")
	fs.Float64Var(&o.LoadThresholdMetPercentage, "cpu-pressure-eviction-load-threshold-met-percentage",
		o.LoadThresholdMetPercentage,
		"the ratio between the times metric value over the bound value and the metric ring size is greater than this percentage "+
			", the eviction or node taint will be triggered")
	fs.Int64Var(&o.CPUPressureEvictionPodGracePeriodSeconds, "cpu-pressure-eviction-pod-grace-period-seconds",
		o.CPUPressureEvictionPodGracePeriodSeconds,
		"the ratio between the times metric value over the bound value and the metric ring size is greater than this percentage "+
			", the eviction or node taint will be triggered")
	fs.IntVar(&o.MetricRingSize, "cpu-pressure-eviction-metric-ring-size",
		o.MetricRingSize,
		"capacity of metric ring")
	fs.DurationVar(&o.CPUPressureEvictionSyncPeriod, "cpu-pressure-eviction-sync-period",
		o.CPUPressureEvictionSyncPeriod, "cpu pressure eviction syncing period")
	fs.DurationVar(&o.CPUPressureEvictionColdPeriod, "cpu-pressure-eviction-cold-period",
		o.CPUPressureEvictionColdPeriod, "specify a cold period after eviction")
	fs.Float64Var(&o.MaxCPUSuppressionToleranceRate, "max-cpu-suppression-tolerance-rate",
		o.MaxCPUSuppressionToleranceRate, "the maximum cpu suppression tolerance rate that can be set by the pod")
}

// ApplyTo applies CPUPressureEvictionPluginOptions to CPUPressureEvictionPluginConfiguration
func (o *CPUPressureEvictionPluginOptions) ApplyTo(c *evictionconfig.CPUPressureEvictionPluginConfiguration) error {
	c.EnableCPUPressureEviction = o.EnableCPUPressureEviction
	c.LoadUpperBoundRatio = o.LoadUpperBoundRatio
	c.LoadThresholdMetPercentage = o.LoadThresholdMetPercentage
	c.CPUPressureEvictionPodGracePeriodSeconds = o.CPUPressureEvictionPodGracePeriodSeconds
	c.MetricRingSize = o.MetricRingSize
	c.CPUPressureEvictionSyncPeriod = o.CPUPressureEvictionSyncPeriod
	c.CPUPressureEvictionColdPeriod = o.CPUPressureEvictionColdPeriod
	c.MaxCPUSuppressionToleranceRate = o.MaxCPUSuppressionToleranceRate
	return nil
}
