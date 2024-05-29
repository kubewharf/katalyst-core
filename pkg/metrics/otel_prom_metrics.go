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
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/metric/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
	controllerTime "go.opentelemetry.io/otel/sdk/metric/controller/time"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	selector "go.opentelemetry.io/otel/sdk/metric/selector/simple"
	"go.opentelemetry.io/otel/sdk/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

const (
	openTelemetryPrometheusCollectPeriod = time.Second * 3
)

type PrometheusMetricPathName string

const (
	PrometheusMetricPathNameDefault      PrometheusMetricPathName = "/metrics"
	PrometheusMetricPathNameCustomMetric PrometheusMetricPathName = "/custom_metric"
)

type prometheusClockTicker struct {
	ticker *time.Ticker
}

func (t *prometheusClockTicker) Stop() {
	t.ticker.Stop()
}

func (t *prometheusClockTicker) C() <-chan time.Time {
	return t.ticker.C
}

type prometheusClock struct {
	last time.Time
	t    *prometheusClockTicker
}

func (c *prometheusClock) Now() time.Time {
	c.last = time.Now()
	return c.last
}

func (c *prometheusClock) Ticker(period time.Duration) controllerTime.Ticker {
	c.t = &prometheusClockTicker{ticker: time.NewTicker(period)}
	return c.t
}

func (c *prometheusClock) Stop() {
	c.t.Stop()
}

func (c *prometheusClock) C() <-chan time.Time {
	return c.t.C()
}

type openTelemetryPrometheusMetricsEmitter struct {
	c           *prometheusClock
	pathName    PrometheusMetricPathName
	metricsConf *generic.MetricsConfiguration

	exporter *prometheus.Exporter
	meter    metric.Meter
}

var _ MetricEmitter = &openTelemetryPrometheusMetricsEmitter{}

type customExportKindSelectorWrapper struct {
	export.ExportKindSelector
}

// ExportKindFor implements ExportKindSelector.
// we only use counter and up down counter as CumulativeExportKind to save memory
func (c customExportKindSelectorWrapper) ExportKindFor(desc *metric.Descriptor, kind aggregation.Kind) export.ExportKind {
	switch desc.InstrumentKind() {
	case metric.CounterInstrumentKind, metric.UpDownCounterInstrumentKind:
		return export.CumulativeExportKind
	default:
		return c.ExportKindSelector.ExportKindFor(desc, kind)
	}
}

// NewOpenTelemetryPrometheusMetricsEmitter implement a MetricEmitter use open-telemetry sdk.
func NewOpenTelemetryPrometheusMetricsEmitter(metricsConf *generic.MetricsConfiguration, pathName PrometheusMetricPathName,
	mux *http.ServeMux,
) (MetricEmitter, error) {
	exporter, err := prometheus.NewExporter(prometheus.Config{}, controller.New(
		processor.New(
			selector.NewWithInexpensiveDistribution(),
			customExportKindSelectorWrapper{export.StatelessExportKindSelector()},
			processor.WithMemory(false),
		),
		controller.WithCollectPeriod(openTelemetryPrometheusCollectPeriod),
		controller.WithResource(resource.NewWithAttributes()),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize prometheus exporter: %w", err)
	}
	c := &prometheusClock{last: time.Now()}
	exporter.Controller().SetClock(c)

	mux.HandleFunc(fmt.Sprintf("%v", pathName), exporter.ServeHTTP)

	meter := exporter.MeterProvider().Meter("")
	p := &openTelemetryPrometheusMetricsEmitter{
		c:           c,
		pathName:    pathName,
		metricsConf: metricsConf,

		exporter: exporter,
		meter:    meter,
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

func (p *openTelemetryPrometheusMetricsEmitter) Run(ctx context.Context) {
	klog.Infof("openTelemetry runs")
	go wait.Until(p.gc, time.Minute, ctx.Done())
}

func (p *openTelemetryPrometheusMetricsEmitter) gc() {
	// usw c.clock.Now() to judge whether we have collected
	if time.Since(p.c.last) > p.metricsConf.EmitterPrometheusGCTimeout {
		klog.Infof("trigger manual gc for %v", p.pathName)
		_ = p.exporter.Controller().Collect(context.Background())
	}
}

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
	instrument, err := p.meter.MeterImpl().NewSyncInstrument(metric.NewDescriptor(key, metric.ValueObserverInstrumentKind, number.Int64Kind))
	if err != nil {
		return err
	}

	instrument.RecordOne(context.TODO(), number.NewInt64Number(val), p.convertMapToKeyValues(tags))
	return err
}

func (p *openTelemetryPrometheusMetricsEmitter) storeRawFloat64(key string, val float64, tags map[string]string) error {
	instrument, err := p.meter.MeterImpl().NewSyncInstrument(metric.NewDescriptor(key, metric.ValueObserverInstrumentKind, number.Float64Kind))
	if err != nil {
		return err
	}

	instrument.RecordOne(context.TODO(), number.NewFloat64Number(val), p.convertMapToKeyValues(tags))
	return err
}

func (p *openTelemetryPrometheusMetricsEmitter) storeCountInt64(key string, val int64, tags map[string]string) error {
	counter, err := p.meter.NewInt64Counter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, p.convertMapToKeyValues(tags)...)
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeCountFloat64(key string, val float64, tags map[string]string) error {
	counter, err := p.meter.NewFloat64Counter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, p.convertMapToKeyValues(tags)...)
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeUpDownCountInt64(key string, val int64, tags map[string]string) error {
	counter, err := p.meter.NewInt64UpDownCounter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, p.convertMapToKeyValues(tags)...)
	return nil
}

func (p *openTelemetryPrometheusMetricsEmitter) storeUpDownCountFloat64(key string, val float64, tags map[string]string) error {
	counter, err := p.meter.NewFloat64UpDownCounter(key)
	if err != nil {
		return err
	}
	counter.Add(context.TODO(), val, p.convertMapToKeyValues(tags)...)
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
