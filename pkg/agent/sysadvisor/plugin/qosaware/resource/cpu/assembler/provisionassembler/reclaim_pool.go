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
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// clampReclaimOverlapMetadata bounds generated reclaim-overlap metadata by the
// aggregate reclaim budget. calculateReclaimPool builds the overlap metadata
// before ratio-based clamping may shrink the aggregate size; this keeps both
// outputs from describing conflicting reclaim capacity.
type podContainerAlias struct {
	podUID        string
	containerName string
}

type overlapAtom struct {
	key            string
	size           int
	poolAlias      string
	containerAlias []podContainerAlias
}

func summarizeOverlapAtoms(atoms []overlapAtom) []string {
	if len(atoms) == 0 {
		return nil
	}
	result := make([]string, 0, len(atoms))
	for _, atom := range atoms {
		aliases := make([]string, 0, len(atom.containerAlias))
		for _, alias := range atom.containerAlias {
			aliases = append(aliases, alias.podUID+"/"+alias.containerName)
		}
		sort.Strings(aliases)
		result = append(result, fmt.Sprintf(
			"key=%s,size=%d,pool=%s,containers=%s",
			atom.key, atom.size, atom.poolAlias, strings.Join(aliases, "|"),
		))
	}
	sort.Strings(result)
	return result
}

func clampReclaimOverlapMetadata(
	result *types.InternalCPUCalculationResult,
	numaID, budget int,
	atoms ...overlapAtom,
) int {
	if result == nil {
		return 0
	}
	if budget <= 0 {
		if byNUMA := result.PoolOverlapInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
			delete(byNUMA, numaID)
		}
		if byNUMA := result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
			delete(byNUMA, numaID)
		}
		return 0
	}

	if len(atoms) == 0 {
		if byNUMA := result.PoolOverlapInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
			delete(byNUMA, numaID)
		}
		if byNUMA := result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
			delete(byNUMA, numaID)
		}
		return 0
	}
	sort.Slice(atoms, func(i, j int) bool {
		return atoms[i].key < atoms[j].key
	})

	if byNUMA := result.PoolOverlapInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
		delete(byNUMA, numaID)
	}
	if byNUMA := result.PoolOverlapPodContainerInfo[commonstate.PoolNameReclaim]; byNUMA != nil {
		delete(byNUMA, numaID)
	}

	remaining := budget
	actual := 0
	for _, atom := range atoms {
		if atom.size <= 0 || remaining == 0 {
			continue
		}
		size := general.Min(atom.size, remaining)
		remaining -= size
		actual += size
		if atom.poolAlias != "" {
			result.SetPoolOverlapInfo(commonstate.PoolNameReclaim, numaID, atom.poolAlias, size)
		}
		for _, alias := range atom.containerAlias {
			result.SetPoolOverlapPodContainerInfo(
				commonstate.PoolNameReclaim,
				numaID,
				alias.podUID,
				alias.containerName,
				size,
			)
		}
	}
	return actual
}

// Publish-layer observability for the reclaim pool. reclaim_publish_delta is the
// direct probe of the historical double-deduction defect: target minus the
// published non-overlap entry plus overlap metadata, which must stay ~0.
const (
	metricReclaimPublishDelta            = "cpu_advisor_reclaim_publish_delta"
	metricReclaimInvariantViolationTotal = "cpu_advisor_reclaim_invariant_violation_total"
)

// effectiveReclaimedCoresSize returns the reclaim pool entry size to publish for
// one scope (a real NUMA or the global FakedNUMAID aggregate): the constrained,
// already-quantified reclaim target minus the portion of that target that was
// already published as overlap metadata, clamped to the non-negative domain.
//
// reservedForReclaim is intentionally NOT adjusted here. In the target-driven
// model the reservation is consumed exactly once as a floor/constraint input at
// target derivation (ApplyReclaimConstraint / the steady floor), so no additive
// reserve component exists at this publish scope to net out. Subtracting reserve
// here a second time under floor encoding has no correct regime: it under-publishes
// when the floor is inactive and fully erases the reservation guarantee when the
// floor binds. Both the FakeNUMA aggregate path and the real-NUMA per-NUMA path
// converge on this single formula so no residual reservedForReclaim adjustment
// can survive at the publish layer.
func effectiveReclaimedCoresSize(reclaimTarget, overlapReclaimedCoresSize int) int {
	return general.Max(reclaimTarget-overlapReclaimedCoresSize, 0)
}

