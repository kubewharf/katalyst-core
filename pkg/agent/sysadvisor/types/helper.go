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
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func (ci *ContainerInfo) IsNumaBinding() bool {
	return qosutil.AnnotationsIndicateNUMABinding(ci.Annotations)
}

func (ci *ContainerInfo) IsNumaExclusive() bool {
	return qosutil.AnnotationsIndicateNUMAExclusive(ci.Annotations)
}

func (ci *ContainerInfo) IsSharedNumaBinding() bool {
	return ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores && ci.IsNumaBinding()
}

// IsDedicatedNumaBinding returns true if current container is for dedicated_cores with numa binding
func (ci *ContainerInfo) IsDedicatedNumaBinding() bool {
	return ci.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores && ci.IsNumaBinding()
}

// GetActualNUMABindingResult returns the actual numa binding result of the container.
// If the container is not numa binding, it will return -1.
func (ci *ContainerInfo) GetActualNUMABindingResult() (int, error) {
	if ci == nil {
		return 0, fmt.Errorf("containerInfo is nil")
	}

	return commonstate.GetSpecifiedNUMABindingNUMAID(ci.Annotations)
}

// GetResourcePackageName returns the resource package name of the container.
// It retrieves the package name from the container's annotations.
// If the container info is nil or the annotation is missing, it returns an empty string.
func (ci *ContainerInfo) GetResourcePackageName() string {
	if ci == nil {
		return ""
	}

	return resourcepackage.GetResourcePackageName(ci.Annotations)
}

func (ci *ContainerInfo) IsDedicatedNumaExclusive() bool {
	return ci.IsDedicatedNumaBinding() && ci.IsNumaExclusive()
}

func (ci *ContainerInfo) Clone() *ContainerInfo {
	if ci == nil {
		return nil
	}
	clone := &ContainerInfo{
		PodUID:                           ci.PodUID,
		PodNamespace:                     ci.PodNamespace,
		PodName:                          ci.PodName,
		ContainerName:                    ci.ContainerName,
		ContainerType:                    ci.ContainerType,
		ContainerIndex:                   ci.ContainerIndex,
		Labels:                           general.DeepCopyMap(ci.Labels),
		Annotations:                      general.DeepCopyMap(ci.Annotations),
		QoSLevel:                         ci.QoSLevel,
		CPURequest:                       ci.CPURequest,
		CPULimit:                         ci.CPULimit,
		MemoryRequest:                    ci.MemoryRequest,
		MemoryLimit:                      ci.MemoryLimit,
		RampUp:                           ci.RampUp,
		OriginOwnerPoolName:              ci.OriginOwnerPoolName,
		OwnerPoolName:                    ci.OwnerPoolName,
		TopologyAwareAssignments:         ci.TopologyAwareAssignments.Clone(),
		OriginalTopologyAwareAssignments: ci.OriginalTopologyAwareAssignments.Clone(),
		RegionNames:                      sets.NewString(ci.RegionNames.List()...),
		Isolated:                         ci.Isolated,
	}
	return clone
}

// UpdateMeta updates mutable container meta from another container info
func (ci *ContainerInfo) UpdateMeta(c *ContainerInfo) {
	// The CPURequest here is calculated from math.Ceil(Actual CPURequest), but the "Actual CPURequest" fails to be retrieved at this stage.
	// So this value will be replaced with its "Actual CPURequest" in periodicWork of MetaCachePlugin(pkg/agent/sysadvisor/plugin/metacache/metacache.go).
	if c.CPURequest > 0 {
		ci.CPURequest = c.CPURequest
	}
	if c.CPULimit > 0 {
		ci.CPULimit = c.CPULimit
	}
	if c.MemoryRequest > 0 {
		ci.MemoryRequest = c.MemoryRequest
	}
	if c.MemoryLimit > 0 {
		ci.MemoryLimit = c.MemoryLimit
	}
}

func (ta TopologyAwareAssignment) Clone() TopologyAwareAssignment {
	if ta == nil {
		return nil
	}
	clone := make(TopologyAwareAssignment)
	for numaID, cpuset := range ta {
		clone[numaID] = cpuset.Clone()
	}
	return clone
}

