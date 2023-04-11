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

package qosaware

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/server"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// QoSAwarePluginConfiguration stores configurations of qos aware plugin
type QoSAwarePluginConfiguration struct {
	SyncPeriod time.Duration

	*resource.ResourceAdvisorConfiguration
	*server.QRMServerConfiguration
	*reporter.HeadroomReporterConfiguration
}

// NewQoSAwarePluginConfiguration creates a new qos aware plugin configuration.
func NewQoSAwarePluginConfiguration() *QoSAwarePluginConfiguration {
	return &QoSAwarePluginConfiguration{
		ResourceAdvisorConfiguration:  resource.NewResourceAdvisorConfiguration(),
		QRMServerConfiguration:        server.NewQRMServerConfiguration(),
		HeadroomReporterConfiguration: reporter.NewHeadroomReporterConfiguration(),
	}
}

// ApplyConfiguration is used to set configuration based on conf.
func (c *QoSAwarePluginConfiguration) ApplyConfiguration(defaultConf *QoSAwarePluginConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.ResourceAdvisorConfiguration.ApplyConfiguration(defaultConf.ResourceAdvisorConfiguration, conf)
	c.QRMServerConfiguration.ApplyConfiguration(defaultConf.QRMServerConfiguration, conf)
	c.HeadroomReporterConfiguration.ApplyConfiguration(defaultConf.HeadroomReporterConfiguration, conf)
}