type reclaimPoolCalculationData struct {
	shareInfo                              regionInfo
	isolationInfo                          isolationRegionInfo
	dedicatedInfo                          regionInfo
	shareAndIsolateDedicatedPoolSizes      map[string]int
	dedicatedPoolSizes                     map[string]int
	dedicatedReclaimCoresSize              int
	shareAndIsolatedDedicatedPoolAvailable int
	reservedForReclaim                     int
	nodeEnableReclaim                      bool
	numaID                                 int
	totalUnusedNonReclaimablePinnedCPUSize int
	reserveHeldOutsidePools                bool
	overlapAtoms                           []overlapAtom
}

type reclaimPoolCalculationPolicy struct {
	allowSharedOverlap    bool
	allowDedicatedOverlap bool
}

func (pa *ProvisionAssemblerCommon) calculateReclaimPool(
	data *reclaimPoolCalculationData,
	policy reclaimPoolCalculationPolicy,
) (int, int, float64, error) {
	if data.nodeEnableReclaim && len(data.dedicatedInfo.requests) > 0 {
		return pa.calculateEnabledReclaimPool(data, policy)
	}
	if policy.allowSharedOverlap {
		return pa.calculateOverlapReclaimPool(data, policy)
	}
	return pa.calculateNonOverlapReclaimPool(data, policy)
}

func (pa *ProvisionAssemblerCommon) calculateEnabledReclaimPool(
	data *reclaimPoolCalculationData,
	policy reclaimPoolCalculationPolicy,
) (int, int, float64, error) {
	overlapSize := 0
	if policy.allowSharedOverlap {
		for poolName, requirement := range data.shareInfo.requirements {
			if !data.shareInfo.reclaimEnable[poolName] {
				continue
			}
			size := data.shareAndIsolateDedicatedPoolSizes[poolName] - requirement
			if size <= 0 {
				continue
			}
			data.overlapAtoms = append(data.overlapAtoms, overlapAtom{
				key:       "0/pool/" + poolName,
				size:      size,
				poolAlias: poolName,
			})
			overlapSize += size
		}
	}
	if policy.allowDedicatedOverlap {
		for poolName, requirement := range data.dedicatedInfo.requirements {
			if !data.dedicatedInfo.reclaimEnable[poolName] {
				continue
			}
			size := data.dedicatedPoolSizes[poolName] - requirement
			if size <= 0 {
				continue
			}
			podSet := data.dedicatedInfo.podSet[poolName]
			if len(podSet) == 0 {
				continue
			}
			atom := overlapAtom{
				key:  "1/dedicated/" + poolName,
				size: size,
			}
			for podUID, containerSet := range podSet {
				for containerName := range containerSet {
					atom.containerAlias = append(atom.containerAlias, podContainerAlias{
						podUID:        podUID,
						containerName: containerName,
					})
				}
			}
			data.overlapAtoms = append(data.overlapAtoms, atom)
			overlapSize += size
		}
	}

	physicalUsage := general.SumUpMapValues(data.shareAndIsolateDedicatedPoolSizes)
	freeStandalone := general.Max(
		data.shareAndIsolatedDedicatedPoolAvailable-physicalUsage-data.totalUnusedNonReclaimablePinnedCPUSize,
		0,
	)
	reclaimedCoresSize := freeStandalone + overlapSize
	if data.reserveHeldOutsidePools {
		reclaimedCoresSize += data.reservedForReclaim
	}
	reclaimedCoresQuota := float64(-1)
	general.InfoS("enabled reclaim pool calculation",
		"numaID", data.numaID,
		"policy", policy,
		"physicalUsage", physicalUsage,
		"freeStandalone", freeStandalone,
		"overlapSize", overlapSize,
		"reclaimedCoresSizeBeforeQuota", reclaimedCoresSize,
		"shareAndIsolatedDedicatedPoolAvailable", data.shareAndIsolatedDedicatedPoolAvailable,
		"totalUnusedNonReclaimablePinnedCPUSize", data.totalUnusedNonReclaimablePinnedCPUSize,
		"shareAndIsolateDedicatedPoolSizes", data.shareAndIsolateDedicatedPoolSizes,
		"dedicatedPoolSizes", data.dedicatedPoolSizes,
		"dedicatedReclaimCoresSize", data.dedicatedReclaimCoresSize,
		"reservedForReclaim", data.reservedForReclaim,
		"overlapAtoms", summarizeOverlapAtoms(data.overlapAtoms))

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return 0, 0, 0, err
	}
	if quotaCtrlKnobEnabled && data.numaID != commonstate.FakedNUMAID && physicalUsage > 0 {
		reclaimedCoresQuota = float64(reclaimedCoresSize)
		for _, quota := range []float64{
			data.shareInfo.minReclaimedCoresCPUQuota,
			data.dedicatedInfo.minReclaimedCoresCPUQuota,
		} {
			if quota >= 0 {
				reclaimedCoresQuota = general.MinFloat64(reclaimedCoresQuota, quota)
			}
		}
		reclaimedCoresQuota = general.MaxFloat64(reclaimedCoresQuota, float64(data.reservedForReclaim))
	}
	general.InfoS("enabled reclaim pool calculation result",
		"numaID", data.numaID,
		"reclaimedCoresSize", reclaimedCoresSize,
		"overlapSize", overlapSize,
		"reclaimedCoresQuota", reclaimedCoresQuota,
		"quotaCtrlKnobEnabled", quotaCtrlKnobEnabled,
		"overlapAtoms", summarizeOverlapAtoms(data.overlapAtoms))
	return reclaimedCoresSize, overlapSize, reclaimedCoresQuota, nil
}

