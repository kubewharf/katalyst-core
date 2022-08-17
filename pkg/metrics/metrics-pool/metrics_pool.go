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

package metrics_pool

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// MetricsEmitterPool is a metrics emitter pool for metrics emitter.
// it stores all the emitter implementations, and provide a getter
// function to acquire them. this thought is much like the thread pool.
type MetricsEmitterPool interface {
	// GetDefaultMetricsEmitter returns the default metrics.MetricEmitter
	// that should be handled by this mux; while SetDefaultMetricsEmitter
	// provides a way to set the default metrics.MetricEmitter
	GetDefaultMetricsEmitter() metrics.MetricEmitter
	SetDefaultMetricsEmitter(metricEmitter metrics.MetricEmitter)

	// GetMetricsEmitter returns the specific metrics.MetricEmitter.
	// the parameters are used for certain pool implementation to parse request info.
	// we will always try to get emitter from local cache, and creating one if not exist.
	GetMetricsEmitter(parameters interface{}) (metrics.MetricEmitter, error)

	// Run starts the syncing logic of emitter pool implementations
	Run(ctx context.Context)
}

type DummyMetricsEmitterPool struct{}

var _ MetricsEmitterPool = DummyMetricsEmitterPool{}

func (d DummyMetricsEmitterPool) GetDefaultMetricsEmitter() metrics.MetricEmitter {
	return metrics.DummyMetrics{}
}

func (d DummyMetricsEmitterPool) SetDefaultMetricsEmitter(_ metrics.MetricEmitter) {}

func (d DummyMetricsEmitterPool) GetMetricsEmitter(_ interface{}) (metrics.MetricEmitter, error) {
	return metrics.DummyMetrics{}, nil
}
func (d DummyMetricsEmitterPool) Run(_ context.Context) {}
