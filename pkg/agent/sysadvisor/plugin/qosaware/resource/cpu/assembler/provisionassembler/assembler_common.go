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
	"time"

	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ProvisionAssemblerCommon is the default implementation of ProvisionAssembler.
// It aggregates per-region provision control knobs into a node-level CPU pool allocation result
// (InternalCPUCalculationResult), which maps each pool (share, isolation, dedicated, reclaim, reserve)
// to its CPU size and optional quota on each NUMA node.
type ProvisionAssemblerCommon struct {
	conf                                  *config.Configuration
	regionMap                             *map[string]region.QoSRegion
	reservedForReclaim                    *map[int]int    // numaID -> cores reserved for reclaim on that NUMA
	numaAvailable                         *map[int]int    // numaID -> total available cores on that NUMA
	nonBindingNumas                       *machine.CPUSet // set of NUMA nodes not exclusively bound by any region
	allowSharedCoresOverlapReclaimedCores *bool           // whether shared pool and reclaim pool may physically overlap

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewProvisionAssemblerCommon(conf *config.Configuration, _ interface{}, regionMap *map[string]region.QoSRegion,
	reservedForReclaim *map[int]int, numaAvailable *map[int]int, nonBindingNumas *machine.CPUSet, allowSharedCoresOverlapReclaimedCores *bool,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionAssembler {
	return &ProvisionAssemblerCommon{
		conf:                                  conf,
		regionMap:                             regionMap,
		reservedForReclaim:                    reservedForReclaim,
		numaAvailable:                         numaAvailable,
		nonBindingNumas:                       nonBindingNumas,
		allowSharedCoresOverlapReclaimedCores: allowSharedCoresOverlapReclaimedCores,

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

// assembleDedicatedNUMAExclusiveRegion handles a single dedicated-NUMA-exclusive region.
// Such a region owns an entire NUMA node exclusively. The only thing to compute here is how
// much of that NUMA is available for the reclaim pool (overlapping with the dedicated pod).
// When quota control is enabled, reclaim gets the full NUMA cpuset but with a CFS quota limit;
// otherwise reclaim gets a hard cpuset size = max(reservedForReclaim, available - nonReclaimRequirement).
func (pa *ProvisionAssemblerCommon) assembleDedicatedNUMAExclusiveRegion(r region.QoSRegion, result *types.InternalCPUCalculationResult) error {
	controlKnob, err := r.GetProvision()
	if err != nil {
		return err
	}

	regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())
	available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
	var reclaimedCoresSize int
	reclaimedCoresLimit := float64(-1)

	// fill in reclaim pool entry for dedicated numa exclusive regions
	nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
	if !r.EnableReclaim() {
		// if reclaim is disabled, the dedicated workload claims the entire NUMA
		nonReclaimRequirement = available
	}

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return err
	}

	if quotaCtrlKnobEnabled {
		// with quota control: reclaim pool gets the full cpuset (size = available) but is
		// throttled via CFS quota to limit = max(reserved, available - nonReclaimRequirement)
		reclaimedCoresSize = available
		reclaimedCoresLimit = general.MaxFloat64(float64(reservedForReclaim), float64(available-nonReclaimRequirement))

		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			reclaimedCoresLimit = general.MinFloat64(reclaimedCoresLimit, quota.Value)
		}
	} else {
		// without quota control: reclaim pool gets a hard cpuset of size max(reserved, available - nonReclaimRequirement)
		reclaimedCoresSize = general.Max(reservedForReclaim, available-nonReclaimRequirement)
	}

	klog.InfoS("assembleDedicatedNUMAExclusive info", "regionName", r.Name(), "reclaimedCoresSize", reclaimedCoresSize,
		"reclaimedCoresLimit", reclaimedCoresLimit,
		"available", available, "nonReclaimRequirement", nonReclaimRequirement,
		"reservedForReclaim", reservedForReclaim, "controlKnob", controlKnob)

	// set pool overlap info for dedicated pool
	for podUID, containerSet := range r.GetPods() {
		for containerName := range containerSet {
			general.InfoS("set pool overlap pod container info",
				"poolName", commonstate.PoolNameReclaim,
				"numaID", regionNuma,
				"podUID", podUID,
				"containerName", containerName,
				"reclaimSize", reclaimedCoresSize)
			result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, regionNuma, podUID, containerName, reclaimedCoresSize)
		}
	}

	// set reclaim pool cpu limit; size is 0 since all reclaim cores overlap with the dedicated pod
	result.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, 0, reclaimedCoresLimit)
	return nil
}