func (pa *ProvisionAssemblerCommon) calculateOverlapReclaimPool(
	data *reclaimPoolCalculationData,
	policy reclaimPoolCalculationPolicy,
) (int, int, float64, error) {
	var reclaimedCoresSize, overlapReclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)

	isolated := 0
	poolSizes := make(map[string]int)
	sharePoolSizes := make(map[string]int)
	reclaimablePoolSizes := make(map[string]int)
	nonReclaimableSharePoolSizes := make(map[string]int)
	reclaimableShareRequirements := make(map[string]int)
	reclaimableRequirements := make(map[string]int)

	for poolName, size := range data.shareAndIsolateDedicatedPoolSizes {
		_, ok := data.shareInfo.requirements[poolName]
		if ok {
			if data.shareInfo.reclaimEnable[poolName] {
				reclaimablePoolSizes[poolName] = size
				reclaimableShareRequirements[poolName] = data.shareInfo.requirements[poolName]
				reclaimableRequirements[poolName] = data.shareInfo.requirements[poolName]
			} else {
				nonReclaimableSharePoolSizes[poolName] = size
			}
			poolSizes[poolName] = size
			sharePoolSizes[poolName] = size
		}

		_, ok = data.isolationInfo.isolationUpperSizes[poolName]
		if ok {
			isolated += size
		}

		_, ok = data.dedicatedInfo.requests[poolName]
		if ok {
			if policy.allowDedicatedOverlap && data.dedicatedInfo.reclaimEnable[poolName] {
				reclaimablePoolSizes[poolName] = size
				reclaimableRequirements[poolName] = data.dedicatedInfo.requirements[poolName]
			}
			poolSizes[poolName] = size
		}
	}

	overlapReclaimSize := make(map[string]int)
	// We deduct totalUnusedNonReclaimablePinnedCPUSize here to ensure that the unused portion of non-reclaimable
	// resource packages is not added to the reclaim pool, preventing those CPUs from being reclaimed.
	shareReclaimCoresSize := data.shareAndIsolatedDedicatedPoolAvailable - isolated -
		general.SumUpMapValues(nonReclaimableSharePoolSizes) - general.SumUpMapValues(reclaimableShareRequirements) -
		general.SumUpMapValues(data.dedicatedPoolSizes) - data.totalUnusedNonReclaimablePinnedCPUSize

	if data.nodeEnableReclaim {
		reclaimedCoresSize = shareReclaimCoresSize + data.dedicatedReclaimCoresSize
		if data.reserveHeldOutsidePools {
			reclaimedCoresSize += data.reservedForReclaim
		}
		if reclaimedCoresSize < data.reservedForReclaim {
			reclaimedCoresSize = data.reservedForReclaim
			overlapCandidates := reclaimablePoolSizes
			if len(overlapCandidates) > 0 {
				overlapTarget := general.Min(reclaimedCoresSize, general.SumUpMapValues(overlapCandidates))
				regulatedOverlapReclaimPoolSize, err := regulateOverlapReclaimPoolSize(overlapCandidates, overlapTarget)
				if err != nil {
					return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize for NUMAs reserved for reclaim: %w", err)
				}
				overlapReclaimSize = regulatedOverlapReclaimPoolSize
			}
		} else {
			for poolName, size := range reclaimablePoolSizes {
				requirement, ok := reclaimableRequirements[poolName]
				if !ok {
					continue
				}
				reclaimSize := size - requirement
				if reclaimSize > 0 {
					overlapReclaimSize[poolName] = reclaimSize
				} else {
					overlapReclaimSize[poolName] = 1
				}
			}
		}
	} else {
		reclaimedCoresSize = data.reservedForReclaim
		if len(poolSizes) > 0 && reclaimedCoresSize > shareReclaimCoresSize {
			reclaimedCoresSize = general.Min(reclaimedCoresSize, general.SumUpMapValues(poolSizes))
			var overlapSharePoolSizes map[string]int
			if reclaimedCoresSize <= general.SumUpMapValues(reclaimablePoolSizes) {
				overlapSharePoolSizes = reclaimablePoolSizes
			} else {
				overlapSharePoolSizes = poolSizes
			}

			reclaimSizes, err := regulateOverlapReclaimPoolSize(overlapSharePoolSizes, reclaimedCoresSize)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
			}
			overlapReclaimSize = reclaimSizes
		} else if len(sharePoolSizes) > 0 && reclaimedCoresSize <= general.SumUpMapValues(sharePoolSizes) {
			reclaimSizes, err := regulateOverlapReclaimPoolSize(sharePoolSizes, reclaimedCoresSize)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to regulateOverlapReclaimPoolSize: %w", err)
			}
			overlapReclaimSize = reclaimSizes
		}
	}

	quotaCtrlKnobEnabled, err := metacache.IsQuotaCtrlKnobEnabled(pa.metaReader)
	if err != nil {
		return 0, 0, 0, err
	}

	if quotaCtrlKnobEnabled && data.numaID != commonstate.FakedNUMAID && len(poolSizes) > 0 {
		reclaimedCoresQuota = float64(general.Max(data.reservedForReclaim, reclaimedCoresSize))
		if data.shareInfo.minReclaimedCoresCPUQuota != -1 || data.dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
			if data.shareInfo.minReclaimedCoresCPUQuota != -1 {
				reclaimedCoresQuota = data.shareInfo.minReclaimedCoresCPUQuota
			}

			if data.dedicatedInfo.minReclaimedCoresCPUQuota != -1 {
				reclaimedCoresQuota = general.MinFloat64(reclaimedCoresQuota, data.dedicatedInfo.minReclaimedCoresCPUQuota)
			}

			reclaimedCoresQuota = general.MaxFloat64(reclaimedCoresQuota, float64(data.reservedForReclaim))
		}

		// if cpu quota enabled, set all reclaimable share pool size to reclaimablePoolSizes
		for poolName := range overlapReclaimSize {
			overlapReclaimSize[poolName] = general.Max(overlapReclaimSize[poolName], reclaimablePoolSizes[poolName])
		}
	}

	for overlapPoolName, reclaimSize := range overlapReclaimSize {
		if _, ok := data.shareInfo.requests[overlapPoolName]; ok {
			if !policy.allowSharedOverlap {
				continue
			}
			general.InfoS("set pool overlap info",
				"poolName", commonstate.PoolNameReclaim,
				"numaID", data.numaID,
				"poolName", overlapPoolName,
				"reclaimSize", reclaimSize)
			data.overlapAtoms = append(data.overlapAtoms, overlapAtom{
				key:       "0/pool/" + overlapPoolName,
				size:      reclaimSize,
				poolAlias: overlapPoolName,
			})
			overlapReclaimedCoresSize += reclaimSize
			continue
		}

		if podSet, ok := data.dedicatedInfo.podSet[overlapPoolName]; ok {
			if !policy.allowDedicatedOverlap {
				continue
			}
			// set pool overlap info for dedicated pool
			atom := overlapAtom{
				key:  "1/dedicated/" + overlapPoolName,
				size: reclaimSize,
			}
			for podUID, containerSet := range podSet {
				for containerName := range containerSet {
					general.InfoS("set pool overlap pod container info",
						"poolName", commonstate.PoolNameReclaim,
						"numaID", data.numaID,
						"podUID", podUID,
						"containerName", containerName,
						"reclaimSize", reclaimSize)
					atom.containerAlias = append(atom.containerAlias, podContainerAlias{
						podUID:        podUID,
						containerName: containerName,
					})
				}
			}
			data.overlapAtoms = append(data.overlapAtoms, atom)
			overlapReclaimedCoresSize += reclaimSize
			continue
		}
	}

	return reclaimedCoresSize, overlapReclaimedCoresSize, reclaimedCoresQuota, nil
}

