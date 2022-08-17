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

package types

import (
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func (ci *ContainerInfo) Clone() *ContainerInfo {
	if ci == nil {
		return nil
	}

	clone := &ContainerInfo{
		PodUID:         ci.PodUID,
		PodNamespace:   ci.PodNamespace,
		PodName:        ci.PodName,
		ContainerName:  ci.ContainerName,
		ContainerType:  ci.ContainerType,
		ContainerIndex: ci.ContainerIndex,
		Labels:         general.DeepCopyMap(ci.Labels),
		Annotations:    general.DeepCopyMap(ci.Annotations),
		QoSLevel:       ci.QoSLevel,
		CPURequest:     ci.CPURequest,
		MemoryRequest:  ci.MemoryRequest,
		RampUp:         ci.RampUp,
		OwnerPoolName:  ci.OwnerPoolName,
	}

	if ci.TopologyAwareAssignments != nil {
		clone.TopologyAwareAssignments = make(map[int]machine.CPUSet)
		for node, cpus := range ci.TopologyAwareAssignments {
			clone.TopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	if ci.OriginalTopologyAwareAssignments != nil {
		clone.OriginalTopologyAwareAssignments = make(map[int]machine.CPUSet)
		for node, cpus := range ci.OriginalTopologyAwareAssignments {
			clone.OriginalTopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	return clone
}

func (pi *PoolInfo) Clone() *PoolInfo {
	if pi == nil {
		return nil
	}

	clone := &PoolInfo{
		PoolName: pi.PoolName,
	}

	if pi.TopologyAwareAssignments != nil {
		clone.TopologyAwareAssignments = make(map[int]machine.CPUSet)
		for node, cpus := range pi.TopologyAwareAssignments {
			clone.TopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	if pi.OriginalTopologyAwareAssignments != nil {
		clone.OriginalTopologyAwareAssignments = make(map[int]machine.CPUSet)
		for node, cpus := range pi.OriginalTopologyAwareAssignments {
			clone.OriginalTopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	return clone
}

func (ce ContainerEntries) Clone() ContainerEntries {
	clone := make(ContainerEntries)
	for containerName, containerInfo := range ce {
		clone[containerName] = containerInfo.Clone()
	}
	return clone
}

func (pe PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range pe {
		clone[podUID] = containerEntries.Clone()
	}
	return clone
}

func (pe PoolEntries) Clone() PoolEntries {
	clone := make(PoolEntries)
	for poolName, poolInfo := range pe {
		clone[poolName] = poolInfo.Clone()
	}
	return clone
}

// UpdateMeta updates mutable container meta from another container info
func (ci *ContainerInfo) UpdateMeta(c *ContainerInfo) {
	if c.CPURequest > 0 {
		ci.CPURequest = c.CPURequest
	}
	if c.MemoryRequest > 0 {
		ci.MemoryRequest = c.MemoryRequest
	}
}
