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

	// PodKiller specify the pod killer implementation
	PodKiller string

	// StrictAuthentication means whether to authenticate plugins strictly
	StrictAuthentication bool

	// PodMetricLabels defines the pod labels to be added in metric selector lists
	PodMetricLabels sets.String

	// HostPathNotifierRootPath
	HostPathNotifierRootPath string
}

type EvictionConfiguration struct {
	*ReclaimedResourcesEvictionConfiguration
	*MemoryPressureEvictionConfiguration
	*CPUPressureEvictionConfiguration
}

func NewGenericEvictionConfiguration() *GenericEvictionConfiguration {
	return &GenericEvictionConfiguration{
		EvictionSkippedAnnotationKeys: sets.NewString(),
		EvictionSkippedLabelKeys:      sets.NewString(),
		PodMetricLabels:               sets.NewString(),
	}
}

func NewEvictionConfiguration() *EvictionConfiguration {
	return &EvictionConfiguration{
		ReclaimedResourcesEvictionConfiguration: NewReclaimedResourcesEvictionConfiguration(),
		MemoryPressureEvictionConfiguration:     NewMemoryPressureEvictionPluginConfiguration(),
		CPUPressureEvictionConfiguration:        NewCPUPressureEvictionConfiguration(),
	}
}
