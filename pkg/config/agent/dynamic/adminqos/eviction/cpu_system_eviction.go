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

type CPUSystemPressureEvictionPluginConfiguration struct {
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
	RankingLabels              map[string][]string
}

func NewCPUSystemPressureEvictionPluginConfiguration() *CPUSystemPressureEvictionPluginConfiguration {
	return &CPUSystemPressureEvictionPluginConfiguration{}
}

func (c *CPUSystemPressureEvictionPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.CPUSystemPressureEvictionConfig != nil {

		config := aqc.Spec.Config.EvictionConfig.CPUSystemPressureEvictionConfig
		if config.EnableCPUSystemPressureEviction != nil {
			c.EnableCPUSystemEviction = *(config.EnableCPUSystemPressureEviction)
		}

		if config.LoadUpperBoundRatio != nil {
			c.SystemLoadUpperBoundRatio = *(config.LoadUpperBoundRatio)
		}

		if config.LoadLowerBoundRatio != nil {
			c.SystemLoadLowerBoundRatio = *(config.LoadLowerBoundRatio)
		}

		if config.UsageUpperBoundRatio != nil {
			c.SystemUsageUpperBoundRatio = *(config.UsageUpperBoundRatio)
		}

		if config.UsageLowerBoundRatio != nil {
			c.SystemUsageLowerBoundRatio = *(config.UsageLowerBoundRatio)
		}

		if config.ThresholdMetPercentage != nil {
			c.ThresholdMetPercentage = *(config.ThresholdMetPercentage)
		}

		if config.MetricRingSize != nil {
			c.MetricRingSize = *(config.MetricRingSize)
		}

		if config.EvictionCoolDownTime != nil {
			c.EvictionCoolDownTime = config.EvictionCoolDownTime.Duration
		}

		if config.EvictionRankingMetrics != nil {
			c.EvictionRankingMetrics = config.EvictionRankingMetrics
		}

		if config.GracePeriod != nil {
			c.GracePeriod = *(config.GracePeriod)
		}

		if config.CheckCPUManager != nil {
			c.CheckCPUManager = *(config.CheckCPUManager)
		}

		if config.RankingLabels != nil {
			c.RankingLabels = config.RankingLabels
		}
	}
}
