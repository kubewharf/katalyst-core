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
	"context"
	"fmt"
	"math/rand"
	"sort"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type verticalContractHierarchy struct {
	entries  map[string]topology.EntryState
	children map[string][]topology.ChildRef
	writes   int
}

func (h *verticalContractHierarchy) Close() error { return nil }

func (h *verticalContractHierarchy) Roots(context.Context) ([]topology.RootRef, error) {
	rels := make([]string, 0, len(h.entries))
	for rel := range h.entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	roots := make([]topology.RootRef, 0, len(rels))
	for _, rel := range rels {
		if len(h.children[rel]) == 0 && rel != "primary" && rel != "reclaim" {
			continue
		}
		roots = append(roots, topology.RootRef{Rel: rel, Identity: h.entries[rel].Identity})
	}
	return roots, nil
}

func (h *verticalContractHierarchy) StatIdentity(
	_ context.Context,
	rel string,
) (topology.CgroupIdentity, error) {
	entry, ok := h.entries[rel]
	if !ok {
		return topology.CgroupIdentity{}, syscall.ENOENT
	}
	return entry.Identity, nil
}

func (h *verticalContractHierarchy) ReadEntry(
	_ context.Context,
	rel string,
) (topology.EntryState, error) {
	entry, ok := h.entries[rel]
	if !ok {
		return topology.EntryState{}, syscall.ENOENT
	}
	entry.CPUs = entry.CPUs.Clone()
	entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
	return entry, nil
}

func (h *verticalContractHierarchy) ListChildren(
	_ context.Context,
	rel string,
) ([]topology.ChildRef, error) {
	return append([]topology.ChildRef(nil), h.children[rel]...), nil
}

func (h *verticalContractHierarchy) WriteCPUs(
	_ context.Context,
	rel string,
	expected topology.CgroupIdentity,
	cpus machine.CPUSet,
) error {
	entry, ok := h.entries[rel]
	if !ok {
		return syscall.ENOENT
	}
	if entry.Identity != expected {
		return topology.ErrCgroupIdentityChanged
	}
	entry.CPUs = cpus.Clone()
	entry.ConfiguredCPUs = cpus.Clone()
	h.entries[rel] = entry
	h.writes++
	return nil
}

func (h *verticalContractHierarchy) WriteMems(
	_ context.Context,
	rel string,
	expected topology.CgroupIdentity,
	mems string,
) error {
	entry, ok := h.entries[rel]
	if !ok {
		return syscall.ENOENT
	}
	if entry.Identity != expected {
		return topology.ErrCgroupIdentityChanged
	}
	entry.Mems = mems
	entry.ConfiguredMems = mems
	h.entries[rel] = entry
	h.writes++
	return nil
}

func (h *verticalContractHierarchy) Classify(
	err error,
	_ topology.HierarchyOperation,
) topology.HierarchyErrorClass {
	if err == syscall.ENOENT || err == topology.ErrCgroupIdentityChanged {
		return topology.HierarchyErrorStale
	}
	return topology.HierarchyErrorInvalid
}

func (h *verticalContractHierarchy) Capabilities() topology.HierarchyCapabilities {
	return topology.HierarchyCapabilities{
		StableIdentity:          true,
		KernelParentContainment: true,
	}
}

type verticalContractCgroup struct {
	cgroupclient.FakeCgroupClient
	driver *verticalContractHierarchy
}

func (c *verticalContractCgroup) Version(context.Context) cgroupclient.CgroupVersion {
	return cgroupclient.CgroupVersionV1
}

func (c *verticalContractCgroup) SnapshotDriver() topology.HierarchyDriver {
	return c.driver
}

