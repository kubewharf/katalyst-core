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

package commonstate

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type AllocationMeta struct {
	PodUid         string `json:"pod_uid,omitempty"`
	PodNamespace   string `json:"pod_namespace,omitempty"`
	PodName        string `json:"pod_name,omitempty"`
	ContainerName  string `json:"container_name,omitempty"`
	ContainerType  string `json:"container_type,omitempty"`
	ContainerIndex uint64 `json:"container_index,omitempty"`
	OwnerPoolName  string `json:"owner_pool_name,omitempty"`
	PodRole        string `json:"pod_role,omitempty"`
	PodType        string `json:"pod_type,omitempty"`

	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	QoSLevel    string            `json:"qosLevel"`
}

func (am *AllocationMeta) GetPodUid() string {
	return am.PodUid
}

func (am *AllocationMeta) GetPodNamespace() string {
	return am.PodNamespace
}

func (am *AllocationMeta) GetPodName() string {
	return am.PodName
}

func (am *AllocationMeta) GetContainerName() string {
	return am.ContainerName
}

func (am *AllocationMeta) GetContainerType() string {
	return am.ContainerType
}

func (am *AllocationMeta) GetContainerIndex() uint64 {
	return am.ContainerIndex
}

func (am *AllocationMeta) GetPodRole() string {
	return am.PodRole
}

func (am *AllocationMeta) GetPodType() string {
	return am.PodType
}

func (am *AllocationMeta) GetLabels() map[string]string {
	return am.Labels
}

func (am *AllocationMeta) GetAnnotations() map[string]string {
	return am.Annotations
}

func (am *AllocationMeta) GetQoSLevel() string {
	return am.QoSLevel
}

func (am *AllocationMeta) Clone() *AllocationMeta {
	clone := &AllocationMeta{
		PodUid:         am.PodUid,
		PodNamespace:   am.PodNamespace,
		PodName:        am.PodName,
		ContainerName:  am.ContainerName,
		ContainerType:  am.ContainerType,
		ContainerIndex: am.ContainerIndex,
		OwnerPoolName:  am.OwnerPoolName,
		PodRole:        am.PodRole,
		PodType:        am.PodType,
		QoSLevel:       am.QoSLevel,
		Labels:         general.DeepCopyMap(am.Labels),
		Annotations:    general.DeepCopyMap(am.Annotations),
	}

	return clone
}

// GetPoolName parses the owner pool name for AllocationInfo
// if owner exists, just return; otherwise, parse from qos-level
func (am *AllocationMeta) GetPoolName() string {
	if am == nil {
		return EmptyOwnerPoolName
	}

	if ownerPoolName := am.GetOwnerPoolName(); ownerPoolName != EmptyOwnerPoolName {
		return ownerPoolName
	}
	return am.GetSpecifiedPoolName()
}

// GetOwnerPoolName parses the owner pool name for AllocationInfo
func (am *AllocationMeta) GetOwnerPoolName() string {
	if am == nil {
		return EmptyOwnerPoolName
	}
	return am.OwnerPoolName
}

func (am *AllocationMeta) GetResourcePackageName() string {
	if am == nil {
		return ""
	}

	return resourcepackage.GetResourcePackageName(am.Annotations)
}

// GetSpecifiedPoolName parses the owner pool name for AllocationInfo from qos-level
func (am *AllocationMeta) GetSpecifiedPoolName() string {
	if am == nil {
		return EmptyOwnerPoolName
	}

	return GetSpecifiedPoolName(am.QoSLevel, am.Annotations[consts.PodAnnotationCPUEnhancementCPUSet])
}

// GetSpecifiedNUMABindingNUMAID parses the numa id for AllocationInfo
func (am *AllocationMeta) GetSpecifiedNUMABindingNUMAID() (int, error) {
	if am == nil {
		return FakedNUMAID, fmt.Errorf("empty am")
	}

	return GetSpecifiedNUMABindingNUMAID(am.Annotations)
}

// SetSpecifiedNUMABindingNUMAID set the numa id for AllocationInfo
func (am *AllocationMeta) SetSpecifiedNUMABindingNUMAID(numaID uint64) {
	if am == nil {
		return
	}

	if am.Annotations == nil {
		am.Annotations = make(map[string]string)
	}

	am.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint] = machine.NewCPUSet(int(numaID)).String()
}

// GetSpecifiedNUMABindingPoolName get numa_binding pool name
// for numa_binding shared_cores according to enhancements and NUMA hint
func (am *AllocationMeta) GetSpecifiedNUMABindingPoolName() (string, error) {
	return GetSpecifiedNUMABindingPoolName(am.QoSLevel, am.Annotations)
}

func GetNUMAPoolName(candidateSpecifiedPoolName string, targetNUMANode int) string {
	return fmt.Sprintf("%s%s%d", candidateSpecifiedPoolName, NUMAPoolInfix, targetNUMANode)
}

// CheckMainContainer returns true if the AllocationInfo is for main container
func (am *AllocationMeta) CheckMainContainer() bool {
	if am == nil {
		return false
	}

	return am.ContainerType == pluginapi.ContainerType_MAIN.String()
}

// CheckSideCar returns true if the AllocationInfo is for side-car container
func (am *AllocationMeta) CheckSideCar() bool {
	if am == nil {
		return false
	}

	return am.ContainerType == pluginapi.ContainerType_SIDECAR.String()
}

