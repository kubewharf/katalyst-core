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

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type AllocationInfo struct {
	PodUid               string         `json:"pod_uid,omitempty"`
	PodNamespace         string         `json:"pod_namespace,omitempty"`
	PodName              string         `json:"pod_name,omitempty"`
	ContainerName        string         `json:"container_name,omitempty"`
	ContainerType        string         `json:"container_type,omitempty"`
	ContainerIndex       uint64         `json:"container_index,omitempty"`
	RampUp               bool           `json:"ramp_up,omitempty"`
	PodRole              string         `json:"pod_role,omitempty"`
	PodType              string         `json:"pod_type,omitempty"`
	AggregatedQuantity   uint64         `json:"aggregated_quantity"`
	NumaAllocationResult machine.CPUSet `json:"numa_allocation_result,omitempty"`
	// key by numa node id, value is assignment for the pod in corresponding NUMA node
	TopologyAwareAllocations map[int]uint64    `json:"topology_aware_allocations"`
	Labels                   map[string]string `json:"labels"`
	Annotations              map[string]string `json:"annotations"`
	QoSLevel                 string            `json:"qosLevel"`
}

type ContainerEntries map[string]*AllocationInfo       // Keyed by container name
type PodEntries map[string]ContainerEntries            // Keyed by pod UID
type PodResourceEntries map[v1.ResourceName]PodEntries // Keyed by resource name

// NUMANodeState records the amount of memory per numa node (in bytes)
type NUMANodeState struct {
	TotalMemSize   uint64     `json:"total"`
	SystemReserved uint64     `json:"systemReserved"`
	Allocatable    uint64     `json:"allocatable"`
	Allocated      uint64     `json:"Allocated"`
	Free           uint64     `json:"free"`
	PodEntries     PodEntries `json:"pod_entries"`
}

type NUMANodeMap map[int]*NUMANodeState                   // Keyed by numa node id
type NUMANodeResourcesMap map[v1.ResourceName]NUMANodeMap // Keyed by resource name

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
		PodUid:                   ai.PodUid,
		PodNamespace:             ai.PodNamespace,
		PodName:                  ai.PodName,
		ContainerName:            ai.ContainerName,
		ContainerType:            ai.ContainerType,
		ContainerIndex:           ai.ContainerIndex,
		RampUp:                   ai.RampUp,
		PodRole:                  ai.PodRole,
		PodType:                  ai.PodType,
		AggregatedQuantity:       ai.AggregatedQuantity,
		NumaAllocationResult:     ai.NumaAllocationResult.Clone(),
		TopologyAwareAllocations: make(map[int]uint64),
		QoSLevel:                 ai.QoSLevel,
		Labels:                   general.DeepCopyMap(ai.Labels),
		Annotations:              general.DeepCopyMap(ai.Annotations),
	}

	for node, quantity := range ai.TopologyAwareAllocations {
		clone.TopologyAwareAllocations[node] = quantity
	}

	return clone
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

func (pre PodResourceEntries) String() string {
	if pre == nil {
		return ""
	}

	contentBytes, err := json.Marshal(pre)

	if err != nil {
		klog.Errorf("[PodResourceEntries.String] marshal PodResourceEntries failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (pre PodResourceEntries) Clone() PodResourceEntries {
	clone := make(PodResourceEntries)
	for resourceName, podEntries := range pre {
		clone[resourceName] = podEntries.Clone()
	}

	return clone
}

func (ns *NUMANodeState) String() string {
	if ns == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ns)

	if err != nil {
		klog.Errorf("[NUMANodeState.String] marshal NUMANodeState failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (ns *NUMANodeState) Clone() *NUMANodeState {
	if ns == nil {
		return nil
	}

	return &NUMANodeState{
		TotalMemSize:   ns.TotalMemSize,
		SystemReserved: ns.SystemReserved,
		Allocatable:    ns.Allocatable,
		Allocated:      ns.Allocated,
		Free:           ns.Free,
		PodEntries:     ns.PodEntries.Clone(),
	}
}

func (ns *NUMANodeState) HasNUMABindingPods() bool {
	if ns == nil {
		return false
	}

	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if allocationInfo != nil &&
				allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				return true
			}
		}
	}

	return false
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

func (nm NUMANodeMap) Clone() NUMANodeMap {
	clone := make(NUMANodeMap)
	for node, ns := range nm {
		clone[node] = ns.Clone()
	}
	return clone
}

func (nm NUMANodeMap) GetNUMANodesWithoutNUMABindingPods() machine.CPUSet {
	res := machine.NewCPUSet()
	for numaId, numaNodeState := range nm {
		if numaNodeState != nil && !numaNodeState.HasNUMABindingPods() {
			res = res.Union(machine.NewCPUSet(numaId))
		}
	}

	return res
}

func (nrm NUMANodeResourcesMap) String() string {
	if nrm == nil {
		return ""
	}

	contentBytes, err := json.Marshal(nrm)

	if err != nil {
		klog.Errorf("[NUMANodeResourcesMap.String] marshal NUMANodeResourcesMap failed with error: %v", err)
		return ""
	}

	return string(contentBytes)
}

func (nrm NUMANodeResourcesMap) Clone() NUMANodeResourcesMap {
	clone := make(NUMANodeResourcesMap)
	for resourceName, nm := range nrm {
		clone[resourceName] = nm.Clone()
	}
	return clone
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() NUMANodeResourcesMap
	GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo
	GetPodResourceEntries() PodResourceEntries
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(numaNodeResourcesMap NUMANodeResourcesMap)
	SetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo)
	SetPodResourceEntries(podResourceEntries PodResourceEntries)

	Delete(resourceName v1.ResourceName, podUID, containerName string)
	ClearState()
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	writer
	ReadonlyState
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader

	GetMachineInfo() *info.MachineInfo
	GetReservedMemory() map[v1.ResourceName]map[int]uint64
}
