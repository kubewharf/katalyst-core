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

// Package metrics is the package that contains those implementations to
// emit metrics to reflect the running states of current process.
package metrics // import "github.com/kubewharf/katalyst-core/pkg/metrics"

import "context"

type MetricTypeName string

const (
	// The MetricTypeNameRaw is stateless, the MetricTypeNameCount and MetricTypeNameUpDownCount
	// are stateful, so when we need store metrics with high dimension tags (such as pod name or uuid),
	// we must use MetricTypeNameRaw to avoid oom.
	// MetricTypeNameRaw emit raw metrics which report the last value
	MetricTypeNameRaw MetricTypeName = "raw"
	// MetricTypeNameCount emit counter metrics which is monotonic
	MetricTypeNameCount MetricTypeName = "count"
	// MetricTypeNameUpDownCount emit up down count metrics which isn't monotonic
	MetricTypeNameUpDownCount MetricTypeName = "up_down_count"
)

type MetricTag struct {
	Key, Val string
}

// MetricEmitter interface defines the action of emitting metrics,
// support to use different kinds of metrics emitter if needed
type MetricEmitter interface {
	// StoreInt64 receives the given int64 metrics item and sends it the backend store.
	StoreInt64(key string, val int64, emitType MetricTypeName, tags ...MetricTag) error
	// StoreFloat64 receives the given float64 metrics item and sends it the backend store.
	StoreFloat64(key string, val float64, emitType MetricTypeName, tags ...MetricTag) error
	// WithTags add unit tag and common tags to emitter.
	WithTags(unit string, commonTags ...MetricTag) MetricEmitter
	// Run is ensure the starting logic works, since emitter like
	// prometheus need to be started to trigger gc logic
	Run(ctx context.Context)
}

type DummyMetrics struct{}

func (d DummyMetrics) StoreInt64(_ string, _ int64, _ MetricTypeName, _ ...MetricTag) error {
	return nil
}

func (d DummyMetrics) StoreFloat64(_ string, _ float64, _ MetricTypeName, _ ...MetricTag) error {
	return nil
}

func (d DummyMetrics) WithTags(unit string, commonTags ...MetricTag) MetricEmitter {
	newMetricTagWrapper := &MetricTagWrapper{MetricEmitter: d}
	return newMetricTagWrapper.WithTags(unit, commonTags...)
}
func (d DummyMetrics) Run(_ context.Context) {}

var _ MetricEmitter = DummyMetrics{}

// ConvertMapToTags only pass map to metrics related function
func ConvertMapToTags(tags map[string]string) []MetricTag {
	res := make([]MetricTag, 0, len(tags))
	for k, v := range tags {
		res = append(res, MetricTag{Key: k, Val: v})
	}
	return res
}
