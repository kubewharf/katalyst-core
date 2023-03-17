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
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type QoSRegionBase struct {
	name                     string
	containerSet             map[string]sets.String // map[podUID][containerName]
	topologyAwareAssignments map[int]machine.CPUSet // map[numaID]CPUSet, keys indicate the max scope of numaIDs and should not be modified
	mutex                    sync.RWMutex

	ownerPoolName string
	regionType    types.QoSRegionType
	// cpu resources are preferred to assigned to higher priority regions
	priority        types.QosRegionPriority
	provisionPolicy types.CPUProvisionPolicyName
	headroomPolicy  headroompolicy.HeadroomPolicy

	cpuLimit            int
	reservePoolSize     int
	reservedForAllocate int

	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter
}

func NewQoSRegionBase(name string, ownerPoolName string, regionType types.QoSRegionType, priority types.QosRegionPriority,
	provisionPolicy types.CPUProvisionPolicyName, headroomPolicy headroompolicy.HeadroomPolicy, numaIDs sets.Int,
	metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) *QoSRegionBase {
	r := &QoSRegionBase{
		name:                     name,
		containerSet:             make(map[string]sets.String),
		topologyAwareAssignments: make(map[int]machine.CPUSet),
		ownerPoolName:            ownerPoolName,
		regionType:               regionType,
		priority:                 priority,
		provisionPolicy:          provisionPolicy,
		headroomPolicy:           headroomPolicy,
		metaCache:                metaCache,
		emitter:                  emitter,
	}
	for numaID := range numaIDs {
		r.topologyAwareAssignments[numaID] = machine.NewCPUSet()
	}
	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

func (r *QoSRegionBase) OwnerPoolName() string {
	return r.ownerPoolName
}

func (r *QoSRegionBase) Type() types.QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) IsEmpty() bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return len(r.containerSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.containerSet = make(map[string]sets.String)
}

func (r *QoSRegionBase) AddContainer(ci *types.ContainerInfo) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if ci == nil {
		return fmt.Errorf("ci is emprt")
	}
	containers, ok := r.containerSet[ci.PodUID]
	if !ok {
		r.containerSet[ci.PodUID] = sets.NewString()
		containers = r.containerSet[ci.PodUID]
	}
	containers.Insert(ci.ContainerName)

	for numaID, containerCPUSet := range ci.TopologyAwareAssignments {
		regionCPUSet, ok := r.topologyAwareAssignments[numaID]
		if !ok {
			continue
		}
		regionCPUSet = regionCPUSet.Union(containerCPUSet)
		r.topologyAwareAssignments[numaID] = regionCPUSet
	}
	return nil
}

func (r *QoSRegionBase) GetNumaIDs() sets.Int {
	numas := sets.NewInt()
	for numaID, cpus := range r.topologyAwareAssignments {
		if cpus.Size() > 0 {
			numas.Insert(numaID)
		}
	}
	return numas
}

func (r *QoSRegionBase) GetContainerSet() map[string]sets.String {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	containerSet := make(map[string]sets.String)
	for podUID, v := range r.containerSet {
		containerSet[podUID] = sets.NewString()
		for containerName := range v {
			containerSet[podUID].Insert(containerName)
		}
	}
	return containerSet
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

func (r *QoSRegionBase) Priority() types.QosRegionPriority {
	return r.priority
}
