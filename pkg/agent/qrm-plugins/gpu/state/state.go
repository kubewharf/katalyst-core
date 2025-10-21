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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	AllocatedAllocation      Allocation            `json:"allocated_allocation"`
	TopologyAwareAllocations map[string]Allocation `json:"topology_aware_allocations"`
}

type Allocation struct {
	// Quantity refers to the amount of device allocated
	Quantity  float64 `json:"quantity"`
	NUMANodes []int   `json:"numa_nodes"`
}

func (a *Allocation) Clone() Allocation {
	if a == nil {
		return Allocation{}
	}

	numaNodes := make([]int, len(a.NUMANodes))
	copy(numaNodes, a.NUMANodes)
	return Allocation{
		Quantity:  a.Quantity,
		NUMANodes: numaNodes,
	}
}

type (
	ContainerEntries   map[string]*AllocationInfo     // Keyed by container name
	PodEntries         map[string]ContainerEntries    // Keyed by pod UID
	PodResourceEntries map[v1.ResourceName]PodEntries // Keyed by resource name
)

type AllocationState struct {
	PodEntries PodEntries `json:"pod_entries"`
}

type AllocationMap map[string]*AllocationState // AllocationMap keyed by device name i.e. GPU-fef8089b-4820-abfc-e83e-94318197576e

type AllocationResourcesMap map[v1.ResourceName]AllocationMap // AllocationResourcesMap keyed by resource name i.e. v1.ResourceName("nvidia.com/gpu")

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
		clone.TopologyAwareAllocations = make(map[string]Allocation)
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

func (pre PodResourceEntries) String() string {
	if pre == nil {
		return ""
	}

	contentBytes, err := json.Marshal(pre)
	if err != nil {
		general.LoggerWithPrefix("PodResourceEntries.String", general.LoggingPKGFull).Errorf("[PodResourceEntries.String] marshal PodResourceEntries failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (pre PodResourceEntries) Clone() PodResourceEntries {
	if pre == nil {
		return nil
	}

	clone := make(PodResourceEntries)
	for resourceName, podEntries := range pre {
		clone[resourceName] = podEntries.Clone()
	}
	return clone
}

func (pre PodResourceEntries) RemovePod(podUID string) {
	if pre == nil {
		return
	}

	for _, podEntries := range pre {
		delete(podEntries, podUID)
	}
}

// GetTotalAllocatedResourceOfContainer returns the total allocated resource quantity of a container together with
// the specific resource IDs that are allocated.
func (pre PodResourceEntries) GetTotalAllocatedResourceOfContainer(
	resourceName v1.ResourceName, podUID, containerName string,
) (int, sets.String) {
	if podEntries, ok := pre[resourceName]; ok {
		if allocationInfo := podEntries.GetAllocationInfo(podUID, containerName); allocationInfo != nil {
			totalAllocationQuantity := int(allocationInfo.AllocatedAllocation.Quantity)
			allocationIDs := sets.NewString()
			for id := range allocationInfo.TopologyAwareAllocations {
				allocationIDs.Insert(id)
			}
			return totalAllocationQuantity, allocationIDs
		}
	}
	return 0, nil
}

func (as *AllocationState) String() string {
	if as == nil {
		return ""
	}

	contentBytes, err := json.Marshal(as)
	if err != nil {
		general.LoggerWithPrefix("AllocationState.String", general.LoggingPKGFull).Errorf("[AllocationState.String]marshal AllocationState failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (as *AllocationState) Clone() *AllocationState {
	if as == nil {
		return nil
	}

	return &AllocationState{
		PodEntries: as.PodEntries.Clone(),
	}
}

func (as *AllocationState) SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo) {
	if as == nil {
		return
	}

	if as.PodEntries == nil {
		as.PodEntries = make(PodEntries)
	}

	if _, ok := as.PodEntries[podUID]; !ok {
		as.PodEntries[podUID] = make(ContainerEntries)
	}

	as.PodEntries[podUID][containerName] = allocationInfo.Clone()
}

func (am AllocationMap) Clone() AllocationMap {
	if am == nil {
		return nil
	}

	clone := make(AllocationMap)
	for id, ns := range am {
		clone[id] = ns.Clone()
	}
	return clone
}

func (arm AllocationResourcesMap) String() string {
	if arm == nil {
		return ""
	}

	contentBytes, err := json.Marshal(arm)
	if err != nil {
		klog.Errorf("[AllocationResourcesMap.String] marshal AllocationResourcesMap failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
}

func (arm AllocationResourcesMap) Clone() AllocationResourcesMap {
	clone := make(AllocationResourcesMap)
	for resourceName, am := range arm {
		clone[resourceName] = am.Clone()
	}
	return clone
}

// GetRatioOfAccompanyResourceToTargetResource returns the ratio of total accompany resource to total target resource.
// For example, if the total number of accompany resource is 4 and the total number of target resource is 2,
// the ratio is 2.
func (arm AllocationResourcesMap) GetRatioOfAccompanyResourceToTargetResource(accompanyResourceName, targetResourceName string) float64 {
	// Find the ratio of the total number of accompany resource to the total number of target resource
	accompanyResourceMap := arm[v1.ResourceName(accompanyResourceName)].Clone()
	accompanyResourceNumber := accompanyResourceMap.getNumberDevices()

	targetResourceMap := arm[v1.ResourceName(targetResourceName)].Clone()
	targetResourceNumber := targetResourceMap.getNumberDevices()

	if targetResourceNumber == 0 {
		return 0
	}

	return float64(accompanyResourceNumber) / float64(targetResourceNumber)
}

func (as *AllocationState) GetQuantityAllocated() float64 {
	if as == nil {
		return 0
	}

	quantityAllocated := float64(0)
	for _, podEntries := range as.PodEntries {
		for _, allocationInfo := range podEntries {
			quantityAllocated += allocationInfo.AllocatedAllocation.Quantity
		}
	}
	return quantityAllocated
}

func (am AllocationMap) GetQuantityAllocated(id string) float64 {
	if am == nil {
		return 0
	}

	return am[id].GetQuantityAllocated()
}

func (am AllocationMap) IsRequestSatisfied(id string, request float64, allocatable float64) bool {
	allocated := am.GetQuantityAllocated(id)
	return allocatable-allocated >= request
}

func (am AllocationMap) getNumberDevices() int {
	if am == nil {
		return 0
	}

	return len(am)
}

// reader is used to get information from local states
type reader interface {
	GetMachineState() AllocationResourcesMap
	GetPodResourceEntries() PodResourceEntries
	GetPodEntries(resourceName v1.ResourceName) PodEntries
	GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(allocationResourcesMap AllocationResourcesMap, persist bool)
	SetPodResourceEntries(podResourceEntries PodResourceEntries, persist bool)
	SetAllocationInfo(
		resourceName v1.ResourceName, podUID, containerName string, allocationInfo *AllocationInfo, persist bool,
	)

	Delete(resourceName v1.ResourceName, podUID, containerName string, persist bool)
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
