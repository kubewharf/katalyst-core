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
	*adminqos.ReclaimedResourceConfiguration

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
		ReclaimedResourceConfiguration: adminqos.NewReclaimedResourceConfiguration(),
		GenericEvictionConfiguration:   eviction.NewGenericEvictionConfiguration(),
		GenericReporterConfiguration:   reporter.NewGenericReporterConfiguration(),
		GenericSysAdvisorConfiguration: sysadvisor.NewGenericSysAdvisorConfiguration(),
		GenericQRMPluginConfiguration:  qrm.NewGenericQRMPluginConfiguration(),
	}
}

func (c *GenericAgentConfiguration) ApplyConfiguration(conf *dynamic.DynamicConfigCRD) {
	c.BaseConfiguration.ApplyConfiguration(conf)
	c.MetaServerConfiguration.ApplyConfiguration(conf)
	c.PluginManagerConfiguration.ApplyConfiguration(conf)
	c.ReclaimedResourceConfiguration.ApplyConfiguration(conf)
	c.QRMAdvisorConfiguration.ApplyConfiguration(conf)
	c.GenericEvictionConfiguration.ApplyConfiguration(conf)
	c.GenericReporterConfiguration.ApplyConfiguration(conf)
	c.GenericSysAdvisorConfiguration.ApplyConfiguration(conf)
	c.GenericQRMPluginConfiguration.ApplyConfiguration(conf)
}

func NewAgentConfiguration() *AgentConfiguration {
	return &AgentConfiguration{
		EvictionPluginsConfiguration:   eviction.NewEvictionPluginsConfiguration(),
		ReporterPluginsConfiguration:   reporter.NewReporterPluginsConfiguration(),
		SysAdvisorPluginsConfiguration: sysadvisor.NewSysAdvisorPluginsConfiguration(),
		QRMPluginsConfiguration:        qrm.NewQRMPluginsConfiguration(),
	}
}

func (c *AgentConfiguration) ApplyConfiguration(conf *dynamic.DynamicConfigCRD) {
	c.EvictionPluginsConfiguration.ApplyConfiguration(conf)
	c.ReporterPluginsConfiguration.ApplyConfiguration(conf)
	c.SysAdvisorPluginsConfiguration.ApplyConfiguration(conf)
	c.QRMPluginsConfiguration.ApplyConfiguration(conf)
}
