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

type FragMemConfiguration struct {
	EnableFragMem              bool
	MemFragScoreAsync          int
	THPDefaultConfig           string
	THPHighOrderScoreThreshold int
}

func NewFragMemConfiguration() *FragMemConfiguration {
	return &FragMemConfiguration{}
}

func (c *FragMemConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.QRMPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig != nil {
		config := aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig
		if config.EnableFragMem != nil {
			c.EnableFragMem = *config.EnableFragMem
		}
		if config.MemFragScoreAsync != nil {
			c.MemFragScoreAsync = int(*config.MemFragScoreAsync)
		}
		if config.THPDefaultConfig != nil {
			c.THPDefaultConfig = *config.THPDefaultConfig
		}
		if config.THPHighOrderScoreThreshold != nil {
			c.THPHighOrderScoreThreshold = int(*config.THPHighOrderScoreThreshold)
		}
	}
}
