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

type IRQCoresExclusionThresholds struct {
	EnableThresholds  *EnableIRQCoresExclusionThresholds
	DisableThresholds *DisableIRQCoresExclusionThresholds
}

type EnableIRQCoresExclusionThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

type DisableIRQCoresExclusionThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

func NewIRQCoresExclusionThresholds() *IRQCoresExclusionThresholds {
	return &IRQCoresExclusionThresholds{
		EnableThresholds: &EnableIRQCoresExclusionThresholds{
			RxPPSThreshold:  60000,
			SuccessiveCount: 30,
		},
		DisableThresholds: &DisableIRQCoresExclusionThresholds{
			RxPPSThreshold:  30000,
			SuccessiveCount: 30,
		},
	}
}

func (c *IRQCoresExclusionThresholds) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.CoresExclusion != nil &&
		itc.Spec.Config.CoresExclusion.Thresholds != nil {
		config := itc.Spec.Config.CoresExclusion.Thresholds

		if config.EnableThresholds != nil {
			if config.EnableThresholds.RxPPSThreshold != nil {
				c.EnableThresholds.RxPPSThreshold = *config.EnableThresholds.RxPPSThreshold
			}
			if config.EnableThresholds.SuccessiveCount != nil {
				c.EnableThresholds.SuccessiveCount = *config.EnableThresholds.SuccessiveCount
			}
		}
		if config.DisableThresholds != nil {
			if config.DisableThresholds.RxPPSThreshold != nil {
				c.DisableThresholds.RxPPSThreshold = *config.DisableThresholds.RxPPSThreshold
			}
			if config.DisableThresholds.SuccessiveCount != nil {
				c.DisableThresholds.SuccessiveCount = *config.DisableThresholds.SuccessiveCount
			}
		}
	}
}
