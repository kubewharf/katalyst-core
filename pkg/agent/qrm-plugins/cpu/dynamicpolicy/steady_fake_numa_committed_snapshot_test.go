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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func testSteadyFakeNUMACommittedDescriptors(
	topology *machine.CPUTopology,
) []advisorBlockDescriptor {
	all := topology.CPUDetails.CPUs()
	return []advisorBlockDescriptor{
		{
			BlockID: "real-0", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: 0, Quantity: 20, ComponentKey: "real-0",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			Committed:    machine.NewCPUSet(1, 129),
			OldPreferred: machine.NewCPUSet(1, 129),
		},
		{
			BlockID: "global", Class: advisorBlockClassMandatoryReclaim,
			NUMAID: commonstate.FakedNUMAID, Quantity: 80,
			ComponentKey: "global", Eligible: all,
			Committed:    machine.NewCPUSet(1, 68, 69, 129, 196, 197),
			OldPreferred: machine.NewCPUSet(68, 69, 196, 197),
		},
		{
			BlockID: "dedicated", Class: advisorBlockClassDedicated,
			NUMAID: 0, Quantity: 2, ComponentKey: "dedicated",
			Eligible:     topology.CPUDetails.CPUsInNUMANodes(0),
			Committed:    machine.NewCPUSet(2, 130),
			OldPreferred: machine.NewCPUSet(2, 130),
		},
	}
}

func cloneSteadyFakeNUMACommittedSnapshotForTest(
	snapshot steadyFakeNUMACommittedSnapshot,
) steadyFakeNUMACommittedSnapshot {
	cloned := steadyFakeNUMACommittedSnapshot{
		assignments:         make([]steadyFakeNUMACommittedAssignment, len(snapshot.assignments)),
		rawReclaimAggregate: snapshot.rawReclaimAggregate.Clone(),
		reclaim:             snapshot.reclaim.Clone(),
	}
	copy(cloned.assignments, snapshot.assignments)
	for i := range cloned.assignments {
		cloned.assignments[i].cpus = cloned.assignments[i].cpus.Clone()
		cloned.assignments[i].eligible = cloned.assignments[i].eligible.Clone()
	}
	return cloned
}

func committedAssignmentForTest(
	t *testing.T,
	snapshot *steadyFakeNUMACommittedSnapshot,
	blockID string,
) *steadyFakeNUMACommittedAssignment {
	t.Helper()
	for i := range snapshot.assignments {
		if snapshot.assignments[i].blockID == blockID {
			return &snapshot.assignments[i]
		}
	}
	t.Fatalf("committed assignment %q not found", blockID)
	return nil
}

func rebuildCommittedReclaimForTest(snapshot *steadyFakeNUMACommittedSnapshot) {
	snapshot.reclaim = machine.NewCPUSet()
	for _, assignment := range snapshot.assignments {
		if assignment.class == advisorBlockClassMandatoryReclaim {
			snapshot.reclaim = snapshot.reclaim.Union(assignment.cpus)
		}
	}
	snapshot.rawReclaimAggregate = snapshot.reclaim.Clone()
}

func newSteadyFakeNUMACommittedSnapshotForTest(
	committed, eligible machine.CPUSet,
) steadyFakeNUMACommittedSnapshot {
	return steadyFakeNUMACommittedSnapshot{
		assignments: []steadyFakeNUMACommittedAssignment{{
			blockID:  "test-reclaim-aggregate",
			class:    advisorBlockClassMandatoryReclaim,
			numaID:   commonstate.FakedNUMAID,
			cpus:     committed.Clone(),
			eligible: eligible.Clone(),
		}},
		rawReclaimAggregate: committed.Clone(),
		reclaim:             committed.Clone(),
	}
}

func TestBuildSteadyFakeNUMACommittedSnapshotPreservesExternalOwnership(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)

	snapshot, err := buildSteadyFakeNUMACommittedSnapshot(
		descriptors, topology.CPUDetails.CPUs())
	require.NoError(t, err)
	require.Equal(t,
		machine.NewCPUSet(1, 68, 69, 129, 196, 197),
		snapshot.reclaim)
	require.Equal(t,
		machine.NewCPUSet(1, 68, 69, 129, 196, 197),
		snapshot.rawReclaimAggregate)
	require.Len(t, snapshot.assignments, 3)
	require.Equal(t, []string{"dedicated", "global", "real-0"}, []string{
		snapshot.assignments[0].blockID,
		snapshot.assignments[1].blockID,
		snapshot.assignments[2].blockID,
	})

	descriptors[0].OldPreferred.Add(2)
	descriptors[0].Eligible.Add(999)
	require.Equal(t,
		machine.NewCPUSet(1, 68, 69, 129, 196, 197),
		snapshot.reclaim)
	require.Equal(t, machine.NewCPUSet(1, 129),
		committedAssignmentForTest(t, &snapshot, "real-0").cpus)
	require.False(t,
		committedAssignmentForTest(t, &snapshot, "real-0").eligible.Contains(999))
}

