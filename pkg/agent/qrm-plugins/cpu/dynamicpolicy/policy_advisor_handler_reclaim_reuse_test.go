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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// setReclaimPoolCPUSet installs a reclaim pool entry so that
// generateReclaimBlockCPUSet observes it as the "previous reclaim" cpuset to
// prefer. An empty cpuset means no prior reclaim pool (first allocation).
func setReclaimPoolCPUSet(t *testing.T, p *DynamicPolicy, cpus machine.CPUSet) {
	t.Helper()
	if cpus.IsEmpty() {
		p.state.SetPodEntries(state.PodEntries{}, false)
		return
	}
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: cpus.Clone(),
			},
		},
	}, false)
}

func numaAwareReclaimBlock(numaID int, blockID string, result int) map[int][]*advisorapi.BlockInfo {
	return map[int][]*advisorapi.BlockInfo{
		numaID: {
			{Block: advisorapi.Block{BlockId: blockID, Result: uint64(result)}},
		},
	}
}

// newReclaimReuseTestPolicy builds a DynamicPolicy on a 96-cpu / 2-numa host
// (node0=0-23,48-71 node1=24-47,72-95) without pool initialization so the test
// fully controls the "previous reclaim" state.
func newReclaimReuseTestPolicy(t *testing.T) (*DynamicPolicy, func()) {
	t.Helper()
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-reclaim-reuse")
	require.NoError(t, err)

	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		require.NoError(t, err)
	}
	p.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	// Production commits always carry a non-empty reclaim pool (the canonical
	// precommit/apply/ensure paths all enforce it). Seed that invariant here so
	// WAL-lifecycle reconciles exercise the success path; tests that control the
	// "previous reclaim" cpuset override it via setReclaimPoolCPUSet.
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))
	return p, func() { _ = os.RemoveAll(tmpDir) }
}

func TestSyncReclaimPoolWithAdjustmentCommitOverride(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(0, 1),
				OriginalAllocationResult:         machine.NewCPUSet(0, 1),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
			},
		},
	}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective: machine.NewCPUSet(2, 3),
		Source:           "cpuset_topology",
	}

	err := p.syncReclaimPoolWithAdjustmentCommitOverride(newEntries, override)
	require.NoError(t, err)

	got := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 3)))
	require.True(t, got.OriginalAllocationResult.Equals(machine.NewCPUSet(2, 3)))
	require.NotEmpty(t, got.TopologyAwareAssignments)
	require.NotEmpty(t, got.OriginalTopologyAwareAssignments)
}

func TestSyncReclaimPoolWithAdjustmentCommitOverrideAlsoSyncsDefaultShare(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	allCPUs := p.machine.machineInfo.CPUDetails.CPUs()
	eligibleCPUs := allCPUs.Difference(p.config.reservedCPUs)
	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         eligibleCPUs.Difference(machine.NewCPUSet(0, 1)),
				OriginalAllocationResult: eligibleCPUs.Difference(machine.NewCPUSet(0, 1)),
			},
		},
	}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective: machine.NewCPUSet(2, 3),
		Source:           "cpuset_topology",
	}

	require.NoError(t, p.syncReclaimPoolWithAdjustmentCommitOverride(newEntries, override))

	share := newEntries[commonstate.PoolNameShare][commonstate.FakedContainerName]
	require.NotNil(t, share)
	require.True(t, share.AllocationResult.Equals(eligibleCPUs.Difference(machine.NewCPUSet(2, 3))),
		"default share must track the residual after reclaim override, got %s", share.AllocationResult)
	require.True(t, share.OriginalAllocationResult.Equals(share.AllocationResult))
	require.NotEmpty(t, share.TopologyAwareAssignments)
}

