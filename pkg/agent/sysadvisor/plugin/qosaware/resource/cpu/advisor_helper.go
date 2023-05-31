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

package cpu

import (
	"fmt"
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

func (cra *cpuResourceAdvisor) getRegionsByRegionNames(names sets.String) []region.QoSRegion {
	var regions []region.QoSRegion = nil
	for regionName := range names {
		r, ok := cra.regionMap[regionName]
		if !ok {
			return nil
		}
		regions = append(regions, r)
	}
	return regions
}

func (cra *cpuResourceAdvisor) getRegionsByPodUID(podUID string) []region.QoSRegion {
	var regions []region.QoSRegion = nil
	for _, r := range cra.regionMap {
		podSet := r.GetPods()
		for uid := range podSet {
			if uid == podUID {
				regions = append(regions, r)
			}
		}
	}
	return regions
}

func (cra *cpuResourceAdvisor) getContainerRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	// For non-newly allocated containers, they already had regionNames,
	// we can directly get the regions by regionMap.
	regions := cra.getRegionsByRegionNames(ci.RegionNames)
	if len(regions) > 0 {
		return regions, nil
	}

	// The regionNames of newly allocated containers are empty, if other containers of the same pod have been assigned regions,
	// we can get regions by pod UID, otherwise create new region.
	regions = cra.getRegionsByPodUID(ci.PodUID)
	return regions, nil
}

func (cra *cpuResourceAdvisor) setContainerRegions(ci *types.ContainerInfo, regions []region.QoSRegion) {
	ci.RegionNames = sets.NewString()
	for _, r := range regions {
		ci.RegionNames.Insert(r.Name())
	}
}

func (cra *cpuResourceAdvisor) getPoolRegions(poolName string) []region.QoSRegion {
	pool, ok := cra.metaCache.GetPoolInfo(poolName)
	if !ok || pool == nil {
		return nil
	}

	var regions []region.QoSRegion = nil
	for regionName := range pool.RegionNames {
		r, ok := cra.regionMap[regionName]
		if !ok {
			return nil
		}
		regions = append(regions, r)
	}
	return regions
}

func (cra *cpuResourceAdvisor) setPoolRegions(poolName string, regions []region.QoSRegion) error {
	pool, ok := cra.metaCache.GetPoolInfo(poolName)
	if !ok {
		return fmt.Errorf("failed to find pool %v", poolName)
	}

	pool.RegionNames = sets.NewString()
	for _, r := range regions {
		pool.RegionNames.Insert(r.Name())
	}
	return cra.metaCache.SetPoolInfo(poolName, pool)
}

// initializeReservedForReclaim generates per numa reserved for reclaim resource map.
// per numa reserved resource is taken in a fair way with even step, e.g.
// 4 -> 1 1 1 1; 2 -> 1 0 1 0
func (cra *cpuResourceAdvisor) initializeReservedForReclaim() {
	reservedTotal := types.ReservedForReclaim
	numNumaNodes := cra.metaServer.NumNUMANodes
	reservedPerNuma := reservedTotal / numNumaNodes
	step := numNumaNodes / reservedTotal

	if reservedPerNuma < 1 {
		reservedPerNuma = 1
	}
	if step < 1 {
		step = 1
	}

	cra.reservedForReclaim = make(map[int]int)
	for id := 0; id < numNumaNodes; id++ {
		if id%step == 0 {
			cra.reservedForReclaim[id] = reservedPerNuma
		} else {
			cra.reservedForReclaim[id] = 0
		}
	}
}

// getNumasReservedForAllocate returns reserved resource for allocate of corresponding numas
func (cra *cpuResourceAdvisor) getNumasReservedForAllocate(numas machine.CPUSet) float64 {
	reserved := cra.conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate()[v1.ResourceCPU]
	return float64(reserved.Value()*int64(numas.Size())) / float64(cra.metaServer.NumNUMANodes)
}

