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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDeriveAdvisorIsolationSourcePool(t *testing.T) {
	t.Parallel()

	entries := state.PodEntries{
		"pod1": state.ContainerEntries{
			"c": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "c",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					Annotations:   map[string]string{},
				},
			},
		},
	}
	block := &advisorapi.BlockInfo{
		Block: advisorapi.Block{BlockId: "block-isolation", Result: 2},
		OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
			commonstate.PoolNamePrefixIsolation + "-pod1": {
				EntryName:    "pod1",
				SubEntryName: "c",
			},
		},
	}

	source, ok := deriveAdvisorIsolationSourcePool(block, entries)
	require.True(t, ok)
	require.Equal(t, commonstate.PoolNameShare, source)
}

func TestDynamicPolicy_tryCarveAdvisorBlockFromSource(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_tryCarveAdvisorBlockFromSource")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"c": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "c",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					Annotations:   map[string]string{},
				},
			},
		},
	}, false)

	blockCPUSet := advisorapi.BlockCPUSet{
		"block-share": machine.NewCPUSet(0, 1, 2, 3),
	}
	sourceBlockByPool := map[string]string{
		commonstate.PoolNameShare: "block-share",
	}
	availableCPUs := machine.NewCPUSet(4, 5)
	nodeRemainingCPUs := machine.NewCPUSet(4, 5, 6, 7)
	block := &advisorapi.BlockInfo{
		Block: advisorapi.Block{BlockId: "block-isolation", Result: 3},
		OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
			commonstate.PoolNamePrefixIsolation + "-pod1": {
				EntryName:    "pod1",
				SubEntryName: "c",
			},
		},
	}

	carved, err := p.tryCarveAdvisorBlockFromSource(
		block, sourceBlockByPool, blockCPUSet, &availableCPUs, &nodeRemainingCPUs, commonstate.FakedNUMAID, 3)
	require.NoError(t, err)
	require.True(t, carved)

	require.Equal(t, 3, blockCPUSet["block-isolation"].Size())
	require.True(t, blockCPUSet["block-isolation"].IsSubsetOf(machine.NewCPUSet(0, 1, 2, 3)),
		"isolation block should be carved from the source share block first, got %s",
		blockCPUSet["block-isolation"].String())
	require.Equal(t, 1, blockCPUSet["block-share"].Size(),
		"source share block should be shrunk after carve, got %s",
		blockCPUSet["block-share"].String())
	require.True(t, availableCPUs.Equals(machine.NewCPUSet(4, 5)),
		"available cpus should not be consumed when source is sufficient, got %s",
		availableCPUs.String())
}

func TestDynamicPolicy_allocateShareBlocks_carvesIsolationFromAllocatedSource(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestDynamicPolicy_allocateShareBlocks_carvesIsolationFromAllocatedSource")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)
	p.state.SetPodEntries(state.PodEntries{
		"pod1": state.ContainerEntries{
			"c": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod1",
					ContainerName: "c",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-pod1",
					Annotations:   map[string]string{},
				},
			},
		},
	}, false)

	blockCPUSet := advisorapi.BlockCPUSet{
		"block-share": machine.NewCPUSet(0, 1, 2, 3),
	}
	availableCPUs := machine.NewCPUSet(4, 5)
	nodeRemainingCPUs := machine.NewCPUSet(4, 5, 6, 7)
	sourceBlockByPool := map[string]string{
		commonstate.PoolNameShare: "block-share",
	}
	blocks := []*advisorapi.BlockInfo{
		{
			Block: advisorapi.Block{BlockId: "block-isolation", Result: 2},
			OwnerPoolEntryMap: map[string]advisorapi.BlockEntry{
				commonstate.PoolNamePrefixIsolation + "-pod1": {
					EntryName:    "pod1",
					SubEntryName: "c",
				},
			},
		},
	}

	err = p.allocateShareBlocks(
		commonstate.FakedNUMAID,
		blocks,
		blockCPUSet,
		machine.NewCPUSet(),
		&nodeRemainingCPUs,
		&availableCPUs,
		nil,
		machine.NewCPUSet(),
		nil,
		sourceBlockByPool,
	)
	require.NoError(t, err)
	require.True(t, blockCPUSet["block-isolation"].IsSubsetOf(machine.NewCPUSet(0, 1, 2, 3)),
		"isolation block should be carved from source share block, got %s",
		blockCPUSet["block-isolation"].String())
	require.Equal(t, 2, blockCPUSet["block-share"].Size())
	require.True(t, availableCPUs.Equals(machine.NewCPUSet(4, 5)))
}
