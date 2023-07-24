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
)

type MetricEmitterPodConfiguration struct {
	PodMetricLabel sets.String
	PodSyncPeriod  time.Duration

	// nominated annotations and labels to skip to avoid too many metrics;
	// if multiple key-value pairs are nominated, skip if any pair matches
	PodSkipAnnotations map[string]string
	PodSkipLabels      map[string]string

	// MetricMapping will override the default to-collected metrics in pod-level
	MetricMapping map[string]string
}

type MetricEmitterNodeConfiguration struct {
	NodeMetricLabel sets.String

	// MetricMapping will override the default to-collected metrics in node-level
	MetricMapping map[string]string
}

type MetricEmitterExternalConfiguration struct{}

// MetricEmitterPluginConfiguration stores configurations of custom-metric emitter plugin
type MetricEmitterPluginConfiguration struct {
	*MetricEmitterPodConfiguration
	*MetricEmitterNodeConfiguration
	*MetricEmitterExternalConfiguration

	// MetricSyncers defines those syncers that should be enabled
	MetricSyncers []string
}

// NewMetricEmitterPluginConfiguration creates a new custom-metric emitter plugin configuration.
func NewMetricEmitterPluginConfiguration() *MetricEmitterPluginConfiguration {
	return &MetricEmitterPluginConfiguration{
		MetricEmitterPodConfiguration: &MetricEmitterPodConfiguration{
			PodMetricLabel:     make(sets.String),
			PodSkipAnnotations: make(map[string]string),
			PodSkipLabels:      make(map[string]string),
		},
		MetricEmitterNodeConfiguration: &MetricEmitterNodeConfiguration{
			NodeMetricLabel: make(sets.String),
		},
		MetricEmitterExternalConfiguration: &MetricEmitterExternalConfiguration{},
	}
}