// getNumasAvailableResource returns available resource of corresponding numas.
// available = total - reserved pool - reserved for reclaim
func (cra *cpuResourceAdvisor) getNumasAvailableResource(numas machine.CPUSet) float64 {
	reservePoolInfo, _ := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	cpusPerNuma := cra.metaServer.CPUsPerNuma()
	res := 0.0

	for _, numaID := range numas.ToSliceInt() {
		reservePoolNuma := 0
		if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[numaID]; ok {
			reservePoolNuma = cpuset.Size()
		}
		reservedForReclaimNuma := 0
		if v, ok := cra.reservedForReclaim[numaID]; ok {
			reservedForReclaimNuma = v
		}
		res += float64(cpusPerNuma - reservePoolNuma - reservedForReclaimNuma)
	}
	return res
}

func (cra *cpuResourceAdvisor) getRegionMaxRequirement(regionName string) float64 {
	r, ok := cra.regionMap[regionName]
	if !ok {
		return 0
	}

	return cra.getNumasAvailableResource(r.GetBindingNumas())
}

func (cra *cpuResourceAdvisor) getRegionMinRequirement(regionName string) float64 {
	r, ok := cra.regionMap[regionName]
	if !ok {
		return 0
	}

	switch r.Type() {
	case types.QoSRegionTypeShare:
		return types.MinShareCPURequirement
	case types.QoSRegionTypeDedicatedNumaExclusive:
		return types.MinDedicatedCPURequirement
	default:
		klog.Errorf("[qosaware-cpu] unknown region type %v", r.Type())
		return 0
	}
}

func (cra *cpuResourceAdvisor) getRegionReservedForAllocate(regionName string) float64 {
	r, ok := cra.regionMap[regionName]
	if !ok {
		return 0
	}

	res := 0.0
	for _, numaID := range r.GetBindingNumas().ToSliceInt() {
		divider := cra.numRegionsPerNuma[numaID]
		if divider < 1 {
			divider = 1
		}
		res += cra.getNumasReservedForAllocate(machine.NewCPUSet(numaID)) / float64(divider)
	}
	return res
}

// regulate pool sizes modifies pool size map to legal values, taking total available resource
// and enable reclaim config into account. should hold any cases and not return error.
func regulatePoolSizes(poolSizes map[string]int, available int, enableReclaim bool) {
	sum := general.SumUpMapValues(poolSizes)
	targetSum := sum

	if !enableReclaim || sum > available {
		// use up all available resource for pools in this case
		targetSum = available
	}

	if err := normalizePoolSizes(poolSizes, targetSum); err != nil {
		// all pools share available resource as fallback if normalization failed
		for k := range poolSizes {
			poolSizes[k] = available
		}
	}
}

func normalizePoolSizes(poolSizes map[string]int, targetSum int) error {
	sum := general.SumUpMapValues(poolSizes)
	if sum == targetSum {
		return nil
	}

	sorted := general.TraverseMapByValueDescending(poolSizes)
	normalizedSum := 0

	for _, v := range sorted {
		v.Value = int(math.Ceil(float64(v.Value*targetSum) / float64(sum)))
		normalizedSum += v.Value
	}

	for i := 0; i < normalizedSum-targetSum; i++ {
		if sorted[i].Value <= 1 {
			return fmt.Errorf("no enough resource")
		}
		sorted[i].Value -= 1
	}

	for _, v := range sorted {
		poolSizes[v.Key] = v.Value
	}
	return nil
}

// assembleRegionEntries generates region entries based on region map
func (cra *cpuResourceAdvisor) assembleRegionEntries() (types.RegionEntries, error) {
	entries := make(types.RegionEntries)

	for regionName, r := range cra.regionMap {
		controlKnobMap, err := r.GetProvision()
		if err != nil {
			return nil, err
		}

		regionInfo := &types.RegionInfo{
			RegionType:     r.Type(),
			BindingNumas:   r.GetBindingNumas(),
			ControlKnobMap: controlKnobMap,
		}
		regionInfo.HeadroomPolicyTopPriority, regionInfo.HeadroomPolicyInUse = r.GetHeadRoomPolicy()
		regionInfo.ProvisionPolicyTopPriority, regionInfo.ProvisionPolicyInUse = r.GetProvisionPolicy()

		headroom, err := r.GetHeadroom()
		if err != nil {
			klog.Warningf("[qosaware-cpu] get headroom for region %v failed: %v", regionName, err)
		} else {
			regionInfo.Headroom = headroom
		}

		entries[regionName] = regionInfo
	}

	return entries, nil
}
