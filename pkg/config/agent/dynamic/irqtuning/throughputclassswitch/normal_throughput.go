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

type NormalThroughputThresholdConfig struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

func NewNormalThroughputThresholdConfig() *NormalThroughputThresholdConfig {
	return &NormalThroughputThresholdConfig{
		RxPPSThresh:     6000,
		SuccessiveCount: 10,
	}
}

func (c *NormalThroughputThresholdConfig) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.ThrouputClassSwitch != nil {
		config := itc.Spec.Config.ThrouputClassSwitch

		if config.NormalThroughputThresholds != nil {
			if config.NormalThroughputThresholds.RxPPSThresh != nil {
				c.RxPPSThresh = *config.NormalThroughputThresholds.RxPPSThresh
			}
			if config.NormalThroughputThresholds.SuccessiveCount != nil {
				c.SuccessiveCount = *config.NormalThroughputThresholds.SuccessiveCount
			}
		}
	}
}
