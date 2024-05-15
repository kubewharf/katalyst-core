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

// Package plugin is the package that defines the SysAdvisor plugin, and
// those strategies must implement ModelResultFetcher interface.

package modelresultfetcher

import (
	"context"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type ModelResultFetcher interface {
	FetchModelResult(ctx context.Context, metaReader metacache.MetaReader,
		metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer) error
}

var _ ModelResultFetcher = DummyModelResultFetcher{}

type DummyModelResultFetcher struct{}

func (d DummyModelResultFetcher) FetchModelResult(ctx context.Context, metaReader metacache.MetaReader,
	metaWriter metacache.MetaWriter, metaServer *metaserver.MetaServer,
) error {
	return nil
}

func NewDummyModelResultFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (ModelResultFetcher, error) {
	return DummyModelResultFetcher{}, nil
}

// ModelResultFetcherInitFunc is used to initialize a particular init ModelResultFetcher
type ModelResultFetcherInitFunc func(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache) (ModelResultFetcher, error)

var advisorPluginInitializers sync.Map

func RegisterModelResultFetcherInitFunc(pluginName string, f ModelResultFetcherInitFunc) {
	advisorPluginInitializers.Store(pluginName, f)
}

func GetRegisteredModelResultFetcherInitFuncs() map[string]ModelResultFetcherInitFunc {
	plugins := make(map[string]ModelResultFetcherInitFunc)
	advisorPluginInitializers.Range(func(key, value interface{}) bool {
		plugins[key.(string)] = value.(ModelResultFetcherInitFunc)
		return true
	})
	return plugins
}
