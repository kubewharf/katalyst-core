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
)

const (
	DefaultCPUCodeName = "default"
)

type MetricThreshold struct {
	Threshold map[string]map[bool]map[string]float64
}

func NewMetricThreshold() *MetricThreshold {
	return &MetricThreshold{
		Threshold: map[string]map[bool]map[string]float64{
			"Intel_CascadeLake": {
				false: {
					"cpu_usage_threshold": 0.55,
					"cpu_load_threshold":  0.68,
				},
				true: {
					"cpu_usage_threshold": 0.54,
					"cpu_load_threshold":  0.68,
				},
			},
			"Intel_EmeraldRapids": {
				false: {
					"cpu_usage_threshold": 0.6,
					"cpu_load_threshold":  1.0,
				},
				true: {
					"cpu_usage_threshold": 0.6,
					"cpu_load_threshold":  0.75,
				},
			},
			"AMD_K19Zen4": {
				false: {
					"cpu_usage_threshold": 0.55,
					"cpu_load_threshold":  0.7,
				},
				true: {
					"cpu_usage_threshold": 0.55,
					"cpu_load_threshold":  0.7,
				},
			},
			"Intel_SapphireRapids": {
				false: {
					"cpu_usage_threshold": 0.59,
					"cpu_load_threshold":  1.0,
				},
				true: {
					"cpu_usage_threshold": 0.59,
					"cpu_load_threshold":  1.0,
				},
			},
			"AMD_K19Zen3": {
				false: {
					"cpu_usage_threshold": 0.55,
					"cpu_load_threshold":  0.7,
				},
				true: {
					"cpu_usage_threshold": 0.55,
					"cpu_load_threshold":  0.6,
				},
			},
			"Intel_IceLake": {
				false: {
					"cpu_usage_threshold": 0.57,
					"cpu_load_threshold":  0.7,
				},
				true: {
					"cpu_usage_threshold": 0.54,
					"cpu_load_threshold":  0.7,
				},
			},
			"AMD_K17Zen2": {
				false: {
					"cpu_usage_threshold": 0.51,
					"cpu_load_threshold":  0.68,
				},
			},
			"Intel_SkyLake": {
				false: {
					"cpu_usage_threshold": 0.53,
					"cpu_load_threshold":  0.68,
				},
				true: {
					"cpu_usage_threshold": 0.53,
					"cpu_load_threshold":  0.68,
				},
			},
			"Intel_Broadwell": {
				false: {
					"cpu_usage_threshold": 0.47,
					"cpu_load_threshold":  0.68,
				},
			},
			DefaultCPUCodeName: {
				false: {
					"cpu_usage_threshold": 0.6,
					"cpu_load_threshold":  1.0,
				},
				true: {
					"cpu_usage_threshold": 0.6,
					"cpu_load_threshold":  1.0,
				},
			},
		},
	}
}

func (sg *MetricThreshold) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
}
