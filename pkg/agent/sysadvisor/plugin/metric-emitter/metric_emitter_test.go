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

package metric_emitter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

// Test_noneExistMetricsFetcher exercises the emitter lifecycle when the
// MetaServer exists but exposes no MetricsFetcher. The fixture is built
// explicitly (a valid MetaServer with a nil MetricsFetcher) rather than
// relying on MetaServer construction failing, and it does not touch the host
// checkpoint directory. The emitter must surface the missing dependency as an
// observable error and shut down cleanly instead of panicking inside a
// background goroutine.
func Test_noneExistMetricsFetcher(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t)
	// MetaCache persists advisor state through StateFileDirectory, while
	// CheckpointManagerDir belongs to MetaServer. Isolate both so this lifecycle
	// test never depends on host-writable production paths.
	conf.CheckpointManagerDir = t.TempDir()
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = t.TempDir()

	conf.EnableMetricsFetcher = false

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	metaServer, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)
	require.NotNil(t, metaServer)
	// Construct all production defaults successfully, then remove only the
	// dependency this test is intended to exercise.
	metaServer.MetricsFetcher = nil

	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, nil)
	require.NoError(t, err)

	f, err := NewCustomMetricEmitter(PluginNameCustomMetricEmitter, conf, struct{}{}, metricspool.DummyMetricsEmitterPool{}, metaServer, metaCache)
	require.NoError(t, err)
	require.NoError(t, f.Init())

	// Do NOT cancel this context: the emitter must return on its own because a
	// syncer reports the missing MetricsFetcher as an error, which triggers the
	// emitter's cleanup. If the lifecycle regressed to a nil-deref panic inside a
	// detached goroutine, this test binary would crash.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()

	select {
	case <-done:
		// Run returns only after CustomMetricEmitter has canceled its shared
		// context and waited for every syncer goroutine. This emitter-owned join is
		// the deterministic leak check; process-wide goroutine counts are polluted
		// by unrelated parallel tests under make test.
	case <-time.After(2 * time.Second):
		t.Fatalf("emitter.Run should have returned after syncer dependency error, but it is still blocking")
	}
}
