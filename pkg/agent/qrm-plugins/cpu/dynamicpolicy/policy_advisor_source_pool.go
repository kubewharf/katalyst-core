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

package dynamicpolicy

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// deriveAdvisorIsolationSourcePool derives the source share pool for a shared_cores
// isolation block from the advisor block owner entries and the current state. It returns
// false on derivation failure so callers can preserve the legacy allocation path.
func deriveAdvisorIsolationSourcePool(block *advisorapi.BlockInfo, entries state.PodEntries) (string, bool) {
	sourcePool, _, ok := deriveAdvisorIsolationSourceDomain(block, entries)
	return sourcePool, ok
}

func deriveAdvisorIsolationSourceDomain(
	block *advisorapi.BlockInfo,
	entries state.PodEntries,
) (string, string, bool) {
	if block == nil {
		return "", "", false
	}

	for ownerPoolName, entry := range block.OwnerPoolEntryMap {
		poolName, resourcePackageName := resourcepackage.UnwrapOwnerPoolName(ownerPoolName)
		if !commonstate.IsIsolationPool(poolName) && !commonstate.IsShareNUMABindingPool(poolName) {
			continue
		}

		allocationInfo := resolveIsolationOwnerAllocation(
			entries, entry.EntryName, entry.SubEntryName, poolName, resourcePackageName)
		if allocationInfo == nil {
			continue
		}
		sourcePool, ok := deriveIsolationSourceSharePool(allocationInfo)
		return sourcePool, resourcePackageName, ok
	}

	return "", "", false
}

// buildAdvisorSourceBlockByPool builds a source pool -> blockID mapping. dedicated,
// isolation, and reclaim blocks are excluded because they are not source share pools.
func buildAdvisorSourceBlockByPool(numaToBlocks map[int][]*advisorapi.BlockInfo) map[string]string {
	sourceBlockByPool := make(map[string]string)
	for _, blocks := range numaToBlocks {
		for _, block := range blocks {
			if block == nil {
				continue
			}
			for ownerPoolName := range block.OwnerPoolEntryMap {
				if ownerPoolName == commonstate.PoolNameDedicated ||
					ownerPoolName == commonstate.PoolNameReclaim ||
					commonstate.IsIsolationPool(ownerPoolName) {
					continue
				}
				if _, found := sourceBlockByPool[ownerPoolName]; !found {
					sourceBlockByPool[ownerPoolName] = block.BlockId
				}
			}
		}
	}
	return sourceBlockByPool
}

func buildAdvisorSourceBlockResultByID(blocks []*advisorapi.BlockInfo, sourceBlockByPool map[string]string) (map[string]int, error) {
	sourceBlockResultByID := make(map[string]int)
	if len(sourceBlockByPool) == 0 {
		return sourceBlockResultByID, nil
	}
	for _, block := range blocks {
		if block == nil {
			continue
		}
		for ownerPoolName := range block.OwnerPoolEntryMap {
			if sourceBlockByPool[ownerPoolName] != block.BlockId {
				continue
			}
			result, err := general.CovertUInt64ToInt(block.Result)
			if err != nil {
				return nil, fmt.Errorf("parse source block: %s result failed with error: %v", block.BlockId, err)
			}
			sourceBlockResultByID[block.BlockId] = result
			break
		}
	}
	return sourceBlockResultByID, nil
}

// allocateAdvisorSourceBlocksForCarve preallocates a normal source share block with a
// sourceResult + isolationResult candidate cpuset. allocateShareBlocks then reuses
// tryCarveAdvisorBlockFromSource to carve isolation from this candidate, leaving the
// source block at the sourceResult size requested by the advisor.
func (p *DynamicPolicy) allocateAdvisorSourceBlocksForCarve(
	sourceShareBlocks []*advisorapi.BlockInfo,
	isolationBlocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	globalNonReclaimableCPUSet machine.CPUSet,
	sourceBlockByPool map[string]string,
) error {
	return p.allocateAdvisorSourceBlocksForCarveWithConstraint(
		sourceShareBlocks, isolationBlocks, blockCPUSet, availableCPUs, nodeRemainingCPUs,
		globalNonReclaimableCPUSet, sourceBlockByPool, frozenReclaimPlacementConstraint{})
}

