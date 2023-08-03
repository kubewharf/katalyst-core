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

package provisionassembler

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ProvisionAssembler assembles internal node provision result.
// Advisor data elements are shared ONLY by assemblers as pointer to avoid rebuild in advisor,
// and NOT supposed to be used by other components.
type ProvisionAssembler interface {
	AssembleProvision() (types.InternalCPUCalculationResult, bool, error)
}

type InitFunc func(conf *config.Configuration, extraConf interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet,
	reader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionAssembler

var initializers sync.Map

func RegisterInitializer(name types.CPUProvisionAssemblerName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.CPUProvisionAssemblerName]InitFunc {
	res := make(map[types.CPUProvisionAssemblerName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.CPUProvisionAssemblerName)] = value.(InitFunc)
		return true
	})
	return res
}
