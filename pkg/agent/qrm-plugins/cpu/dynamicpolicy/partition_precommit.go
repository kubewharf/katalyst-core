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
	"reflect"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type pendingCPUPartition struct {
	expectedRevision          uint64
	entries                   state.PodEntries
	baseMachineState          state.NUMANodeMap
	allowOverlap              bool
	disableDedicated          bool
	persist                   bool
	source                    string
	validate                  func(state.PodEntries, state.NUMANodeMap, bool, bool) error
	requireCoreAlignedReclaim bool
	enforceSteadyReclaim      bool
	residualFloor             machine.CPUSet
	dynamicConfig             *dynamicconfig.Configuration
}

type preparedCPUPartition struct {
	pending      pendingCPUPartition
	entries      state.PodEntries
	machineState state.NUMANodeMap
}

type advisorPostCommitPendingError struct {
	pendingRevision   uint64
	attemptedRevision uint64
	source            string
}

func (e *advisorPostCommitPendingError) Error() string {
	return fmt.Sprintf(
		"cpu state writer %q must retry while advisor post-commit revision %d is pending (attempted revision %d)",
		e.source, e.pendingRevision, e.attemptedRevision)
}

func (e *advisorPostCommitPendingError) Retryable() bool {
	return true
}

// preparePendingCPUPartition performs every fallible precommit step without
// mutating plugin state: clone, hooks, normalize, rebuild, and validation.
func (p *DynamicPolicy) preparePendingCPUPartition(
	pending pendingCPUPartition,
) (*preparedCPUPartition, error) {
	if p == nil || p.state == nil {
		return nil, fmt.Errorf("prepare pending cpu partition: policy is not initialized")
	}
	if pending.dynamicConfig == nil {
		pending.dynamicConfig = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	if pending.entries == nil {
		return nil, fmt.Errorf("prepare pending cpu partition: entries are nil")
	}

	currentEntries := p.state.GetPodEntries()
	candidate := pending.entries.Clone()
	if err := p.invokeAllocationHooksForPodEntries(currentEntries, candidate); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, "run allocation hooks", err)
	}
	p.syncCandidateSidecarsFromMain(candidate)
	var machineState state.NUMANodeMap
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		if pending.baseMachineState == nil {
			return nil, fmt.Errorf("prepare pending cpu partition: cpu topology is not initialized")
		}
		machineState = pending.baseMachineState.Clone()
	} else {
		if err := p.normalizePendingCPUPartition(candidate); err != nil {
			return nil, p.wrapPartitionPrecommitError(pending.source, "normalize allocation entries", err)
		}
		if err := validateAllocationShapeAfterHooks(
			pending.entries, candidate, p.machine.machineInfo.CPUTopology,
		); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, "revalidate allocation shape after hooks", err)
		}
		if pending.requireCoreAlignedReclaim {
			if err := assertCoreAligned(reclaimPoolCPUSet(candidate), p.machine.machineInfo.CPUTopology); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, "revalidate reclaim core alignment after hooks", err)
			}
		}
		if pending.enforceSteadyReclaim {
			plannedReclaim := reclaimPoolCPUSet(pending.entries)
			candidateReclaim := reclaimPoolCPUSet(candidate)
			if err := validateSteadyReclaimPrecommitInvariant(
				plannedReclaim, candidateReclaim, p.machine.machineInfo.CPUTopology,
			); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, "revalidate steady reclaim after hooks", err)
			}
		}
		baseMachineState := pending.baseMachineState
		if baseMachineState == nil {
			baseMachineState = p.state.GetMachineState()
		} else {
			baseMachineState = baseMachineState.Clone()
		}
		var err error
		machineState, err = generateMachineStateFromPodEntries(
			p.machine.machineInfo.CPUTopology, candidate, baseMachineState)
		if err != nil {
			return nil, p.wrapPartitionPrecommitError(pending.source, "rebuild machine state", err)
		}
	}
	if err := p.validateResidualBackfillCandidateWithDynamicConfig(
		candidate, machineState, pending.residualFloor, pending.dynamicConfig); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, "validate residual backfill candidate", err)
	}
	if err := validatePendingPoolOwnership(candidate); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, "validate allocation ownership", err)
	}
	validate := pending.validate
	if validate == nil {
		validate = func(
			entries state.PodEntries,
			machineState state.NUMANodeMap,
			allowOverlap, disableDedicated bool,
		) error {
			return p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
				entries, machineState, allowOverlap, disableDedicated, pending.dynamicConfig)
		}
	}
	preValidationEntries := candidate.Clone()
	preValidationMachineState := machineState.Clone()
	if err := validate(candidate, machineState, pending.allowOverlap, pending.disableDedicated); err != nil {
		return nil, p.wrapPartitionPrecommitError(pending.source, "validate hard floor and partition", err)
	}
	// Keep the frozen plan authoritative through the complete prepare phase.
	// A validator is expected to be read-only, but re-check the candidate here
	// so a late mutation cannot bypass the hook-time replacement invariants.
	if p.machine.machineInfo != nil && p.machine.machineInfo.CPUTopology != nil {
		if err := validateAllocationShapeAfterHooks(
			pending.entries, candidate, p.machine.machineInfo.CPUTopology,
		); err != nil {
			return nil, p.wrapPartitionPrecommitError(
				pending.source, "revalidate replacement baseline after partition validation", err)
		}
		if pending.requireCoreAlignedReclaim {
			if err := assertCoreAligned(reclaimPoolCPUSet(candidate), p.machine.machineInfo.CPUTopology); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, "revalidate replacement baseline after partition validation",
					fmt.Errorf("reclaim is not core-aligned: %w", err))
			}
		}
		if pending.enforceSteadyReclaim {
			if err := validateSteadyReclaimPrecommitInvariant(
				reclaimPoolCPUSet(pending.entries),
				reclaimPoolCPUSet(candidate),
				p.machine.machineInfo.CPUTopology,
			); err != nil {
				return nil, p.wrapPartitionPrecommitError(
					pending.source, "revalidate replacement baseline after partition validation", err)
			}
		}
	}
	if !reflect.DeepEqual(preValidationEntries, candidate) ||
		!reflect.DeepEqual(preValidationMachineState, machineState) {
		return nil, p.wrapPartitionPrecommitError(
			pending.source, "revalidate replacement baseline after partition validation",
			fmt.Errorf("partition validator mutated its candidate"))
	}
	return &preparedCPUPartition{
		pending:      pending,
		entries:      candidate,
		machineState: machineState,
	}, nil
}

