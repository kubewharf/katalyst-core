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
	fmt "fmt"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// FakedContainerID represents a placeholder since pool entry has no container-level
const FakedContainerID = ""

// GetNumaCalculationResult returns numa-level calculation results
func (m *CalculationEntries) GetNumaCalculationResult(container string, numa int64) (*NumaCalculationResult, bool) {
	info, ok := m.Entries[container]
	if !ok || info == nil {
		return nil, false
	}

	if numaInfo, ok := info.CalculationResultsByNumas[numa]; ok {
		return numaInfo, true
	}
	return nil, false
}

// GetBlocks parses ListAndWatchResponse and returns map[int]map[string]*Block
// whose first level key is NUMA id and second level key is block id
func (m *ListAndWatchResponse) GetBlocks() (map[int]map[string]*Block, error) {
	if m == nil {
		return nil, fmt.Errorf("got nil ListAndWatchResponse")
	}

	blocks := make(map[int]map[string]*Block)

	visBlocksToNUMA := make(map[string]int)

	for entryName, entry := range m.Entries {
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