// MergeCPUSet returns a merged machine.CPUSet belonging to this TopologyAwareAssignment
func (ta TopologyAwareAssignment) MergeCPUSet() machine.CPUSet {
	cpusets := machine.NewCPUSet()
	for _, cpuset := range ta {
		cpusets = cpusets.Union(cpuset)
	}
	return cpusets
}

func (ta TopologyAwareAssignment) Equals(t TopologyAwareAssignment) bool {
	return reflect.DeepEqual(ta, t)
}

func (pi *PoolInfo) Clone() *PoolInfo {
	if pi == nil {
		return nil
	}
	clone := &PoolInfo{
		PoolName:                         pi.PoolName,
		TopologyAwareAssignments:         pi.TopologyAwareAssignments.Clone(),
		OriginalTopologyAwareAssignments: pi.OriginalTopologyAwareAssignments.Clone(),
		RegionNames:                      sets.NewString(pi.RegionNames.List()...),
	}
	return clone
}

func (ri *RegionInfo) Clone() *RegionInfo {
	if ri == nil {
		return nil
	}
	clone := &RegionInfo{
		RegionName:    ri.RegionName,
		RegionType:    ri.RegionType,
		OwnerPoolName: ri.OwnerPoolName,
		BindingNumas:  ri.BindingNumas.Clone(),
		RegionStatus:  ri.RegionStatus.Clone(),
		Pods:          ri.Pods.Clone(),

		HeadroomPolicyTopPriority: ri.HeadroomPolicyTopPriority,
		HeadroomPolicyInUse:       ri.HeadroomPolicyInUse,
		Headroom:                  ri.Headroom,

		ProvisionPolicyTopPriority: ri.ProvisionPolicyTopPriority,
		ProvisionPolicyInUse:       ri.ProvisionPolicyInUse,
		ControlKnobMap:             ri.ControlKnobMap.Clone(),
	}
	return clone
}

func (ce ContainerEntries) Clone() ContainerEntries {
	if ce == nil {
		return nil
	}
	clone := make(ContainerEntries)
	for containerName, containerInfo := range ce {
		clone[containerName] = containerInfo.Clone()
	}
	return clone
}

func (pe PodEntries) Clone() PodEntries {
	if pe == nil {
		return nil
	}
	clone := make(PodEntries)
	for podUID, containerEntries := range pe {
		clone[podUID] = containerEntries.Clone()
	}
	return clone
}

func (pe PoolEntries) Clone() PoolEntries {
	if pe == nil {
		return nil
	}
	clone := make(PoolEntries)
	for poolName, poolInfo := range pe {
		clone[poolName] = poolInfo.Clone()
	}
	return clone
}

func (re RegionEntries) Clone() RegionEntries {
	if re == nil {
		return nil
	}
	clone := make(RegionEntries)
	for regionName, regionInfo := range re {
		clone[regionName] = regionInfo.Clone()
	}
	return clone
}

func (rs RegionStatus) Clone() RegionStatus {
	clone := RegionStatus{
		OvershootStatus: make(map[string]OvershootType),
		BoundType:       rs.BoundType,
	}

	for metric, overshootType := range rs.OvershootStatus {
		clone.OvershootStatus[metric] = overshootType
	}
	return clone
}

func (ps PodSet) Clone() PodSet {
	if ps == nil {
		return nil
	}
	clone := make(PodSet)
	for k, v := range ps {
		clone[k] = sets.NewString(v.List()...)
	}
	return clone
}

func (hi *HeadroomInfo) Clone() *HeadroomInfo {
	if hi == nil {
		return nil
	}
	clone := &HeadroomInfo{
		NUMAHeadroom:  map[int]float64{},
		TotalHeadroom: hi.TotalHeadroom,
	}

	for k, v := range hi.NUMAHeadroom {
		clone.NUMAHeadroom[k] = v
	}

	return clone
}

func (ps PodSet) Insert(podUID string, containerName string) {
	containerSet, ok := ps[podUID]
	if !ok {
		ps[podUID] = sets.NewString()
		containerSet = ps[podUID]
	}
	containerSet.Insert(containerName)
}

// PopAny Returns a single element from the set, in format of podUID, containerName
func (ps PodSet) PopAny() (string, string, bool) {
	var zeroValue string
	for podUID, containerNames := range ps {
		containerName, ok := containerNames.PopAny()
		if !ok {
			return zeroValue, zeroValue, false
		}
		if containerNames.Len() == 0 {
			delete(ps, podUID)
		}
		return podUID, containerName, true
	}
	return zeroValue, zeroValue, false
}

