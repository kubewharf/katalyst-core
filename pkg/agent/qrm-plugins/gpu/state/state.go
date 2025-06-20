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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	AllocatedAllocation      GPUAllocation            `json:"allocated_allocation"`
	TopologyAwareAllocations map[string]GPUAllocation `json:"topology_aware_allocations"`
}

type GPUAllocation struct {
	GPUMemoryQuantity uint64 `json:"gpu_memory_quantity"`
}

func (a *GPUAllocation) Clone() GPUAllocation {
	return GPUAllocation{
		GPUMemoryQuantity: a.GPUMemoryQuantity,
	}
}

type (
	ContainerEntries map[string]*AllocationInfo  // Keyed by container name
	PodEntries       map[string]ContainerEntries // Keyed by pod UID
)
type GPUState struct {
	GPUMemoryAllocatable uint64     `json:"gpu_memory_allocatable"`
	GPUMemoryAllocated   uint64     `json:"gpu_memory_allocated"`
	PodEntries           PodEntries `json:"pod_entries"`
}

type GPUMap map[string]*GPUState // GPUMap keyed by gpu device name i.e. GPU-fef8089b-4820-abfc-e83e-94318197576e

func (i *AllocationInfo) String() string {
	if i == nil {
		return ""
	}

	contentBytes, err := json.Marshal(i)
	if err != nil {
		general.LoggerWithPrefix("AllocationInfo.String", general.LoggingPKGFull).Errorf("marshal AllocationInfo failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (i *AllocationInfo) Clone() *AllocationInfo {
	if i == nil {
		return nil
	}

	clone := &AllocationInfo{
		AllocationMeta:      *i.AllocationMeta.Clone(),
		AllocatedAllocation: i.AllocatedAllocation.Clone(),
	}

	if i.TopologyAwareAllocations != nil {
		clone.TopologyAwareAllocations = make(map[string]GPUAllocation)
		for k, v := range i.TopologyAwareAllocations {
			clone.TopologyAwareAllocations[k] = v.Clone()
		}
	}

	return clone
}

func (e PodEntries) String() string {
	if e == nil {
		return ""
	}

	contentBytes, err := json.Marshal(e)
	if err != nil {
		general.LoggerWithPrefix("PodEntries.String", general.LoggingPKGFull).Errorf("marshal PodEntries failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (e PodEntries) Clone() PodEntries {
	clone := make(PodEntries)
	for podUID, containerEntries := range e {
		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}
	return clone
}

func (e PodEntries) GetAllocationInfo(uid string, name string) *AllocationInfo {
	if e == nil {
		return nil
	}

	if containerEntries, ok := e[uid]; ok {
		if allocationInfo, ok := containerEntries[name]; ok {
			return allocationInfo.Clone()
		}
	}
	return nil
}

func (e PodEntries) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if e == nil {
		return
	}

	if _, ok := e[podUID]; !ok {
		e[podUID] = make(ContainerEntries)
	}

	e[podUID][containerName] = allocationInfo.Clone()
}

func (ns *GPUState) String() string {
	if ns == nil {
		return ""
	}

	contentBytes, err := json.Marshal(ns)
	if err != nil {
		general.LoggerWithPrefix("GPUState.String", general.LoggingPKGFull).Errorf("marshal GPUState failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (ns *GPUState) Clone() *GPUState {
	if ns == nil {
		return nil
	}

	return &GPUState{
		GPUMemoryAllocatable: ns.GPUMemoryAllocatable,
		PodEntries:           ns.PodEntries.Clone(),
	}
}

func (ns *GPUState) GetGPUMemoryAllocatable() uint64 {
	if ns == nil {
		return 0
	}

	return ns.GPUMemoryAllocatable
}

// SetAllocationInfo adds a new AllocationInfo (for pod/container pairs) into the given NICState
func (ns *GPUState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if ns == nil {
		return
	}

	if allocationInfo == nil {
		general.LoggerWithPrefix("GPUState.SetAllocationInfo", general.LoggingPKGFull).Errorf("passed allocationInfo is nil")
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

func (m GPUMap) Clone() GPUMap {
	clone := make(GPUMap)
	for id, ns := range m {
		clone[id] = ns.Clone()
	}
	return clone
}

func (m GPUMap) String() string {
	if m == nil {
		return ""
	}

	contentBytes, err := json.Marshal(m)
	if err != nil {
		general.LoggerWithPrefix("GPUMap.String", general.LoggingPKGFull).Errorf("marshal GPUMap failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (m GPUMap) GetGPUMemoryAllocatable(id string) uint64 {
	if m == nil {
		return 0
	}
	return m[id].GetGPUMemoryAllocatable()
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() GPUMap
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(gpuMap GPUMap, persist bool)
	SetPodEntries(podEntries PodEntries, persist bool)
	SetAllocationInfo(podUID, containerName string, allocationInfo *AllocationInfo, persist bool)

	Delete(podUID, containerName string, persist bool)
	ClearState()
	StoreState() error
}

// ReadonlyState interface only provides methods for tracking pod assignments
type ReadonlyState interface {
	reader
}

// State interface provides methods for tracking and setting pod assignments
type State interface {
	writer
	ReadonlyState
}