func (p *DynamicPolicy) allocateAdvisorSourceBlocksForCarveWithConstraint(
	sourceShareBlocks []*advisorapi.BlockInfo,
	isolationBlocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	globalNonReclaimableCPUSet machine.CPUSet,
	sourceBlockByPool map[string]string,
	frozenReclaim frozenReclaimPlacementConstraint,
) error {
	entries := frozenReclaim.entries
	if entries == nil {
		entries = p.state.GetPodEntries()
	}
	isolationQuantityBySourceBlock := make(map[string]int)
	sourceHasOverlapAllowedIsolation := make(map[string]bool)
	for _, block := range isolationBlocks {
		sourcePoolName, ok := deriveAdvisorIsolationSourcePool(block, entries)
		if !ok {
			continue
		}
		sourceBlockID, ok := sourceBlockByPool[sourcePoolName]
		if !ok {
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse isolation block: %s result failed with error: %v", block.BlockId, err)
		}
		if !frozenReclaim.cpus.IsEmpty() && !frozenReclaim.blockDisallowed(block.OwnerPoolEntryMap) {
			sourceHasOverlapAllowedIsolation[sourceBlockID] = true
		}
		isolationQuantityBySourceBlock[sourceBlockID] += blockResult
	}
	if len(isolationQuantityBySourceBlock) == 0 {
		return nil
	}

	for _, block := range sourceShareBlocks {
		if block == nil {
			continue
		}
		if _, found := blockCPUSet[block.BlockId]; found {
			continue
		}

		isolationQuantity := isolationQuantityBySourceBlock[block.BlockId]
		if isolationQuantity == 0 || sourceHasOverlapAllowedIsolation[block.BlockId] {
			continue
		}

		sourceResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse source block: %s result failed with error: %v", block.BlockId, err)
		}

		combinedResult := sourceResult + isolationQuantity
		currentAvailableCPUs := availableCPUs.Difference(globalNonReclaimableCPUSet)
		if frozenReclaim.blockDisallowed(block.OwnerPoolEntryMap) {
			currentAvailableCPUs = currentAvailableCPUs.Difference(frozenReclaim.cpus)
		}
		cpuset, _, err := calculator.TakeByNUMABalance(p.machine.machineInfo, currentAvailableCPUs, combinedResult)
		if err != nil {
			return fmt.Errorf("allocate source block: %s with combined req: %d failed with error: %v",
				block.BlockId, combinedResult, err)
		}

		blockCPUSet[block.BlockId] = cpuset
		*availableCPUs = availableCPUs.Difference(cpuset)
		*nodeRemainingCPUs = nodeRemainingCPUs.Difference(cpuset)
		general.InfoS("preallocated advisor source block for isolation carve",
			"blockID", block.BlockId,
			"sourceResult", sourceResult,
			"isolationResult", isolationQuantity,
			"allocatedCPUSet", cpuset.String())
	}

	return nil
}

// tryCarveAdvisorBlockFromSource tries to carve an isolation block from an already
// allocated source share block. If the source block does not exist or has not been
// allocated yet, it returns carved=false so the caller can continue the legacy path.
func (p *DynamicPolicy) tryCarveAdvisorBlockFromSource(
	block *advisorapi.BlockInfo,
	sourceBlockByPool map[string]string,
	sourceBlockResultByID map[string]int,
	blockCPUSet advisorapi.BlockCPUSet,
	fallbackCandidate machine.CPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	numaID int,
	blockResult int,
) (bool, error) {
	return p.tryCarveAdvisorBlockFromSourceWithEntries(
		block, sourceBlockByPool, sourceBlockResultByID, blockCPUSet, fallbackCandidate,
		availableCPUs, nodeRemainingCPUs, numaID, blockResult, p.state.GetPodEntries())
}

func (p *DynamicPolicy) tryCarveAdvisorBlockFromSourceWithEntries(
	block *advisorapi.BlockInfo,
	sourceBlockByPool map[string]string,
	sourceBlockResultByID map[string]int,
	blockCPUSet advisorapi.BlockCPUSet,
	fallbackCandidate machine.CPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	numaID int,
	blockResult int,
	entries state.PodEntries,
) (bool, error) {
	if block == nil {
		return false, nil
	}
	if _, found := blockCPUSet[block.BlockId]; found {
		return true, nil
	}

	sourcePoolName, ok := deriveAdvisorIsolationSourcePool(block, entries)
	if !ok {
		return false, nil
	}

	sourceBlockID, ok := sourceBlockByPool[sourcePoolName]
	if !ok {
		return false, nil
	}

	sourceCPUSet, ok := blockCPUSet[sourceBlockID]
	if !ok || sourceCPUSet.IsEmpty() {
		return false, nil
	}

	sourceCandidate := sourceCPUSet
	if numaID != commonstate.FakedNUMAID {
		sourceCandidate = sourceCandidate.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
	}
	if sourceResult, ok := sourceBlockResultByID[sourceBlockID]; ok {
		sourceSurplusSize := sourceCPUSet.Size() - sourceResult
		if sourceSurplusSize <= 0 {
			sourceCandidate = machine.NewCPUSet()
		} else if sourceCandidate.Size() > sourceSurplusSize {
			var err error
			sourceCandidate, err = calculator.TakeByTopology(p.machine.machineInfo, sourceCandidate, sourceSurplusSize, true)
			if err != nil {
				return false, fmt.Errorf("reserve source block: %s result: %d failed with error: %v",
					sourceBlockID, sourceResult, err)
			}
		}
	}

	carveCandidates := sourceCandidate.Union(fallbackCandidate)
	taken, remainingCandidates, err := p.takeByTieredPreferredCPUs(carveCandidates, []machine.CPUSet{sourceCandidate}, blockResult)
	if err != nil {
		return false, fmt.Errorf("carve advisor block: %s from source pool: %s failed with error: %v",
			block.BlockId, sourcePoolName, err)
	}

	takenFromSource := taken.Intersection(sourceCPUSet)
	takenFromFallback := taken.Difference(sourceCPUSet)
	blockCPUSet[block.BlockId] = taken
	blockCPUSet[sourceBlockID] = sourceCPUSet.Difference(takenFromSource)
	*availableCPUs = availableCPUs.Difference(takenFromFallback)
	*nodeRemainingCPUs = nodeRemainingCPUs.Difference(takenFromFallback)

	general.InfoS("carved advisor block from source share block",
		"blockID", block.BlockId,
		"sourcePoolName", sourcePoolName,
		"sourceBlockID", sourceBlockID,
		"taken", taken.String(),
		"remainingCandidates", remainingCandidates.String())
	return true, nil
}