func TestReclaimCommitOverrideSyncsOwnerAllocationsBeforePrecommit(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	eligible := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs)
	initialReclaim := machine.NewCPUSet(0, 1, 24, 25)
	finalReclaim := machine.NewCPUSet(2, 3, 26, 27)
	initialShare := eligible.Difference(initialReclaim)
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         initialReclaim.Clone(),
				OriginalAllocationResult: initialReclaim.Clone(),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         initialShare.Clone(),
				OriginalAllocationResult: initialShare.Clone(),
			},
		},
		"reclaimed": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "reclaimed",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult:         machine.NewCPUSet(24, 25),
				OriginalAllocationResult: machine.NewCPUSet(24, 25),
			},
		},
		"rnb": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"shared": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult:         initialShare.Clone(),
				OriginalAllocationResult: initialShare.Clone(),
			},
		},
	}
	entries["rnb"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{0})

	require.NoError(t, p.syncReclaimPoolWithAdjustmentCommitOverride(
		entries,
		&cpusetutil.CPUSetAdjustmentCommitOverride{
			ReclaimEffective: finalReclaim,
			Source:           "cpuset_topology",
		},
		eligible,
	))

	committed, _, err := p.commitPendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          entries,
		persist:          false,
		source:           "reclaim override test",
	})
	require.NoError(t, err)

	assertAllocation := func(allocation *state.AllocationInfo, expected machine.CPUSet) {
		t.Helper()
		require.NotNil(t, allocation)
		require.True(t, allocation.AllocationResult.Equals(expected))
		require.True(t, allocation.OriginalAllocationResult.Equals(expected))
		expectedAssignments, assignmentErr := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, expected)
		require.NoError(t, assignmentErr)
		require.True(t, cpuAssignmentsEqual(allocation.TopologyAwareAssignments, expectedAssignments))
		require.True(t, cpuAssignmentsEqual(allocation.OriginalTopologyAwareAssignments, expectedAssignments))
	}

	finalShare := eligible.Difference(finalReclaim)
	assertAllocation(committed[commonstate.PoolNameReclaim].GetPoolEntry(), finalReclaim)
	assertAllocation(committed[commonstate.PoolNameShare].GetPoolEntry(), finalShare)
	assertAllocation(committed["rnb"]["main"], machine.NewCPUSet(2, 3))
	assertAllocation(committed["reclaimed"]["main"], machine.NewCPUSet(26, 27))
	assertAllocation(committed["shared"]["main"], finalShare)
	require.Equal(t, commonstate.PoolNameReclaim, committed["rnb"]["main"].GetOwnerPoolName())
	require.Equal(t, commonstate.PoolNameReclaim, committed["reclaimed"]["main"].GetOwnerPoolName())
	require.Equal(t, commonstate.PoolNameShare, committed["shared"]["main"].GetOwnerPoolName())
}

func TestSyncReclaimPoolWithAdjustmentCommitOverrideNoopWhenEmpty(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}

	err := p.syncReclaimPoolWithAdjustmentCommitOverride(newEntries, &cpusetutil.CPUSetAdjustmentCommitOverride{})
	require.NoError(t, err)

	got := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(0, 1)))
	require.True(t, got.OriginalAllocationResult.Equals(machine.NewCPUSet(0, 1)))
}

func TestBuildAdjustmentCommitOverrideFromPodEntriesRemovesReclaimNonReclaimOverlap(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
				OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}

	override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, false)
	require.NoError(t, err)
	require.Equal(t, "advisor_pending_entries", override.Source)
	require.True(t, override.ReclaimEffective.Equals(machine.NewCPUSet(2, 3)))
}

func TestBuildAdjustmentCommitOverrideFromPodEntriesSkipsWhenResponseAllowsOverlap(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
				OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}

	// The advisor response mode is the source of truth for the pending
	// transaction. A stale checkpoint value must not clip reclaim in overlap
	// mode before CommitAdvisorState persists the new mode.
	override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, true, false)
	require.NoError(t, err)
	require.Nil(t, override)
}

func TestBuildAdjustmentCommitOverrideFromPodEntriesProtectsDedicatedWhenSharedOverlapAllowed(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
				OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:         machine.NewCPUSet(0, 1),
				OriginalAllocationResult: machine.NewCPUSet(0, 1),
			},
		},
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      "dedicated_cores",
				},
				AllocationResult: machine.NewCPUSet(2),
			},
		},
	}

	override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, true, true)
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Empty(t, override.ReclaimEffective.ToSliceInt(),
		"dedicated disjoint mode must drop every partial physical core from reclaim, got %s",
		override.ReclaimEffective)
}

