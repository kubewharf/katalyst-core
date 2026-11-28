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
	"fmt"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestAdmissionDrainMatchesRemovedReclaimCPUOutsideParent(t *testing.T) {
	t.Parallel()
	for _, parentContainsReclaim := range []bool{false, true} {
		parentContainsReclaim := parentContainsReclaim
		t.Run(fmt.Sprint(parentContainsReclaim), func(t *testing.T) {
			t.Parallel()
			parent := machine.NewCPUSet(0, 2)
			if parentContainsReclaim {
				parent.Add(1)
			}
			plan := &PhasePlan{Base: &CompleteSnapshot{
				Entries:     map[string]EntryState{"primary": {CPUs: parent}},
				DomainByRel: map[string]DomainID{"primary/child": DomainPrimary},
				DomainUnion: map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(1)},
			}, Operations: []PlanOperation{{
				Rel: "primary/child", ParentRel: "primary", Direction: WriteShrink,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0)},
			}}}
			required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
			if err != nil {
				t.Fatal(err)
			}
			if parentContainsReclaim {
				if len(required.Operations) != 0 || len(deferred.Operations) != 1 {
					t.Fatal("contained reclaim CPU must remain deferred")
				}
			} else if len(required.Operations) != 1 {
				t.Fatal("uncontained reclaim CPU must be drained despite another removed CPU being contained")
			}
		})
	}
}

func TestAdmissionRequiredShrinkIncludesChildRelease(t *testing.T) {
	t.Parallel()
	plan := &PhasePlan{Base: &CompleteSnapshot{
		Entries:     map[string]EntryState{"primary": {CPUs: machine.NewCPUSet(0)}, "primary/middle": {CPUs: machine.NewCPUSet(0, 1)}, "primary/middle/leaf": {CPUs: machine.NewCPUSet(0, 1)}},
		Children:    map[string][]ChildRef{"primary/middle": {{Name: "leaf"}}, "primary/middle/leaf": {}},
		DomainByRel: map[string]DomainID{"primary/middle": DomainPrimary, "primary/middle/leaf": DomainPrimary},
		DomainUnion: map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(1)},
	}, Operations: []PlanOperation{
		{Rel: "primary/middle/leaf", ParentRel: "primary/middle", Direction: WriteShrink, ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0)}},
		{Rel: "primary/middle", ParentRel: "primary", Direction: WriteShrink, ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0)}},
	}}
	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(required.Operations) != 2 || len(deferred.Operations) != 0 {
		t.Fatalf("required shrink lost its child release: %+v", required.Operations)
	}
	if required.Operations[0].Rel != "primary/middle/leaf" {
		t.Fatal("child release must precede parent shrink")
	}
}

func TestConvergenceIDHashesEffectiveSemanticTarget(t *testing.T) {
	t.Parallel()
	in := PhasePlanInput{DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)}, AllowedCPUs: machine.NewCPUSet(0)}
	fallback := canonicalConvergenceID(in)
	in.SemanticByRel = map[string]machine.CPUSet{}
	if canonicalConvergenceID(in) == fallback {
		t.Fatal("explicit empty semantic target collided with desired fallback")
	}
	in.SemanticByRel = in.DesiredByRel
	if canonicalConvergenceID(in) != fallback {
		t.Fatal("same effective semantic target must have same convergence id")
	}
}

func TestEffectiveTargetsFailurePreservesRequiredFloor(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG(snbAdmissionNodeSpecs())
	if err != nil {
		t.Fatal(err)
	}
	floors := map[string]machine.CPUSet{"reclaimed/reclaimed-0": machine.NewCPUSet(1, 2, 3)}
	details := snbAdmissionCPUDetails()
	info := details[2]
	info.NUMANodeID = 1
	details[2] = info
	_, _, err = computeEffectiveTargets(dag, false, details, map[string]machine.CPUSet{"kubepods": machine.NewCPUSet(1)}, floors)
	if err == nil {
		t.Fatal("wrong NUMA binding should fail validation")
	}
	if !floors["reclaimed/reclaimed-0"].Equals(machine.NewCPUSet(1, 2, 3)) {
		t.Fatal("failed normalization changed caller floor")
	}
}

