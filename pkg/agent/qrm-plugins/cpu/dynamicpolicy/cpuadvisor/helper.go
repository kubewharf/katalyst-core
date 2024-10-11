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

package cpuadvisor

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type BlockCPUSet map[string]machine.CPUSet

func NewBlockCPUSet() BlockCPUSet {
	return make(BlockCPUSet)
}

func (ci *CalculationInfo) IsSharedNUMABindingPool() bool {
	if ci == nil {
		return false
	}

	return len(ci.CalculationResultsByNumas) == 1 && ci.CalculationResultsByNumas[commonstate.FakedNUMAID] == nil
}

func (ce *CalculationEntries) IsPoolEntry() bool {
	if ce == nil {
		return false
	}

	return len(ce.Entries) == 1 && ce.Entries[commonstate.FakedContainerName] != nil
}

func (ce *CalculationEntries) IsSharedNUMABindingPoolEntry() bool {
	if !ce.IsPoolEntry() {
		return false
	}

	return len(ce.Entries[commonstate.FakedContainerName].CalculationResultsByNumas) == 1 &&
		ce.Entries[commonstate.FakedContainerName].CalculationResultsByNumas[commonstate.FakedNUMAID] == nil
}

func (lwr *ListAndWatchResponse) GetSharedBindingNUMAs() (sets.Int, error) {
	if lwr == nil {
		return sets.NewInt(), fmt.Errorf("got nil ListAndWatchResponse")
	}

	sharedBindingNUMAs := sets.NewInt()

	for _, entry := range lwr.Entries {

		if !entry.IsSharedNUMABindingPoolEntry() {
			continue
		}

		for _, calculationInfo := range entry.Entries {
			for numaIdInt64 := range calculationInfo.CalculationResultsByNumas {
				if numaIdInt64 < 0 {
					return sets.NewInt(), fmt.Errorf("SharedNUMABindingPoolEntry has invalid numaID: %v", numaIdInt64)
				}
				sharedBindingNUMAs.Insert(int(numaIdInt64))
			}
		}
	}

	return sharedBindingNUMAs, nil
}

// GetBlocks parses ListAndWatchResponse and returns map[int][]*Block,
// the map is keyed as numa id -> blocks slice (which has been sorted and deduped)
func (lwr *ListAndWatchResponse) GetBlocks() (map[int][]*Block, error) {
	if lwr == nil {
		return nil, fmt.Errorf("got nil ListAndWatchResponse")
	}

	numaToBlocks := make(map[int]map[string]*Block)
	numaToSortedBlocks := make(map[int][]*Block)
	blocksEntryNames := make(map[string][][]string)
	visBlocksToNUMA := make(map[string]int)

	for entryName, entry := range lwr.Entries {
		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo == nil {
				general.Warningf("[ListAndWatchResponse.GetBlocks] got nil calculationInfo entry: %s, subEntry: %s",
					entryName, subEntryName)
				continue
			}

			for numaIdInt64, calculationResult := range calculationInfo.CalculationResultsByNumas {
				if calculationResult == nil {
					general.Warningf("[ListAndWatchResponse.GetBlocks] got nil NUMA result entry: %s, subEntry: %s",
						entryName, subEntryName)
					continue
				}

				numaIdInt, err := general.CovertInt64ToInt(numaIdInt64)
				if err != nil {
					return nil, fmt.Errorf("parse numa id: %d failed with error: %v entry: %s, subEntry: %s",
						numaIdInt64, err, entryName, subEntryName)
				}

				if numaToBlocks[numaIdInt] == nil {
					numaToBlocks[numaIdInt] = make(map[string]*Block)
				}

				for _, block := range calculationResult.Blocks {
					if block == nil {
						general.Warningf("[ListAndWatchResponse.GetBlocks] got nil block result entry: %s, subEntry: %s",
							entryName, subEntryName)
						continue
					}

					blockId := block.BlockId
					if foundNUMAId, found := visBlocksToNUMA[blockId]; found && numaToBlocks[foundNUMAId][blockId] != nil {
						if foundNUMAId != numaIdInt {
							return nil, fmt.Errorf("found block: %s both in NUMA: %d and NUMA: %d, entry: %s, subEntry: %s",
								blockId, foundNUMAId, numaIdInt, entryName, subEntryName)
						} else if numaToBlocks[foundNUMAId][blockId].Result != block.Result {
							return nil, fmt.Errorf("found block: %s result is different with current block: %s result, entry: %s, subEntry: %s",
								numaToBlocks[foundNUMAId][blockId].String(), block.String(), entryName, subEntryName)
						}
					}

					numaToBlocks[numaIdInt][blockId] = block
					visBlocksToNUMA[blockId] = numaIdInt
					blocksEntryNames[blockId] = append(blocksEntryNames[blockId], []string{entryName, subEntryName})
				}
			}
		}
	}

	for numaId, blocksMap := range numaToBlocks {
		blocks := make([]*Block, 0, len(blocksMap))

		for _, block := range blocksMap {
			blocks = append(blocks, block)

			if len(blocksEntryNames[block.BlockId]) == 0 {
				return nil, fmt.Errorf("there is no entryName for block: %s", block.BlockId)
			}
		}

		sort.Slice(blocks, getBlocksLessFunc(blocksEntryNames, blocks))

		numaToSortedBlocks[numaId] = blocks
	}

	logNUMAToBlocksSeq(numaToSortedBlocks, blocksEntryNames)

	return numaToSortedBlocks, nil
}

