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

// Package config is the package that contains those important configurations
// for all running components, including Manager, eviction manager and external
// controller.
package config // import "github.com/kubewharf/katalyst-core/pkg/config"

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/config/webhook"
)

// DynamicConfiguration stores all the configurations needed by core katalyst components,
// and those configurations can be modified dynamically
type DynamicConfiguration struct {
	// those configurations are used by agents
	*agent.GenericAgentConfiguration
	*agent.AgentConfiguration
}

func NewDynamicConfiguration() *DynamicConfiguration {
	return &DynamicConfiguration{
		GenericAgentConfiguration: agent.NewGenericAgentConfiguration(),
		AgentConfiguration:        agent.NewAgentConfiguration(),
	}
}

// ApplyConfiguration is used to set configuration contents by CR dynamically.
func (d *DynamicConfiguration) ApplyConfiguration(defaultConf *DynamicConfiguration, conf *dynamic.DynamicConfigCRD) {
	d.GenericAgentConfiguration.ApplyConfiguration(defaultConf.GenericAgentConfiguration, conf)
	d.AgentConfiguration.ApplyConfiguration(defaultConf.AgentConfiguration, conf)
}

// Configuration stores all the configurations needed by core katalyst components,
// both for static config (only support to be modified by flag) and dynamic config
type Configuration struct {
	// those configurations for multi components
	*generic.GenericConfiguration

	// those configurations are used by controllers
	*webhook.GenericWebhookConfiguration
	*webhook.WebhooksConfiguration

	// those configurations are used by controllers
	*controller.GenericControllerConfiguration
	*controller.ControllersConfiguration

	// those configurations are used by metric
	*metric.GenericMetricConfiguration
	*metric.CustomMetricConfiguration

	*DynamicConfiguration
}

func NewConfiguration() *Configuration {
	return &Configuration{
		GenericConfiguration:           generic.NewGenericConfiguration(),
		GenericWebhookConfiguration:    webhook.NewGenericWebhookConfiguration(),
		WebhooksConfiguration:          webhook.NewWebhooksConfiguration(),
		GenericControllerConfiguration: controller.NewGenericControllerConfiguration(),
		ControllersConfiguration:       controller.NewControllersConfiguration(),
		GenericMetricConfiguration:     metric.NewGenericMetricConfiguration(),
		CustomMetricConfiguration:      metric.NewCustomMetricConfiguration(),
		DynamicConfiguration:           NewDynamicConfiguration(),
	}
}