// planDisjointAdvisorBlocks materializes the negotiated descriptor plan in strict
// phase order. Every dynamic phase receives descriptors sorted by stable owner
// identity, and no partial result escapes on an infeasible phase.
func (p *DynamicPolicy) planDisjointAdvisorBlocks(
	resp *advisorapi.ListAndWatchResponse,
	hardActive bool,
) (advisorapi.BlockCPUSet, error) {
	return p.planDisjointAdvisorBlocksWithCheckpointTransition(resp, hardActive, nil)
}

func (p *DynamicPolicy) planDisjointAdvisorBlocksWithCheckpointTransition(
	resp *advisorapi.ListAndWatchResponse,
	hardActive bool,
	checkpointTransition *steadyFakeNUMAMigrationCheckpointTransition,
) (advisorapi.BlockCPUSet, error) {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.conf != nil {
		dynamicConf = p.config.conf.GetDynamicConfiguration()
	}
	return p.planDisjointAdvisorBlocksWithCheckpointTransitionAndDynamicConfig(
		resp, hardActive, checkpointTransition, dynamicConf, frozenReclaimPlacementConstraint{})
}

func (p *DynamicPolicy) planDisjointAdvisorBlocksWithCheckpointTransitionAndDynamicConfig(
	resp *advisorapi.ListAndWatchResponse,
	hardActive bool,
	checkpointTransition *steadyFakeNUMAMigrationCheckpointTransition,
	dynamicConf *dynamicconfig.Configuration,
	frozenReclaim frozenReclaimPlacementConstraint,
) (advisorapi.BlockCPUSet, error) {
	topology := p.machine.machineInfo.CPUTopology
	allCPUs := topology.CPUDetails.CPUs()
	podEntries, machineState, _ := p.state.GetAdvisorStateSnapshot()
	rpPinnedCPUSet := machineState.GetResourcePackagePinnedCPUSet()

	if dynamicConf == nil {
		return nil, fmt.Errorf("disjoint advisor planning requires dynamic configuration")
	}
	selectorText := dynamicConf.DisableReclaimPinnedCPUSetResourcePackageSelector
	disableReclaimSelector, err := general.ParseSelector(selectorText)
	if err != nil {
		return nil, err
	}
	nonReclaimableCPUSet := cpuutil.GetAggResourcePackagePinnedCPUSet(disableReclaimSelector, machineState)
	descriptors, err := buildAdvisorBlockDescriptors(
		resp, topology.CPUDetails, podEntries, rpPinnedCPUSet, nonReclaimableCPUSet,
	)
	if err != nil {
		return nil, err
	}
	descriptors, err = applyFrozenReclaimConstraintToDisjointDescriptors(descriptors, frozenReclaim)
	if err != nil {
		return nil, err
	}
	// Runtime-config gate: target-driven provenance is honored only when all three
	// runtime-config conditions hold. Otherwise every descriptor stays on the
	// legacy floor/ownership path regardless of block quantities.
	allowElasticActual := dynamicConf.EnableReclaim && dynamicConf.EnableRampUpReclaimHardPartition &&
		resp.DisableDedicatedCoresOverlapReclaimedCores
	descriptors = applyTargetDrivenReclaimGate(descriptors, allowElasticActual)

	result := advisorapi.NewBlockCPUSet()
	available, err := p.allocateStaticAndForbiddenPools(resp, result, allCPUs)
	if err != nil {
		return nil, err
	}
	// This exclusion is committed-state ownership, not an inference from pod
	// annotations or the existence of a source/isolation component. Only a
	// steady dedicated NUMA-binding exclusive allocation owns an entire NUMA;
	// ordinary shared-core entries remain eligible for fake-NUMA balancing.
	// Reuse the same set in normalization and solving so both phases agree on
	// which NUMAs are immutable.
	skipNUMAs := podEntries.SteadyExclusiveNUMAs(topology)
	if resp.DisableDedicatedCoresOverlapReclaimedCores {
		if hardActive {
			descriptors, err = normalizeAdvisorDescriptorsForHardPartitionWholeCoreReclaim(
				descriptors, available, topology, skipNUMAs)
		} else {
			// Steady real-NUMA path: join the per-NUMA mandatory-reclaim and
			// dedicated targets and rewrite an odd reclaim advice to the
			// committed-anchored whole-core value, rather than borrowing a core out
			// of a running dedicated group.
			var normalizeReport steadyReclaimNormalizeReport
			descriptors, normalizeReport, err = normalizeSteadyRealNUMATargets(
				descriptors, available, topology, skipNUMAs)
			p.emitSteadyReclaimNormalizeMetrics(normalizeReport)
		}
		if err != nil {
			return nil, err
		}
	}
	baseAllocatable := available.Clone()
	if err := allocateAdvisorStaticDescriptors(descriptors, result); err != nil {
		return nil, err
	}

	core := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassDedicated ||
			descriptor.Class == advisorBlockClassMandatoryReclaim ||
			(!hardActive && descriptor.Class == advisorBlockClassShared && descriptor.NUMAID != commonstate.FakedNUMAID)
	})
	available, err = p.solveAdvisorDescriptorPhaseWithCheckpointTransitionAndSkipNUMAs(
		core, available, result, true, hardActive, checkpointTransition, skipNUMAs,
		podEntries, allowElasticActual)
	if err != nil {
		return nil, fmt.Errorf("solve dedicated and mandatory reclaim: %w", err)
	}

	sourceComponents, componentMembers, err := p.advisorSourceIsolationComponentsWithEntries(
		descriptors, podEntries)
	if err != nil {
		return nil, err
	}
	for _, sourceBlockID := range sourceComponents {
		members := componentMembers[sourceBlockID]
		if !hardActive {
			members = filterAdvisorDescriptors(members, func(descriptor advisorBlockDescriptor) bool {
				_, allocated := result[descriptor.BlockID]
				return !allocated
			})
		}
		available, err = p.solveAdvisorDescriptorPhaseWithCheckpointTransitionAndSkipNUMAs(
			members, available, result, false, hardActive, nil, skipNUMAs,
			podEntries, allowElasticActual)
		if err != nil {
			return nil, fmt.Errorf("solve source/isolation component %q: %w", sourceBlockID, err)
		}
	}

	remainingShared := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		_, allocated := result[descriptor.BlockID]
		return descriptor.Class == advisorBlockClassShared && !allocated
	})
	_, err = p.allocateStableAdvisorDescriptors(remainingShared, available, result)
	if err != nil {
		return nil, fmt.Errorf("allocate remaining shared blocks: %w", err)
	}

	protected := advisorDescriptorClassUnion(
		descriptors, result,
		advisorBlockClassStatic, advisorBlockClassDedicated, advisorBlockClassMandatoryReclaim,
	)
	overlapCandidates := baseAllocatable.Difference(protected)
	overlap := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassReclaimOverlap
	})
	if _, err := p.allocateStableAdvisorDescriptors(overlap, overlapCandidates, result); err != nil {
		return nil, fmt.Errorf("allocate overlap reclaim blocks: %w", err)
	}

	for _, descriptor := range descriptors {
		if descriptor.Quantity == 0 {
			if _, found := result[descriptor.BlockID]; !found {
				result[descriptor.BlockID] = machine.NewCPUSet()
			}
		}
	}
	if err := validateAdvisorDescriptorPlan(descriptors, result); err != nil {
		return nil, err
	}
	return result, nil
}

