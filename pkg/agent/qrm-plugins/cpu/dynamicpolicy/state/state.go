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
	"math"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// to compatible with checkpoint checksum calculation,
// we should make guarantees below in checkpoint properties assignment
// 1. resource.Quantity use resource.MustParse("0") to initialize, not to use resource.Quantity{}
// 2. CPUSet use NewCPUSet(...) to initialize, not to use CPUSet{}
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

	// key by numa node id, value is assignment for the pod in corresponding NUMA node
	TopologyAwareAssignments map[int]machine.CPUSet `json:"topology_aware_assignments"`
	// key by numa node id, value is assignment for the pod in corresponding NUMA node
	OriginalTopologyAwareAssignments map[int]machine.CPUSet `json:"original_topology_aware_assignments"`
	// for ramp up calculation. notice we don't use time.Time type here to avid checksum corruption.
	InitTimestamp string `json:"init_timestamp"`

	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	QoSLevel        string            `json:"qosLevel"`
	RequestQuantity float64           `json:"request_quantity,omitempty"`
}

type ContainerEntries map[string]*AllocationInfo // Keyed by containerName.
type PodEntries map[string]ContainerEntries      // Keyed by podUID.

type NUMANodeState struct {
	// equals to allocatable cpuset subtracting original allocation result of dedicated_cores with NUMA binding
	DefaultCPUSet machine.CPUSet `json:"default_cpuset,omitempty"`
	// equals to original allocation result of dedicated_cores with NUMA binding
	AllocatedCPUSet machine.CPUSet `json:"allocated_cpuset,omitempty"`

	PodEntries PodEntries `json:"pod_entries"`
}

type NUMANodeMap map[int]*NUMANodeState // keyed by numa node id

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
		OwnerPoolName:            ai.OwnerPoolName,
		PodRole:                  ai.PodRole,
		PodType:                  ai.PodType,
		AllocationResult:         ai.AllocationResult.Clone(),
		OriginalAllocationResult: ai.OriginalAllocationResult.Clone(),
		InitTimestamp:            ai.InitTimestamp,
		QoSLevel:                 ai.QoSLevel,
		Labels:                   general.DeepCopyMap(ai.Labels),
		Annotations:              general.DeepCopyMap(ai.Annotations),
		RequestQuantity:          ai.RequestQuantity,
	}

	if ai.TopologyAwareAssignments != nil {
		clone.TopologyAwareAssignments = make(map[int]machine.CPUSet)

		for node, cpus := range ai.TopologyAwareAssignments {
			clone.TopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	if ai.OriginalTopologyAwareAssignments != nil {
		clone.OriginalTopologyAwareAssignments = make(map[int]machine.CPUSet)

		for node, cpus := range ai.OriginalTopologyAwareAssignments {
			clone.OriginalTopologyAwareAssignments[node] = cpus.Clone()
		}
	}

	return clone
}

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

// GetPoolName parses the owner pool name for AllocationInfo
// if owner exists, just return; otherwise, parse from qos-level
func (ai *AllocationInfo) GetPoolName() string {
	if ai == nil {
		return cpuadvisor.EmptyOwnerPoolName
	}

	if ownerPoolName := ai.GetOwnerPoolName(); ownerPoolName != cpuadvisor.EmptyOwnerPoolName {
		return ownerPoolName
	}
	return ai.GetSpecifiedPoolName()
}

// GetOwnerPoolName parses the owner pool name for AllocationInfo
func (ai *AllocationInfo) GetOwnerPoolName() string {
	if ai == nil {
		return cpuadvisor.EmptyOwnerPoolName
	}
	return ai.OwnerPoolName
}

// GetSpecifiedPoolName parses the owner pool name for AllocationInfo from qos-level
func (ai *AllocationInfo) GetSpecifiedPoolName() string {
	if ai == nil {
		return cpuadvisor.EmptyOwnerPoolName
	}

	return GetSpecifiedPoolName(ai.QoSLevel, ai.Annotations[consts.PodAnnotationCPUEnhancementCPUSet])
}

// GetSpecifiedNUMABindingPoolName get numa_binding pool name
// for numa_binding shared_cores according to enhancements and NUMA hint
func (ai *AllocationInfo) GetSpecifiedNUMABindingPoolName() (string, error) {
	if !CheckSharedNUMABinding(ai) {
		return cpuadvisor.EmptyOwnerPoolName, fmt.Errorf("GetSpecifiedNUMABindingPoolName only for numa_binding shared_cores")
	}

	numaSet, pErr := machine.Parse(ai.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint])
	if pErr != nil {
		return cpuadvisor.EmptyOwnerPoolName, fmt.Errorf("parse numaHintStr: %s failed with error: %v",
			ai.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint], pErr)
	} else if numaSet.Size() != 1 {
		return cpuadvisor.EmptyOwnerPoolName, fmt.Errorf("parse numaHintStr: %s with invalid size", numaSet.String())
	}

	specifiedPoolName := ai.GetSpecifiedPoolName()

	if specifiedPoolName == advisorapi.EmptyOwnerPoolName {
		return cpuadvisor.EmptyOwnerPoolName, fmt.Errorf("empty specifiedPoolName")
	}

	return GetNUMAPoolName(specifiedPoolName, numaSet.ToSliceNoSortUInt64()[0]), nil
}

