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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// deriveAdvisorIsolationSourcePool derives the source share pool for a shared_cores
// isolation block from the advisor block owner entries and the current state. It returns
// false on derivation failure so callers can preserve the legacy allocation path.
func deriveAdvisorIsolationSourcePool(block *advisorapi.BlockInfo, entries state.PodEntries) (string, bool) {
	if block == nil {
		return "", false
	}

	for ownerPoolName, entry := range block.OwnerPoolEntryMap {
		if !commonstate.IsIsolationPool(ownerPoolName) {
			continue
		}

		if allocationInfo := entries[entry.EntryName][entry.SubEntryName]; allocationInfo != nil {
			return deriveIsolationSourceSharePool(allocationInfo)
		}

		for _, containerEntries := range entries {
			if containerEntries.IsPoolEntry() {
				continue
			}
			for _, allocationInfo := range containerEntries {
				if allocationInfo == nil || allocationInfo.GetOwnerPoolName() != ownerPoolName {
					continue
				}
				return deriveIsolationSourceSharePool(allocationInfo)
			}
		}
	}

	return "", false
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

// tryCarveAdvisorBlockFromSource tries to carve an isolation block from an already
// allocated source share block. If the source block does not exist or has not been
// allocated yet, it returns carved=false so the caller can continue the legacy path.
func (p *DynamicPolicy) tryCarveAdvisorBlockFromSource(
	block *advisorapi.BlockInfo,
	sourceBlockByPool map[string]string,
	blockCPUSet advisorapi.BlockCPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	numaID int,
	blockResult int,
) (bool, error) {
	if block == nil {
		return false, nil
	}
	if _, found := blockCPUSet[block.BlockId]; found {
		return true, nil
	}

	sourcePoolName, ok := deriveAdvisorIsolationSourcePool(block, p.state.GetPodEntries())
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
		sourceCandidate = sourceCandidate.Intersection(p.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
	}

	carveCandidates := sourceCandidate.Union(*availableCPUs)
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
