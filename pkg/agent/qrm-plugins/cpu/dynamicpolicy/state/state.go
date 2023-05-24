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

package state

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// [notice]
// to compatible with checkpoint checksum calculation,
// we should make guarantees below in checkpoint properties assignment
// 1. resource.Quantity use resource.MustParse("0") to initialize, not to use resource.Quantity{}
// 2. CPUSet use NewCPUset(...) to initialize, not to use CPUSet{}
// 3. not use omitempty in map property and must make new map to do initialization

type AllocationInfo struct {
	PodUid                   string         `json:"pod_uid,omitempty"`
	PodNamespace             string         `json:"pod_namespace,omitempty"`
	PodName                  string         `json:"pod_name,omitempty"`
	ContainerName            string         `json:"container_name,omitempty"`
	ContainerType            string         `json:"container_type,omitempty"`
	ContainerIndex           uint64         `json:"container_index,omitempty"`
	RampUp                   bool           `json:"ramp_up,omitempty"`
	OwnerPoolName            string         `json:"owner_pool_name,omitempty"`
	PodRole                  string         `json:"pod_role,omitempty"`
	PodType                  string         `json:"pod_type,omitempty"`
	AllocationResult         machine.CPUSet `json:"allocation_result,omitempty"`
	OriginalAllocationResult machine.CPUSet `json:"original_allocation_result,omitempty"`
	//key by numa node id, value is assignment for the pod in corresponding NUMA node
	TopologyAwareAssignments map[int]machine.CPUSet `json:"topology_aware_assignments"`
	//key by numa node id, value is assignment for the pod in corresponding NUMA node
	OriginalTopologyAwareAssignments map[int]machine.CPUSet `json:"original_topology_aware_assignments"`
	//for ramp up calculation. notice we don't use time.Time type here to avid checksum corruption.
	InitTimestamp   string            `json:"init_timestamp"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	QoSLevel        string            `json:"qosLevel"`
	RequestQuantity int               `json:"request_quantity,omitempty"`
}

type ContainerEntries map[string]*AllocationInfo // Keyed by containerName.
type PodEntries map[string]ContainerEntries      // Keyed by podUID.

type NUMANodeState struct {
	// equals to allocatable cpuset subtracting original allocation result of dedicated_cores with NUMA binding
	DefaultCPUSet machine.CPUSet `json:"default_cpuset,omitempty"`
	// equals to original allocation result of dedicated_cores with NUMA binding
	AllocatedCPUSet machine.CPUSet `json:"allocated_cpuset,omitempty"`
	PodEntries      PodEntries     `json:"pod_entries"`
}

type NUMANodeMap map[int]*NUMANodeState

func (ai *AllocationInfo) String() string {
	if ai == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ai)
	if err != nil {
		klog.Errorf("[AllocationInfo.String] marshal AllocationInfo failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (ai *AllocationInfo) Clone() *AllocationInfo {
	if ai == nil {
		return nil
	}

	clone := &AllocationInfo{
		PodUid:                           ai.PodUid,
		PodNamespace:                     ai.PodNamespace,
		PodName:                          ai.PodName,
		ContainerName:                    ai.ContainerName,
		ContainerType:                    ai.ContainerType,
		ContainerIndex:                   ai.ContainerIndex,
		RampUp:                           ai.RampUp,
		OwnerPoolName:                    ai.OwnerPoolName,
		PodRole:                          ai.PodRole,
		PodType:                          ai.PodType,
		AllocationResult:                 ai.AllocationResult.Clone(),
		OriginalAllocationResult:         ai.OriginalAllocationResult.Clone(),
		TopologyAwareAssignments:         make(map[int]machine.CPUSet),
		OriginalTopologyAwareAssignments: make(map[int]machine.CPUSet),
		InitTimestamp:                    ai.InitTimestamp,
		QoSLevel:                         ai.QoSLevel,
		Labels:                           general.DeepCopyMap(ai.Labels),
		Annotations:                      general.DeepCopyMap(ai.Annotations),
		RequestQuantity:                  ai.RequestQuantity,
	}

	for node, cpus := range ai.TopologyAwareAssignments {
		clone.TopologyAwareAssignments[node] = cpus.Clone()
	}

	for node, cpus := range ai.OriginalTopologyAwareAssignments {
		clone.OriginalTopologyAwareAssignments[node] = cpus.Clone()
	}

	return clone
}

func (ce ContainerEntries) IsPoolEntry() bool {
	return len(ce) == 1 && ce[""] != nil
}

func (ce ContainerEntries) GetPoolEntry() *AllocationInfo {
	if !ce.IsPoolEntry() {
		return nil
	}
	return ce[""]
}

func (pe PodEntries) String() string {
	if pe == nil {
		return ""
	}

	contentBytes, err := json.Marshal(pe)

	if err != nil {
		klog.Errorf("[PodEntries.String] marshal PodEntries failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (pe PodEntries) GetPoolCPUset(poolName string) (machine.CPUSet, error) {
	if pe == nil {
		return machine.NewCPUSet(), fmt.Errorf("GetPoolCPUset from nil podEntries")
	}

	if !pe[poolName].IsPoolEntry() {
		return machine.NewCPUSet(), fmt.Errorf("pool not found")
	}

	return pe[poolName][""].AllocationResult.Clone(), nil
}

func (pe PodEntries) GetPoolsCPUset(ignorePools sets.String) map[string]machine.CPUSet {
	ret := make(map[string]machine.CPUSet)

	if pe == nil {
		return ret
	}

	for poolName, entries := range pe {
		allocationInfo := entries.GetPoolEntry()

		if allocationInfo != nil && !ignorePools.Has(poolName) {
			ret[poolName] = allocationInfo.AllocationResult.Clone()
		}
	}

	return ret
}

func (pe PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range pe {
		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}

	return clone
}

func (ns *NUMANodeState) GetAvailableCPUSet(reservedCPUs machine.CPUSet) machine.CPUSet {
	if ns == nil {
		return machine.NewCPUSet()
	}

	return ns.DefaultCPUSet.Difference(reservedCPUs)
}

func (ns *NUMANodeState) Clone() *NUMANodeState {
	if ns == nil {
		return nil
	}
	return &NUMANodeState{
		DefaultCPUSet:   ns.DefaultCPUSet.Clone(),
		AllocatedCPUSet: ns.AllocatedCPUSet.Clone(),
		PodEntries:      ns.PodEntries.Clone(),
	}
}

func (ns *NUMANodeState) GetDefaultCPUSetExcludeDedicatedCoresPods() machine.CPUSet {
	if ns == nil {
		return machine.NewCPUSet()
	}

	res := ns.DefaultCPUSet.Clone()
	res = res.Union(ns.AllocatedCPUSet)

	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if allocationInfo != nil && allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores {
				res = res.Difference(allocationInfo.AllocationResult)
			}
		}
	}

	return res
}

func (ns *NUMANodeState) GetDefaultCPUSetExcludeNUMABindingPods() machine.CPUSet {
	if ns == nil {
		return machine.NewCPUSet()
	}

	res := ns.DefaultCPUSet.Clone()
	res = res.Union(ns.AllocatedCPUSet)

	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if allocationInfo != nil &&
				allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				// currently if there is a numa_binding pod in the NUMA node,
				// we don't allocate cpus in this NUMA node to pools, since the memory allocation can't be controlled now.
				// maybe we will use res = res.Difference(allocationInfo.AllocationResult) here later.
				return machine.NewCPUSet()
			}
		}
	}

	return res
}

func (ns *NUMANodeState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if ns == nil {
		return
	}

	if ns.PodEntries == nil {
		ns.PodEntries = make(PodEntries)
	}

	if _, ok := ns.PodEntries[podUID]; !ok {
		ns.PodEntries[podUID] = make(ContainerEntries)
	}

	ns.PodEntries[podUID][containerName] = allocationInfo.Clone()
}

func (nm NUMANodeMap) GetDefaultCPUSet() machine.CPUSet {
	res := machine.NewCPUSet()
	for _, numaNodeState := range nm {
		res = res.Union(numaNodeState.DefaultCPUSet)
	}

	return res
}

func (nm NUMANodeMap) GetDefaultCPUSetExcludeNUMABindingPods() machine.CPUSet {
	res := machine.NewCPUSet()
	for _, numaNodeState := range nm {
		res = res.Union(numaNodeState.GetDefaultCPUSetExcludeNUMABindingPods())
	}

	return res
}

func (nm NUMANodeMap) GetAvailableCPUSetExcludeNUMABindingPods(reservedCPUs machine.CPUSet) machine.CPUSet {
	return nm.GetDefaultCPUSetExcludeNUMABindingPods().Difference(reservedCPUs)
}

func (nm NUMANodeMap) GetDefaultCPUSetExcludeDedicatedCoresPods() machine.CPUSet {
	res := machine.NewCPUSet()
	for _, numaNodeState := range nm {
		res = res.Union(numaNodeState.GetDefaultCPUSetExcludeDedicatedCoresPods())
	}

	return res
}

func (nm NUMANodeMap) GetDefaultCPUSetExcludeDedicatedCoresPodsAndPools(podEntries PodEntries, ignorePools sets.String) machine.CPUSet {
	res := nm.GetDefaultCPUSetExcludeDedicatedCoresPods()

	poolsCPUSet := podEntries.GetPoolsCPUset(ignorePools)
	for _, poolCPUSet := range poolsCPUSet {
		res = res.Difference(poolCPUSet)
	}

	return res
}

func (nm NUMANodeMap) GetAvailableCPUSetExcludeDedicatedCoresPods(reservedCPUs machine.CPUSet) machine.CPUSet {
	return nm.GetDefaultCPUSetExcludeDedicatedCoresPods().Difference(reservedCPUs)
}

func (nm NUMANodeMap) GetAvailableCPUSetExcludeDedicatedCoresPodsAndPools(reservedCPUs machine.CPUSet,
	podEntries PodEntries, ignorePools sets.String) machine.CPUSet {
	return nm.GetDefaultCPUSetExcludeDedicatedCoresPodsAndPools(podEntries, ignorePools).Difference(reservedCPUs)
}

func (nm NUMANodeMap) String() string {
	if nm == nil {
		return ""
	}

	contentBytes, err := json.Marshal(nm)

	if err != nil {
		klog.Errorf("[NUMANodeMap.String] marshal NUMANodeMap failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (nm NUMANodeMap) Clone() NUMANodeMap {
	clone := make(NUMANodeMap)
	for node, ns := range nm {
		clone[node] = ns.Clone()
	}
	return clone
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() NUMANodeMap
	GetAllocationInfo(podUID string, containerName string) *AllocationInfo
	GetPodEntries() PodEntries
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(numaNodeMap NUMANodeMap)
	SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo)
	SetPodEntries(podEntries PodEntries)

	Delete(podUID string, containerName string)
	ClearState()
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	reader
	writer
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader
}
