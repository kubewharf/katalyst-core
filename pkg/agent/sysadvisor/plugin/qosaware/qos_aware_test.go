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

package qosaware

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir

	return conf
}

func generateTestMetaServer(t *testing.T, conf *config.Configuration) *metaserver.MetaServer {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)
	require.NotNil(t, metaServer)

	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	metaServer.MetaAgent = &agent.MetaAgent{
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
			MachineInfo: &info.MachineInfo{},
		},
		MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
	}

	return metaServer
}

func generateTestMetaCache(t *testing.T, conf *config.Configuration, metricsReader types.MetricsReader) *metacache.MetaCacheImp {
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsReader)
	require.NoError(t, err)
	require.NotNil(t, metaCache)
	return metaCache
}

func TestQoSAwarePlugin(t *testing.T) {
	t.Parallel()

	checkpoinDir, err := ioutil.TempDir("", "checkpoint-TestQoSAwarePlugin")
	require.NoError(t, err)
	defer os.RemoveAll(checkpoinDir)

	statefileDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer os.RemoveAll(statefileDir)

	conf := generateTestConfiguration(t, checkpoinDir, statefileDir)
	metaServer := generateTestMetaServer(t, conf)
	metaCache := generateTestMetaCache(t, conf, metaServer.MetricsFetcher)

	plugin, err := NewQoSAwarePlugin("test-qos-aware-plugin", conf, nil, metricspool.DummyMetricsEmitterPool{}, metaServer, metaCache)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	err = plugin.Init()
	require.NoError(t, err)

	name := plugin.Name()
	assert.Equal(t, name, "test-qos-aware-plugin")

	ctx, cancel := context.WithCancel(context.Background())
	go plugin.Run(ctx)

	time.Sleep(10 * time.Millisecond)
	cancel()
}