func (p *DynamicPolicy) syncCandidateSidecarsFromMain(entries state.PodEntries) {
	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		main := containerEntries.GetMainContainerEntry()
		if main == nil {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation == nil || !allocation.CheckSideCar() {
				continue
			}
			p.applySidecarAllocationInfoFromMainContainer(allocation, main)
		}
	}
}

func (p *DynamicPolicy) validateResidualBackfillCandidate(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	residualFloor machine.CPUSet,
) error {
	var dynamicConfig *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConfig = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.validateResidualBackfillCandidateWithDynamicConfig(
		entries, machineState, residualFloor, dynamicConfig)
}

func (p *DynamicPolicy) validateResidualBackfillCandidateWithDynamicConfig(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	residualFloor machine.CPUSet,
	dynamicConfig *dynamicconfig.Configuration,
) error {
	if dynamicConfig == nil || !dynamicConfig.FillDefaultSharePoolWithNonReclaimCPUs {
		return nil
	}
	if p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return fmt.Errorf("cpu topology is not initialized")
	}

	share := machine.NewCPUSet()
	var shareEntry *state.AllocationInfo
	if poolEntries := entries[commonstate.PoolNameShare]; poolEntries != nil {
		shareEntry = poolEntries.GetPoolEntry()
		if shareEntry == nil {
			return fmt.Errorf("default share pool entry is invalid")
		}
		share = shareEntry.AllocationResult
	}

	fixedPools := machine.NewCPUSet()
	for poolName, containerEntries := range entries {
		if poolName == commonstate.PoolNameShare || !containerEntries.IsPoolEntry() {
			continue
		}
		poolEntry := containerEntries.GetPoolEntry()
		if poolEntry != nil {
			fixedPools = fixedPools.Union(poolEntry.AllocationResult)
		}
	}
	dedicated := unionIsolatedCPUSet(dedicatedCPUSetFromPodEntries(entries))

	if overlap := share.Intersection(fixedPools); !overlap.IsEmpty() {
		return fmt.Errorf("default share overlaps fixed pools: %s", overlap.String())
	}
	if overlap := share.Intersection(dedicated); !overlap.IsEmpty() {
		return fmt.Errorf("default share overlaps isolated or dedicated cpus: %s", overlap.String())
	}

	eligible := p.buildDefaultShareEligibleCPUSet(entries, machineState, residualFloor)
	expected := eligible.Difference(fixedPools).Difference(dedicated)
	activeRampUp := globalDomainActiveRampUpCPUSet(entries)
	extraShare := share.Difference(expected)
	uncoveredResidual := expected.Difference(share).Difference(activeRampUp)
	if !extraShare.IsEmpty() || !uncoveredResidual.IsEmpty() {
		return fmt.Errorf("default share cpuset %s with active ramp-up %s differs from eligible residual %s (uncovered=%s extra_share=%s eligible=%s fixed=%s dedicated=%s)",
			share.String(), activeRampUp.String(), expected.String(), uncoveredResidual.String(), extraShare.String(),
			eligible.String(), fixedPools.String(), dedicated.String())
	}
	if shareEntry == nil {
		return nil
	}

	expectedAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, share)
	if err != nil {
		return fmt.Errorf("calculate default share topology assignments: %w", err)
	}
	if !cpuAssignmentsEqual(shareEntry.TopologyAwareAssignments, expectedAssignments) {
		return fmt.Errorf("default share topology assignments are inconsistent with cpuset %s", share.String())
	}
	return nil
}

