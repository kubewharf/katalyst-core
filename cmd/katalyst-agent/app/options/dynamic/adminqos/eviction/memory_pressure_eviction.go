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

// MemoryPressureEvictionOptions is the options of MemoryPressureEviction
type MemoryPressureEvictionOptions struct {
	EnableNumaLevelEviction                 bool
	EnableSystemLevelEviction               bool
	NumaVictimMinimumUtilizationThreshold   float64
	NumaFreeBelowWatermarkTimesThreshold    int
	SystemKswapdRateThreshold               int
	SystemKswapdRateExceedDurationThreshold int
	NumaEvictionRankingMetrics              []string
	SystemEvictionRankingMetrics            []string
	GracePeriod                             int64
	EnableRSSOveruseEviction                bool
	RSSOveruseRateThreshold                 float64
}

// NewMemoryPressureEvictionOptions returns a new MemoryPressureEvictionOptions
func NewMemoryPressureEvictionOptions() *MemoryPressureEvictionOptions {
	return &MemoryPressureEvictionOptions{
		EnableNumaLevelEviction:                 eviction.DefaultEnableNumaLevelEviction,
		EnableSystemLevelEviction:               eviction.DefaultEnableSystemLevelEviction,
		NumaVictimMinimumUtilizationThreshold:   eviction.DefaultNumaVictimMinimumUtilizationThreshold,
		NumaFreeBelowWatermarkTimesThreshold:    eviction.DefaultNumaFreeBelowWatermarkTimesThreshold,
		SystemKswapdRateThreshold:               eviction.DefaultSystemKswapdRateThreshold,
		SystemKswapdRateExceedDurationThreshold: eviction.DefaultSystemKswapdRateExceedDurationThreshold,
		NumaEvictionRankingMetrics:              eviction.DefaultNumaEvictionRankingMetrics,
		SystemEvictionRankingMetrics:            eviction.DefaultSystemEvictionRankingMetrics,
		GracePeriod:                             eviction.DefaultGracePeriod,
		EnableRSSOveruseEviction:                eviction.DefaultEnableRssOveruseDetection,
		RSSOveruseRateThreshold:                 eviction.DefaultRSSOveruseRateThreshold,
	}
}

// AddFlags parses the flags to MemoryPressureEvictionOptions
func (o *MemoryPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-memory-pressure")

	fs.BoolVar(&o.EnableNumaLevelEviction, "eviction-enable-numa-level", o.EnableNumaLevelEviction,
		"whether to enable numa-level eviction")
	fs.BoolVar(&o.EnableSystemLevelEviction, "eviction-enable-system-level", o.EnableSystemLevelEviction,
		"whether to enable system-level eviction")
	fs.Float64Var(&o.NumaVictimMinimumUtilizationThreshold, "eviction-numa-victim-minimum-utilization-threshold", o.NumaVictimMinimumUtilizationThreshold,
		"the threshold for the victim's minimum memory utilization on a NUMA node")
	fs.IntVar(&o.NumaFreeBelowWatermarkTimesThreshold, "eviction-numa-free-below-watermark-times-threshold", o.NumaFreeBelowWatermarkTimesThreshold,
		"the threshold for the number of times NUMA's free memory falls below the watermark")
	fs.IntVar(&o.SystemKswapdRateThreshold, "eviction-system-kswapd-rate-threshold", o.SystemKswapdRateThreshold,
		"the threshold for the rate of kswapd reclaiming rate")
	fs.IntVar(&o.SystemKswapdRateExceedDurationThreshold, "eviction-system-kswapd-rate-exceed-duration-threshold", o.SystemKswapdRateExceedDurationThreshold,
		"the threshold for the duration the kswapd reclaiming rate exceeds the threshold")
	fs.StringSliceVar(&o.NumaEvictionRankingMetrics, "eviction-numa-ranking-metrics", o.NumaEvictionRankingMetrics,
		"the metrics used to rank pods for eviction at the NUMA level")
	fs.StringSliceVar(&o.SystemEvictionRankingMetrics, "eviction-system-ranking-metrics", o.SystemEvictionRankingMetrics,
		"the metrics used to rank pods for eviction at the system level")
	fs.Int64Var(&o.GracePeriod, "eviction-memory-grace-period", o.GracePeriod,
		"the grace period of memory pressure eviction")
	fs.BoolVar(&o.EnableRSSOveruseEviction, "eviction-enable-rss-overuse", o.EnableRSSOveruseEviction,
		"whether to enable pod-level rss overuse eviction")
	fs.Float64Var(&o.RSSOveruseRateThreshold, "eviction-rss-overuse-rate-threshold", o.RSSOveruseRateThreshold,
		"the threshold for the rate of rss overuse threshold")
}

// ApplyTo applies MemoryPressureEvictionOptions to MemoryPressureEvictionConfiguration
func (o *MemoryPressureEvictionOptions) ApplyTo(c *eviction.MemoryPressureEvictionConfiguration) error {
	c.EnableNumaLevelEviction = o.EnableNumaLevelEviction
	c.EnableSystemLevelEviction = o.EnableSystemLevelEviction
	c.NumaVictimMinimumUtilizationThreshold = o.NumaVictimMinimumUtilizationThreshold
	c.NumaFreeBelowWatermarkTimesThreshold = o.NumaFreeBelowWatermarkTimesThreshold
	c.SystemKswapdRateThreshold = o.SystemKswapdRateThreshold
	c.SystemKswapdRateExceedDurationThreshold = o.SystemKswapdRateExceedDurationThreshold
	c.NumaEvictionRankingMetrics = o.NumaEvictionRankingMetrics
	c.SystemEvictionRankingMetrics = o.SystemEvictionRankingMetrics
	c.GracePeriod = o.GracePeriod
	c.EnableRSSOveruseEviction = o.EnableRSSOveruseEviction
	c.RSSOveruseRateThreshold = o.RSSOveruseRateThreshold

	return nil
}
