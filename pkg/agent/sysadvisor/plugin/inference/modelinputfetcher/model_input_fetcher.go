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
