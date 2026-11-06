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

package provisionassembler

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// regionInfo is a struct that contains region information
// for share region the key of map is owner pool name
// for dedicated region the key of map is region name
type regionInfo struct {
	requirements              map[string]int
	requests                  map[string]int
	reclaimEnable             map[string]bool
	podSet                    map[string]types.PodSet
	minReclaimedCoresCPUQuota float64
	regionMap                 map[string]region.QoSRegion
}

func (r *regionInfo) merge(other regionInfo) {
	for poolName, size := range other.requirements {
		r.requirements[poolName] = size
	}

	for poolName, size := range other.requests {
		r.requests[poolName] = size
	}

	for poolName, enable := range other.reclaimEnable {
		r.reclaimEnable[poolName] = enable
	}

	for poolName, podSet := range other.podSet {
		r.podSet[poolName] = podSet
	}

	// minReclaimedCoresCPUQuota uses -1 as the "no reclaim quota set" sentinel.
	// Only adopt a real quota from the other side: a sentinel other must never
	// overwrite a quota already accumulated here, otherwise merging a region
	// without reclaim quota would reset the merged quota back to -1.
	if other.minReclaimedCoresCPUQuota != -1 &&
		(r.minReclaimedCoresCPUQuota == -1 || other.minReclaimedCoresCPUQuota < r.minReclaimedCoresCPUQuota) {
		r.minReclaimedCoresCPUQuota = other.minReclaimedCoresCPUQuota
	}

	for poolName, reg := range other.regionMap {
		r.regionMap[poolName] = reg
	}
}

func initRegionInfo() regionInfo {
	return regionInfo{
		requirements:              make(map[string]int),
		requests:                  make(map[string]int),
		reclaimEnable:             make(map[string]bool),
		podSet:                    make(map[string]types.PodSet),
		minReclaimedCoresCPUQuota: -1,
		regionMap:                 make(map[string]region.QoSRegion),
	}
}

func (pa *ProvisionAssemblerCommon) getPinnedCPUSizeByPackage(numaSet machine.CPUSet, cfg types.ResourcePackageConfig) map[string]int {
	pinnedCPUSizeByPkg := make(map[string]int)

	if len(cfg) > 0 {
		for _, numaID := range numaSet.ToSliceInt() {
			pkgMap, ok := cfg[numaID]
			if !ok {
				continue
			}
			for pkgName, state := range pkgMap {
				if state == nil {
					continue
				}
				size := state.PinnedCPUSet.Size()
				if size <= 0 {
					continue
				}
				pinnedCPUSizeByPkg[pkgName] += size
			}
		}
		return pinnedCPUSizeByPkg
	}
	return pinnedCPUSizeByPkg
}

func getEligibilityDomainCapacities(
	numaSet machine.CPUSet,
	cfg types.ResourcePackageConfig,
	pinnedCPUSizeByPkg map[string]int,
	availableBeforeReserve,
	reservedForReclaim int,
	metaReader metacache.MetaReader,
) (map[string]int, map[string]int, int, int) {
	capacityByPkg := general.DeepCopyIntMap(pinnedCPUSizeByPkg)
	totalPinned := general.SumUpMapValues(pinnedCPUSizeByPkg)
	reserveByPkg := make(map[string]int)

	reserveCPUSet := machine.NewCPUSet()
	if reservePool, ok := metaReader.GetPoolInfo(commonstate.PoolNameReserve); ok && reservePool != nil {
		for _, numaID := range numaSet.ToSliceInt() {
			reserveCPUSet = reserveCPUSet.Union(reservePool.TopologyAwareAssignments[numaID])
		}
	}

	reservePinnedCPUSet := machine.NewCPUSet()
	for pkgName := range pinnedCPUSizeByPkg {
		pkgCPUSet := machine.NewCPUSet()
		for _, numaID := range numaSet.ToSliceInt() {
			if state := cfg[numaID][pkgName]; state != nil {
				pkgCPUSet = pkgCPUSet.Union(state.PinnedCPUSet)
			}
		}
		reserveInPkg := reserveCPUSet.Intersection(pkgCPUSet)
		reserveByPkg[pkgName] = reserveInPkg.Size()
		reservePinnedCPUSet = reservePinnedCPUSet.Union(reserveInPkg)
	}

	unpinnedReserve := general.Max(reservedForReclaim-reservePinnedCPUSet.Size(), 0)
	return capacityByPkg,
		reserveByPkg,
		general.Max(availableBeforeReserve-totalPinned, 0),
		unpinnedReserve
}