func TestResetContinuesAfterDisappearedSibling(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.allowUnwitnessedExpansion = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("primary/a", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
	fake.add("primary/b", CgroupIdentity{Device: 1, Inode: 3}, "0-1", "0")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary/a" {
			delete(fake.nodes, rel)
			return syscall.ENOENT
		}
		return nil
	}
	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0", TrustAnchor: true}})
	if err != nil {
		t.Fatal(err)
	}
	result := &ConvergenceResult{}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "0", result)
	if err := writer.execute(context.Background(), dag, map[string]machine.CPUSet{"primary": machine.MustParse("0-3")}, false, nil, nil, nil, map[string]struct{}{"primary/a": {}, "primary/b": {}}); err != nil {
		t.Fatal(err)
	}
	if got := fake.nodes["primary/b"].cpus; !got.Equals(machine.MustParse("0-3")) {
		t.Fatalf("later safe sibling stayed %s after earlier sibling disappeared", got.String())
	}
}

func TestResetReachesPermittedDeepDescendants(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.allowUnwitnessedExpansion = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	safe := map[string]struct{}{}
	rel := "primary"
	for depth := 1; depth < defaultApplyMaxSnapshotDepth; depth++ {
		rel = filepath.Join(rel, "child")
		fake.add(rel, CgroupIdentity{Device: 1, Inode: uint64(depth + 1)}, "0-1", "0")
		safe[rel] = struct{}{}
	}
	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0", TrustAnchor: true}})
	if err != nil {
		t.Fatal(err)
	}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "0", &ConvergenceResult{})
	if err := writer.execute(context.Background(), dag, map[string]machine.CPUSet{"primary": machine.MustParse("0-3")}, false, nil, nil, nil, safe); err != nil {
		t.Fatal(err)
	}
	if got := fake.nodes[rel].cpus; !got.Equals(machine.MustParse("0-3")) {
		t.Fatalf("permitted deep descendant stayed %s", got.String())
	}
}

func TestResetOrdersMemsShrinkWithUnchangedCPUs(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.allowUnwitnessedExpansion = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0-1")
	fake.add("primary/child", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0-1")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationWriteMems && rel == "primary" && fake.nodes["primary/child"].mems != "0" {
			return fmt.Errorf("parent mems shrink before child")
		}
		return nil
	}
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0", TrustAnchor: true},
		{Rel: "primary/child", ParentRel: "primary", Domain: DomainPrimary, CPUs: machine.MustParse("0-1"), Mems: "0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "0", &ConvergenceResult{})
	if err := writer.execute(context.Background(), dag, map[string]machine.CPUSet{"primary": machine.MustParse("0-3"), "primary/child": machine.MustParse("0-1")}, false, nil, nil, nil, nil); err != nil {
		t.Fatalf("feasible mems shrink failed: %v", err)
	}
	if fake.nodes["primary"].mems != "0" || fake.nodes["primary/child"].mems != "0" {
		t.Fatal("memory targets were not reached")
	}
}

func TestV2InheritanceClearKeepsConfiguredDescendant(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	entry := hierarchy.snapshot.Entries[projectedInheritRel]
	entry.ConfiguredCPUs = machine.MustParse("0-1")
	entry.CPUs = machine.MustParse("0-1")
	hierarchy.snapshot.Entries[projectedInheritRel] = entry
	operation := hierarchy.cpuOperation(projectedRootRel, machine.MustParse("0-3"), machine.NewCPUSet())
	hierarchy.externalByRel[projectedRootRel] = projectedExternalInheritance{CPUs: machine.MustParse("0-3"), hasCPUs: true}
	if err := hierarchy.applyOperation(operation); err != nil {
		t.Fatalf("feasible parent inheritance clear rejected: %v", err)
	}
	if got := hierarchy.snapshot.Entries[projectedInheritRel].ConfiguredCPUs; !got.Equals(machine.MustParse("0-1")) {
		t.Fatalf("configured descendant changed: %s", got.String())
	}
}

func TestV2TargetProofDetectsLatentConfiguredCPUs(t *testing.T) {
	t.Parallel()
	entry := EntryState{CPUs: machine.MustParse("0-1"), ConfiguredCPUs: machine.MustParse("0-3")}
	got := observedCPUsForTargetProof(entry, machine.MustParse("0-1"), v2Capabilities())
	if got.Equals(machine.MustParse("0-1")) {
		t.Fatal("latent configured CPUs were accepted as target proof")
	}
	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary,
		Domain: DomainPrimary, CPUs: machine.MustParse("0-1"), Mems: "0", TrustAnchor: true,
	}})
	entry.Identity, entry.Mems = CgroupIdentity{Inode: 1}, "0"
	snapshot := planSnapshot(map[string]EntryState{"primary": entry},
		map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-1")})
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.MustParse("0-1")},
		AllowedCPUs:  machine.MustParse("0-3"), AllowEmptyTarget: true,
		Capabilities: v2Capabilities(), Budget: NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || !plan.Operations[0].ExpectedCurrent.CPUs.Equals(machine.MustParse("0-3")) ||
		!plan.Operations[0].Target.CPUs.Equals(machine.MustParse("0-1")) {
		t.Fatalf("planner failed to emit configured shrink: %+v", plan.Operations)
	}
}

