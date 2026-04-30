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
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type CPUSystemPressureEvictionOptions struct {
	EnableCPUSystemEviction    bool
	SystemLoadUpperBoundRatio  float64
	SystemLoadLowerBoundRatio  float64
	SystemUsageUpperBoundRatio float64
	SystemUsageLowerBoundRatio float64
	ThresholdMetPercentage     float64
	MetricRingSize             int
	EvictionCoolDownTime       time.Duration
	EvictionRankingMetrics     []string
	GracePeriod                int64
	CheckCPUManager            bool
	SystemEvictionMetricMode   string
}

func NewCPUSystemPressureEvictionOptions() *CPUSystemPressureEvictionOptions {
	return &CPUSystemPressureEvictionOptions{
		EnableCPUSystemEviction:    false,
		SystemLoadUpperBoundRatio:  0,
		SystemLoadLowerBoundRatio:  0,
		SystemUsageUpperBoundRatio: 0,
		SystemUsageLowerBoundRatio: 0,
		ThresholdMetPercentage:     0.8,
		MetricRingSize:             10,
		EvictionCoolDownTime:       300 * time.Second,
		EvictionRankingMetrics:     eviction.DefaultEvictionRankingMetrics,
		GracePeriod:                -1,
		CheckCPUManager:            false,
		SystemEvictionMetricMode:   string(eviction.NodeMetric),
	}
}

func (s *CPUSystemPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-cpu-system-pressure")

	fs.BoolVar(&s.EnableCPUSystemEviction, "enable-cpu-system-eviction", s.EnableCPUSystemEviction, "whether to enable cpu system eviction")
	fs.Float64Var(&s.SystemLoadUpperBoundRatio, "system-load-upper-bound-ratio", s.SystemLoadUpperBoundRatio, "the upper bound ratio of system load pressure")
	fs.Float64Var(&s.SystemLoadLowerBoundRatio, "system-load-lower-bound-ratio", s.SystemLoadLowerBoundRatio, "the lower bound ratio of system load pressure")
	fs.Float64Var(&s.SystemUsageUpperBoundRatio, "system-usage-upper-bound-ratio", s.SystemUsageUpperBoundRatio, "the upper bound ratio of system usage pressure")
	fs.Float64Var(&s.SystemUsageLowerBoundRatio, "system-usage-lower-bound-ratio", s.SystemUsageLowerBoundRatio, "the lower bound ratio of system usage pressure")
	fs.Float64Var(&s.ThresholdMetPercentage, "cpu-system-eviction-threshold-met-percentage", s.ThresholdMetPercentage,
		"when the ratio of metrics which is greater than threshold is greater than this percentage, it met the threshold")
	fs.IntVar(&s.MetricRingSize, "cpu-system-eviction-metric-ring-size", s.MetricRingSize, "how many latest metrics records the plugin will store")
	fs.DurationVar(&s.EvictionCoolDownTime, "cpu-system-eviction-cool-down-time", s.EvictionCoolDownTime, "the cool down time of the plugin evict pods")
	fs.StringSliceVar(&s.EvictionRankingMetrics, "cpu-system-eviction-ranking-metrics", s.EvictionRankingMetrics, "the metrics used to rank pods for eviction")
	fs.Int64Var(&s.GracePeriod, "cpu-system-eviction-grace-period", s.GracePeriod, "the grace period of pod deletion")
	fs.BoolVar(&s.CheckCPUManager, "cpu-system-eviction-check-cpu-manager", s.CheckCPUManager, "whether to check cpu manager and filter guaranteed pods")
	fs.StringVar(&s.SystemEvictionMetricMode, "cpu-system-eviction-metric-mode", s.SystemEvictionMetricMode, "the metric mode for cpu system eviction plugin, e.g. NodeMetric or PodAggregatedMetric")
}

func (s *CPUSystemPressureEvictionOptions) ApplyTo(c *eviction.CPUSystemPressureEvictionPluginConfiguration) error {
	c.EnableCPUSystemEviction = s.EnableCPUSystemEviction
	c.SystemLoadUpperBoundRatio = s.SystemLoadUpperBoundRatio
	c.SystemLoadLowerBoundRatio = s.SystemLoadLowerBoundRatio
	c.SystemUsageUpperBoundRatio = s.SystemUsageUpperBoundRatio
	c.SystemUsageLowerBoundRatio = s.SystemUsageLowerBoundRatio
	c.ThresholdMetPercentage = s.ThresholdMetPercentage
	c.MetricRingSize = s.MetricRingSize
	c.EvictionCoolDownTime = s.EvictionCoolDownTime
	c.EvictionRankingMetrics = s.EvictionRankingMetrics
	c.GracePeriod = s.GracePeriod
	c.CheckCPUManager = s.CheckCPUManager
	c.SystemEvictionMetricMode = eviction.SystemEvictionMetricMode(s.SystemEvictionMetricMode)
	return nil
}