func TestFakeNUMASteadyBalanceAndHardFloorVerticalContract(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, cpuTopology.CPUsPerCore())

	numaIDs := []int{0, 1}
	capacityByNUMA := map[int]int{0: 48, 1: 48}
	oldQuotaByNUMA := map[int]int{0: 14, 1: 42}
	finalQuota, err := planWholeCoreCapacityQuotas(
		56, cpuTopology.CPUsPerCore(), numaIDs, capacityByNUMA,
		map[int]int{0: 0, 1: 0}, oldQuotaByNUMA, true)
	require.NoError(t, err)
	require.Equal(t, map[int]int{0: 28, 1: 28}, finalQuota)

	all := cpuTopology.CPUDetails.CPUs()
	current := coresInNUMA(cpuTopology, 0, 0, 7).
		Union(coresInNUMA(cpuTopology, 1, 0, 21))
	frozenTarget := coresInNUMA(cpuTopology, 0, 0, 14).
		Union(coresInNUMA(cpuTopology, 1, 0, 14))
	desired := map[string]machine.CPUSet{
		"fake":  frozenTarget,
		"share": all.Difference(frozenTarget),
	}
	demands := stagedMigrationDemands(all, current, frozenTarget.Size())
	stage, err := projectSteadyFakeNUMAStage(
		demands,
		[]string{"fake"},
		newSteadyFakeNUMACommittedSnapshotForTest(current, all),
		desired,
		nil,
		cpuTopology,
	)
	require.NoError(t, err)
	stageFake := stage["fake"]
	require.Equal(t, frozenTarget.Size(), stageFake.Size())
	require.LessOrEqual(t,
		steadyFakeNUMAMigrationChurn(current, stageFake),
		steadyFakeNUMAMaxMigratedCPUs)
	require.Less(t,
		steadyFakeNUMAMigrationChurn(stageFake, frozenTarget),
		steadyFakeNUMAMigrationChurn(current, frozenTarget))

	observedFloor := coresInNUMA(cpuTopology, 0, 0, 7)
	required := coresInNUMA(cpuTopology, 0, 0, 9)
	sourceBefore := coresInNUMA(cpuTopology, 0, 7, 10)
	sourceAfter := coresInNUMA(cpuTopology, 0, 9, 10)
	require.Equal(t, 14, observedFloor.Size())
	require.Equal(t, 18, required.Size())
	requiredByRel := topology.RequiredCPUSetByRelFromNodeSpecs([]topology.NodeSpec{{
		Rel: "reclaim", Role: topology.TopoNodeRoleReclaimNUMABucket,
		CPUs: required,
	}})
	require.Equal(t, required, requiredByRel["reclaim"])

	dag, err := topology.BuildDAG([]topology.NodeSpec{
		{
			Rel: "primary", Role: topology.TopoNodeRolePrimary, Domain: topology.DomainPrimary,
			CPUs: sourceAfter, Mems: "0", ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim", Role: topology.TopoNodeRoleReclaim, Domain: topology.DomainReclaim,
			CPUs: required, Mems: "0", ControlledRoot: true, TrustAnchor: true,
		},
	})
	require.NoError(t, err)
	driver := &verticalContractHierarchy{
		entries: map[string]topology.EntryState{
			"primary": {
				Rel: "primary", Identity: topology.CgroupIdentity{Device: 1, Inode: 1},
				CPUs: sourceBefore, ConfiguredCPUs: sourceBefore,
				Mems: "0", ConfiguredMems: "0",
			},
			"reclaim": {
				Rel: "reclaim", Identity: topology.CgroupIdentity{Device: 1, Inode: 2},
				CPUs: observedFloor, ConfiguredCPUs: observedFloor,
				Mems: "0", ConfiguredMems: "0",
			},
		},
		children: map[string][]topology.ChildRef{},
	}
	result, err := (topology.TopologyCoordinator{}).Converge(
		context.Background(),
		topology.CoordinatorInput{
			DAG: dag,
			Cgroup: &verticalContractCgroup{
				driver: driver,
			},
			Mode:                topology.NormalModeGuard(),
			CPUDetails:          cpuTopology.CPUDetails,
			RequiredCPUSetByRel: requiredByRel,
			Objective:           topology.ConvergenceObjectiveParentSafe,
			AdmissionBudget: &topology.AdmissionConvergenceBudget{
				MaxRequiredWrites: 4,
			},
		},
	)
	require.NoError(t, err)
	require.True(t, result.FinalSnapshotCurrent)
	require.True(t, result.Converged || result.ParentSafe)
	require.NotNil(t, result.FinalSnapshot)
	require.GreaterOrEqual(t, driver.writes, 2)

	appliedReclaim, ok := result.FinalSnapshot.TargetProofCPUs("reclaim", required)
	require.True(t, ok)
	require.Equal(t, required, appliedReclaim)
	require.True(t, required.Difference(appliedReclaim).IsEmpty())
	require.True(t,
		result.FinalSnapshot.DomainUnion[topology.DomainPrimary].
			Intersection(result.FinalSnapshot.DomainUnion[topology.DomainReclaim]).IsEmpty())
}

func TestValidateHardPartitionBlockPlanMixedRealAndFakeReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "real-0", quantity: 6,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "fake", quantity: 2,
		},
	}, rand.New(rand.NewSource(0)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	blockCPUSet, err := policy.generateBlockCPUSet(resp, featureGates, true)
	require.NoError(t, err)
	require.Equal(t, 6, blockCPUSet["real-0"].Size())
	require.Equal(t, 2, blockCPUSet["fake"].Size())
	require.True(t, blockCPUSet["real-0"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(0)))
	require.True(t, blockCPUSet["fake"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(1)))

	require.NoError(t, policy.validateHardPartitionBlockPlan(resp, blockCPUSet, true))
}

