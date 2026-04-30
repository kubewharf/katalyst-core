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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type SystemEvictionMetricMode string

const (
	// NodeMetric represents the metric mode that fetches metrics directly from node
	NodeMetric SystemEvictionMetricMode = "NodeMetric"
	// PodAggregatedMetric represents the metric mode that calculates node metric by aggregating all pods' metrics
	PodAggregatedMetric SystemEvictionMetricMode = "PodAggregatedMetric"
)

type CPUSystemPressureEvictionPluginConfiguration struct {
	// EnableCPUSystemEviction represents whether to enable cpu system eviction
	EnableCPUSystemEviction bool
	// SystemLoadUpperBoundRatio is the upper bound ratio of system load pressure, if it is set to 0, it means load pressure eviction is disabled
	SystemLoadUpperBoundRatio float64
	// SystemLoadLowerBoundRatio is the lower bound ratio of system load pressure
	SystemLoadLowerBoundRatio float64
	// SystemUsageUpperBoundRatio is the upper bound ratio of system usage pressure, if it is set to 0, it means usage pressure eviction is disabled
	SystemUsageUpperBoundRatio float64
	// SystemUsageLowerBoundRatio is the lower bound ratio of system usage pressure
	SystemUsageLowerBoundRatio float64
	// ThresholdMetPercentage means the plugin considers the node is facing pressure only when the ratio of metric history which is greater than
	// threshold is greater than this percentage
	ThresholdMetPercentage float64
	// MetricRingSize is the size of the metric ring, which is used to calculate the system pressure
	MetricRingSize int
	// EvictionCoolDownTime is the cool-down time of the plugin evict pods
	EvictionCoolDownTime time.Duration
	// EvictionRankingMetrics is the metrics used to rank pods for eviction
	EvictionRankingMetrics []string
	// GracePeriod is the grace period of pod deletion
	GracePeriod int64
	// CheckCPUManager represents whether to check cpu manager and filter guaranteed pods
	CheckCPUManager bool
	// RankingLabels is the labels used to rank pods for eviction, key is the label key, value is the label value list sorted by priority
	RankingLabels map[string][]string
	// SystemEvictionMetricMode is the metric mode for cpu system eviction plugin
	SystemEvictionMetricMode SystemEvictionMetricMode
}

func NewCPUSystemPressureEvictionPluginConfiguration() *CPUSystemPressureEvictionPluginConfiguration {
	return &CPUSystemPressureEvictionPluginConfiguration{
		SystemEvictionMetricMode: NodeMetric,
	}
}

func (c *CPUSystemPressureEvictionPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.CPUSystemPressureEvictionConfig != nil {

		config := aqc.Spec.Config.EvictionConfig.CPUSystemPressureEvictionConfig
		if config.EnableCPUSystemPressureEviction != nil {
			c.EnableCPUSystemEviction = *config.EnableCPUSystemPressureEviction
		}

		if config.LoadUpperBoundRatio != nil {
			c.SystemLoadUpperBoundRatio = *config.LoadUpperBoundRatio
		}

		if config.LoadLowerBoundRatio != nil {
			c.SystemLoadLowerBoundRatio = *config.LoadLowerBoundRatio
		}

		if config.UsageUpperBoundRatio != nil {
			c.SystemUsageUpperBoundRatio = *config.UsageUpperBoundRatio
		}

		if config.UsageLowerBoundRatio != nil {
			c.SystemUsageLowerBoundRatio = *config.UsageLowerBoundRatio
		}

		if config.ThresholdMetPercentage != nil {
			c.ThresholdMetPercentage = *config.ThresholdMetPercentage
		}

		if config.MetricRingSize != nil {
			c.MetricRingSize = *config.MetricRingSize
		}

		if config.EvictionCoolDownTime != nil {
			c.EvictionCoolDownTime = config.EvictionCoolDownTime.Duration
		}

		if config.EvictionRankingMetrics != nil {
			c.EvictionRankingMetrics = config.EvictionRankingMetrics
		}

		if config.GracePeriod != nil {
			c.GracePeriod = *config.GracePeriod
		}

		if config.CheckCPUManager != nil {
			c.CheckCPUManager = *config.CheckCPUManager
		}

		if config.RankingLabels != nil {
			c.RankingLabels = config.RankingLabels
		}
	}
}
