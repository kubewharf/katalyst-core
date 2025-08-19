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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func RegisterCPUAdvisorHealthCheck() {
	general.Infof("register CPU advisor health check")
	general.RegisterHeartbeatCheck(cpuAdvisorHealthCheckName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
}

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

func (cra *cpuResourceAdvisor) getContainerRegions(ci *types.ContainerInfo, regionType configapi.QoSRegionType) ([]region.QoSRegion, error) {
	var regions []region.QoSRegion

	// For non-newly allocated containers, they already had regionNames,
	// we can directly get the regions by regionMap.
	for _, r := range cra.getRegionsByRegionNames(ci.RegionNames) {
		if r.Type() == regionType {
			regions = append(regions, r)
		}
	}
	if len(regions) > 0 {
		return regions, nil
	}

	// The regionNames of newly allocated containers are empty, if other containers of the same pod have been assigned regions,
	// we can get regions by pod UID, otherwise create new region.
	for _, r := range cra.getRegionsByPodUID(ci.PodUID) {
		if r.Type() == regionType {
			regions = append(regions, r)
		}
	}
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
		klog.Warningf("pool %s doesn't exist, create a new pool by advisor", poolName)
		return nil
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
	cra.provisionAssembler = initializer(cra.conf, cra.extraConf, &cra.regionMap, &cra.reservedForReclaim, &cra.numaAvailable, &cra.nonBindingNumas, &cra.allowSharedCoresOverlapReclaimedCores, cra.metaCache, cra.metaServer, cra.emitter)

	return nil
}

func (cra *cpuResourceAdvisor) initializeHeadroomAssembler() error {
	assemblerName := cra.conf.CPUAdvisorConfiguration.HeadroomAssembler
	initializers := headroomassembler.GetRegisteredInitializers()

	initializer, ok := initializers[assemblerName]
	if !ok {
		return fmt.Errorf("unsupported headroom assembler %v", assemblerName)
	}
	cra.headroomAssembler = initializer(cra.conf, cra.extraConf, &cra.regionMap, &cra.reservedForReclaim, &cra.numaAvailable, &cra.nonBindingNumas, cra.metaCache, cra.metaServer, cra.emitter)

	return nil
}

// updateNumasAvailableResource updates available resource of all numa nodes.
// available = total - reserved pool - forbidden pool
func (cra *cpuResourceAdvisor) updateNumasAvailableResource() {
	cra.numaAvailable = make(map[int]int)
	reservePoolInfo, _ := cra.metaCache.GetPoolInfo(commonstate.PoolNameReserve)

	forbiddenCPUsMap := make(map[int]int)
	for _, poolName := range state.ForbiddenPools.List() {
		poolInfo, ok := cra.metaCache.GetPoolInfo(poolName)
		if poolInfo == nil || !ok {
			continue
		}
		for numaID, cpuset := range poolInfo.TopologyAwareAssignments {
			forbiddenCPUsMap[numaID] += cpuset.Size()
		}
	}

	cra.updateReservedForReclaim()

	for id := 0; id < cra.metaServer.NumNUMANodes; id++ {
		reservePoolNuma := 0
		if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[id]; ok {
			reservePoolNuma = cpuset.Size()
		}
		forbiddenPoolNuma := 0
		if v, ok := forbiddenCPUsMap[id]; ok {
			forbiddenPoolNuma = v
		}
		cra.numaAvailable[id] = cra.metaServer.NUMAToCPUs.CPUSizeInNUMAs(id) - reservePoolNuma - forbiddenPoolNuma
	}
}

func (cra *cpuResourceAdvisor) updateReservedForReclaim() {
	coreNumReservedForReclaim := cra.conf.GetDynamicConfiguration().MinReclaimedResourceForAllocate[v1.ResourceCPU]
	if coreNumReservedForReclaim.Value() > int64(cra.metaServer.NumCPUs) {
		coreNumReservedForReclaim.Set(int64(cra.metaServer.NumCPUs))
	}

	// make sure coreNumReservedForReclaim >= NumNUMANodes
	if coreNumReservedForReclaim.Value() < int64(cra.metaServer.NumNUMANodes) {
		coreNumReservedForReclaim.Set(int64(cra.metaServer.NumNUMANodes))
	}
	cra.reservedForReclaim = machine.GetCoreNumReservedForReclaim(int(coreNumReservedForReclaim.Value()), cra.metaServer.NumNUMANodes)
}

func (cra *cpuResourceAdvisor) getNumasReservedForAllocate(numas machine.CPUSet) float64 {
	reserved := cra.conf.GetDynamicConfiguration().ReservedResourceForAllocate[v1.ResourceCPU]
	return float64(reserved.Value()*int64(numas.Size())) / float64(cra.metaServer.NumNUMANodes)
}

func (cra *cpuResourceAdvisor) getRegionMaxRequirement(r region.QoSRegion) float64 {
	res := 0.0
	switch r.Type() {
	case configapi.QoSRegionTypeIsolation:
		cra.metaCache.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
			if _, ok := r.GetPods()[podUID]; ok {
				if ci.ContainerType == v1alpha1.ContainerType_MAIN || cra.conf.IsolationIncludeSidecarRequirement {
					// for pods without limits, fallback to requests instead
					res += general.MaxFloat64(ci.CPULimit, ci.CPURequest)
				}
			}
			return true
		})
		res = general.MaxFloat64(1, res)
	default:
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			res += float64(cra.numaAvailable[numaID] - cra.reservedForReclaim[numaID])
		}
	}
	return res
}