func TestBuildSteadyFakeNUMACommittedSnapshotMergesStableComponentOwnership(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
	alias := descriptors[2]
	alias.BlockID = "dedicated-rotated"
	descriptors = append(descriptors, alias)

	snapshot, err := buildSteadyFakeNUMACommittedSnapshot(descriptors, all)
	require.NoError(t, err)
	require.Len(t, snapshot.assignments, 3,
		"multiple solver blocks in one stable component must produce one committed owner")
	require.NoError(t, validateCommittedSteadyFakeNUMASnapshot(snapshot, nil, topology))

	descriptors[len(descriptors)-1].ComponentKey = "different-component"
	snapshot, err = buildSteadyFakeNUMACommittedSnapshot(descriptors, all)
	require.NoError(t, err)
	require.Len(t, snapshot.assignments, 4)
	require.ErrorContains(t,
		validateCommittedSteadyFakeNUMASnapshot(snapshot, nil, topology),
		"blocks \"dedicated\" and \"dedicated-rotated\" overlap")
}

func TestBuildSteadyFakeNUMACommittedSnapshotRejectsInvalidBlockIDs(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()

	tests := []struct {
		name string
		edit func([]advisorBlockDescriptor)
		want string
	}{
		{
			name: "empty block ID",
			edit: func(descriptors []advisorBlockDescriptor) {
				descriptors[2].BlockID = ""
			},
			want: "empty block ID",
		},
		{
			name: "duplicate block ID",
			edit: func(descriptors []advisorBlockDescriptor) {
				descriptors[2].BlockID = descriptors[0].BlockID
			},
			want: "duplicate committed block ID \"real-0\"",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
			tt.edit(descriptors)

			_, buildErr := buildSteadyFakeNUMACommittedSnapshot(descriptors, all)

			require.ErrorContains(t, buildErr, tt.want)
		})
	}
}

func TestValidateSteadyFakeNUMACommittedSnapshotAllowsOverlappingPreferences(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
	snapshot, err := buildSteadyFakeNUMACommittedSnapshot(descriptors, all)
	require.NoError(t, err)
	demands, blockIDByDemandKey, floors, err := expandSteadyFakeNUMAReclaimPhase(
		descriptors, all, topology, nil)
	require.NoError(t, err)

	var floor, residual partitionDemand
	require.Len(t, floors, 1)
	for _, demand := range demands {
		if blockIDByDemandKey[demand.key] != "real-0" {
			continue
		}
		if demand.key == floors[0].demandKey {
			floor = demand
		} else {
			residual = demand
		}
	}
	require.Equal(t, machine.NewCPUSet(1, 129),
		floor.preferred.Intersection(residual.preferred))
	require.NoError(t, validateCommittedSteadyFakeNUMASnapshot(
		snapshot, floors, topology))
}