func TestBuildAdjustmentCommitOverrideFromPodEntriesHandlesDirectContainerOverlap(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
				OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      "dedicated_cores",
				},
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}

	// A pending non-overlap transaction may contain stale reclaim raw CPUs that
	// overlap newly allocated non-reclaim containers. The commit override must
	// use the same pending partition view to make reclaim consistent before
	// pre-commit validation runs.
	override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, false)
	require.NoError(t, err)
	require.Equal(t, "advisor_pending_entries", override.Source)
	require.True(t, override.ReclaimEffective.Equals(machine.NewCPUSet(2, 3)))
}

func TestBuildAdjustmentCommitOverrideFromPodEntriesPreservesWholeCores(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	t.Run("drops the partial sibling pair", func(t *testing.T) {
		t.Parallel()
		newEntries := state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult:         machine.NewCPUSet(0, 1, 48, 49),
					OriginalAllocationResult: machine.NewCPUSet(0, 1, 48, 49),
				},
			},
			"pod-dedicated": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "pod-dedicated",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameDedicated,
						QoSLevel:      "dedicated_cores",
					},
					AllocationResult: machine.NewCPUSet(0),
				},
			},
		}

		override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, true)
		require.NoError(t, err)
		require.NotNil(t, override)
		require.True(t, override.ReclaimEffective.Equals(machine.NewCPUSet(1, 49)),
			"partial core must be removed from reclaim, got %s", override.ReclaimEffective)
	})

	t.Run("commits an empty reclaim override", func(t *testing.T) {
		t.Parallel()
		newEntries := state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult:         machine.NewCPUSet(0, 48),
					OriginalAllocationResult: machine.NewCPUSet(0, 48),
				},
			},
			"pod-dedicated": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "pod-dedicated",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameDedicated,
						QoSLevel:      "dedicated_cores",
					},
					AllocationResult: machine.NewCPUSet(0),
				},
			},
		}

		override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, true)
		require.NoError(t, err)
		require.NotNil(t, override)
		require.Empty(t, override.ReclaimEffective.ToSliceInt())
		require.NoError(t, p.syncReclaimPoolWithAdjustmentCommitOverride(newEntries, override))
		require.Empty(t, reclaimPoolCPUSet(newEntries).ToSliceInt())
	})
}

// TestGenerateReclaimBlockCPUSet_InPlaceReuse verifies that when the previous
// reclaim cpuset is still available and large enough, the recompute keeps
// reclaim inside those prior cores instead of re-selecting from scratch.
func TestGenerateReclaimBlockCPUSet_InPlaceReuse(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	prev := machine.NewCPUSet(0, 1, 2, 3)
	setReclaimPoolCPUSet(t, p, prev)

	node0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.generateReclaimBlockCPUSet(
		numaAwareReclaimBlock(0, "b0", 2),
		node0, node0, machine.NewCPUSet(), blockCPUSet)
	require.NoError(t, err)

	got := blockCPUSet["b0"]
	require.Equal(t, 2, got.Size())
	require.True(t, got.IsSubsetOf(prev), "reclaim must stay within prior cores, got=%s prev=%s", got, prev)
}

// TestGenerateReclaimBlockCPUSet_ReleaseRefill verifies that a grown reclaim
// requirement first reuses all still-available prior cores and only spills to
// fresh cores for the remainder.
func TestGenerateReclaimBlockCPUSet_ReleaseRefill(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	prev := machine.NewCPUSet(0, 1)
	setReclaimPoolCPUSet(t, p, prev)

	node0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.generateReclaimBlockCPUSet(
		numaAwareReclaimBlock(0, "b0", 4),
		node0, node0, machine.NewCPUSet(), blockCPUSet)
	require.NoError(t, err)

	got := blockCPUSet["b0"]
	require.Equal(t, 4, got.Size())
	require.True(t, prev.IsSubsetOf(got), "grown reclaim must keep all prior cores, got=%s prev=%s", got, prev)
	require.True(t, got.IsSubsetOf(node0), "spill must stay on the same NUMA node")
}

