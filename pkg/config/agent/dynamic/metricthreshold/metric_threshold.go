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

package metricthreshold

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

const (
	DefaultCPUCodeName = "default"

	NUMACPUUsageRatioThreshold = "numa_cpu_usage_ratio_threshold"
	NUMACPULoadRatioThreshold  = "numa_cpu_load_ratio_threshold"
)

var ThresholdNameToResourceName = map[string]string{
	NUMACPUUsageRatioThreshold: consts.MetricCPUUsageContainer,
	NUMACPULoadRatioThreshold:  consts.MetricLoad1MinContainer,
}

type MetricThresholdConfiguration struct {
	Threshold map[string]map[bool]map[string]float64
}

func NewMetricThresholdConfiguration() *MetricThresholdConfiguration {
	return &MetricThresholdConfiguration{
		Threshold: map[string]map[bool]map[string]float64{
			"Intel_CascadeLake": {
				false: {
					NUMACPUUsageRatioThreshold: 0.55,
					NUMACPULoadRatioThreshold:  0.68,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.54,
					NUMACPULoadRatioThreshold:  0.68,
				},
			},
			"Intel_EmeraldRapids": {
				false: {
					NUMACPUUsageRatioThreshold: 0.6,
					NUMACPULoadRatioThreshold:  1.0,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.6,
					NUMACPULoadRatioThreshold:  0.75,
				},
			},
			"AMD_K19Zen4": {
				false: {
					NUMACPUUsageRatioThreshold: 0.55,
					NUMACPULoadRatioThreshold:  0.7,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.55,
					NUMACPULoadRatioThreshold:  0.7,
				},
			},
			"Intel_SapphireRapids": {
				false: {
					NUMACPUUsageRatioThreshold: 0.59,
					NUMACPULoadRatioThreshold:  1.0,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.59,
					NUMACPULoadRatioThreshold:  1.0,
				},
			},
			"AMD_K19Zen3": {
				false: {
					NUMACPUUsageRatioThreshold: 0.55,
					NUMACPULoadRatioThreshold:  0.7,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.55,
					NUMACPULoadRatioThreshold:  0.6,
				},
			},
			"Intel_IceLake": {
				false: {
					NUMACPUUsageRatioThreshold: 0.57,
					NUMACPULoadRatioThreshold:  0.7,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.54,
					NUMACPULoadRatioThreshold:  0.7,
				},
			},
			"AMD_K17Zen2": {
				false: {
					NUMACPUUsageRatioThreshold: 0.51,
					NUMACPULoadRatioThreshold:  0.68,
				},
			},
			"Intel_SkyLake": {
				false: {
					NUMACPUUsageRatioThreshold: 0.53,
					NUMACPULoadRatioThreshold:  0.68,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.53,
					NUMACPULoadRatioThreshold:  0.68,
				},
			},
			"Intel_Broadwell": {
				false: {
					NUMACPUUsageRatioThreshold: 0.47,
					NUMACPULoadRatioThreshold:  0.68,
				},
			},
			DefaultCPUCodeName: {
				false: {
					NUMACPUUsageRatioThreshold: 0.6,
					NUMACPULoadRatioThreshold:  1.0,
				},
				true: {
					NUMACPUUsageRatioThreshold: 0.6,
					NUMACPULoadRatioThreshold:  1.0,
				},
			},
		},
	}
}

func (sg *MetricThresholdConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
}
