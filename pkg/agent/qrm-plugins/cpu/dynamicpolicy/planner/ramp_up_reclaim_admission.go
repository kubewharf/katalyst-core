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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// PlanRampUpReclaimPoolTarget returns a candidate snapshot whose reclaim pool
// entry represents the hard ramp-up reclaim target.
func PlanRampUpReclaimPoolTarget(snapshot *CPUStateSnapshot, hardReclaim machine.CPUSet, topology *machine.CPUTopology) (CPUStateSnapshot, error) {
	if hardReclaim.IsEmpty() {
		return CPUStateSnapshot{}, fmt.Errorf("hard ramp-up reclaim target must not be empty")
	}
	if topology == nil {
		return CPUStateSnapshot{}, fmt.Errorf("cpu topology is nil")
	}
	if snapshot == nil {
		snapshot = &CPUStateSnapshot{}
	}

	reclaimInfo := currentReclaimPoolEntry(snapshot.PodEntries).Clone()
	if reclaimInfo == nil {
		reclaimInfo = &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		}
	}

	// Ramp-up hard reclaim is represented once by the reclaim pool entry. Main
	// containers keep workload allocations in AllocationResult; the reclaim pool
	// entry is the committed target that bulkhead and advisor materialization read.
	reclaimInfo.AllocationResult = hardReclaim.Clone()
	reclaimInfo.OriginalAllocationResult = hardReclaim.Clone()
	reclaimInfo.TopologyAwareAssignments = make(map[int]machine.CPUSet)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		assigned := hardReclaim.Intersection(numaCPUs)
		if !assigned.IsEmpty() {
			reclaimInfo.TopologyAwareAssignments[numaID] = assigned
		}
	}
	reclaimInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(reclaimInfo.TopologyAwareAssignments)

	candidate := NewCPUStateCandidate(snapshot)
	candidate.UpdatePodEntry(commonstate.PoolNameReclaim, commonstate.FakedContainerName, reclaimInfo)
	return candidate.Materialize(), nil
}

func currentReclaimPoolEntry(entries state.PodEntries) *state.AllocationInfo {
	if entries == nil || entries[commonstate.PoolNameReclaim] == nil {
		return nil
	}
	return entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
}