func cpuAssignmentsEqual(left, right map[int]machine.CPUSet) bool {
	if len(left) != len(right) {
		return false
	}
	for numaID, leftCPUSet := range left {
		rightCPUSet, ok := right[numaID]
		if !ok || !leftCPUSet.Equals(rightCPUSet) {
			return false
		}
	}
	return true
}

func validatePendingPoolOwnership(entries state.PodEntries) error {
	actualRNBTargetNUMAs := make(map[int]struct{})
	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || allocation.RampUp ||
				!allocation.CheckReclaimedActualNUMABinding() {
				continue
			}
			numaID, err := allocation.GetSpecifiedNUMABindingNUMAID()
			if err != nil {
				return fmt.Errorf("%s allocation %s/%s has invalid target NUMA: %w",
					allocation.QoSLevel, podUID, containerName, err)
			}
			actualRNBTargetNUMAs[numaID] = struct{}{}
		}
	}

	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || allocation.RampUp ||
				(!allocation.CheckShared() && !allocation.CheckReclaimed()) {
				continue
			}
			ownerPool := allocation.GetOwnerPoolName()
			if ownerPool == commonstate.EmptyOwnerPoolName {
				return fmt.Errorf("%s allocation %s/%s has empty owner pool",
					allocation.QoSLevel, podUID, containerName)
			}
			pool := entries[ownerPool].GetPoolEntry()
			if pool == nil {
				return fmt.Errorf("%s allocation %s/%s owns missing pool %q",
					allocation.QoSLevel, podUID, containerName, ownerPool)
			}
			expectedAllocation := pool.AllocationResult
			if allocation.CheckReclaimedActualNUMABinding() {
				numaID, err := allocation.GetSpecifiedNUMABindingNUMAID()
				if err != nil {
					return fmt.Errorf("%s allocation %s/%s has invalid target NUMA: %w",
						allocation.QoSLevel, podUID, containerName, err)
				}
				expectedAllocation = pool.TopologyAwareAssignments[numaID]
			} else if allocation.CheckReclaimed() {
				expectedAllocation = machine.NewCPUSet()
				for numaID, numaAllocation := range pool.TopologyAwareAssignments {
					if _, ok := actualRNBTargetNUMAs[numaID]; ok {
						continue
					}
					expectedAllocation = expectedAllocation.Union(numaAllocation)
				}
			}
			if !allocation.AllocationResult.Equals(expectedAllocation) {
				return fmt.Errorf("%s allocation %s/%s cpuset %s differs from owner pool %q cpuset %s",
					allocation.QoSLevel, podUID, containerName,
					allocation.AllocationResult.String(), ownerPool, expectedAllocation.String())
			}
		}
	}
	return nil
}