// CheckMainContainer returns true if the AllocationInfo is for main container
func (ai *AllocationInfo) CheckMainContainer() bool {
	if ai == nil {
		return false
	}

	return ai.ContainerType == pluginapi.ContainerType_MAIN.String()
}

// GetAllocationResultNUMASet returns numaSet parsed from TopologyAwareAssignments
func (ai *AllocationInfo) GetAllocationResultNUMASet() machine.CPUSet {
	if ai == nil {
		return machine.NewCPUSet()
	}

	numaSet := machine.NewCPUSet()
	for numaNode, cset := range ai.TopologyAwareAssignments {
		if numaNode < 0 {
			general.Errorf("%s/%s/%s TopologyAwareAssignments got negtive NUMA: %d",
				ai.PodNamespace, ai.PodName, ai.ContainerName, numaNode)
			continue
		}
		if cset.Size() > 0 {
			numaSet.Add(numaNode)
		}
	}

	return numaSet
}

// CheckSideCar returns true if the AllocationInfo is for side-car container
func (ai *AllocationInfo) CheckSideCar() bool {
	if ai == nil {
		return false
	}

	return ai.ContainerType == pluginapi.ContainerType_SIDECAR.String()
}

// CheckDedicated returns true if the AllocationInfo is for pod with dedicated-qos
func CheckDedicated(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return ai.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores
}

// CheckShared returns true if the AllocationInfo is for pod with shared-qos
func CheckShared(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return ai.QoSLevel == consts.PodAnnotationQoSLevelSharedCores
}

// CheckReclaimed returns true if the AllocationInfo is for pod with reclaimed-qos
func CheckReclaimed(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return ai.QoSLevel == consts.PodAnnotationQoSLevelReclaimedCores
}

// CheckNUMABinding returns true if the AllocationInfo is for pod with numa-binding enhancement
func CheckNUMABinding(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return ai.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable
}

// CheckDedicatedNUMABinding returns true if the AllocationInfo is for pod with
// dedicated-qos and numa-binding enhancement
func CheckDedicatedNUMABinding(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return CheckDedicated(ai) && CheckNUMABinding(ai)
}

// CheckSharedNUMABinding returns true if the AllocationInfo is for pod with
// shared-qos and numa-binding enhancement
func CheckSharedNUMABinding(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return CheckShared(ai) && CheckNUMABinding(ai)
}

// CheckDedicatedPool returns true if the AllocationInfo is for a container in the dedicated pool
func CheckDedicatedPool(ai *AllocationInfo) bool {
	if ai == nil {
		return false
	}

	return ai.OwnerPoolName == PoolNameDedicated
}

