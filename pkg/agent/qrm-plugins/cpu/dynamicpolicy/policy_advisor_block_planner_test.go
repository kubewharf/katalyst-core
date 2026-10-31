/*
Copyright 2026 The Katalyst Authors.

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
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type advisorBlockTestAlias struct {
	entry    string
	subEntry string
	owner    string
	numaID   int64
	blockID  string
	quantity uint64
	overlap  bool
}

func advisorBlockTestResponse(aliases []advisorBlockTestAlias, r *rand.Rand) *advisorapi.ListAndWatchResponse {
	shuffled := append([]advisorBlockTestAlias(nil), aliases...)
	r.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	resp := &advisorapi.ListAndWatchResponse{Entries: make(map[string]*advisorapi.CalculationEntries)}
	for _, alias := range shuffled {
		var overlapTargets []*advisorapi.OverlapTarget
		if alias.overlap {
			overlapTargets = []*advisorapi.OverlapTarget{{
				OverlapTargetPoolName: commonstate.PoolNameShare,
				OverlapType:           advisorapi.OverlapType_OverlapWithPool,
			}}
		}
		if resp.Entries[alias.entry] == nil {
			resp.Entries[alias.entry] = &advisorapi.CalculationEntries{
				Entries: make(map[string]*advisorapi.CalculationInfo),
			}
		}
		info := resp.Entries[alias.entry].Entries[alias.subEntry]
		if info == nil {
			info = &advisorapi.CalculationInfo{
				OwnerPoolName:             alias.owner,
				CalculationResultsByNumas: make(map[int64]*advisorapi.NumaCalculationResult),
			}
			resp.Entries[alias.entry].Entries[alias.subEntry] = info
		}
		if info.OwnerPoolName != alias.owner {
			panic("test aliases reuse an entry/sub-entry with different owners")
		}
		if info.CalculationResultsByNumas[alias.numaID] == nil {
			info.CalculationResultsByNumas[alias.numaID] = &advisorapi.NumaCalculationResult{}
		}
		info.CalculationResultsByNumas[alias.numaID].Blocks = append(
			info.CalculationResultsByNumas[alias.numaID].Blocks,
			&advisorapi.Block{
				BlockId:        alias.blockID,
				Result:         alias.quantity,
				OverlapTargets: overlapTargets,
			},
		)
	}
	return resp
}

func TestBuildAdvisorBlockDescriptors_StableAcrossMapOrderAndBlockIDRotation(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	aliases := []advisorBlockTestAlias{
		{entry: "pool-b", subEntry: commonstate.FakedContainerName, owner: "pool-b", numaID: 0, blockID: "old-b", quantity: 2},
		{entry: "pool-a", subEntry: commonstate.FakedContainerName, owner: "pool-a", numaID: 0, blockID: "old-a", quantity: 2},
		{entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName, owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID, blockID: "old-r", quantity: 4},
	}

	var stableDescriptors []advisorBlockDescriptor
	for seed := int64(0); seed < 20; seed++ {
		rotated := append([]advisorBlockTestAlias(nil), aliases...)
		resp := advisorBlockTestResponse(rotated, rand.New(rand.NewSource(seed)))
		descriptors, err := buildAdvisorBlockDescriptors(
			resp,
			p.machine.machineInfo.CPUDetails,
			p.state.GetPodEntries(),
			nil,
			machine.NewCPUSet(),
		)
		require.NoError(t, err)

		if seed == 0 {
			stableDescriptors = descriptors
		} else {
			require.Equal(t, stableDescriptors, descriptors)
		}
	}
}

func TestBuildAdvisorBlockDescriptors_MandatoryFakeEligibleExcludesNonReclaimableCPUs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	allCPUs := topology.CPUDetails.CPUs()
	nonReclaimable := machine.NewCPUSet(0, 3, 4)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry:    commonstate.PoolNameReclaim,
		subEntry: commonstate.FakedContainerName,
		owner:    commonstate.PoolNameReclaim,
		numaID:   commonstate.FakedNUMAID,
		blockID:  "fake-reclaim",
		quantity: 2,
	}}, rand.New(rand.NewSource(0)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp, topology.CPUDetails, nil, nil, nonReclaimable)

	require.NoError(t, err)
	require.Len(t, descriptors, 1)
	require.Equal(t, advisorBlockClassMandatoryReclaim, descriptors[0].Class)
	require.Equal(t, commonstate.FakedNUMAID, descriptors[0].NUMAID)
	require.True(t, descriptors[0].Eligible.Equals(allCPUs.Difference(nonReclaimable)))
}

// TestBuildAdvisorBlockDescriptors_MandatoryEligibleExcludesIsolationCPUs proves
// that isolation-pool CPUs are protected from mandatory reclaim eligibility.
//
// Isolation pods own exclusive whole cores that the bulkhead domain model folds
// into the primary (NonReclaimPool) side; they can never be released to reclaim.
// If mandatory-reclaim eligibility still includes an isolation CPU, whole-core
// alignment can select the isolation core's SMT sibling as a reclaim donor,
// which later fails the bulkhead core-release witness with
// "incomplete required core release witness: source=primary destination=reclaim".
// buildAdvisorBlockDescriptors must therefore exclude isolation CPUs observed in
// the committed pod entries, exactly as it already excludes the non-reclaimable
// resource-package pinned set.
func TestBuildAdvisorBlockDescriptors_MandatoryEligibleExcludesIsolationCPUs(t *testing.T) {
	t.Parallel()

	// 8 CPUs / 1 socket / 2 NUMA -> cpusPerCore=2. NUMA0 owns {0,1,4,5} with SMT
	// pairs (0,4) and (1,5); isolate CPU 1 so its sibling 5 must stay protected.
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	isolationCPUs := machine.NewCPUSet(1)

	podEntries := state.PodEntries{
		commonstate.PoolNamePrefixIsolation + "-pod1": state.ContainerEntries{
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
				},
				AllocationResult: isolationCPUs.Clone(),
			},
		},
	}

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry:    commonstate.PoolNameReclaim,
		subEntry: commonstate.FakedContainerName,
		owner:    commonstate.PoolNameReclaim,
		numaID:   0,
		blockID:  "reclaim-numa0",
		quantity: 2,
	}}, rand.New(rand.NewSource(0)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp, topology.CPUDetails, podEntries, nil, machine.NewCPUSet())
	require.NoError(t, err)
	require.Len(t, descriptors, 1)
	require.Equal(t, advisorBlockClassMandatoryReclaim, descriptors[0].Class)
	require.True(t, descriptors[0].Eligible.Intersection(isolationCPUs).IsEmpty(),
		"isolation CPUs must be excluded from mandatory reclaim eligibility, got %s",
		descriptors[0].Eligible.String())
	require.True(t, descriptors[0].Eligible.Equals(numa0.Difference(isolationCPUs)),
		"mandatory reclaim eligibility must be NUMA0 minus isolation, got %s",
		descriptors[0].Eligible.String())
}

func TestPlanDisjointAdvisorBlocks_ResponseDescriptorPlanAggregatesExpandedDemandsByBlockID(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry:    commonstate.PoolNameReclaim,
		subEntry: commonstate.FakedContainerName,
		owner:    commonstate.PoolNameReclaim,
		numaID:   commonstate.FakedNUMAID,
		blockID:  "fake-reclaim",
		quantity: 4,
	}}, rand.New(rand.NewSource(0)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true

	descriptors, err := buildAdvisorBlockDescriptors(
		resp,
		topology.CPUDetails,
		p.state.GetPodEntries(),
		nil,
		machine.NewCPUSet(),
	)
	require.NoError(t, err)
	require.Len(t, descriptors, 1)
	require.Equal(t, "fake-reclaim", descriptors[0].BlockID)

	planned, err := p.planDisjointAdvisorBlocks(resp, true)
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, 4, planned["fake-reclaim"].Size())
	require.Equal(t, 2, planned["fake-reclaim"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 2, planned["fake-reclaim"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(1)).Size())
	requireCoreAligned(t, topology, planned["fake-reclaim"])
}

func TestPlanDisjointAdvisorBlocks_ZeroQuantityFakeBlockRemainsInCompletePlan(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry:    commonstate.PoolNameReclaim,
		subEntry: commonstate.FakedContainerName,
		owner:    commonstate.PoolNameReclaim,
		numaID:   commonstate.FakedNUMAID,
		blockID:  "fake-reclaim-zero",
		quantity: 0,
	}}, rand.New(rand.NewSource(0)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true

	planned, err := p.planDisjointAdvisorBlocks(resp, true)

	require.NoError(t, err)
	require.Contains(t, planned, "fake-reclaim-zero")
	require.True(t, planned["fake-reclaim-zero"].IsEmpty())
}

func TestGenerateAndValidateDisjointAdvisorBlocks_ZeroQuantityFakeBlock(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry:    commonstate.PoolNameReclaim,
		subEntry: commonstate.FakedContainerName,
		owner:    commonstate.PoolNameReclaim,
		numaID:   commonstate.FakedNUMAID,
		blockID:  "fake-reclaim-zero",
		quantity: 0,
	}}, rand.New(rand.NewSource(0)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	planned, err := p.generateBlockCPUSet(resp, featureGates, true)

	require.NoError(t, err)
	require.Contains(t, planned, "fake-reclaim-zero")
	require.True(t, planned["fake-reclaim-zero"].IsEmpty())
	require.NoError(t, p.validateHardPartitionBlockPlan(resp, planned, true))
}

func TestGenerateBlockCPUSetOwnerUnionsStableAcrossRandomMapOrderAndBlockIDRotation(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.state.SetPodEntries(state.PodEntries{
		"pod-a": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-a",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
				},
				AllocationResult: machine.NewCPUSet(0, 1),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(2, 3),
				},
			},
		},
	}, false)

	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}
	ownerUnions := func(t *testing.T, resp *advisorapi.ListAndWatchResponse, blocks advisorapi.BlockCPUSet) map[string]machine.CPUSet {
		t.Helper()
		got := make(map[string]machine.CPUSet)
		for entryName, entries := range resp.Entries {
			for subEntryName, info := range entries.Entries {
				cpus, err := info.GetCPUSet(entryName, subEntryName, blocks)
				require.NoError(t, err)
				owner := canonicalAdvisorBlockOwner(info.OwnerPoolName, entryName, subEntryName, "")
				got[owner] = got[owner].Union(cpus)
			}
		}
		return got
	}

	dedicatedOwner := canonicalAdvisorBlockOwner(
		commonstate.PoolNameDedicated, "pod-a", "main", "")
	reclaimOwner := canonicalAdvisorBlockOwner(
		commonstate.PoolNameReclaim, commonstate.PoolNameReclaim,
		commonstate.FakedContainerName, "")
	wantOwners := map[string]machine.CPUSet{
		dedicatedOwner: machine.NewCPUSet(0, 1),
		reclaimOwner:   coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 2, 3),
	}
	wantUnion := wantOwners[dedicatedOwner].Union(wantOwners[reclaimOwner])
	for seed := int64(0); seed < 1000; seed++ {
		aliases := []advisorBlockTestAlias{
			{
				entry: "pod-a", subEntry: "main", owner: commonstate.PoolNameDedicated,
				numaID: 0, blockID: fmt.Sprintf("dedicated-a-%d", seed), quantity: 1,
			},
			{
				entry: "pod-a", subEntry: "main", owner: commonstate.PoolNameDedicated,
				numaID: 0, blockID: fmt.Sprintf("dedicated-b-%d", seed), quantity: 1,
			},
			{
				entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
				owner: commonstate.PoolNameReclaim, numaID: 0,
				blockID: fmt.Sprintf("reclaim-%d", seed), quantity: 2,
			},
		}
		resp := advisorBlockTestResponse(aliases, rand.New(rand.NewSource(seed)))
		resp.DisableDedicatedCoresOverlapReclaimedCores = true

		blocks, err := p.generateBlockCPUSet(resp, featureGates, false)
		require.NoError(t, err, "seed %d", seed)
		got := ownerUnions(t, resp, blocks)
		require.Equal(t, wantOwners, got, "seed %d owner keys or assignments", seed)
		require.True(t, got[dedicatedOwner].Intersection(got[reclaimOwner]).IsEmpty(),
			"seed %d dedicated and reclaim owners overlap", seed)
		require.Equal(t, wantUnion, got[dedicatedOwner].Union(got[reclaimOwner]),
			"seed %d owner union does not cover the complete partition", seed)
	}
}

func TestGenerateBlockCPUSetSkipsDefaultShareUpperBound(t *testing.T) {
	t.Parallel()

	for _, disjoint := range []bool{false, true} {
		disjoint := disjoint
		t.Run(fmt.Sprintf("disjoint=%t", disjoint), func(t *testing.T) {
			t.Parallel()

			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

			resp := advisorBlockTestResponse([]advisorBlockTestAlias{
				{
					entry: commonstate.PoolNameShare, subEntry: commonstate.FakedContainerName,
					owner: commonstate.PoolNameShare, numaID: commonstate.FakedNUMAID,
					blockID: "share-upper-bound", quantity: 96,
				},
				{
					entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
					owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
					blockID: "reclaim", quantity: 4,
				},
			}, rand.New(rand.NewSource(1)))
			resp.DisableDedicatedCoresOverlapReclaimedCores = disjoint

			featureGates := map[string]*advisorsvc.FeatureGate{
				feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
					Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
				},
			}
			blocks, err := p.generateBlockCPUSet(resp, featureGates, false)
			require.NoError(t, err)
			require.NotContains(t, blocks, "share-upper-bound")
			require.Equal(t, 4, blocks["reclaim"].Size())
		})
	}
}

func TestGenerateBlockCPUSetNormalizesRealMandatoryReclaimToWholeCore(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 48, 49),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1, 48, 49),
				},
			},
		},
		"pod-0": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-0",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
				},
				AllocationResult: machine.NewCPUSet(2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59),
				},
			},
		},
	}, false)

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: "pod-0", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 0, blockID: "dedicated-0", quantity: 21,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "reclaim-0", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID, blockID: "fake-reclaim", quantity: 2,
		},
	}, rand.New(rand.NewSource(1)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true

	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}
	blocks, err := p.generateBlockCPUSet(resp, featureGates, true)

	require.NoError(t, err)
	// The hard/ramp-up whole-core normalization borrows upward to a complete core.
	require.Equal(t, 20, blocks["dedicated-0"].Size())
	require.Equal(t, 4, blocks["reclaim-0"].Size())
	require.True(t, blocks["dedicated-0"].Intersection(blocks["reclaim-0"]).IsEmpty())
}

func TestGenerateBlockCPUSetHardNormalizationIgnoresUnavailableHeterogeneousCore(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 5, NumCores: 3, NumSockets: 1, NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		},
	}
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReserve: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
	}, false)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReserve, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReserve, numaID: commonstate.FakedNUMAID,
			blockID: "reserve", quantity: 1,
		},
		{
			entry: "pod-0", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 0, blockID: "dedicated-0", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "reclaim-0", quantity: 1,
		},
	}, rand.New(rand.NewSource(1)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	blocks, err := p.generateBlockCPUSet(resp, featureGates, true)

	require.NoError(t, err)
	require.True(t, blocks["reserve"].Equals(machine.NewCPUSet(0)))
	require.Equal(t, 2, blocks["dedicated-0"].Size())
	require.Equal(t, 2, blocks["reclaim-0"].Size())
	require.True(t, blocks["dedicated-0"].Intersection(blocks["reclaim-0"]).IsEmpty())
	require.True(t, blocks["dedicated-0"].Union(blocks["reclaim-0"]).Equals(machine.NewCPUSet(1, 2, 3, 4)))
}

func TestGenerateBlockCPUSetSteadyNormalizesRealMandatoryReclaimToWholeCore(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: "pod-0", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 0, blockID: "dedicated-0", quantity: 21,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "reclaim-0", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "fake-reclaim", quantity: 2,
		},
	}, rand.New(rand.NewSource(1)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	blocks, err := p.generateBlockCPUSet(resp, featureGates, false)

	require.NoError(t, err)
	// The steady real-NUMA path whole-core normalizes the odd advice (3) to the
	// committed-anchored lower value (2) rather than borrowing a dedicated core
	// upward to 4; the released CPU returns to dedicated (21 -> 22).
	require.Equal(t, 22, blocks["dedicated-0"].Size())
	require.Equal(t, 2, blocks["reclaim-0"].Size())
	require.True(t, blocks["dedicated-0"].Intersection(blocks["reclaim-0"]).IsEmpty())
}

func TestBuildAdvisorBlockDescriptors_BlockIDIsOnlyFinalTieBreak(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	build := func(ids []string) []advisorBlockDescriptor {
		resp := advisorBlockTestResponse([]advisorBlockTestAlias{
			{entry: "pool-a", subEntry: commonstate.FakedContainerName, owner: "pool-a", numaID: 0, blockID: ids[0], quantity: 2},
			{entry: "pool-a", subEntry: commonstate.FakedContainerName, owner: "pool-a", numaID: 0, blockID: ids[1], quantity: 2},
		}, rand.New(rand.NewSource(7)))
		descriptors, err := buildAdvisorBlockDescriptors(resp, p.machine.machineInfo.CPUDetails, nil, nil, machine.NewCPUSet())
		require.NoError(t, err)
		return descriptors
	}

	first := build([]string{"block-b", "block-a"})
	rotated := build([]string{"new-z", "new-y"})
	require.Equal(t, []string{"block-a", "block-b"}, []string{first[0].BlockID, first[1].BlockID})
	require.Equal(t, []string{"new-y", "new-z"}, []string{rotated[0].BlockID, rotated[1].BlockID})
	for i := range first {
		first[i].BlockID = ""
		rotated[i].BlockID = ""
	}
	require.Equal(t, first, rotated)
}

func TestBuildAdvisorBlockDescriptors_IntersectsAliasEligibilityAndAggregatesOldPreferred(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.state.SetPodEntries(state.PodEntries{
		"pod-a": {
			"container-a": &state.AllocationInfo{
				AllocationResult: machine.NewCPUSet(1, 2),
			},
		},
		"pool-a": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}, false)

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "pod-a", subEntry: "container-a", owner: "pool-a", numaID: 0, blockID: "alias", quantity: 2},
		{entry: "pool-a", subEntry: commonstate.FakedContainerName, owner: "pool-a", numaID: 0, blockID: "alias", quantity: 2},
	}, rand.New(rand.NewSource(1)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		p.state.GetPodEntries(),
		nil,
		machine.NewCPUSet(),
	)
	require.NoError(t, err)
	require.Len(t, descriptors, 1)
	require.Equal(t, []string{
		"pool-a\x00pod-a\x00container-a\x00",
		"pool-a\x00pool-a\x00" + commonstate.FakedContainerName + "\x00",
	}, descriptors[0].Owners)
	require.Equal(t, machine.NewCPUSet(1, 2, 3), descriptors[0].OldPreferred)
}

func TestBuildAdvisorBlockDescriptors_PreservesRawReclaimAggregateAndLeafOwnership(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	rawAggregate := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 2)
	realLeaf := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationResult: rawAggregate,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: realLeaf,
				},
			},
		},
	}, false)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "aggregate", quantity: 4,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0,
			blockID: "leaf", quantity: 2,
		},
	}, rand.New(rand.NewSource(13)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp, p.machine.machineInfo.CPUDetails, p.state.GetPodEntries(), nil, machine.NewCPUSet())
	require.NoError(t, err)
	require.Len(t, descriptors, 2)
	byID := make(map[string]advisorBlockDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		byID[descriptor.BlockID] = descriptor
	}
	require.Equal(t, rawAggregate, byID["aggregate"].Committed)
	require.Equal(t, realLeaf, byID["leaf"].Committed)

	snapshot, err := buildSteadyFakeNUMACommittedSnapshot(
		descriptors, p.machine.machineInfo.CPUDetails.CPUs())
	require.NoError(t, err)
	require.Equal(t, rawAggregate, snapshot.rawReclaimAggregate)
	require.Equal(t, rawAggregate, snapshot.reclaim)
	require.Equal(t, rawAggregate.Difference(realLeaf),
		committedAssignmentForTest(t, &snapshot, "aggregate").cpus)
	require.Equal(t, realLeaf,
		committedAssignmentForTest(t, &snapshot, "leaf").cpus)
	require.NoError(t, validateCommittedSteadyFakeNUMASnapshot(snapshot, nil, p.machine.machineInfo.CPUTopology))
}

func TestBuildAdvisorBlockDescriptors_PreservesCommittedCPUsOutsideEligibility(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	rawAggregate := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1)
	threads := rawAggregate.ToSliceInt()
	require.Len(t, threads, 2)
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationResult: rawAggregate,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: rawAggregate,
				},
			},
		},
	}, false)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "aggregate", quantity: 2,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0,
			blockID: "leaf", quantity: 1,
		},
	}, rand.New(rand.NewSource(14)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp, p.machine.machineInfo.CPUDetails, p.state.GetPodEntries(), nil, machine.NewCPUSet(threads[1]))
	require.NoError(t, err)
	byID := make(map[string]advisorBlockDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		byID[descriptor.BlockID] = descriptor
	}
	require.Equal(t, rawAggregate, byID["aggregate"].Committed)
	require.Equal(t, rawAggregate, byID["leaf"].Committed)
	require.Equal(t, machine.NewCPUSet(threads[0]), byID["leaf"].OldPreferred)

	snapshot, err := buildSteadyFakeNUMACommittedSnapshot(
		descriptors, p.machine.machineInfo.CPUDetails.CPUs())
	require.NoError(t, err)
	require.ErrorContains(t,
		validateCommittedSteadyFakeNUMASnapshot(snapshot, nil, p.machine.machineInfo.CPUTopology),
		fmt.Sprintf(`committed block "leaf" is outside eligibility: %d`, threads[1]))
}

func TestBuildAdvisorBlockDescriptors_FailsClosedForDifferentAliasResourcePackages(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "pod-b", subEntry: "container-b", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-b"), numaID: 0, blockID: "alias", quantity: 2},
		{entry: "pod-a", subEntry: "container-a", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2},
	}, rand.New(rand.NewSource(2)))

	_, err := buildAdvisorBlockDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		nil,
		map[string]machine.CPUSet{
			"rp-a": machine.NewCPUSet(0, 1, 2),
			"rp-b": machine.NewCPUSet(1, 2, 3),
		},
		machine.NewCPUSet(),
	)
	require.ErrorContains(t, err, `block "alias" aliases have incompatible resource packages`)
}

func TestBuildAdvisorBlockDescriptors_EnforcesAliasResourcePackageCompatibility(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	tests := []struct {
		name      string
		aliases   []advisorBlockTestAlias
		wantError bool
	}{
		{
			name: "same non-empty resource package",
			aliases: []advisorBlockTestAlias{
				{entry: "pod-a", subEntry: "container-a", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2},
				{entry: "pod-b", subEntry: "container-b", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2},
			},
		},
		{
			name: "all unpinned",
			aliases: []advisorBlockTestAlias{
				{entry: "pod-a", subEntry: "container-a", owner: "pool-a", numaID: 0, blockID: "alias", quantity: 2},
				{entry: "pod-b", subEntry: "container-b", owner: "pool-a", numaID: 0, blockID: "alias", quantity: 2},
			},
		},
		{
			name: "pinned and unpinned",
			aliases: []advisorBlockTestAlias{
				{entry: "pod-a", subEntry: "container-a", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2},
				{entry: "pod-b", subEntry: "container-b", owner: "pool-a", numaID: 0, blockID: "alias", quantity: 2},
			},
			wantError: true,
		},
		{
			name: "empty resource package reclaim owner does not conflict",
			aliases: []advisorBlockTestAlias{
				{entry: "pod-a", subEntry: "container-a", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2, overlap: true},
				{entry: "reclaim", subEntry: commonstate.FakedContainerName, owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "alias", quantity: 2, overlap: true},
			},
		},
	}

	for i, tt := range tests {
		i := i
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := advisorBlockTestResponse(tt.aliases, rand.New(rand.NewSource(int64(i))))
			_, err := buildAdvisorBlockDescriptors(
				resp,
				p.machine.machineInfo.CPUDetails,
				nil,
				map[string]machine.CPUSet{"rp-a": machine.NewCPUSet(0, 1, 2)},
				machine.NewCPUSet(),
			)
			if tt.wantError {
				require.ErrorContains(t, err, `block "alias" aliases have incompatible resource packages`)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildAdvisorBlockDescriptors_RejectsDifferentResourcePackagesBeforeCapacityCheck(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "pod-a", subEntry: "container-a", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "alias", quantity: 2},
		{entry: "pod-b", subEntry: "container-b", owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-b"), numaID: 0, blockID: "alias", quantity: 2},
	}, rand.New(rand.NewSource(5)))

	_, err := buildAdvisorBlockDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		nil,
		map[string]machine.CPUSet{
			"rp-a": machine.NewCPUSet(0, 1),
			"rp-b": machine.NewCPUSet(1, 2),
		},
		machine.NewCPUSet(),
	)
	require.ErrorContains(t, err, `block "alias" aliases have incompatible resource packages`)
}

func TestBuildAdvisorBlockDescriptors_ClassifiesAllBlockClasses(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "static", owner: commonstate.PoolNameReserve, numaID: 0, blockID: "static", quantity: 1},
		{entry: "mandatory", owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "mandatory", quantity: 1},
		{entry: "dedicated", owner: commonstate.PoolNameDedicated, numaID: 0, blockID: "dedicated", quantity: 1},
		{entry: "shared", owner: commonstate.PoolNameShare, numaID: 0, blockID: "shared", quantity: 1},
		{entry: "overlap", owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "overlap", quantity: 1, overlap: true},
	}, rand.New(rand.NewSource(6)))

	descriptors, err := buildAdvisorBlockDescriptors(
		resp, p.machine.machineInfo.CPUDetails, nil, nil, machine.NewCPUSet(),
	)
	require.NoError(t, err)
	require.Equal(t, []advisorBlockClass{
		advisorBlockClassStatic,
		advisorBlockClassMandatoryReclaim,
		advisorBlockClassDedicated,
		advisorBlockClassShared,
		advisorBlockClassReclaimOverlap,
	}, []advisorBlockClass{
		descriptors[0].Class,
		descriptors[1].Class,
		descriptors[2].Class,
		descriptors[3].Class,
		descriptors[4].Class,
	})
}

func TestBuildAdvisorBlockDescriptors_ReportsClassificationErrors(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	t.Run("empty owner", func(t *testing.T) {
		t.Parallel()

		resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
			entry: "bad", numaID: 0, blockID: "bad", quantity: 1,
		}}, rand.New(rand.NewSource(8)))
		_, err := buildAdvisorBlockDescriptors(resp, p.machine.machineInfo.CPUDetails, nil, nil, machine.NewCPUSet())
		require.ErrorContains(t, err, "cannot classify empty owner pool")
	})

	t.Run("nil overlap target", func(t *testing.T) {
		t.Parallel()

		resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
			entry: "bad", owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "bad", quantity: 1,
		}}, rand.New(rand.NewSource(9)))
		resp.Entries["bad"].Entries[""].CalculationResultsByNumas[0].Blocks[0].OverlapTargets = []*advisorapi.OverlapTarget{nil}
		_, err := buildAdvisorBlockDescriptors(resp, p.machine.machineInfo.CPUDetails, nil, nil, machine.NewCPUSet())
		require.ErrorContains(t, err, "cannot classify nil overlap target")
	})
}

func TestAdvisorBlockDescriptorLess_UsesCanonicalKeyOrder(t *testing.T) {
	t.Parallel()

	base := advisorBlockDescriptor{
		BlockID:      "block-b",
		Owners:       []string{"owner-b"},
		Class:        advisorBlockClassShared,
		NUMAID:       1,
		Quantity:     2,
		ComponentKey: "component-b",
	}
	tests := []struct {
		name  string
		left  advisorBlockDescriptor
		right advisorBlockDescriptor
	}{
		{
			name:  "NUMA precedes class",
			left:  advisorBlockDescriptor{NUMAID: 0, Class: advisorBlockClassReclaimOverlap},
			right: advisorBlockDescriptor{NUMAID: 1, Class: advisorBlockClassStatic},
		},
		{
			name:  "real NUMA zero precedes fake NUMA",
			left:  advisorBlockDescriptor{NUMAID: 0},
			right: advisorBlockDescriptor{NUMAID: commonstate.FakedNUMAID},
		},
		{
			name:  "real NUMA one precedes fake NUMA",
			left:  advisorBlockDescriptor{NUMAID: 1},
			right: advisorBlockDescriptor{NUMAID: commonstate.FakedNUMAID},
		},
		{
			name: "class precedes component key",
			left: func() advisorBlockDescriptor {
				d := base
				d.Class, d.ComponentKey = advisorBlockClassDedicated, "component-z"
				return d
			}(),
			right: func() advisorBlockDescriptor {
				d := base
				d.Class, d.ComponentKey = advisorBlockClassShared, "component-a"
				return d
			}(),
		},
		{
			name: "component key precedes quantity",
			left: func() advisorBlockDescriptor {
				d := base
				d.ComponentKey, d.Quantity = "component-a", 9
				return d
			}(),
			right: func() advisorBlockDescriptor {
				d := base
				d.ComponentKey, d.Quantity = "component-b", 1
				return d
			}(),
		},
		{
			name: "quantity precedes alias signature",
			left: func() advisorBlockDescriptor {
				d := base
				d.Quantity, d.Owners = 1, []string{"owner-z"}
				return d
			}(),
			right: func() advisorBlockDescriptor {
				d := base
				d.Quantity, d.Owners = 2, []string{"owner-a"}
				return d
			}(),
		},
		{
			name: "alias signature precedes block id",
			left: func() advisorBlockDescriptor {
				d := base
				d.Owners, d.BlockID = []string{"owner-a"}, "block-z"
				return d
			}(),
			right: func() advisorBlockDescriptor {
				d := base
				d.Owners, d.BlockID = []string{"owner-b"}, "block-a"
				return d
			}(),
		},
		{
			name: "block id is final tie break",
			left: func() advisorBlockDescriptor {
				d := base
				d.BlockID = "block-a"
				return d
			}(),
			right: base,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, advisorBlockDescriptorLess(tt.left, tt.right))
			require.False(t, advisorBlockDescriptorLess(tt.right, tt.left))
		})
	}
}

func TestAdvisorBlockDescriptorLess_SortsRealNUMAsAscendingAndFakeNUMALast(t *testing.T) {
	t.Parallel()

	const iterations = 100
	for seed := int64(0); seed < iterations; seed++ {
		descriptors := []advisorBlockDescriptor{
			{BlockID: "fake", NUMAID: commonstate.FakedNUMAID},
			{BlockID: "one", NUMAID: 1},
			{BlockID: "zero", NUMAID: 0},
		}
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(descriptors), func(i, j int) {
			descriptors[i], descriptors[j] = descriptors[j], descriptors[i]
		})
		sort.Slice(descriptors, func(i, j int) bool {
			return advisorBlockDescriptorLess(descriptors[i], descriptors[j])
		})
		require.Equal(t, []int{0, 1, commonstate.FakedNUMAID}, []int{
			descriptors[0].NUMAID,
			descriptors[1].NUMAID,
			descriptors[2].NUMAID,
		})
	}
}

func TestBuildAdvisorBlockDescriptors_EnforcesRPAndReclaimBoundaries(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "pinned", subEntry: commonstate.FakedContainerName, owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "pinned", quantity: 2},
		{entry: "unpinned", subEntry: commonstate.FakedContainerName, owner: "pool-b", numaID: 0, blockID: "unpinned", quantity: 2},
		{entry: "reclaim", subEntry: commonstate.FakedContainerName, owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "reclaim", quantity: 2},
	}, rand.New(rand.NewSource(3)))

	numa0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	numa0CPUs := numa0.ToSliceInt()
	pinned := machine.NewCPUSet(numa0CPUs[0], numa0CPUs[1])
	nonReclaimable := machine.NewCPUSet(numa0CPUs[2])
	descriptors, err := buildAdvisorBlockDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		nil,
		map[string]machine.CPUSet{"rp-a": pinned},
		nonReclaimable,
	)
	require.NoError(t, err)

	byID := make(map[string]advisorBlockDescriptor)
	for _, descriptor := range descriptors {
		byID[descriptor.BlockID] = descriptor
	}
	require.Equal(t, pinned, byID["pinned"].Eligible)
	require.True(t, byID["unpinned"].Eligible.Intersection(pinned).IsEmpty())
	require.True(t, byID["reclaim"].Eligible.Intersection(nonReclaimable).IsEmpty())
	require.True(t, pinned.IsSubsetOf(byID["reclaim"].Eligible))
}

func TestBuildAdvisorBlockDescriptors_AppliesEligibilityByOwnerPoolSemantics(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	numa0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	numa0CPUs := numa0.ToSliceInt()
	require.GreaterOrEqual(t, len(numa0CPUs), 4)
	pinned := machine.NewCPUSet(numa0CPUs[0], numa0CPUs[1])
	nonReclaimable := machine.NewCPUSet(numa0CPUs[len(numa0CPUs)-1])
	rpPinnedCPUSet := map[string]machine.CPUSet{"rp-a": pinned}

	t.Run("pinned shared and reclaim aliases intersect owner eligibility", func(t *testing.T) {
		t.Parallel()

		resp := advisorBlockTestResponse([]advisorBlockTestAlias{
			{
				entry: "pod-a", subEntry: "container-a",
				owner:  resourcepackage.WrapOwnerPoolName(commonstate.PoolNameShare, "rp-a"),
				numaID: 0, blockID: "shared-reclaim", quantity: 1, overlap: true,
			},
			{
				entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
				owner:  commonstate.PoolNameReclaim,
				numaID: 0, blockID: "shared-reclaim", quantity: 1, overlap: true,
			},
		}, rand.New(rand.NewSource(10)))

		descriptors, err := buildAdvisorBlockDescriptors(
			resp, p.machine.machineInfo.CPUDetails, nil, rpPinnedCPUSet, nonReclaimable,
		)
		require.NoError(t, err)
		require.Len(t, descriptors, 1)
		require.Equal(t, pinned, descriptors[0].Eligible)
	})

	t.Run("unpinned shared overlap excludes every pinned resource package", func(t *testing.T) {
		t.Parallel()

		resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
			entry: "pod-b", subEntry: "container-b", owner: commonstate.PoolNameShare,
			numaID: 0, blockID: "unpinned-shared", quantity: 1, overlap: true,
		}}, rand.New(rand.NewSource(11)))

		descriptors, err := buildAdvisorBlockDescriptors(
			resp, p.machine.machineInfo.CPUDetails, nil, rpPinnedCPUSet, nonReclaimable,
		)
		require.NoError(t, err)
		require.Len(t, descriptors, 1)
		require.Equal(t, numa0.Difference(pinned), descriptors[0].Eligible)
	})

	t.Run("mandatory reclaim keeps excluding only non-reclaimable CPUs", func(t *testing.T) {
		t.Parallel()

		resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner:  commonstate.PoolNameReclaim,
			numaID: 0, blockID: "mandatory-reclaim", quantity: 1,
		}}, rand.New(rand.NewSource(12)))

		descriptors, err := buildAdvisorBlockDescriptors(
			resp, p.machine.machineInfo.CPUDetails, nil, rpPinnedCPUSet, nonReclaimable,
		)
		require.NoError(t, err)
		require.Len(t, descriptors, 1)
		require.Equal(t, numa0.Difference(nonReclaimable), descriptors[0].Eligible)
		require.True(t, pinned.IsSubsetOf(descriptors[0].Eligible))
	})
}

func TestBuildAdvisorBlockDescriptors_FailsClosedWhenEligibleCapacityIsInsufficient(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{entry: "pinned", subEntry: commonstate.FakedContainerName, owner: resourcepackage.WrapOwnerPoolName("pool-a", "rp-a"), numaID: 0, blockID: "pinned", quantity: 3},
	}, rand.New(rand.NewSource(4)))

	_, err := buildAdvisorBlockDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		nil,
		map[string]machine.CPUSet{"rp-a": machine.NewCPUSet(0, 1)},
		machine.NewCPUSet(),
	)
	require.ErrorContains(t, err, "eligible capacity")
}

func TestExpandHardPartitionReclaimPhase_MixedRealAndFakeUseDisjointNUMAs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	available := numa0.Union(numa1)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID:      "real-0",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       0,
			Quantity:     2,
			ComponentKey: "real-0",
			Eligible:     numa0,
		},
		{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     4,
			ComponentKey: "fake",
			Eligible:     available,
		},
	}

	demands, _, err := expandHardPartitionReclaimPhase(descriptors, available, topology, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"real-0": {0: 2},
		"fake":   {1: 4},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_ExcludesSkipNUMAsWithoutRealMandatory(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 2, ComponentKey: "fake", Eligible: all,
	}}

	demands, _, err := expandHardPartitionReclaimPhase(
		descriptors, all, topology, sets.NewInt(0))

	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"fake": {1: 2},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_RejectsHeterogeneousParticipatingRealAndFakeDomains(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 6, NumCores: 4, NumSockets: 1, NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			5: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
		},
	}
	all := topology.CPUDetails.CPUs()
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-smt1", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 1, ComponentKey: "real-smt1", Eligible: topology.CPUDetails.CPUsInNUMANodes(0),
		},
		{
			BlockID: "fake-smt2", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 2, ComponentKey: "fake-smt2", Eligible: all,
		},
	}

	_, _, err := expandHardPartitionReclaimPhase(descriptors, all, topology, nil)
	require.ErrorContains(t, err, "non-uniform physical core width")
}

func TestExpandHardPartitionReclaimPhase_IgnoresDedicatedSaturatedHeterogeneousNUMAForFakeCoreWidth(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 6, NumCores: 4, NumSockets: 1, NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			5: {NUMANodeID: 1, SocketID: 0, CoreID: 3},
		},
	}
	all := topology.CPUDetails.CPUs()
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "dedicated-smt1", Class: advisorBlockClassDedicated, NUMAID: 0,
			Quantity: numa0.Size(), ComponentKey: "dedicated-smt1", Eligible: numa0,
		},
		{
			BlockID: "fake-smt2", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 2, ComponentKey: "fake-smt2", Eligible: all,
		},
	}

	demands, _, err := expandHardPartitionReclaimPhase(descriptors, all, topology, nil)

	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"fake-smt2": {1: 2},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_IgnoresSkippedHeterogeneousCoreWidth(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 9, NumCores: 5, NumSockets: 1, NumNUMANodes: 3,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4: {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5: {NUMANodeID: 2, SocketID: 0, CoreID: 3},
			6: {NUMANodeID: 2, SocketID: 0, CoreID: 3},
			7: {NUMANodeID: 2, SocketID: 0, CoreID: 4},
			8: {NUMANodeID: 2, SocketID: 0, CoreID: 4},
		},
	}
	all := topology.CPUDetails.CPUs()
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 4, ComponentKey: "fake", Eligible: all,
	}}
	skipNUMAs := sets.NewInt(0)

	demands, _, err := expandHardPartitionReclaimPhase(
		descriptors, all, topology, skipNUMAs)
	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"fake": {1: 2, 2: 2},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_IgnoresDedicatedOnNonParticipatingNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(12, 1, 3)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	numa2 := topology.CPUDetails.CPUsInNUMANodes(2)

	for _, tc := range []struct {
		name         string
		fakeEligible machine.CPUSet
		skipNUMAs    sets.Int
	}{
		{
			name:         "outside fake eligibility",
			fakeEligible: numa0.Union(numa1),
		},
		{
			name:         "skipped for fake reclaim",
			fakeEligible: all,
			skipNUMAs:    sets.NewInt(2),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			descriptors := []advisorBlockDescriptor{
				{
					BlockID: "dedicated-2", Class: advisorBlockClassDedicated, NUMAID: 2,
					Quantity: 2, ComponentKey: "dedicated-2", Eligible: numa2,
				},
				{
					BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
					Quantity: 4, ComponentKey: "fake", Eligible: tc.fakeEligible,
				},
			}

			demands, _, err := expandHardPartitionReclaimPhase(
				descriptors, all, topology, tc.skipNUMAs)

			require.NoError(t, err)
			require.Equal(t, map[string]map[int]int{
				"fake": {0: 2, 1: 2},
			}, hardReclaimDemandQuotasByBlock(t, demands, topology))
		})
	}
}

func TestExpandHardPartitionReclaimPhase_ZeroQuantityAllowsEmptyTarget(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 0, ComponentKey: "fake", Eligible: machine.NewCPUSet(),
	}}

	demands, blockIDs, err := expandHardPartitionReclaimPhase(
		descriptors, topology.CPUDetails.CPUs(), topology, nil)
	require.NoError(t, err)
	require.Empty(t, demands)
	require.Empty(t, blockIDs)
}

func TestExpandHardPartitionReclaimPhase_MultipleRealNUMAsDeductCapacityAndStayDeterministic(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(32, 1, 4)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	base := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 2, ComponentKey: "real-0", Eligible: topology.CPUDetails.CPUsInNUMANodes(0),
		},
		{
			BlockID: "real-2", Class: advisorBlockClassMandatoryReclaim, NUMAID: 2,
			Quantity: 2, ComponentKey: "real-2", Eligible: topology.CPUDetails.CPUsInNUMANodes(2),
		},
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 8, ComponentKey: "fake", Eligible: all,
		},
	}
	want := map[string]map[int]int{
		"real-0": {0: 2},
		"real-2": {2: 2},
		"fake":   {1: 4, 3: 4},
	}
	for seed := int64(0); seed < 20; seed++ {
		descriptors := append([]advisorBlockDescriptor(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(descriptors), func(i, j int) {
			descriptors[i], descriptors[j] = descriptors[j], descriptors[i]
		})
		demands, blockIDs, solveErr := expandHardPartitionReclaimPhase(
			descriptors, all, topology, nil)
		require.NoError(t, solveErr, "seed %d", seed)
		require.Equal(t, want, hardReclaimDemandQuotasByBlock(t, demands, topology), "seed %d", seed)
		for demandKey, blockID := range blockIDs {
			require.Contains(t, demandKey, "\x00block\x00"+blockID+"\x00", "seed %d", seed)
		}
	}
}

func TestExpandHardPartitionReclaimPhase_AllowsCapacitySaturatedImbalance(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	eligible := coresInNUMA(topology, 0, 0, 1).
		Union(coresInNUMA(topology, 1, 0, 3))
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 8, ComponentKey: "fake", Eligible: eligible, OldPreferred: eligible,
	}}

	demands, _, err := expandHardPartitionReclaimPhase(
		descriptors, topology.CPUDetails.CPUs(), topology, nil)

	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"fake": {0: 2, 1: 6},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_RejectsFakeConfinedToRealMandatoryNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 2, ComponentKey: "real-0", Eligible: numa0,
		},
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 2, ComponentKey: "fake", Eligible: numa0,
		},
	}

	_, _, err = expandHardPartitionReclaimPhase(
		descriptors, topology.CPUDetails.CPUs(), topology, nil)
	require.ErrorContains(t, err, "no effective NUMA capacity")
}

func TestExpandHardPartitionReclaimPhase_IgnoresNonCandidateCoreWidth(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 5, NumCores: 3, NumSockets: 1, NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		},
	}
	eligible := machine.NewCPUSet(0, 1, 2, 3)
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 4, ComponentKey: "fake", Eligible: eligible,
	}}

	demands, _, err := expandHardPartitionReclaimPhase(
		descriptors, topology.CPUDetails.CPUs(), topology, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"fake": {0: 4},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_RejectsNonUniformPhysicalCoreWidths(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 3, NumCores: 2, NumSockets: 1, NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		},
	}
	descriptors := []advisorBlockDescriptor{{
		BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
		Quantity: 1, ComponentKey: "fake", Eligible: topology.CPUDetails.CPUs(),
	}}

	_, _, err := expandHardPartitionReclaimPhase(
		descriptors, topology.CPUDetails.CPUs(), topology, nil)
	require.ErrorContains(t, err, "non-uniform physical core width")
}

func TestExpandHardPartitionReclaimPhase_WaterFillsCompleteCores(t *testing.T) {
	t.Parallel()

	// SMT2 topology: NUMA0 CPUs {0,1,4,5} (cores 0,1), NUMA1 CPUs {2,3,6,7} (cores 2,3).
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	cpusPerCore := topology.CPUsPerCore()
	require.Equal(t, 2, cpusPerCore)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	available := numa0.Union(numa1)
	// real-0 pins 2 cpus (1 core) on NUMA0; the fake block spreads 4 more cpus (2 cores).
	// CPU-granular water-filling would balance to {0:1,1:3}, giving NUMA0 a 3-cpu (1.5-core)
	// total and NUMA1 a 3-cpu (1.5-core) total: two orphan half-cores. Core-granular
	// water-filling must instead keep every per-NUMA total on a whole-core boundary.
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 2, ComponentKey: "real-0", Eligible: numa0,
		},
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 4, ComponentKey: "fake", Eligible: available,
		},
	}

	demands, _, err := expandHardPartitionReclaimPhase(descriptors, available, topology, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"real-0": {0: 2},
		"fake":   {1: 4},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}

func TestExpandHardPartitionReclaimPhase_MultipleFakeBlocksFailClosed(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	available := topology.CPUDetails.CPUs()
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "fake-a", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 2, ComponentKey: "a", Eligible: available,
		},
		{
			BlockID: "fake-b", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 2, ComponentKey: "b", Eligible: available,
		},
	}

	_, _, err = expandHardPartitionReclaimPhase(descriptors, available, topology, nil)
	require.ErrorContains(t, err,
		"hard reclaim protocol error: expected at most one fake-NUMA mandatory reclaim block, got 2")
}

func TestExpandHardPartitionReclaimPhase_FailsWhenAggregateCapacityIsInsufficient(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()
	available := machine.NewCPUSet(numa0[0], numa0[1], numa1[0], numa1[1], numa1[2])
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 2, ComponentKey: "real-0", Eligible: machine.NewCPUSet(numa0[0], numa0[1]),
		},
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 4, ComponentKey: "fake", Eligible: available,
		},
	}

	_, _, err = expandHardPartitionReclaimPhase(descriptors, available, topology, nil)
	require.ErrorContains(t, err, "insufficient aggregate capacity")
}

func TestExpandHardPartitionReclaimPhase_FailsWhenPositiveFakeDemandHasNoEligibleNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	descriptors := []advisorBlockDescriptor{{
		BlockID:      "empty-eligible",
		Class:        advisorBlockClassMandatoryReclaim,
		NUMAID:       commonstate.FakedNUMAID,
		Quantity:     1,
		ComponentKey: "empty-eligible",
		Eligible:     machine.NewCPUSet(),
	}}

	require.NotPanics(t, func() {
		_, _, err = expandHardPartitionReclaimPhase(descriptors, topology.CPUDetails.CPUs(), topology, nil)
	})
	require.ErrorContains(t, err, `hard reclaim fake block "empty-eligible" has quantity 1 but no effective NUMA capacity`)
}

func TestExpandHardPartitionReclaimPhase_RejectsFakeWhenEveryNUMAHasRealMandatory(t *testing.T) {
	t.Parallel()

	// SMT2, two NUMAs. Both NUMAs publish real mandatory reclaim blocks, so the
	// fake block has no NUMA domain left. A steady-exclusive skip does not make a
	// real mandatory NUMA eligible for fake reclaim again.
	topology, err := machine.GenerateDummyCPUTopology(64, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	// NUMA0 only exposes its 2-cpu steady reserve; the other 30 cpus are held by
	// the steady exclusive DNB and are not reclaim-eligible.
	numa0Reserve := machine.NewCPUSet(numa0[0], numa0[1])
	available := numa0Reserve.Union(numa1)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID:      "real-0",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       0,
			Quantity:     2,
			ComponentKey: "real-0",
			Eligible:     numa0Reserve,
		},
		{
			BlockID:      "real-1",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       1,
			Quantity:     2,
			ComponentKey: "real-1",
			Eligible:     numa1,
		},
		{
			BlockID:      "fake",
			Class:        advisorBlockClassMandatoryReclaim,
			NUMAID:       commonstate.FakedNUMAID,
			Quantity:     4,
			ComponentKey: "fake",
			Eligible:     available,
		},
	}

	_, _, err = expandHardPartitionReclaimPhase(descriptors, available, topology, nil)
	require.ErrorContains(t, err, "no effective NUMA capacity")

	_, _, err = expandHardPartitionReclaimPhase(
		descriptors, available, topology, sets.NewInt(0))
	require.ErrorContains(t, err, "no effective NUMA capacity")
}

func hardReclaimDemandQuotasByBlock(
	t *testing.T,
	demands []partitionDemand,
	topology *machine.CPUTopology,
) map[string]map[int]int {
	t.Helper()

	quotas := make(map[string]map[int]int)
	for _, demand := range demands {
		parts := strings.Split(demand.key, "\x00")
		blockID := ""
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "block" {
				blockID = parts[i+1]
				break
			}
		}
		require.NotEmpty(t, blockID, "demand %q must contain a block id", demand.key)
		if quotas[blockID] == nil {
			quotas[blockID] = make(map[int]int)
		}
		numaIDs := topology.CPUDetails.KeepOnly(demand.eligible).NUMANodes().ToSliceInt()
		require.Len(t, numaIDs, 1, "demand %q must be NUMA-scoped", demand.key)
		quotas[blockID][numaIDs[0]] += demand.quantity
	}
	return quotas
}

func TestExpandHardPartitionReclaimPhaseFakeCapacityExcludesRealReclaimNUMA(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	realEligible := coresInNUMA(topology, 0, 0, 1)
	available := numa0.Union(numa1)
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "real", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0,
			Quantity: 2, ComponentKey: "real", Eligible: realEligible,
		},
		{
			BlockID: "fake", Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID,
			Quantity: 4, ComponentKey: "fake", Eligible: realEligible.Union(numa1),
		},
	}

	demands, _, err := expandHardPartitionReclaimPhase(
		descriptors, available, topology, nil)

	require.NoError(t, err)
	require.Equal(t, map[string]map[int]int{
		"real": {0: 2},
		"fake": {1: 4},
	}, hardReclaimDemandQuotasByBlock(t, demands, topology))
}
