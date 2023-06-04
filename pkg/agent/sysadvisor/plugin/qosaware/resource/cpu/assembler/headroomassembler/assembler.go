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

package headroomassembler

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type HeadroomAssembler interface {
	GetHeadroom() (resource.Quantity, error)
}

type InitFunc func(conf *config.Configuration, regionMap *map[string]region.QoSRegion,
	metaCache metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomAssembler

var initializers sync.Map

func RegisterInitializer(name types.CPUHeadroomAssemblerName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.CPUHeadroomAssemblerName]InitFunc {
	res := make(map[types.CPUHeadroomAssemblerName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.CPUHeadroomAssemblerName)] = value.(InitFunc)
		return true
	})
	return res
}
