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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type NumaCPUPressureEvictionConfiguration struct {
	EnableEviction                 bool
	ThresholdMetPercentage         float64
	MetricRingSize                 int
	GracePeriod                    int64
	ThresholdExpandFactor          float64
	CpuUsageRatioThreshold         float64
	CandidateCount                 int
	WorkloadMetricsLabelKeys       []string
	SkippedPodKinds                []string
	EnabledFilters                 []string
	EnabledScorers                 []string
	WorkloadEvictionFrequencyLimit []float64
}

func NewNumaCPUPressureEvictionConfiguration() NumaCPUPressureEvictionConfiguration {
	return NumaCPUPressureEvictionConfiguration{}
}

func (n *NumaCPUPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	general.Infof("[DEBUG-AQC] NumaCPUPressureEvictionConfiguration.ApplyConfiguration called, conf is nil: %v", conf == nil)
	if conf == nil {
		general.Infof("[DEBUG-AQC] conf is nil, skip applying configuration")
		return
	}

	general.Infof("[DEBUG-AQC] AdminQoSConfiguration is nil: %v", conf.AdminQoSConfiguration == nil)
	if conf.AdminQoSConfiguration == nil {
		general.Infof("[DEBUG-AQC] AdminQoSConfiguration is nil, skip")
		return
	}

	general.Infof("[DEBUG-AQC] AdminQoSConfiguration not nil, checking EvictionConfig, is nil: %v", conf.AdminQoSConfiguration.Spec.Config.EvictionConfig == nil)
	if conf.AdminQoSConfiguration.Spec.Config.EvictionConfig == nil {
		general.Infof("[DEBUG-AQC] EvictionConfig is nil, skip")
		return
	}

	general.Infof("[DEBUG-AQC] CPUPressureEvictionConfig is nil: %v", conf.AdminQoSConfiguration.Spec.Config.EvictionConfig.CPUPressureEvictionConfig == nil)
	if conf.AdminQoSConfiguration.Spec.Config.EvictionConfig.CPUPressureEvictionConfig == nil {
		general.Infof("[DEBUG-AQC] CPUPressureEvictionConfig is nil, skip")
		return
	}

	config := conf.AdminQoSConfiguration.Spec.Config.EvictionConfig.CPUPressureEvictionConfig.NumaCPUPressureEvictionConfig
	general.Infof("[DEBUG-AQC] NumaCPUPressureEvictionConfig extracted, ThresholdExpandFactor pointer is nil: %v, CpuUsageRatioThreshold pointer is nil: %v",
		config.ThresholdExpandFactor == nil, config.CpuUsageRatioThreshold == nil)

	if config.EnableEviction != nil {
		n.EnableEviction = *config.EnableEviction
	}

	if config.ThresholdMetPercentage != nil {
		n.ThresholdMetPercentage = *config.ThresholdMetPercentage
	}

	if config.MetricRingSize != nil {
		n.MetricRingSize = *config.MetricRingSize
	}

	if config.GracePeriod != nil {
		n.GracePeriod = *config.GracePeriod
	}

	if config.ThresholdExpandFactor != nil {
		oldValue := n.ThresholdExpandFactor
		n.ThresholdExpandFactor = *config.ThresholdExpandFactor
		general.Infof("update numa cpu pressure eviction ThresholdExpandFactor from %v to %v via AdminQoSConfiguration",
			oldValue, n.ThresholdExpandFactor)
	}

	if config.CpuUsageRatioThreshold != nil {
		oldValue := n.CpuUsageRatioThreshold
		n.CpuUsageRatioThreshold = *config.CpuUsageRatioThreshold
		general.Infof("update numa cpu pressure eviction CpuUsageRatioThreshold from %v to %v via AdminQoSConfiguration",
			oldValue, n.CpuUsageRatioThreshold)
	}

	if config.CandidateCount != nil {
		n.CandidateCount = *config.CandidateCount
	}

	if config.SkippedPodKinds != nil {
		n.SkippedPodKinds = config.SkippedPodKinds
	}
}