// CheckNUMABindingSharedCoresAntiAffinity returns true
// if the AllocationInfo isn't compatible for the annotations of a numa binding shared cores candidate
func CheckNUMABindingSharedCoresAntiAffinity(ai *AllocationInfo, annotations map[string]string) bool {
	if ai == nil {
		return false
	} else if len(annotations) == 0 {
		return false
	}

	if CheckDedicatedNUMABinding(ai) {
		return true
	}

	if CheckSharedNUMABinding(ai) {
		// considering isolation, use specified pool instead of actual pool name here
		candidateSpecifiedPoolName := GetSpecifiedPoolName(apiconsts.PodAnnotationQoSLevelSharedCores,
			annotations[apiconsts.PodAnnotationCPUEnhancementCPUSet])
		aiSpecifiedPoolName := ai.GetSpecifiedPoolName()

		// shared_cores with numa binding doesn't support two share type pools with same specified name existing at same NUMA
		if candidateSpecifiedPoolName != aiSpecifiedPoolName {
			return true
		}
	}

	return false
}

// IsPoolEntry returns true if this entry is for a pool;
// otherwise, this entry is for a container entity.
func (ce ContainerEntries) IsPoolEntry() bool {
	return len(ce) == 1 && ce[cpuadvisor.FakedContainerName] != nil
}

func (ce ContainerEntries) GetPoolEntry() *AllocationInfo {
	if !ce.IsPoolEntry() {
		return nil
	}
	return ce[cpuadvisor.FakedContainerName]
}

// GetMainContainerEntry returns the main container entry in pod container entries
func (ce ContainerEntries) GetMainContainerEntry() *AllocationInfo {
	var mainContainerEntry *AllocationInfo

	for _, siblingEntry := range ce {
		if siblingEntry != nil && siblingEntry.CheckMainContainer() {
			mainContainerEntry = siblingEntry
			break
		}
	}

	return mainContainerEntry
}

// GetMainContainerPoolName returns the main container owner pool name in pod container entries
func (ce ContainerEntries) GetMainContainerPoolName() string {
	return ce.GetMainContainerEntry().GetOwnerPoolName()
}

