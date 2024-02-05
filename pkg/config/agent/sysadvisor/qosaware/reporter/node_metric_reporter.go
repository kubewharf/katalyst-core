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

	v1 "k8s.io/api/core/v1"
)

// NodeMetricReporterConfiguration stores configurations of node metric reporter in qos aware plugin
type NodeMetricReporterConfiguration struct {
	SyncPeriod              time.Duration
	MetricSlidingWindowTime time.Duration
	AggregateFuncs          map[v1.ResourceName]string
	AggregateArgs           map[v1.ResourceName]string
}

// NewNodeMetricReporterConfiguration creates new headroom reporter configurations
func NewNodeMetricReporterConfiguration() *NodeMetricReporterConfiguration {
	return &NodeMetricReporterConfiguration{
		SyncPeriod:              time.Second * 10,
		MetricSlidingWindowTime: time.Minute,
		AggregateFuncs:          map[v1.ResourceName]string{},
		AggregateArgs:           map[v1.ResourceName]string{},
	}
}
