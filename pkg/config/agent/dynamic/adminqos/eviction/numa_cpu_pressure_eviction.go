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

type NumaCPUPressureEvictionConfiguration struct {
	EnableEviction         bool
	ThresholdMetPercentage float64
	MetricRingSize         int
	GracePeriod            int64
	ThresholdExpandFactor  float64
	CandidateCount         int
	SkippedPodKinds        []string
	EnabledFilters         []string
	EnabledScorers         []string
}

func NewNumaCPUPressureEvictionConfiguration() NumaCPUPressureEvictionConfiguration {
	return NumaCPUPressureEvictionConfiguration{}
}

func (n *NumaCPUPressureEvictionConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil && aqc.Spec.Config.EvictionConfig != nil &&
		aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig != nil {
		config := aqc.Spec.Config.EvictionConfig.CPUPressureEvictionConfig.NumaCPUPressureEvictionConfig
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
			n.ThresholdExpandFactor = *config.ThresholdExpandFactor
		}

		if config.CandidateCount != nil {
			n.CandidateCount = *config.CandidateCount
		}

		if config.SkippedPodKinds != nil {
			n.SkippedPodKinds = config.SkippedPodKinds
		}
	}
}