func (am *AllocationMeta) GetSpecifiedSystemPoolName() (string, error) {
	if !am.CheckSystem() {
		return EmptyOwnerPoolName, fmt.Errorf("GetSpecifiedSystemPoolName only for system_cores")
	}

	specifiedPoolName := am.GetSpecifiedPoolName()
	if specifiedPoolName == EmptyOwnerPoolName {
		return specifiedPoolName, nil
	}

	return fmt.Sprintf("%s%s%s", PoolNamePrefixSystem, "-", specifiedPoolName), nil
}

func (am *AllocationMeta) CheckSystem() bool {
	if am == nil {
		return false
	}
	return am.QoSLevel == consts.PodAnnotationQoSLevelSystemCores
}

// CheckDedicated returns true if the AllocationInfo is for pod with dedicated-qos
func (am *AllocationMeta) CheckDedicated() bool {
	if am == nil {
		return false
	}
	return am.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores
}

// CheckShared returns true if the AllocationInfo is for pod with shared-qos
func (am *AllocationMeta) CheckShared() bool {
	if am == nil {
		return false
	}
	return am.QoSLevel == consts.PodAnnotationQoSLevelSharedCores
}

// CheckReclaimed returns true if the AllocationInfo is for pod with reclaimed-qos
func (am *AllocationMeta) CheckReclaimed() bool {
	if am == nil {
		return false
	}
	return am.QoSLevel == consts.PodAnnotationQoSLevelReclaimedCores
}

// CheckNUMABinding returns true if the AllocationInfo is for pod with numa-binding enhancement
func (am *AllocationMeta) CheckNUMABinding() bool {
	if am == nil {
		return false
	}
	return am.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] ==
		consts.PodAnnotationMemoryEnhancementNumaBindingEnable
}

func (am *AllocationMeta) CheckNUMANotShare() bool {
	if am == nil {
		return false
	}
	return am.Annotations[consts.PodAnnotationCPUEnhancementNUMAShare] ==
		consts.PodAnnotationCPUEnhancementNUMAShareDisable
}

// CheckActualNUMABinding returns true if the AllocationInfo is for pod actual numa-binding
func (am *AllocationMeta) CheckActualNUMABinding() bool {
	if am == nil {
		return false
	}
	return am.Annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint] != ""
}

// CheckDedicatedNUMABinding returns true if the AllocationInfo is for pod with
// dedicated-qos and numa-binding enhancement
func (am *AllocationMeta) CheckDedicatedNUMABinding() bool {
	return am.CheckDedicated() && am.CheckNUMABinding()
}

// CheckDedicatedNUMABindingNUMAExclusive returns true if the AllocationInfo is for pod with
// dedicated-qos and numa-binding and numa-exclusive enhancement
func (am *AllocationMeta) CheckDedicatedNUMABindingNUMAExclusive() bool {
	return am.CheckDedicatedNUMABinding() && am.CheckNumaExclusive()
}

// CheckSharedNUMABinding returns true if the AllocationInfo is for pod with
// shared-qos and numa-binding enhancement
func (am *AllocationMeta) CheckSharedNUMABinding() bool {
	return am.CheckShared() && am.CheckNUMABinding()
}

// CheckSharedOrDedicatedNUMABinding returns true if the AllocationInfo is for pod with
// shared-qos or dedicated-qos and numa-binding enhancement
func (am *AllocationMeta) CheckSharedOrDedicatedNUMABinding() bool {
	return am.CheckSharedNUMABinding() || am.CheckDedicatedNUMABinding()
}

// CheckReclaimedNUMABinding returns true if the AllocationInfo is for pod with
// reclaimed-qos and numa-binding enhancement
func (am *AllocationMeta) CheckReclaimedNUMABinding() bool {
	return am.CheckReclaimed() && am.CheckNUMABinding()
}

// CheckReclaimedActualNUMABinding returns true if the AllocationInfo is for pod with
// reclaimed-qos and numa-binding enhancement and numa hint is not empty, which means
// the container is allocated on a specific NUMA node
func (am *AllocationMeta) CheckReclaimedActualNUMABinding() bool {
	return am.CheckReclaimedNUMABinding() && am.CheckActualNUMABinding()
}

// CheckReclaimedNonActualNUMABinding returns true if the AllocationInfo is for pod with
// reclaimed-qos and without binding to a specific NUMA node actually, which means
// the pod can be allocated on multi NUMA nodes
func (am *AllocationMeta) CheckReclaimedNonActualNUMABinding() bool {
	return am.CheckReclaimed() && !am.CheckActualNUMABinding()
}

// CheckNumaExclusive returns true if the AllocationInfo is for pod with numa-exclusive enhancement
func (am *AllocationMeta) CheckNumaExclusive() bool {
	if am == nil {
		return false
	}

	return am.Annotations[consts.PodAnnotationMemoryEnhancementNumaExclusive] ==
		consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable
}

// CheckDedicatedPool returns true if the AllocationInfo is for a container in the dedicated pool
func (am *AllocationMeta) CheckDedicatedPool() bool {
	if am == nil {
		return false
	}
	return am.OwnerPoolName == PoolNameDedicated
}
