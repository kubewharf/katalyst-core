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

package logcache

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.EnableMetricsFetcher = false
	conf.HighThreshold = 30
	conf.LowThreshold = 10
	conf.MaxInterval = time.Second * 120
	conf.MinInterval = time.Second
	conf.PathList = []string{"."}
	conf.FileFilters = []string{".*.log.*"}
	return conf
}

func TestFileCacheEvictionManager_EvictLogCache(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	sfDir, err := ioutil.TempDir("", "statefile")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(sfDir) }()

	conf := generateTestConfiguration(t, ckDir, sfDir)

	genericCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)

	mgr := NewManager(conf, metaServer)

	stopCh := make(chan struct{})
	go func() {
		time.Sleep(time.Minute * 4)
		stopCh <- struct{}{}
	}()

	go wait.Until(func() {
		var extraConf interface{}
		mgr.EvictLogCache(conf, extraConf, conf.DynamicAgentConfiguration, metrics.DummyMetrics{}, metaServer)
	}, time.Second, stopCh)
	<-stopCh
}