func TestValidateHardPartitionBlockPlanWithoutPositiveFakeStillValidatesCanonicalFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	realAliases := []advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "real-0", quantity: 6,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 1, blockID: "real-1", quantity: 2,
		},
	}
	realBlockCPUSet := advisorapi.BlockCPUSet{
		"real-0": coresInNUMA(topology, 0, 0, 3),
		"real-1": coresInNUMA(topology, 1, 0, 1),
	}

	noFakeResp := advisorBlockTestResponse(realAliases, rand.New(rand.NewSource(6)))
	noFakeResp.DisableDedicatedCoresOverlapReclaimedCores = true
	require.ErrorContains(t,
		policy.validateHardPartitionBlockPlan(noFakeResp, realBlockCPUSet, true),
		"imbalanced across physical NUMAs")

	zeroFakeAliases := append(append([]advisorBlockTestAlias(nil), realAliases...),
		advisorBlockTestAlias{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "fake-zero", quantity: 0,
		})
	zeroFakeResp := advisorBlockTestResponse(zeroFakeAliases, rand.New(rand.NewSource(6)))
	zeroFakeResp.DisableDedicatedCoresOverlapReclaimedCores = true

	blockCPUSet := advisorapi.BlockCPUSet{
		"real-0":    realBlockCPUSet["real-0"],
		"real-1":    realBlockCPUSet["real-1"],
		"fake-zero": machine.NewCPUSet(),
	}
	require.ErrorContains(t,
		policy.validateHardPartitionBlockPlan(zeroFakeResp, blockCPUSet, true),
		"imbalanced across physical NUMAs")

	blockCPUSet["fake-zero"] = coresInNUMA(topology, 0, 0, 1)
	require.ErrorContains(t,
		policy.validateHardPartitionBlockPlan(zeroFakeResp, blockCPUSet, true),
		`zero-quantity fake reclaim block "fake-zero" is not empty`)
}

func TestHardPartitionFakeReclaimBalancesAgainstEffectiveNUMACapacity(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, topology.CPUsPerCore())
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: "pod-dedicated", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 0, blockID: "dedicated-0", quantity: 6,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
			blockID: "fake", quantity: 8,
		},
	}, rand.New(rand.NewSource(5)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	planned, err := policy.generateBlockCPUSet(resp, featureGates, true)
	require.NoError(t, err)
	require.Equal(t, 6, planned["dedicated-0"].Size())
	require.Equal(t, 2, planned["fake"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(0)).Size())
	require.Equal(t, 6, planned["fake"].Intersection(
		topology.CPUDetails.CPUsInNUMANodes(1)).Size())
	require.NoError(t, policy.validateHardPartitionBlockPlan(resp, planned, true))
}

func TestHardPartitionFakeReclaimSkipsNUMAWithoutCompleteCoreAfterDedicated(t *testing.T) {
	t.Parallel()

	for _, dedicatedQuantity := range []uint64{7, 8} {
		dedicatedQuantity := dedicatedQuantity
		t.Run(fmt.Sprintf("dedicated-%d-of-8", dedicatedQuantity), func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
			require.NoError(t, err)
			require.Equal(t, 2, topology.CPUsPerCore())
			policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
			require.NoError(t, err)

			resp := advisorBlockTestResponse([]advisorBlockTestAlias{
				{
					entry: "pod-dedicated", subEntry: "main", owner: commonstate.PoolNameDedicated,
					numaID: 0, blockID: "dedicated-0", quantity: dedicatedQuantity,
				},
				{
					entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
					owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
					blockID: "fake", quantity: 4,
				},
			}, rand.New(rand.NewSource(int64(dedicatedQuantity))))
			resp.DisableDedicatedCoresOverlapReclaimedCores = true
			featureGates := map[string]*advisorsvc.FeatureGate{
				feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
					Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
				},
			}

			planned, err := policy.generateBlockCPUSet(resp, featureGates, true)
			require.NoError(t, err)
			require.Equal(t, int(dedicatedQuantity), planned["dedicated-0"].Size())
			require.True(t, planned["fake"].IsSubsetOf(
				topology.CPUDetails.CPUsInNUMANodes(1)))
			require.NoError(t, policy.validateHardPartitionBlockPlan(resp, planned, true))
		})
	}
}

