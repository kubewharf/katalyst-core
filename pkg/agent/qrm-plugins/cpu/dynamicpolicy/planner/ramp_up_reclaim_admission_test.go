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

package planner

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestPlanRampUpReclaimPoolTargetUpdatesReclaimPoolThroughCandidate(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	cleanNUMA := &state.NUMANodeState{
		DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
	}
	existingReclaim := &state.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(0),
	}
	snapshot := &CPUStateSnapshot{
		PodEntries: state.PodEntries{
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: existingReclaim,
			},
		},
		MachineState: state.NUMANodeMap{
			0: cleanNUMA,
		},
	}

	planned, err := PlanRampUpReclaimPoolTarget(snapshot, machine.NewCPUSet(0, 4), topology)
	require.NoError(t, err)

	reclaimInfo := planned.PodEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.True(t, reclaimInfo.AllocationResult.Equals(machine.NewCPUSet(0, 4)))
	require.True(t, reclaimInfo.OriginalAllocationResult.Equals(machine.NewCPUSet(0, 4)))
	require.True(t, reclaimInfo.TopologyAwareAssignments[0].Equals(machine.NewCPUSet(0, 4)))
	require.Equal(t, reclaimInfo.TopologyAwareAssignments, reclaimInfo.OriginalTopologyAwareAssignments)

	require.True(t, existingReclaim.AllocationResult.Equals(machine.NewCPUSet(0)))
	require.Same(t, cleanNUMA, planned.MachineState[0])
}

func TestPlanRampUpReclaimPoolTargetRejectsEmptyHardReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	_, err = PlanRampUpReclaimPoolTarget(nil, machine.NewCPUSet(), topology)
	require.ErrorContains(t, err, "hard ramp-up reclaim target must not be empty")
}

func TestPlanRampUpReclaimPoolTargetRejectsNilTopology(t *testing.T) {
	t.Parallel()

	_, err := PlanRampUpReclaimPoolTarget(nil, machine.NewCPUSet(0), nil)
	require.ErrorContains(t, err, "cpu topology is nil")
}

func TestPlanRampUpReclaimPoolTargetCopiesOriginalAssignments(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	planned, err := PlanRampUpReclaimPoolTarget(&CPUStateSnapshot{}, machine.NewCPUSet(0, 4), topology)
	require.NoError(t, err)

	reclaimInfo := planned.PodEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.NotNil(t, reclaimInfo)
	for _, cpuID := range []int{0, 4} {
		numaID := topology.CPUDetails[cpuID].NUMANodeID
		require.True(t, reclaimInfo.TopologyAwareAssignments[numaID].Contains(cpuID))
	}
	require.Equal(t, reclaimInfo.TopologyAwareAssignments, reclaimInfo.OriginalTopologyAwareAssignments)

	for numaID, cpus := range reclaimInfo.TopologyAwareAssignments {
		cpus.Add(1)
		reclaimInfo.TopologyAwareAssignments[numaID] = cpus
		break
	}
	require.NotEqual(t, reclaimInfo.TopologyAwareAssignments, reclaimInfo.OriginalTopologyAwareAssignments)
}