// getBlocksLessFunc judges if a block is less than another by entryNames of them
func getBlocksLessFunc(blocksEntryNames map[string][][]string, blocks []*Block) func(i, j int) bool {
	return func(i, j int) bool {
		blockId1 := blocks[i].BlockId
		blockId2 := blocks[j].BlockId

		entryNames1 := blocksEntryNames[blockId1]
		entryNames2 := blocksEntryNames[blockId2]

		reclaimBlock1, reclaimBlock2 := IsBlockOfRelcaim(entryNames1), IsBlockOfRelcaim(entryNames2)

		// always alloate reclaim block lastly
		if reclaimBlock1 && !reclaimBlock2 {
			return false
		} else if !reclaimBlock1 && reclaimBlock2 {
			return true
		}

		getNames := func(entryNames [][]string, isPod bool) []string {
			names := make([]string, 0, len(entryNames))
			for _, namesTuple := range entryNames {
				entryName, subEntryName := namesTuple[0], namesTuple[1]

				if isPod {
					if subEntryName != commonstate.FakedContainerName {
						names = append(names, entryName)
					}
				} else {
					if subEntryName == commonstate.FakedContainerName {
						names = append(names, entryName)
					}
				}
			}
			sort.Strings(names)
			return names
		}

		podNames1, poolNames1, podNames2, poolNames2 := getNames(entryNames1, true),
			getNames(entryNames1, false),
			getNames(entryNames2, true),
			getNames(entryNames2, false)

		if len(podNames1) > len(podNames2) {
			return true
		} else if len(podNames1) < len(podNames2) {
			return false
		} else {
			if len(podNames1) > 0 {
				return strings.Join(podNames1, commonstate.NameSeparator) < strings.Join(podNames2, commonstate.NameSeparator)
			}

			if len(poolNames1) > len(poolNames2) {
				return true
			} else if len(poolNames1) < len(poolNames2) {
				return false
			} else {
				return strings.Join(poolNames1, commonstate.NameSeparator) < strings.Join(poolNames2, commonstate.NameSeparator)
			}
		}
	}
}

func logNUMAToBlocksSeq(numaToSortedBlocks map[int][]*Block, blocksEntryNames map[string][][]string) {
	for numaId, blocks := range numaToSortedBlocks {
		for i, block := range blocks {
			general.InfoS("logNUMAToBlocksSeq", "numaId", numaId, "seq", i, "blockId", block.BlockId, "entryNames", blocksEntryNames[block.BlockId])
		}
	}
}

// GeEntryNUMABlocks returns Block lists according to the given [entry, subEntry] pair
func (lwr *ListAndWatchResponse) GeEntryNUMABlocks(entry, subEntry string, numa int64) ([]*Block, bool) {
	if entry, ok := lwr.Entries[entry]; !ok {
		return []*Block{}, false
	} else if info, ok := entry.Entries[subEntry]; !ok {
		return []*Block{}, false
	} else if results, ok := info.CalculationResultsByNumas[numa]; !ok {
		return []*Block{}, false
	} else {
		return results.Blocks, true
	}
}

