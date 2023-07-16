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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestNewCustomMetricsEmitterPool(t *testing.T) {
	t.Parallel()

	m, err := NewOpenTelemetryPrometheusMetricsEmitterPool(generic.NewMetricsConfiguration(), http.NewServeMux())
	assert.NoError(t, err)

	p := NewCustomMetricsEmitterPool(m)

	custom, err := m.GetMetricsEmitter(PrometheusMetricOptions{
		Path: "/custom-metrics",
	})
	assert.NoError(t, err)

	p.SetDefaultMetricsEmitter(custom)
}
