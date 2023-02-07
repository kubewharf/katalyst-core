package cpuadvisor

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

func NewPoolCalculationEntries(poolName string) *CalculationEntries {
	ci := &CalculationInfo{
		OwnerPoolName:             poolName, // Must make sure pool names from cpu provision following qrm definition
		CalculationResultsByNumas: make(map[int64]*NumaCalculationResult),
	}

	return &CalculationEntries{Entries: map[string]*CalculationInfo{"": ci}}
}

func NewBlock(size uint64, blockID string) *Block {
	if blockID == "" {
		blockID = string(uuid.NewUUID())
	}
	return &Block{
		Result:         size,
		OverlapTargets: make([]*OverlapTarget, 0),
		BlockId:        blockID,
	}
}

type BlockWrapper struct {
	Block                 *Block
	PoolName              string
	ContainerInfo         *types.ContainerInfo
	NumaCalculationResult *NumaCalculationResult
}

// Join the blocks with same blockID
func (bw *BlockWrapper) Join(blockID string, set BlockSet) {
	blockWrappers := set.Get(blockID)
	if blockWrappers == nil || len(blockWrappers) == 0 {
		set.Add(bw)
		return
	}
	thisTarget := &OverlapTarget{}
	if bw.ContainerInfo == nil {
		thisTarget.OverlapTargetPoolName = bw.PoolName
		thisTarget.OverlapType = OverlapType_OverlapWithPool
	} else {
		thisTarget.OverlapTargetPodUid = bw.ContainerInfo.PodUID
		thisTarget.OverlapTargetContainerName = bw.ContainerInfo.ContainerName
		thisTarget.OverlapType = OverlapType_OverlapWithPod
	}

	if bw.Block.Result == blockWrappers[0].Block.Result {
		for _, blockWrapper := range blockWrappers {
			blockWrapper.Block.OverlapTargets = append(blockWrapper.Block.OverlapTargets, thisTarget)

			target := &OverlapTarget{}
			if blockWrapper.ContainerInfo == nil {
				target.OverlapTargetPoolName = blockWrapper.PoolName
				target.OverlapType = OverlapType_OverlapWithPool
			} else {
				target.OverlapTargetPodUid = blockWrapper.ContainerInfo.PodUID
				target.OverlapTargetContainerName = blockWrapper.ContainerInfo.ContainerName
				target.OverlapType = OverlapType_OverlapWithPod
			}
			bw.Block.OverlapTargets = append(bw.Block.OverlapTargets, target)
			bw.Block.BlockId = blockID
		}
		set.Add(bw)
	} else if bw.Block.Result > blockWrappers[0].Block.Result {
		newBlock := NewBlock(blockWrappers[0].Block.Result, blockID)
		newBlockWrapper := NewBlockWrapper(newBlock, bw.PoolName, bw.ContainerInfo, bw.NumaCalculationResult)
		bw.NumaCalculationResult.Blocks = append(bw.NumaCalculationResult.Blocks, newBlock)
		newBlockWrapper.Join(blockID, set)

		bw.Block.Result = bw.Block.Result - blockWrappers[0].Block.Result
		bw.Block.OverlapTargets = nil
		set.Add(bw)
	} else {
		for _, blockWrapper := range blockWrappers {
			newBlock := NewBlock(blockWrappers[0].Block.Result-bw.Block.Result, "")
			newBlockWrapper := NewBlockWrapper(newBlock, blockWrapper.PoolName, blockWrapper.ContainerInfo, blockWrapper.NumaCalculationResult)
			blockWrapper.NumaCalculationResult.Blocks = append(blockWrapper.NumaCalculationResult.Blocks, newBlock)
			blockWrapper.Block.Result = bw.Block.Result
			newBlockWrapper.Join(newBlock.BlockId, set)
		}
		bw.Join(bw.Block.BlockId, set)
	}
}

func NewBlockWrapper(block *Block, poolName string, containerInfo *types.ContainerInfo, numaCalculationResult *NumaCalculationResult) *BlockWrapper {
	return &BlockWrapper{
		Block:                 block,
		PoolName:              poolName,
		ContainerInfo:         containerInfo,
		NumaCalculationResult: numaCalculationResult,
	}
}

type BlockSet map[string][]*BlockWrapper

func NewBlockSet() BlockSet {
	return make(map[string][]*BlockWrapper)
}

func (bs BlockSet) Add(block *BlockWrapper) error {
	if block == nil || block.Block == nil {
		return fmt.Errorf("block is invalid")
	}
	blocks, ok := bs[block.Block.BlockId]
	if !ok {
		blocks = make([]*BlockWrapper, 0)
	}
	blocks = append(blocks, block)
	bs[block.Block.BlockId] = blocks
	return nil
}

func (bs BlockSet) Get(blockID string) []*BlockWrapper {
	bws, ok := bs[blockID]
	if !ok {
		return nil
	}
	return bws
}
