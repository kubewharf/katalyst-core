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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
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
	reclaimBlocks []*advisorapi.BlockInfo,
	isolationBlocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	globalNonReclaimableCPUSet machine.CPUSet,
	sourceBlockByPool map[string]string,
) error {
	isolationQuantityBySource := make(map[string]int)
	for _, block := range isolationBlocks {
		sourcePoolName, ok := deriveAdvisorIsolationSourcePool(block, p.state.GetPodEntries())
		if !ok {
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse isolation block: %s result failed with error: %v", block.BlockId, err)
		}
		isolationQuantityBySource[sourcePoolName] += blockResult
	}
	if len(isolationQuantityBySource) == 0 {
		return nil
	}

	for _, block := range reclaimBlocks {
		if block == nil {
			continue
		}
		if _, found := blockCPUSet[block.BlockId]; found {
			continue
		}

		var sourcePoolName string
		for ownerPoolName := range block.OwnerPoolEntryMap {
			if sourceBlockByPool[ownerPoolName] == block.BlockId {
				sourcePoolName = ownerPoolName
				break
			}
		}
		if sourcePoolName == "" || isolationQuantityBySource[sourcePoolName] == 0 {
			continue
		}

		sourceResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse source block: %s result failed with error: %v", block.BlockId, err)
		}

		combinedResult := sourceResult + isolationQuantityBySource[sourcePoolName]
		currentAvailableCPUs := availableCPUs.Difference(globalNonReclaimableCPUSet)
		cpuset, _, err := calculator.TakeByNUMABalance(p.machineInfo, currentAvailableCPUs, combinedResult)
		if err != nil {
			return fmt.Errorf("allocate source block: %s with combined req: %d failed with error: %v",
				block.BlockId, combinedResult, err)
		}

		blockCPUSet[block.BlockId] = cpuset
		*availableCPUs = availableCPUs.Difference(cpuset)
		*nodeRemainingCPUs = nodeRemainingCPUs.Difference(cpuset)
		general.InfoS("preallocated advisor source block for isolation carve",
			"blockID", block.BlockId,
			"sourcePoolName", sourcePoolName,
			"sourceResult", sourceResult,
			"isolationResult", isolationQuantityBySource[sourcePoolName],
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
	if sourceResult, ok := sourceBlockResultByID[sourceBlockID]; ok {
		sourceSurplusSize := sourceCPUSet.Size() - sourceResult
		if sourceSurplusSize <= 0 {
			sourceCandidate = machine.NewCPUSet()
		} else if sourceCandidate.Size() > sourceSurplusSize {
			var err error
			sourceCandidate, err = calculator.TakeByTopology(p.machineInfo, sourceCandidate, sourceSurplusSize, true)
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