func TestValidateSteadyFakeNUMACommittedSnapshotRejectsInvalidOwnership(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
	base, err := buildSteadyFakeNUMACommittedSnapshot(descriptors, all)
	require.NoError(t, err)
	_, _, floors, err := expandSteadyFakeNUMAReclaimPhase(descriptors, all, topology, nil)
	require.NoError(t, err)
	require.ErrorContains(t, validateCommittedSteadyFakeNUMASnapshot(
		base, floors, nil), "topology is nil")

	tests := []struct {
		name   string
		edit   func(*steadyFakeNUMACommittedSnapshot, *[]partitionCoreFloorConstraint)
		want   string
		floors bool
	}{
		{
			name: "exclusive block overlap",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "dedicated").cpus = machine.NewCPUSet(1, 129)
			},
			want: "blocks \"dedicated\" and \"real-0\" overlap",
		},
		{
			name: "outside topology",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "real-0").cpus = machine.NewCPUSet(999)
				rebuildCommittedReclaimForTest(snapshot)
			},
			want: "outside machine topology",
		},
		{
			name: "outside effective eligibility",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "real-0").cpus = machine.NewCPUSet(68, 196)
				rebuildCommittedReclaimForTest(snapshot)
			},
			want: "outside eligibility",
		},
		{
			name: "reclaim union mismatch",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				snapshot.reclaim = snapshot.reclaim.Union(machine.NewCPUSet(0))
			},
			want: "mandatory reclaim union",
		},
		{
			name: "raw aggregate mismatch",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				snapshot.rawReclaimAggregate = snapshot.rawReclaimAggregate.Difference(machine.NewCPUSet(1))
			},
			want: "raw committed reclaim aggregate",
		},
		{
			name: "fragmented reclaim",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "real-0").cpus = machine.NewCPUSet(1)
				rebuildCommittedReclaimForTest(snapshot)
			},
			want: "not core-aligned",
		},
		{
			name: "empty block ID",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "dedicated").blockID = ""
			},
			want: "empty block ID",
		},
		{
			name: "duplicate block ID",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "dedicated").blockID = "real-0"
			},
			want: "duplicate committed block ID \"real-0\"",
		},
		{
			name: "missing floor block",
			edit: func(_ *steadyFakeNUMACommittedSnapshot, floors *[]partitionCoreFloorConstraint) {
				(*floors)[0].committedBlockID = "missing"
			},
			want:   "floor block \"missing\" is missing",
			floors: true,
		},
		{
			name: "floor points to dedicated block",
			edit: func(_ *steadyFakeNUMACommittedSnapshot, floors *[]partitionCoreFloorConstraint) {
				(*floors)[0].committedBlockID = "dedicated"
			},
			want:   "floor block \"dedicated\"",
			floors: true,
		},
		{
			name: "floor points to shared block",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, floors *[]partitionCoreFloorConstraint) {
				snapshot.assignments = append(snapshot.assignments,
					steadyFakeNUMACommittedAssignment{
						blockID: "shared", class: advisorBlockClassShared,
						numaID: 0, cpus: machine.NewCPUSet(), eligible: all,
					})
				(*floors)[0].committedBlockID = "shared"
			},
			want:   "floor block \"shared\"",
			floors: true,
		},
		{
			name: "floor points to fake mandatory block",
			edit: func(_ *steadyFakeNUMACommittedSnapshot, floors *[]partitionCoreFloorConstraint) {
				(*floors)[0].committedBlockID = "global"
			},
			want:   "floor block \"global\"",
			floors: true,
		},
		{
			name: "floor block has no complete core",
			edit: func(snapshot *steadyFakeNUMACommittedSnapshot, _ *[]partitionCoreFloorConstraint) {
				committedAssignmentForTest(t, snapshot, "real-0").cpus = machine.NewCPUSet(1, 2)
				committedAssignmentForTest(t, snapshot, "global").cpus = machine.NewCPUSet(68, 69, 129, 130, 196, 197)
				rebuildCommittedReclaimForTest(snapshot)
			},
			want:   "block \"real-0\" violates its NUMA/core floor",
			floors: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snapshot := cloneSteadyFakeNUMACommittedSnapshotForTest(base)
			testFloors := append([]partitionCoreFloorConstraint(nil), floors...)
			tt.edit(&snapshot, &testFloors)
			if !tt.floors {
				testFloors = nil
			}

			err := validateCommittedSteadyFakeNUMASnapshot(
				snapshot, testFloors, topology)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateSteadyFakeNUMACommittedSnapshotAllowsQuantityChanges(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	all := topology.CPUDetails.CPUs()
	descriptors := testSteadyFakeNUMACommittedDescriptors(topology)
	baseline, err := buildSteadyFakeNUMACommittedSnapshot(descriptors, all)
	require.NoError(t, err)

	for _, quantity := range []int{2, 120} {
		changed := append([]advisorBlockDescriptor(nil), descriptors...)
		changed[1].Quantity = quantity
		snapshot, buildErr := buildSteadyFakeNUMACommittedSnapshot(changed, all)
		require.NoError(t, buildErr)
		require.Equal(t, baseline, snapshot,
			"quantity %d must not change any committed CPU set", quantity)
		require.NoError(t, validateCommittedSteadyFakeNUMASnapshot(
			snapshot, nil, topology),
			"committed CPU ownership must not be equated with latest quantity %d", quantity)
	}
}