// assembleReserve fills in the reserve pool entry. The reserve pool is not NUMA-bound
// (uses FakedNUMAID) and its size comes directly from the metacache.
func (pa *ProvisionAssemblerCommon) assembleReserve(result *types.InternalCPUCalculationResult) {
	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(commonstate.PoolNameReserve)
	result.SetPoolEntry(commonstate.PoolNameReserve, commonstate.FakedNUMAID, reservePoolSize, -1)
}

// AssembleProvision is the main entry point that converts per-region provision control knobs into a
// unified node-level CPU allocation result. The assembly proceeds in five ordered phases:
//  1. assembleReserve               – populate the reserve pool (system-reserved CPUs)
//  2. assembleWithNUMABinding       – for each real NUMA, allocate share/isolation/dedicated(non-exclusive) pools
//  3. assembleWithoutNUMABinding    – allocate the same pool types for non-NUMA-bound regions (FakedNUMAID)
//  4. assembleNUMABindingNUMAExclusive – allocate reclaim overlay for dedicated-NUMA-exclusive regions
//  5. assembleDummyRegion           – allocate reclaim pool with CFS quota for NUMAs with no real workloads
//
// Phases 2 and 3 both delegate to assembleWithoutNUMAExclusivePool, which is the core logic that
// computes pool sizes, regulates them to fit available resources, and determines reclaim pool size/overlap.
func (pa *ProvisionAssemblerCommon) AssembleProvision() (types.InternalCPUCalculationResult, error) {
	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries:                           make(map[string]map[int]types.CPUResource),
		PoolOverlapInfo:                       map[string]map[int]map[string]int{},
		PoolOverlapPodContainerInfo:           map[string]map[int]map[string]map[string]int{},
		TimeStamp:                             time.Now(),
		AllowSharedCoresOverlapReclaimedCores: *pa.allowSharedCoresOverlapReclaimedCores,
	}

	pa.assembleReserve(&calculationResult)

	regionHelper := NewRegionMapHelper(*pa.regionMap)

	err := pa.assembleWithNUMABinding(regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleWithNUMABinding failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	dummyRegions := regionHelper.GetRegionsByType("dummy")
	if len(dummyRegions) == 0 {
		err = pa.assembleWithoutNUMABinding(regionHelper, &calculationResult)
		if err != nil {
			general.Errorf("assembleWithoutNUMABinding failed with error: %v", err)
			return types.InternalCPUCalculationResult{}, err
		}
	} else {
		err = pa.assembleDummyRegion(regionHelper, &calculationResult)
		if err != nil {
			general.Errorf("assembleDummyRegion failed with error: %v", err)
			return types.InternalCPUCalculationResult{}, err
		}
	}

	err = pa.assembleNUMABindingNUMAExclusive(regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleNUMABindingNUMAExclusive failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	return calculationResult, nil
}

// assembleWithoutNUMABinding handles non-NUMA-bound regions by delegating to
// assembleWithoutNUMAExclusivePool with FakedNUMAID. The available resource for these
// regions spans all nonBindingNumas (NUMAs not exclusively occupied by any region).
func (pa *ProvisionAssemblerCommon) assembleWithoutNUMABinding(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	return pa.assembleWithoutNUMAExclusivePool(regionHelper, commonstate.FakedNUMAID, result)
}

// assembleWithNUMABinding iterates each real NUMA node and calls assembleWithoutNUMAExclusivePool
// to process NUMA-bound share/isolation/dedicated(non-exclusive) regions on that NUMA.
func (pa *ProvisionAssemblerCommon) assembleWithNUMABinding(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range *pa.numaAvailable {
		err := pa.assembleWithoutNUMAExclusivePool(regionHelper, numaID, result)
		if err != nil {
			return err
		}
	}

	return nil
}

// assembleNUMABindingNUMAExclusive handles dedicated regions that exclusively own a NUMA node.
// These regions are NOT processed by assembleWithoutNUMAExclusivePool; instead each one is
// handled individually by assembleDedicatedNUMAExclusiveRegion which only computes a reclaim
// pool overlay on the exclusively-owned NUMA.
func (pa *ProvisionAssemblerCommon) assembleNUMABindingNUMAExclusive(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range *pa.numaAvailable {
		dedicatedNUMAExclusiveRegions := regionHelper.GetRegionsByNUMA(numaID, configapi.QoSRegionTypeDedicated)
		for _, r := range dedicatedNUMAExclusiveRegions {
			if !r.IsNumaBinding() || !r.IsNumaExclusive() {
				continue
			}

			if err := pa.assembleDedicatedNUMAExclusiveRegion(r, result); err != nil {
				return fmt.Errorf("failed to assemble dedicatedNUMAExclusiveRegion: %v", err)
			}
		}
	}

	return nil
}

// assembleDummyRegion handles dummy regions on NUMA nodes that have no real workload regions.
// It retrieves the provision control knob (ControlKnobReclaimedCoresCPUQuota) from each dummy region,
// then sets the reclaimed_cores pool entry on the corresponding NUMA. Only CFS quota control is supported:
// the reclaim pool gets the full NUMA cpuset (size = available) with a CFS quota limit from the control knob.
func (pa *ProvisionAssemblerCommon) assembleDummyRegion(regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range *pa.numaAvailable {
		dummyRegions := regionHelper.GetRegionsByNUMA(numaID, "dummy")
		for _, r := range dummyRegions {
			controlKnob, err := r.GetProvision()
			if err != nil {
				klog.Warningf("assembleDummyRegion: skip region %v on NUMA %v due to GetProvision error: %v", r.Name(), numaID, err)
				continue
			}

			available := getNUMAsResource(*pa.numaAvailable, r.GetBindingNumas())
			reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, r.GetBindingNumas())

			reclaimedCoresSize := available
			reclaimedCoresQuota := float64(-1)
			if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
				reclaimedCoresQuota = general.MaxFloat64(quota.Value, float64(reservedForReclaim))
			}

			klog.InfoS("assembleDummyRegion info",
				"regionName", r.Name(),
				"numaID", numaID,
				"reclaimedCoresSize", reclaimedCoresSize,
				"reclaimedCoresQuota", reclaimedCoresQuota,
				"available", available,
				"reservedForReclaim", reservedForReclaim,
				"controlKnob", controlKnob)

			result.SetPoolEntry(commonstate.PoolNameReclaim, numaID, reclaimedCoresSize, reclaimedCoresQuota)
		}
	}

	return nil
}