func extractShareRegionInfo(
	shareRegions []region.QoSRegion,
	pinnedCPUSizeByPkg map[string]int,
	nonReclaimablePackages sets.String,
	metaReader metacache.MetaReader,
) (regionInfo, map[string]*regionInfo, error) {
	unpinnedRegionInfo := initRegionInfo()
	pinnedRegionInfos := make(map[string]*regionInfo)

	for _, r := range shareRegions {
		poolName := r.OwnerPoolName()
		request := r.GetPodsRequest()
		if poolName == commonstate.PoolNameShare && !r.IsNumaBinding() &&
			r.GetResourcePackageName() == "" && request <= 0 {
			hasCPUOwnership := false
			if poolSize, ok := metaReader.GetPoolSize(poolName); ok && poolSize > 0 {
				hasCPUOwnership = true
			}
			if !hasCPUOwnership {
				for podUID, containers := range r.GetPods() {
					for containerName := range containers {
						container, ok := metaReader.GetContainerInfo(podUID, containerName)
						if ok && container != nil &&
							(!container.TopologyAwareAssignments.MergeCPUSet().IsEmpty() ||
								!container.OriginalTopologyAwareAssignments.MergeCPUSet().IsEmpty()) {
							hasCPUOwnership = true
							break
						}
					}
					if hasCPUOwnership {
						break
					}
				}
			}
			if !hasCPUOwnership {
				// A zero-request default non-NUMA share region with no committed
				// CPUs is metadata only. Its containers inherit their parent cpuset
				// and must not keep a zero-sized pool active. Named/NUMA/package
				// pools, a positive request, or any current/original CPU ownership
				// still take the normal path and retain the zero-size safety guard.
				continue
			}
		}

		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		reclaimEnable := r.EnableReclaim()
		if pkgName != "" && nonReclaimablePackages.Has(pkgName) {
			reclaimEnable = false // override reclaim Enable if the resource package is non-reclaimable
		}

		// The one-CPU floor is intentional for every active placement domain.
		// Named pools retain a distinct ownership/isolation identity even when
		// their aggregate request is zero, so they still need a concrete pool
		// entry that QRM can validate and inherit. Only the default non-NUMA,
		// unpinned share region may be filtered above as metadata-only.
		ri.requirements[poolName] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		ri.requests[poolName] = general.Max(1, int(math.Ceil(request)))
		ri.reclaimEnable[poolName] = reclaimEnable
		if reclaimEnable {
			if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
				if ri.minReclaimedCoresCPUQuota == -1 || quota.Value < ri.minReclaimedCoresCPUQuota {
					ri.minReclaimedCoresCPUQuota = quota.Value
				}
			}
		}
		ri.regionMap[poolName] = r
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

func getPoolSizeRequirements(info regionInfo) map[string]int {
	result := make(map[string]int)
	for name, reclaimEnable := range info.reclaimEnable {
		if !reclaimEnable {
			result[name] = info.requests[name]
		} else {
			result[name] = info.requirements[name]
		}
	}
	return result
}

type isolationRegionInfo struct {
	isolationUpperSizes map[string]int
	isolationLowerSizes map[string]int
}

func (r *isolationRegionInfo) merge(other isolationRegionInfo) {
	for poolName, size := range other.isolationUpperSizes {
		r.isolationUpperSizes[poolName] = size
	}

	for poolName, size := range other.isolationLowerSizes {
		r.isolationLowerSizes[poolName] = size
	}
}

func initIsolationRegionInfo() isolationRegionInfo {
	return isolationRegionInfo{
		isolationUpperSizes: make(map[string]int),
		isolationLowerSizes: make(map[string]int),
	}
}

func extractIsolationRegionInfo(isolationRegions []region.QoSRegion, pinnedCPUSizeByPkg map[string]int, _ sets.String) (isolationRegionInfo, map[string]*isolationRegionInfo, error) {
	unpinnedRegionInfo := initIsolationRegionInfo()
	pinnedRegionInfos := make(map[string]*isolationRegionInfo)

	for _, r := range isolationRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return isolationRegionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initIsolationRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		// Isolation region currently doesn't use reclaimEnable in the same way as Share and Dedicated,
		// but we still process it just in case, though it only sets upper/lower sizes.
		ri.isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value)
		ri.isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

func extractDedicatedRegionInfo(regions []region.QoSRegion, pinnedCPUSizeByPkg map[string]int, nonReclaimablePackages sets.String) (regionInfo, map[string]*regionInfo, error) {
	unpinnedRegionInfo := initRegionInfo()
	pinnedRegionInfos := make(map[string]*regionInfo)

	for _, r := range regions {
		if r.IsNumaExclusive() {
			continue
		}

		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, nil, err
		}

		ri := &unpinnedRegionInfo
		pkgName := r.GetResourcePackageName()
		if pkgName != "" {
			if _, ok := pinnedCPUSizeByPkg[pkgName]; ok {
				if _, exists := pinnedRegionInfos[pkgName]; !exists {
					info := initRegionInfo()
					pinnedRegionInfos[pkgName] = &info
				}
				ri = pinnedRegionInfos[pkgName]
			}
		}

		reclaimEnable := r.EnableReclaim()
		if pkgName != "" && nonReclaimablePackages.Has(pkgName) {
			reclaimEnable = false // override reclaim Enable if the resource package is non-reclaimable
		}

		regionName := r.Name()
		ri.requirements[regionName] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		if r.IsNumaBinding() {
			numaBindingSize := r.GetBindingNumas().Size()
			if numaBindingSize == 0 {
				return regionInfo{}, nil, fmt.Errorf("numa binding size is zero, region name: %s", r.Name())
			}
			ri.requests[regionName] = int(math.Ceil(r.GetPodsRequest() / float64(numaBindingSize)))
		} else {
			ri.requests[regionName] = int(math.Ceil(r.GetPodsRequest()))
		}
		ri.reclaimEnable[regionName] = reclaimEnable
		ri.podSet[regionName] = r.GetPods()
		if reclaimEnable {
			if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
				if ri.minReclaimedCoresCPUQuota == -1 || quota.Value < ri.minReclaimedCoresCPUQuota {
					ri.minReclaimedCoresCPUQuota = quota.Value
				}
			}
		}
		ri.regionMap[regionName] = r
	}

	return unpinnedRegionInfo, pinnedRegionInfos, nil
}

