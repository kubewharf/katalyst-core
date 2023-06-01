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
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type HeadroomAssemblerCommon struct {
	enableReclaim bool

	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewHeadroomAssemblerCommon(conf *config.Configuration, _ *map[string]region.QoSRegion,
	metaCache metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomAssembler {
	return &HeadroomAssemblerCommon{
		enableReclaim: conf.ReclaimedResourceConfiguration.EnableReclaim(),

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (ha *HeadroomAssemblerCommon) GetHeadroom() (resource.Quantity, error) {
	// return zero when reclaim is disabled
	if !ha.enableReclaim {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	// return total reclaim pool size as headroom
	v, ok := ha.metaCache.GetPoolSize(state.PoolNameReclaim)
	if !ok {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}
	return *resource.NewQuantity(int64(v), resource.DecimalSI), nil
}