func TestDomainGateRequiresPhysicalReleaseBeyondDormantSemanticOwnership(t *testing.T) {
	t.Parallel()
	snapshot := &CompleteSnapshot{
		Entries:        map[string]EntryState{"primary": {CPUs: machine.NewCPUSet(0)}, "reclaim": {CPUs: machine.NewCPUSet(1)}, "reclaim/bucket": {CPUs: machine.NewCPUSet(0, 1)}},
		DomainByRel:    map[string]DomainID{"primary": DomainPrimary, "reclaim": DomainReclaim, "reclaim/bucket": DomainReclaim},
		DomainUnion:    map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0), DomainReclaim: machine.NewCPUSet(0, 1)},
		OwnershipByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0), "reclaim": machine.NewCPUSet(1), "reclaim/bucket": machine.NewCPUSet()},
	}
	gate, err := NewDomainGate("semantic", snapshot, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1), DomainReclaim: machine.NewCPUSet(1)}, machine.NewCPUSet(0, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := CloneCompleteSnapshot(snapshot)
	entry := fresh.Entries["reclaim"]
	entry.CPUs = machine.NewCPUSet()
	fresh.Entries["reclaim"] = entry
	fresh.OwnershipByRel["reclaim"] = machine.NewCPUSet()
	gate.Revalidate("semantic", fresh, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1), DomainReclaim: machine.NewCPUSet()})
	if gate.AllowedEntering(DomainPrimary).Contains(1) {
		t.Fatal("semantic ownership cleanup authorized an unverified physical release")
	}
	bucket := fresh.Entries["reclaim/bucket"]
	bucket.CPUs = machine.NewCPUSet()
	fresh.Entries["reclaim/bucket"] = bucket
	fresh.DomainUnion[DomainReclaim] = machine.NewCPUSet()
	gate.Revalidate("semantic", fresh, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1), DomainReclaim: machine.NewCPUSet()})
	if !gate.AllowedEntering(DomainPrimary).Contains(1) {
		t.Fatal("verified empty physical domain did not authorize CPU transfer")
	}
}

func TestFrozenExecutionFailureRestoresCompletePrefix(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9FinalizeFixture(t, false)
	trace, compileErr := fixture.round.compileFixedPointTrace(context.Background(), base)
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	driver := fixture.driver
	initial := driver.snapshot()
	injected := errors.New("injected later forward write")
	failed := false
	driver.beforeCall = func(op HierarchyOperation, _ string) error {
		if !failed && driver.PhysicalWriteCount() > 0 && (op == HierarchyOperationWriteCPUs || op == HierarchyOperationWriteMems) {
			failed = true
			return injected
		}
		return nil
	}
	round := frozenExecutionRound(t, trace, driver)
	result := &ConvergenceResult{}
	_, err := round.executeFrozenTrace(context.Background(), trace, reserveTraceWithBudget(t, round.budget, trace), result)
	if !errors.Is(err, injected) || !failed {
		t.Fatalf("injection not reached: %v", err)
	}
	if !reflect.DeepEqual(initial, driver.snapshot()) {
		for rel, before := range initial {
			if !reflect.DeepEqual(before, driver.snapshot()[rel]) {
				t.Errorf("state mismatch %s before=%+v after=%+v", rel, before, driver.snapshot()[rel])
			}
		}
		t.Fatalf("failed frozen execution retained a partial write prefix: error=%v result=%+v", err, result)
	}
	if result.Published || result.FinalSnapshotCurrent {
		t.Fatal("failed execution advertised current publication")
	}
}

