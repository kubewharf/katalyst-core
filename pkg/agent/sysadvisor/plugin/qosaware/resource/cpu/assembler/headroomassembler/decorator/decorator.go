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

package decorator

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var (
	initializers     sync.Map
	enabledDecorator string
	lock             sync.RWMutex
)

type InitFunc func(inner headroomassembler.HeadroomAssembler,
	conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) headroomassembler.HeadroomAssembler

func RegisterInitializer(name string, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[string]InitFunc {
	res := make(map[string]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(string)] = value.(InitFunc)
		return true
	})
	return res
}

func EnablePlugin(name string) {
	lock.Lock()
	defer lock.Unlock()
	enabledDecorator = name
}

func GetEnabledPlugin() string {
	lock.RLock()
	defer lock.RUnlock()

	return enabledDecorator
}
