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

package eviction

import (
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type GenericEvictionConfiguration struct {
	// Inner plugins is the list of plugins implemented in katalyst to enable or disable
	// '*' means "all enabled by default"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	InnerPlugins []string

	// ConditionTransitionPeriod is duration the eviction manager has to wait before transitioning out of a condition.
	ConditionTransitionPeriod time.Duration

	// EvictionManagerSyncPeriod is the interval duration that eviction manager fetches information from registered plugins
	EvictionManagerSyncPeriod time.Duration

	// those two variables are used to filter out eviction-free pods
	EvictionSkippedAnnotationKeys sets.String
	EvictionSkippedLabelKeys      sets.String

	// EvictionBurst limit the burst eviction counts
	EvictionBurst int
}

type EvictionPluginsConfiguration struct {
	*ReclaimedResourcesEvictionPluginConfiguration
	*MemoryPressureEvictionPluginConfiguration
	*CPUPressureEvictionPluginConfiguration
}

func NewGenericEvictionConfiguration() *GenericEvictionConfiguration {
	return &GenericEvictionConfiguration{
		EvictionSkippedAnnotationKeys: sets.NewString(),
		EvictionSkippedLabelKeys:      sets.NewString(),
	}
}

func (c *GenericEvictionConfiguration) ApplyConfiguration(*GenericEvictionConfiguration, *dynamic.DynamicConfigCRD) {
}

func NewEvictionPluginsConfiguration() *EvictionPluginsConfiguration {
	return &EvictionPluginsConfiguration{
		ReclaimedResourcesEvictionPluginConfiguration: NewReclaimedResourcesEvictionPluginConfiguration(),
		MemoryPressureEvictionPluginConfiguration:     NewMemoryPressureEvictionPluginConfiguration(),
		CPUPressureEvictionPluginConfiguration:        NewCPUPressureEvictionPluginConfiguration(),
	}
}

func (c *EvictionPluginsConfiguration) ApplyConfiguration(defaultConf *EvictionPluginsConfiguration, conf *dynamic.DynamicConfigCRD) {
	c.ReclaimedResourcesEvictionPluginConfiguration.ApplyConfiguration(defaultConf.ReclaimedResourcesEvictionPluginConfiguration, conf)
	c.MemoryPressureEvictionPluginConfiguration.ApplyConfiguration(defaultConf.MemoryPressureEvictionPluginConfiguration, conf)
	c.CPUPressureEvictionPluginConfiguration.ApplyConfiguration(defaultConf.CPUPressureEvictionPluginConfiguration, conf)
}
