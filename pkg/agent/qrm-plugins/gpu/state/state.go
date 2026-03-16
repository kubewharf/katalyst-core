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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type AllocationInfo struct {
	commonstate.AllocationMeta `json:",inline"`

	AllocatedAllocation      Allocation            `json:"allocated_allocation"`
	TopologyAwareAllocations map[string]Allocation `json:"topology_aware_allocations"`
	DeviceName               string                `json:"device_name"`
}

type Allocation struct {
	// Quantity refers to the device count or the quantity of resource allocated.
	// Note: sum of quantity may be greater than allocatable because some pods can share devices.
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
	// Allocatable refers to the total quantity of resource or device count that can be allocated.
	Allocatable float64    `json:"allocatable"`
	PodEntries  PodEntries `json:"pod_entries"`
}

type AllocationMap map[string]*AllocationState // AllocationMap keyed by device id i.e. GPU-fef8089b-4820-abfc-e83e-94318197576e

// AllocationResourcesMap keyed by resource name.
// For devices, they are keyed by device type (e.g. rdma_device, gpu_device).
// For resources, they are keyed by the resource name (e.g. resource.katalyst.kubewharf.io/gpu_memory).
type AllocationResourcesMap map[v1.ResourceName]AllocationMap

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
		DeviceName:          i.DeviceName,
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
		PodEntries:  as.PodEntries.Clone(),
		Allocatable: as.Allocatable,
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

func (am AllocationMap) String() string {
	if am == nil {
		return ""
	}

	contentBytes, err := json.Marshal(am)
	if err != nil {
		general.LoggerWithPrefix("AllocationMap.String", general.LoggingPKGFull).Errorf("[AllocationMap.String]marshal AllocationMap failed with error: %v", err)
		return ""
	}
	return string(contentBytes)
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

// GetAllocatedDeviceIDs returns the deviceIDs that are allocated to the specific container.
func (arm AllocationResourcesMap) GetAllocatedDeviceIDs(resourceName v1.ResourceName, podUID, containerName string) []string {
	result := make([]string, 0)
	allocationMap, ok := arm[resourceName]
	if !ok {
		return result
	}

	for deviceID, allocationState := range allocationMap {
		if allocationState == nil || allocationState.PodEntries == nil {
			continue
		}

		containerEntries, ok := allocationState.PodEntries[podUID]
		if !ok {
			continue
		}

		if allocationInfo, ok := containerEntries[containerName]; ok && allocationInfo != nil {
			result = append(result, deviceID)
		}
	}

	return result
}

// GetRatioOfAccompanyResourceToTargetResource returns the ratio of total accompany resource to total target resource.
// For example, if the total number of accompany resource is 4 and the total number of target resource is 2,
// the ratio is 2.
func (arm AllocationResourcesMap) GetRatioOfAccompanyResourceToTargetResource(accompanyResourceName, resourceName string) float64 {
	// Find the ratio of the total number of pre-allocated resource to the total number of target resource
	accompanyResourceMap := arm[v1.ResourceName(accompanyResourceName)].Clone()
	accompanyResourceNumber := accompanyResourceMap.getNumberDevices()

	targetResourceMap := arm[v1.ResourceName(resourceName)].Clone()
	targetResourceNumber := targetResourceMap.getNumberDevices()

	if targetResourceNumber == 0 {
		return 0
	}

	return float64(accompanyResourceNumber) / float64(targetResourceNumber)
}

func (as *AllocationState) GetQuantityAllocated() float64 {
	return as.GetQuantityAllocatedWithFilter(nil)
}

func (as *AllocationState) GetQuantityAllocatedWithFilter(filter func(ai *AllocationInfo) bool) float64 {
	if as == nil {
		return 0
	}

	quantityAllocated := float64(0)
	for _, containerEntries := range as.PodEntries {
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if filter != nil && !filter(allocationInfo) {
				continue
			}
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
	if am == nil {
		return false
	}

	allocated := am.GetQuantityAllocated(id)
	return allocatable-allocated >= request
}

func (am AllocationMap) getNumberDevices() int {
	if am == nil {
		return 0
	}

	return len(am)
}

type DefaultResourceStateGeneratorRegistry struct {
	mutex      sync.RWMutex
	generators map[string]DefaultResourceStateGenerator
}

func NewDefaultResourceStateGeneratorRegistry() *DefaultResourceStateGeneratorRegistry {
	return &DefaultResourceStateGeneratorRegistry{
		generators: make(map[string]DefaultResourceStateGenerator),
	}
}

func (r *DefaultResourceStateGeneratorRegistry) RegisterResourceStateGenerator(resourceName string, generator DefaultResourceStateGenerator) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.generators[resourceName] = generator
}

func (r *DefaultResourceStateGeneratorRegistry) GetGenerators() map[string]DefaultResourceStateGenerator {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return r.generators
}

func (r *DefaultResourceStateGeneratorRegistry) GetGenerator(resourceName string) (DefaultResourceStateGenerator, bool) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	generator, ok := r.generators[resourceName]
	return generator, ok
}
