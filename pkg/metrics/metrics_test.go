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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestMetrics(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	emitter, err := NewOpenTelemetryPrometheusMetricsEmitter(
		generic.NewMetricsConfiguration(),
		PrometheusMetricPathNameCustomMetric,
		mux,
	)
	if err != nil {
		t.Fatalf("new open telemetry Prometheus Metrics Emitter error: %v", err)
	}

	emitter = emitter.WithTags("test", MetricTag{Key: "source", Val: "unit-test"})
	for _, tc := range []struct {
		key      string
		value    int64
		emitType MetricTypeName
	}{
		{key: "raw_value", value: 1, emitType: MetricTypeNameRaw},
		{key: "raw_value", value: 2, emitType: MetricTypeNameRaw},
		{key: "count_value", value: 2, emitType: MetricTypeNameCount},
		{key: "count_value", value: 3, emitType: MetricTypeNameCount},
		{key: "up_down_value", value: 5, emitType: MetricTypeNameUpDownCount},
		{key: "up_down_value", value: -2, emitType: MetricTypeNameUpDownCount},
	} {
		if err := emitter.StoreInt64(tc.key, tc.value, tc.emitType); err != nil {
			t.Fatalf("store %s: %v", tc.key, err)
		}
	}
	for _, tc := range []struct {
		key      string
		value    float64
		emitType MetricTypeName
	}{
		{key: "raw_float_value", value: 1.5, emitType: MetricTypeNameRaw},
		{key: "raw_float_value", value: 2.5, emitType: MetricTypeNameRaw},
		{key: "count_float_value", value: 1.25, emitType: MetricTypeNameCount},
		{key: "count_float_value", value: 2.25, emitType: MetricTypeNameCount},
		{key: "up_down_float_value", value: 4.5, emitType: MetricTypeNameUpDownCount},
		{key: "up_down_float_value", value: -1.25, emitType: MetricTypeNameUpDownCount},
	} {
		if err := emitter.StoreFloat64(tc.key, tc.value, tc.emitType); err != nil {
			t.Fatalf("store %s: %v", tc.key, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, string(PrometheusMetricPathNameCustomMetric), nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected scrape status: %d", recorder.Code)
	}

	body := recorder.Body.String()
	for _, expected := range []string{
		`raw_value{emmit_unit="test",source="unit-test"} 2`,
		`count_value{emmit_unit="test",source="unit-test"} 5`,
		`up_down_value{emmit_unit="test",source="unit-test"} 3`,
		`raw_float_value{emmit_unit="test",source="unit-test"} 2.5`,
		`count_float_value{emmit_unit="test",source="unit-test"} 3.5`,
		`up_down_float_value{emmit_unit="test",source="unit-test"} 3.25`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("scrape output does not contain %q:\n%s", expected, body)
		}
	}
}
