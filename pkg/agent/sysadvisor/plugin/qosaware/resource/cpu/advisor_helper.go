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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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

func (cra *cpuResourceAdvisor) initializeProvisionAssembler() error {
	assemblerName := cra.conf.CPUAdvisorConfiguration.ProvisionAssembler
	initializers := provisionassembler.GetRegisteredInitializers()

	initializer, ok := initializers[assemblerName]
	if !ok {
		return fmt.Errorf("unsupported provision assembler %v", assemblerName)
	}
	cra.provisionAssembler = initializer(cra.conf, &cra.regionMap, &cra.reservedForReclaim, &cra.numaAvailable, &cra.nonBindingNumas, cra.metaCache, cra.metaServer, cra.emitter)

	return nil
}

func (cra *cpuResourceAdvisor) initializeHeadroomAssembler() error {
	assemblerName := cra.conf.CPUAdvisorConfiguration.HeadroomAssembler
	initializers := headroomassembler.GetRegisteredInitializers()

	initializer, ok := initializers[assemblerName]
	if !ok {
		return fmt.Errorf("unsupported headroom assembler %v", assemblerName)
	}
	cra.headroomAssembler = initializer(cra.conf, &cra.regionMap, cra.metaCache, cra.metaServer, cra.emitter)

	return nil
}

// initializeReservedForReclaim generates per numa reserved for reclaim resource value map.
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

// updateNumasAvailableResource updates available resource of all numa nodes.
// available = total - reserved pool - reserved for reclaim
func (cra *cpuResourceAdvisor) updateNumasAvailableResource() {
	cra.numaAvailable = make(map[int]int)
	reservePoolInfo, _ := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	cpusPerNuma := cra.metaServer.CPUsPerNuma()

	for id := 0; id < cra.metaServer.NumNUMANodes; id++ {
		reservePoolNuma := 0
		if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[id]; ok {
			reservePoolNuma = cpuset.Size()
		}
		reservedForReclaimNuma := 0
		if v, ok := cra.reservedForReclaim[id]; ok {
			reservedForReclaimNuma = v
		}
		cra.numaAvailable[id] = cpusPerNuma - reservePoolNuma - reservedForReclaimNuma
	}
}

func (cra *cpuResourceAdvisor) getNumasReservedForAllocate(numas machine.CPUSet) float64 {
	reserved := cra.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]
	return float64(reserved.Value()*int64(numas.Size())) / float64(cra.metaServer.NumNUMANodes)
}

func (cra *cpuResourceAdvisor) getRegionMaxRequirement(regionName string) float64 {
	r, ok := cra.regionMap[regionName]
	if !ok {
		return 0
	}

	res := 0.0
	switch r.Type() {
	case types.QoSRegionTypeIsolation:
		cra.metaCache.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
			if _, ok := r.GetPods()[podUID]; ok && ci.ContainerType == v1alpha1.ContainerType_MAIN {
				res += ci.CPULimit
			}
			return true
		})
	default:
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			res += float64(cra.numaAvailable[numaID])
		}
	}
	return res
}

func (cra *cpuResourceAdvisor) getRegionMinRequirement(regionName string) float64 {
	r, ok := cra.regionMap[regionName]
	if !ok {
		return 0
	}

	switch r.Type() {
	case types.QoSRegionTypeShare:
		return types.MinShareCPURequirement
	case types.QoSRegionTypeIsolation:
		res := 0.0
		cra.metaCache.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
			if _, ok := r.GetPods()[podUID]; ok && ci.ContainerType == v1alpha1.ContainerType_MAIN {
				res += ci.CPURequest
			}
			return true
		})
		return res
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

func (cra *cpuResourceAdvisor) setRegionEntries() {
	entries := make(types.RegionEntries)

	for regionName, r := range cra.regionMap {
		regionInfo := &types.RegionInfo{
			RegionType:   r.Type(),
			BindingNumas: r.GetBindingNumas(),
		}
		entries[regionName] = regionInfo
	}

	_ = cra.metaCache.SetRegionEntries(entries)
}

func (cra *cpuResourceAdvisor) updateRegionProvision() {
	for regionName, r := range cra.regionMap {
		regionInfo, ok := cra.metaCache.GetRegionInfo(regionName)
		if !ok {
			continue
		}

		controlKnobMap, err := r.GetProvision()
		if err != nil {
			continue
		}
		regionInfo.ControlKnobMap = controlKnobMap
		regionInfo.ProvisionPolicyTopPriority, regionInfo.ProvisionPolicyInUse = r.GetProvisionPolicy()

		_ = cra.metaCache.SetRegionInfo(regionName, regionInfo)
	}
}

func (cra *cpuResourceAdvisor) updateRegionHeadroom() {
	for regionName, r := range cra.regionMap {
		regionInfo, ok := cra.metaCache.GetRegionInfo(regionName)
		if !ok {
			continue
		}

		headroom, err := r.GetHeadroom()
		if err != nil {
			continue
		}
		regionInfo.Headroom = headroom
		regionInfo.HeadroomPolicyTopPriority, regionInfo.HeadroomPolicyInUse = r.GetHeadRoomPolicy()

		_ = cra.metaCache.SetRegionInfo(regionName, regionInfo)
	}
}
