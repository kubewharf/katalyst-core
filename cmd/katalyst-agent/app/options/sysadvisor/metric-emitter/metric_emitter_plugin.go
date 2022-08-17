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
	cliflag "k8s.io/component-base/cli/flag"

	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
)

// MetricEmitterPluginOptions holds the configurations for custom-metric emitter plugin.
type MetricEmitterPluginOptions struct {
	PodMetricLabels  []string
	NodeMetricLabels []string

	PodSyncPeriod time.Duration
}

// NewMetricEmitterPluginOptions creates a new Options with a default config.
func NewMetricEmitterPluginOptions() *MetricEmitterPluginOptions {
	return &MetricEmitterPluginOptions{
		PodMetricLabels:  []string{},
		NodeMetricLabels: []string{},

		PodSyncPeriod: 30 * time.Second,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetricEmitterPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-emitter-plugin")

	fs.StringSliceVar(&o.PodMetricLabels, "metric-pod-labels", o.PodMetricLabels,
		"pod labels to be added in metric selector lists")
	fs.StringSliceVar(&o.NodeMetricLabels, "metric-node-labels", o.NodeMetricLabels,
		"node labels to be added in metric selector lists")

	fs.DurationVar(&o.PodSyncPeriod, "metric-pod-sync-period", o.PodSyncPeriod,
		"the period that pod sync logic")
}

// ApplyTo fills up config with options
func (o *MetricEmitterPluginOptions) ApplyTo(c *metricemitter.MetricEmitterPluginConfiguration) error {
	c.PodMetricLabel = sets.NewString(o.PodMetricLabels...)
	c.NodeMetricLabel = sets.NewString(o.NodeMetricLabels...)

	c.PodSyncPeriod = o.PodSyncPeriod
	return nil
}
