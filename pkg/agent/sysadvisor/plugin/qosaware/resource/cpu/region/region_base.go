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
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QoSRegionBase struct {
	name         string
	containerSet map[string]sets.String // map[podUID][containerName]

	ownerPoolName string
	regionType    QoSRegionType
	regionPolicy  types.CPUProvisionPolicyName

	cpuLimit            int
	reservePoolSize     int
	reservedForAllocate int

	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter
}

func NewQoSRegionBase(name string, ownerPoolName string, regionType QoSRegionType, regionPolicy types.CPUProvisionPolicyName, metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) *QoSRegionBase {
	r := &QoSRegionBase{
		name:          name,
		containerSet:  make(map[string]sets.String),
		ownerPoolName: ownerPoolName,
		regionType:    regionType,
		regionPolicy:  regionPolicy,
		metaCache:     metaCache,
		emitter:       emitter,
	}
	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

func (r *QoSRegionBase) OwnerPoolName() string {
	return r.ownerPoolName
}

func (r *QoSRegionBase) Type() QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) IsEmpty() bool {
	return len(r.containerSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.containerSet = make(map[string]sets.String)
}

func (r *QoSRegionBase) AddContainer(podUID string, containerName string) {
	containers, ok := r.containerSet[podUID]
	if !ok {
		r.containerSet[podUID] = sets.NewString()
		containers = r.containerSet[podUID]
	}
	containers.Insert(containerName)
}

func (r *QoSRegionBase) SetCPULimit(value int) {
	r.cpuLimit = value
}

func (r *QoSRegionBase) SetReservePoolSize(value int) {
	r.reservePoolSize = value
}

func (r *QoSRegionBase) SetReservedForAllocate(value int) {
	r.reservedForAllocate = value
}
