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

	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// customMetricsEmitterPool registers itself as an implementation
// metrics.MetricEmitter, so that whenever SetDefaultMetricsEmitter is called,
// it will always work the newly set MetricEmitter as default implementation.
type customMetricsEmitterPool struct {
	started                    *atomic.Bool
	emitterPool                MetricsEmitterPool
	customDefaultMetricEmitter metrics.MetricEmitter
}

var _ MetricsEmitterPool = &customMetricsEmitterPool{}

func NewCustomMetricsEmitterPool(emitterPool MetricsEmitterPool) MetricsEmitterPool {
	return &customMetricsEmitterPool{
		started:                    atomic.NewBool(false),
		emitterPool:                emitterPool,
		customDefaultMetricEmitter: emitterPool.GetDefaultMetricsEmitter(),
	}
}

// GetDefaultMetricsEmitter return custom wrapped metric emitter
func (p *customMetricsEmitterPool) GetDefaultMetricsEmitter() metrics.MetricEmitter {
	return p
}

func (p *customMetricsEmitterPool) SetDefaultMetricsEmitter(metricEmitter metrics.MetricEmitter) {
	if p.started.Load() {
		return
	}

	p.customDefaultMetricEmitter = metricEmitter
}

func (p *customMetricsEmitterPool) GetMetricsEmitter(parameters interface{}) (metrics.MetricEmitter, error) {
	return p.emitterPool.GetMetricsEmitter(parameters)
}

func (p *customMetricsEmitterPool) Run(ctx context.Context) {
	if p.started.Swap(true) {
		return
	}

	p.customDefaultMetricEmitter.Run(ctx)
	p.emitterPool.Run(ctx)
}

func (p *customMetricsEmitterPool) StoreInt64(key string, val int64,
	emitType metrics.MetricTypeName, tags ...metrics.MetricTag,
) error {
	return p.customDefaultMetricEmitter.StoreInt64(key, val,
		emitType, tags...)
}

func (p *customMetricsEmitterPool) StoreFloat64(key string, val float64,
	emitType metrics.MetricTypeName, tags ...metrics.MetricTag,
) error {
	return p.customDefaultMetricEmitter.StoreFloat64(key, val,
		emitType, tags...)
}

func (p *customMetricsEmitterPool) WithTags(unit string,
	commonTags ...metrics.MetricTag,
) metrics.MetricEmitter {
	return p.customDefaultMetricEmitter.WithTags(unit,
		commonTags...)
}
