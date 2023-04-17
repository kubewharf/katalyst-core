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

package sysadvisor

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metacache"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// GenericSysAdvisorConfiguration stores configurations of generic sysadvisor
type GenericSysAdvisorConfiguration struct {
	SysAdvisorPlugins  []string
	StateFileDirectory string
}

// NewGenericSysAdvisorConfiguration creates a new generic sysadvisor plugin configuration.
func NewGenericSysAdvisorConfiguration() *GenericSysAdvisorConfiguration {
	return &GenericSysAdvisorConfiguration{}
}

// ApplyConfiguration is used to set configuration based on the parameter.
func (c *GenericSysAdvisorConfiguration) ApplyConfiguration(*GenericSysAdvisorConfiguration, *dynamic.DynamicConfigCRD) {
}

// SysAdvisorPluginsConfiguration stores configurations of sysadvisor plugins
type SysAdvisorPluginsConfiguration struct {
	*qosaware.QoSAwarePluginConfiguration
	*metacache.MetaCachePluginConfiguration
	*metricemitter.MetricEmitterPluginConfiguration
}

// NewSysAdvisorPluginsConfiguration creates a new sysadvisor plugins configuration.
func NewSysAdvisorPluginsConfiguration() *SysAdvisorPluginsConfiguration {
	return &SysAdvisorPluginsConfiguration{
		QoSAwarePluginConfiguration:      qosaware.NewQoSAwarePluginConfiguration(),
		MetaCachePluginConfiguration:     metacache.NewMetaCachePluginConfiguration(),
		MetricEmitterPluginConfiguration: metricemitter.NewMetricEmitterPluginConfiguration(),
	}
}

// ApplyConfiguration is used to set configuration based on the conf.
func (c *SysAdvisorPluginsConfiguration) ApplyConfiguration(defaultConf *SysAdvisorPluginsConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.QoSAwarePluginConfiguration.ApplyConfiguration(defaultConf.QoSAwarePluginConfiguration, conf)
	c.MetaCachePluginConfiguration.ApplyConfiguration(defaultConf.MetaCachePluginConfiguration, conf)
	c.MetricEmitterPluginConfiguration.ApplyConfiguration(defaultConf.MetricEmitterPluginConfiguration, conf)
}
