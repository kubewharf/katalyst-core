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

	"github.com/stretchr/testify/assert"
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

// todo: change to dummy malachite implementation instead of fake testing
func Test_noneExistMetricsFetcher(t *testing.T) {
	client := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}

	conf := generateTestConfiguration(t)
	meta, err := metaserver.NewMetaServer(client, metrics.DummyMetrics{}, conf)
	if err == nil {
		ctx, cancel := context.WithCancel(context.Background())
		go meta.Run(ctx)
		time.Sleep(100 * time.Millisecond)
		cancel()
	}

	metaCache, err := metacache.NewMetaCacheImp(conf, nil)
	assert.NoError(t, err, nil)

	f, err := NewCustomMetricEmitter(conf, struct{}{}, metricspool.DummyMetricsEmitterPool{}, meta, metaCache)
	assert.NoError(t, err)

	err = f.Init()
	assert.NoError(t, err)

	go f.Run(context.Background())
	time.Sleep(time.Second * 3)
}
