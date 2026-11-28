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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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
	// The provision assembler no longer receives the advisor's live maps at
	// construction: those arrive per-cycle inside ProvisionContext.Snapshot, built
	// by assembleProvision. The initializer only captures long-lived dependencies.
	cra.provisionAssembler = initializer(cra.conf, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)

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
func (cra *cpuResourceAdvisor) updateNumasAvailableResource(
	dynamicConf *dynamic.Configuration,
	hardActive bool,
	steadyExclusiveNUMAs sets.Int,
	rampUpDomains sets.Int,
) error {
	numaAvailable := make(map[int]int)
	reservePoolInfo, _ := cra.metaCache.GetPoolInfo(commonstate.PoolNameReserve)
	numaIDs := cra.metaServer.CPUDetails.NUMANodes().ToSliceInt()

	forbiddenCPUsMap := make(map[int]int)
	cra.metaCache.RangePool(func(poolName string, poolInfo *types.PoolInfo) bool {
		if poolInfo == nil {
			return true
		}
		if !state.ForbiddenPools.Has(poolName) && !commonstate.IsSystemPool(poolName) {
			return true
		}
		for numaID, cpuset := range poolInfo.TopologyAwareAssignments {
			forbiddenCPUsMap[numaID] += cpuset.Size()
		}
		return true
	})

	for _, id := range numaIDs {
		reservePoolNuma := 0
		if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[id]; ok {
			reservePoolNuma = cpuset.Size()
		}
		forbiddenPoolNuma := 0
		if v, ok := forbiddenCPUsMap[id]; ok {
			forbiddenPoolNuma = v
		}
		numaAvailable[id] = cra.metaServer.NUMAToCPUs.CPUSizeInNUMAs(id) - reservePoolNuma - forbiddenPoolNuma
	}

	if dynamicConf != nil && dynamicConf.EnableRampUpReclaimHardPartition {
		for _, id := range numaIDs {
			if numaAvailable[id] < 2 {
				general.Warningf("NUMA %d has %d available CPUs; QRM ramp-up reclaim hard partition may reject admission",
					id, numaAvailable[id])
			}
		}
	}

	cra.numaAvailable = numaAvailable
	if err := cra.updateReservedForReclaim(dynamicConf); err != nil {
		cra.rampUpReclaimCPUSetCap = make(map[int]int)
		return err
	}
	return cra.updateRampUpReclaimCPUSetCap(dynamicConf, hardActive, steadyExclusiveNUMAs, rampUpDomains)
}

func (cra *cpuResourceAdvisor) updateReservedForReclaim(dynamicConf *dynamic.Configuration) error {
	if dynamicConf == nil {
		cra.reservedForReclaim = nil
		return fmt.Errorf("dynamic configuration is nil")
	}

	cra.reservedForReclaim = machine.ResolvePerNUMAReservedForReclaim(dynamicConf, cra.metaServer.CPUTopology)
	numaReservedRatio := dynamicConf.NumaMinReclaimedResourceRatioForAllocate[v1.ResourceCPU]
	numaReserved := dynamicConf.NumaMinReclaimedResourceForAllocate[v1.ResourceCPU]
	globalReserved := dynamicConf.MinReclaimedResourceForAllocate[v1.ResourceCPU]
	general.Infof("reservedForReclaim: %v, numaReservedRatio %v, numaReserved %v, globalReserved %v",
		cra.reservedForReclaim,
		numaReservedRatio.AsApproximateFloat64(),
		numaReserved.AsApproximateFloat64(),
		globalReserved.Value())
	return nil
}