type pinnedCPUSetAllRegionInfo struct {
	shareRegionInfo      regionInfo
	isolationRegionInfo  isolationRegionInfo
	dedicatedRegionInfos regionInfo
}

func initPinnedCPUSetAllRegionInfo() *pinnedCPUSetAllRegionInfo {
	return &pinnedCPUSetAllRegionInfo{
		shareRegionInfo:      initRegionInfo(),
		isolationRegionInfo:  initIsolationRegionInfo(),
		dedicatedRegionInfos: initRegionInfo(),
	}
}

func getPinnedCPUSetAllRegionInfo(
	shareRegionInfo map[string]*regionInfo,
	isolationRegionInfo map[string]*isolationRegionInfo,
	dedicatedRegionInfos map[string]*regionInfo,
) map[string]*pinnedCPUSetAllRegionInfo {
	res := make(map[string]*pinnedCPUSetAllRegionInfo)
	for pkgName, info := range shareRegionInfo {
		_, ok := res[pkgName]
		if !ok {
			res[pkgName] = initPinnedCPUSetAllRegionInfo()
		}
		res[pkgName].shareRegionInfo = *info
	}

	// All three incoming maps are keyed by package name (see
	// extractShareRegionInfo / extractIsolationRegionInfo / extractDedicatedRegionInfo),
	// so the results merge under a single pkgName namespace. The loop variables
	// below are pkgNames, not region names.
	for pkgName, info := range isolationRegionInfo {
		if _, ok := res[pkgName]; !ok {
			res[pkgName] = initPinnedCPUSetAllRegionInfo()
		}
		res[pkgName].isolationRegionInfo = *info
	}

	for pkgName, info := range dedicatedRegionInfos {
		if _, ok := res[pkgName]; !ok {
			res[pkgName] = initPinnedCPUSetAllRegionInfo()
		}
		res[pkgName].dedicatedRegionInfos = *info
	}

	return res
}