func allocateAdvisorStaticDescriptors(
	descriptors []advisorBlockDescriptor,
	result advisorapi.BlockCPUSet,
) error {
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassStatic {
			continue
		}
		if cpus, found := result[descriptor.BlockID]; found {
			if cpus.Size() != descriptor.Quantity || !cpus.IsSubsetOf(descriptor.Eligible) {
				return fmt.Errorf("static block %q does not satisfy descriptor", descriptor.BlockID)
			}
			continue
		}
		cpus := descriptor.OldPreferred.Intersection(descriptor.Eligible)
		if cpus.Size() != descriptor.Quantity {
			return fmt.Errorf("static block %q has no stable allocation", descriptor.BlockID)
		}
		result[descriptor.BlockID] = cpus
	}
	return nil
}

func (p *DynamicPolicy) solveAdvisorDescriptorPhase(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	result advisorapi.BlockCPUSet,
	preserveClass bool,
	hardActive bool,
) (machine.CPUSet, error) {
	return p.solveAdvisorDescriptorPhaseWithCheckpointTransition(
		descriptors, available, result, preserveClass, hardActive, nil)
}

func (p *DynamicPolicy) solveAdvisorDescriptorPhaseWithCheckpointTransition(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	result advisorapi.BlockCPUSet,
	preserveClass bool,
	hardActive bool,
	checkpointTransition *steadyFakeNUMAMigrationCheckpointTransition,
) (machine.CPUSet, error) {
	entries := p.state.GetPodEntries()
	skipNUMAs := entries.SteadyExclusiveNUMAs(p.machine.machineInfo.CPUTopology)
	return p.solveAdvisorDescriptorPhaseWithCheckpointTransitionAndSkipNUMAs(
		descriptors, available, result, preserveClass, hardActive, checkpointTransition, skipNUMAs,
		entries, p.isRampUpReclaimHardPartitionEnabled() &&
			p.state.GetDisableDedicatedCoresOverlapReclaimedCores())
}