func (cra *cpuResourceAdvisor) updateRampUpReclaimCPUSetCap(
	dynamicConf *dynamic.Configuration,
	hardActive bool,
	steadyExclusiveNUMAs sets.Int,
	rampUpDomains sets.Int,
) error {
	targets := make(map[int]int)
	domainTargets := make(map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget)
	if dynamicConf == nil || !dynamicConf.EnableReclaim ||
		!dynamicConf.EnableRampUpReclaimHardPartition || !hardActive {
		cra.rampUpReclaimCPUSetCap = targets
		cra.rampUpDomainTargets = domainTargets
		return nil
	}

	cpusPerCore := cra.cpusPerCore()
	maxRatio := dynamicConf.ReclaimedCPUMaxRatio

	// Real-NUMA partition domains: each real NUMA that hosts an active ramp-up
	// source keeps its own per-NUMA hard target. The global domain (FakedNUMAID/-1)
	// is handled separately below because its capacity is the AGGREGATE of all
	// non-binding NUMAs, not a single NUMA.
	activeRealNUMAs := sets.NewInt()
	for numaID := range rampUpDomains {
		if numaID == commonstate.FakedNUMAID {
			continue
		}
		activeRealNUMAs.Insert(numaID)
	}
	if activeRealNUMAs.Len() > 0 {
		resolved, err := machine.ResolveHardPartitionReclaimTargets(
			dynamicConf,
			cra.metaServer.CPUTopology,
			0,
			func(numaID int) int { return cra.reservedForReclaim[numaID] },
			nil,
		)
		if err != nil {
			cra.rampUpReclaimCPUSetCap = make(map[int]int)
			cra.rampUpDomainTargets = domainTargets
			return fmt.Errorf("resolve active ramp-up reclaim targets: %w", err)
		}
		// Keep only the NUMAs that host an active ramp-up source; every other
		// NUMA keeps its steady reserve and must not inherit a foreign target.
		for numaID := range resolved {
			if activeRealNUMAs.Has(numaID) {
				targets[numaID] = resolved[numaID]
				scope := cputypes.NewNonExclusiveReclaimConstraintScope(numaID)
				cap := 0
				if cra.metaServer != nil && cra.metaServer.CPUDetails != nil {
					cap = cra.metaServer.CPUDetails.CPUsInNUMANodes(numaID).Size()
				}
				steadyCap, _ := machine.CalculateAggregateRampUpTarget(cap, maxRatio, cpusPerCore)
				snDesired := clampDescriptorDesired(resolved[numaID], steadyCap, cra.reservedForReclaim[numaID])
				domainTargets[scope] = types.ReclaimConstraintTarget{
					Desired:     snDesired,
					Floor:       cra.reservedForReclaim[numaID],
					SteadyCap:   steadyCap,
					MemberNUMAs: []int{numaID},
				}
			}
		}
	}
	// A steady exclusive DNB has already finalized its NUMA partition. Keep that
	// NUMA at the steady reserve even while another NUMA activates the ramp-up
	// hard target.
	for numaID := range steadyExclusiveNUMAs {
		delete(targets, numaID)
	}

	// Global (faked) domain: a non-NUMA-binding ramp-up aggregates reclaim over
	// every non-binding NUMA. The target is derived from the AGGREGATE member
	// capacity first (whole-core rounding on the aggregate, never per-NUMA
	// rounding then summing) and then distributed back across the member NUMAs as
	// complete cores. It only writes onto non-binding NUMAs, so a global ramp-up
	// cannot leak a reservation onto a dedicated-binding NUMA.
	if rampUpDomains.Has(commonstate.FakedNUMAID) {
		globalTargets, desc, err := cra.resolveGlobalRampUpTarget(dynamicConf, maxRatio)
		if err != nil {
			// Fail closed: leave the global domain without a ramp-up target
			// rather than widening reservation across the node. The scope is not
			// given a descriptor, so the guard leaves its ceiling nil/unconstrained
			// and the pool keeps its steady size.
			general.Warningf("rampUpReclaimCPUSetCap: skip global domain target: %v", err)
			if cra.emitter != nil {
				_ = cra.emitter.StoreInt64(metricReclaimScopeTargetError, 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "scope", Val: string(cputypes.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID))})
			}
		} else {
			for numaID, size := range globalTargets {
				targets[numaID] = size
			}
			scope := cputypes.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)
			domainTargets[scope] = types.ReclaimConstraintTarget{
				Desired:     desc.DesiredTarget,
				Floor:       desc.ReserveFloor,
				SteadyCap:   desc.SteadyCap,
				MemberNUMAs: desc.MemberNUMAs,
			}
		}
	}

	cra.rampUpReclaimCPUSetCap = targets
	cra.rampUpDomainTargets = domainTargets
	general.Infof("rampUpReclaimCPUSetCap: %v, ratio %v", targets, dynamicConf.InitialRampUpReclaimCPUSetRatio)
	return nil
}