func (ps PodSet) Pods() int {
	count := 0
	for _, containerNames := range ps {
		if containerNames.Len() > 0 {
			count++
		}
	}
	return count
}

func (r *InternalCPUCalculationResult) SetPoolEntry(poolName string, numaID int, poolSize int, cpuLimit float64) {
	if r.PoolEntries[poolName] == nil {
		r.PoolEntries[poolName] = make(map[int]CPUResource)
	}
	r.PoolEntries[poolName][numaID] = CPUResource{Size: poolSize, Quota: cpuLimit}
}

func (r *InternalCPUCalculationResult) SetPoolOverlapInfo(poolName string, numaID int, overlapPoolName string, poolSize int) {
	if poolSize <= 0 {
		return
	}
	if r.PoolOverlapInfo[poolName] == nil {
		r.PoolOverlapInfo[poolName] = map[int]map[string]int{}
	}
	if r.PoolOverlapInfo[poolName][numaID] == nil {
		r.PoolOverlapInfo[poolName][numaID] = map[string]int{}
	}
	r.PoolOverlapInfo[poolName][numaID][overlapPoolName] = poolSize
}

func (r *InternalCPUCalculationResult) GetPoolOverlapInfo(poolName string, numaID int) map[string]int {
	v, ok := r.PoolOverlapInfo[poolName]
	if !ok {
		return nil
	}
	return v[numaID]
}

func (r *InternalCPUCalculationResult) SetPoolOverlapPodContainerInfo(poolName string, numaID int, podUID string, containerName string, poolSize int) {
	if poolSize <= 0 {
		return
	}
	if r.PoolOverlapPodContainerInfo[poolName] == nil {
		r.PoolOverlapPodContainerInfo[poolName] = map[int]map[string]map[string]int{}
	}
	if r.PoolOverlapPodContainerInfo[poolName][numaID] == nil {
		r.PoolOverlapPodContainerInfo[poolName][numaID] = map[string]map[string]int{}
	}
	if r.PoolOverlapPodContainerInfo[poolName][numaID][podUID] == nil {
		r.PoolOverlapPodContainerInfo[poolName][numaID][podUID] = map[string]int{}
	}
	r.PoolOverlapPodContainerInfo[poolName][numaID][podUID][containerName] = poolSize
}

func (r *InternalCPUCalculationResult) GetPoolOverlapPodContainerInfo(poolName string, numaID int) map[string]map[string]int {
	v, ok := r.PoolOverlapPodContainerInfo[poolName]
	if !ok {
		return nil
	}
	return v[numaID]
}

func (ck ControlKnob) Clone() ControlKnob {
	if ck == nil {
		return nil
	}
	clone := make(ControlKnob)
	for k, v := range ck {
		clone[k] = v
	}
	return clone
}

func (i Indicator) Clone() Indicator {
	if i == nil {
		return nil
	}
	clone := make(Indicator)
	for k, v := range i {
		clone[k] = v
	}
	return clone
}

type ContainerInfoList struct {
	containers []*ContainerInfo
}

var _ general.SourceList = &ContainerInfoList{}

func NewContainerSourceImpList(containers []*ContainerInfo) general.SourceList {
	return &ContainerInfoList{
		containers: containers,
	}
}

func (cl *ContainerInfoList) Len() int {
	return len(cl.containers)
}

func (cl *ContainerInfoList) GetSource(index int) interface{} {
	return cl.containers[index]
}

func (cl *ContainerInfoList) SetSource(index int, p interface{}) {
	cl.containers[index] = p.(*ContainerInfo)
}

func NumaIDBitMask(numaIDs []int) int {
	ret := int(0)
	for _, id := range numaIDs {
		ret += 1 << id
	}
	return ret
}

func CompatibleLegacyCPUHeadroomPolicyName(policy string) CPUHeadroomPolicyName {
	if CPUHeadroomPolicyName(policy) == CPUHeadroomPolicyNUMAExclusive {
		return CPUHeadroomPolicyNUMADedicated
	}
	return CPUHeadroomPolicyName(policy)
}