func (p *DynamicPolicy) solveAdvisorDescriptorPhaseWithCheckpointTransitionAndSkipNUMAs(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	result advisorapi.BlockCPUSet,
	preserveClass bool,
	hardActive bool,
	checkpointTransition *steadyFakeNUMAMigrationCheckpointTransition,
	skipNUMAs sets.Int,
	requestEntries state.PodEntries,
	allowElasticActual bool,
) (machine.CPUSet, error) {
	if len(descriptors) == 0 {
		return available, nil
	}
	demands := make([]partitionDemand, 0, len(descriptors))
	blockIDByDemandKey := make(map[string]string, len(descriptors))
	ordinalByStableKey := make(map[string]int, len(descriptors))
	var coreFloors []partitionCoreFloorConstraint
	var steadyFakeDemandKeys []string
	var committedSnapshot steadyFakeNUMACommittedSnapshot
	expandHardReclaimPhase := preserveClass && hardActive
	expandSteadyReclaimPhase := preserveClass && !hardActive &&
		hasFakeNUMAMandatoryReclaimDescriptor(descriptors)
	if expandHardReclaimPhase || expandSteadyReclaimPhase {
		// NUMAs owned by a committed steady exclusive DNB keep only their finalized
		// reserve once ramp-up ends; the planner must skip them so its per-NUMA
		// minimum and cross-NUMA imbalance guards do not re-impose the ratio-derived
		// target and reject every other ramp-up QoS on the node.
		var (
			expanded         []partitionDemand
			expandedBlockIDs map[string]string
			err              error
		)
		if expandHardReclaimPhase {
			expanded, expandedBlockIDs, err = expandHardPartitionReclaimPhase(
				descriptors, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
		} else {
			committedSnapshot, err = buildSteadyFakeNUMACommittedSnapshot(
				descriptors, available)
			if err != nil {
				return available, fmt.Errorf("build committed blocks: %w", err)
			}
			expanded, expandedBlockIDs, coreFloors, err = expandSteadyFakeNUMAReclaimPhase(
				descriptors, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
		}
		if err != nil {
			return available, fmt.Errorf("expand reclaim phase: %w", err)
		}
		demands = append(demands, expanded...)
		for demandKey, blockID := range expandedBlockIDs {
			blockIDByDemandKey[demandKey] = blockID
		}
		if expandSteadyReclaimPhase {
			mandatoryBlockIDs := make(map[string]struct{})
			for _, descriptor := range descriptors {
				if descriptor.Class == advisorBlockClassMandatoryReclaim {
					mandatoryBlockIDs[descriptor.BlockID] = struct{}{}
				}
			}
			for demandKey, blockID := range expandedBlockIDs {
				if _, mandatory := mandatoryBlockIDs[blockID]; mandatory {
					steadyFakeDemandKeys = append(steadyFakeDemandKeys, demandKey)
				}
			}
			sort.Strings(steadyFakeDemandKeys)
		}
	}
	for _, descriptor := range descriptors {
		if (expandHardReclaimPhase || expandSteadyReclaimPhase) &&
			descriptor.Class == advisorBlockClassMandatoryReclaim {
			continue
		}

		class := descriptor.Class
		if !preserveClass {
			class = advisorBlockClassDedicated
		}
		stableKey := fmt.Sprintf("%s\x00%d\x00%s",
			descriptor.ComponentKey, descriptor.Quantity, strings.Join(descriptor.Owners, "\x1f"))
		ordinal := ordinalByStableKey[stableKey]
		ordinalByStableKey[stableKey] = ordinal + 1
		demandKey := fmt.Sprintf("%s\x00%d", stableKey, ordinal)
		requestQuantity := float64(descriptor.Quantity)
		requestGroupKey := demandKey
		if class == advisorBlockClassDedicated {
			requestQuantity = advisorDescriptorRequestQuantityFromEntries(
				descriptor, requestEntries, allowElasticActual)
			owners := append([]string(nil), descriptor.Owners...)
			sort.Strings(owners)
			requestGroupKey = strings.Join(owners, "\x1f")
		}
		demands = append(demands, partitionDemand{
			key:             demandKey,
			requestGroupKey: requestGroupKey,
			quantity:        descriptor.Quantity,
			requestQuantity: requestQuantity,
			eligible:        descriptor.Eligible.Intersection(available),
			preferred:       descriptor.OldPreferred,
			class:           class,
			// target-driven reclaim provenance (zero for legacy blocks).
			targetDriven: descriptor.TargetDriven,
			sourceTarget: descriptor.SourceTarget,
			reclaimQuota: descriptor.ReclaimQuota,
			numaID:       descriptor.NUMAID,
		})
		blockIDByDemandKey[demandKey] = descriptor.BlockID
	}
	if expandHardReclaimPhase {
		pinnedDemands, err := pinHardReclaimPartitionDemands(demands, available, p.machine.machineInfo.CPUTopology, false)
		if err != nil {
			return available, fmt.Errorf("plan hard reclaim partition: %w", err)
		}
		demands = pinnedDemands
	} else if preserveClass && !expandSteadyReclaimPhase {
		// Steady real-NUMA path owns the committed whole-core fallback; the
		// ramp-up/hard path above deliberately keeps its original semantics.
		pinnedDemands, err := pinHardReclaimPartitionDemands(demands, available, p.machine.machineInfo.CPUTopology, true)
		if err != nil {
			p.emitSteadyReclaimPlanOutcome(classifySteadyReclaimFailureReason(err))
			return available, fmt.Errorf("plan steady real-NUMA reclaim partition: %w", err)
		}
		demands = pinnedDemands
	}
	var assignments map[string]machine.CPUSet
	var solveErr error
	if len(steadyFakeDemandKeys) > 0 {
		assignments, solveErr = solveSteadyFakeNUMAWholeCoreWithFloorsAndProject(
			demands,
			steadyFakeDemandKeys,
			committedSnapshot,
			coreFloors,
			p.machine.machineInfo.CPUTopology,
			func(
				demands []partitionDemand,
				fakeKeys []string,
				committed steadyFakeNUMACommittedSnapshot,
				desired map[string]machine.CPUSet,
				floors []partitionCoreFloorConstraint,
				_ *machine.CPUTopology,
			) (map[string]machine.CPUSet, error) {
				projected, transition, err := p.planSteadyFakeNUMAStageWithCheckpoint(
					demands, fakeKeys, committed, desired, floors)
				if err == nil && checkpointTransition != nil {
					*checkpointTransition = transition
				}
				return projected, err
			},
		)
	} else if len(coreFloors) > 0 {
		assignments, solveErr = solveDisjointPartitionsWithCoreFloors(
			demands, coreFloors, p.machine.machineInfo.CPUTopology)
	} else {
		assignments, solveErr = solveDisjointPartitions(demands, p.machine.machineInfo.CPUTopology)
	}
	if solveErr != nil {
		return available, solveErr
	}
	used := machine.NewCPUSet()
	for demandKey, cpus := range assignments {
		blockID := blockIDByDemandKey[demandKey]
		result[blockID] = result[blockID].Union(cpus)
		used = used.Union(cpus)
	}
	return available.Difference(used), nil
}

func advisorDescriptorRequestQuantityFromEntries(
	descriptor advisorBlockDescriptor,
	entries state.PodEntries,
	allowElasticActual bool,
) float64 {
	requestQuantity := float64(descriptor.Quantity)
	for _, owner := range descriptor.Owners {
		_, entryName, subEntryName, _, ok := advisorDescriptorOwner(owner)
		if !ok || entries[entryName] == nil {
			continue
		}
		allocationInfo := entries[entryName][subEntryName]
		if allocationInfo == nil {
			continue
		}
		ownerRequest := allocationInfo.RequestQuantity
		if allowElasticActual && allocationInfo.RampUp &&
			allocationInfo.CheckDedicatedNUMABinding() &&
			!allocationInfo.OriginalAllocationResult.IsEmpty() &&
			float64(allocationInfo.OriginalAllocationResult.Size()) < ownerRequest {
			ownerRequest = float64(allocationInfo.OriginalAllocationResult.Size())
		}
		if ownerRequest > requestQuantity {
			requestQuantity = ownerRequest
		}
	}
	return requestQuantity
}

func (p *DynamicPolicy) allocateStableAdvisorDescriptors(
	descriptors []advisorBlockDescriptor,
	available machine.CPUSet,
	result advisorapi.BlockCPUSet,
) (machine.CPUSet, error) {
	for _, descriptor := range descriptors {
		candidates := available.Intersection(descriptor.Eligible)
		cpus, remaining, err := p.takeByTieredPreferredCPUs(
			candidates, []machine.CPUSet{descriptor.OldPreferred}, descriptor.Quantity,
		)
		if err != nil {
			return available, fmt.Errorf("allocate block %q: %w", descriptor.BlockID, err)
		}
		result[descriptor.BlockID] = cpus
		available = available.Difference(candidates).Union(remaining)
	}
	return available, nil
}

func (p *DynamicPolicy) advisorSourceIsolationComponents(
	descriptors []advisorBlockDescriptor,
) ([]string, map[string][]advisorBlockDescriptor, error) {
	return p.advisorSourceIsolationComponentsWithEntries(descriptors, p.state.GetPodEntries())
}

func (p *DynamicPolicy) advisorSourceIsolationComponentsWithEntries(
	descriptors []advisorBlockDescriptor,
	entries state.PodEntries,
) ([]string, map[string][]advisorBlockDescriptor, error) {
	type sourceDomain struct {
		poolName            string
		resourcePackageName string
		numaID              int
	}

	sourceByDomain := make(map[sourceDomain]advisorBlockDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassShared || advisorDescriptorIsIsolation(descriptor) {
			continue
		}
		for _, owner := range descriptor.Owners {
			poolName, resourcePackageName, ok := advisorDescriptorOwnerDomain(owner)
			if !ok {
				continue
			}
			domain := sourceDomain{
				poolName: poolName, resourcePackageName: resourcePackageName, numaID: descriptor.NUMAID,
			}
			if source, found := sourceByDomain[domain]; found && source.BlockID != descriptor.BlockID {
				firstBlockID, secondBlockID := source.BlockID, descriptor.BlockID
				if secondBlockID < firstBlockID {
					firstBlockID, secondBlockID = secondBlockID, firstBlockID
				}
				return nil, nil, fmt.Errorf(
					"source domain pool %q resource package %q numa %d has conflicting descriptors %q and %q",
					poolName, resourcePackageName, descriptor.NUMAID, firstBlockID, secondBlockID,
				)
			}
			sourceByDomain[domain] = descriptor
		}
	}

	members := make(map[string][]advisorBlockDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.Class != advisorBlockClassShared || !advisorDescriptorIsIsolation(descriptor) {
			continue
		}
		domain, resolved, err := advisorIsolationDescriptorSourceDomain(descriptor, entries)
		if err != nil {
			return nil, nil, fmt.Errorf("derive isolation block %q source domain: %w", descriptor.BlockID, err)
		}
		if !resolved {
			// orphan isolation descriptor: the advisor still references an
			// isolation block whose backing pod is gone from QRM state, so no
			// state allocation resolves its source domain. this is a soft
			// degradation (aligned with the legacy deriveAdvisorIsolationSourceDomain
			// path); skip the block instead of aborting the whole advisor cycle.
			continue
		}
		source, ok := sourceByDomain[domain]
		if !ok {
			continue
		}
		if len(members[source.BlockID]) == 0 {
			members[source.BlockID] = append(members[source.BlockID], source)
		}
		members[source.BlockID] = append(members[source.BlockID], descriptor)
	}

	keys := make([]string, 0, len(members))
	for _, descriptor := range descriptors {
		if _, found := members[descriptor.BlockID]; !found {
			continue
		}
		keys = append(keys, descriptor.BlockID)
		sort.Slice(members[descriptor.BlockID], func(i, j int) bool {
			return advisorBlockDescriptorLess(members[descriptor.BlockID][i], members[descriptor.BlockID][j])
		})
	}
	return keys, members, nil
}

func advisorIsolationDescriptorSourceDomain(
	descriptor advisorBlockDescriptor,
	entries state.PodEntries,
) (struct {
	poolName            string
	resourcePackageName string
	numaID              int
}, bool, error,
) {
	var resolved struct {
		poolName            string
		resourcePackageName string
		numaID              int
	}
	resolvedSet := false
	for _, owner := range descriptor.Owners {
		poolName, entryName, subEntryName, resourcePackageName, ok := advisorDescriptorOwner(owner)
		if !ok {
			return resolved, false, fmt.Errorf("owner %q is malformed", owner)
		}
		if !commonstate.IsIsolationPool(poolName) {
			continue
		}
		allocationInfo := resolveIsolationOwnerAllocation(entries, entryName, subEntryName, poolName, resourcePackageName)
		if allocationInfo == nil {
			// orphan owner: the backing pod no longer exists in QRM state, so
			// neither the direct pool-entry lookup nor the pod-style fallback
			// resolves an allocation. soft-degrade this owner (aligned with the
			// legacy deriveAdvisorIsolationSourceDomain path) rather than
			// hard-failing and aborting the whole advisor cycle.
			continue
		}
		allocationPool, allocationResourcePackage := resourcepackage.UnwrapOwnerPoolName(
			allocationInfo.GetOwnerPoolName())
		if allocationPool != poolName || allocationResourcePackage != resourcePackageName {
			return resolved, false, fmt.Errorf("owner %q disagrees with state owner domain", owner)
		}
		sourcePool, ok := deriveIsolationSourceSharePool(allocationInfo)
		if !ok {
			// the resolved allocation yields no derivable source share pool (e.g.
			// it is no longer a shared_cores pod, or its cpuset_pool enhancement
			// maps to the empty pool). the legacy deriveAdvisorIsolationSourceDomain
			// path propagates this as ok=false and its caller skips the block, so
			// soft-degrade this owner here rather than hard-failing and aborting
			// the whole advisor cycle.
			continue
		}
		sourcePool, _ = resourcepackage.UnwrapOwnerPoolName(sourcePool)
		current := struct {
			poolName            string
			resourcePackageName string
			numaID              int
		}{
			poolName: sourcePool, resourcePackageName: resourcePackageName, numaID: descriptor.NUMAID,
		}
		if resolvedSet && resolved != current {
			return resolved, false, fmt.Errorf("aliases resolve to different source domains")
		}
		resolved = current
		resolvedSet = true
	}
	if !resolvedSet {
		// every isolation owner soft-degraded (orphaned) or the descriptor had no
		// isolation owner at all; report unresolved so the caller skips the block.
		return resolved, false, nil
	}
	return resolved, true, nil
}

// resolveIsolationOwnerAllocation resolves the state allocation backing an
// isolation owner. isolation pools are never materialized as pool entries in
// the QRM state, so a pool-style owner (poolName == entryName, empty
// subEntryName) never hits the direct entryName/subEntryName lookup. in that
// case we fall back to scanning pod-style container entries and matching the
// (unwrapped) owner pool name plus resource package. it is the single shared
// resolver for both the descriptor-based advisorIsolationDescriptorSourceDomain
// and the legacy deriveAdvisorIsolationSourceDomain path.
func resolveIsolationOwnerAllocation(
	entries state.PodEntries,
	entryName, subEntryName, poolName, resourcePackageName string,
) *state.AllocationInfo {
	if allocationInfo := entries[entryName][subEntryName]; allocationInfo != nil {
		return allocationInfo
	}

	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			allocationPool, allocationResourcePackage := resourcepackage.UnwrapOwnerPoolName(
				allocationInfo.GetOwnerPoolName())
			if allocationPool != poolName || allocationResourcePackage != resourcePackageName {
				continue
			}
			return allocationInfo
		}
	}
	return nil
}

