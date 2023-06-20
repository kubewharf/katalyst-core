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

package util

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	metacacheplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware"
	"github.com/kubewharf/katalyst-core/pkg/client"
	katalystconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generatePluginConfig(t *testing.T, ckDir, sfDir string) *katalystconfig.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.QRMServers = []string{}

	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = sfDir
	testConfiguration.MetaServerConfiguration.CheckpointManagerDir = ckDir

	return testConfiguration
}

func TestAdvisor(t *testing.T) {

	ckDir, err := ioutil.TempDir("", "checkpoint")
	require.NoError(t, err)
	defer os.RemoveAll(ckDir)

	sfDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer os.RemoveAll(sfDir)

	conf := generatePluginConfig(t, ckDir, sfDir)

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	meta, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	meta.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
		},
	}

	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)
	meta.MetricsFetcher = fakeMetricsFetcher
	meta.PodFetcher = &pod.PodFetcherStub{}

	advisor, err := sysadvisor.NewAdvisorAgent(conf, struct{}{}, meta, metricspool.DummyMetricsEmitterPool{})
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go advisor.Run(ctx)

	time.Sleep(time.Second * 3)
	cancel()
}

func TestPlugins(t *testing.T) {
	type args struct {
		initFn plugin.AdvisorPluginInitFunc
	}
	tests := []struct {
		name string
		arg  args
	}{
		{
			name: "test-metacachePlugin",
			arg: args{
				initFn: metacacheplugin.NewMetaCachePlugin,
			},
		},
		{
			name: "test-qosAwarePlugin",
			arg: args{
				initFn: qosaware.NewQoSAwarePlugin,
			},
		},
	}

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}

	ckDir, err := ioutil.TempDir("", "checkpoint")
	require.NoError(t, err)
	defer os.RemoveAll(ckDir)

	sfDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer os.RemoveAll(sfDir)

	ctx, cancel := context.WithCancel(context.Background())
	conf := generatePluginConfig(t, ckDir, sfDir)
	meta, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	meta.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
		},
	}

	fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)
	meta.MetricsFetcher = fakeMetricsFetcher
	meta.PodFetcher = &pod.PodFetcherStub{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metaCache, err := metacache.NewMetaCacheImp(conf, nil)
			assert.NoError(t, err)
			assert.NotNil(t, metaCache)

			curPlugin, _ := tt.arg.initFn(conf, nil, metricspool.DummyMetricsEmitterPool{}, meta, metaCache)
			err = curPlugin.Init()
			assert.NotEqual(t, curPlugin.Name(), nil)
			assert.Equal(t, err, nil)
			go curPlugin.Run(ctx)
		})
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
}

func TestMetaServer(t *testing.T) {
	client := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}

	ckDir, err := ioutil.TempDir("", "checkpoint")
	require.NoError(t, err)
	defer os.RemoveAll(ckDir)

	sfDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer os.RemoveAll(sfDir)

	conf := generatePluginConfig(t, ckDir, sfDir)
	meta, err := metaserver.NewMetaServer(client, metrics.DummyMetrics{}, conf)
	if err == nil {
		ctx, cancel := context.WithCancel(context.Background())
		go meta.Run(ctx)
		time.Sleep(100 * time.Millisecond)
		cancel()
	}
}