// commitPreparedCPUPartition is the only state mutation for a prepared CPU
// partition. No fallible response or finalize work may follow this revision-CAS.
func (p *DynamicPolicy) commitPreparedCPUPartition(prepared *preparedCPUPartition) error {
	return p.commitPreparedCPUPartitionForAdvisorTarget(prepared, nil)
}

func (p *DynamicPolicy) commitPreparedCPUPartitionForAdvisorTarget(
	prepared *preparedCPUPartition,
	reconcileTarget *advisorPostCommitTarget,
) error {
	if prepared == nil {
		return fmt.Errorf("commit prepared cpu partition: candidate is nil")
	}
	var permits []*state.WritePermit
	if reconcileTarget != nil {
		permits = append(permits, p.newAdvisorStateWritePermit(reconcileTarget))
	}
	if err := p.state.CommitAdvisorStateIfRevision(
		prepared.pending.expectedRevision,
		prepared.entries,
		prepared.machineState,
		prepared.pending.allowOverlap,
		prepared.pending.disableDedicated,
		prepared.pending.persist,
		permits...,
	); err != nil {
		return err
	}
	p.emitFinalPoolSizeMetrics(prepared.entries)
	return nil
}

func (p *DynamicPolicy) commitPendingCPUPartition(
	pending pendingCPUPartition,
) (state.PodEntries, state.NUMANodeMap, error) {
	return p.commitPendingCPUPartitionForAdvisorTarget(pending, nil)
}

// commitPendingCPUPartitionForAdvisorTarget is the canonical mutation boundary
// for a planned CPU partition. It validates writer ownership, prepares the
// complete candidate, and publishes it only through the pending revision CAS;
// an advisor target supplies the narrow fence permit for that exact commit.
func (p *DynamicPolicy) commitPendingCPUPartitionForAdvisorTarget(
	pending pendingCPUPartition,
	reconcileTarget *advisorPostCommitTarget,
) (state.PodEntries, state.NUMANodeMap, error) {
	if err := p.ensureCPUStateWriterAllowed(
		pending.expectedRevision, pending.source, reconcileTarget); err != nil {
		return nil, nil, err
	}
	prepared, err := p.preparePendingCPUPartition(pending)
	if err != nil {
		return nil, nil, err
	}
	if err := p.commitPreparedCPUPartitionForAdvisorTarget(prepared, reconcileTarget); err != nil {
		return nil, nil, err
	}
	return prepared.entries, prepared.machineState, nil
}

func (p *DynamicPolicy) normalizePendingCPUPartition(entries state.PodEntries) error {
	for podUID, containerEntries := range entries {
		for containerName, allocation := range containerEntries {
			if allocation == nil {
				continue
			}
			assignments, err := machine.GetNumaAwareAssignments(
				p.machine.machineInfo.CPUTopology, allocation.AllocationResult)
			if err != nil {
				return fmt.Errorf("%s/%s allocation result: %w", podUID, containerName, err)
			}
			originalAssignments, err := machine.GetNumaAwareAssignments(
				p.machine.machineInfo.CPUTopology, allocation.OriginalAllocationResult)
			if err != nil {
				return fmt.Errorf("%s/%s original allocation result: %w", podUID, containerName, err)
			}
			allocation.TopologyAwareAssignments = assignments
			allocation.OriginalTopologyAwareAssignments = originalAssignments
		}
	}
	return nil
}

