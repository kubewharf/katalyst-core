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

package netoverload

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

// IRQCoreNetOverloadThresholds is the configuration for IRQCoreNetOverloadThresholds.
type IRQCoreNetOverloadThresholds struct {
	// ratio of softnet_stat 3rd col time_squeeze packets / softnet_stat 1st col processed packets
	SoftNetTimeSqueezeRatio float64
}

func NewIRQCoreNetOverloadThresholds() *IRQCoreNetOverloadThresholds {
	return &IRQCoreNetOverloadThresholds{
		SoftNetTimeSqueezeRatio: 0.1,
	}
}
func (c *IRQCoreNetOverloadThresholds) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if itc := conf.IRQTuningConfiguration; itc != nil &&
		itc.Spec.Config.CoreNetOverLoadThresh != nil {
		config := itc.Spec.Config.CoreNetOverLoadThresh

		if config.SoftNetTimeSqueezeRatio != nil {
			c.SoftNetTimeSqueezeRatio = *config.SoftNetTimeSqueezeRatio
		}
	}
}
