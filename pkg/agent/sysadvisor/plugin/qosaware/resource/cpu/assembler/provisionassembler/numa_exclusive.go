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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func (pa *ProvisionAssemblerCommon) assembleDedicatedNUMAExclusiveRegion(ctx ProvisionContext, r region.QoSRegion, result *types.InternalCPUCalculationResult) error {
	if !ctx.DisableDedicatedCoresOverlapReclaimedCores {
		return pa.assembleLegacyDedicatedNUMAExclusiveRegion(ctx, r, result)
	}

	controlKnob, err := r.GetProvision()
	if err != nil {
		return err
	}

	bindingNumas := r.GetBindingNumas()
	if bindingNumas.IsEmpty() {
		return fmt.Errorf("dedicated NUMA-exclusive region %q has empty binding NUMAs", r.Name())
	}
	regionNuma := bindingNumas.ToSliceInt()[0] // always one binding numa for this type of region
	reservedForReclaim := pa.effectiveReservedForReclaim(ctx, r.GetBindingNumas())
	available := getNUMAsResource(ctx.NUMAAvailable, r.GetBindingNumas())
	partitionCapacity, dedicatedCapacity, reclaimCapacity, err := pa.getExclusivePartitionCapacities(ctx, r, regionNuma, available)
	if err != nil {
		return err
	}

	ratioPhysicalCap := 0
	if ratio := ctx.DynamicConfiguration.ReclaimedCPUMaxRatio; ratio > 0 {
		if cpuCount := pa.cpuCountInNUMAs(r.GetBindingNumas()); cpuCount > 0 {
			cpusPerCore, err := pa.reclaimRatioCPUsPerCore(ctx, regionNuma)
			if err != nil {
				return err
			}
			ratioPhysicalCap, err = calculateReclaimRatioCap(
				cpuCount, ratio, reservedForReclaim, cpusPerCore)
			if err != nil {
				return fmt.Errorf("calculate reclaim ratio cap: %w", err)
			}
		}
	}

	nonReclaimed := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
	dedicatedTarget, reclaimTarget, err := calculateExclusiveDisjointTargets(exclusivePartitionInput{
		PartitionCapacity: partitionCapacity,
		DedicatedCapacity: dedicatedCapacity,
		ReclaimCapacity:   reclaimCapacity,
		Reserved:          reservedForReclaim,
		NonReclaimed:      nonReclaimed,
		EnableReclaim:     r.EnableReclaim(),
		RatioPhysicalCap:  ratioPhysicalCap,
	})
	if err != nil {
		return fmt.Errorf("calculate disjoint targets for exclusive region %q: %w", r.Name(), err)
	}

	reclaimQuotaLimit := float64(-1)
	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return err
	}
	if quotaCtrlKnobEnabled {
		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			reclaimQuotaLimit = calculateReclaimQuotaLimit(reclaimTarget, quota.Value, ratioPhysicalCap)
		}
	}
	originalReclaimTarget := reclaimTarget
	constraintScope := NewExclusiveReclaimConstraintScope(r.Name())
	desiredReclaimTarget := reclaimTarget
	var constraintExcess int
	reclaimTarget, reclaimQuotaLimit, constraintExcess = ApplyReclaimConstraint(
		constraintScope,
		reclaimTarget,
		reclaimQuotaLimit,
		reservedForReclaim,
		ctx.ReclaimConstraint,
		ctx.ReclaimCeilings,
		ctx.ReclaimActiveScopes,
	)
	// Exclusive-region invariant: an exclusive (NUMA-binding) region recomputes
	// its own reclaim pool size above and the guard never drains it back. We
	// therefore borrow the desired as the SteadyCap, so the published ceiling
	// converges immediately (ceiling >= SteadyCap) and the scope retires rather
	// than back-pulling the exclusive region. It is NOT the true MaxRatio upper
	// bound -- there is no steady pool to return to for an exclusive region.
	RecordReclaimConstraintTarget(result, ctx.ReclaimConstraint,
		constraintScope, types.ReclaimConstraintTarget{
			Desired:     desiredReclaimTarget,
			Floor:       reservedForReclaim,
			SteadyCap:   desiredReclaimTarget,
			MemberNUMAs: r.GetBindingNumas().ToSliceInt(),
		}, constraintExcess)
	if reclaimTarget > reclaimCapacity {
		return fmt.Errorf(
			"ramp-up reclaim target %d exceeds reclaim capacity %d for exclusive region %q",
			reclaimTarget,
			reclaimCapacity,
			r.Name(),
		)
	}
	if reclaimTarget != originalReclaimTarget {
		nextDedicatedTarget := partitionCapacity - reclaimTarget
		if nextDedicatedTarget > dedicatedCapacity {
			return fmt.Errorf(
				"dedicated target %d exceeds dedicated capacity %d after ramp-up reclaim cap for exclusive region %q",
				nextDedicatedTarget,
				dedicatedCapacity,
				r.Name(),
			)
		}
		dedicatedTarget = nextDedicatedTarget
	}

	// Quantize the final real-NUMA dedicated/reclaim target to a whole-core value:
	// an odd reclaim target cannot be expressed as complete physical cores, which
	// QRM materializes as whole cores. Rewrite it to the committed-anchored whole-core
	// value so the block we publish is already representable. It is conservative:
	// never shrinks below the reserved reclaim, and passes through when no legal
	// whole-core value exists (QRM owns the reconcile there).
	cpusPerCore := 1
	if pa.metaServer != nil && pa.metaServer.CPUTopology != nil {
		cpusPerCore = pa.metaServer.CPUTopology.CPUsPerCore()
	}
	quantizedReclaimTarget, quantizeDecision := quantizeDisjointReclaimTargetToWholeCore(
		reclaimTarget, reservedForReclaim, cpusPerCore)
	if quantizedReclaimTarget != reclaimTarget {
		nextDedicated := partitionCapacity - quantizedReclaimTarget
		if nextDedicated >= 0 && nextDedicated <= dedicatedCapacity {
			general.InfoS("quantized disjoint reclaim target to whole-core value",
				"regionName", r.Name(),
				"regionNuma", regionNuma,
				"cpusPerCore", cpusPerCore,
				"reservedForReclaim", reservedForReclaim,
				"originalReclaimTarget", reclaimTarget,
				"quantizedReclaimTarget", quantizedReclaimTarget,
				"decision", quantizeDecision)
			reclaimTarget = quantizedReclaimTarget
			dedicatedTarget = nextDedicated
		} else {
			general.InfoS("passing through non-whole-core reclaim target: quantized value out of dedicated bounds",
				"regionName", r.Name(),
				"originalReclaimTarget", reclaimTarget,
				"quantizedReclaimTarget", quantizedReclaimTarget,
				"dedicatedCapacity", dedicatedCapacity,
				"decision", quantizeDecision)
		}
	}

	for podUID := range r.GetPods() {
		result.SetPoolEntry(podUID, regionNuma, dedicatedTarget, -1)
	}
	result.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, reclaimTarget, reclaimQuotaLimit)

	general.InfoS("assemble disjoint dedicated NUMA-exclusive region",
		"regionName", r.Name(),
		"partitionCapacity", partitionCapacity,
		"dedicatedCapacity", dedicatedCapacity,
		"reclaimCapacity", reclaimCapacity,
		"dedicatedTarget", dedicatedTarget,
		"reclaimTarget", reclaimTarget,
		"reclaimQuotaLimit", reclaimQuotaLimit,
		"reclaimConstraint", ctx.ReclaimConstraint,
		"reservedForReclaim", reservedForReclaim,
		"ratioPhysicalCap", ratioPhysicalCap)
	return nil
}

