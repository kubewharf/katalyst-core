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

package region

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type QoSRegionBase struct {
	name          string
	ownerPoolName string
	regionType    types.QoSRegionType

	// bindingNumas records numas binding with region. Should not be changed after initialization
	bindingNumas machine.CPUSet

	// containerTopologyAwareAssignment changes dynamically by adding container
	containerTopologyAwareAssignment types.TopologyAwareAssignment

	// podSet records current pod and containers in region keyed by pod uid and container name
	podSet types.PodSet

	total               int
	reservePoolSize     int
	reservedForAllocate int

	metaCache  *metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewQoSRegionShare returns a base qos region instance with common region methods
func NewQoSRegionBase(name string, ownerPoolName string, regionType types.QoSRegionType, bindingNumas machine.CPUSet,
	metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *QoSRegionBase {
	r := &QoSRegionBase{
		name:          name,
		ownerPoolName: ownerPoolName,
		regionType:    regionType,

		bindingNumas:                     bindingNumas,
		containerTopologyAwareAssignment: make(types.TopologyAwareAssignment),
		podSet:                           make(types.PodSet),

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}
	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

func (r *QoSRegionBase) Type() types.QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) IsEmpty() bool {
	return len(r.podSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.containerTopologyAwareAssignment = make(types.TopologyAwareAssignment)
	r.podSet = make(types.PodSet)
}

func (r *QoSRegionBase) GetNumas() machine.CPUSet {
	return r.bindingNumas
}

func (r *QoSRegionBase) GetPods() types.PodSet {
	return r.podSet
}

func (r *QoSRegionBase) SetEssentials(total, reservePoolSize, reservedForAllocate int) {
	r.total = total
	r.reservePoolSize = reservePoolSize
	r.reservedForAllocate = reservedForAllocate
}
