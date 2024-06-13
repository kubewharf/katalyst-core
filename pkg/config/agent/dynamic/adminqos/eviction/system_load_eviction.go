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

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type SystemLoadEvictionPluginConfiguration struct {
	// SoftThreshold is the soft threshold of system load pressure, it should be an integral multiple of 100, which means
	// the real threshold is (SoftThreshold / 100) * CoreNumber
	SoftThreshold int64
	// HardThreshold is the hard threshold of system load pressure, it should be an integral multiple of 100, which means
	// the real threshold is (SoftThreshold / 100) * CoreNumber
	HardThreshold int64
	// HistorySize is the size of the load metric ring, which is used to calculate the system load
	HistorySize int64
	// SyncPeriod is the interval in seconds of the plugin fetch the load information
	SyncPeriod int64
	// CoolDownTimeInSeconds is the cool-down time of the plugin evict pods
	CoolDownTime int64
	// GracePeriod is the grace period of pod deletion
	GracePeriod int64
	// the plugin considers the node is facing load pressure only when the ratio of load history which is greater than
	// threshold is greater than this percentage
	ThresholdMetPercentage float64
}

func NewSystemLoadEvictionPluginConfiguration() *SystemLoadEvictionPluginConfiguration {
	return &SystemLoadEvictionPluginConfiguration{}
}

func (l *SystemLoadEvictionPluginConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		// SystemLoadPressureEvictionConfig is legacy config, we'll remove it in the future. For now,
		// we ignore the static check for this two line to prevent lint fails.
		aqc.Spec.Config.EvictionConfig.SystemLoadPressureEvictionConfig != nil { // nolint:staticcheck
		config := aqc.Spec.Config.EvictionConfig.SystemLoadPressureEvictionConfig // nolint:staticcheck
		if config.SoftThreshold != nil {
			l.SoftThreshold = *config.SoftThreshold
		}

		if config.HardThreshold != nil {
			l.HardThreshold = *config.HardThreshold
		}

		if config.HistorySize != nil {
			l.HistorySize = *config.HistorySize
		}

		if config.SyncPeriod != nil {
			l.SyncPeriod = *config.SyncPeriod
		}

		if config.CoolDownTime != nil {
			l.CoolDownTime = *config.CoolDownTime
		}

		if config.GracePeriod != nil {
			l.GracePeriod = *config.GracePeriod
		}

		if config.ThresholdMetPercentage != nil {
			l.ThresholdMetPercentage = *config.ThresholdMetPercentage
		}
	}
}
