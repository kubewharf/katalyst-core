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
	all := p.machine.machineInfo.CPUDetails.CPUs()
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

	blockCPUSet, err := p.generateBlockCPUSet(resp, nil, false)
	require.NoError(t, err)
	require.Equal(t, shareCPUSet, blockCPUSet["new-share-block"])
	require.Equal(t, reclaimCPUSet, blockCPUSet["new-reclaim-block"])
}

func TestGenerateLegacyBlockCPUSet_HardPartitionBalancesFakeReclaim(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		quantity    uint64
		wantPerNUMA []int
	}{
		{name: "four CPUs", quantity: 4, wantPerNUMA: []int{2, 2}},
		// whole-core water-filling on cpusPerCore==2: 6 CPUs are handed out one
		// complete core at a time to the least-loaded NUMA (NUMA0<-2, NUMA1<-2,
		// NUMA0<-2), so NUMA0 ends with 4 and NUMA1 with 2. an odd quantity is
		// rejected upstream because it cannot be a whole-core multiple.
		{name: "six CPUs", quantity: 6, wantPerNUMA: []int{4, 2}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy := newLegacyHardPartitionTestPolicy(t)
			blockCPUSet, err := policy.generateBlockCPUSet(
				legacyFakeReclaimResponse(tc.quantity, false), nil, false)
			require.NoError(t, err)

			reclaim := blockCPUSet["reclaim"]
			require.Equal(t, int(tc.quantity), reclaim.Size())
			for numaID, want := range tc.wantPerNUMA {
				require.Equal(t, want, reclaim.Intersection(
					policy.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)).Size())
			}
		})
	}
}

func TestGenerateLegacyBlockCPUSet_HardPartitionRebalancesPreviousFakeReclaim(t *testing.T) {
	t.Parallel()

	policy := newLegacyHardPartitionTestPolicy(t)
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
	}, false)

	blockCPUSet, err := policy.generateBlockCPUSet(legacyFakeReclaimResponse(4, false), nil, true)
	require.NoError(t, err)
	reclaim := blockCPUSet["reclaim"]
	require.Equal(t, 2, reclaim.Intersection(policy.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 2, reclaim.Intersection(policy.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1)).Size())
}

func TestGenerateLegacyBlockCPUSet_HardPartitionReservesBeforeNormalShare(t *testing.T) {
	t.Parallel()

	policy := newLegacyHardPartitionTestPolicy(t)
	resp := legacyFakeReclaimResponse(4, false)
	resp.Entries["share"] = &advisorapi.CalculationEntries{
		Entries: map[string]*advisorapi.CalculationInfo{
			commonstate.FakedContainerName: {
				OwnerPoolName: "share",
				CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
					commonstate.FakedNUMAID: {
						Blocks: []*advisorapi.Block{{BlockId: "share", Result: 5}},
					},
				},
			},
		},
	}

	got, err := policy.generateBlockCPUSet(resp, nil, true)
	require.Nil(t, got)
	require.ErrorContains(t, err, "allocate normal share block")
}

func TestGenerateLegacyBlockCPUSet_HardPartitionRejectsInsufficientCapacity(t *testing.T) {
	t.Parallel()

	policy := newLegacyHardPartitionTestPolicy(t)
	got, err := policy.generateBlockCPUSet(legacyFakeReclaimResponse(9, false), nil, true)
	require.Nil(t, got)
	require.ErrorContains(t, err, "eligible capacity 8 is smaller than quantity 9")
}

func TestGenerateLegacyBlockCPUSet_HardPartitionKeepsOverlapReclaimLegacy(t *testing.T) {
	t.Parallel()

	policy := newLegacyHardPartitionTestPolicy(t)
	previous := machine.NewCPUSet(0, 1, 2, 3)
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: previous,
			},
		},
	}, false)

	blockCPUSet, err := policy.generateBlockCPUSet(legacyFakeReclaimResponse(4, true), nil, true)
	require.NoError(t, err)
	require.Equal(t, previous, blockCPUSet["reclaim"])
}

func TestGenerateLegacyBlockCPUSet_HardPartitionDisabledKeepsLegacyPlacement(t *testing.T) {
	t.Parallel()

	policy := newLegacyHardPartitionTestPolicy(t)
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = false
	previous := machine.NewCPUSet(0, 1, 2, 3)
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: previous,
			},
		},
	}, false)

	blockCPUSet, err := policy.generateBlockCPUSet(legacyFakeReclaimResponse(4, false), nil, false)
	require.NoError(t, err)
	require.Equal(t, previous, blockCPUSet["reclaim"])
}

func newLegacyHardPartitionTestPolicy(t *testing.T) *DynamicPolicy {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	return policy
}

func legacyFakeReclaimResponse(quantity uint64, overlap bool) *advisorapi.ListAndWatchResponse {
	var overlapTargets []*advisorapi.OverlapTarget
	if overlap {
		overlapTargets = []*advisorapi.OverlapTarget{{
			OverlapTargetPoolName: commonstate.PoolNameShare,
			OverlapType:           advisorapi.OverlapType_OverlapWithPool,
		}}
	}
	return &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: false,
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{
									BlockId:        "reclaim",
									Result:         quantity,
									OverlapTargets: overlapTargets,
								}},
							},
						},
					},
				},
			},
		},
	}
}
