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

package reporter

import (
	"time"

	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NodeMetricReporterOptions holds the configurations for node metric reporter in qos aware plugin
type NodeMetricReporterOptions struct {
	SyncPeriod              time.Duration
	MetricSlidingWindowTime time.Duration
	AggregateFuncs          map[string]string
	AggregateArgs           map[string]string
}

// NewNodeMetricReporterOptions creates new Options with default config
func NewNodeMetricReporterOptions() *NodeMetricReporterOptions {
	return &NodeMetricReporterOptions{
		SyncPeriod:              10 * time.Second,
		MetricSlidingWindowTime: time.Minute,
		AggregateFuncs: map[string]string{
			string(v1.ResourceCPU):    general.SmoothWindowAggFuncAvg,
			string(v1.ResourceMemory): general.SmoothWindowAggFuncPerc,
		},
		AggregateArgs: map[string]string{
			string(v1.ResourceMemory): "95",
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *NodeMetricReporterOptions) AddFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&o.SyncPeriod, "node-metric-reporter-sync-period", o.SyncPeriod,
		"period for node metric reporter to sync node metrics")
	fs.DurationVar(&o.MetricSlidingWindowTime, "node-metric-reporter-sliding-window", o.MetricSlidingWindowTime,
		"the window duration for smoothing headroom resource sample")
	fs.StringToStringVar(&o.AggregateFuncs, "node-metric-reporter-aggregate-functions", o.AggregateFuncs,
		"the aggregate function of sliding window, like average, percentile, min, max, std")
	fs.StringToStringVar(&o.AggregateArgs, "node-metric-reporter-aggregate-args", o.AggregateArgs,
		"the args of aggregator function")
}

// ApplyTo fills up config with options
func (o *NodeMetricReporterOptions) ApplyTo(c *reporter.NodeMetricReporterConfiguration) error {
	c.SyncPeriod = o.SyncPeriod
	c.MetricSlidingWindowTime = o.MetricSlidingWindowTime
	for name, f := range o.AggregateFuncs {
		c.AggregateFuncs[v1.ResourceName(name)] = f
	}
	for name, a := range o.AggregateArgs {
		c.AggregateArgs[v1.ResourceName(name)] = a
	}
	return nil
}
