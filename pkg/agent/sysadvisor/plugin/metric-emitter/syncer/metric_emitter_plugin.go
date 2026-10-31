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

package syncer

import (
	"context"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

// CustomMetricSyncer is used as a common implementation for custom-metrics emitter.
type CustomMetricSyncer interface {
	Name() string

	// Run starts the syncer's background loops.
	//
	// Implementation contract:
	//   - It MUST validate every dependency it relies on before spawning any
	//     goroutine, and return a non-nil error when a dependency is missing or
	//     uninitialized. Returning such an error MUST NOT leave any goroutine
	//     behind.
	//   - On success it spawns its background goroutines and then blocks until
	//     ctx is canceled. Once ctx is canceled it MUST wait for its own
	//     background goroutines to exit before returning, so that callers can
	//     wait on Run's return value without leaking goroutines.
	//   - Errors are observable by the caller (the emitter logs them) and MUST
	//     not be swallowed or recovered inside the syncer.
	Run(ctx context.Context) error
}

type SyncerInitFunc func(conf *config.Configuration, extraConf interface{},
	metricEmitter metrics.MetricEmitter, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader) (CustomMetricSyncer, error)

var metricSyncInitializers sync.Map

func RegisterMetricSyncInitializers(plugin string, f SyncerInitFunc) {
	metricSyncInitializers.Store(plugin, f)
}

func GetRegisteredMetricSyncers() map[string]SyncerInitFunc {
	metricSyncers := make(map[string]SyncerInitFunc)
	metricSyncInitializers.Range(func(key, value interface{}) bool {
		metricSyncers[key.(string)] = value.(SyncerInitFunc)
		return true
	})
	return metricSyncers
}
