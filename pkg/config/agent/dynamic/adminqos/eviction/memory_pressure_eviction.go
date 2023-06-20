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
	// DefaultNumaFreeBelowWatermarkTimesThreshold is the default threshold for the number of times
	// that NUMA's free memory falls below the watermark
	DefaultNumaFreeBelowWatermarkTimesThreshold = 4
	// DefaultSystemKswapdRateThreshold is the default threshold for the rate of kswapd reclaiming rate
	DefaultSystemKswapdRateThreshold = 2000
	// DefaultSystemKswapdRateExceedTimesThreshold is the default threshold for the number of times
	// that the kswapd reclaiming rate exceeds the threshold
	DefaultSystemKswapdRateExceedTimesThreshold = 4
	// DefaultGracePeriod is the default value of grace period
	DefaultGracePeriod int64 = -1
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
	EnableNumaLevelEviction              bool
	EnableSystemLevelEviction            bool
	NumaFreeBelowWatermarkTimesThreshold int
	SystemKswapdRateThreshold            int
	SystemKswapdRateExceedTimesThreshold int
	NumaEvictionRankingMetrics           []string
	SystemEvictionRankingMetrics         []string
	EnableRSSOveruseEviction             bool
	RSSOveruseRateThreshold              float64
	GracePeriod                          int64
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

		if config.NumaFreeBelowWatermarkTimesThreshold != nil {
			c.NumaFreeBelowWatermarkTimesThreshold = *(config.NumaFreeBelowWatermarkTimesThreshold)
		}

		if config.SystemKswapdRateThreshold != nil {
			c.SystemKswapdRateThreshold = *(config.SystemKswapdRateThreshold)
		}

		if config.SystemKswapdRateExceedTimesThreshold != nil {
			c.SystemKswapdRateExceedTimesThreshold = *(config.SystemKswapdRateExceedTimesThreshold)
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

		if config.EnableRSSOveruseEviction != nil {
			c.EnableRSSOveruseEviction = *(config.EnableRSSOveruseEviction)
		}

		if config.RSSOveruseRateThreshold != nil {
			c.RSSOveruseRateThreshold = *(config.RSSOveruseRateThreshold)
		}
	}
}