// TestGenerateReclaimBlockCPUSet_FirstAllocation verifies that with no prior
// reclaim pool the legacy topology-aware take shape is preserved.
func TestGenerateReclaimBlockCPUSet_FirstAllocation(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	// no reclaim pool entry => prevReclaim empty => legacy TakeByTopology path.
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet())

	node0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.generateReclaimBlockCPUSet(
		numaAwareReclaimBlock(0, "b0", 4),
		node0, node0, machine.NewCPUSet(), blockCPUSet)
	require.NoError(t, err)

	got := blockCPUSet["b0"]
	require.Equal(t, 4, got.Size())
	require.True(t, got.IsSubsetOf(node0), "first allocation must stay on the requested NUMA node")
}

// TestGenerateReclaimBlockCPUSet_CrossNUMAIsolation verifies that a NUMA-aware
// reclaim block only reuses the prior reclaim cores that live on its own NUMA
// node and never pulls prior cores from a sibling NUMA node.
func TestGenerateReclaimBlockCPUSet_CrossNUMAIsolation(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	// prior reclaim spans both NUMA nodes: 0,1 on node0 and 24,25 on node1.
	prev := machine.NewCPUSet(0, 1, 24, 25)
	setReclaimPoolCPUSet(t, p, prev)

	node0 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	node1 := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1)
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.generateReclaimBlockCPUSet(
		numaAwareReclaimBlock(0, "b0", 2),
		node0, node0, machine.NewCPUSet(), blockCPUSet)
	require.NoError(t, err)

	got := blockCPUSet["b0"]
	require.Equal(t, machine.NewCPUSet(0, 1), got, "node0 reclaim must reuse exactly its own prior cores")
	require.True(t, got.Intersection(node1).IsEmpty(), "node0 reclaim must not pull node1 prior cores")
}

// TestGenerateReclaimBlockCPUSet_NonNUMAReuse verifies that non-NUMA-aware
// reclaim blocks also prefer the previous reclaim cpuset before spilling.
func TestGenerateReclaimBlockCPUSet_NonNUMAReuse(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	prev := machine.NewCPUSet(10, 11)
	setReclaimPoolCPUSet(t, p, prev)

	all := p.machine.machineInfo.CPUDetails.CPUs()
	blocks := map[int][]*advisorapi.BlockInfo{
		commonstate.FakedNUMAID: {
			{Block: advisorapi.Block{BlockId: "b0", Result: uint64(2)}},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{}
	err := p.generateReclaimBlockCPUSet(blocks, all, all, machine.NewCPUSet(), blockCPUSet)
	require.NoError(t, err)

	got := blockCPUSet["b0"]
	require.Equal(t, 2, got.Size())
	require.True(t, got.IsSubsetOf(prev), "non-NUMA reclaim must reuse prior cores, got=%s prev=%s", got, prev)
}

// TestBuildAdjustmentCommitOverrideCarriesPendingSharedProjection proves the
// synchronous admission/commit override path computes and carries the transient
// per-container pending-shared-projection side-channel (previously discarded), so
// pending non-SNB shared RampUp CPUs are projected onto the non-reclaim domain
// rather than folding exactly at the RPC boundary.
func TestBuildAdjustmentCommitOverrideCarriesPendingSharedProjection(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)

	reclaim := machine.NewCPUSet(4, 20, 36, 52, 68, 84, 8, 24)
	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				OriginalAllocationResult: reclaim.Clone(),
			},
		},
		"pod-shared": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-shared",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:                   true,
				AllocationResult:         machine.NewCPUSet(0),
				OriginalAllocationResult: machine.NewCPUSet(0),
			},
		},
	}

	override, err := p.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, false)
	require.NoError(t, err)
	require.NotNil(t, override)
	// The side-channel must classify the pending shared ramp-up container.
	require.Contains(t, override.PendingSharedProjection, "pod-shared")
	require.Contains(t, override.PendingSharedProjection["pod-shared"], "main")
	// Reclaim is preserved (not emptied by exact fold).
	require.True(t, override.ReclaimEffective.Equals(reclaim),
		"reclaim = %s, want %s", override.ReclaimEffective, reclaim)
	// The hint must be staged for the synchronous adjustment round to consume.
	require.Equal(t,
		override.PendingSharedProjection,
		p.adjustment.stagedPendingSharedProjection,
		"admission projection must be staged for the adjustment loop")
}

