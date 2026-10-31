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

package topology

import (
	"errors"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildPhaseTransitionKeepsV1DisjointReplacementNonEmpty(t *testing.T) {
	t.Parallel()
	current := machine.NewCPUSet(1)
	final := machine.NewCPUSet(0)

	drain, err := buildPhaseTransition(PhaseDrain, RelTransition{
		Current: current, Final: final, SafeDrainTarget: machine.NewCPUSet(),
	})
	if err != nil {
		t.Fatalf("drain transition: %v", err)
	}
	if !drain.Equals(current) {
		t.Fatalf("v1 disjoint drain = %s, want hold %s", drain.String(), current.String())
	}

	expand, err := buildPhaseTransition(PhaseExpand, RelTransition{
		Current: current, Final: final, AuthorizedEntering: machine.NewCPUSet(0),
	})
	if err != nil {
		t.Fatalf("expand transition: %v", err)
	}
	if want := machine.NewCPUSet(0, 1); !expand.Equals(want) {
		t.Fatalf("v1 disjoint expand = %s, want bridge %s", expand.String(), want.String())
	}
}

func TestBuildPhaseTransitionDoesNotInventArbitrarySingleCPUHold(t *testing.T) {
	t.Parallel()
	current := machine.NewCPUSet(0, 1)
	target, err := buildPhaseTransition(PhaseDrain, RelTransition{
		Current: current, Final: machine.NewCPUSet(2), SafeDrainTarget: machine.NewCPUSet(),
	})
	if err != nil {
		t.Fatalf("drain transition: %v", err)
	}
	if !target.Equals(current) {
		t.Fatalf("v1 empty-blocked drain = %s, want conservative hold %s", target.String(), current.String())
	}
}

func TestBuildPhaseTransitionHoldsCurrentWhenV1FinalTargetIsEmpty(t *testing.T) {
	t.Parallel()
	current := machine.NewCPUSet(1, 49)
	target, err := buildPhaseTransition(PhaseDrain, RelTransition{
		Current: current, Final: machine.NewCPUSet(), SafeDrainTarget: machine.NewCPUSet(),
	})
	if err != nil {
		t.Fatalf("drain transition: %v", err)
	}
	if !target.Equals(current) {
		t.Fatalf("v1 empty-final drain = %s, want current hold %s", target.String(), current.String())
	}
}

func TestBuildPhaseTransitionPreservesPartialOverlapAndPureDirections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind PhaseKind
		in   RelTransition
		want machine.CPUSet
	}{
		{
			name: "partial overlap drains to overlap",
			kind: PhaseDrain,
			in: RelTransition{
				Current: machine.NewCPUSet(0, 1), Final: machine.NewCPUSet(1, 2),
				SafeDrainTarget: machine.NewCPUSet(1),
			},
			want: machine.NewCPUSet(1),
		},
		{
			name: "pure shrink",
			kind: PhaseDrain,
			in: RelTransition{
				Current: machine.NewCPUSet(0, 1), Final: machine.NewCPUSet(0),
				SafeDrainTarget: machine.NewCPUSet(0),
			},
			want: machine.NewCPUSet(0),
		},
		{
			name: "pure authorized grow",
			kind: PhaseExpand,
			in: RelTransition{
				Current: machine.NewCPUSet(0), Final: machine.NewCPUSet(0, 1),
				AuthorizedEntering: machine.NewCPUSet(1),
			},
			want: machine.NewCPUSet(0, 1),
		},
		{
			name: "unauthorized grow is excluded",
			kind: PhaseExpand,
			in: RelTransition{
				Current: machine.NewCPUSet(0), Final: machine.NewCPUSet(0, 1),
			},
			want: machine.NewCPUSet(0),
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildPhaseTransition(tc.kind, tc.in)
			if err != nil {
				t.Fatalf("buildPhaseTransition: %v", err)
			}
			if !got.Equals(tc.want) {
				t.Fatalf("target = %s, want %s", got.String(), tc.want.String())
			}
		})
	}
}

func TestValidateFinalTargetsRequiresNUMABucketsToPartitionReclaimFinal(t *testing.T) {
	t.Parallel()
	bucket0 := machine.NewCPUSet(0, 1)
	bucket1 := machine.NewCPUSet(2, 3)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: bucket0.Union(bucket1)},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: bucket0,
			Constraint: TopologyConstraint{CPUUpperBound: bucket0, Scope: TopologyScopeNUMANode},
		},
		{
			Rel: "reclaim/bucket-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: bucket1,
			Constraint: TopologyConstraint{CPUUpperBound: bucket1, Scope: TopologyScopeNUMANode},
		},
	})
	in := PhasePlanInput{
		DAG: dag,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim":          bucket0.Union(bucket1),
			"reclaim/bucket-0": bucket0,
			"reclaim/bucket-1": machine.NewCPUSet(2),
		},
	}
	if err := validateFinalTargets(in); !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("partition error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}
	in.DesiredByRel["reclaim/bucket-1"] = bucket1
	if err := validateFinalTargets(in); err != nil {
		t.Fatalf("valid partition rejected: %v", err)
	}
}

func TestValidateFinalTargetsRejectsOverlappingBucketsEvenWhenUnionIsComplete(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1, 2)},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1),
		},
		{
			Rel: "reclaim/bucket-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(1, 2),
		},
	})
	err := validateFinalTargets(PhasePlanInput{
		DAG: dag,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim":          machine.NewCPUSet(0, 1, 2),
			"reclaim/bucket-0": machine.NewCPUSet(0, 1),
			"reclaim/bucket-1": machine.NewCPUSet(1, 2),
		},
	})
	if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("overlapping complete bucket partition error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}
}

