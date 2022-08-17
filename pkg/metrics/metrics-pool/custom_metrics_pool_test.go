package metrics_pool

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewCustomMetricsEmitterPool(t *testing.T) {
	m, err := NewOpenTelemetryPrometheusMetricsEmitterPool(http.NewServeMux())
	assert.NoError(t, err)

	p := NewCustomMetricsEmitterPool(m)

	custom, err := m.GetMetricsEmitter(PrometheusMetricOptions{
		Path: "/custom-metrics",
	})

	p.SetDefaultMetricsEmitter(custom)
}