// assembleWithoutNUMAExclusivePool is the core allocation function for share, isolation, and
// dedicated(non-NUMA-exclusive) regions on a given NUMA (or FakedNUMAID for non-bound regions).
//
// The algorithm proceeds in the following steps:
//  1. Extract region info – gather requirements (provision-computed), requests (pod-requested),
//     reclaimEnable flags, and isolation upper/lower bounds for each region type.
//  2. Compute available resource – total NUMA cores minus reservedForReclaim (if no overlap allowed).
//  3. Choose isolation pool sizes – use upper bounds if resources suffice; otherwise fall back to lower bounds.
//  4. Regulate pool sizes – call regulatePoolSizes to proportionally fit all pools within available
//     resources. Share pools are expandable (may grow to fill slack when reclaim is off or overlap is on);
//     isolation and dedicated pools are unexpandable (keep their computed size).
//  5. Write pool entries – fill PoolEntries in the result for share/isolation/dedicated pools.
//  6. Compute reclaim pool – the most complex part, with two major branches:
//     a. allowSharedCoresOverlapReclaimedCores=true: reclaim pool physically overlaps with share/dedicated
//     pools. Overlap sizes are recorded in PoolOverlapInfo/PoolOverlapPodContainerInfo.
//     b. allowSharedCoresOverlapReclaimedCores=false: reclaim pool has its own dedicated cpuset.
//     In both cases, quota control (CFS) may further cap the reclaim pool.
//  7. Write reclaim pool entry – set nonOverlapReclaimedCoresSize and optional quota.
func (pa *ProvisionAssemblerCommon) assembleWithoutNUMAExclusivePool(
	regionHelper *RegionMapHelper,
	numaID int,
	result *types.InternalCPUCalculationResult,
) error {
	// Step 1: Extract region info from each region type on this NUMA.
	shareRegions := regionHelper.GetRegionsByNUMA(numaID, configapi.QoSRegionTypeShare)
	shareInfo, err := extractShareRegionInfo(shareRegions)
	if err != nil {
		return err
	}

	isolationRegions := regionHelper.GetRegionsByNUMA(numaID, configapi.QoSRegionTypeIsolation)
	isolationInfo, err := extractIsolationRegionInfo(isolationRegions)
	if err != nil {
		return err
	}

	dedicatedRegions := regionHelper.GetRegionsByNUMA(numaID, configapi.QoSRegionTypeDedicated)
	dedicatedInfo, err := extractDedicatedRegionInfo(dedicatedRegions)
	if err != nil {
		return err
	}

	// skip empty numa binding region
	if len(shareRegions) == 0 && len(isolationRegions) == 0 && len(dedicatedRegions) == 0 && numaID != commonstate.FakedNUMAID {
		return nil
	}

	nodeEnableReclaim := pa.conf.GetDynamicConfiguration().EnableReclaim

	// Step 2: Compute the available CPU resource for this NUMA set.
	// For FakedNUMAID, numaSet spans all non-binding NUMAs; for a real NUMA, it's just {numaID}.
	var numaSet machine.CPUSet
	if numaID == commonstate.FakedNUMAID {
		numaSet = *pa.nonBindingNumas
	} else {
		numaSet = machine.NewCPUSet(numaID)
	}

	reservedForReclaim := getNUMAsResource(*pa.reservedForReclaim, numaSet)
	shareAndIsolatedDedicatedPoolAvailable := getNUMAsResource(*pa.numaAvailable, numaSet)
	if !*pa.allowSharedCoresOverlapReclaimedCores {
		// when overlap is not allowed, reclaim pool has its own dedicated cpuset,
		// so subtract the reserved cores from the available pool for share/isolation/dedicated
		shareAndIsolatedDedicatedPoolAvailable -= reservedForReclaim
	}
	// sharePoolSizeRequirements: for reclaim-enabled pools use provision requirement;
	// for non-reclaimable pools use pod request (the pool cannot shrink below request).
	sharePoolSizeRequirements := getPoolSizeRequirements(shareInfo)

	// Step 3: Choose isolation pool sizes. Prefer upper bounds; fall back to lower bounds
	// if total demand (share + isolation upper) exceeds available minus dedicated.
	isolationUppers := general.SumUpMapValues(isolationInfo.isolationUpperSizes)
	isolationPoolSizes := isolationInfo.isolationUpperSizes
	// if the maximum of share sharePoolSizeRequirements and share requests adds up with isolation upper sizes is larger than
	// the available cores of share and isolated pool, we should shrink the isolation pool sizes to lower sizes
	if general.Max(general.SumUpMapValues(shareInfo.requests), general.SumUpMapValues(shareInfo.requirements))+isolationUppers >
		shareAndIsolatedDedicatedPoolAvailable-general.SumUpMapValues(dedicatedInfo.requests) {
		isolationPoolSizes = isolationInfo.isolationLowerSizes
	}

	// Step 4: Regulate pool sizes to fit within available resources.
	// allowExpand=true means share pools can grow to fill all remaining slack (reclaim is off or overlap is on).
	// Share pools are "expandable"; isolation and dedicated pools are "unexpandable" (fixed-size).
	allowExpand := !nodeEnableReclaim || *pa.allowSharedCoresOverlapReclaimedCores
	var regulateSharePoolSizes map[string]int
	if allowExpand {
		// when expandable, use raw pod requests as the base so pools can grow proportionally
		regulateSharePoolSizes = shareInfo.requests
	} else {
		// when not expandable, use provision requirements to leave room for reclaim
		regulateSharePoolSizes = sharePoolSizeRequirements
	}
	unexpandableRequirements := general.MergeMapInt(isolationPoolSizes, dedicatedInfo.requests)
	shareAndIsolateDedicatedPoolSizes, poolThrottled := regulatePoolSizes(regulateSharePoolSizes, unexpandableRequirements, shareAndIsolatedDedicatedPoolAvailable, allowExpand)
	for _, r := range shareRegions {
		r.SetThrottled(poolThrottled)
	}

	// Compute dedicated pool reclaimable headroom: allocated - provision requirement
	dedicatedPoolSizes := make(map[string]int)
	for poolName := range dedicatedInfo.requests {
		if size, ok := shareAndIsolateDedicatedPoolSizes[poolName]; ok {
			dedicatedPoolSizes[poolName] = size
		}
	}
	dedicatedPoolAvailable := general.SumUpMapValues(dedicatedPoolSizes)
	dedicatedPoolSizeRequirements := getPoolSizeRequirements(dedicatedInfo)
	dedicatedReclaimCoresSize := dedicatedPoolAvailable - general.SumUpMapValues(dedicatedPoolSizeRequirements)

	general.InfoS("pool info",
		"numaID", numaID,
		"reservedForReclaim", reservedForReclaim,
		"shareRequirements", shareInfo.requirements,
		"shareRequests", shareInfo.requests,
		"shareReclaimEnable", shareInfo.reclaimEnable,
		"shareMinReclaimedCoresCPUQuota", shareInfo.minReclaimedCoresCPUQuota,
		"dedicatedRequirements", dedicatedInfo.requirements,
		"dedicatedRequests", dedicatedInfo.requests,
		"dedicatedReclaimEnable", dedicatedInfo.reclaimEnable,
		"dedicatedMinReclaimedCoresCPUQuota", dedicatedInfo.minReclaimedCoresCPUQuota,
		"dedicatedPoolAvailable", dedicatedPoolAvailable,
		"dedicatedPoolSizeRequirements", dedicatedPoolSizeRequirements,
		"dedicatedReclaimCoresSize", dedicatedReclaimCoresSize,
		"sharePoolSizeRequirements", sharePoolSizeRequirements,
		"isolationUpperSizes", isolationInfo.isolationUpperSizes,
		"isolationLowerSizes", isolationInfo.isolationLowerSizes,
		"shareAndIsolateDedicatedPoolSizes", shareAndIsolateDedicatedPoolSizes,
		"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable)

	// Step 5: Fill in regulated share, isolation, and dedicated pool entries into the result.
	for poolName, poolSize := range shareAndIsolateDedicatedPoolSizes {
		if podSet, ok := dedicatedInfo.podSet[poolName]; ok {
			// fill in dedicated pool entries with pod uid for each pod
			for uid := range podSet {
				result.SetPoolEntry(uid, numaID, poolSize, -1)
			}
		} else {
			// fill in share pool or isolation pool entries with pool name for each pod
			result.SetPoolEntry(poolName, numaID, poolSize, -1)
		}
	}

	// Step 6: Assemble the reclaim pool. This is split into two major branches based on
	// whether shared cores are allowed to physically overlap with reclaimed cores.
	var reclaimedCoresSize, overlapReclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)
	if *pa.allowSharedCoresOverlapReclaimedCores {
		// Branch A: Overlap mode – reclaim pool shares the same physical CPUs with share/dedicated pools.
		// We need to compute how much of each pool overlaps with reclaim and record the overlap info.
		isolated := 0
		poolSizes := make(map[string]int)
		sharePoolSizes := make(map[string]int)
		reclaimablePoolSizes := make(map[string]int)
		nonReclaimableSharePoolSizes := make(map[string]int)
		reclaimableShareRequirements := make(map[string]int)
		reclaimableRequirements := make(map[string]int)
		// Classify each pool from the regulated result into reclaimable vs non-reclaimable buckets.
		for poolName, size := range shareAndIsolateDedicatedPoolSizes {
			_, ok := sharePoolSizeRequirements[poolName]
			if ok {
				if shareInfo.reclaimEnable[poolName] {
					reclaimablePoolSizes[poolName] = size
					reclaimableShareRequirements[poolName] = shareInfo.requirements[poolName]
					reclaimableRequirements[poolName] = shareInfo.requirements[poolName]
				} else {
					nonReclaimableSharePoolSizes[poolName] = size
				}
				poolSizes[poolName] = size
				sharePoolSizes[poolName] = size
			}

			_, ok = isolationPoolSizes[poolName]
			if ok {
				isolated += size
			}

			_, ok = dedicatedInfo.requests[poolName]
			if ok {
				if dedicatedInfo.reclaimEnable[poolName] {
					reclaimablePoolSizes[poolName] = size
					reclaimableRequirements[poolName] = dedicatedInfo.requirements[poolName]
				}
				poolSizes[poolName] = size
			}
		}

		overlapReclaimSize := make(map[string]int)
		// shareReclaimCoresSize: how many cores can be reclaimed from the share region area
		// = total available - isolation - non-reclaimable share - reclaimable share requirements - dedicated
		shareReclaimCoresSize := shareAndIsolatedDedicatedPoolAvailable - isolated -
			general.SumUpMapValues(nonReclaimableSharePoolSizes) - general.SumUpMapValues(reclaimableShareRequirements) -
			general.SumUpMapValues(dedicatedPoolSizes)
		if nodeEnableReclaim {
			// When reclaim is enabled, the total reclaimable = share reclaimable + dedicated reclaimable.
			// If this is below reservedForReclaim, bump up to reserved and spread overlap proportionally.
			reclaimedCoresSize = shareReclaimCoresSize + dedicatedReclaimCoresSize
			if reclaimedCoresSize < reservedForReclaim {
				reclaimedCoresSize = reservedForReclaim
				regulatedOverlapReclaimPoolSize, err := regulateOverlapReclaimPoolSize(poolSizes, reclaimedCoresSize)
				if err != nil {
					return fmt.Errorf("failed to regulateOverlapReclaimPoolSize for NUMAs reserved for reclaim: %w", err)
				}
				overlapReclaimSize = regulatedOverlapReclaimPoolSize
			} else {
				for poolName, size := range reclaimablePoolSizes {
					requirement, ok := reclaimableRequirements[poolName]
					if !ok {
						continue
					}

					// calculate the reclaim size for each share pool by subtracting the share requirement from the share pool size
					reclaimSize := size - requirement
					if reclaimSize > 0 {
						overlapReclaimSize[poolName] = reclaimSize
					} else {
						overlapReclaimSize[poolName] = 1
					}
				}
			}
		} else {
			// When reclaim is disabled node-wide, reclaim only gets the minimum reserved cores.
			// Overlap with share/dedicated pools is still needed when reserved > what's naturally free.
			reclaimedCoresSize = reservedForReclaim
			if len(poolSizes) > 0 && reclaimedCoresSize > shareReclaimCoresSize {
				// only if reclaimedCoresSize > shareReclaimCoresSize, overlap reclaim pool with both share pool and dedicated pool
				reclaimedCoresSize = general.Min(reclaimedCoresSize, general.SumUpMapValues(poolSizes))
				var overlapSharePoolSizes map[string]int
				if reclaimedCoresSize <= general.SumUpMapValues(reclaimablePoolSizes) {
					overlapSharePoolSizes = reclaimablePoolSizes
				} else {
					overlapSharePoolSizes = poolSizes
				}

				reclaimSizes, err := regulateOverlapReclaimPoolSize(overlapSharePoolSizes, reclaimedCoresSize)
				if err != nil {
					return fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
				}
				overlapReclaimSize = reclaimSizes
			} else if len(sharePoolSizes) > 0 && reclaimedCoresSize <= general.SumUpMapValues(sharePoolSizes) {
				// if exit share pool, and reclaimedCoresSize <= sum of share pool size, overlap reclaim pool with share pool
				reclaimSizes, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimedCoresSize)
				if err != nil {
					return fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
				}
				overlapReclaimSize = reclaimSizes
			}
		}

		quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
		if err != nil {
			return err
		}

		// Apply CFS quota control for reclaim pool if enabled (only for real NUMAs with pools).
		// This limits reclaim's actual CPU usage even though its cpuset may overlap with other pools.
		if quotaCtrlKnobEnabled && numaID != commonstate.FakedNUMAID && len(poolSizes) > 0 {
			reclaimedCoresQuota = float64(general.Max(reservedForReclaim, reclaimedCoresSize))
			if shareInfo.minReclaimedCoresCPUQuota != -1 || dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
				if shareInfo.minReclaimedCoresCPUQuota != -1 {
					reclaimedCoresQuota = shareInfo.minReclaimedCoresCPUQuota
				}

				if dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
					reclaimedCoresQuota = general.MinFloat64(reclaimedCoresQuota, dedicatedInfo.minReclaimedCoresCPUQuota)
				}

				reclaimedCoresQuota = general.MaxFloat64(reclaimedCoresQuota, float64(reservedForReclaim))
			}

			// if cpu quota enabled, set all reclaimable share pool size to reclaimablePoolSizes
			for poolName := range overlapReclaimSize {
				overlapReclaimSize[poolName] = general.Max(overlapReclaimSize[poolName], reclaimablePoolSizes[poolName])
			}
		}

		// Record the overlap relationships into the result.
		// For share pools: use PoolOverlapInfo (reclaim overlaps with named pool).
		// For dedicated pods: use PoolOverlapPodContainerInfo (reclaim overlaps with specific pod/container).
		for overlapPoolName, reclaimSize := range overlapReclaimSize {
			if _, ok := shareInfo.requests[overlapPoolName]; ok {
				general.InfoS("set pool overlap info",
					"poolName", commonstate.PoolNameReclaim,
					"numaID", numaID,
					"poolName", overlapPoolName,
					"reclaimSize", reclaimSize)
				result.SetPoolOverlapInfo(commonstate.PoolNameReclaim, numaID, overlapPoolName, reclaimSize)
				overlapReclaimedCoresSize += reclaimSize
				continue
			}

			if podSet, ok := dedicatedInfo.podSet[overlapPoolName]; ok {
				// set pool overlap info for dedicated pool
				for podUID, containerSet := range podSet {
					for containerName := range containerSet {
						general.InfoS("set pool overlap pod container info",
							"poolName", commonstate.PoolNameReclaim,
							"numaID", numaID,
							"podUID", podUID,
							"containerName", containerName,
							"reclaimSize", reclaimSize)
						result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, numaID, podUID, containerName, reclaimSize)
					}
				}
				overlapReclaimedCoresSize += reclaimSize
				continue
			}
		}
	} else {
		// Branch B: Non-overlap mode – reclaim pool has its own dedicated cpuset, separate from share pools.
		// The reclaim pool size = leftover after allocating share/isolation/dedicated + dedicated reclaimable + reserved.
		if nodeEnableReclaim {
			// When reclaim is enabled, dedicated pools that allow reclaim contribute their surplus
			// (allocated - requirement) as reclaim overlap.
			for poolName, size := range dedicatedInfo.requests {
				if dedicatedInfo.reclaimEnable[poolName] {
					reclaimSize := size - dedicatedInfo.requirements[poolName]
					if reclaimSize <= 0 {
						continue
					}
					if podSet, ok := dedicatedInfo.podSet[poolName]; ok {
						// set pool overlap info for dedicated pool
						for podUID, containerSet := range podSet {
							for containerName := range containerSet {
								general.InfoS("set pool overlap pod container info",
									"poolName", commonstate.PoolNameReclaim,
									"numaID", numaID,
									"podUID", podUID,
									"containerName", containerName,
									"reclaimSize", reclaimSize)
								result.SetPoolOverlapPodContainerInfo(commonstate.PoolNameReclaim, numaID, podUID, containerName, reclaimSize)
							}
						}
						overlapReclaimedCoresSize += reclaimSize
						continue
					}
				}
			}

			// shareReclaimedCoresSize: cores left after allocating all share/isolation/dedicated pools
			shareReclaimedCoresSize := shareAndIsolatedDedicatedPoolAvailable - general.SumUpMapValues(shareAndIsolateDedicatedPoolSizes)
			reclaimedCoresSize = shareReclaimedCoresSize + dedicatedReclaimCoresSize + reservedForReclaim
		} else {
			reclaimedCoresSize = reservedForReclaim
		}
	}

	// Step 7: Write the final reclaim pool entry. The non-overlap portion is the reclaim pool's
	// own dedicated cpuset size; the overlap portion is already recorded in PoolOverlapInfo above.
	nonOverlapReclaimedCoresSize := general.Max(reclaimedCoresSize-overlapReclaimedCoresSize, 0)
	result.SetPoolEntry(commonstate.PoolNameReclaim, numaID, nonOverlapReclaimedCoresSize, reclaimedCoresQuota)

	general.InfoS("assemble reclaim pool entry",
		"numaID", numaID,
		"reservedForReclaim", reservedForReclaim,
		"reclaimedCoresSize", reclaimedCoresSize,
		"overlapReclaimedCoresSize", overlapReclaimedCoresSize,
		"nonOverlapReclaimedCoresSize", nonOverlapReclaimedCoresSize,
		"reclaimedCoresQuota", reclaimedCoresQuota)

	return nil
}

