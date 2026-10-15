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
	"math"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
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
	DefaultNumaFreeBelowWatermarkTimesThreshold = 12
	// DefaultNumaFreeBelowWatermarkTimesReclaimedThreshold is the default threshold for the number of times
	// that NUMA's free memory of the reclaimed instance falls below the watermark.
	DefaultNumaFreeBelowWatermarkTimesReclaimedThreshold = 6
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
	// DefaultNumaEvictionRankingMetrics is the default metrics used to rank pods for eviction at the NUMA level
	DefaultNumaEvictionRankingMetrics = append(DefaultEvictionRankingMetrics, consts.MetricsMemTotalPerNumaContainer)
	// DefaultSystemEvictionRankingMetrics is the default metrics used to rank pods for eviction at the system level
	DefaultSystemEvictionRankingMetrics = append(DefaultEvictionRankingMetrics, consts.MetricMemUsageContainer)
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

	// EnableReclaimedMemorySafeZoneGate controls whether the reclaimed-memory
	// NUMA safe-zone gate may really skip actionReclaimedEviction on a NUMA when
	// the memory held by reclaimed_cores pods on that NUMA is small relative to
	// the NUMA's total memory (mem.total.numa). It defaults to false. The gate
	// state machine is keyed off (EnableReclaimedMemorySafeZoneGate,
	// ReclaimedMemoryUsageRatioThreshold) and has three closed/shadow/armed
	// states:
	//   - ratio <= 0:   fully closed. The safe zone is not even evaluated: no
	//                   observation, no would-skip decision, and no skip.
	//                   gate_state=closed.
	//   - ratio > 0 && Enable == false: auto-shadow arm. The safe-zone decision is
	//                   evaluated and a would-skip hit is recorded
	//                   (auto_shadow_would_skip) but never applied.
	//                   gate_state=auto_shadow. A nil CR field resolves to
	//                   false here.
	//   - ratio > 0 && Enable == true:  the only arm that really skips on a low
	//                   reclaimed-usage ratio. gate_state=enabled.
	// There is no "unknown" mode: the arm is a plain bool, so an omitted / old-CR
	// field always means "do not really skip".
	//
	// Regardless of arm, danger watermarks (free < min, or additional pressure)
	// always bypass the gate, and actionEviction (evicting all pods) is never
	// affected by it. Every failure path (missing watermark snapshot, metric /
	// QoS errors, invalid threshold) fails safe by NOT skipping, i.e. falling
	// back to the regular eviction path.
	EnableReclaimedMemorySafeZoneGate bool
	// ReclaimedMemoryUsageRatioThreshold is the fraction of a NUMA's total memory
	// (mem.total.numa) currently held by reclaimed_cores pods (sum of
	// mem.total.numa.container over reclaimed pods on that NUMA) below which the
	// safe-zone skip fires when the gate is armed. It must be a finite value
	// within [0,1]; a non-positive value (the default 0.0) keeps the gate fully
	// closed regardless of EnableReclaimedMemorySafeZoneGate, and an invalid
	// (NaN / Inf / out-of-range) value fails closed to 0.0.
	ReclaimedMemoryUsageRatioThreshold float64
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
			c.NumaEvictionRankingMetrics = ConvertNumaEvictionRankingMetricsToStringList(config.NumaEvictionRankingMetrics)
		}

		if len(config.SystemEvictionRankingMetrics) > 0 {
			c.SystemEvictionRankingMetrics = ConvertSystemEvictionRankingMetricsToStringList(config.SystemEvictionRankingMetrics)
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

		// A nil CR field (omitted / old CR) leaves the default false untouched,
		// which is the auto-shadow / do-not-really-skip arm once the ratio is
		// positive.
		if config.EnableReclaimedMemorySafeZoneGate != nil {
			c.EnableReclaimedMemorySafeZoneGate = *config.EnableReclaimedMemorySafeZoneGate
		}

		if config.ReclaimedMemoryUsageRatioThreshold != nil {
			threshold := *config.ReclaimedMemoryUsageRatioThreshold
			if validReclaimedMemoryUsageRatio(threshold) {
				c.ReclaimedMemoryUsageRatioThreshold = threshold
			} else {
				// Fail closed: an invalid (NaN / Inf / outside [0,1]) threshold
				// must not arm the gate. Reset to the closed default 0.0 and log
				// observably; this never panics and never changes default
				// behavior.
				general.Warningf("ignore invalid ReclaimedMemoryUsageRatioThreshold %v: not finite within [0,1], reclaimed-memory safe-zone gate stays closed", threshold)
				c.ReclaimedMemoryUsageRatioThreshold = 0.0
			}
		}
	}
}

// validReclaimedMemoryUsageRatio reports whether r is a finite fraction within
// the documented [0,1] range. NaN, Inf and out-of-range values are invalid and
// make the gate fail closed rather than being silently armed.
func validReclaimedMemoryUsageRatio(r float64) bool {
	return !math.IsNaN(r) && !math.IsInf(r, 0) && r >= 0 && r <= 1
}