// TestPendingSharedProjectionStagingUnionsConcurrentBatch proves two admissions
// in one debounced batch both survive (no last-writer-wins), and the manager
// union keeps the live superset.
func TestPendingSharedProjectionStagingUnionsConcurrentBatch(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	reclaim := machine.NewCPUSet(4, 20, 36, 52, 68, 84, 8, 24)
	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:         reclaim.Clone(),
				OriginalAllocationResult: reclaim.Clone(),
			},
		},
	}
	addShared := func(pod string) {
		newEntries[pod] = map[string]*state.AllocationInfo{
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        pod,
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:                   true,
				AllocationResult:         machine.NewCPUSet(0),
				OriginalAllocationResult: machine.NewCPUSet(0),
			},
		}
	}
	addShared("pod-a")
	addShared("pod-b")

	ov, err := policy.buildAdjustmentCommitOverrideFromPodEntries(newEntries, false, false)
	require.NoError(t, err)
	require.Contains(t, ov.PendingSharedProjection, "pod-a")
	require.Contains(t, ov.PendingSharedProjection, "pod-b")

	// Union staging: both pods survive, not last-writer-wins.
	staged := policy.adjustment.stagedPendingSharedProjection
	require.Contains(t, staged, "pod-a")
	require.Contains(t, staged, "pod-b")
	require.Contains(t, staged["pod-a"], "main")
	require.Contains(t, staged["pod-b"], "main")

	// Manager union keeps live superset plus override-only key.
	live := map[string]map[string]struct{}{
		"pod-live": {"main": {}},
	}
	merged := bulkheadutils.UnionPendingSharedProjection(live, staged)
	require.Contains(t, merged, "pod-a")
	require.Contains(t, merged, "pod-b")
	require.Contains(t, merged, "pod-live")
}

func TestReviseReclaimPoolFallbackReservedMissReusesCommittedOrSkipsEmpty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		committedNUMA1   machine.CPUSet
		expectNUMA1Empty bool
	}{
		{
			name:             "no committed bucket skips materializing empty",
			committedNUMA1:   machine.NewCPUSet(),
			expectNUMA1Empty: true,
		},
		{
			name:             "committed bucket reused on reserved miss",
			committedNUMA1:   machine.NewCPUSet(24, 72),
			expectNUMA1Empty: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()

			// node0=0-23,48-71 (NUMA 0); node1=24-47,72-95 (NUMA 1).
			// Reserved preference has NO entry for NUMA 1 (map miss).
			p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 48),
			}

			// Install the already-committed reclaim pool in plugin state.
			committedReclaim := machine.NewCPUSet(0, 48).Union(tc.committedNUMA1)
			committedAssignments := map[int]machine.CPUSet{0: machine.NewCPUSet(0, 48)}
			if !tc.committedNUMA1.IsEmpty() {
				committedAssignments[1] = tc.committedNUMA1.Clone()
			}
			p.state.SetPodEntries(state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         committedReclaim.Clone(),
						TopologyAwareAssignments: committedAssignments,
					},
				},
			}, false)

			// Advisor omits NUMA 1 entirely; reclaim only planned on NUMA 0.
			newEntries := state.PodEntries{
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:                 machine.NewCPUSet(0, 48),
						OriginalAllocationResult:         machine.NewCPUSet(0, 48),
						TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(0, 48)},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 48)},
					},
				},
			}

			// NUMA 0 is non-RNB; NUMA 1 is the RNB-target NUMA the loop backfills.
			err := p.reviseReclaimPool(
				newEntries,
				machine.NewCPUSet(0),
				machine.NewCPUSet(),
				reclaimFallbackMode{allowSharedOverlap: false, disableDedicatedOverlap: false},
			)
			require.NoError(t, err)

			reclaimPool := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
			bucket1, bucket1Present := reclaimPool.TopologyAwareAssignments[1]
			if tc.expectNUMA1Empty {
				require.False(t, bucket1Present,
					"no committed/reserved bucket must not materialize an initialized-empty NUMA-1 bucket")
				require.True(t, reclaimPool.AllocationResult.Intersection(tc.committedNUMA1).IsEmpty())
			} else {
				// The reserved preference missed for NUMA 1, so the fallback must
				// source its size and shape from the committed bucket: a non-empty,
				// core-aligned bucket of the same size, scoped to NUMA 1.
				require.True(t, bucket1Present, "committed NUMA-1 bucket must be reused")
				require.False(t, bucket1.IsEmpty())
				require.Equal(t, tc.committedNUMA1.Size(), bucket1.Size(),
					"reused bucket %s must preserve committed size", bucket1.String())
				requireCoreAligned(t, p.machine.machineInfo.CPUTopology, bucket1)
				require.True(t, bucket1.IsSubsetOf(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1)))
				require.True(t, bucket1.IsSubsetOf(reclaimPool.AllocationResult))
			}
		})
	}
}