func TestValidateHardPartitionBlockPlanIgnoresNonParticipatingHeterogeneousNUMA(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 13, NumCores: 7, NumSockets: 1, NumNUMANodes: 3,
		CPUDetails: machine.CPUDetails{
			0:  {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			2:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			3:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			6:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			7:  {NUMANodeID: 2, SocketID: 0, CoreID: 4},
			8:  {NUMANodeID: 2, SocketID: 0, CoreID: 4},
			9:  {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			10: {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			11: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
			12: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
		},
	}
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.state.SetPodEntries(state.PodEntries{
		"steady-exclusive": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "steady-exclusive",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult:         machine.NewCPUSet(0),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0)},
			},
		},
	}, false)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{{
		entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
		owner: commonstate.PoolNameReclaim, numaID: commonstate.FakedNUMAID,
		blockID: "fake", quantity: 6,
	}}, rand.New(rand.NewSource(3)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true

	require.NoError(t, policy.validateHardPartitionBlockPlan(resp, advisorapi.BlockCPUSet{
		"fake": machine.NewCPUSet(1, 2, 7, 8, 9, 10),
	}, true))
}

func TestDisjointPlannerUsesParticipatingMandatoryCoreWidth(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 15, NumCores: 8, NumSockets: 1, NumNUMANodes: 3,
		CPUDetails: machine.CPUDetails{
			0:  {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			2:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			3:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			6:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			7:  {NUMANodeID: 1, SocketID: 0, CoreID: 4},
			8:  {NUMANodeID: 1, SocketID: 0, CoreID: 4},
			9:  {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			10: {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			11: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
			12: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
			13: {NUMANodeID: 2, SocketID: 0, CoreID: 7},
			14: {NUMANodeID: 2, SocketID: 0, CoreID: 7},
		},
	}
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: "pod-dedicated", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 1, blockID: "dedicated", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 1, blockID: "real-reclaim", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 2, blockID: "real-reclaim-2", quantity: 2,
		},
	}, rand.New(rand.NewSource(4)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	planned, err := policy.generateBlockCPUSet(resp, featureGates, true)

	require.NoError(t, err)
	require.Equal(t, 2, planned["dedicated"].Size())
	require.Equal(t, 4, planned["real-reclaim"].Size())
	require.Equal(t, 2, planned["real-reclaim-2"].Size())
	require.True(t, planned["dedicated"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(1)))
	require.True(t, planned["real-reclaim"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(1)))
	require.True(t, planned["real-reclaim-2"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(2)))
	require.True(t, planned["dedicated"].Intersection(planned["real-reclaim"]).IsEmpty())
	requireCoreAligned(t, topology, planned["real-reclaim"].Union(planned["real-reclaim-2"]))
	require.Equal(t, 1, topology.CPUsPerCore())
}

func TestDisjointPlannerExcludesSteadyNUMAFromHardCoreWidth(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs: 15, NumCores: 8, NumSockets: 1, NumNUMANodes: 3,
		CPUDetails: machine.CPUDetails{
			0:  {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			2:  {NUMANodeID: 1, SocketID: 0, CoreID: 1},
			3:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			4:  {NUMANodeID: 1, SocketID: 0, CoreID: 2},
			5:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			6:  {NUMANodeID: 1, SocketID: 0, CoreID: 3},
			7:  {NUMANodeID: 1, SocketID: 0, CoreID: 4},
			8:  {NUMANodeID: 1, SocketID: 0, CoreID: 4},
			9:  {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			10: {NUMANodeID: 2, SocketID: 0, CoreID: 5},
			11: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
			12: {NUMANodeID: 2, SocketID: 0, CoreID: 6},
			13: {NUMANodeID: 2, SocketID: 0, CoreID: 7},
			14: {NUMANodeID: 2, SocketID: 0, CoreID: 7},
		},
	}
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.state.SetPodEntries(state.PodEntries{
		"steady-exclusive": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "steady-exclusive",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult:         machine.NewCPUSet(0),
				TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0)},
			},
		},
	}, false)
	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 0, blockID: "steady-real", quantity: 1,
		},
		{
			entry: "pod-dedicated", subEntry: "main", owner: commonstate.PoolNameDedicated,
			numaID: 1, blockID: "dedicated", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 1, blockID: "real-reclaim", quantity: 3,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner: commonstate.PoolNameReclaim, numaID: 2, blockID: "real-reclaim-2", quantity: 2,
		},
	}, rand.New(rand.NewSource(7)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	planned, err := policy.generateBlockCPUSet(resp, featureGates, true)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(0), planned["steady-real"])
	require.Equal(t, 2, planned["dedicated"].Size())
	require.Equal(t, 4, planned["real-reclaim"].Size())
	require.Equal(t, 2, planned["real-reclaim-2"].Size())
}
