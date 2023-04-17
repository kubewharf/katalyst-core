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

import (
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type GenericQRMPluginConfiguration struct {
	StateFileDirectory            string
	QRMPluginSocketDirs           []string
	ExtraStateFileAbsPath         string
	ReclaimRelativeRootCgroupPath string
}

type QRMPluginsConfiguration struct {
	*CPUQRMPluginConfig
	*MemoryQRMPluginConfig
}

func NewGenericQRMPluginConfiguration() *GenericQRMPluginConfiguration {
	return &GenericQRMPluginConfiguration{}
}

func (c *GenericQRMPluginConfiguration) ApplyConfiguration(*GenericQRMPluginConfiguration, *dynamic.DynamicConfigCRD) {
}

func NewQRMPluginsConfiguration() *QRMPluginsConfiguration {
	return &QRMPluginsConfiguration{
		CPUQRMPluginConfig:    NewCPUQRMPluginConfig(),
		MemoryQRMPluginConfig: NewMemoryQRMPluginConfig(),
	}
}

func (c *QRMPluginsConfiguration) ApplyConfiguration(defaultConf *QRMPluginsConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.CPUQRMPluginConfig.ApplyConfiguration(defaultConf.CPUQRMPluginConfig, conf)
	c.MemoryQRMPluginConfig.ApplyConfiguration(defaultConf.MemoryQRMPluginConfig, conf)
}