// clampDescriptorDesired reconciles the ramp-up desired with the steady upper
// bound and the pure floor: desired = min(desired, steadyCap), then max(floor).
func clampDescriptorDesired(desired, steadyCap, floor int) int {
	if steadyCap > 0 && desired > steadyCap {
		desired = steadyCap
	}
	if desired < floor {
		desired = floor
	}
	return desired
}

// resolveGlobalRampUpTarget derives the per-NUMA ramp-up reclaim shares for the
// global (faked) domain and the full domain descriptor (desired, steady upper
// bound, pure floor, member NUMAs). Member NUMAs are exactly the non-binding
// NUMAs; their logical capacities are aggregated, the whole-core-floor target is
// computed on that aggregate with the shared domain target algorithm, and the
// result is distributed back across members as complete cores (summing exactly
// to the aggregate target). A missing topology / empty member set fails closed.
func (cra *cpuResourceAdvisor) resolveGlobalRampUpTarget(
	dynamicConf *dynamic.Configuration,
	maxRatio float64,
) (map[int]int, machine.RampUpDomainDescriptor, error) {
	desc := machine.RampUpDomainDescriptor{}
	if cra.metaServer == nil || cra.metaServer.CPUTopology == nil || cra.metaServer.CPUDetails == nil {
		return nil, desc, fmt.Errorf("meta server topology unavailable")
	}
	memberNUMAs := cra.nonBindingNumas
	if memberNUMAs.IsEmpty() {
		return nil, desc, fmt.Errorf("global ramp-up domain has no non-binding NUMAs")
	}
	cpusPerCore := cra.metaServer.CPUTopology.CPUsPerCore()
	memberIDs := memberNUMAs.ToSliceInt()
	desc.MemberNUMAs = memberIDs

	capacityByNUMA := make(map[int]int, len(memberIDs))
	aggregateCapacity := 0
	reserveFloor := 0
	for _, numaID := range memberIDs {
		cap := cra.metaServer.CPUDetails.CPUsInNUMANodes(numaID).Size()
		capacityByNUMA[numaID] = cap
		aggregateCapacity += cap
		reserveFloor += cra.reservedForReclaim[numaID]
	}
	desc.ReserveFloor = reserveFloor

	// steadyCap = coreAligned(aggregateCapacity * MaxRatio); MaxRatio only caps.
	steadyCap, err := machine.CalculateAggregateRampUpTarget(aggregateCapacity, maxRatio, cpusPerCore)
	if err != nil {
		return nil, desc, fmt.Errorf("global ramp-up steady cap: %w", err)
	}
	desc.SteadyCap = steadyCap

	target, err := machine.CalculateAggregateRampUpTarget(
		aggregateCapacity, dynamicConf.InitialRampUpReclaimCPUSetRatio, cpusPerCore)
	if err != nil {
		return nil, desc, fmt.Errorf("global ramp-up aggregate target: %w", err)
	}
	desc.DesiredTarget = clampDescriptorDesired(target, steadyCap, reserveFloor)
	if desc.DesiredTarget == 0 {
		return map[int]int{}, desc, nil
	}

	baselineByNUMA := make(map[int]int, len(memberIDs))
	for _, numaID := range memberIDs {
		baselineByNUMA[numaID] = cra.reservedForReclaim[numaID]
	}
	distributed, err := machine.DistributeDomainTarget(desc.DesiredTarget, capacityByNUMA, baselineByNUMA, cpusPerCore)
	if err != nil {
		return nil, desc, fmt.Errorf("global ramp-up distribute: %w", err)
	}
	return distributed, desc, nil
}