func (pa *ProvisionAssemblerCommon) getExclusivePartitionCapacities(
	ctx ProvisionContext,
	r region.QoSRegion,
	regionNuma, available int,
) (partition, dedicated, reclaim int, err error) {
	if pa.metaServer == nil || pa.metaServer.CPUDetails == nil {
		return 0, 0, 0, fmt.Errorf("CPU topology is unavailable for NUMA %d", regionNuma)
	}

	availableCPUSet := pa.metaServer.CPUDetails.CPUsInNUMANodes(regionNuma)
	if reservePool, ok := pa.metaReader.GetPoolInfo(commonstate.PoolNameReserve); ok && reservePool != nil {
		availableCPUSet = availableCPUSet.Difference(reservePool.TopologyAwareAssignments[regionNuma])
	}
	pa.metaReader.RangePool(func(poolName string, poolInfo *types.PoolInfo) bool {
		if poolInfo != nil && (qrmstate.ForbiddenPools.Has(poolName) || commonstate.IsSystemPool(poolName)) {
			availableCPUSet = availableCPUSet.Difference(poolInfo.TopologyAwareAssignments[regionNuma])
		}
		return true
	})
	if availableCPUSet.Size() != available {
		return 0, 0, 0, fmt.Errorf(
			"available CPUSet size %d for NUMA %d does not match numaAvailable %d",
			availableCPUSet.Size(), regionNuma, available,
		)
	}

	cfg := pa.metaReader.GetResourcePackageConfig()
	pkgMap := cfg[regionNuma]
	allPinned := machine.NewCPUSet()
	for _, state := range pkgMap {
		if state != nil {
			allPinned = allPinned.Union(state.PinnedCPUSet)
		}
	}

	disableReclaimSelector, err := general.ParseSelector(
		ctx.DynamicConfiguration.DisableReclaimPinnedCPUSetResourcePackageSelector,
	)
	if err != nil {
		return 0, 0, 0, err
	}
	nonReclaimablePinned := resourcepackage.GetMatchedPinnedCPUSet(pkgMap, disableReclaimSelector)

	pkgName := r.GetResourcePackageName()
	dedicatedCPUSet := machine.NewCPUSet()
	if pkgName == "" {
		dedicatedCPUSet = availableCPUSet.Difference(allPinned)
	} else if state, ok := pkgMap[pkgName]; ok && state != nil {
		dedicatedCPUSet = availableCPUSet.Intersection(state.PinnedCPUSet)
	}

	reclaimCPUSet := availableCPUSet.Difference(nonReclaimablePinned)
	partitionCPUSet := dedicatedCPUSet.Union(reclaimCPUSet)
	return partitionCPUSet.Size(), dedicatedCPUSet.Size(), reclaimCPUSet.Size(), nil
}

