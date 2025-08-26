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

package coresadjust

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type IRQCoresDecConfig struct {
	// interval of two successive irq cores decrease MUST greater-equal this interval
	SuccessiveDecInterval    int
	PingPongAdjustInterval   int
	SinceLastBalanceInterval int
	// max cores to decrease each time, deault 1
	DecCoresMaxEachTime int
	Thresholds          *IRQCoresDecThresholds
}

type IRQCoresDecThresholds struct {
	// threshold of decreasing irq cores, generally this threshold should be less-than IrqCoresExpectedCpuUtil
	AvgCPUUtilThreshold int
}

func NewIRQCoresDecConfig() *IRQCoresDecConfig {
	return &IRQCoresDecConfig{
		SuccessiveDecInterval:    30,
		PingPongAdjustInterval:   300,
		SinceLastBalanceInterval: 60,
		DecCoresMaxEachTime:      1,
		Thresholds: &IRQCoresDecThresholds{
			AvgCPUUtilThreshold: 40,
		},
	}
}

func (c *IRQCoresDecConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.CoresAdjust != nil &&
		itc.Spec.Config.CoresAdjust.DecConf != nil {
		config := itc.Spec.Config.CoresAdjust.DecConf

		if config.SuccessiveDecInterval != nil {
			c.SuccessiveDecInterval = *config.SuccessiveDecInterval
		}
		if config.PingPongAdjustInterval != nil {
			c.PingPongAdjustInterval = *config.PingPongAdjustInterval
		}
		if config.SinceLastBalanceInterval != nil {
			c.SinceLastBalanceInterval = *config.SinceLastBalanceInterval
		}
		if config.DecCoresMaxEachTime != nil {
			c.DecCoresMaxEachTime = *config.DecCoresMaxEachTime
		}
		if config.Thresholds != nil {
			if config.Thresholds.AvgCPUUtilThreshold != nil {
				c.Thresholds.AvgCPUUtilThreshold = *config.Thresholds.AvgCPUUtilThreshold
			}
		}
	}
}