// regionInfo holds aggregated resource information for a set of regions of the same type.
// For share regions the map key is the owner pool name; for dedicated regions the key is the region name.
type regionInfo struct {
	requirements              map[string]int          // provision-computed non-reclaim CPU requirement per pool
	requests                  map[string]int          // actual pod CPU request (ceiling) per pool
	reclaimEnable             map[string]bool         // whether each pool allows reclaim
	podSet                    map[string]types.PodSet // pods belonging to each pool (dedicated only)
	minReclaimedCoresCPUQuota float64                 // minimum reclaimed cores CPU quota across all regions (-1 if unset)
}

// extractShareRegionInfo extracts provision info from share regions.
// Each share region's ControlKnob provides NonReclaimedCPURequirement (the provision policy output)
// and optionally ReclaimedCoresCPUQuota. Pod request is the ceiling of the region's aggregate pod CPU request.
// The map key for all returned maps is the region's owner pool name.
func extractShareRegionInfo(shareRegions []region.QoSRegion) (regionInfo, error) {
	shareRequirements := make(map[string]int)
	shareRequests := make(map[string]int)
	shareReclaimEnable := make(map[string]bool)
	minReclaimedCoresCPUQuota := float64(-1)

	for _, r := range shareRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, err
		}
		shareRequirements[r.OwnerPoolName()] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		shareRequests[r.OwnerPoolName()] = general.Max(1, int(math.Ceil(r.GetPodsRequest())))
		shareReclaimEnable[r.OwnerPoolName()] = r.EnableReclaim()
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			if minReclaimedCoresCPUQuota == -1 || quota.Value < minReclaimedCoresCPUQuota {
				minReclaimedCoresCPUQuota = quota.Value
			}
		}
	}

	return regionInfo{
		requirements:              shareRequirements,
		requests:                  shareRequests,
		reclaimEnable:             shareReclaimEnable,
		minReclaimedCoresCPUQuota: minReclaimedCoresCPUQuota,
	}, nil
}

