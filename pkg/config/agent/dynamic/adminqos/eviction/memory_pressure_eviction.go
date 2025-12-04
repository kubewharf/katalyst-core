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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

// Fake metrics are not fetched from meta-server
const (
	FakeMetricQoSLevel = "qos.pod"
	FakeMetricPriority = "priority.pod"
)

const (
	// DefaultEnableNumaLevelEviction is the default value of whether enable numa-level eviction
	DefaultEnableNumaLevelEviction = true
	// DefaultEnableSystemLevelEviction is the default value of whether enable system-level eviction
	DefaultEnableSystemLevelEviction = true
	// DefaultNumaVictimMinimumUtilizationThreshold is the victim's minimum memory usage on a NUMA node, if a pod
	// uses less memory on a NUMA node than this threshold,it won't be evicted by this NUMA's memory pressure.
	DefaultNumaVictimMinimumUtilizationThreshold = 0.001
	// DefaultNumaFreeBelowWatermarkTimesThreshold is the default threshold for the number of times
	// that NUMA's free memory falls below the watermark
	DefaultNumaFreeBelowWatermarkTimesThreshold = 4
	// DefaultNumaFreeBelowWatermarkTimesReclaimedThreshold is the default threshold for the number of times
	// that NUMA's free memory of the reclaimed instance falls below the watermark.
	DefaultNumaFreeBelowWatermarkTimesReclaimedThreshold = 2
	// DefaultNumaFreeConstraintFastEvictionWaitCycle is the waiting cycle when memory is tight and fast eviction is needed.
	DefaultNumaFreeConstraintFastEvictionWaitCycle = 1
	// DefaultSystemFreeMemoryThresholdMinimum is the minimum of free memory threshold.
	DefaultSystemFreeMemoryThresholdMinimum = "0Gi"
	// DefaultSystemKswapdRateThreshold is the default threshold for the rate of kswapd reclaiming rate
	DefaultSystemKswapdRateThreshold = 2000
	// DefaultSystemKswapdRateExceedTimesThreshold is the default threshold for the number of times
	// that the kswapd reclaiming rate exceeds the threshold
	DefaultSystemKswapdRateExceedDurationThreshold = 120
	// DefaultGracePeriod is the default value of grace period
	DefaultGracePeriod int64 = -1
	// DefaultReclaimedGracePeriod is the default value of grace period
	DefaultReclaimedGracePeriod int64 = 1
	// DefaultEnableRssOveruseDetection is the default value of whether enable pod-level rss overuse detection
	DefaultEnableRssOveruseDetection = false
	// DefaultRSSOveruseRateThreshold is the default threshold for the rate of rss
	DefaultRSSOveruseRateThreshold = 1.05
)

var (
	// FakeEvictionRankingMetrics is fake metrics to rank pods
	FakeEvictionRankingMetrics = []string{FakeMetricQoSLevel, FakeMetricPriority}
	// DefaultNumaEvictionRankingMetrics is the default metrics used to rank pods for eviction at the NUMA level
	DefaultNumaEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricsMemTotalPerNumaContainer)
	// DefaultSystemEvictionRankingMetrics is the default metrics used to rank pods for eviction at the system level
	DefaultSystemEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricMemUsageContainer)
)

type MemoryPressureEvictionConfiguration struct {
	EnableNumaLevelEviction                       bool
	EnableSystemLevelEviction                     bool
	NumaVictimMinimumUtilizationThreshold         float64
	NumaFreeBelowWatermarkTimesThreshold          int
	NumaFreeBelowWatermarkTimesReclaimedThreshold int
	NumaFreeConstraintFastEvictionWaitCycle       int
	SystemFreeMemoryThresholdMinimum              int64
	SystemKswapdRateThreshold                     int
	SystemKswapdRateExceedDurationThreshold       int
	NumaEvictionRankingMetrics                    []string
	SystemEvictionRankingMetrics                  []string
	EnableRSSOveruseEviction                      bool
	RSSOveruseRateThreshold                       float64
	GracePeriod                                   int64
	ReclaimedGracePeriod                          int64
	EvictNonReclaimedAnnotationSelector           string
	EvictNonReclaimedLabelSelector                string
}

func NewMemoryPressureEvictionPluginConfiguration() *MemoryPressureEvictionConfiguration {
	return &MemoryPressureEvictionConfiguration{}
}

// ApplyConfiguration applies dynamic.DynamicConfigCRD to MemoryPressureEvictionConfiguration
func (c *MemoryPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.MemoryPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.MemoryPressureEvictionConfig
		if config.EnableNumaLevelEviction != nil {
			c.EnableNumaLevelEviction = *(config.EnableNumaLevelEviction)
		}

		if config.EnableSystemLevelEviction != nil {
			c.EnableSystemLevelEviction = *(config.EnableSystemLevelEviction)
		}

		if config.NumaVictimMinimumUtilizationThreshold != nil {
			c.NumaVictimMinimumUtilizationThreshold = *(config.NumaVictimMinimumUtilizationThreshold)
		}

		if config.NumaFreeBelowWatermarkTimesThreshold != nil {
			c.NumaFreeBelowWatermarkTimesThreshold = *(config.NumaFreeBelowWatermarkTimesThreshold)
		}

		if config.NumaFreeBelowWatermarkTimesReclaimedThreshold != nil {
			c.NumaFreeBelowWatermarkTimesReclaimedThreshold = *(config.NumaFreeBelowWatermarkTimesReclaimedThreshold)
		}

		if config.NumaFreeConstraintFastEvictionWaitCycle != nil {
			c.NumaFreeConstraintFastEvictionWaitCycle = *(config.NumaFreeConstraintFastEvictionWaitCycle)
		}

		if config.SystemFreeMemoryThresholdMinimum != nil {
			c.SystemFreeMemoryThresholdMinimum = config.SystemFreeMemoryThresholdMinimum.Value()
		}

		if config.SystemKswapdRateThreshold != nil {
			c.SystemKswapdRateThreshold = *(config.SystemKswapdRateThreshold)
		}

		if config.SystemKswapdRateExceedDurationThreshold != nil {
			c.SystemKswapdRateExceedDurationThreshold = *(config.SystemKswapdRateExceedDurationThreshold)
		}

		if len(config.NumaEvictionRankingMetrics) > 0 {
			c.NumaEvictionRankingMetrics = util.ConvertNumaEvictionRankingMetricsToStringList(config.NumaEvictionRankingMetrics)
		}

		if len(config.SystemEvictionRankingMetrics) > 0 {
			c.SystemEvictionRankingMetrics = util.ConvertSystemEvictionRankingMetricsToStringList(config.SystemEvictionRankingMetrics)
		}

		if config.GracePeriod != nil {
			c.GracePeriod = *(config.GracePeriod)
		}

		if config.ReclaimedGracePeriod != nil {
			c.ReclaimedGracePeriod = *(config.ReclaimedGracePeriod)
		}

		if config.EnableRSSOveruseEviction != nil {
			c.EnableRSSOveruseEviction = *(config.EnableRSSOveruseEviction)
		}

		if config.RSSOveruseRateThreshold != nil {
			c.RSSOveruseRateThreshold = *(config.RSSOveruseRateThreshold)
		}

		if len(config.EvictNonReclaimedAnnotationSelector) > 0 {
			c.EvictNonReclaimedAnnotationSelector = config.EvictNonReclaimedAnnotationSelector
		}

		if len(config.EvictNonReclaimedLabelSelector) > 0 {
			c.EvictNonReclaimedLabelSelector = config.EvictNonReclaimedLabelSelector
		}
	}
}
