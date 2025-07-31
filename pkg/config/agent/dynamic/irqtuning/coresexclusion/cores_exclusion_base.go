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

package coresexclusion

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

// IRQCoresExclusionConfig is the configuration for irq cores exclusion.
type IRQCoresExclusionConfig struct {
	Thresholds *IRQCoresExclusionThresholds
	// interval of successive enable/disable irq cores exclusion MUST >= SuccessiveSwitchInterval
	SuccessiveSwitchInterval float64
}

func NewIRQCoresExclusionConfig() *IRQCoresExclusionConfig {
	return &IRQCoresExclusionConfig{
		Thresholds:               NewIRQCoresExclusionThresholds(),
		SuccessiveSwitchInterval: 600,
	}
}

func (c *IRQCoresExclusionConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.CoresExclusion != nil {
		config := itc.Spec.Config.CoresExclusion

		if config.Thresholds != nil {
			c.Thresholds.ApplyConfiguration(conf)
		}
		if config.SuccessiveSwitchInterval != nil {
			c.SuccessiveSwitchInterval = *config.SuccessiveSwitchInterval
		}
	}

}
