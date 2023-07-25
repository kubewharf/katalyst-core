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
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PrometheusMetricOptions struct {
	Path metrics.PrometheusMetricPathName
}

// openTelemetryPrometheusMetricsEmitterPool is a metrics emitter mux for metrics.openTelemetryPrometheusMetricsEmitter.
type openTelemetryPrometheusMetricsEmitterPool struct {
	sync.Mutex
	genericConf *generic.MetricsConfiguration

	mux      *http.ServeMux
	emitters map[metrics.PrometheusMetricPathName]metrics.MetricEmitter
	started  map[metrics.PrometheusMetricPathName]bool
}

var _ MetricsEmitterPool = &openTelemetryPrometheusMetricsEmitterPool{}

func NewOpenTelemetryPrometheusMetricsEmitterPool(genericConf *generic.MetricsConfiguration, mux *http.ServeMux) (MetricsEmitterPool, error) {
	m := &openTelemetryPrometheusMetricsEmitterPool{
		mux:         mux,
		genericConf: genericConf,
		emitters:    make(map[metrics.PrometheusMetricPathName]metrics.MetricEmitter),
		started:     make(map[metrics.PrometheusMetricPathName]bool),
	}

	if _, err := m.GetMetricsEmitter(PrometheusMetricOptions{
		Path: metrics.PrometheusMetricPathNameDefault,
	}); err != nil {
		return nil, fmt.Errorf("init default emitter err: %v", err)
	}

	return m, nil
}

// GetDefaultMetricsEmitter returns metrics emitter with default path
func (m *openTelemetryPrometheusMetricsEmitterPool) GetDefaultMetricsEmitter() metrics.MetricEmitter {
	m.Lock()
	defer m.Unlock()
	return m.emitters[metrics.PrometheusMetricPathNameDefault]
}

// SetDefaultMetricsEmitter is not supported by openTelemetryPrometheusMetricsEmitterPool
func (m *openTelemetryPrometheusMetricsEmitterPool) SetDefaultMetricsEmitter(_ metrics.MetricEmitter) {
}

// GetMetricsEmitter get a prometheus metrics emitter which is exposed to an input path.
func (m *openTelemetryPrometheusMetricsEmitterPool) GetMetricsEmitter(para interface{}) (metrics.MetricEmitter, error) {
	m.Lock()
	defer m.Unlock()

	options, ok := para.(PrometheusMetricOptions)
	if !ok {
		return metrics.DummyMetrics{}, fmt.Errorf("failed to transform %v to path", para)
	}

	pathName := options.Path
	if _, ok := m.emitters[pathName]; !ok {
		e, err := metrics.NewOpenTelemetryPrometheusMetricsEmitter(m.genericConf, pathName, m.mux)
		if err != nil {
			return nil, err
		}
		m.emitters[pathName] = e
		general.Infof("add path %s to metric emitter", pathName)
	}
	m.started[pathName] = false
	return m.emitters[pathName], nil
}

func (m *openTelemetryPrometheusMetricsEmitterPool) Run(ctx context.Context) {
	go wait.Until(func() {
		m.Lock()
		defer m.Unlock()

		for pathName := range m.emitters {
			if !m.started[pathName] {
				m.emitters[pathName].Run(ctx)
				m.started[pathName] = true
			}
		}
	}, time.Minute, ctx.Done())
}
