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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/model"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/server"
)

// QoSAwarePluginConfiguration stores configurations of qos aware plugin
type QoSAwarePluginConfiguration struct {
	SyncPeriod time.Duration

	*resource.ResourceAdvisorConfiguration
	*server.QRMServerConfiguration
	*reporter.ReporterConfiguration
	*model.ModelConfiguration
}

// NewQoSAwarePluginConfiguration creates a new qos aware plugin configuration.
func NewQoSAwarePluginConfiguration() *QoSAwarePluginConfiguration {
	return &QoSAwarePluginConfiguration{
		ResourceAdvisorConfiguration: resource.NewResourceAdvisorConfiguration(),
		QRMServerConfiguration:       server.NewQRMServerConfiguration(),
		ReporterConfiguration:        reporter.NewReporterConfiguration(),
		ModelConfiguration:           model.NewModelConfiguration(),
	}
}
