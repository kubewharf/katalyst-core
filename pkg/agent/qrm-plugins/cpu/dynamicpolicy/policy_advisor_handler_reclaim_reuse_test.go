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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
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
	return p, func() { _ = os.RemoveAll(tmpDir) }
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

	node0 := p.machineInfo.CPUDetails.CPUsInNUMANodes(0)
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

	node0 := p.machineInfo.CPUDetails.CPUsInNUMANodes(0)
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

	node0 := p.machineInfo.CPUDetails.CPUsInNUMANodes(0)
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

	node0 := p.machineInfo.CPUDetails.CPUsInNUMANodes(0)
	node1 := p.machineInfo.CPUDetails.CPUsInNUMANodes(1)
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

	all := p.machineInfo.CPUDetails.CPUs()
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