func TestFrozenFinalProofRejectsActivatedDormantBucket(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.driver.capabilities = cgroupV1Policy.capabilities(true)
	fixture.addPrimary("primary", "0-1", "0")
	fixture.addReclaim("reclaim", "2-3", "0")
	fixture.specs = append(fixture.specs, NodeSpec{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Metadata: map[string]string{"numa": "0"}})
	fixture.driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 99}, "2-3", "0")
	fixture.driver.markInactive("reclaim/numa-0")
	fixture.requireCPUSet("primary", "0-1")
	fixture.targetByRel["reclaim/numa-0"] = machine.NewCPUSet()
	dag, dagErr := BuildDAG(fixture.specs)
	if dagErr != nil {
		t.Fatal(dagErr)
	}
	snapshot, snapshotErr := BuildCompleteSnapshot(context.Background(), fixture.driver, dag, SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary", "reclaim", "reclaim/numa-0"}, CollectDormantActivity: true}, NewBudgetTracker(ConvergenceBudget{}))
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	proof, ok := dormantLeafProof(snapshot, "reclaim/numa-0")
	if !ok {
		t.Fatal("fixture did not establish dormant proof")
	}
	boundary, boundaryErr := compileFrozenBoundaryV1(snapshot, FrozenCoordinatorEvaluationInput{DAGSpecs: fixture.specs}, nil)
	if boundaryErr != nil {
		t.Fatal(boundaryErr)
	}
	snapshot = snapshotWithDormantOwnershipOverlay(snapshot, map[string]struct{}{"reclaim/numa-0": {}}, map[string]machine.CPUSet{"reclaim/numa-0": machine.NewCPUSet()})
	trace := &CompiledPhaseTrace{FrozenBoundary: boundary, FinalSnapshot: snapshot, Objective: ConvergenceObjectiveFull, EvaluationInput: FrozenCoordinatorEvaluationInput{
		DAGSpecs: fixture.specs, AllowedCPUs: machine.MustParse("0-3"), Capabilities: fixture.driver.Capabilities(),
		TargetByRel:     map[string]machine.CPUSet{"primary": machine.MustParse("0-1"), "reclaim": machine.MustParse("2-3"), "reclaim/numa-0": machine.NewCPUSet()},
		DesiredByDomain: map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-1"), DomainReclaim: machine.MustParse("2-3")},
	}}
	round := &coordinatorRound{dag: dag, driver: fixture.driver, budget: NewBudgetTracker(ConvergenceBudget{}), dormantProofs: map[string]DormantLeafProof{"reclaim/numa-0": proof}}
	if _, err := round.proveFrozenTraceFinalState(context.Background(), trace); err != nil {
		t.Fatalf("valid dormant final proof rejected: %v", err)
	}
	fixture.driver.nodes["reclaim/numa-0"].activity.TasksEmpty = false
	_, err := round.proveFrozenTraceFinalState(context.Background(), trace)
	if err == nil || !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("activated dormant ownership was not rejected as stale: %v", err)
	}
}

func TestBenchmarkScaleCasesDescribeUniqueNodeCounts(t *testing.T) {
	t.Parallel()
	seen := make(map[int]struct{})
	for _, tc := range benchmarkScaleCases() {
		if tc.name != fmt.Sprintf("nodes_%d", tc.nodes) {
			t.Fatalf("benchmark label %q does not describe actual node input %d", tc.name, tc.nodes)
		}
		if _, duplicate := seen[tc.nodes]; duplicate {
			t.Fatalf("duplicate node fixture %d", tc.nodes)
		}
		seen[tc.nodes] = struct{}{}
	}
}

