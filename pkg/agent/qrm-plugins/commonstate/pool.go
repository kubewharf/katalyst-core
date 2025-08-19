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
	"strings"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// notice that pool-name may not have direct mapping relations with qos-level, for instance
// - both isolated_shared_cores and dedicated_cores fall into PoolNameDedicated
const (
	PoolNameShare           = "share"
	PoolNameReclaim         = "reclaim"
	PoolNameDedicated       = "dedicated"
	PoolNameReserve         = "reserve"
	PoolNamePrefixIsolation = "isolation"
	PoolNamePrefixSystem    = "system"
	PoolNameInterrupt       = "interrupt"

	EmptyOwnerPoolName = ""

	// PoolNameFallback is not a real pool, and is a union of
	// all none-reclaimed pools to put pod should have been isolated
	PoolNameFallback = "fallback"
)

// FakedContainerName represents a placeholder since pool entry has no container-level
// FakedNUMAID represents a placeholder since pools like shared/reclaimed will not contain a specific numa
const (
	FakedContainerName = ""
	FakedNUMAID        = -1
	NameSeparator      = "#"
	NUMAPoolInfix      = "-NUMA"
)

const (
	PoolNotFoundErrMsg = "pool not found"
)

func IsIsolationPool(poolName string) bool {
	return strings.HasPrefix(poolName, PoolNamePrefixIsolation)
}

func IsSystemPool(poolName string) bool {
	return strings.HasPrefix(poolName, PoolNamePrefixSystem)
}

func GetPoolType(poolName string) string {
	if IsIsolationPool(poolName) {
		return PoolNamePrefixIsolation
	} else if IsSystemPool(poolName) {
		return PoolNamePrefixSystem
	}
	switch poolName {
	case PoolNameReclaim, PoolNameDedicated, PoolNameReserve, PoolNameInterrupt, PoolNameFallback:
		return poolName
	default:
		return PoolNameShare
	}
}

// GetSpecifiedPoolName todo: this function (along with pool-name consts) should be moved to generic qos conf
func GetSpecifiedPoolName(qosLevel, cpusetEnhancementValue string) string {
	switch qosLevel {
	case apiconsts.PodAnnotationQoSLevelSharedCores:
		if cpusetEnhancementValue != EmptyOwnerPoolName {
			return cpusetEnhancementValue
		}
		return PoolNameShare
	case apiconsts.PodAnnotationQoSLevelSystemCores:
		return cpusetEnhancementValue
	case apiconsts.PodAnnotationQoSLevelReclaimedCores:
		return PoolNameReclaim
	case apiconsts.PodAnnotationQoSLevelDedicatedCores:
		return PoolNameDedicated
	default:
		return EmptyOwnerPoolName
	}
}

// GetSpecifiedNUMABindingNUMAID parses the numa id for AllocationInfo
func GetSpecifiedNUMABindingNUMAID(annotations map[string]string) (int, error) {
	if _, ok := annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint]; !ok {
		return FakedNUMAID, nil
	}

	numaSet, pErr := machine.Parse(annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint])
	if pErr != nil {
		return FakedNUMAID, fmt.Errorf("parse numaHintStr: %s failed with error: %v",
			annotations[cpuconsts.CPUStateAnnotationKeyNUMAHint], pErr)
	} else if numaSet.Size() != 1 {
		return FakedNUMAID, fmt.Errorf("parse numaHintStr: %s with invalid size", numaSet.String())
	}

	return numaSet.ToSliceNoSortInt()[0], nil
}

// GetSpecifiedNUMABindingPoolName get numa_binding pool name
// for numa_binding shared_cores according to enhancements and NUMA hint
func GetSpecifiedNUMABindingPoolName(qosLevel string, annotations map[string]string) (string, error) {
	if qosLevel != apiconsts.PodAnnotationQoSLevelSharedCores ||
		annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] != apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable {
		return EmptyOwnerPoolName, fmt.Errorf("GetSpecifiedNUMABindingPoolName is only for numa_binding shared_cores")
	}

	numaID, err := GetSpecifiedNUMABindingNUMAID(annotations)
	if err != nil {
		return EmptyOwnerPoolName, err
	}

	if numaID == FakedNUMAID {
		return EmptyOwnerPoolName, fmt.Errorf("invalid numa id for numa_binding shared_cores")
	}

	specifiedPoolName := GetSpecifiedPoolName(qosLevel, annotations[apiconsts.PodAnnotationCPUEnhancementCPUSet])

	if specifiedPoolName == EmptyOwnerPoolName {
		return EmptyOwnerPoolName, fmt.Errorf("empty specifiedPoolName")
	}

	return GetNUMAPoolName(specifiedPoolName, numaID), nil
}