func TestReviseReclaimPoolFailsClosedWhenActiveRNBHasNoFallback(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	// node0=0-23,48-71 (NUMA 0); node1=24-47,72-95 (NUMA 1).
	// Reserved preference has no NUMA 1 entry, and there is no committed NUMA 1
	// bucket. nonReclaimCPUSet claims every NUMA-1 CPU, so nothing is available
	// there for reclaim.
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0, 48),
	}
	p.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 48),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 48),
				},
			},
		},
	}, false)

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(0, 48),
				OriginalAllocationResult:         machine.NewCPUSet(0, 48),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(0, 48)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 48)},
			},
		},
		"rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: machine.NewCPUSet(24, 72),
			},
		},
	}
	newEntries["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{1})

	// Every NUMA-1 CPU is non-reclaim, so the RNB target NUMA has no usable
	// fallback. reviseReclaimPool must fail closed instead of silently leaving
	// the active RNB pod unprovisioned.
	err := p.reviseReclaimPool(
		newEntries,
		machine.NewCPUSet(0), // NUMA 0 is the non-reclaim-binding NUMA (skipped).
		p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(1), // all NUMA-1 CPUs unavailable.
		reclaimFallbackMode{allowSharedOverlap: false, disableDedicatedOverlap: false},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "active reclaimed-numa-binding on NUMA 1")
}

// TestReviseReclaimPoolFourNUMAActiveRNBOnOverlap exercises a 4-NUMA host where
// the reclaim pool is sparse across NUMA 0-2, an active (non-ramp-up) RNB pod is
// pinned to NUMA 3 which the advisor omitted, and a shared RampUp pod is present.
// With overlap=true the reserved preference must backfill NUMA 3 without error.
func TestReviseReclaimPoolFourNUMAActiveRNBOnOverlap(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 1, 4)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-reclaim-4numa")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	// Reserved reclaim preference wants a core-aligned pair on every NUMA.
	reserved := map[int]machine.CPUSet{}
	reclaimResult := machine.NewCPUSet()
	for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
		perNuma := takeCoreAlignedCPUSet(cpuTopology,
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID),
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID), 2)
		require.Equal(t, 2, perNuma.Size(), "numa %d should offer a core pair", numaID)
		reserved[numaID] = perNuma
		if numaID != 3 {
			reclaimResult = reclaimResult.Union(perNuma)
		}
	}
	p.reclaim.reservedReclaimedTopologyAwareAssignments = reserved

	// Advisor omits NUMA 3: reclaim is sparse across NUMA 0-2 only.
	reclaimBuckets := map[int]machine.CPUSet{}
	for numaID := 0; numaID <= 2; numaID++ {
		reclaimBuckets[numaID] = reserved[numaID].Clone()
	}
	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaimResult.Clone(),
				OriginalAllocationResult:         reclaimResult.Clone(),
				TopologyAwareAssignments:         reclaimBuckets,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(reclaimBuckets),
			},
		},
		// Active RNB pinned to the omitted NUMA 3.
		"rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: reserved[3].Clone(),
			},
		},
		// Shared RampUp pod: must not be treated as an active RNB target.
		"rampup-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rampup-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(),
				RampUp:           true,
			},
		},
	}
	newEntries["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{3})

	err = p.reviseReclaimPool(newEntries, machine.NewCPUSet(), machine.NewCPUSet(),
		reclaimFallbackMode{allowSharedOverlap: true, disableDedicatedOverlap: false})
	require.NoError(t, err)

	reclaimPool := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	numa3, ok := reclaimPool.TopologyAwareAssignments[3]
	require.True(t, ok, "NUMA 3 must be backfilled for the active RNB")
	require.True(t, numa3.Equals(reserved[3]))
	require.True(t, reserved[3].IsSubsetOf(reclaimPool.AllocationResult))
}