func (cra *cpuResourceAdvisor) getRegionMinRequirement(r region.QoSRegion) float64 {
	switch r.Type() {
	case configapi.QoSRegionTypeShare:
		return types.MinShareCPURequirement
	case configapi.QoSRegionTypeIsolation:
		res := 0.0
		cra.metaCache.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
			if _, ok := r.GetPods()[podUID]; ok {
				if ci.ContainerType == v1alpha1.ContainerType_MAIN || cra.conf.IsolationIncludeSidecarRequirement {
					// todo: to be compatible with resource over-commit,
					//  set lower-bound as limit too, but we need to reconsider this in the future
					res += general.MaxFloat64(ci.CPULimit, ci.CPURequest)
				}
			}
			return true
		})
		res = general.MaxFloat64(1, res)
		return res
	case configapi.QoSRegionTypeDedicatedNumaExclusive:
		return types.MinDedicatedCPURequirement
	default:
		klog.Errorf("[qosaware-cpu] unknown region type %v", r.Type())
		return 0.0
	}
}

func (cra *cpuResourceAdvisor) getRegionReservedForReclaim(r region.QoSRegion) float64 {
	res := 0.0
	for _, numaID := range r.GetBindingNumas().ToSliceInt() {
		res += float64(cra.reservedForReclaim[numaID])
	}
	return res
}

func (cra *cpuResourceAdvisor) getRegionReservedForAllocate(r region.QoSRegion) float64 {
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

func (cra *cpuResourceAdvisor) updateRegionEntries() {
	entries := make(types.RegionEntries)
	for regionName, r := range cra.regionMap {
		regionInfo := &types.RegionInfo{
			RegionName:    r.Name(),
			RegionType:    r.Type(),
			OwnerPoolName: r.OwnerPoolName(),
			BindingNumas:  r.GetBindingNumas(),
			Pods:          r.GetPods(),
		}

		if r.Type() == configapi.QoSRegionTypeShare || r.Type() == configapi.QoSRegionTypeDedicatedNumaExclusive {
			headroom, err := r.GetHeadroom()
			if err != nil {
				general.ErrorS(err, "failed to get region headroom", "regionName", r.Name())
				headroom = types.InvalidHeadroom
			}
			regionInfo.Headroom = headroom
			regionInfo.HeadroomPolicyTopPriority, regionInfo.HeadroomPolicyInUse = r.GetHeadRoomPolicy()

			controlKnobMap, err := r.GetProvision()
			if err != nil {
				controlKnobMap = types.InvalidControlKnob
				general.ErrorS(err, "failed to get region provision", "regionName", r.Name())
			}
			regionInfo.ControlKnobMap = controlKnobMap
			regionInfo.ProvisionPolicyTopPriority, regionInfo.ProvisionPolicyInUse = r.GetProvisionPolicy()
		}

		entries[regionName] = regionInfo

		general.InfoS("region info", "info", regionInfo)
	}

	_ = cra.metaCache.SetRegionEntries(entries)
}

func (cra *cpuResourceAdvisor) updateRegionStatus() {
	for regionName, r := range cra.regionMap {
		r.UpdateStatus()
		regionInfo, ok := cra.metaCache.GetRegionInfo(regionName)
		if !ok {
			continue
		}

		status := r.GetStatus()
		regionInfo.RegionStatus = status
		_ = cra.metaCache.SetRegionInfo(regionName, regionInfo)
	}
}
