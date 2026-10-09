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
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestPrepareIncrementalContextBuildsMinimumOwnershipFrontier(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "root", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"root":   {CPUs: machine.NewCPUSet(0, 1)},
		"root/a": {CPUs: machine.NewCPUSet(0)},
		"root/b": {CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{
		"root": {{Name: "a"}, {Name: "b"}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"root": DomainPrimary, "root/a": DomainPrimary, "root/b": DomainPrimary,
	}
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"root": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
	}
	depth, relOrder := buildSnapshotDepthByRel(snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(snapshot, dag, depth, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	projectionContext, err := buildDrainProjectionContext(
		context.Background(), snapshot, nil, nil)
	if err != nil {
		t.Fatalf("buildDrainProjectionContext: %v", err)
	}
	err = prepareIncrementalDrainProjectionContext(DrainProjectionInput{
		PlanInput:       input,
		LeavingByDomain: map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet()},
		DomainByRel:     domainByRel,
		ParentByRel:     parentByRel,
		DepthByRel:      depth,
		Context:         projectionContext,
	}, projectionContext)
	if err != nil {
		t.Fatalf("prepareIncrementalDrainProjectionContext: %v", err)
	}
	if got, want := projectionContext.affectedRelsByCPU[0], []string{"root/a", "root/b", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CPU 0 affected rels = %v, want %v", got, want)
	}
	if got, want := projectionContext.affectedRelsByCPU[1], []string{"root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root residual CPU affected rels = %v, want %v", got, want)
	}
}

func TestPrepareIncrementalContextIgnoresNonTransferMemberships(t *testing.T) {
	t.Parallel()
	contextCost := func(cpus machine.CPUSet) int {
		dag := mustPlanDAG(t, []NodeSpec{{
			Rel: "root", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
			CPUs: machine.NewCPUSet(), TrustAnchor: true,
		}})
		snapshot := planSnapshot(map[string]EntryState{
			"root":       {CPUs: cpus},
			"root/child": {CPUs: cpus},
		}, map[DomainID]machine.CPUSet{DomainPrimary: cpus})
		snapshot.Children = map[string][]ChildRef{"root": {{Name: "child"}}}
		snapshot.DomainByRel = map[string]DomainID{
			"root": DomainPrimary, "root/child": DomainPrimary,
		}
		input := PhasePlanInput{
			Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{"root": machine.NewCPUSet()},
			AllowedCPUs:  cpus,
		}
		depth, relOrder := buildSnapshotDepthByRel(snapshot, nil)
		domainByRel, parentByRel := buildPlannerRelations(snapshot, dag, depth, relOrder.relsAsc, relOrder.childRelsByRel, nil)
		projectionContext, err := buildDrainProjectionContext(
			context.Background(), snapshot, nil, nil)
		if err != nil {
			t.Fatalf("buildDrainProjectionContext: %v", err)
		}
		err = prepareIncrementalDrainProjectionContext(DrainProjectionInput{
			PlanInput:       input,
			LeavingByDomain: map[DomainID]machine.CPUSet{DomainPrimary: cpus},
			DomainByRel:     domainByRel,
			ParentByRel:     parentByRel,
			DepthByRel:      depth,
			Context:         projectionContext,
			TransferCPUs:    machine.NewCPUSet(0),
		}, projectionContext)
		if err != nil {
			t.Fatalf("prepareIncrementalDrainProjectionContext: %v", err)
		}
		return projectionContext.cost
	}

	transferOnly := contextCost(machine.NewCPUSet(0))
	withNonTransfer := contextCost(replayCPUSet([2]int{0, 95}))
	if withNonTransfer != transferOnly {
		t.Fatalf("context cost with non-transfer memberships = %d, want %d",
			withNonTransfer, transferOnly)
	}
}

func TestBuildDrainProjectionContextMatchesLegacyProtectedDescendantScan(t *testing.T) {
	t.Parallel()
	snapshot := planSnapshot(map[string]EntryState{
		"root":        {CPUs: machine.NewCPUSet(0, 1, 2, 3)},
		"root/a":      {CPUs: machine.NewCPUSet(0, 1)},
		"root/a/leaf": {CPUs: machine.NewCPUSet(1)},
		"root/b":      {CPUs: machine.NewCPUSet(2)},
		"root/b/leaf": {CPUs: machine.NewCPUSet(2)},
	}, nil)
	protectedByRel := map[string]machine.CPUSet{
		"root/a/leaf": machine.NewCPUSet(1),
		"root/b":      machine.NewCPUSet(2),
		"root/c/leaf": machine.NewCPUSet(3),
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxDeadlockProbeOperations: 100})
	projectionContext, err := buildDrainProjectionContext(
		context.Background(), snapshot, protectedByRel, budget)
	if err != nil {
		t.Fatalf("buildDrainProjectionContext: %v", err)
	}
	want := map[string]machine.CPUSet{
		"root":        machine.NewCPUSet(1, 2, 3),
		"root/a":      machine.NewCPUSet(1),
		"root/a/leaf": machine.NewCPUSet(1),
		"root/b":      machine.NewCPUSet(2),
		"root/b/leaf": machine.NewCPUSet(),
	}
	for rel, wantCPUs := range want {
		got := projectionContext.protectedDescendantUnionByRel[rel]
		if got.IsEmpty() != wantCPUs.IsEmpty() || (!wantCPUs.IsEmpty() && !got.Equals(wantCPUs)) {
			t.Fatalf("%s protected descendant union = %s, want %s",
				rel, got.String(), wantCPUs.String())
		}
	}
	if projectionContext.cost != 6 {
		t.Fatalf("preaggregation cost = %d, want 6", projectionContext.cost)
	}
	if got := budget.Usage().DeadlockProbeOperations; got != projectionContext.cost {
		t.Fatalf("charged probe operations = %d, want context cost %d", got, projectionContext.cost)
	}
}

