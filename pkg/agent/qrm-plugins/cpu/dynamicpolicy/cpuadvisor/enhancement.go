package cpuadvisor

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"k8s.io/klog/v2"
)

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
