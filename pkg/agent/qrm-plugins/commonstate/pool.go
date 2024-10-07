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
	"strings"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
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
	case PoolNameReclaim, PoolNameDedicated, PoolNameReserve, PoolNameFallback:
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
