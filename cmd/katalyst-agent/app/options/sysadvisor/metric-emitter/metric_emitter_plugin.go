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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/types"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// MetricEmitterPluginOptions holds the configurations for custom-metric emitter plugin.
type MetricEmitterPluginOptions struct {
	PodMetricLabels    []string
	PodSkipAnnotations string
	PodSkipLabels      string
	PodSyncPeriod      time.Duration
	PodMetricMapping   map[string]string

	NodeMetricLabels  []string
	NodeMetricMapping map[string]string

	MetricSyncers []string
}

// NewMetricEmitterPluginOptions creates a new Options with a default config.
func NewMetricEmitterPluginOptions() *MetricEmitterPluginOptions {
	return &MetricEmitterPluginOptions{
		PodMetricLabels:    []string{},
		PodSkipAnnotations: "",
		PodSkipLabels:      "",
		PodSyncPeriod:      30 * time.Second,

		NodeMetricLabels: []string{},

		MetricSyncers: []string{types.MetricSyncerNamePod, types.MetricSyncerNameNode},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetricEmitterPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-emitter-plugin")

	fs.StringSliceVar(&o.PodMetricLabels, "metric-pod-labels", o.PodMetricLabels,
		"pod labels to be added in metric selector lists")
	fs.StringVar(&o.PodSkipAnnotations, "metric-pod-skip-annotations", o.PodSkipAnnotations,
		"if any pod has annotations as defined, skip to collect metrics for them")
	fs.StringVar(&o.PodSkipLabels, "metric-pod-skip-labels", o.PodSkipLabels,
		"if any pod has annotations as defined, skip to collect metrics for them")
	fs.DurationVar(&o.PodSyncPeriod, "metric-pod-sync-period", o.PodSyncPeriod,
		"the period that pod sync logic")
	fs.StringToStringVar(&o.PodMetricMapping, "metric-pod-metrics", o.PodMetricMapping,
		"the metric name for pod-level to override the default collecting logic")

	fs.StringSliceVar(&o.NodeMetricLabels, "metric-node-labels", o.NodeMetricLabels,
		"node labels to be added in metric selector lists")
	fs.StringToStringVar(&o.NodeMetricMapping, "metric-node-metrics", o.NodeMetricMapping,
		"the metric name for node-level to override the default collecting logic")

	fs.StringSliceVar(&o.MetricSyncers, "metric-syncers", o.MetricSyncers,
		"those syncers that should be enabled")
}

// ApplyTo fills up config with options
func (o *MetricEmitterPluginOptions) ApplyTo(c *metricemitter.MetricEmitterPluginConfiguration) error {
	c.PodMetricLabel = sets.NewString(o.PodMetricLabels...)
	c.PodSyncPeriod = o.PodSyncPeriod

	podSkipLabels, err := general.ParseMapWithPrefix("", o.PodSkipLabels)
	if err != nil {
		return err
	}
	c.PodSkipLabels = podSkipLabels

	podSkipAnnotations, err := general.ParseMapWithPrefix("", o.PodSkipAnnotations)
	if err != nil {
		return err
	}
	c.PodSkipAnnotations = podSkipAnnotations

	if o.PodMetricMapping != nil && len(o.PodMetricLabels) != 0 {
		c.MetricEmitterPodConfiguration.MetricMapping = o.PodMetricMapping
	}

	c.NodeMetricLabel = sets.NewString(o.NodeMetricLabels...)

	if o.NodeMetricMapping != nil && len(o.NodeMetricMapping) != 0 {
		c.MetricEmitterNodeConfiguration.MetricMapping = o.NodeMetricMapping
	}

	c.MetricSyncers = o.MetricSyncers
	return nil
}
