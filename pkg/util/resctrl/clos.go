/*
Copyright 2026 The Katalyst Authors.

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

package resctrl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type ClosAssignmentMeta struct {
	QoSLevel  string
	OwnerPool string
}

var numericSharedClosPattern = regexp.MustCompile(`^shared?-\d+$`)

// SharedSubgroupClosID returns the physical CLOS for an explicitly configured
// subgroup of a non-default shared-core pool.
func SharedSubgroupClosID(subgroup int) string {
	if subgroup < 0 {
		return consts.ResctrlGroupShare
	}
	return fmt.Sprintf("%s%02d", consts.ResctrlShareSubgroupPrefix, subgroup)
}

func NormalizeClosID(closID string) string {
	// "shared-50" is the canonical physical CLOS of the default share pool.
	// Other "shared-*" names are legacy aliases for explicit "share-*" subgroups.
	if closID == consts.ResctrlGroupDefaultShare {
		return closID
	}
	if strings.HasPrefix(closID, consts.ResctrlObsoleteSharedSubgroupPrefix) {
		return consts.ResctrlShareSubgroupPrefix + strings.TrimPrefix(closID, consts.ResctrlObsoleteSharedSubgroupPrefix)
	}
	return closID
}

func ResolveSharedPoolClosID(poolName string, config *qrmresctrl.ResctrlConfig) string {
	if IsExplicitSharedPoolMapping(poolName, config) {
		return SharedSubgroupClosID(config.CPUSetPoolToSharedSubgroup[poolName])
	}
	// Unmapped shared-core pools intentionally join the default shared CLOS.
	return consts.ResctrlGroupDefaultShare
}

func ResolvePoolClosID(meta ClosAssignmentMeta, config *qrmresctrl.ResctrlConfig) (string, error) {
	switch meta.QoSLevel {
	case apiconsts.PodAnnotationQoSLevelSharedCores:
		return ResolveSharedPoolClosID(meta.OwnerPool, config), nil
	case apiconsts.PodAnnotationQoSLevelDedicatedCores:
		return consts.ResctrlGroupDedicated, nil
	case apiconsts.PodAnnotationQoSLevelReclaimedCores:
		return consts.ResctrlGroupReclaim, nil
	case apiconsts.PodAnnotationQoSLevelSystemCores:
		return consts.ResctrlGroupSystem, nil
	default:
		if meta.OwnerPool == "" {
			return "", fmt.Errorf("empty owner pool for qos %q", meta.QoSLevel)
		}
		return NormalizeClosID(meta.OwnerPool), nil
	}
}

func BuildExpectedClosPools(metas []ClosAssignmentMeta, config *qrmresctrl.ResctrlConfig) (map[string][]string, error) {
	poolsByClos := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, meta := range metas {
		closID, err := ResolvePoolClosID(meta, config)
		if err != nil {
			return nil, err
		}
		source := meta.OwnerPool
		if source == "" {
			source = meta.QoSLevel
		}
		if seen[closID] == nil {
			seen[closID] = make(map[string]struct{})
		}
		if _, ok := seen[closID][source]; ok {
			continue
		}
		seen[closID][source] = struct{}{}
		poolsByClos[closID] = append(poolsByClos[closID], source)
	}
	for closID := range poolsByClos {
		sort.Strings(poolsByClos[closID])
	}
	return poolsByClos, nil
}

func ResolveCATWayKey(key string, config *qrmresctrl.ResctrlConfig) string {
	// "share" is a logical CAT key for the fixed default shared CLOS.
	// "share-50" remains a distinct explicit subgroup CLOS.
	if key == consts.ResctrlGroupShare {
		return consts.ResctrlGroupDefaultShare
	}
	if IsReservedPhysicalClosID(key) {
		return NormalizeClosID(key)
	}
	if IsExplicitSharedPoolMapping(key, config) {
		return SharedSubgroupClosID(config.CPUSetPoolToSharedSubgroup[key])
	}
	return NormalizeClosID(key)
}

func IsReservedPhysicalClosID(key string) bool {
	switch key {
	case consts.ResctrlGroupDedicated, consts.ResctrlGroupReclaim, consts.ResctrlGroupSystem,
		consts.ResctrlGroupDefaultShare:
		return true
	default:
		return numericSharedClosPattern.MatchString(key)
	}
}

func IsExplicitSharedPoolMapping(poolName string, config *qrmresctrl.ResctrlConfig) bool {
	if config == nil || poolName == consts.ResctrlGroupShare || IsReservedPhysicalClosID(poolName) {
		return false
	}
	subgroup, ok := config.CPUSetPoolToSharedSubgroup[poolName]
	return ok && subgroup >= 0
}

func IsManagedClosID(closID string, config *qrmresctrl.ResctrlConfig) bool {
	closID = NormalizeClosID(closID)
	managed := map[string]struct{}{
		consts.ResctrlGroupDedicated:    {},
		consts.ResctrlGroupReclaim:      {},
		consts.ResctrlGroupSystem:       {},
		consts.ResctrlGroupDefaultShare: {},
	}
	if config != nil {
		for _, defaultClosID := range config.DefaultClosIDs {
			managed[NormalizeClosID(defaultClosID)] = struct{}{}
		}
		for poolName, subgroup := range config.CPUSetPoolToSharedSubgroup {
			// Ignore invalid explicit mappings defensively. Static option validation rejects
			// the default share pool and negative subgroup IDs during normal startup, while
			// direct config construction must not add unreachable CLOS groups.
			if !IsExplicitSharedPoolMapping(poolName, config) {
				continue
			}
			managed[SharedSubgroupClosID(subgroup)] = struct{}{}
		}
	}
	_, ok := managed[closID]
	return ok
}

func IsCPUListManagedClosID(closID string, config *qrmresctrl.ResctrlConfig) bool {
	closID = NormalizeClosID(closID)
	if closID == consts.ResctrlGroupDedicated || closID == consts.ResctrlGroupDefaultShare {
		return true
	}
	if config == nil {
		return false
	}
	for poolName, subgroup := range config.CPUSetPoolToSharedSubgroup {
		if !IsExplicitSharedPoolMapping(poolName, config) {
			continue
		}
		if closID == SharedSubgroupClosID(subgroup) {
			return true
		}
	}
	return false
}
