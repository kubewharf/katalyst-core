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
	"sync"

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
	DynamicConf *MemoryPressureEvictionPluginDynamicConfiguration
}

// NewMemoryPressureEvictionPluginConfiguration returns a new MemoryPressureEvictionPluginConfiguration
func NewMemoryPressureEvictionPluginConfiguration() *MemoryPressureEvictionPluginConfiguration {
	return &MemoryPressureEvictionPluginConfiguration{
		DynamicConf: NewMemoryPressureEvictionPluginDynamicConfiguration(),
	}
}

func (c *MemoryPressureEvictionPluginConfiguration) ApplyConfiguration(configuration *MemoryPressureEvictionPluginConfiguration,
	conf *dynamic.DynamicConfigCRD) {
	c.DynamicConf.ApplyConfiguration(configuration.DynamicConf, conf)
}

type MemoryPressureEvictionPluginDynamicConfiguration struct {
	mutex                                sync.RWMutex
	enableNumaLevelDetection             bool
	enableSystemLevelDetection           bool
	numaFreeBelowWatermarkTimesThreshold int
	systemKswapdRateThreshold            int
	systemKswapdRateExceedTimesThreshold int
	numaEvictionRankingMetrics           []string
	systemEvictionRankingMetrics         []string
	gracePeriod                          int64
}

func NewMemoryPressureEvictionPluginDynamicConfiguration() *MemoryPressureEvictionPluginDynamicConfiguration {
	return &MemoryPressureEvictionPluginDynamicConfiguration{}
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) EnableNumaLevelDetection() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.enableNumaLevelDetection
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetEnableNumaLevelDetection(enableNumaLevelDetection bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.enableNumaLevelDetection = enableNumaLevelDetection
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) EnableSystemLevelDetection() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.enableSystemLevelDetection
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetEnableSystemLevelDetection(enableSystemLevelDetection bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.enableSystemLevelDetection = enableSystemLevelDetection
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) NumaFreeBelowWatermarkTimesThreshold() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.numaFreeBelowWatermarkTimesThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetNumaFreeBelowWatermarkTimesThreshold(numaFreeBelowWatermarkTimesThreshold int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.numaFreeBelowWatermarkTimesThreshold = numaFreeBelowWatermarkTimesThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SystemKswapdRateThreshold() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.systemKswapdRateThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetSystemKswapdRateThreshold(systemKswapdRateThreshold int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.systemKswapdRateThreshold = systemKswapdRateThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SystemKswapdRateExceedTimesThreshold() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.systemKswapdRateExceedTimesThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetSystemKswapdRateExceedTimesThreshold(systemKswapdRateExceedTimesThreshold int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.systemKswapdRateExceedTimesThreshold = systemKswapdRateExceedTimesThreshold
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) NumaEvictionRankingMetrics() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.numaEvictionRankingMetrics
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetNumaEvictionRankingMetrics(numaEvictionRankingMetrics []string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.numaEvictionRankingMetrics = numaEvictionRankingMetrics
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SystemEvictionRankingMetrics() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.systemEvictionRankingMetrics
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetSystemEvictionRankingMetrics(systemEvictionRankingMetrics []string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.systemEvictionRankingMetrics = systemEvictionRankingMetrics
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) GracePeriod() int64 {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.gracePeriod
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) SetGracePeriod(gracePeriod int64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.gracePeriod = gracePeriod
}

// ApplyConfiguration applies dynamic.DynamicConfigCRD to MemoryPressureEvictionPluginConfiguration
func (c *MemoryPressureEvictionPluginDynamicConfiguration) ApplyConfiguration(defaultConf *MemoryPressureEvictionPluginDynamicConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.applyDefault(defaultConf)
	if ec := conf.EvictionConfiguration; ec != nil {
		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableNumaLevelDetection != nil {
			c.enableNumaLevelDetection = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableNumaLevelDetection)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableSystemLevelDetection != nil {
			c.enableSystemLevelDetection = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.EnableSystemLevelDetection)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold != nil {
			c.numaFreeBelowWatermarkTimesThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaFreeBelowWatermarkTimesThreshold)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateThreshold != nil {
			c.systemKswapdRateThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateThreshold)
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold != nil {
			c.systemKswapdRateExceedTimesThreshold = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemKswapdRateExceedTimesThreshold)
		}

		if len(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics) > 0 {
			c.numaEvictionRankingMetrics = ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.NumaEvictionRankingMetrics
		}

		if len(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics) > 0 {
			c.systemEvictionRankingMetrics = ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.SystemEvictionRankingMetrics
		}

		if ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.GracePeriod != nil {
			c.gracePeriod = *(ec.Spec.Config.EvictionPluginsConfig.MemoryEvictionPluginConfig.GracePeriod)
		}
	}
}

func (c *MemoryPressureEvictionPluginDynamicConfiguration) applyDefault(defaultConf *MemoryPressureEvictionPluginDynamicConfiguration) {
	c.enableNumaLevelDetection = defaultConf.enableNumaLevelDetection
	c.enableSystemLevelDetection = defaultConf.enableSystemLevelDetection
	c.numaFreeBelowWatermarkTimesThreshold = defaultConf.numaFreeBelowWatermarkTimesThreshold
	c.systemKswapdRateThreshold = defaultConf.systemKswapdRateThreshold
	c.systemKswapdRateExceedTimesThreshold = defaultConf.systemKswapdRateExceedTimesThreshold
	c.numaEvictionRankingMetrics = defaultConf.numaEvictionRankingMetrics
	c.systemEvictionRankingMetrics = defaultConf.systemEvictionRankingMetrics
	c.gracePeriod = defaultConf.gracePeriod
}
