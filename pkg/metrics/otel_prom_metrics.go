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

package metrics

import (
	"context"
	"fmt"
	"net/http"

	prometheusclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

type PrometheusMetricPathName string

const (
	PrometheusMetricPathNameDefault      PrometheusMetricPathName = "/metrics"
	PrometheusMetricPathNameCustomMetric PrometheusMetricPathName = "/custom_metric"
)

type openTelemetryPrometheusMetricsEmitter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
}

var _ MetricEmitter = &openTelemetryPrometheusMetricsEmitter{}

// NewOpenTelemetryPrometheusMetricsEmitter implement a MetricEmitter use open-telemetry sdk.
func NewOpenTelemetryPrometheusMetricsEmitter(_ *generic.MetricsConfiguration, pathName PrometheusMetricPathName,
	mux *http.ServeMux,
) (MetricEmitter, error) {
	registry := prometheusclient.NewRegistry()
	exporter, err := prometheus.New(
		prometheus.WithRegisterer(registry),
		prometheus.WithoutCounterSuffixes(),
		prometheus.WithoutScopeInfo(),
		prometheus.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize prometheus exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	mux.Handle(string(pathName), promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	p := &openTelemetryPrometheusMetricsEmitter{
		provider: provider,
		meter:    provider.Meter(""),
	}

	return p, nil
}

// StoreInt64 store a int64 metrics to prometheus collector.
func (p *openTelemetryPrometheusMetricsEmitter) StoreInt64(
	key string, val int64, emitType MetricTypeName, tags ...MetricTag,
) error {
	return p.storeInt64(key, val, emitType, p.convertTagsToMap(tags))
}

// StoreFloat64 store a float64 metrics to prometheus collector.
func (p *openTelemetryPrometheusMetricsEmitter) StoreFloat64(
	key string, val float64, emitType MetricTypeName, tags ...MetricTag,
) error {
	return p.storeFloat64(key, val, emitType, p.convertTagsToMap(tags))
}

func (p *openTelemetryPrometheusMetricsEmitter) WithTags(
	unit string, commonTags ...MetricTag,
) MetricEmitter {
	newMetricTagWrapper := &MetricTagWrapper{MetricEmitter: p}
	return newMetricTagWrapper.WithTags(unit, commonTags...)
}

func (p *openTelemetryPrometheusMetricsEmitter) Run(_ context.Context) {}

func (p *openTelemetryPrometheusMetricsEmitter) storeInt64(
	key string, val int64, emitType MetricTypeName, tags map[string]string,
) error {
	var err error
	switch emitType {
	case MetricTypeNameRaw:
		err = p.storeRawInt64(key, val, tags)
	case MetricTypeNameCount:
		err = p.storeCountInt64(key, val, tags)
	case MetricTypeNameUpDownCount:
		err = p.storeUpDownCountInt64(key, val, tags)
	default:
		err = fmt.Errorf("metrics type %s is not support", emitType)
	}

	if err != nil {
		klog.Errorf("storeInt64 failed emitType: %s, %s", emitType, err)
		return err
	}

	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeFloat64(key string,
	val float64, emitType MetricTypeName, tags map[string]string,
) error {
	var err error
	switch emitType {
	case MetricTypeNameRaw:
		err = p.storeRawFloat64(key, val, tags)
	case MetricTypeNameCount:
		err = p.storeCountFloat64(key, val, tags)
	case MetricTypeNameUpDownCount:
		err = p.storeUpDownCountFloat64(key, val, tags)
	default:
		err = fmt.Errorf("metrics type %s is not support", emitType)
	}

	if err != nil {
		klog.Errorf("storeFloat64 failed with emitType: %s, %s", emitType, err)
		return err
	}

	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeRawInt64(key string, val int64, tags map[string]string) error {
	instrument, err := p.meter.Int64Gauge(key)
	if err != nil {
		return err
	}

	instrument.Record(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeRawFloat64(key string, val float64, tags map[string]string) error {
	instrument, err := p.meter.Float64Gauge(key)
	if err != nil {
		return err
	}

	instrument.Record(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeCountInt64(key string, val int64, tags map[string]string) error {
	counter, err := p.meter.Int64Counter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeCountFloat64(key string, val float64, tags map[string]string) error {
	counter, err := p.meter.Float64Counter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeUpDownCountInt64(key string, val int64, tags map[string]string) error {
	counter, err := p.meter.Int64UpDownCounter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeUpDownCountFloat64(key string, val float64, tags map[string]string) error {
	counter, err := p.meter.Float64UpDownCounter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, metric.WithAttributes(p.convertMapToKeyValues(tags)...))
	return nil
}

// for simplify, only pass map to metrics related function
func (p *openTelemetryPrometheusMetricsEmitter) convertMapToKeyValues(tags map[string]string) []attribute.KeyValue {
	res := make([]attribute.KeyValue, 0, len(tags))
	for k, v := range tags {
		res = append(res, attribute.String(k, v))
	}
	return res
}

// to avoid duplicate tags, we will convert tags to map first
func (p *openTelemetryPrometheusMetricsEmitter) convertTagsToMap(tags []MetricTag) map[string]string {
	mTags := make(map[string]string)
	for _, t := range tags {
		mTags[t.Key] = t.Val
	}
	return mTags
}
