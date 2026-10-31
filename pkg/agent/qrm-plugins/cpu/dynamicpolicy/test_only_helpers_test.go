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

	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// adjustPoolsAndIsolatedEntriesWithRampUpFloor is a test-only thin wrapper over
// adjustPoolsAndIsolatedEntriesWithRampUpFloorAtRevision at the current state revision.
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntriesWithRampUpFloor(
	poolsQuantityMap map[string]map[int]int,
	isolatedQuantityMap map[string]map[string]int,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	persistCheckpoint bool,
	explicitRampUpFloor machine.CPUSet,
	runCPUSetHandlers bool,
) error {
	return p.adjustPoolsAndIsolatedEntriesWithRampUpFloorAtRevision(
		context.Background(), poolsQuantityMap, isolatedQuantityMap, entries, machineState, persistCheckpoint,
		explicitRampUpFloor, runCPUSetHandlers, p.state.GetRevision())
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
