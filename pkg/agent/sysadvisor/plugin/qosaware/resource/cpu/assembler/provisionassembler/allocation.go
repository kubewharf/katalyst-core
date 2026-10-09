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

	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func (pa *ProvisionAssemblerCommon) assembleWithoutNUMAExclusivePool(
	ctx ProvisionContext,
	regionHelper *RegionMapHelper,
	numaID int,
	result *types.InternalCPUCalculationResult,
) error {
	shareRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeShare)
	var numaSet machine.CPUSet
	if numaID == commonstate.FakedNUMAID {
		numaSet = ctx.NonBindingNumas
	} else {
		numaSet = machine.NewCPUSet(numaID)
	}

	cfg := pa.metaReader.GetResourcePackageConfig()
	pinnedCPUSizeByPkg := pa.getPinnedCPUSizeByPackage(numaSet, cfg)
	totalPinnedCPUSize := general.SumUpMapValues(pinnedCPUSizeByPkg)

	disableReclaimSelectorStr := ctx.DynamicConfiguration.DisableReclaimPinnedCPUSetResourcePackageSelector
	disableReclaimSelector, err := general.ParseSelector(disableReclaimSelectorStr)
	if err != nil {
		return err
	}
	nonReclaimablePackages := sets.NewString()
	// Iterate the backing NUMAs under a distinct variable name: the outer
	// numaID parameter selects which scope this call assembles, and shadowing it
	// here would silently bind a later edit to the loop value instead of the scope.
	for _, nid := range numaSet.ToSliceInt() {
		if pkgMap, ok := cfg[nid]; ok {
			nonReclaimablePackages = nonReclaimablePackages.Union(resourcepackage.GetMatchedPackages(pkgMap, disableReclaimSelector))
		}
	}

	unpinnedShareRegionInfo, pinnedShareRegionInfos, err := extractShareRegionInfo(shareRegions, pinnedCPUSizeByPkg, nonReclaimablePackages)
	if err != nil {
		return err
	}

	isolationRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeIsolation)
	unpinnedIsolationInfo, pinnedIsolationInfo, err := extractIsolationRegionInfo(isolationRegions, pinnedCPUSizeByPkg, nonReclaimablePackages)
	if err != nil {
		return err
	}

	dedicatedRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicated)
	unpinnedDedicatedInfo, pinnedDedicatedInfo, err := extractDedicatedRegionInfo(dedicatedRegions, pinnedCPUSizeByPkg, nonReclaimablePackages)
	if err != nil {
		return err
	}

	dynamicConf := ctx.DynamicConfiguration
	// Hard partition only applies to NUMAs that actually host a ramp-up source.
	// A real NUMA must itself be an active ramp-up domain; the global
	// (FakedNUMAID) shared pool aggregates over nonBindingNUMAs, so it engages when
	// any of those backing NUMAs is active. Dedicated-binding NUMAs are excluded
	// from nonBindingNUMAs by construction and are never pulled in here.
	effectiveHard := pa.rampUpHardPartitionActive(ctx, numaID)
	if !effectiveHard && numaID == commonstate.FakedNUMAID {
		effectiveHard = pa.rampUpHardPartitionActiveAnyIn(ctx, numaSet)
	}

	// While ramp-up hard partition is active, publish the canonical target even
	// when this NUMA has no workload region.
	if len(shareRegions) == 0 && len(isolationRegions) == 0 && len(dedicatedRegions) == 0 && numaID != commonstate.FakedNUMAID {
		if effectiveHard {
			result.SetPoolEntry(
				commonstate.PoolNameReclaim,
				numaID,
				getNUMAsResource(ctx.RampUpReclaimCPUSetCap, numaSet),
				-1,
			)
		}
		return nil
	}

	nodeEnableReclaim := dynamicConf.EnableReclaim

	reservedForReclaim := pa.effectiveReservedForReclaim(ctx, numaSet)
	poolAvailableBeforeReserve := getNUMAsResource(ctx.NUMAAvailable, numaSet)
	pinnedPoolAvailableByPkg, pinnedReserveByPkg,
		unpinnedShareAndIsolatedDedicatedPoolAvailable, unpinnedReserve := getEligibilityDomainCapacities(
		numaSet,
		cfg,
		pinnedCPUSizeByPkg,
		poolAvailableBeforeReserve,
		reservedForReclaim,
		pa.metaReader,
	)
	shareAndIsolatedDedicatedPoolAvailable := poolAvailableBeforeReserve
	legacySharedOnly := len(dedicatedRegions) == 0
	reserveHeldOutsidePools := effectiveHard
	if reserveHeldOutsidePools || (legacySharedOnly && !ctx.AllowSharedCoresOverlapReclaimedCores) {
		for pkgName, reserve := range pinnedReserveByPkg {
			pinnedPoolAvailableByPkg[pkgName] = general.Max(pinnedPoolAvailableByPkg[pkgName]-reserve, 0)
		}
		unpinnedShareAndIsolatedDedicatedPoolAvailable = general.Max(unpinnedShareAndIsolatedDedicatedPoolAvailable-unpinnedReserve, 0)
		shareAndIsolatedDedicatedPoolAvailable = general.Max(shareAndIsolatedDedicatedPoolAvailable-reservedForReclaim, 0)
	}

	getShareAndIsolateDedicatedPoolSizesFunc := func(
		shareAndIsolatedDedicatedPoolAvailable, reserveInDomain int,
		shareRegionInfo, dedicatedRegionInfo regionInfo,
		isolationRegionInfo isolationRegionInfo,
	) map[string]int {
		sharePoolSizeRequirements := getPoolSizeRequirements(shareRegionInfo)
		allowExpand := !nodeEnableReclaim || ctx.AllowSharedCoresOverlapReclaimedCores
		dedicatedMinimums := getPoolSizeRequirements(dedicatedRegionInfo)
		shareExpansionTargets := sharePoolSizeRequirements
		if allowExpand {
			shareExpansionTargets = shareRegionInfo.requests
		}
		expansionTargets := general.MergeMapInt(shareExpansionTargets, isolationRegionInfo.isolationUpperSizes)
		for poolName, request := range dedicatedRegionInfo.requests {
			expansionTargets[poolName] = desiredDedicatedPhysical(
				request,
				dedicatedRegionInfo.requirements[poolName],
				dedicatedRegionInfo.reclaimEnable[poolName],
				ctx.DisableDedicatedCoresOverlapReclaimedCores,
			)
		}

		general.InfoS("getShareAndIsolateDedicatedPoolSizesFunc pre priority allocation",
			"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable,
			"allowExpand", allowExpand,
			"dedicatedMinimums", dedicatedMinimums,
			"isolationLowerSizes", isolationRegionInfo.isolationLowerSizes,
			"sharePoolSizeRequirements", sharePoolSizeRequirements,
			"expansionTargets", expansionTargets)

		allocateAtCapacity := func(capacity int) (map[string]int, bool) {
			sharedAvailable := capacity
			if !reserveHeldOutsidePools && !legacySharedOnly && !ctx.AllowSharedCoresOverlapReclaimedCores {
				sharedAvailable = general.Max(sharedAvailable-reserveInDomain, 0)
			}
			dedicatedAvailable := capacity
			if !reserveHeldOutsidePools && ctx.DisableDedicatedCoresOverlapReclaimedCores {
				dedicatedAvailable = general.Max(dedicatedAvailable-reserveInDomain, 0)
			}

			var poolSizes map[string]int
			var throttled bool
			if legacySharedOnly {
				poolSizes, throttled = allocatePoolSizesByPriority(
					capacity,
					dedicatedMinimums,
					isolationRegionInfo.isolationLowerSizes,
					sharePoolSizeRequirements,
					expansionTargets,
				)
			} else {
				poolSizes, throttled = allocatePoolSizesByWorkloadPriority(
					capacity,
					dedicatedAvailable,
					sharedAvailable,
					dedicatedMinimums,
					isolationRegionInfo.isolationLowerSizes,
					sharePoolSizeRequirements,
					expansionTargets,
				)
			}
			if allowExpand {
				expandSharePoolsToCapacity(poolSizes, shareRegionInfo.requests, sharedAvailable)
			}
			return poolSizes, throttled
		}

		allocationCapacity := shareAndIsolatedDedicatedPoolAvailable
		shareAndIsolateDedicatedPoolSizes, poolThrottled := allocateAtCapacity(allocationCapacity)
		if nodeEnableReclaim && !legacySharedOnly && !reserveHeldOutsidePools {
			for {
				overlapCapacity := 0
				if ctx.AllowSharedCoresOverlapReclaimedCores {
					for poolName, requirement := range shareRegionInfo.requirements {
						if shareRegionInfo.reclaimEnable[poolName] {
							overlapCapacity += general.Max(shareAndIsolateDedicatedPoolSizes[poolName]-requirement, 0)
						}
					}
				}
				if !ctx.DisableDedicatedCoresOverlapReclaimedCores {
					for poolName, requirement := range dedicatedRegionInfo.requirements {
						if dedicatedRegionInfo.reclaimEnable[poolName] {
							overlapCapacity += general.Max(shareAndIsolateDedicatedPoolSizes[poolName]-requirement, 0)
						}
					}
				}
				freeCapacity := general.Max(
					shareAndIsolatedDedicatedPoolAvailable-general.SumUpMapValues(shareAndIsolateDedicatedPoolSizes),
					0,
				)
				deficit := general.Max(reserveInDomain-freeCapacity-overlapCapacity, 0)
				if deficit == 0 || allocationCapacity == 0 {
					break
				}
				allocationCapacity = general.Max(allocationCapacity-deficit, 0)
				shareAndIsolateDedicatedPoolSizes, poolThrottled = allocateAtCapacity(allocationCapacity)
			}
		}

		general.InfoS("getShareAndIsolateDedicatedPoolSizesFunc post priority allocation",
			"shareAndIsolateDedicatedPoolSizes", shareAndIsolateDedicatedPoolSizes,
			"poolThrottled", poolThrottled)

		for _, r := range shareRegionInfo.regionMap {
			r.SetThrottled(poolThrottled)
		}

		return shareAndIsolateDedicatedPoolSizes
	}

	shareInfo := initRegionInfo()
	isolationInfo := initIsolationRegionInfo()
	dedicatedInfo := initRegionInfo()
	shareAndIsolateDedicatedPoolSizes := make(map[string]int)
	pinnedCPUSetAllInfo := getPinnedCPUSetAllRegionInfo(pinnedShareRegionInfos, pinnedIsolationInfo, pinnedDedicatedInfo)
	totalUnusedNonReclaimablePinnedCPUSize := 0

	general.InfoS("pool info start",
		"numaID", numaID,
		"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable,
		"totalPinnedCPUSize", totalPinnedCPUSize,
		"unpinnedShareAndIsolatedDedicatedPoolAvailable", unpinnedShareAndIsolatedDedicatedPoolAvailable,
		"nonReclaimablePackages", nonReclaimablePackages,
		"disableReclaimSelector", disableReclaimSelector)

	// dedicatedPoolCeilings records, per dedicated pool, the post-reserve available
	// capacity of its eligibility domain (its pinned package, or the unpinned domain).
	// A dedicated pool may only grow within its own domain, so joint normalization
	// must not use the NUMA-wide available as the ceiling.
	dedicatedPoolCeilings := make(map[string]int)
	// first calculate share and isolate dedicated pool sizes for each pinned region
	for pkgName, pinnedCPUSize := range pinnedCPUSizeByPkg {
		pinnedPoolAvailable := pinnedPoolAvailableByPkg[pkgName]
		allInfo, ok := pinnedCPUSetAllInfo[pkgName]
		if !ok {
			// No regions for this package, so allocated size is 0
			if nonReclaimablePackages.Has(pkgName) {
				totalUnusedNonReclaimablePinnedCPUSize += pinnedPoolAvailable
			}
			continue
		}

		poolSizes := getShareAndIsolateDedicatedPoolSizesFunc(
			pinnedPoolAvailable,
			pinnedReserveByPkg[pkgName],
			allInfo.shareRegionInfo,
			allInfo.dedicatedRegionInfos,
			allInfo.isolationRegionInfo,
		)

		allocatedForPkg := general.SumUpMapValues(poolSizes)
		unusedForPkg := pinnedPoolAvailable - allocatedForPkg
		if nonReclaimablePackages.Has(pkgName) {
			totalUnusedNonReclaimablePinnedCPUSize += unusedForPkg
		}

		for poolName, size := range poolSizes {
			shareAndIsolateDedicatedPoolSizes[poolName] = size
		}
		for poolName := range allInfo.dedicatedRegionInfos.requests {
			dedicatedPoolCeilings[poolName] = pinnedPoolAvailable
		}

		shareInfo.merge(allInfo.shareRegionInfo)
		isolationInfo.merge(allInfo.isolationRegionInfo)
		dedicatedInfo.merge(allInfo.dedicatedRegionInfos)

		general.InfoS("pinned pool info",
			"numaID", numaID,
			"pkgName", pkgName,
			"shareRegionInfo", allInfo.shareRegionInfo,
			"isolationRegionInfo", allInfo.isolationRegionInfo,
			"dedicatedRegionInfos", allInfo.dedicatedRegionInfos,
			"pinnedCPUSize", pinnedCPUSize,
			"pinnedPoolAvailable", pinnedPoolAvailable,
			"poolSizes", poolSizes)
	}

	unpinnedPoolSizes := getShareAndIsolateDedicatedPoolSizesFunc(
		unpinnedShareAndIsolatedDedicatedPoolAvailable,
		unpinnedReserve,
		unpinnedShareRegionInfo,
		unpinnedDedicatedInfo,
		unpinnedIsolationInfo,
	)
	for poolName, size := range unpinnedPoolSizes {
		shareAndIsolateDedicatedPoolSizes[poolName] = size
	}
	for poolName := range unpinnedDedicatedInfo.requests {
		dedicatedPoolCeilings[poolName] = unpinnedShareAndIsolatedDedicatedPoolAvailable
	}

	shareInfo.merge(unpinnedShareRegionInfo)
	isolationInfo.merge(unpinnedIsolationInfo)
	dedicatedInfo.merge(unpinnedDedicatedInfo)

	general.InfoS("unpinned pool info",
		"numaID", numaID,
		"unpinnedShareRegionInfo", unpinnedShareRegionInfo,
		"unpinnedIsolationRegionInfo", unpinnedIsolationInfo,
		"unpinnedDedicatedRegionInfos", unpinnedDedicatedInfo,
		"unpinnedShareAndIsolatedDedicatedPoolAvailable", unpinnedShareAndIsolatedDedicatedPoolAvailable,
		"poolSizes", unpinnedPoolSizes)

	dedicatedPoolSizes := make(map[string]int)
	for poolName := range dedicatedInfo.requests {
		if size, ok := shareAndIsolateDedicatedPoolSizes[poolName]; ok {
			dedicatedPoolSizes[poolName] = size
		}
	}
	dedicatedPoolAvailable := general.SumUpMapValues(dedicatedPoolSizes)
	dedicatedPoolSizeRequirements := getPoolSizeRequirements(dedicatedInfo)
	dedicatedReclaimCoresSize := dedicatedPoolAvailable - general.SumUpMapValues(dedicatedPoolSizeRequirements)
	if ctx.DisableDedicatedCoresOverlapReclaimedCores {
		dedicatedReclaimCoresSize = general.Max(dedicatedReclaimCoresSize, 0)
	}

	for poolName, podSet := range dedicatedInfo.podSet {
		if len(podSet) > 0 && shareAndIsolateDedicatedPoolSizes[poolName] <= 0 {
			return fmt.Errorf("active dedicated pool %q was regulated to zero", poolName)
		}
	}
	for poolName := range isolationInfo.isolationUpperSizes {
		if shareAndIsolateDedicatedPoolSizes[poolName] <= 0 {
			return fmt.Errorf("active isolation pool %q was regulated to zero", poolName)
		}
	}
	for poolName := range shareInfo.regionMap {
		if shareAndIsolateDedicatedPoolSizes[poolName] <= 0 {
			return fmt.Errorf("active shared pool %q was regulated to zero", poolName)
		}
	}

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
		"sharePoolSizeRequirements", getPoolSizeRequirements(shareInfo),
		"isolationUpperSizes", isolationInfo.isolationUpperSizes,
		"isolationLowerSizes", isolationInfo.isolationLowerSizes,
		"shareAndIsolateDedicatedPoolSizes", shareAndIsolateDedicatedPoolSizes,
		"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable,
		"totalUnusedNonReclaimablePinnedCPUSize", totalUnusedNonReclaimablePinnedCPUSize,
		"unpinnedShareAndIsolatedDedicatedPoolAvailable", unpinnedShareAndIsolatedDedicatedPoolAvailable)

	// fill in regulated share-and-isolated pool entries
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

	reclaimPoolData := &reclaimPoolCalculationData{
		shareInfo:                              shareInfo,
		isolationInfo:                          isolationInfo,
		dedicatedInfo:                          dedicatedInfo,
		shareAndIsolateDedicatedPoolSizes:      shareAndIsolateDedicatedPoolSizes,
		dedicatedPoolSizes:                     dedicatedPoolSizes,
		dedicatedReclaimCoresSize:              dedicatedReclaimCoresSize,
		shareAndIsolatedDedicatedPoolAvailable: shareAndIsolatedDedicatedPoolAvailable,
		reservedForReclaim:                     reservedForReclaim,
		nodeEnableReclaim:                      nodeEnableReclaim,
		numaID:                                 numaID,
		totalUnusedNonReclaimablePinnedCPUSize: totalUnusedNonReclaimablePinnedCPUSize,
		reserveHeldOutsidePools:                reserveHeldOutsidePools,
	}

	policy := reclaimPoolCalculationPolicy{
		allowSharedOverlap:    ctx.AllowSharedCoresOverlapReclaimedCores,
		allowDedicatedOverlap: !ctx.DisableDedicatedCoresOverlapReclaimedCores,
	}
	general.InfoS("reclaim pool calculation input",
		"numaID", numaID,
		"nodeEnableReclaim", nodeEnableReclaim,
		"policy", policy,
		"reservedForReclaim", reservedForReclaim,
		"shareAndIsolatedDedicatedPoolAvailable", shareAndIsolatedDedicatedPoolAvailable,
		"shareAndIsolateDedicatedPoolSizes", shareAndIsolateDedicatedPoolSizes,
		"dedicatedPoolSizes", dedicatedPoolSizes,
		"dedicatedPoolSizeRequirements", dedicatedPoolSizeRequirements,
		"dedicatedReclaimCoresSize", dedicatedReclaimCoresSize,
		"totalUnusedNonReclaimablePinnedCPUSize", totalUnusedNonReclaimablePinnedCPUSize,
		"shareRequirements", shareInfo.requirements,
		"shareReclaimEnable", shareInfo.reclaimEnable,
		"dedicatedRequirements", dedicatedInfo.requirements,
		"dedicatedReclaimEnable", dedicatedInfo.reclaimEnable)
	reclaimedCoresSize, _, reclaimedCoresQuota, err := pa.calculateReclaimPool(reclaimPoolData, policy)
	if err != nil {
		return err
	}

	ratio := ctx.DynamicConfiguration.ReclaimedCPUMaxRatio
	cpuCount := 0
	if ratio > 0 {
		cpuCount = pa.cpuCountInNUMAs(numaSet)
	}
	clamp := reclaimClampResult{
		RawSize:    reclaimedCoresSize,
		FinalSize:  reclaimedCoresSize,
		FinalLimit: reclaimedCoresQuota,
	}
	if ratio <= 0 || cpuCount > 0 {
		cpusPerCore, err := pa.reclaimRatioCPUsPerCore(ctx, numaID)
		if err != nil {
			return err
		}
		clamp, err = clampByReclaimedCPUMaxRatioWithDiagnostics(
			reclaimedCoresSize,
			reclaimedCoresQuota,
			ratio,
			cpuCount,
			reservedForReclaim,
			cpusPerCore,
		)
		if err != nil {
			return fmt.Errorf("clamp reclaim by max ratio: %w", err)
		}
		// preserve the original write semantics: the final reclaim size and
		// quota are exactly what the underlying helper returns.
		reclaimedCoresSize = clamp.FinalSize
		reclaimedCoresQuota = clamp.FinalLimit
	}
	// The ramp-up target is no longer enforced here. It is the desired value the
	// per-scope optional ceiling tracks; the single quantity boundary is the
	// ApplyReclaimConstraint clamp below, which honors the rate-limited ceiling
	// while the steady MaxRatio clamp above remains the upper bound.
	// steadyCap is the steady (MaxRatio) size captured before any live-floor raise,
	// so the descriptive SteadyCap the guard sees is the pool's no-ramp-up upper
	// bound, not a value already lifted by live continuity.
	steadyCap := reclaimedCoresSize

	// Domain-scoped live reclaim continuity (best-effort). Reuse the extracted
	// helper so the live floor is defined in exactly one place; the global
	// (faked) aggregate pool has no per-real-NUMA live floor here -- its
	// continuity is seeded by the per-scope ceiling state machine instead.
	reclaimedCoresSize = pa.applyDomainScopedLiveReclaimFloor(ctx, numaID, reclaimedCoresSize, shareAndIsolateDedicatedPoolSizes)

	constraintScope := NewNonExclusiveReclaimConstraintScope(numaID)
	// The descriptive desired for this scope comes from the ramp-up domain built
	// by the advisor. When no domain targets this scope (e.g. draining back to
	// steady), the desired reverts to steadyCap so the guard publishes a coherent
	// contract. The actual quantity decision is the optional ceiling clamp below.
	domainTarget := ctx.ReclaimDomainTargets[constraintScope]
	desiredReclaimed := domainTarget.Desired
	if desiredReclaimed == 0 {
		desiredReclaimed = steadyCap
	}
	var constraintExcess int
	reclaimedCoresSize, reclaimedCoresQuota, constraintExcess = ApplyReclaimConstraint(
		constraintScope,
		reclaimedCoresSize,
		reclaimedCoresQuota,
		reservedForReclaim,
		ctx.ReclaimConstraint,
		ctx.ReclaimCeilings,
		ctx.ReclaimActiveScopes,
	)
	memberNUMAs := numaSet.ToSliceInt()
	if domainTarget.MemberNUMAs != nil {
		memberNUMAs = domainTarget.MemberNUMAs
	}
	RecordReclaimConstraintTarget(result, ctx.ReclaimConstraint,
		constraintScope, types.ReclaimConstraintTarget{
			Desired:     desiredReclaimed,
			Floor:       reservedForReclaim,
			SteadyCap:   steadyCap,
			Ceiling:     ctx.ReclaimCeilings[constraintScope],
			MemberNUMAs: memberNUMAs,
		}, constraintExcess)
	clamp.FinalSize = reclaimedCoresSize
	clamp.FinalLimit = reclaimedCoresQuota
	clamp.ReleasedSize = general.Max(0, clamp.RawSize-clamp.FinalSize)
	// accumulate the default share backfill diagnostics for this scope
	// once; released cores from exclusive NUMA scopes are intentionally
	// excluded and must not be counted here.
	result.DefaultShareBackfill.RawReclaimSize += clamp.RawSize
	result.DefaultShareBackfill.FinalReclaimSize += clamp.FinalSize
	result.DefaultShareBackfill.ReleasedReclaimSize += clamp.ReleasedSize

	overlapBudget := reclaimedCoresSize
	if effectiveHard {
		overlapBudget = general.Max(overlapBudget-reservedForReclaim, 0)
	}
	overlapReclaimedCoresSize := clampReclaimOverlapMetadata(
		result,
		numaID,
		overlapBudget,
		reclaimPoolData.overlapAtoms...,
	)
	// Publish the effective reclaim target directly. reservedForReclaim was
	// already consumed once as the steady floor / ApplyReclaimConstraint input
	// (floor encoding); subtracting it here a second time under-published the
	// FakeNUMA hard pool by the reserve value and erased the floor guarantee when
	// the floor bound. Both scopes now publish through the same single formula.
	nonOverlapReclaimedCoresSize := effectiveReclaimedCoresSize(reclaimedCoresSize, overlapReclaimedCoresSize)
	// publishedEffectiveTarget is the baseline for the publishDelta probe. It is the
	// constrained target for the FakeNUMA aggregate path, which has no whole-core
	// normalization. For a real NUMA that gets jointly rounded below, the baseline is
	// rewritten to the rounded reclaim size plus the overlap metadata: the rounding
	// is a controlled, size-conserving reclaim<->dedicated adjustment (the released /
	// gained CPUs move with the single dedicated pool), not a defect, so it must not
	// surface as a non-zero delta. The invariant (entry + overlap == baseline) holds
	// in the target-driven domain; after whole-core normalization it is measured
	// against the normalized baseline, where the residual rounding error is by design.
	publishedEffectiveTarget := reclaimedCoresSize
	// No intermediate "reclaim pool calculation output" snapshot is logged here: the
	// nonOverlap / quota values are still pre-normalization and may be rewritten by
	// the real-NUMA joint normalization below, so logging them now would reproduce
	// the stale-snapshot / "log prints 8, pool lands 4" observation split. The single
	// terminal log after the quantity decision is the only authoritative record.
	// Jointly align the non-overlap reclaim target to a whole-core value before
	// publication, only on a real NUMA under the three hard-partition gates. The
	// adjustment keeps the controlled partition (non-overlap reclaim + the single
	// dedicated pool) size-conserved: releasing reclaim returns CPUs to dedicated,
	// growing reclaim takes them from dedicated headroom. share and isolation pools
	// are never moved, and multi-source (more than one dedicated pool) passes through
	// because there is no per-pool provenance to assign the core.
	if numaID != commonstate.FakedNUMAID && pa.metaServer != nil && pa.metaServer.CPUTopology != nil &&
		nodeEnableReclaim && effectiveHard && ctx.DisableDedicatedCoresOverlapReclaimedCores {
		joint := jointlyNormalizeNonExclusiveTargets(nonExclusiveJointInput{
			CPUsPerCore:        pa.metaServer.CPUTopology.CPUsPerCore(),
			ReclaimTarget:      nonOverlapReclaimedCoresSize,
			ReservedForReclaim: reservedForReclaim,
			DedicatedPoolSizes: dedicatedPoolSizes,
			DedicatedMinimums:  getPoolSizeRequirements(dedicatedInfo),
			DedicatedCeilings:  dedicatedPoolCeilings,
		})
		switch joint.Decision {
		case nonExclusiveJointRoundedDown, nonExclusiveJointRoundedUp:
			general.InfoS("non-exclusive reclaim jointly normalized to whole-core target",
				"numaID", numaID,
				"decision", joint.Decision,
				"beforeReclaim", nonOverlapReclaimedCoresSize,
				"afterReclaim", joint.ReclaimSize,
				"reservedForReclaim", reservedForReclaim)
			nonOverlapReclaimedCoresSize = joint.ReclaimSize
			// The normalized baseline for publishDelta is the rounded reclaim plus the
			// overlap metadata, so the controlled whole-core rounding does not trip the
			// conservation probe.
			publishedEffectiveTarget = joint.ReclaimSize + overlapReclaimedCoresSize
			// quota must not exceed the published non-overlap reclaim size.
			if reclaimedCoresQuota > float64(nonOverlapReclaimedCoresSize) {
				reclaimedCoresQuota = float64(nonOverlapReclaimedCoresSize)
			}
			if nonOverlapReclaimedCoresSize < overlapReclaimedCoresSize {
				general.InfoS("non-exclusive reclaim normalization left overlap larger than non-overlap reclaim",
					"numaID", numaID,
					"nonOverlapReclaimedCoresSize", nonOverlapReclaimedCoresSize,
					"overlapReclaimedCoresSize", overlapReclaimedCoresSize)
			}
			for poolName, newSize := range joint.DedicatedSizes {
				for podUID := range dedicatedInfo.podSet[poolName] {
					result.SetPoolEntry(podUID, numaID, newSize, -1)
				}
			}
		case nonExclusiveJointPassthrough:
			general.InfoS("non-exclusive reclaim leaves non-whole-core target to QRM",
				"numaID", numaID,
				"nonOverlapReclaimedCoresSize", nonOverlapReclaimedCoresSize,
				"dedicatedPoolCount", len(dedicatedPoolSizes),
				"reservedForReclaim", reservedForReclaim)
		}
	}
	// Final-state invariant check and observability. This runs after every
	// adjustment (ratio clamp, live floor, constraint clamp, overlap metadata and
	// whole-core joint normalization) and is emitted as a single log line and
	// metrics sample immediately BEFORE the entry is published, so no value
	// after this point can silently change the published contract.
	// publishDelta is entry + overlap - baseline: 0 under the publish-layer
	// conservation invariant (entry + overlap == publishedEffectiveTarget), and
	// positive precisely when overlap overshoots the target. The baseline is the
	// constrained target for FakeNUMA and the normalized target for real NUMA.
	publishDelta := nonOverlapReclaimedCoresSize + overlapReclaimedCoresSize - publishedEffectiveTarget
	invariantStatus := "ok"
	switch {
	case nonOverlapReclaimedCoresSize < 0:
		invariantStatus = "negative_entry"
	// overlap(n) must not exceed the allocatable target itself. The previous
	// entry < overlap test is equivalent to overlap > target/2 for the convergent
	// formula (entry = target - overlap), which falsely flags legitimate high
	// overlap. A real double-count only exists when overlap exceeds the target.
	//
	// This case is unreachable BY CONSTRUCTION: overlapReclaimedCoresSize is the
	// return of clampReclaimOverlapMetadata, which bounds overlap to overlapBudget
	// (== reclaimedCoresSize, or reclaimedCoresSize - reservedForReclaim under the
	// effective-hard floor), so overlap <= reclaimedCoresSize always. It is kept on
	// purpose as a regression probe: if a future edit ever widens the overlap budget
	// or stops clamping, this lights up metricReclaimInvariantViolationTotal instead
	// of silently publishing a double-counted reclaim. Removing it would trade a
	// one-line, zero-cost observability hook for a silent-contract-broken risk.
	case overlapReclaimedCoresSize > reclaimedCoresSize:
		invariantStatus = "overlap_double_count"
	case reclaimedCoresQuota >= 0 && reclaimedCoresQuota > float64(nonOverlapReclaimedCoresSize):
		invariantStatus = "quota_gt_size"
	}
	if pa.emitter != nil {
		tags := []metrics.MetricTag{{Key: "numa_id", Val: fmt.Sprintf("%d", numaID)}}
		_ = pa.emitter.StoreInt64(metricReclaimPublishDelta, int64(publishDelta), metrics.MetricTypeNameRaw, tags...)
		if invariantStatus != "ok" {
			_ = pa.emitter.StoreInt64(metricReclaimInvariantViolationTotal, 1, metrics.MetricTypeNameCount,
				append(tags, metrics.MetricTag{Key: "kind", Val: invariantStatus})...)
		}
	}

	ceiling := -1
	if c, ok := ctx.ReclaimCeilings[constraintScope]; ok && c != nil {
		ceiling = *c
	}
	general.InfoS("assemble reclaim pool entry",
		"numaID", numaID,
		"desired", desiredReclaimed,
		"effectiveTarget", reclaimedCoresSize,
		"ceiling", ceiling,
		"floor", reservedForReclaim,
		"overlapReclaimedCoresSize", overlapReclaimedCoresSize,
		"publishedEntry", nonOverlapReclaimedCoresSize,
		"memberNUMAs", memberNUMAs,
		"reclaimedCoresQuota", reclaimedCoresQuota,
		"publishDelta", publishDelta,
		"invariant", invariantStatus)

	result.SetPoolEntry(commonstate.PoolNameReclaim, numaID, nonOverlapReclaimedCoresSize, reclaimedCoresQuota)

	return nil
}
