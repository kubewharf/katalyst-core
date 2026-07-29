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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestClassifyFakeNUMABlock(t *testing.T) {
	t.Parallel()

	normalShare := &advisorapi.BlockInfo{
		Block: advisorapi.Block{BlockId: "share"},
		OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
			"seedpool-test": {EntryName: "seedpool-test"},
		},
	}
	actualReclaim := &advisorapi.BlockInfo{
		Block: advisorapi.Block{BlockId: "reclaim"},
		OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
			commonstate.PoolNameReclaim: {EntryName: commonstate.PoolNameReclaim},
		},
	}

	require.Equal(t, fakeNUMABlockClassNormalShare, classifyFakeNUMABlock(normalShare))
	require.Equal(t, fakeNUMABlockClassActualReclaim, classifyFakeNUMABlock(actualReclaim))
}

func TestAllocateFakeNUMANormalShareBlocks_ReusesOwnPoolCPUSet(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	shareCPUSet := machine.NewCPUSet(26, 27, 74, 75)
	reclaimCPUSet := machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87)
	p.state.SetPodEntries(state.PodEntries{
		"seedpool-test": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("seedpool-test"),
				AllocationResult: shareCPUSet,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: reclaimCPUSet,
			},
		},
	}, false)

	block := &advisorapi.BlockInfo{
		Block: advisorapi.Block{BlockId: "new-share-block", Result: 4},
		OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
			"seedpool-test": {EntryName: "seedpool-test"},
		},
	}
	all := p.machineInfo.CPUDetails.CPUs()
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.allocateFakeNUMANormalShareBlocks(
		[]*advisorapi.BlockInfo{block},
		blockCPUSet,
		&all,
		&all,
		machine.NewCPUSet(),
	)
	require.NoError(t, err)
	require.Equal(t, shareCPUSet, blockCPUSet[block.BlockId])
	require.True(t, blockCPUSet[block.BlockId].Intersection(reclaimCPUSet).IsEmpty())
}

func TestGenerateBlockCPUSet_FakeNUMANormalShareDoesNotConsumePreviousReclaim(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	shareCPUSet := machine.NewCPUSet(26, 27, 74, 75)
	reclaimCPUSet := machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87)
	p.state.SetPodEntries(state.PodEntries{
		"seedpool-test": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("seedpool-test"),
				AllocationResult: shareCPUSet,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: reclaimCPUSet,
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"seedpool-test": {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: "seedpool-test",
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "new-share-block", Result: 4}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "new-reclaim-block", Result: 14}},
							},
						},
					},
				},
			},
		},
	}

	blockCPUSet, err := p.generateBlockCPUSet(resp)
	require.NoError(t, err)
	require.Equal(t, shareCPUSet, blockCPUSet["new-share-block"])
	require.Equal(t, reclaimCPUSet, blockCPUSet["new-reclaim-block"])
}