func TestValidateFinalTargetsRequiresCompleteCoverageForNestedAndExternalReclaimBuckets(t *testing.T) {
	t.Parallel()
	firstHalf := machine.NewCPUSet(0, 1)
	secondHalf := machine.NewCPUSet(2, 3)
	reclaimFinal := firstHalf.Union(secondHalf)

	tests := []struct {
		name         string
		complete     bool
		specs        []NodeSpec
		desiredByRel map[string]machine.CPUSet
	}{
		{
			name:     "nested prefix missing coverage",
			complete: false,
			specs: []NodeSpec{
				{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimFinal},
				{Rel: "reclaim/prefix", ParentRel: "reclaim", Domain: DomainReclaim, CPUs: reclaimFinal},
				{
					Rel: "reclaim/prefix/bucket-0", ParentRel: "reclaim/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: firstHalf,
					Constraint: TopologyConstraint{CPUUpperBound: firstHalf, Scope: TopologyScopeNUMANode},
				},
			},
			desiredByRel: map[string]machine.CPUSet{
				"reclaim":                 reclaimFinal,
				"reclaim/prefix":          reclaimFinal,
				"reclaim/prefix/bucket-0": firstHalf,
			},
		},
		{
			name:     "nested prefix complete coverage",
			complete: true,
			specs: []NodeSpec{
				{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimFinal},
				{Rel: "reclaim/prefix", ParentRel: "reclaim", Domain: DomainReclaim, CPUs: reclaimFinal},
				{
					Rel: "reclaim/prefix/bucket-0", ParentRel: "reclaim/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: firstHalf,
					Constraint: TopologyConstraint{CPUUpperBound: firstHalf, Scope: TopologyScopeNUMANode},
				},
				{
					Rel: "reclaim/prefix/bucket-1", ParentRel: "reclaim/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: secondHalf,
					Constraint: TopologyConstraint{CPUUpperBound: secondHalf, Scope: TopologyScopeNUMANode},
				},
			},
			desiredByRel: map[string]machine.CPUSet{
				"reclaim":                 reclaimFinal,
				"reclaim/prefix":          reclaimFinal,
				"reclaim/prefix/bucket-0": firstHalf,
				"reclaim/prefix/bucket-1": secondHalf,
			},
		},
		{
			name:     "external prefix reclaim index missing coverage",
			complete: false,
			specs: []NodeSpec{
				{
					Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimFinal,
					Metadata: map[string]string{"reclaim-index": "0"},
				},
				{Rel: "external/prefix", Domain: DomainReclaim, CPUs: reclaimFinal},
				{
					Rel: "external/prefix/bucket-0", ParentRel: "external/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: firstHalf,
					Constraint: TopologyConstraint{CPUUpperBound: firstHalf, Scope: TopologyScopeNUMANode},
					Metadata:   map[string]string{"reclaim-index": "0"},
				},
			},
			desiredByRel: map[string]machine.CPUSet{
				"reclaim":                  reclaimFinal,
				"external/prefix":          reclaimFinal,
				"external/prefix/bucket-0": firstHalf,
			},
		},
		{
			name:     "external prefix reclaim index complete coverage",
			complete: true,
			specs: []NodeSpec{
				{
					Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: reclaimFinal,
					Metadata: map[string]string{"reclaim-index": "0"},
				},
				{Rel: "external/prefix", Domain: DomainReclaim, CPUs: reclaimFinal},
				{
					Rel: "external/prefix/bucket-0", ParentRel: "external/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: firstHalf,
					Constraint: TopologyConstraint{CPUUpperBound: firstHalf, Scope: TopologyScopeNUMANode},
					Metadata:   map[string]string{"reclaim-index": "0"},
				},
				{
					Rel: "external/prefix/bucket-1", ParentRel: "external/prefix",
					Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: secondHalf,
					Constraint: TopologyConstraint{CPUUpperBound: secondHalf, Scope: TopologyScopeNUMANode},
					Metadata:   map[string]string{"reclaim-index": "0"},
				},
			},
			desiredByRel: map[string]machine.CPUSet{
				"reclaim":                  reclaimFinal,
				"external/prefix":          reclaimFinal,
				"external/prefix/bucket-0": firstHalf,
				"external/prefix/bucket-1": secondHalf,
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFinalTargets(PhasePlanInput{
				DAG:          mustPlanDAG(t, tc.specs),
				DesiredByRel: tc.desiredByRel,
			})
			if tc.complete {
				if err != nil {
					t.Fatalf("complete bucket coverage rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
				t.Fatalf("incomplete bucket coverage error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
			}
		})
	}
}

func TestValidateFinalTargetsIgnoresObservedBucketOverflowWhenFinalAndEnteringAreLegal(t *testing.T) {
	t.Parallel()
	upper := machine.NewCPUSet(0, 1)
	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "reclaim/bucket-0", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim,
		CPUs: upper, Constraint: TopologyConstraint{CPUUpperBound: upper, Scope: TopologyScopeNUMANode},
	}})
	in := PhasePlanInput{
		DAG:          dag,
		DesiredByRel: map[string]machine.CPUSet{"reclaim/bucket-0": machine.NewCPUSet(0)},
		Snapshot: planSnapshot(map[string]EntryState{
			"reclaim/bucket-0": {CPUs: machine.NewCPUSet(2)},
		}, map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(2)}),
	}
	if err := validateFinalTargets(in); err != nil {
		t.Fatalf("legal final should permit observed repair: %v", err)
	}
}
