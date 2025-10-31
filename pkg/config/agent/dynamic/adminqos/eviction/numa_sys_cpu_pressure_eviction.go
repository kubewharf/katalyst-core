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

type NumaSysCPUPressureEvictionConfiguration struct {
	EnableEviction bool
	MetricRingSize int
	GracePeriod    int64
	SyncPeriod     int64

	ThresholdMetPercentage                 float64
	NumaCPUUsageSoftThreshold              float64
	NumaCPUUsageHardThreshold              float64
	NUMASysOverTotalUsageSoftThreshold     float64
	NUMASysOverTotalUsageHardThreshold     float64
	NUMASysOverTotalUsageEvictionThreshold float64
}

func NewNumaSysCPUPressureEvictionConfiguration() NumaSysCPUPressureEvictionConfiguration {
	return NumaSysCPUPressureEvictionConfiguration{}
}

func (n *NumaSysCPUPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig.NumaSysCPUPressureEvictionConfig
		if config.EnableEviction != nil {
			n.EnableEviction = *config.EnableEviction
		}

		if config.MetricRingSize != nil {
			n.MetricRingSize = *config.MetricRingSize
		}

		if config.GracePeriod != nil {
			n.GracePeriod = *config.GracePeriod
		}

		if config.SyncPeriod != nil {
			n.SyncPeriod = *config.SyncPeriod
		}

		if config.ThresholdMetPercentage != nil {
			n.ThresholdMetPercentage = *config.ThresholdMetPercentage
		}

		if config.NumaCPUUsageSoftThreshold != nil {
			n.NumaCPUUsageSoftThreshold = *config.NumaCPUUsageSoftThreshold
		}

		if config.NumaCPUUsageHardThreshold != nil {
			n.NumaCPUUsageHardThreshold = *config.NumaCPUUsageHardThreshold
		}

		if config.NUMASysOverTotalUsageSoftThreshold != nil {
			n.NUMASysOverTotalUsageSoftThreshold = *config.NUMASysOverTotalUsageSoftThreshold
		}

		if config.NUMASysOverTotalUsageHardThreshold != nil {
			n.NUMASysOverTotalUsageHardThreshold = *config.NUMASysOverTotalUsageHardThreshold
		}

		if config.NUMASysOverTotalUsageEvictionThreshold != nil {
			n.NUMASysOverTotalUsageEvictionThreshold = *config.NUMASysOverTotalUsageEvictionThreshold
		}
	}
}
