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
	"context"

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// adjustPoolsAndIsolatedEntriesWithRampUpFloor is a test-only thin wrapper over
// adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision at the current state revision.
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntriesWithRampUpFloor(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
) error {
	return p.adjustPoolsAndIsolatedEntriesWithRampUpFloorForModeAtRevision(
		context.Background(), poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint,
		explicitRampUpFloor, runCPUSetHandlers, p.state.GetRevision(),
		defaultShareMaterializationNormal, p.config.dynamicConfig.GetDynamicConfiguration())
}

// publishAdvisorPostCommitTargetInMemory swaps the published advisor
// post-commit target under the retry mutex and records the Published phase,
// mirroring the final publication step of commitAdvisorResponseWithWriteAheadTransition
// without touching the WAL or canonical state.
func (p *DynamicPolicy) publishAdvisorPostCommitTargetInMemory(
	target *advisorPostCommitTarget,
) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.setAdvisorPostCommitTargetLocked(target)
	p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhasePublished)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

// publishAdvisorPostCommitTarget is a test-only helper that clones, persists and
// publishes an advisor post-commit target for the given revision.
func (p *DynamicPolicy) publishAdvisorPostCommitTarget(
	resp *advisorapi.ListAndWatchResponse,
	revision uint64,
) *advisorPostCommitTarget {
	target := cloneAdvisorPostCommitTarget(resp, revision)
	if revision > 0 {
		target.preCommitRevision = revision - 1
	}
	if err := p.storeAdvisorPostCommitTarget(target, p.advisorPostCommitCheckpointPath()); err != nil {
		general.Errorf("persist advisor post-commit target for revision %d failed: %v", revision, err)
	}
	p.publishAdvisorPostCommitTargetInMemory(target)
	return target
}

// hasPendingAdvisorPostCommitTarget is a test-only helper reporting whether the
// currently published advisor post-commit target matches the given revision.
func (p *DynamicPolicy) hasPendingAdvisorPostCommitTarget(revision uint64) bool {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return p.adjustment.advisorPostCommitTarget != nil && p.adjustment.advisorPostCommitTarget.revision == revision
}

// validatePendingPoolOwnership adapts existing test fixtures to the production implementation.
func validatePendingPoolOwnership(entries state.PodEntries) error {
	if err := validateNoReclaimedRampUp(entries); err != nil {
		return err
	}
	view, err := buildReclaimOwnershipView(entries, nil)
	if err != nil {
		return err
	}
	return validatePendingPoolOwnershipWithView(entries, view)
}

// validateHardPartitionBlockPlan adapts existing test fixtures to the production implementation.
func (p *DynamicPolicy) validateHardPartitionBlockPlan(
	resp *advisorapi.ListAndWatchResponse,
	blockCPUSet advisorapi.BlockCPUSet,
	hardActive bool,
) error {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.conf != nil {
		dynamicConf = p.config.conf.GetDynamicConfiguration()
	}
	domains := sets.NewInt()
	if hardActive {
		domains.Insert(commonstate.FakedNUMAID)
	}
	return p.validateHardPartitionBlockPlanWithDynamicConfig(
		resp, blockCPUSet, domains, dynamicConf)
}

// validateHardPartitionReclaimDistribution adapts existing test fixtures to the production implementation.
func validateHardPartitionReclaimDistribution(
	reclaim machine.CPUSet,
	eligible machine.CPUSet,
	topology *machine.CPUTopology,
	excludedNUMAs sets.Int,
	minimumPerNUMA int,
) error {
	cpusPerCore := 0
	if topology != nil {
		cpusPerCore = topology.CPUsPerCore()
	}
	return validateHardPartitionReclaimDistributionWithEffectiveCapacity(
		reclaim, eligible, nil, topology, excludedNUMAs, cpusPerCore, minimumPerNUMA)
}

// allocateNumaBindingCPUs adapts existing test fixtures to the production implementation.
func (p *DynamicPolicy) allocateNumaBindingCPUs(numCPUs int, hint *pluginapi.TopologyHint,
	machineState state.NUMANodeMap, reqAnnotations map[string]string, podReclaimEnabled bool,
) (machine.CPUSet, machine.CPUSet, error) {
	result, hardReclaimCPUs, _, err := p.allocateNumaBindingCPUsWithSnapshot(
		numCPUs, hint, machineState, reqAnnotations, podReclaimEnabled, nil,
		p.state.GetPodEntries(), p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		p.config.dynamicConfig.GetDynamicConfiguration())
	return result, hardReclaimCPUs, err
}
