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
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func newMetricsEmitter() (MetricEmitter, error) {
	return NewOpenTelemetryPrometheusMetricsEmitter(generic.NewMetricsConfiguration(), "/metrics", http.DefaultServeMux)
}

func TestMetrics(t *testing.T) {
	t.Parallel()

	emitt, err := newMetricsEmitter()
	if err != nil {
		t.Errorf("new open telemetry Prometheus Metrics Emitter error:%v", err)
	}

	w := emitt.WithTags("test")
	err = w.StoreInt64("int", 1, MetricTypeNameRaw)
	if err != nil {
		t.Errorf("store int error:%v", err)
	}

	err = w.StoreFloat64("float", 2.0, MetricTypeNameRaw)
	if err != nil {
		t.Errorf("store float error:%v", err)
	}

	go wait.Until(func() {
		_ = w.StoreFloat64("float_count_every_1s", 1.0, MetricTypeNameCount)
		_ = w.StoreInt64("int_count_every_1s", 1, MetricTypeNameCount)
		_ = w.StoreFloat64("float_up_down_count_every_1s", 1.0, MetricTypeNameUpDownCount)
		_ = w.StoreInt64("int_up_down_count_every_1s", 1, MetricTypeNameUpDownCount)
	}, 1*time.Second, context.TODO().Done())

	// NOTE: previously this test started an HTTP server on the hardcoded ":8080"
	// and klog.Fatal'd on bind failure. That bound a well-known port (which may
	// already be held by a host service) and aborted the whole test binary via
	// os.Exit, making the test flaky/environment-dependent. The test only
	// exercises metric storage, so no listener is required; the prometheus
	// handler remains registered on the provided mux without being served.
}

func TestClock(t *testing.T) {
	t.Parallel()

	last := time.Now()
	c := prometheusClock{last: last}
	_ = c.Ticker(time.Millisecond * 3)

	select {
	case <-c.t.C():
		c.Now()
	}

	assert.Equal(t, true, c.Now().Sub(last) > time.Millisecond*3)
	c.Stop()
}
