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
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
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
)

var (
	// FakeEvictionRankingMetrics is fake metrics to rank pods
	FakeEvictionRankingMetrics = []string{FakeMetricQoSLevel, FakeMetricPriority}
	// DefaultNumaEvictionRankingMetrics is the default metrics used to rank pods for eviction at the NUMA level
	DefaultNumaEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricsMemTotalPerNumaContainer)
	// DefaultSystemEvictionRankingMetrics is the default metrics used to rank pods for eviction at the system level
	DefaultSystemEvictionRankingMetrics = append(FakeEvictionRankingMetrics, consts.MetricMemUsageContainer)
)

// MemoryPressureEvictionPluginConfiguration is the config of MemoryPressureEvictionPlugin
type MemoryPressureEvictionPluginConfiguration struct {
	EnableNumaLevelDetection             bool
	EnableSystemLevelDetection           bool
	NumaFreeBelowWatermarkTimesThreshold int
	SystemKswapdRateThreshold            int
	SystemKswapdRateExceedTimesThreshold int
	NumaEvictionRankingMetrics           []string
	SystemEvictionRankingMetrics         []string
	GracePeriod                          int64
}

// NewMemoryPressureEvictionPluginConfiguration returns a new MemoryPressureEvictionPluginConfiguration
func NewMemoryPressureEvictionPluginConfiguration() *MemoryPressureEvictionPluginConfiguration {
	return &MemoryPressureEvictionPluginConfiguration{}
}

// ApplyConfiguration applies dynamic.DynamicConfigCRD to MemoryPressureEvictionPluginConfiguration
func (c *MemoryPressureEvictionPluginConfiguration) ApplyConfiguration(conf *dynamic.DynamicConfigCRD) {
	if kac := conf.KatalystAgentConfig; kac != nil {
		if kac.Spec.Config.MemoryEvictionPluginConfig.EnableNumaLevelDetection != nil {
			c.EnableNumaLevelDetection = *(kac.Spec.Config.MemoryEvictionPluginConfig.EnableNumaLevelDetection)
		}

		if kac.Spec.Config.MemoryEvictionPluginConfig.EnableSystemLevelDetection != nil {
			c.EnableSystemLevelDetection = *(kac.Spec.Config.MemoryEvictionPluginConfig.EnableSystemLevelDetection)
		}

		if kac.Spec.Config.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold != nil {
			c.NumaFreeBelowWatermarkTimesThreshold = *(kac.Spec.Config.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold)
		}

		if kac.Spec.Config.MemoryEvictionPluginConfig.SystemKswapdRateThreshold != nil {
			c.SystemKswapdRateThreshold = *(kac.Spec.Config.MemoryEvictionPluginConfig.SystemKswapdRateThreshold)
		}

		if kac.Spec.Config.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold != nil {
			c.SystemKswapdRateExceedTimesThreshold = *(kac.Spec.Config.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold)
		}

		if len(kac.Spec.Config.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics) > 0 {
			c.NumaEvictionRankingMetrics = kac.Spec.Config.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics
		}

		if len(kac.Spec.Config.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics) > 0 {
			c.SystemEvictionRankingMetrics = kac.Spec.Config.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics
		}

		if kac.Spec.Config.MemoryEvictionPluginConfig.GracePeriod != nil {
			c.GracePeriod = *(kac.Spec.Config.MemoryEvictionPluginConfig.GracePeriod)
		}
	}
}