func filterAdvisorDescriptors(
	descriptors []advisorBlockDescriptor,
	keep func(advisorBlockDescriptor) bool,
) []advisorBlockDescriptor {
	result := make([]advisorBlockDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if keep(descriptor) {
			result = append(result, descriptor)
		}
	}
	return result
}

func advisorDescriptorPoolNames(descriptor advisorBlockDescriptor) []string {
	pools := make([]string, 0, len(descriptor.Owners))
	for _, owner := range descriptor.Owners {
		poolName, _, ok := advisorDescriptorOwnerDomain(owner)
		if ok {
			pools = append(pools, poolName)
		}
	}
	return pools
}

func advisorDescriptorOwnerDomain(owner string) (string, string, bool) {
	poolName, _, _, resourcePackageName, ok := advisorDescriptorOwner(owner)
	return poolName, resourcePackageName, ok
}

func advisorDescriptorOwner(owner string) (string, string, string, string, bool) {
	parts := strings.Split(owner, "\x00")
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

func advisorDescriptorIsIsolation(descriptor advisorBlockDescriptor) bool {
	for _, poolName := range advisorDescriptorPoolNames(descriptor) {
		unwrappedPoolName, _ := resourcepackage.UnwrapOwnerPoolName(poolName)
		if commonstate.IsIsolationPool(unwrappedPoolName) {
			return true
		}
	}
	return false
}

func advisorDescriptorClassUnion(
	descriptors []advisorBlockDescriptor,
	result advisorapi.BlockCPUSet,
	classes ...advisorBlockClass,
) machine.CPUSet {
	wanted := make(map[advisorBlockClass]struct{}, len(classes))
	for _, class := range classes {
		wanted[class] = struct{}{}
	}
	union := machine.NewCPUSet()
	for _, descriptor := range descriptors {
		if _, ok := wanted[descriptor.Class]; ok {
			union = union.Union(result[descriptor.BlockID])
		}
	}
	return union
}

func validateAdvisorDescriptorPlan(
	descriptors []advisorBlockDescriptor,
	result advisorapi.BlockCPUSet,
) error {
	for _, descriptor := range descriptors {
		cpus, found := result[descriptor.BlockID]
		if !found {
			return fmt.Errorf("block %q has no planned cpuset", descriptor.BlockID)
		}
		if cpus.Size() != descriptor.Quantity {
			return fmt.Errorf("block %q planned quantity %d does not match %d",
				descriptor.BlockID, cpus.Size(), descriptor.Quantity)
		}
		if !cpus.IsSubsetOf(descriptor.Eligible) {
			return fmt.Errorf("block %q planned cpuset violates eligibility", descriptor.BlockID)
		}
	}
	return nil
}

// targetDrivenHardReclaimEnabled gates target-driven reclaim on three
// runtime-config conditions; any one disabled means full legacy behavior.
func (p *DynamicPolicy) targetDrivenHardReclaimEnabled() bool {
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	return dyn != nil && dyn.EnableReclaim && dyn.EnableRampUpReclaimHardPartition &&
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
}

// applyTargetDrivenReclaimGate honors target-driven reclaim only when all three
// runtime-config conditions hold. Otherwise every descriptor stays on legacy
// semantics (TargetDriven=false, no frozen target/quota).
func (p *DynamicPolicy) applyTargetDrivenReclaimGate(descriptors []advisorBlockDescriptor) []advisorBlockDescriptor {
	return applyTargetDrivenReclaimGate(descriptors, p.targetDrivenHardReclaimEnabled())
}

// applyTargetDrivenReclaimGate applies the config-gated target-driven provenance.
//
// Degradation is per-NUMA, not global: a real NUMA that hosts more than one
// distinct dedicated source cannot be given a safe per-source donation cap in the
// fast path (the group donation limit is aggregated per group), so only that NUMA
// stays legacy. Single-source NUMAs on other NUMA nodes are still upgraded.
func applyTargetDrivenReclaimGate(descriptors []advisorBlockDescriptor, enabled bool) []advisorBlockDescriptor {
	if !enabled {
		return descriptors
	}
	ambiguous := identifyAmbiguousTargetDrivenNUMAs(descriptors)
	if len(ambiguous) > 0 {
		general.InfoS("target-driven reclaim downgraded ambiguous NUMAs to legacy",
			"ambiguousNUMAs", sortedIntKeys(ambiguous))
	}
	for i := range descriptors {
		if descriptors[i].Class != advisorBlockClassDedicated {
			continue
		}
		if descriptors[i].NUMAID == commonstate.FakedNUMAID {
			continue
		}
		if _, isAmbiguous := ambiguous[descriptors[i].NUMAID]; isAmbiguous {
			// Multiple distinct dedicated sources share this NUMA: keep legacy and
			// do not guess a quota.
			continue
		}
		descriptors[i].TargetDriven = true
		descriptors[i].SourceTarget = descriptors[i].Quantity
		// ReclaimQuota is the donatable excess: old preferred footprint minus the
		// frozen target. On a single-source NUMA this is exactly the amount a
		// dedicated source may donate; the group sum floor backstops it.
		descriptors[i].ReclaimQuota = general.Max(0, descriptors[i].OldPreferred.Size()-descriptors[i].Quantity)
	}
	return descriptors
}

// identifyAmbiguousTargetDrivenNUMAs returns the set of real NUMA nodes that host
// more than one distinct dedicated source. Distinct sources are distinguished by
// BlockID: the same BlockID repeated (a pod and its sidecar sharing one block)
// counts as a single source. FakeNUMA is excluded because it never participates in
// target-driven reclaim. Ambiguous NUMAs must stay legacy: without a per-source
// fast-path donation cap, guessing a quota would be unsafe.
func identifyAmbiguousTargetDrivenNUMAs(descriptors []advisorBlockDescriptor) map[int]struct{} {
	blockIDsByNUMA := map[int]map[string]struct{}{}
	ambiguous := make(map[int]struct{})
	for _, d := range descriptors {
		if d.Class != advisorBlockClassDedicated {
			continue
		}
		// FakeNUMA sources stay legacy and are excluded from ambiguity counting.
		if d.NUMAID == commonstate.FakedNUMAID {
			continue
		}
		// Defensive: an empty BlockID cannot identify a source, so two empty
		// BlockIDs on one NUMA must not collapse into a single deduped source.
		// Mark such a NUMA ambiguous directly (BlockIDs come from the advisor and
		// are non-empty in practice; this guards against a malformed descriptor).
		if d.BlockID == "" {
			ambiguous[d.NUMAID] = struct{}{}
			continue
		}
		if _, ok := blockIDsByNUMA[d.NUMAID]; !ok {
			blockIDsByNUMA[d.NUMAID] = map[string]struct{}{}
		}
		blockIDsByNUMA[d.NUMAID][d.BlockID] = struct{}{}
	}
	for numaID, blockIDs := range blockIDsByNUMA {
		if len(blockIDs) > 1 {
			ambiguous[numaID] = struct{}{}
		}
	}
	return ambiguous
}
