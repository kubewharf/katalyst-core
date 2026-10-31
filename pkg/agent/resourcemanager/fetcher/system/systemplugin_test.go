//go:build linux
// +build linux

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

package system

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

// generateTestConfiguration builds a Configuration suitable for unit tests:
// production defaults (e.g. /var/lib/katalyst, real containerd endpoint) are
// overridden so that no system directory is created and no local containerd
// socket is required. Production code paths are unaffected.
func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.EnableMetricsFetcher = false
	// Redirect checkpoint dir to a per-test temp dir so we never touch
	// /var/lib/katalyst (which requires root and is absent in CI).
	testConfiguration.CheckpointManagerDir = t.TempDir()
	return testConfiguration
}

func newTestMetaServer(t *testing.T, conf *config.Configuration) *metaserver.MetaServer {
	t.Helper()

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}

	meta, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)
	require.NotNil(t, meta)
	return meta
}

func Test_systemPlugin_GetReportContent(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	meta := newTestMetaServer(t, conf)

	metricsFetcher := meta.MetricsFetcher.(*metric.FakeMetricsFetcher)
	metricsFetcher.SetSynced(true)
	metricsFetcher.SetByStringIndex(consts.MetricCPUCodeName, "Intel_SkyLake")
	metricsFetcher.SetByStringIndex(consts.MetricInfoIsVM, false)

	plugin, err := NewSystemReporterPlugin(metrics.DummyMetrics{}, meta, conf, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	content, err := plugin.GetReportContent(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, content)
}

func Test_systemPlugin_WithCache(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	meta := newTestMetaServer(t, conf)

	// mock a cache response
	mockCache := &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			{
				GroupVersionKind: &util.CNRGroupVersionKind,
			},
		},
	}

	plugin, err := NewSystemReporterPlugin(metrics.DummyMetrics{}, meta, conf, nil, mockCache)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	// Verify that the cache is properly set during initialization
	cachedResponse := plugin.GetCache()
	assert.NotNil(t, cachedResponse)
	assert.Equal(t, mockCache, cachedResponse)
}

// --- boundary tests: construction failure must return an error and never
// produce a usable plugin with nil dependencies. These tests use non-nil
// placeholders so they do not depend on a fully bootstrapped MetaServer. ---

func TestNewSystemReporterPlugin_NilEmitter(t *testing.T) {
	t.Parallel()

	plugin, err := NewSystemReporterPlugin(nil, &metaserver.MetaServer{}, &config.Configuration{}, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, plugin)
	assert.Contains(t, err.Error(), "emitter")
}

func TestNewSystemReporterPlugin_NilMetaServer(t *testing.T) {
	t.Parallel()

	plugin, err := NewSystemReporterPlugin(metrics.DummyMetrics{}, nil, &config.Configuration{}, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, plugin)
	assert.Contains(t, err.Error(), "metaServer")
}

func TestNewSystemReporterPlugin_NilConf(t *testing.T) {
	t.Parallel()

	plugin, err := NewSystemReporterPlugin(metrics.DummyMetrics{}, &metaserver.MetaServer{}, nil, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, plugin)
	assert.Contains(t, err.Error(), "conf")
}

// TestNewSystemReporterPlugin_PartialDeps ensures that when one required
// dependency is missing the constructor rejects the request atomically: no
// plugin is returned and no nil reference leaks into the caller.
func TestNewSystemReporterPlugin_PartialDeps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		emitter metrics.MetricEmitter
		meta    *metaserver.MetaServer
		conf    *config.Configuration
		errSub  string
	}{
		{"all-nil", nil, nil, nil, "emitter"},
		{"nil-meta", metrics.DummyMetrics{}, nil, &config.Configuration{}, "metaServer"},
		{"nil-conf", metrics.DummyMetrics{}, &metaserver.MetaServer{}, nil, "conf"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plugin, err := NewSystemReporterPlugin(tc.emitter, tc.meta, tc.conf, nil, nil)
			assert.Error(t, err)
			assert.Nil(t, plugin)
			assert.Contains(t, err.Error(), tc.errSub)
		})
	}
}