func (pe PodEntries) Clone() PodEntries {
	if pe == nil {
		return nil
	}

	clone := make(PodEntries)
	for podUID, containerEntries := range pe {
		if containerEntries == nil {
			continue
		}

		clone[podUID] = make(ContainerEntries)
		for containerName, allocationInfo := range containerEntries {
			clone[podUID][containerName] = allocationInfo.Clone()
		}
	}
	return clone
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

// CheckPoolEmpty returns true if the given pool doesn't exist
func (pe PodEntries) CheckPoolEmpty(poolName string) bool {
	return pe[poolName][cpuadvisor.FakedContainerName] == nil ||
		pe[poolName][cpuadvisor.FakedContainerName].AllocationResult.IsEmpty()
}

// GetCPUSetForPool returns cpuset that belongs to the given pool
func (pe PodEntries) GetCPUSetForPool(poolName string) (machine.CPUSet, error) {
	if pe == nil {
		return machine.NewCPUSet(), fmt.Errorf("GetCPUSetForPool from nil podEntries")
	}

	if !pe[poolName].IsPoolEntry() {
		return machine.NewCPUSet(), fmt.Errorf("pool not found")
	}
	return pe[poolName][cpuadvisor.FakedContainerName].AllocationResult.Clone(), nil
}

// GetFilteredPoolsCPUSet returns a mapping of pools for all of them (except for those skipped ones)
func (pe PodEntries) GetFilteredPoolsCPUSet(ignorePools sets.String) machine.CPUSet {
	ret := machine.NewCPUSet()
	if pe == nil {
		return ret
	}

	for poolName, entries := range pe {
		allocationInfo := entries.GetPoolEntry()
		if allocationInfo != nil && !ignorePools.Has(poolName) {
			ret = ret.Union(allocationInfo.AllocationResult.Clone())
		}
	}
	return ret
}

// GetFilteredPoolsCPUSetMap returns a mapping of pools for all of them (except for those skipped ones)
func (pe PodEntries) GetFilteredPoolsCPUSetMap(ignorePools sets.String) (map[string]map[int]machine.CPUSet, error) {
	ret := make(map[string]map[int]machine.CPUSet)
	if pe == nil {
		return ret, nil
	}

	for poolName, entries := range pe {
		allocationInfo := entries.GetPoolEntry()
		if allocationInfo != nil && !ignorePools.Has(poolName) {
			cset := allocationInfo.AllocationResult.Clone()

			// pool entry containing SharedNUMABinding containers also has SharedNUMABinding declarations,
			// it's also applicable to isolation pools for SharedNUMABinding containers.
			// it helps us to differentiate them from normal share pools when getting targetNUMAID for the pool.
			if CheckSharedNUMABinding(allocationInfo) {
				// pool for numa_binding shared_cores containers

				numaSet := allocationInfo.GetAllocationResultNUMASet()
				numaSetSize := numaSet.Size()

				if numaSetSize == 0 {
					return nil, fmt.Errorf("empty numaSet for pool: %s, cset: %s", poolName, cset.String())
				} else if numaSetSize == 1 {
					ret[poolName] = make(map[int]machine.CPUSet)
					targetNUMAID := numaSet.ToSliceInt()[0]

					if targetNUMAID < 0 {
						return nil, fmt.Errorf("pool: %s has invalid NUMA: %d",
							poolName, targetNUMAID)
					}

					ret[poolName][targetNUMAID] = cset
				} else {
					return nil, fmt.Errorf("pool: %s for numa_binding shared_cores cross NUMAs: %s cset: %s",
						poolName, numaSet.String(), cset.String())
				}
			} else {
				// pool for shared_cores without numa_binding containers
				ret[poolName] = make(map[int]machine.CPUSet)
				ret[poolName][cpuadvisor.FakedNUMAID] = cset
			}
		}
	}

	return ret, nil
}

// GetFilteredPodEntries filter out PodEntries according to the given filter logic
func (pe PodEntries) GetFilteredPodEntries(filter func(ai *AllocationInfo) bool) PodEntries {
	numaBindingEntries := make(PodEntries)
	for podUID, containerEntries := range pe {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo != nil && filter(allocationInfo) {
				if numaBindingEntries[podUID] == nil {
					numaBindingEntries[podUID] = make(ContainerEntries)
				}
				numaBindingEntries[podUID][containerName] = allocationInfo.Clone()
			}
		}
	}
	return numaBindingEntries
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

// GetAvailableCPUSet returns available cpuset in this numa
func (ns *NUMANodeState) GetAvailableCPUSet(reservedCPUs machine.CPUSet) machine.CPUSet {
	if ns == nil {
		return machine.NewCPUSet()
	}
	return ns.DefaultCPUSet.Difference(reservedCPUs)
}

// GetAvailableCPUQuantity calculates available quantity by allocatable - sum(requested)
// It's used when allocating CPUs for shared_cores with numa_binding containers,
// since pool size may be adjusted, and DefaultCPUSet & AllocatedCPUSet are calculated by pool size,
// we should use allocationInfo.RequestQuantity to calculate available cpu quantity for candidate shared_cores with numa_binding container.
func (ns *NUMANodeState) GetAvailableCPUQuantity(reservedCPUs machine.CPUSet) int {
	if ns == nil {
		return 0
	}

	allocatableQuantity := ns.GetFilteredDefaultCPUSet(nil, nil).Difference(reservedCPUs).Size()
	var preciseAllocatedQuantity float64 = 0

	for _, containerEntries := range ns.PodEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {
			// sidecar doesn't contribute to allocated quantity currently
			if allocationInfo == nil ||
				!CheckSharedNUMABinding(allocationInfo) {
				continue
			}

			preciseAllocatedQuantity += allocationInfo.RequestQuantity
		}
	}

	allocatedQuantity := int(math.Ceil(preciseAllocatedQuantity))
	return general.Max(allocatableQuantity-allocatedQuantity, 0)
}

// GetFilteredDefaultCPUSet returns default cpuset in this numa, along with the filter functions
func (ns *NUMANodeState) GetFilteredDefaultCPUSet(excludeEntry, excludeWholeNUMA func(ai *AllocationInfo) bool) machine.CPUSet {
	if ns == nil {
		return machine.NewCPUSet()
	}

	res := ns.DefaultCPUSet.Clone()
	res = res.Union(ns.AllocatedCPUSet)
	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if excludeWholeNUMA != nil && excludeWholeNUMA(allocationInfo) {
				return machine.NewCPUSet()
			} else if excludeEntry != nil && excludeEntry(allocationInfo) {
				res = res.Difference(allocationInfo.AllocationResult)
			}
		}
	}
	return res
}

