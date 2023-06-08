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
)

// Fake metrics are not fetched from meta-server
const (
	FakeMetricQoSLevel = "qos.pod"
	FakeMetricPriority = "priority.pod"
)

const (
	// DefaultEnableNumaLevelDetection is the default value of whether enable numa-level detection
	DefaultEnableNumaLevelDetection = true
	// DefaultEnableSystemLevelDetection is the default value of whether enable system-level detection
	DefaultEnableSystemLevelDetection = true
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
	// DefaultRssOveruseRateThreshold is the default threshold for the rate of rss
	DefaultRssOveruseRateThreshold = 1.05
)

var (
	// FakeEvictionRankingMetrics is fake metrics to rank pods
	FakeEvictionRankingMetrics = []string{FakeMetricQoSLevel, FakeMetricPriority}
	// DefaultNumaEvictionRankingMetrics is the default metrics used to rank pods for eviction at the NUMA level
	DefaultNumaEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricsMemTotalPerNumaContainer)
	// DefaultSystemEvictionRankingMetrics is the default metrics used to rank pods for eviction at the system level
	DefaultSystemEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricMemUsageContainer)
)

type MemoryPressureEvictionPluginConfiguration struct {
	EnableNumaLevelDetection             bool
	EnableSystemLevelDetection           bool
	NumaFreeBelowWatermarkTimesThreshold int
	SystemKswapdRateThreshold            int
	SystemKswapdRateExceedTimesThreshold int
	NumaEvictionRankingMetrics           []string
	SystemEvictionRankingMetrics         []string
	GracePeriod                          int64

	EnableRssOveruseDetection bool
	RssOveruseRateThreshold   float64
}

func NewMemoryPressureEvictionPluginConfiguration() *MemoryPressureEvictionPluginConfiguration {
	return &MemoryPressureEvictionPluginConfiguration{}
}

// ApplyConfiguration applies dynamic.DynamicConfigCRD to MemoryPressureEvictionPluginConfiguration
func (c *MemoryPressureEvictionPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if ec := conf.EvictionConfiguration; ec != nil {
		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableNumaLevelDetection != nil {
			c.EnableNumaLevelDetection = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableNumaLevelDetection)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableSystemLevelDetection != nil {
			c.EnableSystemLevelDetection = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableSystemLevelDetection)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold != nil {
			c.NumaFreeBelowWatermarkTimesThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateThreshold != nil {
			c.SystemKswapdRateThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateThreshold)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold != nil {
			c.SystemKswapdRateExceedTimesThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold)
		}

		if len(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics) > 0 {
			c.NumaEvictionRankingMetrics = ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics
		}

		if len(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics) > 0 {
			c.SystemEvictionRankingMetrics = ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.GracePeriod != nil {
			c.GracePeriod = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.GracePeriod)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableRssOveruseDetection != nil {
			c.EnableRssOveruseDetection = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableRssOveruseDetection)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.RssOveruseRateThreshold != nil {
			c.RssOveruseRateThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.RssOveruseRateThreshold)
		}
	}
}
