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
)

// SuppressionUsageCPUPressureEvictionConfiguration holds the config of the
// suppression usage cpu pressure eviction channel.
type SuppressionUsageCPUPressureEvictionConfiguration struct {
	EnableSuppressionUsageEviction bool
	SyncPeriod                     int64
	MetricRingSize                 int
	ThresholdMetPercentage         float64
	SoftSuppressionRateThreshold   float64
	SoftCPUUsageThreshold          float64
	HardSuppressionRateThreshold   float64
	HardCPUUsageThreshold          float64
	PodCPUUsageEvictionThreshold   float64
	GracePeriod                    int64
}

// NewSuppressionUsageCPUPressureEvictionConfiguration creates a
// SuppressionUsageCPUPressureEvictionConfiguration with zero values.
func NewSuppressionUsageCPUPressureEvictionConfiguration() SuppressionUsageCPUPressureEvictionConfiguration {
	return SuppressionUsageCPUPressureEvictionConfiguration{}
}

// ApplyConfiguration applies the dynamic CRD config to the current config.
func (s *SuppressionUsageCPUPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig.SuppressionUsageCPUPressureEvictionConfig
		if config == nil {
			return
		}

		if config.EnableSuppressionUsageEviction != nil {
			s.EnableSuppressionUsageEviction = *config.EnableSuppressionUsageEviction
		}

		if config.SyncPeriod != nil {
			s.SyncPeriod = *config.SyncPeriod
		}

		if config.MetricRingSize != nil {
			s.MetricRingSize = *config.MetricRingSize
		}

		if config.ThresholdMetPercentage != nil {
			s.ThresholdMetPercentage = *config.ThresholdMetPercentage
		}

		if config.SoftSuppressionRateThreshold != nil {
			s.SoftSuppressionRateThreshold = *config.SoftSuppressionRateThreshold
		}

		if config.SoftCPUUsageThreshold != nil {
			s.SoftCPUUsageThreshold = *config.SoftCPUUsageThreshold
		}

		if config.HardSuppressionRateThreshold != nil {
			s.HardSuppressionRateThreshold = *config.HardSuppressionRateThreshold
		}

		if config.HardCPUUsageThreshold != nil {
			s.HardCPUUsageThreshold = *config.HardCPUUsageThreshold
		}

		if config.PodCPUUsageEvictionThreshold != nil {
			s.PodCPUUsageEvictionThreshold = *config.PodCPUUsageEvictionThreshold
		}

		if config.GracePeriod != nil {
			s.GracePeriod = *config.GracePeriod
		}
	}
}