// TestReviseReclaimPoolOverlapModeFlip adapts the existing response-mode flip
// harness: toggling allowSharedCoresOverlapReclaimedCores must switch the NUMA-3
// backfill between reusing the reserved preference (overlap=true) and failing to
// core-align (overlap=false) without mutating committed state.
func TestReviseReclaimPoolOverlapModeFlip(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 1, 4)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-reclaim-modeflip")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	p, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	reserved := map[int]machine.CPUSet{}
	reclaimResult := machine.NewCPUSet()
	for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
		perNuma := takeCoreAlignedCPUSet(cpuTopology,
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID),
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID), 2)
		reserved[numaID] = perNuma
		if numaID != 3 {
			reclaimResult = reclaimResult.Union(perNuma)
		}
	}
	p.reclaim.reservedReclaimedTopologyAwareAssignments = reserved

	newEntries := func() state.PodEntries {
		buckets := map[int]machine.CPUSet{}
		for numaID := 0; numaID <= 2; numaID++ {
			buckets[numaID] = reserved[numaID].Clone()
		}
		entry := state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: &state.AllocationInfo{
					AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
					AllocationResult:                 reclaimResult.Clone(),
					TopologyAwareAssignments:         buckets,
					OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(buckets),
				},
			},
			"rnb-pod": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "rnb-pod",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameReclaim,
						QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
						Annotations: map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
					},
					AllocationResult: reserved[3].Clone(),
				},
			},
		}
		entry["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{3})
		return entry
	}

	// Overlap on: reserved NUMA-3 preference is reused directly.
	on := newEntries()
	require.NoError(t, p.reviseReclaimPool(on, machine.NewCPUSet(), machine.NewCPUSet(),
		reclaimFallbackMode{allowSharedOverlap: true, disableDedicatedOverlap: false}))
	require.True(t, on[commonstate.PoolNameReclaim][commonstate.FakedContainerName].
		TopologyAwareAssignments[3].Equals(reserved[3]))

	// Overlap off: the same active RNB is backfilled from the available set as a
	// core-aligned bucket (mode flip changes selection, not the contract).
	off := newEntries()
	require.NoError(t, p.reviseReclaimPool(off, machine.NewCPUSet(), machine.NewCPUSet(),
		reclaimFallbackMode{allowSharedOverlap: false, disableDedicatedOverlap: false}))
	numa3 := off[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments[3]
	require.False(t, numa3.IsEmpty())
	require.Equal(t, reserved[3].Size(), numa3.Size())
	requireCoreAligned(t, cpuTopology, numa3)
}