func TestNormalPhaseFailureRestoresCompletePrefix(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cpu_failure", "mems_failure", "canceled", "rollback_failure", "third_state", "repeated_rel"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			driver := newFakeHierarchyDriver()
			driver.allowUnwitnessedExpansion = true
			driver.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0-1")
			driver.add("b", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0-1")
			initial := driver.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("later normal operation failed")
			rollbackFailure := errors.New("inverse write failed")
			failed := false
			driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if failed && mode == "rollback_failure" && op == HierarchyOperationWriteCPUs && rel == "a" {
					return rollbackFailure
				}
				targetOp := HierarchyOperationWriteCPUs
				if mode == "mems_failure" {
					targetOp = HierarchyOperationWriteMems
				}
				if !failed && op == targetOp && rel == "b" {
					failed = true
					if mode == "third_state" {
						driver.nodes["a"].cpus = machine.NewCPUSet(1)
						driver.nodes["a"].configuredCPUs = machine.NewCPUSet(1)
					}
					if mode == "canceled" {
						cancel()
						return context.Canceled
					}
					return injected
				}
				return nil
			}
			plan := PhasePlan{ConvergenceID: "normal-prefix", Kind: PhaseDrain, Capabilities: driver.Capabilities(), Operations: []PlanOperation{
				{Rel: "a", ExpectedIdentity: driver.nodes["a"].identity, ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0-1"}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"}, Direction: WriteShrink, WriteMems: true, OwnsMems: true},
				{Rel: "b", ExpectedIdentity: driver.nodes["b"].identity, ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("2-3"), Mems: "0-1"}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(2), Mems: "0"}, Direction: WriteShrink, WriteMems: true, OwnsMems: true},
			}}
			if mode == "repeated_rel" {
				grow := plan.Operations[0]
				grow.ExpectedCurrent = plan.Operations[0].Target
				grow.Target = plan.Operations[0].ExpectedCurrent
				grow.Direction = WriteGrow
				plan.Operations = []PlanOperation{plan.Operations[0], grow, plan.Operations[1]}
			}
			plan.PlanID = canonicalExecutionPlanID(plan)
			for i := range plan.Operations {
				plan.Operations[i].PlanID = plan.PlanID
			}
			result := &ConvergenceResult{}
			writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), result)
			writer.recoveryContextFactory = deterministicRecoveryContext
			err := writer.execute(ctx, plan)
			expected := injected
			if mode == "canceled" {
				expected = context.Canceled
			}
			if !failed || !errors.Is(err, expected) {
				t.Fatalf("expected execution failure: %v", err)
			}
			if mode == "rollback_failure" || mode == "third_state" {
				var related *executionRelatedError
				if !errors.As(err, &related) {
					t.Fatalf("rollback failure was hidden: %v", err)
				}
				if mode == "rollback_failure" && !errors.Is(err, rollbackFailure) {
					t.Fatalf("inverse cause lost: %v", err)
				}
				if mode == "third_state" && !driver.nodes["a"].cpus.Equals(machine.NewCPUSet(1)) {
					t.Fatal("rollback overwrote external third state")
				}
				if len(result.Journal) == 0 {
					t.Fatal("residual physical impact was lost")
				}
			} else {
				if !reflect.DeepEqual(initial, driver.snapshot()) {
					t.Fatal("normal failure retained earlier successful operation")
				}
				if len(result.Journal) != 0 || result.Applied != 0 {
					t.Fatal("restored prefix retained net progress")
				}
			}
		})
	}
}

func TestDormantAncestorOwnershipDoesNotClaimUnwrittenTarget(t *testing.T) {
	t.Parallel()
	snapshot := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"reclaim":         {CPUs: machine.NewCPUSet(0)},
			"reclaim/dormant": {CPUs: machine.NewCPUSet(0)},
		},
		DomainByRel: map[string]DomainID{"reclaim": DomainReclaim, "reclaim/dormant": DomainReclaim},
		DomainUnion: map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(0)},
	}
	projected := snapshotWithDormantOwnershipOverlay(snapshot,
		map[string]struct{}{"reclaim/dormant": {}},
		map[string]machine.CPUSet{"reclaim": machine.NewCPUSet(0, 1), "reclaim/dormant": machine.NewCPUSet()})
	if projected.OwnershipByRel["reclaim"].Contains(1) {
		t.Fatal("dormant ancestor claimed a target CPU before any physical write")
	}
	gate, err := NewDomainGate("dormant-ancestor", projected,
		map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(0, 1)}, machine.NewCPUSet(0, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.AllowedEntering(DomainReclaim).Contains(1) {
		t.Fatal("unwritten target CPU was not authorized for its sole destination")
	}
}

func TestCompleteSnapshotRejectsDetachedEntry(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0", TrustAnchor: true}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCompleteSnapshotEvidence(snapshot); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	snapshot.Entries["orphan"] = EntryState{Rel: "orphan", Identity: CgroupIdentity{Device: 1, Inode: 9}, CPUs: machine.NewCPUSet(0), ConfiguredCPUs: machine.NewCPUSet(0), Mems: "0", ConfiguredMems: "0"}
	snapshot.DomainByRel["orphan"] = DomainPrimary
	snapshot.DomainUnion[DomainPrimary] = snapshot.DomainUnion[DomainPrimary].Union(machine.NewCPUSet(0))
	if err := validateCompleteSnapshotEvidence(snapshot); err == nil {
		t.Fatal("detached entry was accepted as structurally closed")
	}
}