func validateAllocationShapeAfterHooks(
	planned, candidate state.PodEntries,
	topology *machine.CPUTopology,
) error {
	for podUID, plannedContainers := range planned {
		for containerName, plannedAllocation := range plannedContainers {
			if plannedAllocation == nil {
				continue
			}
			// Sidecars are intentionally normalized to their main container
			// after hooks, so their previous quantity and NUMA shape are not an
			// independent allocation contract.
			if plannedAllocation.CheckSideCar() {
				continue
			}
			candidateAllocation := candidate[podUID][containerName]
			if candidateAllocation == nil {
				return fmt.Errorf("allocation %s/%s was removed after hooks", podUID, containerName)
			}
			if plannedAllocation.AllocationResult.Size() != candidateAllocation.AllocationResult.Size() {
				return fmt.Errorf(
					"allocation quantity changed after hooks for %s/%s: planned=%d candidate=%d",
					podUID, containerName,
					plannedAllocation.AllocationResult.Size(),
					candidateAllocation.AllocationResult.Size())
			}
			for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
				numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
				plannedQuantity := plannedAllocation.AllocationResult.Intersection(numaCPUs).Size()
				candidateQuantity := candidateAllocation.AllocationResult.Intersection(numaCPUs).Size()
				if plannedQuantity != candidateQuantity {
					return fmt.Errorf(
						"allocation NUMA distribution changed after hooks for %s/%s NUMA %d: planned=%d candidate=%d",
						podUID, containerName, numaID, plannedQuantity, candidateQuantity)
				}
			}
		}
	}
	return nil
}

func reclaimPoolCPUSet(entries state.PodEntries) machine.CPUSet {
	if reclaimEntries := entries[commonstate.PoolNameReclaim]; reclaimEntries != nil {
		if reclaim := reclaimEntries[commonstate.FakedContainerName]; reclaim != nil {
			return reclaim.AllocationResult.Clone()
		}
	}
	return machine.NewCPUSet()
}

func validateSteadyReclaimPrecommitInvariant(
	planned, candidate machine.CPUSet,
	topology *machine.CPUTopology,
) error {
	if topology == nil {
		return fmt.Errorf("steady reclaim precommit topology is nil")
	}
	if err := assertCoreAligned(candidate, topology); err != nil {
		return fmt.Errorf("steady reclaim is not core-aligned: %w", err)
	}
	if candidate.Size() != planned.Size() {
		return fmt.Errorf(
			"steady reclaim quantity changed after hooks: planned=%d candidate=%d",
			planned.Size(), candidate.Size())
	}
	if churn := steadyFakeNUMAMigrationChurn(planned, candidate); churn >
		steadyFakeNUMAMaxMigratedCPUs {
		return fmt.Errorf(
			"steady reclaim hook migration churn %d exceeds limit %d",
			churn, steadyFakeNUMAMaxMigratedCPUs)
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		plannedQuantity := planned.Intersection(numaCPUs).Size()
		candidateQuantity := candidate.Intersection(numaCPUs).Size()
		if plannedQuantity != candidateQuantity {
			return fmt.Errorf(
				"steady reclaim NUMA distribution changed after hooks for NUMA %d: planned=%d candidate=%d",
				numaID, plannedQuantity, candidateQuantity)
		}
	}
	return nil
}

func (p *DynamicPolicy) wrapPartitionPrecommitError(source, stage string, err error) error {
	if source == "" {
		source = "cpu partition"
	}
	return fmt.Errorf("%s precommit %s: %w", source, stage, err)
}
