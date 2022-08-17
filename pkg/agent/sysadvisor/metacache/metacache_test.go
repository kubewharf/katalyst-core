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

package metacache

import (
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir

	return testConfiguration
}

func newTestMetaCache(t *testing.T) *MetaCache {
	metaCache, err := NewMetaCache(generateTestConfiguration(t), nil)
	require.NoError(t, err)
	require.NotNil(t, metaCache)
	return metaCache
}

func TestContainer(t *testing.T) {
	metaCache := newTestMetaCache(t)

	err := metaCache.SetContainerInfo("pod-0", "container-0", &types.ContainerInfo{})
	assert.Nil(t, err)
	err = metaCache.SetContainerInfo("pod-1", "container-1", &types.ContainerInfo{})
	assert.Nil(t, err)

	_, ok := metaCache.GetContainerInfo("pod-0", "container-0")
	assert.True(t, ok)
	_, ok = metaCache.GetContainerInfo("pod-1", "container-1")
	assert.True(t, ok)

	err = metaCache.RemovePod("pod-0")
	assert.Nil(t, err)
	err = metaCache.DeleteContainer("pod-1", "container-1")
	assert.Nil(t, err)

	_, ok = metaCache.GetContainerInfo("pod-0", "container-0")
	assert.False(t, ok)
	_, ok = metaCache.GetContainerInfo("pod-1", "container-1")
	assert.False(t, ok)
}

func TestPool(t *testing.T) {
	metaCache := newTestMetaCache(t)

	err := metaCache.SetPoolInfo("pool-0", &types.PoolInfo{})
	assert.Nil(t, err)
	err = metaCache.SetPoolInfo("pool-1", &types.PoolInfo{})
	assert.Nil(t, err)
	err = metaCache.SetPoolInfo("pool-2", &types.PoolInfo{})
	assert.Nil(t, err)

	_, ok := metaCache.GetPoolInfo("pool-0")
	assert.True(t, ok)
	_, ok = metaCache.GetPoolInfo("pool-1")
	assert.True(t, ok)
	_, ok = metaCache.GetPoolInfo("pool-2")
	assert.True(t, ok)

	err = metaCache.DeletePool("pool-0")
	assert.Nil(t, err)
	err = metaCache.GCPoolEntries(map[string]struct{}{"pool-2": {}})
	assert.Nil(t, err)

	_, ok = metaCache.GetPoolInfo("pool-0")
	assert.False(t, ok)
	_, ok = metaCache.GetPoolInfo("pool-1")
	assert.False(t, ok)
	_, ok = metaCache.GetPoolInfo("pool-2")
	assert.True(t, ok)
}