// getPoolSizeRequirements returns the effective pool size requirement for each pool:
// if reclaim is enabled for a pool, use the provision requirement (allowing surplus to be reclaimed);
// if reclaim is disabled, use the pod request (the pool must hold at least this much).
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

// isolationRegionInfo holds the upper and lower CPU size bounds for isolation regions.
// The assembler uses upper bounds when resources are sufficient, falling back to lower bounds under pressure.
type isolationRegionInfo struct {
	isolationUpperSizes map[string]int
	isolationLowerSizes map[string]int
}

// extractIsolationRegionInfo extracts upper and lower CPU size bounds from isolation regions.
// Upper = ControlKnobNonIsolatedUpperCPUSize, Lower = ControlKnobNonIsolatedLowerCPUSize.
func extractIsolationRegionInfo(isolationRegions []region.QoSRegion) (isolationRegionInfo, error) {
	isolationUpperSizes := make(map[string]int)
	isolationLowerSizes := make(map[string]int)

	for _, r := range isolationRegions {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return isolationRegionInfo{}, err
		}
		// save limits and requests for isolated region
		isolationUpperSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value)
		isolationLowerSizes[r.Name()] = int(controlKnob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)
	}

	return isolationRegionInfo{
		isolationUpperSizes: isolationUpperSizes,
		isolationLowerSizes: isolationLowerSizes,
	}, nil
}

