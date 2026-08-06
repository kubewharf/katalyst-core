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

package qrm

import "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"

type HostWatermarkConfiguration struct {
	EnableHostWatermark       bool
	VMWatermarkScaleFactor    int
	VMWatermarkBoostFactor    int
	VMExtFragThreshold        int
	ReservedKswapdWatermarkGB uint64
}

func NewHostWatermarkConfiguration() *HostWatermarkConfiguration {
	return &HostWatermarkConfiguration{}
}

func (c *HostWatermarkConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.QRMPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.HostWatermarkConfig != nil {
		config := aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.HostWatermarkConfig
		if config.EnableHostWatermark != nil {
			c.EnableHostWatermark = *config.EnableHostWatermark
		}
		if config.VMWatermarkScaleFactor != nil {
			c.VMWatermarkScaleFactor = int(*config.VMWatermarkScaleFactor)
		}
		if config.VMWatermarkBoostFactor != nil {
			c.VMWatermarkBoostFactor = int(*config.VMWatermarkBoostFactor)
		}
		if config.VMExtFragThreshold != nil {
			c.VMExtFragThreshold = int(*config.VMExtFragThreshold)
		}
		if config.ReservedKswapdWatermarkGB != nil {
			c.ReservedKswapdWatermarkGB = uint64(*config.ReservedKswapdWatermarkGB)
		}
	}
}
