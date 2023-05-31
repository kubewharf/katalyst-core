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

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// FakedContainerName represents a placeholder since pool entry has no container-level
// FakedNUMAID represents a placeholder since pools like shared/reclaimed will not contain a specific numa
const (
	EmptyOwnerPoolName = ""
	FakedContainerName = ""
	FakedNUMAID        = -1
)

type BlockCPUSet map[string]machine.CPUSet

func NewBlockCPUSet() BlockCPUSet {
	return make(BlockCPUSet)
}

func (b BlockCPUSet) GetCPUSet() machine.CPUSet {
	cpusets := machine.NewCPUSet()
	for _, cpuset := range b {
		cpusets = cpusets.Union(cpuset)
	}
	return cpusets
}

// GetBlocks parses ListAndWatchResponse and returns map[int]map[string]*Block,
// the map is keyed as numa id -> block id -> block
func (lwr *ListAndWatchResponse) GetBlocks() (map[int]map[string]*Block, error) {
	if lwr == nil {
		return nil, fmt.Errorf("got nil ListAndWatchResponse")
	}

	blocks := make(map[int]map[string]*Block)
	visBlocksToNUMA := make(map[string]int)
	for entryName, entry := range lwr.Entries {
		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo == nil {
				klog.Warningf("[ListAndWatchResponse.GetBlocks] got nil calculationInfo entry: %s, subEntry: %s",
					entryName, subEntryName)
				continue
			}

			for numaIdInt64, calculationResult := range calculationInfo.CalculationResultsByNumas {
				if calculationResult == nil {
					klog.Warningf("[ListAndWatchResponse.GetBlocks] got nil NUMA result entry: %s, subEntry: %s",
						entryName, subEntryName)
					continue
				}

				numaIdInt, err := general.CovertInt64ToInt(numaIdInt64)
				if err != nil {
					return nil, fmt.Errorf("parse nuam id: %d failed with error: %v entry: %s, subEntry: %s",
						numaIdInt64, err, entryName, subEntryName)
				}

				if blocks[numaIdInt] == nil {
					blocks[numaIdInt] = make(map[string]*Block)
				}

				for _, block := range calculationResult.Blocks {
					if block == nil {
						klog.Warningf("[ListAndWatchResponse.GetBlocks] got nil block result entry: %s, subEntry: %s",
							entryName, subEntryName)
						continue
					}

					blockId := block.BlockId
					if foundNUMAId, found := visBlocksToNUMA[blockId]; found && blocks[foundNUMAId][blockId] != nil {
						if foundNUMAId != numaIdInt {
							return nil, fmt.Errorf("found block: %s both in NUMA: %d and NUMA: %d, entry: %s, subEntry: %s",
								blockId, foundNUMAId, numaIdInt, entryName, subEntryName)
						} else if blocks[foundNUMAId][blockId].Result != block.Result {
							return nil, fmt.Errorf("found block: %s result is different with current block: %s result, entry: %s, subEntry: %s",
								blocks[foundNUMAId][blockId].String(), block.String(), entryName, subEntryName)
						}
					}

					blocks[numaIdInt][blockId] = block
					visBlocksToNUMA[blockId] = numaIdInt
				}
			}
		}
	}
	return blocks, nil
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
			klog.Warningf("[GetTotalQuantity] got nil NUMA result")
			continue
		}

		var quantityUInt64 uint64 = 0
		for _, block := range numaResult.Blocks {
			if block == nil {
				klog.Warningf("[GetTotalQuantity] got nil block")
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

	var totalQuantity = 0
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