func TestProjectDrainTargetsMatchesPlannerAndReportsDomainUnion(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {CPUs: machine.NewCPUSet(0, 1)},
		"reclaim": {CPUs: machine.NewCPUSet(2)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1),
		DomainReclaim: machine.NewCPUSet(2),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
	}
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(1), "reclaim": machine.NewCPUSet(0),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	}
	plan, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	depth, relOrder := buildSnapshotDepthByRel(snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(snapshot, dag, depth, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	projection, err := projectDrainTargets(DrainProjectionInput{
		PlanInput:       input,
		DrainBatch:      plan.DrainBatch,
		LeavingByDomain: map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0), DomainReclaim: machine.NewCPUSet(2)},
		DomainByRel:     domainByRel,
		ParentByRel:     parentByRel,
		DepthByRel:      depth,
	})
	if err != nil {
		t.Fatalf("projectDrainTargets: %v", err)
	}
	for rel, want := range plan.TargetByRel {
		if got := projection.TargetByRel[rel]; !got.CPUs.Equals(want.CPUs) {
			t.Fatalf("%s projected target = %s, planner = %s", rel, got.CPUs.String(), want.CPUs.String())
		}
	}
	if got := projection.DomainUnion[DomainPrimary]; !got.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("primary projected union = %s, want 1", got.String())
	}
	if projection.Cost.Rels != len(snapshot.Entries) {
		t.Fatalf("projection cost = %+v, want %d rels", projection.Cost, len(snapshot.Entries))
	}
}

func TestProjectDrainTargetsReportsV1NonEmptyBlocker(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(1), TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)})
	snapshot.DomainByRel = map[string]DomainID{"primary": DomainPrimary}
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(1)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
	}
	depth, relOrder := buildSnapshotDepthByRel(snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(snapshot, dag, depth, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	projection, err := projectDrainTargets(DrainProjectionInput{
		PlanInput: input, DrainBatch: map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)},
		LeavingByDomain: map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)},
		DomainByRel:     domainByRel, ParentByRel: parentByRel, DepthByRel: depth,
	})
	if err != nil {
		t.Fatalf("projectDrainTargets: %v", err)
	}
	if got := projection.EmptyBlockers["primary"]; !got.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("empty blocker = %s, want 0", got.String())
	}
}

func TestProjectDrainTargetsKeepsV1AnchorForObservedNUMAOverflow(t *testing.T) {
	t.Parallel()
	upper := machine.NewCPUSet(0)
	overflow := machine.NewCPUSet(2)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: upper, TrustAnchor: true},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: upper,
			Constraint: TopologyConstraint{CPUUpperBound: upper, Scope: TopologyScopeNUMANode},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"reclaim":                {CPUs: overflow},
		"reclaim/bucket-0":       {CPUs: overflow},
		"reclaim/bucket-0/child": {CPUs: overflow},
	}, map[DomainID]machine.CPUSet{DomainReclaim: overflow})
	snapshot.Children = map[string][]ChildRef{
		"reclaim":          {{Name: "bucket-0"}},
		"reclaim/bucket-0": {{Name: "child"}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"reclaim": DomainReclaim, "reclaim/bucket-0": DomainReclaim,
		"reclaim/bucket-0/child": DomainReclaim,
	}
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim": upper, "reclaim/bucket-0": upper,
		},
		AllowedCPUs: machine.NewCPUSet(0, 2),
	}
	depth, relOrder := buildSnapshotDepthByRel(snapshot, nil)
	domainByRel, parentByRel := buildPlannerRelations(snapshot, dag, depth, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	projection, err := projectDrainTargets(DrainProjectionInput{
		PlanInput:       input,
		DrainBatch:      map[DomainID]machine.CPUSet{DomainReclaim: overflow},
		LeavingByDomain: map[DomainID]machine.CPUSet{DomainReclaim: overflow},
		DomainByRel:     domainByRel, ParentByRel: parentByRel, DepthByRel: depth,
	})
	if err != nil {
		t.Fatalf("projectDrainTargets: %v", err)
	}
	if got := projection.TargetByRel["reclaim/bucket-0/child"].CPUs; !got.Equals(overflow) {
		t.Fatalf("overflow child target = %s, want v1 hold/anchor %s", got.String(), overflow.String())
	}
}

func TestBuildPhasePlanChargesActualDrainProjectionCost(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {CPUs: machine.NewCPUSet(0)},
		"reclaim": {CPUs: machine.NewCPUSet(1)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0),
		DomainReclaim: machine.NewCPUSet(1),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary,
		"reclaim": DomainReclaim,
	}

	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0),
			"reclaim": machine.NewCPUSet(1),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1),
		Budget: NewBudgetTracker(ConvergenceBudget{
			MaxPlanOperations: 1,
		}),
	})
	if !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("BuildPhasePlan error = %v, want projection cost to exhaust %v", err, ErrPlanOperationBudgetExceeded)
	}
}
