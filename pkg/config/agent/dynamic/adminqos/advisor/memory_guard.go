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

package advisor

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type MemoryGuardConfiguration struct {
	Enable                       bool
	CriticalWatermarkScaleFactor float64
}

func NewMemoryGuardConfiguration() *MemoryGuardConfiguration {
	return &MemoryGuardConfiguration{
		Enable:                       true,
		CriticalWatermarkScaleFactor: 1.0,
	}
}

func (c *MemoryGuardConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.AdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.MemoryAdvisorConfig != nil &&
		aqc.Spec.Config.AdvisorConfig.MemoryAdvisorConfig.MemoryGuardConfig != nil {
		config := aqc.Spec.Config.AdvisorConfig.MemoryAdvisorConfig.MemoryGuardConfig
		if config.Enable != nil {
			c.Enable = *config.Enable
		}
		if config.CriticalWatermarkScaleFactor != nil {
			c.CriticalWatermarkScaleFactor = *config.CriticalWatermarkScaleFactor
		}
	}
}
