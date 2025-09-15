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
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type ModelInputFetcher interface {
	FetchModelInput(ctx context.Context, metaReader metacache.MetaReader,
		metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer) error
}

var _ ModelInputFetcher = DummyModelInputFetcher{}

type DummyModelInputFetcher struct{}

func (d DummyModelInputFetcher) FetchModelInput(ctx context.Context, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) error {
	return nil
}

func NewDummyModelInputFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (ModelInputFetcher, error) {
	return DummyModelInputFetcher{}, nil
}

type ModelInputFetcherInitFunc func(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache) (ModelInputFetcher, error)

var advisorPluginInitializers sync.Map

func RegisterModelInputFetcherInitFunc(pluginName string, f ModelInputFetcherInitFunc) {
	advisorPluginInitializers.Store(pluginName, f)
}

func GetRegisteredModelInputFetcherInitFuncs() map[string]ModelInputFetcherInitFunc {
	plugins := make(map[string]ModelInputFetcherInitFunc)
	advisorPluginInitializers.Range(func(key, value interface{}) bool {
		plugins[key.(string)] = value.(ModelInputFetcherInitFunc)
		return true
	})
	return plugins
}
