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

package agent

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type GenericAgentConfiguration struct {
	// those configurations should be used as generic configurations, and
	// be shared by all agent components.
	*global.BaseConfiguration
	*global.PluginManagerConfiguration
	*global.MetaServerConfiguration
	*global.QRMAdvisorConfiguration
	*adminqos.AdminQoSConfiguration

	*eviction.GenericEvictionConfiguration
	*reporter.GenericReporterConfiguration
	*sysadvisor.GenericSysAdvisorConfiguration
	*qrm.GenericQRMPluginConfiguration
}

type AgentConfiguration struct {
	*eviction.EvictionPluginsConfiguration
	*reporter.ReporterPluginsConfiguration
	*sysadvisor.SysAdvisorPluginsConfiguration
	*qrm.QRMPluginsConfiguration
}

func NewGenericAgentConfiguration() *GenericAgentConfiguration {
	return &GenericAgentConfiguration{
		BaseConfiguration:              global.NewBaseConfiguration(),
		PluginManagerConfiguration:     global.NewPluginManagerConfiguration(),
		MetaServerConfiguration:        global.NewMetaServerConfiguration(),
		QRMAdvisorConfiguration:        global.NewQRMAdvisorConfiguration(),
		AdminQoSConfiguration:          adminqos.NewAdminQoSConfiguration(),
		GenericEvictionConfiguration:   eviction.NewGenericEvictionConfiguration(),
		GenericReporterConfiguration:   reporter.NewGenericReporterConfiguration(),
		GenericSysAdvisorConfiguration: sysadvisor.NewGenericSysAdvisorConfiguration(),
		GenericQRMPluginConfiguration:  qrm.NewGenericQRMPluginConfiguration(),
	}
}

func (c *GenericAgentConfiguration) ApplyConfiguration(defaultConf *GenericAgentConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.BaseConfiguration.ApplyConfiguration(defaultConf.BaseConfiguration, conf)
	c.MetaServerConfiguration.ApplyConfiguration(defaultConf.MetaServerConfiguration, conf)
	c.PluginManagerConfiguration.ApplyConfiguration(defaultConf.PluginManagerConfiguration, conf)
	c.AdminQoSConfiguration.ApplyConfiguration(defaultConf.ReclaimedResourceConfiguration, conf)
	c.QRMAdvisorConfiguration.ApplyConfiguration(defaultConf.QRMAdvisorConfiguration, conf)
	c.GenericEvictionConfiguration.ApplyConfiguration(defaultConf.GenericEvictionConfiguration, conf)
	c.GenericReporterConfiguration.ApplyConfiguration(defaultConf.GenericReporterConfiguration, conf)
	c.GenericSysAdvisorConfiguration.ApplyConfiguration(defaultConf.GenericSysAdvisorConfiguration, conf)
	c.GenericQRMPluginConfiguration.ApplyConfiguration(defaultConf.GenericQRMPluginConfiguration, conf)
}

func NewAgentConfiguration() *AgentConfiguration {
	return &AgentConfiguration{
		EvictionPluginsConfiguration:   eviction.NewEvictionPluginsConfiguration(),
		ReporterPluginsConfiguration:   reporter.NewReporterPluginsConfiguration(),
		SysAdvisorPluginsConfiguration: sysadvisor.NewSysAdvisorPluginsConfiguration(),
		QRMPluginsConfiguration:        qrm.NewQRMPluginsConfiguration(),
	}
}

func (c *AgentConfiguration) ApplyConfiguration(defaultConf *AgentConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.EvictionPluginsConfiguration.ApplyConfiguration(defaultConf.EvictionPluginsConfiguration, conf)
	c.ReporterPluginsConfiguration.ApplyConfiguration(defaultConf.ReporterPluginsConfiguration, conf)
	c.SysAdvisorPluginsConfiguration.ApplyConfiguration(defaultConf.SysAdvisorPluginsConfiguration, conf)
	c.QRMPluginsConfiguration.ApplyConfiguration(defaultConf.QRMPluginsConfiguration, conf)
}