func (pa *ProvisionAssemblerCommon) assembleLegacyDedicatedNUMAExclusiveRegion(ctx ProvisionContext, r region.QoSRegion, result *types.InternalCPUCalculationResult) error {
	controlKnob, err := r.GetProvision()
	if err != nil {
		return err
	}

	bindingNumas := r.GetBindingNumas()
	if bindingNumas.IsEmpty() {
		return fmt.Errorf("dedicated NUMA-exclusive region %q has empty binding NUMAs", r.Name())
	}
	regionNuma := bindingNumas.ToSliceInt()[0] // always one binding numa for this type of region
	reservedForReclaim := pa.effectiveReservedForReclaim(ctx, r.GetBindingNumas())
	available := getNUMAsResource(ctx.NUMAAvailable, r.GetBindingNumas())
	var reclaimedCoresSize int
	reclaimedCoresLimit := float64(-1)

	// fill in reclaim pool entry for dedicated numa exclusive regions
	nonReclaimRequirement := int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
	if !r.EnableReclaim() {
		nonReclaimRequirement = available
	}

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return err
	}

	if quotaCtrlKnobEnabled {
		reclaimedCoresSize = available
		reclaimedCoresLimit = general.MaxFloat64(float64(reservedForReclaim), float64(available-nonReclaimRequirement))

		if quota, ok := controlKnob[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
			reclaimedCoresLimit = general.MinFloat64(reclaimedCoresLimit, quota.Value)
		}
	} else {
		reclaimedCoresSize = general.Max(reservedForReclaim, available-nonReclaimRequirement)
	}

	if ratio := ctx.DynamicConfiguration.ReclaimedCPUMaxRatio; ratio > 0 {
		if cpuCount := pa.cpuCountInNUMAs(r.GetBindingNumas()); cpuCount > 0 {
			cpusPerCore, err := pa.reclaimRatioCPUsPerCore(ctx, regionNuma)
			if err != nil {
				return err
			}
			reclaimedCoresSize, reclaimedCoresLimit, err = clampByReclaimedCPUMaxRatio(
				reclaimedCoresSize,
				reclaimedCoresLimit,
				ratio,
				cpuCount,
				reservedForReclaim,
				cpusPerCore,
			)
			if err != nil {
				return fmt.Errorf("clamp reclaim by max ratio: %w", err)
			}
		}
	}
	constraintScope := NewLegacyExclusiveReclaimConstraintScope(r.Name())
	desiredReclaimedCoresSize := reclaimedCoresSize
	var constraintExcess int
	reclaimedCoresSize, reclaimedCoresLimit, constraintExcess = ApplyReclaimConstraint(
		constraintScope,
		reclaimedCoresSize,
		reclaimedCoresLimit,
		reservedForReclaim,
		ctx.ReclaimConstraint,
		ctx.ReclaimCeilings,
		ctx.ReclaimActiveScopes,
	)
	// Exclusive-region invariant: an exclusive (NUMA-binding) region recomputes its
	// own reclaim pool size above and the guard never drains it back. We therefore
	// borrow the desired as the SteadyCap, so the published ceiling converges
	// immediately and the scope retires rather than back-pulling the exclusive
	// region. It is NOT the true MaxRatio upper bound -- there is no steady pool to
	// return to.
	RecordReclaimConstraintTarget(result, ctx.ReclaimConstraint,
		constraintScope, types.ReclaimConstraintTarget{
			Desired:     desiredReclaimedCoresSize,
			Floor:       reservedForReclaim,
			SteadyCap:   desiredReclaimedCoresSize,
			MemberNUMAs: r.GetBindingNumas().ToSliceInt(),
		}, constraintExcess)
	general.InfoS("assembleDedicatedNUMAExclusive info", "regionName", r.Name(), "reclaimedCoresSize", reclaimedCoresSize,
		"reclaimedCoresLimit", reclaimedCoresLimit,
		"available", available, "nonReclaimRequirement", nonReclaimRequirement,
		"reclaimConstraint", ctx.ReclaimConstraint,
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

	// set reclaim pool cpu limit
	result.SetPoolEntry(commonstate.PoolNameReclaim, regionNuma, 0, reclaimedCoresLimit)
	return nil
}
