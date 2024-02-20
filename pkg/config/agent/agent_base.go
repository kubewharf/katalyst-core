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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/orm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor"
)

// AgentConfiguration stores all the configurations needed by core katalyst components,
// and those configurations can be modified dynamically
type AgentConfiguration struct {
	// those configurations are used by agents
	*GenericAgentConfiguration
	*StaticAgentConfiguration
	*dynamic.DynamicAgentConfiguration
}

func NewAgentConfiguration() *AgentConfiguration {
	return &AgentConfiguration{
		GenericAgentConfiguration: NewGenericAgentConfiguration(),
		StaticAgentConfiguration:  NewStaticAgentConfiguration(),
		DynamicAgentConfiguration: dynamic.NewDynamicAgentConfiguration(),
	}
}

type GenericAgentConfiguration struct {
	// those configurations should be used as generic configurations, and
	// be shared by all agent components.
	*global.BaseConfiguration
	*global.PluginManagerConfiguration
	*global.QRMAdvisorConfiguration

	*metaserver.MetaServerConfiguration
	*eviction.GenericEvictionConfiguration
	*reporter.GenericReporterConfiguration
	*sysadvisor.GenericSysAdvisorConfiguration
	*qrm.GenericQRMPluginConfiguration
	*orm.GenericORMConfiguration
}

type StaticAgentConfiguration struct {
	*eviction.EvictionConfiguration
	*reporter.ReporterPluginsConfiguration
	*sysadvisor.SysAdvisorPluginsConfiguration
	*qrm.QRMPluginsConfiguration
}

func NewGenericAgentConfiguration() *GenericAgentConfiguration {
	return &GenericAgentConfiguration{
		BaseConfiguration:              global.NewBaseConfiguration(),
		PluginManagerConfiguration:     global.NewPluginManagerConfiguration(),
		MetaServerConfiguration:        metaserver.NewMetaServerConfiguration(),
		QRMAdvisorConfiguration:        global.NewQRMAdvisorConfiguration(),
		GenericEvictionConfiguration:   eviction.NewGenericEvictionConfiguration(),
		GenericReporterConfiguration:   reporter.NewGenericReporterConfiguration(),
		GenericSysAdvisorConfiguration: sysadvisor.NewGenericSysAdvisorConfiguration(),
		GenericQRMPluginConfiguration:  qrm.NewGenericQRMPluginConfiguration(),
		GenericORMConfiguration:        orm.NewGenericORMConfiguration(),
	}
}

func NewStaticAgentConfiguration() *StaticAgentConfiguration {
	return &StaticAgentConfiguration{
		EvictionConfiguration:          eviction.NewEvictionConfiguration(),
		ReporterPluginsConfiguration:   reporter.NewReporterPluginsConfiguration(),
		SysAdvisorPluginsConfiguration: sysadvisor.NewSysAdvisorPluginsConfiguration(),
		QRMPluginsConfiguration:        qrm.NewQRMPluginsConfiguration(),
	}
}