// GetCalculationInfo returns CalculationInfo according to the given [entry, subEntry]
func (lwr *ListAndWatchResponse) GetCalculationInfo(entry, subEntry string) (*CalculationInfo, bool) {
	if entry, ok := lwr.Entries[entry]; !ok {
		return nil, false
	} else if info, ok := entry.Entries[subEntry]; !ok {
		return nil, false
	} else {
		return info, true
	}
}

// FilterCalculationInfo filter out CalculationInfo only for dedicated pod,
// and the returned map and formatted as pod -> container -> CalculationInfo
func (lwr *ListAndWatchResponse) FilterCalculationInfo(pool string) map[string]map[string]*CalculationInfo {
	dedicatedCalculationInfos := make(map[string]map[string]*CalculationInfo)
	for entryName, entry := range lwr.Entries {
		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo != nil && calculationInfo.OwnerPoolName == pool {
				if dedicatedCalculationInfos[entryName] == nil {
					dedicatedCalculationInfos[entryName] = make(map[string]*CalculationInfo)
				}

				dedicatedCalculationInfos[entryName][subEntryName] = calculationInfo
			}
		}
	}
	return dedicatedCalculationInfos
}

// GetNUMACalculationResult returns numa-level calculation results
func (ce *CalculationEntries) GetNUMACalculationResult(container string, numa int64) (*NumaCalculationResult, bool) {
	info, ok := ce.Entries[container]
	if !ok || info == nil {
		return nil, false
	}

	if numaInfo, ok := info.CalculationResultsByNumas[numa]; ok {
		return numaInfo, true
	}
	return nil, false
}

// GetNUMAQuantities returns quantity in each numa for in this CalculationInfo
func (ci *CalculationInfo) GetNUMAQuantities() (map[int]int, error) {
	if ci == nil {
		return nil, fmt.Errorf("got nil ci")
	}

	numaQuantities := make(map[int]int)
	for numaId, numaResult := range ci.CalculationResultsByNumas {
		if numaResult == nil {
			general.Warningf("[GetTotalQuantity] got nil NUMA result")
			continue
		}

		var quantityUInt64 uint64 = 0
		for _, block := range numaResult.Blocks {
			if block == nil {
				general.Warningf("[GetTotalQuantity] got nil block")
				continue
			}
			quantityUInt64 += block.Result
		}

		quantityInt, err := general.CovertUInt64ToInt(quantityUInt64)
		if err != nil {
			return nil, fmt.Errorf("converting quantity: %d to int failed with error: %v",
				quantityUInt64, err)
		}

		numaIdInt, err := general.CovertInt64ToInt(numaId)
		if err != nil {
			return nil, fmt.Errorf("converting quantity: %d to int failed with error: %v",
				numaId, err)
		}
		numaQuantities[numaIdInt] = quantityInt
	}
	return numaQuantities, nil
}

// GetTotalQuantity returns total quantity for in this CalculationInfo
func (ci *CalculationInfo) GetTotalQuantity() (int, error) {
	numaQuantities, err := ci.GetNUMAQuantities()
	if err != nil {
		return 0, err
	}

	totalQuantity := 0
	for _, quantity := range numaQuantities {
		totalQuantity += quantity
	}
	return totalQuantity, nil
}

// GetCPUSet returns cpuset for this container by union all blocks for it
func (ci *CalculationInfo) GetCPUSet(entry, subEntry string, b BlockCPUSet) (machine.CPUSet, error) {
	cpusets := machine.NewCPUSet()
	for _, results := range ci.CalculationResultsByNumas {
		if results == nil {
			general.Warningf("got nil numa result entry: %s, subEntry: %s", entry, subEntry)
			continue
		}

		for _, block := range results.Blocks {
			if block == nil {
				general.Warningf("got nil block result entry: %s, subEntry: %s", entry, subEntry)
				continue
			}

			if cset, found := b[block.BlockId]; !found {
				return machine.CPUSet{}, fmt.Errorf("block %s not found, entry: %s, subEntry: %s", block.BlockId, entry, subEntry)
			} else {
				cpusets = cpusets.Union(cset)
			}
		}
	}
	return cpusets, nil
}

func IsBlockOfRelcaim(blockEntryNames [][]string) bool {
	for _, namesTuple := range blockEntryNames {
		entryName, subEntryName := namesTuple[0], namesTuple[1]

		if entryName == commonstate.PoolNameReclaim && subEntryName == commonstate.FakedContainerName {
			return true
		}
	}

	return false
}
