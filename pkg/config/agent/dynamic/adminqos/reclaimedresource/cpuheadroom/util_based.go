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

package cpuheadroom

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type CPUUtilBasedConfiguration struct {
	Enable                         bool
	TargetReclaimedCoreUtilization float64
	MaxReclaimedCoreUtilization    float64
	MaxOversoldRate                float64
	MaxHeadroomCapacityRate        float64
	NonReclaimUtilizationHigh      float64
	NonReclaimUtilizationLow       float64
}

func NewCPUUtilBasedConfiguration() *CPUUtilBasedConfiguration {
	return &CPUUtilBasedConfiguration{}
}

func (c *CPUUtilBasedConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig.CPUHeadroomConfig != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig.CPUHeadroomConfig.UtilBasedConfig != nil {
		config := aqc.Spec.Config.ReclaimedResourceConfig.CPUHeadroomConfig.UtilBasedConfig
		if config.Enable != nil {
			c.Enable = *config.Enable
		}

		if config.TargetReclaimedCoreUtilization != nil {
			c.TargetReclaimedCoreUtilization = *config.TargetReclaimedCoreUtilization
		}

		if config.MaxReclaimedCoreUtilization != nil {
			c.MaxReclaimedCoreUtilization = *config.MaxReclaimedCoreUtilization
		}

		if config.MaxOversoldRate != nil {
			c.MaxOversoldRate = *config.MaxOversoldRate
		}

		if config.MaxHeadroomCapacityRate != nil {
			c.MaxHeadroomCapacityRate = *config.MaxHeadroomCapacityRate
		}

		if config.NonReclaimUtilizationHigh != nil {
			c.NonReclaimUtilizationHigh = *config.NonReclaimUtilizationHigh
		}

		if config.NonReclaimUtilizationLow != nil {
			c.NonReclaimUtilizationLow = *config.NonReclaimUtilizationLow
		}
	}
}
