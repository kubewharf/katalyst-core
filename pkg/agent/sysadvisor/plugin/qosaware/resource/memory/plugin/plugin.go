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

package plugin

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// MemoryAdvisorPlugin generates resource provision result based on configured algorithm
type MemoryAdvisorPlugin interface {
	// Reconcile triggers an episode of plugin update
	Reconcile(status *types.MemoryPressureStatus) error
	// GetAdvices return the advices
	GetAdvices() types.InternalMemoryCalculationResult
}

type InitFunc func(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin

var initializers sync.Map

func RegisterInitializer(name types.MemoryAdvisorPluginName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.MemoryAdvisorPluginName]InitFunc {
	res := make(map[types.MemoryAdvisorPluginName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.MemoryAdvisorPluginName)] = value.(InitFunc)
		return true
	})
	return res
}
