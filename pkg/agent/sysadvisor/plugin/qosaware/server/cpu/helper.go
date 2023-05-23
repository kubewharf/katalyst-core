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

package cpu

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

// NewPoolCalculationEntries returns CalculationEntries,
// and it will only fill up with OwnerPoolName and leaves numa info empty.
func NewPoolCalculationEntries(poolName string) *cpuadvisor.CalculationEntries {
	ci := &cpuadvisor.CalculationInfo{
		OwnerPoolName:             poolName,
		CalculationResultsByNumas: make(map[int64]*cpuadvisor.NumaCalculationResult),
	}

	return &cpuadvisor.CalculationEntries{Entries: map[string]*cpuadvisor.CalculationInfo{"": ci}}
}

// NewBlock constructs a Block structure; generate a new one if blockID is missed
func NewBlock(size uint64, blockID string) *cpuadvisor.Block {
	if blockID == "" {
		blockID = string(uuid.NewUUID())
	}

	return &cpuadvisor.Block{
		Result:         size,
		OverlapTargets: make([]*cpuadvisor.OverlapTarget, 0),
		BlockId:        blockID,
	}
}

// internalBlock works as a packed structure for Block;
// aside for Block, it also stores some extra info to speed up efficiency.
type internalBlock struct {
	// Block stores the standard Block info
	// - it is thread-safe since it is referred only by this internalBlock
	Block *cpuadvisor.Block
	// NumaID stores the numa node that this Block belongs to
	NumaID int64

	// only one of PoolName and ContainerInfo can be set as valid
	// PoolName stores pool that owns this block
	// - it may be nil if this block belongs to pod rather than pool
	PoolName string
	// ContainerInfo stores container that own this block
	// - it may be nil if this block belongs to pool rather than pod
	// - it is thread-safe since it is deep-copied when constructed internalBlock
	ContainerInfo *types.ContainerInfo

	// NumaCalculationResult stores
	// - it is thread-safe since it is referred only by this internalBlock
	NumaCalculationResult *cpuadvisor.NumaCalculationResult
}

func NewInnerBlock(block *cpuadvisor.Block, numaID int64, poolName string, containerInfo *types.ContainerInfo,
	numaCalculationResult *cpuadvisor.NumaCalculationResult) *internalBlock {
	ib := &internalBlock{
		Block:                 block,
		NumaID:                numaID,
		PoolName:              poolName,
		NumaCalculationResult: numaCalculationResult,
	}

	if containerInfo != nil {
		ib.ContainerInfo = containerInfo.Clone()
	}
	return ib
}

// initialOverlapTarget constructs cpuadvisor.OverlapTarget based on Block info
func (ib *internalBlock) initialOverlapTarget() *cpuadvisor.OverlapTarget {
	target := &cpuadvisor.OverlapTarget{}

	if ib.ContainerInfo == nil {
		target.OverlapTargetPoolName = ib.PoolName
		target.OverlapType = cpuadvisor.OverlapType_OverlapWithPool
	} else {
		target.OverlapTargetPodUid = ib.ContainerInfo.PodUID
		target.OverlapTargetContainerName = ib.ContainerInfo.ContainerName
		target.OverlapType = cpuadvisor.OverlapType_OverlapWithPod
	}

	return target
}

// for multiple Block in one single NUMA node, join tries to
// split the existed Block into more Block to ensure we must
// aggregate different Block according to the size of them.
func (ib *internalBlock) join(blockID string, set blockSet) {
	ibList := set.get(blockID)
	if ibList == nil || len(ibList) == 0 {
		_ = set.add(ib)
		return
	}

	// support to merge current Block into blockSet iff they belong to the same numa node
	if ib.NumaID != ibList[0].NumaID {
		return
	}

	thisTarget := ib.initialOverlapTarget()
	if ib.Block.Result == ibList[0].Block.Result {
		// if cpu size equals, it means current Block can share the same;
		// otherwise, a split Block must be generated to ensure size aggregation

		for _, innerBlock := range ibList {
			innerBlock.Block.OverlapTargets = append(innerBlock.Block.OverlapTargets, thisTarget)

			target := innerBlock.initialOverlapTarget()
			ib.Block.OverlapTargets = append(ib.Block.OverlapTargets, target)
			ib.Block.BlockId = blockID
		}
		_ = set.add(ib)
	} else if ib.Block.Result > ibList[0].Block.Result {
		// if cpu size for current Block is greater, split current Block into multiple ones
		// i.e. one to match with cpu size for existing blockSet, one to add as a new blockSet

		newBlock := NewBlock(ibList[0].Block.Result, blockID)
		newInnerBlock := NewInnerBlock(newBlock, ib.NumaID, ib.PoolName, ib.ContainerInfo, ib.NumaCalculationResult)
		newInnerBlock.join(blockID, set)

		ib.NumaCalculationResult.Blocks = append(ib.NumaCalculationResult.Blocks, newBlock)
		ib.Block.Result = ib.Block.Result - ibList[0].Block.Result
		ib.Block.OverlapTargets = nil
		_ = set.add(ib)
	} else {
		// if cpu size for current Block is smaller, split existing Block in blockSet into multiple ones
		// i.e. one to match with cpu size for current Block, one to add as a new blockSet

		for _, innerBlock := range ibList {
			newBlock := NewBlock(ibList[0].Block.Result-ib.Block.Result, "")
			newInnerBlock := NewInnerBlock(newBlock, innerBlock.NumaID, innerBlock.PoolName, innerBlock.ContainerInfo, innerBlock.NumaCalculationResult)
			newInnerBlock.join(newBlock.BlockId, set)

			innerBlock.NumaCalculationResult.Blocks = append(innerBlock.NumaCalculationResult.Blocks, newBlock)
			innerBlock.Block.Result = ib.Block.Result
		}

		ib.join(ib.Block.BlockId, set)
	}
}

// blockSet is a global variable to store, and the mapping relation
// block id -> the internalBlock that belongs to this block id
//
// those internalBlock sharing with the same id should satisfy several constraints
// - they belong to the same NUMA Node (though there exists no NUMA concepts here)
// - they share with the same cpuset pool
type blockSet map[string][]*internalBlock

func NewBlockSet() blockSet {
	return make(map[string][]*internalBlock)
}

func (bs blockSet) add(ib *internalBlock) error {
	if ib == nil || ib.Block == nil {
		return fmt.Errorf("internal block is invalid")
	}

	internalBlocks, ok := bs[ib.Block.BlockId]
	if !ok {
		internalBlocks = make([]*internalBlock, 0)
	}

	internalBlocks = append(internalBlocks, ib)
	bs[ib.Block.BlockId] = internalBlocks
	return nil
}

func (bs blockSet) get(blockID string) []*internalBlock {
	internalBlocks, ok := bs[blockID]
	if !ok {
		return nil
	}
	return internalBlocks
}

// GetNumaCalculationResult returns numa-level calculation results
func GetNumaCalculationResult(calculationEntriesMap map[string]*cpuadvisor.CalculationEntries,
	entryName, containerName string, numa int64) (*cpuadvisor.NumaCalculationResult, bool) {
	entries, ok := calculationEntriesMap[entryName]
	if !ok {
		return nil, false
	}

	return entries.GetNUMACalculationResult(containerName, numa)
}
