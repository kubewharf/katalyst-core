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

package throughputclassswitch

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type LowThroughputThresholdConfig struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

func NewLowThroughputThresholdConfig() *LowThroughputThresholdConfig {
	return &LowThroughputThresholdConfig{
		RxPPSThreshold:  3000,
		SuccessiveCount: 30,
	}
}

func (c *LowThroughputThresholdConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.ThroughputClassSwitch != nil &&
		itc.Spec.Config.ThroughputClassSwitch.LowThroughputThresholds != nil {
		config := itc.Spec.Config.ThroughputClassSwitch.LowThroughputThresholds

		if config.RxPPSThreshold != nil {
			c.RxPPSThreshold = *config.RxPPSThreshold
		}
		if config.SuccessiveCount != nil {
			c.SuccessiveCount = *config.SuccessiveCount
		}
	}
}
