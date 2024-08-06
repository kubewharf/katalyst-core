package logcache

import (
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type Manager interface {
	EvictLogCache(_ *coreconfig.Configuration, _ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
		emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer)
}