// ExistMatchedAllocationInfo returns true if the stated predicate holds true for some pods of this numa else it returns false.
func (ns *NUMANodeState) ExistMatchedAllocationInfo(f func(ai *AllocationInfo) bool) bool {
	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if f(allocationInfo) {
				return true
			}
		}
	}

	return false
}

// ExistMatchedAllocationInfo returns true if the stated predicate (with annotations of candidate)
// holds true for some pods of this numa else it returns false.
func (ns *NUMANodeState) ExistMatchedAllocationInfoWithAnnotations(
	f func(ai *AllocationInfo, annotations map[string]string) bool,
	annotations map[string]string) bool {

	for _, containerEntries := range ns.PodEntries {
		for _, allocationInfo := range containerEntries {
			if f(allocationInfo, annotations) {
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

// GetDefaultCPUSet returns default cpuset in this node
func (nm NUMANodeMap) GetDefaultCPUSet() machine.CPUSet {
	res := machine.NewCPUSet()
	for _, numaNodeState := range nm {
		res = res.Union(numaNodeState.DefaultCPUSet)
	}
	return res
}

// GetAvailableCPUSet returns available cpuset in this node
func (nm NUMANodeMap) GetAvailableCPUSet(reservedCPUs machine.CPUSet) machine.CPUSet {
	return nm.GetDefaultCPUSet().Difference(reservedCPUs)
}

// GetFilteredDefaultCPUSet returns default cpuset in this node, along with the filter functions
func (nm NUMANodeMap) GetFilteredDefaultCPUSet(excludeEntry, excludeWholeNUMA func(ai *AllocationInfo) bool) machine.CPUSet {
	res := machine.NewCPUSet()
	for _, numaNodeState := range nm {
		res = res.Union(numaNodeState.GetFilteredDefaultCPUSet(excludeEntry, excludeWholeNUMA))
	}
	return res
}

// GetFilteredAvailableCPUSet returns available cpuset in this node, along with the filter functions
func (nm NUMANodeMap) GetFilteredAvailableCPUSet(reservedCPUs machine.CPUSet,
	excludeEntry, excludeWholeNUMA func(ai *AllocationInfo) bool) machine.CPUSet {
	return nm.GetFilteredDefaultCPUSet(excludeEntry, excludeWholeNUMA).Difference(reservedCPUs)
}

// GetFilteredNUMASet return numa set except the numa which are excluded by the predicate.
func (nm NUMANodeMap) GetFilteredNUMASet(excludeNUMAPredicate func(ai *AllocationInfo) bool) machine.CPUSet {
	res := machine.NewCPUSet()
	for numaID, numaNodeState := range nm {
		if numaNodeState.ExistMatchedAllocationInfo(excludeNUMAPredicate) {
			continue
		}
		res.Add(numaID)
	}
	return res
}

// GetFilteredNUMASetWithAnnotations return numa set except the numa
// which are excluded by the predicate accepting AllocationInfo in the target NUMA and input annotations of candidate.
func (nm NUMANodeMap) GetFilteredNUMASetWithAnnotations(
	excludeNUMAPredicate func(ai *AllocationInfo, annotations map[string]string) bool,
	annotations map[string]string) machine.CPUSet {
	res := machine.NewCPUSet()
	for numaID, numaNodeState := range nm {
		if numaNodeState.ExistMatchedAllocationInfoWithAnnotations(excludeNUMAPredicate, annotations) {
			continue
		}
		res.Add(numaID)
	}
	return res
}

func (nm NUMANodeMap) Clone() NUMANodeMap {
	if nm == nil {
		return nil
	}

	clone := make(NUMANodeMap)
	for node, ns := range nm {
		clone[node] = ns.Clone()
	}
	return clone
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

// reader is used to get information from local states
type reader interface {
	GetMachineState() NUMANodeMap
	GetPodEntries() PodEntries
	GetAllocationInfo(podUID string, containerName string) *AllocationInfo
}

// writer is used to store information into local states,
// and it also provides functionality to maintain the local files
type writer interface {
	SetMachineState(numaNodeMap NUMANodeMap)
	SetPodEntries(podEntries PodEntries)
	SetAllocationInfo(podUID string, containerName string, allocationInfo *AllocationInfo)

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