// extractDedicatedRegionInfo extracts provision info from dedicated (non-NUMA-exclusive) regions.
// NUMA-exclusive regions are skipped here (handled separately by assembleNUMABindingNUMAExclusive).
// For NUMA-binding regions, the per-NUMA request is the total pod request divided by the number of bound NUMAs.
// The map key for all returned maps is the region name.
func extractDedicatedRegionInfo(regions []region.QoSRegion) (regionInfo, error) {
	dedicatedRequirements := make(map[string]int)
	dedicatedRequests := make(map[string]int)
	dedicatedEnable := make(map[string]bool)
	dedicatedPodSet := make(map[string]types.PodSet)
	minReclaimedCoresCPUQuota := float64(-1)
	for _, r := range regions {
		if r.IsNumaExclusive() {
			continue
		}

		controlKnob, err := r.GetProvision()
		if err != nil {
			return regionInfo{}, err
		}

		regionName := r.Name()
		dedicatedRequirements[regionName] = general.Max(1, int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value))
		if r.IsNumaBinding() {
			numaBindingSize := r.GetBindingNumas().Size()
			if numaBindingSize == 0 {
				return regionInfo{}, fmt.Errorf("numa binding size is zero, region name: %s", r.Name())
			}
			dedicatedRequests[regionName] = int(math.Ceil(r.GetPodsRequest() / float64(numaBindingSize)))
		} else {
			dedicatedRequests[regionName] = int(math.Ceil(r.GetPodsRequest()))
		}
		dedicatedEnable[regionName] = r.EnableReclaim()
		dedicatedPodSet[regionName] = r.GetPods()
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			if minReclaimedCoresCPUQuota == -1 || quota.Value < minReclaimedCoresCPUQuota {
				minReclaimedCoresCPUQuota = quota.Value
			}
		}
	}

	return regionInfo{
		requirements:              dedicatedRequirements,
		requests:                  dedicatedRequests,
		reclaimEnable:             dedicatedEnable,
		podSet:                    dedicatedPodSet,
		minReclaimedCoresCPUQuota: minReclaimedCoresCPUQuota,
	}, nil
}