func (pa *ProvisionAssemblerCommon) calculateNonOverlapReclaimPool(
	data *reclaimPoolCalculationData,
	policy reclaimPoolCalculationPolicy,
) (int, int, float64, error) {
	var reclaimedCoresSize, overlapReclaimedCoresSize int
	reclaimedCoresQuota := float64(-1)

	if data.nodeEnableReclaim {
		if policy.allowDedicatedOverlap {
			for poolName, size := range data.dedicatedInfo.requests {
				if data.dedicatedInfo.reclaimEnable[poolName] {
					reclaimSize := size - data.dedicatedInfo.requirements[poolName]
					if reclaimSize <= 0 {
						continue
					}
					if podSet, ok := data.dedicatedInfo.podSet[poolName]; ok {
						atom := overlapAtom{
							key:  "1/dedicated/" + poolName,
							size: reclaimSize,
						}
						for podUID, containerSet := range podSet {
							for containerName := range containerSet {
								general.InfoS("set pool overlap pod container info",
									"poolName", commonstate.PoolNameReclaim,
									"numaID", data.numaID,
									"podUID", podUID,
									"containerName", containerName,
									"reclaimSize", reclaimSize)
								atom.containerAlias = append(atom.containerAlias, podContainerAlias{
									podUID:        podUID,
									containerName: containerName,
								})
							}
						}
						data.overlapAtoms = append(data.overlapAtoms, atom)
						overlapReclaimedCoresSize += reclaimSize
						continue
					}
				}
			}
		}

		// We deduct totalUnusedNonReclaimablePinnedCPUSize here to ensure that the unused portion of non-reclaimable
		// resource packages is not added to the reclaim pool, preventing those CPUs from being reclaimed.
		shareReclaimedCoresSize := data.shareAndIsolatedDedicatedPoolAvailable - general.SumUpMapValues(data.shareAndIsolateDedicatedPoolSizes) - data.totalUnusedNonReclaimablePinnedCPUSize
		reclaimedCoresSize = shareReclaimedCoresSize + data.dedicatedReclaimCoresSize + data.reservedForReclaim
	} else {
		reclaimedCoresSize = data.reservedForReclaim
	}

	general.InfoS("non-overlap reclaim pool calculation result",
		"numaID", data.numaID,
		"policy", policy,
		"nodeEnableReclaim", data.nodeEnableReclaim,
		"reclaimedCoresSize", reclaimedCoresSize,
		"overlapReclaimedCoresSize", overlapReclaimedCoresSize,
		"reclaimedCoresQuota", reclaimedCoresQuota,
		"shareAndIsolatedDedicatedPoolAvailable", data.shareAndIsolatedDedicatedPoolAvailable,
		"shareAndIsolateDedicatedPoolSizes", data.shareAndIsolateDedicatedPoolSizes,
		"dedicatedReclaimCoresSize", data.dedicatedReclaimCoresSize,
		"reservedForReclaim", data.reservedForReclaim,
		"totalUnusedNonReclaimablePinnedCPUSize", data.totalUnusedNonReclaimablePinnedCPUSize,
		"overlapAtoms", summarizeOverlapAtoms(data.overlapAtoms))
	return reclaimedCoresSize, overlapReclaimedCoresSize, reclaimedCoresQuota, nil
}
