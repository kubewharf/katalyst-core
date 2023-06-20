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

package memoryheadroom

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type MemoryUtilBasedConfiguration struct {
	Enable              bool
	FreeBasedRatio      float64
	StaticBasedCapacity float64
	CacheBasedRatio     float64
}

func NewMemoryUtilBasedConfiguration() *MemoryUtilBasedConfiguration {
	return &MemoryUtilBasedConfiguration{}
}

func (c *MemoryUtilBasedConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig.MemoryHeadroomConfig != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig.MemoryHeadroomConfig.UtilBasedConfig != nil {
		config := aqc.Spec.Config.ReclaimedResourceConfig.MemoryHeadroomConfig.UtilBasedConfig
		if config.Enable != nil {
			c.Enable = *config.Enable
		}

		if config.FreeBasedRatio != nil {
			c.FreeBasedRatio = *config.FreeBasedRatio
		}

		if config.StaticBasedCapacity != nil {
			c.StaticBasedCapacity = *config.StaticBasedCapacity
		}

		if config.CacheBasedRatio != nil {
			c.CacheBasedRatio = *config.CacheBasedRatio
		}
	}
}
