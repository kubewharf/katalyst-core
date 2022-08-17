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

package metric_emitter

import (
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// MetricEmitterPluginConfiguration stores configurations of custom-metric emitter plugin
type MetricEmitterPluginConfiguration struct {
	PodMetricLabel  sets.String
	NodeMetricLabel sets.String

	PodSyncPeriod time.Duration
}

// NewMetricEmitterPluginConfiguration creates a new custom-metric emitter plugin configuration.
func NewMetricEmitterPluginConfiguration() *MetricEmitterPluginConfiguration {
	return &MetricEmitterPluginConfiguration{
		PodMetricLabel:  make(sets.String),
		NodeMetricLabel: make(sets.String),
	}
}

// ApplyConfiguration is used to set configuration based on conf.
func (c *MetricEmitterPluginConfiguration) ApplyConfiguration(conf *dynamic.DynamicConfigCRD) {
}
