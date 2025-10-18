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

package modelinputfetcher

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

// dummyModelInputFetcher is a dummy implementation of ModelInputFetcher for testing.
type dummyModelInputFetcher struct{}

func (d *dummyModelInputFetcher) FetchModelInput(_ context.Context, _ metacache.MetaReader,
	_ metacache.MetaWriter, _ *metaserver.MetaServer,
) error {
	return nil
}

// dummyInitFunc is a dummy implementation of ModelInputFetcherInitFunc for testing.
func dummyInitFunc(_ string, _ *config.Configuration, _ interface{},
	_ metricspool.MetricsEmitterPool, _ *metaserver.MetaServer,
	_ metacache.MetaCache,
) (ModelInputFetcher, error) {
	return &dummyModelInputFetcher{}, nil
}

// dummyInitFunc2 is another dummy implementation of ModelInputFetcherInitFunc for testing.
func dummyInitFunc2(_ string, _ *config.Configuration, _ interface{},
	_ metricspool.MetricsEmitterPool, _ *metaserver.MetaServer,
	_ metacache.MetaCache,
) (ModelInputFetcher, error) {
	return &dummyModelInputFetcher{}, nil
}

var registerTestLock sync.Mutex

func TestRegisterModelInputFetcher(t *testing.T) {
	t.Parallel()

	// Clean up before and after the test.
	clearRegisteredFuncs := func() {
		advisorPluginInitializers.Range(func(key, _ interface{}) bool {
			advisorPluginInitializers.Delete(key)
			return true
		})
	}

	t.Run("test register and get", func(t *testing.T) {
		t.Parallel()
		registerTestLock.Lock()
		defer registerTestLock.Unlock()
		clearRegisteredFuncs()
		defer clearRegisteredFuncs()

		RegisterModelInputFetcherInitFunc("plugin1", dummyInitFunc)
		RegisterModelInputFetcherInitFunc("plugin2", dummyInitFunc2)

		funcs := GetRegisteredModelInputFetcherInitFuncs()
		assert.Equal(t, 2, len(funcs))
		assert.Equal(t, reflect.ValueOf(dummyInitFunc).Pointer(), reflect.ValueOf(funcs["plugin1"]).Pointer())
		assert.Equal(t, reflect.ValueOf(dummyInitFunc2).Pointer(), reflect.ValueOf(funcs["plugin2"]).Pointer())
	})

	t.Run("test override", func(t *testing.T) {
		t.Parallel()
		registerTestLock.Lock()
		defer registerTestLock.Unlock()
		clearRegisteredFuncs()
		defer clearRegisteredFuncs()

		RegisterModelInputFetcherInitFunc("plugin1", dummyInitFunc)
		RegisterModelInputFetcherInitFunc("plugin1", dummyInitFunc2) // override

		funcs := GetRegisteredModelInputFetcherInitFuncs()
		assert.Equal(t, 1, len(funcs))
		assert.Equal(t, reflect.ValueOf(dummyInitFunc2).Pointer(), reflect.ValueOf(funcs["plugin1"]).Pointer())
	})
}
