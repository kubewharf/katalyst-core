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

package dynamic

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/auth"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
)

type DynamicAgentConfiguration struct {
	mutex sync.RWMutex
	conf  *Configuration
}

func NewDynamicAgentConfiguration() *DynamicAgentConfiguration {
	return &DynamicAgentConfiguration{
		conf: NewConfiguration(),
	}
}

func (c *DynamicAgentConfiguration) GetDynamicConfiguration() *Configuration {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.conf
}

func (c *DynamicAgentConfiguration) SetDynamicConfiguration(conf *Configuration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.conf = conf
}

type Configuration struct {
	*adminqos.AdminQoSConfiguration
	*auth.AuthConfiguration
	*tmo.TransparentMemoryOffloadingConfiguration
	*strategygroup.StrategyGroupConfiguration
	*metricthreshold.MetricThresholdConfiguration
	*irqtuning.IRQTuningConfiguration
}

func NewConfiguration() *Configuration {
	return &Configuration{
		AdminQoSConfiguration:                    adminqos.NewAdminQoSConfiguration(),
		AuthConfiguration:                        auth.NewAuthConfiguration(),
		TransparentMemoryOffloadingConfiguration: tmo.NewTransparentMemoryOffloadingConfiguration(),
		StrategyGroupConfiguration:               strategygroup.NewStrategyGroupConfiguration(),
		MetricThresholdConfiguration:             metricthreshold.NewMetricThresholdConfiguration(),
		IRQTuningConfiguration:                   irqtuning.NewIRQTuningConfiguration(),
	}
}

func (c *Configuration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	c.AdminQoSConfiguration.ApplyConfiguration(conf)
	c.AuthConfiguration.ApplyConfiguration(conf)
	c.TransparentMemoryOffloadingConfiguration.ApplyConfiguration(conf)
	c.StrategyGroupConfiguration.ApplyConfiguration(conf)
	c.MetricThresholdConfiguration.ApplyConfiguration(conf)
	c.IRQTuningConfiguration.ApplyConfiguration(conf)
}