func (cra *cpuResourceAdvisor) getNumasReservedForAllocate(dynamicConf *dynamic.Configuration, numas machine.CPUSet) float64 {
	reserved := dynamicConf.ReservedResourceForAllocate[v1.ResourceCPU]
	return float64(reserved.Value()*int64(numas.Size())) / float64(cra.metaServer.NumNUMANodes)
}

// getEffectiveReservedForReclaim returns the pure steady reservation floor for a
// NUMA. It is never raised by the ramp-up hard target: the ramp-up target is the
// desired value tracked by the optional per-scope ceiling, not a floor. Raising
// the floor here would pin the reclaim pool to the ramp-up target and bypass the
// slow ceiling state machine.
func (cra *cpuResourceAdvisor) getEffectiveReservedForReclaim(numaID int) int {
	return cra.reservedForReclaim[numaID]
}

func (cra *cpuResourceAdvisor) getRegionMaxRequirement(
	r region.QoSRegion,
	pinnedCPUSizeByNuma map[int]int,
	pinnedCPUSizeByPackageByNuma map[string]map[int]int,
) float64 {
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
	case configapi.QoSRegionTypeDedicated:
		if r.IsNumaExclusive() {
			for _, numaID := range r.GetBindingNumas().ToSliceInt() {
				res += float64(cra.numaAvailable[numaID] - cra.getEffectiveReservedForReclaim(numaID))
			}
		} else {
			// ResourceUpperBound is the ceiling of CPUs this region may ever burst
			// to, not its steady demand estimate. It deliberately sums CPULimit: the
			// region is allowed to spike to its container limit, and reserving the
			// limit keeps reclaim from stealing CPUs the region may need. This is a
			// different quantity from PolicyCanonical.estimateCPUUsage, which -- per
			// the reclaim-disabled non-exclusive dedicated contract -- estimates
			// demand from CPURequest. The two views must not be conflated: the
			// estimate drives the NonReclaimedCPURequirement knob, while this upper
			// bound only caps how far the provision policy may grow the region.
			cra.metaCache.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
				if _, ok := r.GetPods()[podUID]; ok {
					res += ci.CPULimit
				}
				return true
			})
			res = general.MaxFloat64(1, res)
		}
	default:
		pkgName := r.GetResourcePackageName()
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			if pkgName != "" {
				if byNuma, ok := pinnedCPUSizeByPackageByNuma[pkgName]; ok {
					if pinnedCPUSize, ok := byNuma[numaID]; ok {
						res += float64(pinnedCPUSize)
						continue
					}
				}
			}

			if pinnedCPUSize, ok := pinnedCPUSizeByNuma[numaID]; ok {
				res += float64(cra.numaAvailable[numaID] - pinnedCPUSize - cra.getEffectiveReservedForReclaim(numaID))
			} else {
				res += float64(cra.numaAvailable[numaID] - cra.getEffectiveReservedForReclaim(numaID))
			}
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
	case configapi.QoSRegionTypeDedicated:
		return types.MinDedicatedCPURequirement
	default:
		klog.Errorf("[qosaware-cpu] unknown region type %v", r.Type())
		return 0.0
	}
}

func (cra *cpuResourceAdvisor) getRegionReservedForReclaim(r region.QoSRegion) float64 {
	res := 0.0
	for _, numaID := range r.GetBindingNumas().ToSliceInt() {
		divider := cra.numRegionsPerNuma[numaID]
		if divider < 1 {
			divider = 1
		}
		res += float64(cra.getEffectiveReservedForReclaim(numaID)) / float64(divider)
	}
	return res
}

func (cra *cpuResourceAdvisor) getRegionReservedForAllocate(dynamicConf *dynamic.Configuration, r region.QoSRegion) float64 {
	res := 0.0
	for _, numaID := range r.GetBindingNumas().ToSliceInt() {
		divider := cra.numRegionsPerNuma[numaID]
		if divider < 1 {
			divider = 1
		}
		res += cra.getNumasReservedForAllocate(dynamicConf, machine.NewCPUSet(numaID)) / float64(divider)
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

		if r.Type() == configapi.QoSRegionTypeShare || r.Type() == configapi.QoSRegionTypeDedicated {
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