// TestResyncPoolOwnedAllocationsRematerializesMembers proves that after pool
// normalization, resyncPoolOwnedAllocations rebuilds pool-owned member
// allocations (an RNB reclaimed member, an ordinary reclaimed member, and a
// shared member) from the re-normalized pool entry before ownership validation,
// updating current and original topology consistently without densifying the
// pool bucket map.
func TestResyncPoolOwnedAllocationsRematerializesMembers(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-resync-members")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	reclaimBuckets := map[int]machine.CPUSet{
		0: machine.NewCPUSet(5, 10),
		1: machine.NewCPUSet(24),
	}
	shareBuckets := map[int]machine.CPUSet{
		0: machine.NewCPUSet(50),
	}
	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(0), // stale scalar
				OriginalAllocationResult:         machine.NewCPUSet(0),
				TopologyAwareAssignments:         reclaimBuckets,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(reclaimBuckets),
			},
		},
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult:                 machine.NewCPUSet(0),
				TopologyAwareAssignments:         shareBuckets,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(shareBuckets),
			},
		},
		"rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "main",
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: machine.NewCPUSet(0), // stale
			},
			"sidecar": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "sidecar",
					ContainerType: pluginapi.ContainerType_SIDECAR.String(),
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: machine.NewCPUSet(0), // stale with the main
			},
		},
		"ordinary-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ordinary-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult: machine.NewCPUSet(0), // stale
			},
		},
		"shared-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(0), // stale
			},
		},
	}
	entries["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{1})
	entries["rnb-pod"]["sidecar"].SetSpecifiedNUMABindingNUMAID([]uint64{1})

	require.NoError(t, p.resyncPoolOwnedAllocations(entries))

	reclaimPool := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.True(t, reclaimPool.AllocationResult.Equals(machine.NewCPUSet(5, 10, 24)),
		"pool scalar rebuilt from sparse buckets: %s", reclaimPool.AllocationResult.String())
	// Pool map stays sparse: no densified empty buckets.
	require.NotContains(t, reclaimPool.TopologyAwareAssignments, 2)

	require.True(t, entries["rnb-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(24)),
		"RNB member rematerialized to its NUMA-1 bucket")
	require.True(t, entries["rnb-pod"]["sidecar"].AllocationResult.Equals(machine.NewCPUSet(24)),
		"RNB sidecar follows its rematerialized main allocation")
	require.True(t, entries["rnb-pod"]["sidecar"].OriginalAllocationResult.Equals(
		entries["rnb-pod"]["main"].OriginalAllocationResult))
	require.True(t, state.CheckAllocationInfoTopologyAwareAssignments(
		entries["rnb-pod"]["sidecar"], entries["rnb-pod"]["main"]))
	require.True(t, state.CheckAllocationInfoOriginTopologyAwareAssignments(
		entries["rnb-pod"]["sidecar"], entries["rnb-pod"]["main"]))
	require.NoError(t, validatePendingPoolOwnership(entries))
	require.True(t, entries["ordinary-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(5, 10)),
		"ordinary reclaimed member rematerialized to non-RNB NUMA-0 buckets")
	require.True(t, entries["shared-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(50)),
		"shared member inherits share pool result")
}

// TestReviseReclaimPoolOverlapFallbackDropsForbiddenAndOffNUMa proves that even
// in overlap mode, the committed/reserved reclaim fallback is intersected with
// the target NUMA topology and CPUs owned by a forbidden pool are dropped, while
// allowed on-NUMA CPUs survive.
func TestReviseReclaimPoolOverlapFallbackDropsForbiddenAndOffNUMa(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-overlap-filter")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	// NUMA0 preference contains: an on-NUMA-0 usable CPU (10), a forbidden-pool
	// CPU (5), and an off-NUMA CPU (24 lives on NUMA1).
	p.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(5, 10, 24),
	}

	entries := state.PodEntries{
		// Forbidden (interrupt) pool owns CPU 5 on NUMA 0.
		commonstate.PoolNameInterrupt: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameInterrupt),
				AllocationResult: machine.NewCPUSet(5),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(24),
				OriginalAllocationResult:         machine.NewCPUSet(24),
				TopologyAwareAssignments:         map[int]machine.CPUSet{1: machine.NewCPUSet(24)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{1: machine.NewCPUSet(24)},
			},
		},
	}

	require.NoError(t, p.reviseReclaimPool(entries, machine.NewCPUSet(), machine.NewCPUSet(),
		reclaimFallbackMode{allowSharedOverlap: true, disableDedicatedOverlap: false}))
	bucket := entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].
		TopologyAwareAssignments[0]
	require.False(t, bucket.IsEmpty())
	require.False(t, bucket.Contains(5), "forbidden-pool CPU 5 must not be reintroduced: %s", bucket.String())
	require.False(t, bucket.Contains(24), "off-NUMA CPU 24 must not be reintroduced: %s", bucket.String())
	require.True(t, bucket.Contains(10), "allowed on-NUMA CPU 10 must survive: %s", bucket.String())
}

// TestResyncPoolOwnedAllocationsPropagatesMissingOwnerBucket proves that an
// active RNB member bound to a NUMA the reclaim pool does not own surfaces as an
// error (with member context) instead of being silently tolerated.
func TestResyncPoolOwnedAllocationsPropagatesMissingOwnerBucket(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	tmpDir, err := ioutil.TempDir("", "checkpoint-resync-err")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	p, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	entries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 machine.NewCPUSet(5, 10),
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: machine.NewCPUSet(5, 10)},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(5, 10)},
			},
		},
		"rnb-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: machine.NewCPUSet(24),
			},
		},
	}
	entries["rnb-pod"]["main"].SetSpecifiedNUMABindingNUMAID([]uint64{1})

	err = p.resyncPoolOwnedAllocations(entries)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rematerialize reclaimed member rnb-pod/main")
}
