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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestSplitPlanForAdmissionClassifiesRequiredAndDeferredWithoutLoss(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Kind: PhaseExpand,
		Operations: []PlanOperation{
			{Rel: "reclaim/source", Direction: WriteShrink, ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(1, 2)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(2)}},
			{Rel: "primary/pod", Direction: WriteGrow, ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)}},
			{Rel: "primary/pod/container", Direction: WriteShrink, ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2)}, Target: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)}},
		},
	}
	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{
		PendingCPUSet: machine.NewCPUSet(1),
		PendingRequiredByRel: map[string]machine.CPUSet{
			"primary/pod": machine.NewCPUSet(1),
		},
		DeferredCPUSetByRel: map[string]machine.CPUSet{
			"primary/pod/container": machine.NewCPUSet(0, 1),
		},
	})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if len(required.Operations) != 2 || len(deferred.Operations) != 1 {
		t.Fatalf("split counts required=%d deferred=%d, want 2/1", len(required.Operations), len(deferred.Operations))
	}
	if required.Operations[0].Requirement != OperationAdmissionSourceDrain {
		t.Fatalf("source requirement = %q", required.Operations[0].Requirement)
	}
	if required.Operations[1].Requirement != OperationAdmissionAncestorGrow {
		t.Fatalf("ancestor requirement = %q", required.Operations[1].Requirement)
	}
	if deferred.Operations[0].Requirement != OperationDeferredLeafExact {
		t.Fatalf("leaf requirement = %q", deferred.Operations[0].Requirement)
	}

	for _, tc := range []struct {
		name        string
		source      DomainID
		destination DomainID
		sourceRel   string
	}{
		{name: "PrimaryToReclaim", source: DomainPrimary, destination: DomainReclaim, sourceRel: "primary/source-owner"},
		{name: "ReclaimToPrimary", source: DomainReclaim, destination: DomainPrimary, sourceRel: "reclaim/source-owner"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sourcePlan := &PhasePlan{
				Kind: PhaseDrain,
				Base: &CompleteSnapshot{
					DomainByRel: map[string]DomainID{tc.sourceRel: tc.source},
				},
				TransferGraph: map[DomainID]map[DomainID]machine.CPUSet{
					tc.source: {
						tc.destination: machine.NewCPUSet(2),
					},
				},
				Operations: []PlanOperation{{
					Rel:             tc.sourceRel,
					Direction:       WriteShrink,
					ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
					Target:          CPUSetTarget{CPUs: machine.NewCPUSet(3)},
				}},
			}

			required, deferred, err := SplitPlanForAdmission(sourcePlan, AdmissionSafetyInput{})
			if err != nil {
				t.Fatalf("SplitPlanForAdmission() error = %v", err)
			}
			if len(required.Operations) != 1 || len(deferred.Operations) != 0 {
				t.Fatalf("split counts required=%d deferred=%d, want %s source owner required",
					len(required.Operations), len(deferred.Operations), tc.source)
			}
			if got := required.Operations[0].Requirement; got != OperationAdmissionSourceDrain {
				t.Fatalf("%s source owner requirement = %q, want %q",
					tc.source, got, OperationAdmissionSourceDrain)
			}
		})
	}
}

func TestSplitPlanForAdmissionRequiresIncomingTransferGrowAndDefersUnrelatedGrow(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Base: &CompleteSnapshot{DomainByRel: map[string]DomainID{
			"reclaim/required": DomainReclaim,
			"primary/history":  DomainPrimary,
		}},
		TransferGraph: map[DomainID]map[DomainID]machine.CPUSet{
			DomainPrimary: {DomainReclaim: machine.NewCPUSet(2)},
		},
		Operations: []PlanOperation{
			{
				Rel:             "reclaim/required",
				Direction:       WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(3)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
			},
			{
				Rel:             "primary/history",
				Direction:       WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(4)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(4, 5)},
			},
		},
	}

	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if len(required.Operations) != 1 || required.Operations[0].Rel != "reclaim/required" {
		t.Fatalf("required operations = %+v, want only incoming transfer grow", required.Operations)
	}
	if len(deferred.Operations) != 1 || deferred.Operations[0].Rel != "primary/history" {
		t.Fatalf("deferred operations = %+v, want only unrelated grow", deferred.Operations)
	}
}

func TestSplitPlanForAdmissionScopesPendingGrowToRequiredRelations(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Kind: PhaseExpand,
		Operations: []PlanOperation{
			{
				Rel: "kubepods", Direction: WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 6, 7)},
			},
			{
				Rel: "kubepods/burstable", Direction: WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 6, 7)},
			},
			{
				Rel: "kubepods/besteffort", Direction: WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 6, 7)},
			},
			{
				Rel: "kubepods/burstable/pod-old", Direction: WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 6, 7)},
			},
		},
	}

	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{
		PendingRequiredByRel: map[string]machine.CPUSet{
			"kubepods":           machine.NewCPUSet(6, 7),
			"kubepods/burstable": machine.NewCPUSet(6, 7),
		},
	})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if got := operationRels(required.Operations); !reflect.DeepEqual(got, []string{
		"kubepods", "kubepods/burstable",
	}) {
		t.Fatalf("required rels = %v, want scoped ancestors", got)
	}
	if got := operationRels(deferred.Operations); !reflect.DeepEqual(got, []string{
		"kubepods/besteffort", "kubepods/burstable/pod-old",
	}) {
		t.Fatalf("deferred rels = %v, want unrelated siblings", got)
	}
}

func operationRels(operations []PlanOperation) []string {
	rels := make([]string, 0, len(operations))
	for _, operation := range operations {
		rels = append(rels, operation.Rel)
	}
	return rels
}

func TestFinalAdmissionTargetPreservesCanonicalTargetOverIntermediateDrainOperation(t *testing.T) {
	t.Parallel()

	const rel = "reclaimed-0"
	canonicalCPUs := machine.NewCPUSet(
		0, 1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16, 17,
	)
	if got := canonicalCPUs.Size(); got != 18 {
		t.Fatalf("canonical CPUs size = %d, want 18", got)
	}
	intermediateDrainCPUs := machine.NewCPUSet(0, 1)
	if got := intermediateDrainCPUs.Size(); got != 2 {
		t.Fatalf("intermediate drain CPUs size = %d, want 2", got)
	}
	canonicalTargets := map[string]CPUSetTarget{
		rel:       {CPUs: canonicalCPUs, Mems: "0-1"},
		"primary": {CPUs: machine.NewCPUSet(18, 19), Mems: "0"},
	}
	plan := &PhasePlan{
		TargetByRel: map[string]CPUSetTarget{
			rel:       {CPUs: canonicalCPUs, Mems: "0-1"},
			"primary": {CPUs: machine.NewCPUSet(18, 19), Mems: "0"},
		},
		Operations: []PlanOperation{{
			Rel:             rel,
			Direction:       WriteShrink,
			ExpectedCurrent: CPUSetTarget{CPUs: canonicalCPUs, Mems: "0-1"},
			Target:          CPUSetTarget{CPUs: intermediateDrainCPUs, Mems: "0"},
		}},
	}

	required, _, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{
		PendingCPUSet: machine.NewCPUSet(2),
	})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if len(required.Operations) != 1 {
		t.Fatalf("required operations = %+v, want intermediate drain operation", required.Operations)
	}
	if got := required.Operations[0].Target.CPUs.Size(); got != 2 {
		t.Fatalf("intermediate drain target size = %d, want 2", got)
	}

	finalTargets := finalAdmissionTarget(required)
	if !reflect.DeepEqual(finalTargets, required.TargetByRel) {
		t.Fatalf("final admission targets = %+v, want required canonical targets %+v",
			finalTargets, required.TargetByRel)
	}
	if !reflect.DeepEqual(finalTargets, canonicalTargets) {
		t.Fatalf("final admission targets = %+v, want original canonical targets %+v",
			finalTargets, canonicalTargets)
	}
	got, ok := finalTargets[rel]
	if !ok {
		t.Fatalf("finalAdmissionTarget() did not find canonical target for %q", rel)
	}
	if got.CPUs.Size() != 18 || !got.CPUs.Equals(canonicalCPUs) {
		t.Fatalf("final admission CPUs = %s (size %d), want canonical %s (size 18)",
			got.CPUs.String(), got.CPUs.Size(), canonicalCPUs.String())
	}
	if got.Mems != "0-1" {
		t.Fatalf("final admission mems = %q, want canonical %q", got.Mems, "0-1")
	}
}

func TestRequiredCPUSetByRelFromNodeSpecsFreezesCanonicalReclaimTargets(t *testing.T) {
	t.Parallel()

	specs := []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3)},
		{Rel: "reclaim/numa-0", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2, 3)},
	}

	required := RequiredCPUSetByRelFromNodeSpecs(specs)
	specs[2].CPUs = machine.NewCPUSet(4, 5)

	if len(required) != 1 || !required["reclaim/numa-0"].Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("required targets = %+v, want frozen reclaim/numa-0=2-3", required)
	}
	if _, ok := required["reclaim"]; ok {
		t.Fatalf("aggregate reclaim rel must not become a required NUMA-floor owner")
	}
}

func TestSplitPlanForAdmissionPromotesAncestorGrowRequiredByChild(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Base: &CompleteSnapshot{
			Entries: map[string]EntryState{
				"primary":           {CPUs: machine.NewCPUSet(2, 3)},
				"primary/container": {CPUs: machine.NewCPUSet(2, 3)},
			},
			DomainByRel: map[string]DomainID{
				"primary":           DomainPrimary,
				"primary/container": DomainReclaim,
			},
		},
		TransferGraph: map[DomainID]map[DomainID]machine.CPUSet{
			DomainPrimary: {DomainReclaim: machine.NewCPUSet(1)},
		},
		Operations: []PlanOperation{
			{
				Rel:             "primary",
				Direction:       WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(1, 2, 3)},
			},
			{
				Rel:             "primary/container",
				ParentRel:       "primary",
				Direction:       WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(1, 2, 3)},
			},
		},
	}

	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if len(required.Operations) != 2 || len(deferred.Operations) != 0 {
		t.Fatalf("split counts required=%d deferred=%d, want 2/0",
			len(required.Operations), len(deferred.Operations))
	}
	if got := required.Operations[0]; got.Rel != "primary" ||
		got.Requirement != OperationAdmissionAncestorGrow {
		t.Fatalf("first required operation = %+v, want promoted parent grow", got)
	}
	if got := required.Operations[1].Rel; got != "primary/container" {
		t.Fatalf("second required operation = %q, want child grow", got)
	}
}

func TestSplitPlanForAdmissionRejectsChildGrowNotCoveredByParent(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Base: &CompleteSnapshot{
			Entries: map[string]EntryState{
				"primary":           {CPUs: machine.NewCPUSet(2, 3)},
				"primary/container": {CPUs: machine.NewCPUSet(2, 3)},
			},
			DomainByRel: map[string]DomainID{
				"primary/container": DomainReclaim,
			},
		},
		TransferGraph: map[DomainID]map[DomainID]machine.CPUSet{
			DomainPrimary: {DomainReclaim: machine.NewCPUSet(1)},
		},
		Operations: []PlanOperation{{
			Rel:             "primary/container",
			ParentRel:       "primary",
			Direction:       WriteGrow,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(1, 2, 3)},
		}},
	}

	_, _, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
	if err == nil {
		t.Fatal("SplitPlanForAdmission() error = nil, want uncovered parent rejection")
	}
}

func TestSplitPlanForAdmissionPromotesReplacementShrinkRequiredByGrow(t *testing.T) {
	t.Parallel()

	plan := &PhasePlan{
		Base: &CompleteSnapshot{
			Entries: map[string]EntryState{
				"primary":           {CPUs: machine.NewCPUSet(0, 1, 2, 3)},
				"primary/container": {CPUs: machine.NewCPUSet(1, 2)},
			},
			DomainByRel: map[string]DomainID{
				"primary/container": DomainReclaim,
			},
		},
		TransferGraph: map[DomainID]map[DomainID]machine.CPUSet{
			DomainPrimary: {DomainReclaim: machine.NewCPUSet(3)},
		},
		Operations: []PlanOperation{
			{
				Rel:             "primary/container",
				ParentRel:       "primary",
				Direction:       WriteShrink,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(1, 2)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(2)},
			},
			{
				Rel:             "primary/container",
				ParentRel:       "primary",
				Direction:       WriteGrow,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(2)},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(2, 3)},
			},
		},
	}

	required, deferred, err := SplitPlanForAdmission(plan, AdmissionSafetyInput{})
	if err != nil {
		t.Fatalf("SplitPlanForAdmission() error = %v", err)
	}
	if len(required.Operations) != 2 || len(deferred.Operations) != 0 {
		t.Fatalf("split counts required=%d deferred=%d, want replacement 2/0",
			len(required.Operations), len(deferred.Operations))
	}
	if got := required.Operations[0]; got.Direction != WriteShrink ||
		got.Requirement != OperationAdmissionSafetyRepair {
		t.Fatalf("first required operation = %+v, want replacement shrink prerequisite", got)
	}
	if got := required.Operations[1].Direction; got != WriteGrow {
		t.Fatalf("second required direction = %q, want grow", got)
	}
}

func TestTransferGraphSupportsThreeDomainCycleAndDrainsBeforeExpand(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(2), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "c", Domain: "c", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
		"c": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(2)},
	}, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1), "c": machine.NewCPUSet(2),
	})

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"a": machine.NewCPUSet(2), "b": machine.NewCPUSet(0), "c": machine.NewCPUSet(1),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got, want := plan.TransferGraph["a"]["b"], machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("a->b = %s, want %s", got.String(), want.String())
	}
	if got, want := plan.TransferGraph["b"]["c"], machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("b->c = %s, want %s", got.String(), want.String())
	}
	if got, want := plan.TransferGraph["c"]["a"], machine.NewCPUSet(2); !got.Equals(want) {
		t.Fatalf("c->a = %s, want %s", got.String(), want.String())
	}
	for _, op := range plan.Operations {
		if op.Direction != WriteShrink {
			t.Fatalf("cycle operation %q direction = %q, want shrink to verified-unowned", op.Rel, op.Direction)
		}
	}
}

func TestPhaseDrainDoesNotGrowExplicitDynamicLeafBeforeParentExpand(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), ControlledRoot: true, TrustAnchor: true},
		{Rel: "tiger", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), ControlledRoot: true, TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":                    {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(), Mems: "0"},
		"tiger":                      {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"tiger/http2p.agent.service": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(),
		DomainReclaim: machine.NewCPUSet(0, 1),
	})
	snapshot.Children = map[string][]ChildRef{"tiger": {{Name: "http2p.agent.service"}}}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "tiger": DomainReclaim, "tiger/http2p.agent.service": DomainReclaim,
	}

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind:     PhaseDrain,
		DAG:      dag,
		Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"tiger":   machine.NewCPUSet(2, 3),
		},
		DynamicByRel: map[string]machine.CPUSet{
			"tiger/http2p.agent.service": machine.NewCPUSet(3),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	target := plan.TargetByRel["tiger/http2p.agent.service"].CPUs
	if !target.IsSubsetOf(snapshot.Entries["tiger/http2p.agent.service"].CPUs) {
		t.Fatalf("drain target for explicit dynamic leaf grew from %s to %s; entering CPUs must wait for expand phase",
			snapshot.Entries["tiger/http2p.agent.service"].CPUs.String(), target.String())
	}
}

func TestV2PlannerEmptyTargetNormalConvergenceUsesConfiguredCPUs(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {
			Identity: CgroupIdentity{Inode: 1},
			CPUs:     machine.MustParse("0-3"), ConfiguredCPUs: machine.NewCPUSet(), Mems: "0",
		},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-3")})
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet()},
		AllowedCPUs:  machine.MustParse("0-3"), AllowEmptyTarget: true,
		Capabilities: cgroupV2Policy.capabilities(true),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	target := plan.TargetByRel["primary"].CPUs
	if !target.IsEmpty() {
		t.Fatalf("planner target = %s, want empty configured target", target.String())
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("configured CPUs already empty, operations = %+v, want none", plan.Operations)
	}

	report, err := buildConvergenceReport(
		snapshot, dag,
		map[string]machine.CPUSet{"primary": target},
		map[string]string{"primary": "0"},
		map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet()},
		machine.MustParse("0-3"), cgroupV2Policy.capabilities(true), true,
	)
	if err != nil {
		t.Fatalf("buildConvergenceReport: %v", err)
	}
	if len(report.NonConvergedTargets) != 0 {
		t.Fatalf("v2 empty configured target should not report an effective CPU mismatch: %+v", report)
	}

	snapshot.Entries["primary"] = EntryState{
		Identity: CgroupIdentity{Inode: 1},
		CPUs:     machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("1-2"), Mems: "0",
	}
	for _, tc := range []struct {
		name         string
		capabilities HierarchyCapabilities
	}{
		{name: "v2", capabilities: cgroupV2Policy.capabilities(true)},
		{name: "v1", capabilities: cgroupV1Policy.capabilities(true)},
	} {
		tc := tc
		t.Run(tc.name+" non-empty target keeps effective semantics", func(t *testing.T) {
			t.Parallel()
			report, reportErr := buildConvergenceReport(
				snapshot, dag,
				map[string]machine.CPUSet{"primary": machine.MustParse("0-3")},
				map[string]string{"primary": "0"},
				map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-3")},
				machine.MustParse("0-3"), tc.capabilities, tc.capabilities.EmptyConfiguredCPUSet,
			)
			if reportErr != nil {
				t.Fatalf("buildConvergenceReport: %v", reportErr)
			}
			if !report.FullyConverged {
				t.Fatalf("non-empty target must use effective CPUs, report=%+v", report)
			}
		})
	}
}

func TestMaterializeTargetsClassifiesOnlyV1EmptyReclaimBucketsAsDormant(t *testing.T) {
	t.Parallel()

	allCPUs := machine.MustParse("0-7")
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/sibling", ParentRel: "reclaim", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0-3"},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-1")}, Metadata: map[string]string{"numa": "0"}},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("2-3")}, Metadata: map[string]string{"numa": "1"}},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("4-5")}, Metadata: map[string]string{"numa": "2"}},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("6-7")}, Metadata: map[string]string{"numa": "3"}},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":         {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"), Mems: "0-3"},
		"reclaim":         {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("4-7"), Mems: "0-3"},
		"reclaim/sibling": {Identity: CgroupIdentity{Inode: 7}, CPUs: machine.MustParse("4-7"), Mems: "0-3"},
		"reclaim/numa-0":  {Identity: CgroupIdentity{Inode: 3}, CPUs: allCPUs, Mems: "0", Activity: CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}},
		"reclaim/numa-1":  {Identity: CgroupIdentity{Inode: 4}, CPUs: allCPUs, Mems: "1", Activity: CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}},
		"reclaim/numa-2":  {Identity: CgroupIdentity{Inode: 5}, CPUs: machine.MustParse("4-5"), Mems: "2"},
		"reclaim/numa-3":  {Identity: CgroupIdentity{Inode: 6}, CPUs: machine.MustParse("6-7"), Mems: "3"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3"),
		DomainReclaim: allCPUs,
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
		"reclaim/sibling": DomainReclaim,
		"reclaim/numa-0":  DomainReclaim, "reclaim/numa-1": DomainReclaim,
		"reclaim/numa-2": DomainReclaim, "reclaim/numa-3": DomainReclaim,
	}
	snapshot.Children = map[string][]ChildRef{
		"reclaim/numa-0": nil,
		"reclaim/numa-1": nil,
	}
	v1 := materializeTargets(dag, snapshot, false, desiredTargets(dag))
	if _, dormant := v1.DormantRels["reclaim/sibling"]; dormant {
		t.Fatal("empty reclaim sibling was classified dormant; only reclaim NUMA buckets may be dormant")
	}
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		if _, dormant := v1.DormantRels[rel]; !dormant {
			t.Fatalf("v1 rel %q was not classified dormant", rel)
		}
		if got := v1.PhysicalByRel[rel]; !got.Equals(allCPUs) {
			t.Fatalf("v1 dormant rel %q physical target = %s, want observed envelope %s", rel, got.String(), allCPUs.String())
		}
		if !v1.SemanticByRel[rel].IsEmpty() {
			t.Fatalf("v1 dormant rel %q semantic target = %s, want empty", rel, v1.SemanticByRel[rel].String())
		}
	}
	// Semantic reclaim ownership lives in OwnershipByRel: the dormant envelope
	// observations must not be promoted into it. DomainUnion is now the physical
	// evidence union and is checked separately for closure.
	if got, want := v1.Snapshot.OwnershipByRel["reclaim"], machine.MustParse("4-7"); !got.Equals(want) {
		t.Fatalf("v1 reclaim semantic ownership = %s, want %s without dormant observations", got.String(), want.String())
	}
	if got, want := v1.Snapshot.DomainUnion[DomainReclaim], allCPUs; !got.Equals(want) {
		t.Fatalf("v1 reclaim physical evidence union = %s, want observed entries %s", got.String(), want.String())
	}
	if got, want := v1.PhysicalByRel["primary"], machine.MustParse("0-3"); !got.Equals(want) {
		t.Fatalf("v1 primary target = %s, want %s without dormant deduction", got.String(), want.String())
	}
	for _, rel := range []string{"reclaim/numa-2", "reclaim/numa-3"} {
		if _, dormant := v1.DormantRels[rel]; dormant {
			t.Fatalf("active v1 rel %q was classified dormant", rel)
		}
		if got, want := v1.PhysicalByRel[rel], dag.index[rel].CPUs; !got.Equals(want) {
			t.Fatalf("active v1 rel %q physical target = %s, want %s", rel, got.String(), want.String())
		}
	}

	v2 := materializeTargets(dag, snapshot, true, desiredTargets(dag))
	if len(v2.DormantRels) != 0 {
		t.Fatalf("v2 dormant rels = %v, want none", v2.DormantRels)
	}
	if got := v2.PhysicalByRel["reclaim/numa-0"]; !got.IsEmpty() {
		t.Fatalf("v2 empty physical target = %s, want unchanged empty target", got.String())
	}
	if got := v2.Snapshot.DomainUnion[DomainReclaim]; !got.Equals(allCPUs) {
		t.Fatalf("v2 reclaim ownership = %s, want observed %s", got.String(), allCPUs.String())
	}
}

func TestMaterializeTargetsKeepsLegalV1FourNUMAParentChildContainment(t *testing.T) {
	t.Parallel()

	allCPUs := machine.MustParse("0-7")
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{
			Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-1")},
		},
		{
			Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("2-3")},
		},
		{
			Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("4-5")},
		},
		{
			Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3",
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("6-7")},
		},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	snapshot := planSnapshot(map[string]EntryState{
		"primary":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"), Mems: "0-3"},
		"reclaim":        {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0-3"},
		"reclaim/numa-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: allCPUs, Mems: "0", Activity: inactive},
		"reclaim/numa-1": {Identity: CgroupIdentity{Inode: 4}, CPUs: allCPUs, Mems: "1", Activity: inactive},
		"reclaim/numa-2": {Identity: CgroupIdentity{Inode: 5}, CPUs: machine.MustParse("4-5"), Mems: "2"},
		"reclaim/numa-3": {Identity: CgroupIdentity{Inode: 6}, CPUs: machine.MustParse("6-7"), Mems: "3"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3"),
		DomainReclaim: allCPUs,
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
		"reclaim/numa-0": DomainReclaim, "reclaim/numa-1": DomainReclaim,
		"reclaim/numa-2": DomainReclaim, "reclaim/numa-3": DomainReclaim,
	}
	snapshot.Children = map[string][]ChildRef{
		"reclaim/numa-0": nil,
		"reclaim/numa-1": nil,
	}

	materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))

	if got, want := materialized.SemanticByRel["reclaim"], machine.MustParse("4-7"); !got.Equals(want) {
		t.Fatalf("semantic reclaim target = %s, want %s", got.String(), want.String())
	}
	// Semantic ownership of the reclaim parent (ancestor of dormant buckets) is
	// recorded in OwnershipByRel; DomainUnion stays the physical entry union.
	if got, want := materialized.Snapshot.OwnershipByRel["reclaim"], machine.MustParse("4-7"); !got.Equals(want) {
		t.Fatalf("reclaim semantic ownership = %s, want semantic ownership %s", got.String(), want.String())
	}
	if got, want := materialized.Snapshot.DomainUnion[DomainReclaim], allCPUs; !got.Equals(want) {
		t.Fatalf("reclaim physical evidence union = %s, want observed entries %s", got.String(), want.String())
	}
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		if got := materialized.PhysicalByRel[rel]; !got.Equals(allCPUs) {
			t.Fatalf("physical dormant target %q = %s, want observed envelope %s", rel, got.String(), allCPUs.String())
		}
	}
	if got := materialized.PhysicalByRel["reclaim"]; !got.Equals(allCPUs) {
		t.Fatalf("physical reclaim parent target = %s, want child-containing envelope %s", got.String(), allCPUs.String())
	}
	if overlap := desiredDomainUnions(dag, materialized.SemanticByRel)[DomainPrimary].
		Intersection(desiredDomainUnions(dag, materialized.SemanticByRel)[DomainReclaim]); !overlap.IsEmpty() {
		t.Fatalf("semantic ownership overlap = %s, want none", overlap.String())
	}
}

func TestMaterializeTargetsRequiresInactivePhysicalSubtreeProof(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("2-3"), Mems: "0", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0"},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	for _, tc := range []struct {
		name    string
		entries map[string]EntryState
		dormant bool
	}{
		{
			name: "childless leaf inactive",
			entries: map[string]EntryState{
				"reclaim":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("2-3"), Activity: inactive},
				"reclaim/numa-0": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"), Activity: inactive},
			},
			dormant: true,
		},
		{
			name: "inactive bucket still has child",
			entries: map[string]EntryState{
				"reclaim":                {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("2-3"), Activity: inactive},
				"reclaim/numa-0":         {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"), Activity: inactive},
				"reclaim/numa-0/runtime": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse("0-1"), Activity: inactive},
			},
		},
		{
			name: "bucket has tasks",
			entries: map[string]EntryState{
				"reclaim":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("2-3"), Activity: inactive},
				"reclaim/numa-0": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"), Activity: CgroupActivity{CgroupProcsEmpty: true}},
			},
		},
		{
			name: "dynamic descendant has cgroup procs",
			entries: map[string]EntryState{
				"reclaim":                {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("2-3"), Activity: inactive},
				"reclaim/numa-0":         {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"), Activity: inactive},
				"reclaim/numa-0/runtime": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse("0-1"), Activity: CgroupActivity{TasksEmpty: true}},
			},
		},
		{
			name: "activity was not proved",
			entries: map[string]EntryState{
				"reclaim":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("2-3"), Activity: inactive},
				"reclaim/numa-0": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3")},
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot := planSnapshot(tc.entries, map[DomainID]machine.CPUSet{DomainReclaim: machine.MustParse("0-3")})
			snapshot.DomainByRel = make(map[string]DomainID, len(tc.entries))
			for rel := range tc.entries {
				snapshot.DomainByRel[rel] = DomainReclaim
			}
			snapshot.Children = map[string][]ChildRef{
				"reclaim/numa-0": nil,
			}
			if _, hasRuntime := tc.entries["reclaim/numa-0/runtime"]; hasRuntime {
				snapshot.Children["reclaim/numa-0"] = []ChildRef{{
					Name: "runtime", Identity: tc.entries["reclaim/numa-0/runtime"].Identity,
				}}
				snapshot.Children["reclaim/numa-0/runtime"] = nil
			}
			got := materializeTargets(dag, snapshot, false, desiredTargets(dag))
			_, dormant := got.DormantRels["reclaim/numa-0"]
			if dormant != tc.dormant {
				t.Fatalf("dormant = %v, want %v", dormant, tc.dormant)
			}
		})
	}
}

func TestDormantReclaimBucketRequiresChildlessLeafAndBindsObservedCPUSet(t *testing.T) {
	t.Parallel()

	const rel = "reclaim/numa-0"
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("2-3")},
		{Rel: rel, ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet()},
	})
	inactiveLeaf := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	base := planSnapshot(map[string]EntryState{
		"reclaim": {Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.MustParse("2-3")},
		rel: {
			Identity: CgroupIdentity{Device: 1, Inode: 2},
			CPUs:     machine.MustParse("0-3"),
			Activity: inactiveLeaf,
		},
	}, map[DomainID]machine.CPUSet{DomainReclaim: machine.MustParse("0-3")})
	base.DomainByRel = map[string]DomainID{"reclaim": DomainReclaim, rel: DomainReclaim}
	base.Children = map[string][]ChildRef{rel: nil}

	materialized := materializeTargets(dag, base, false, desiredTargets(dag))
	proof, dormant := materialized.DormantProofs[rel]
	if !dormant {
		t.Fatal("childless inactive reclaim NUMA bucket was not dormant")
	}
	if !proof.ObservedCPUs.Equals(machine.MustParse("0-3")) {
		t.Fatalf("proof cpuset = %s, want observed 0-3", proof.ObservedCPUs.String())
	}

	withChild := *base
	withChild.Children = map[string][]ChildRef{
		rel: {{Name: "runtime", Identity: CgroupIdentity{Device: 1, Inode: 3}}},
	}
	entry := withChild.Entries[rel]
	entry.Activity.Childless = false
	withChild.Entries = map[string]EntryState{"reclaim": base.Entries["reclaim"], rel: entry}
	if got := materializeTargets(dag, &withChild, false, desiredTargets(dag)); len(got.DormantRels) != 0 {
		t.Fatalf("bucket with child classified dormant: %v", got.DormantRels)
	}

	fresh := *base
	fresh.Entries = map[string]EntryState{"reclaim": base.Entries["reclaim"], rel: base.Entries[rel]}
	changed := fresh.Entries[rel]
	changed.CPUs = machine.MustParse("0-2")
	fresh.Entries[rel] = changed
	if err := validateDormantProofs(&fresh, materialized.DormantProofs); !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("cpuset drift validation error = %v, want ErrCoordinatorPlanStale", err)
	}
}

func TestDormantReclaimBucketRequiresNoNewSemanticPrimaryOwnership(t *testing.T) {
	t.Parallel()

	const rel = "reclaim/numa-0"
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-1")},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("2-3")},
		{
			Rel: rel, ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(),
			Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-3")},
		},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-1")},
		"reclaim": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3")},
		rel:       {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse("0-3"), Activity: inactive},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-1"),
		DomainReclaim: machine.MustParse("0-3"),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim, rel: DomainReclaim,
	}
	snapshot.Children = map[string][]ChildRef{rel: nil}

	materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))
	if _, dormant := materialized.DormantRels[rel]; dormant {
		t.Fatal("bucket was dormant even though observed CPUs inside its upper bound include non-primary semantic ownership")
	}
}

func TestMaterializeTargetsRejectsDormantProofTruncatedByTraversalBoundary(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("2-3"), Mems: "0", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0"},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true}
	driver := newFakeHierarchyDriver()
	driver.add("reclaim", CgroupIdentity{Device: 1, Inode: 1}, "2-3", "0")
	driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0")
	driver.add("reclaim/numa-0/runtime", CgroupIdentity{Device: 1, Inode: 3}, "0-3", "0")
	driver.add("reclaim/numa-0/runtime/busy", CgroupIdentity{Device: 1, Inode: 4}, "0-1", "0")
	driver.nodes["reclaim"].activity = inactive
	driver.nodes["reclaim/numa-0"].activity = inactive
	driver.nodes["reclaim/numa-0/runtime"].activity = inactive
	driver.nodes["reclaim/numa-0/runtime/busy"].activity = CgroupActivity{
		TasksEmpty:       false,
		CgroupProcsEmpty: false,
	}

	snapshot, err := buildCompleteSnapshot(
		context.Background(),
		driver,
		dag,
		SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           []string{"reclaim/numa-0"},
			CollectDormantActivity: true,
		},
		NewBudgetTracker(ConvergenceBudget{}),
		map[string]struct{}{"reclaim/numa-0/runtime": {}},
		nil,
	)
	if err != nil {
		t.Fatalf("buildCompleteSnapshot() error = %v", err)
	}
	if _, scanned := snapshot.Entries["reclaim/numa-0/runtime"]; scanned {
		t.Fatal("snapshot crossed traversal boundary")
	}

	materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))
	if _, dormant := materialized.DormantRels["reclaim/numa-0"]; dormant {
		t.Fatal("truncated subtree produced a dormant proof despite a busy descendant behind the traversal boundary")
	}
}

func TestDormantProofRevalidationRejectsActivityIdentityAndChildChanges(t *testing.T) {
	t.Parallel()

	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	base := &CompleteSnapshot{Entries: map[string]EntryState{
		"reclaim/numa-0": {Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.MustParse("0-3"), Activity: inactive},
	}, Children: map[string][]ChildRef{
		"reclaim/numa-0": nil,
	}}
	proof, ok := dormantLeafProof(base, "reclaim/numa-0")
	if !ok {
		t.Fatal("dormantSubtreeProof() rejected inactive leaf")
	}
	if err := validateDormantProofs(base, map[string]DormantLeafProof{"reclaim/numa-0": proof}); err != nil {
		t.Fatalf("validateDormantProofs(base) error = %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]EntryState)
	}{
		{
			name: "root becomes active",
			mutate: func(entries map[string]EntryState) {
				entry := entries["reclaim/numa-0"]
				entry.Activity.TasksEmpty = false
				entries["reclaim/numa-0"] = entry
			},
		},
		{
			name: "leaf ABA replacement",
			mutate: func(entries map[string]EntryState) {
				entry := entries["reclaim/numa-0"]
				entry.Identity.Inode++
				entries["reclaim/numa-0"] = entry
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fresh := &CompleteSnapshot{
				Entries:  make(map[string]EntryState, len(base.Entries)),
				Children: base.Children,
			}
			for rel, entry := range base.Entries {
				fresh.Entries[rel] = entry
			}
			tc.mutate(fresh.Entries)
			err := validateDormantProofs(fresh, map[string]DormantLeafProof{"reclaim/numa-0": proof})
			if !errors.Is(err, ErrCoordinatorPlanStale) {
				t.Fatalf("validateDormantProofs() error = %v, want ErrCoordinatorPlanStale", err)
			}
		})
	}
}

func TestDormantProofRevalidationRejectsMissingFreshChildClosure(t *testing.T) {
	t.Parallel()

	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	base := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"reclaim/numa-0": {Identity: CgroupIdentity{Device: 1, Inode: 1}, Activity: inactive},
		},
		Children: map[string][]ChildRef{
			"reclaim/numa-0": nil,
		},
	}
	proof, ok := dormantLeafProof(base, "reclaim/numa-0")
	if !ok {
		t.Fatal("dormantSubtreeProof() rejected complete inactive leaf")
	}
	fresh := &CompleteSnapshot{
		Entries:  base.Entries,
		Children: map[string][]ChildRef{},
	}
	err := validateDormantProofs(fresh, map[string]DormantLeafProof{"reclaim/numa-0": proof})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("validateDormantProofs() error = %v, want stale truncated closure rejection", err)
	}
}

func TestV1DormantReclaimBucketsProduceNoWrites(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("0-1")}, Metadata: map[string]string{"numa": "0"}},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("2-3")}, Metadata: map[string]string{"numa": "1"}},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("4-5")}, Metadata: map[string]string{"numa": "2"}},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3", Constraint: TopologyConstraint{CPUUpperBound: machine.MustParse("6-7")}, Metadata: map[string]string{"numa": "3"}},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"), Mems: "0-3"},
		"reclaim":        {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("4-7"), Mems: "0-3"},
		"reclaim/numa-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse("0-7"), Mems: "0", Activity: CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}},
		"reclaim/numa-1": {Identity: CgroupIdentity{Inode: 4}, CPUs: machine.MustParse("0-7"), Mems: "1", Activity: CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}},
		"reclaim/numa-2": {Identity: CgroupIdentity{Inode: 5}, CPUs: machine.MustParse("4-5"), Mems: "2"},
		"reclaim/numa-3": {Identity: CgroupIdentity{Inode: 6}, CPUs: machine.MustParse("6-7"), Mems: "3"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3"),
		DomainReclaim: machine.MustParse("0-7"),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim,
		"reclaim/numa-0": DomainReclaim, "reclaim/numa-1": DomainReclaim,
		"reclaim/numa-2": DomainReclaim, "reclaim/numa-3": DomainReclaim,
	}
	snapshot.Children = map[string][]ChildRef{
		"reclaim/numa-0": nil,
		"reclaim/numa-1": nil,
	}
	materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
		4: {NUMANodeID: 2}, 5: {NUMANodeID: 2},
		6: {NUMANodeID: 3}, 7: {NUMANodeID: 3},
	}
	for _, phase := range []PhaseKind{PhaseDrain, PhaseExpand} {
		plan, err := BuildPhasePlan(PhasePlanInput{
			Kind: phase, DAG: dag, Snapshot: materialized.Snapshot,
			DesiredByRel:  materialized.PhysicalByRel,
			SemanticByRel: materialized.SemanticByRel,
			DormantRels:   materialized.DormantRels,
			AllowedCPUs:   machine.MustParse("0-7"),
			Capabilities:  cgroupV1Policy.capabilities(true),
			CPUDetails:    cpuDetails,
			Budget:        NewBudgetTracker(ConvergenceBudget{}),
		})
		if err != nil {
			t.Fatalf("BuildPhasePlan(%s): %v", phase, err)
		}
		for rel, want := range map[string]machine.CPUSet{
			"primary": machine.MustParse("0-3"),
			"reclaim": func() machine.CPUSet {
				if phase == PhaseExpand {
					return machine.MustParse("0-7")
				}
				return machine.MustParse("4-7")
			}(),
		} {
			if got := plan.TargetByRel[rel].CPUs; !got.Equals(want) {
				t.Fatalf("%s target for %q = %s, want %s", phase, rel, got.String(), want.String())
			}
		}
		for rel := range materialized.DormantRels {
			if _, targeted := plan.TargetByRel[rel]; targeted {
				t.Fatalf("%s dormant rel %q received a physical plan target", phase, rel)
			}
		}
		for _, operation := range plan.Operations {
			if _, dormant := materialized.DormantRels[operation.Rel]; dormant {
				t.Fatalf("%s dormant rel %q received operation %+v", phase, operation.Rel, operation)
			}
		}
	}
}

func TestV2PlannerNonEmptyConfiguredCPUsEmitsOneClearOperation(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {
			Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"),
			ConfiguredCPUs: machine.MustParse("0-3"), Mems: "0",
		},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-3")})

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet()},
		AllowedCPUs:  machine.MustParse("0-3"), AllowEmptyTarget: true,
		Capabilities: cgroupV2Policy.capabilities(true),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("operations = %+v, want one configured clear", plan.Operations)
	}
	operation := plan.Operations[0]
	if operation.Direction != WriteShrink || !operation.Target.CPUs.IsEmpty() ||
		!operation.ExpectedCurrent.CPUs.Equals(machine.MustParse("0-3")) {
		t.Fatalf("clear operation = %+v, want one shrink from configured 0-3 to empty", operation)
	}
}

func TestIncludeMaterializedDynamicConvergenceUsesConfiguredProofForV2EmptyTarget(t *testing.T) {
	t.Parallel()

	snapshot := planSnapshot(map[string]EntryState{
		"primary/leaf": {
			Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"),
			ConfiguredCPUs: machine.NewCPUSet(), Mems: "0",
		},
	}, nil)
	report := ConvergenceReport{FullyConverged: true}
	includeMaterializedDynamicConvergence(
		&report, snapshot, map[string]machine.CPUSet{"primary/leaf": machine.NewCPUSet()},
		cgroupV2Policy.capabilities(true),
	)
	if !report.FullyConverged || len(report.NonConvergedTargets) != 0 {
		t.Fatalf("dynamic empty configured target must converge, report=%+v", report)
	}
}

func TestPhaseExpandOrdersParentGrowBeforeExplicitDynamicLeafGrow(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), ControlledRoot: true, TrustAnchor: true},
		{Rel: "tiger", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), ControlledRoot: true, TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":                    {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(), Mems: "0"},
		"tiger":                      {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"tiger/http2p.agent.service": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(),
		DomainReclaim: machine.NewCPUSet(0, 1),
	})
	snapshot.Children = map[string][]ChildRef{"tiger": {{Name: "http2p.agent.service"}}}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "tiger": DomainReclaim, "tiger/http2p.agent.service": DomainReclaim,
	}
	input := PhasePlanInput{
		Kind:     PhaseExpand,
		DAG:      dag,
		Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"tiger":   machine.NewCPUSet(2, 3),
		},
		DynamicByRel: map[string]machine.CPUSet{
			"tiger/http2p.agent.service": machine.NewCPUSet(3),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	}
	input.Witnesses = []ReleaseWitness{
		NewReleaseWitness(canonicalConvergenceID(input), DomainPrimary, DomainReclaim, machine.NewCPUSet(2, 3), snapshot),
	}

	plan, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}

	parentGrow, childGrow := -1, -1
	for i, op := range plan.Operations {
		if op.Direction != WriteGrow {
			continue
		}
		switch op.Rel {
		case "tiger":
			parentGrow = i
		case "tiger/http2p.agent.service":
			childGrow = i
			if parent := plan.TargetByRel["tiger"].CPUs; !op.Target.CPUs.IsSubsetOf(parent) {
				t.Fatalf("child grow target %s is outside parent target %s", op.Target.CPUs.String(), parent.String())
			}
		}
	}
	if parentGrow < 0 || childGrow < 0 {
		t.Fatalf("missing parent/child grow operations: parent=%d child=%d targets=%+v ops=%+v", parentGrow, childGrow, plan.TargetByRel, plan.Operations)
	}
	if parentGrow > childGrow {
		t.Fatalf("parent grow operation index=%d must precede child grow index=%d ops=%+v", parentGrow, childGrow, plan.Operations)
	}
}

func TestMaxCPUsPerDrainRound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		ratio float64
		want  int
	}{
		{name: "unlimited", ratio: 0, want: 0},
		{name: "minimum two", ratio: 0.01, want: 2},
		{name: "round down to even", ratio: 0.10, want: 8},
		{name: "quarter", ratio: 0.25, want: 24},
		{name: "all", ratio: 1, want: 96},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := maxCPUsPerDrainRound(96, tc.ratio); got != tc.want {
				t.Fatalf("maxCPUsPerDrainRound(96, %v) = %d, want %d", tc.ratio, got, tc.want)
			}
		})
	}
}

func TestStableDrainBatchUsesRatioAndBestEffortTopologySelection(t *testing.T) {
	t.Parallel()

	details := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		4: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		5: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		6: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		7: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
	}
	candidates := machine.NewCPUSet(0, 1, 2, 3, 4)

	full := stableDrainBatch(candidates, details, DrainSelectionPolicy{})
	if !full.Equals(candidates) {
		t.Fatalf("full drain selection = %s, want %s", full.String(), candidates.String())
	}

	selected := stableDrainBatch(candidates, details, DrainSelectionPolicy{MaxCPUsDrainRatio: 0.5})
	if want := machine.NewCPUSet(0, 1, 2, 3); !selected.Equals(want) {
		t.Fatalf("topology selection = %s, want complete cores first %s", selected.String(), want.String())
	}

	incomplete := stableDrainBatch(machine.NewCPUSet(4), details, DrainSelectionPolicy{MaxCPUsDrainRatio: 0.01})
	if want := machine.NewCPUSet(4); !incomplete.Equals(want) {
		t.Fatalf("incomplete core selection = %s, want thread fallback %s", incomplete.String(), want.String())
	}

	few := stableDrainBatch(machine.NewCPUSet(0, 1, 2), details, DrainSelectionPolicy{MaxCPUsDrainRatio: 1})
	if want := machine.NewCPUSet(0, 1, 2); !few.Equals(want) {
		t.Fatalf("candidate-limited selection = %s, want all candidates %s", few.String(), want.String())
	}
}

func TestStableDrainBatchGroupByNUMAChangesSelection(t *testing.T) {
	t.Parallel()

	details := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		2: {NUMANodeID: 1, SocketID: 0, CoreID: 1},
		3: {NUMANodeID: 1, SocketID: 0, CoreID: 1},
	}
	candidates := machine.NewCPUSet(0, 2, 3)

	ungrouped := stableDrainBatch(candidates, details, DrainSelectionPolicy{
		MaxCPUsDrainRatio: 0.5,
		GroupByNUMA:       false,
	})
	if want := machine.NewCPUSet(2, 3); !ungrouped.Equals(want) {
		t.Fatalf("ungrouped selection = %s, want global complete-core preference %s", ungrouped.String(), want.String())
	}

	grouped := stableDrainBatch(candidates, details, DrainSelectionPolicy{
		MaxCPUsDrainRatio: 0.5,
		GroupByNUMA:       true,
	})
	if want := machine.NewCPUSet(0, 2); !grouped.Equals(want) {
		t.Fatalf("NUMA-grouped selection = %s, want first-NUMA-first selection %s", grouped.String(), want.String())
	}
}

func TestPairedCycleSelectionReplacesFixedBatchWithExecutableSubset(t *testing.T) {
	t.Parallel()

	details := machine.CPUDetails{
		0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		1: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
		2: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		3: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
		4: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		5: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
		6: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		7: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
	}
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(4, 5), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true},
		{Rel: "c", Domain: "c", CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
	})
	desired := map[string]machine.CPUSet{
		"a": machine.NewCPUSet(4, 5),
		"b": machine.NewCPUSet(2, 3),
		"c": machine.NewCPUSet(0, 1),
	}
	selection := DrainSelectionPolicy{
		MaxCPUsDrainRatio:         0.25,
		RequirePairedSwapProgress: true,
	}

	first, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag,
		Snapshot: planSnapshot(map[string]EntryState{
			"a": {CPUs: machine.NewCPUSet(0, 1, 2, 3)},
			"b": {CPUs: machine.NewCPUSet(4, 5)},
			"c": {CPUs: machine.NewCPUSet()},
		}, map[DomainID]machine.CPUSet{
			"a": machine.NewCPUSet(0, 1, 2, 3),
			"b": machine.NewCPUSet(4, 5),
			"c": machine.NewCPUSet(),
		}),
		DesiredByRel: desired,
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		CPUDetails:   details,
		Selection:    selection,
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan first round: %v", err)
	}
	if got, want := first.DrainBatch["a"], machine.NewCPUSet(2, 3); !got.Equals(want) {
		t.Fatalf("first a batch = %s, want cycle edge replacing fixed external edge %s", got.String(), want.String())
	}
	if got, want := first.DrainBatch["b"], machine.NewCPUSet(4, 5); !got.Equals(want) {
		t.Fatalf("first b batch = %s, want paired cycle edge %s", got.String(), want.String())
	}
	if len(first.Operations) == 0 {
		t.Fatal("paired-progress SCC was cleared instead of producing an executable drain")
	}

	second, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag,
		Snapshot: planSnapshot(map[string]EntryState{
			"a": {CPUs: machine.NewCPUSet(0, 1, 4, 5)},
			"b": {CPUs: machine.NewCPUSet(2, 3)},
			"c": {CPUs: machine.NewCPUSet()},
		}, map[DomainID]machine.CPUSet{
			"a": machine.NewCPUSet(0, 1, 4, 5),
			"b": machine.NewCPUSet(2, 3),
			"c": machine.NewCPUSet(),
		}),
		DesiredByRel: desired,
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		CPUDetails:   details,
		Selection:    selection,
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan second round: %v", err)
	}
	if got, want := second.DrainBatch["a"], machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("second a batch = %s, want remaining external edge %s", got.String(), want.String())
	}

	terminal, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag,
		Snapshot: planSnapshot(map[string]EntryState{
			"a": {CPUs: machine.NewCPUSet(4, 5)},
			"b": {CPUs: machine.NewCPUSet(2, 3)},
			"c": {CPUs: machine.NewCPUSet(0, 1)},
		}, map[DomainID]machine.CPUSet{
			"a": machine.NewCPUSet(4, 5),
			"b": machine.NewCPUSet(2, 3),
			"c": machine.NewCPUSet(0, 1),
		}),
		DesiredByRel: desired,
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		CPUDetails:   details,
		Selection:    selection,
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan terminal round: %v", err)
	}
	if len(terminal.Operations) != 0 {
		t.Fatalf("terminal plan still has operations after two progress rounds: %#v", terminal.Operations)
	}
}

func TestExpandDoesNotInferPairedCycleFromFilteredWitnesses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		domains       []string
		witnesses     []ReleaseWitness
		authorizedCPU machine.CPUSet
	}{
		{
			name:    "protected two-domain edge",
			domains: []string{"a", "b"},
			witnesses: []ReleaseWitness{
				{ConvergenceID: "ignored", Source: "a", Destination: "b", CPUs: machine.NewCPUSet()},
				{ConvergenceID: "ignored", Source: "b", Destination: "a", CPUs: machine.NewCPUSet(1)},
			},
			authorizedCPU: machine.NewCPUSet(1),
		},
		{
			name:    "SMT-incomplete three-domain edge",
			domains: []string{"a", "b", "c"},
			witnesses: []ReleaseWitness{
				{ConvergenceID: "ignored", Source: "a", Destination: "b", CPUs: machine.NewCPUSet(0)},
				{ConvergenceID: "ignored", Source: "b", Destination: "c", CPUs: machine.NewCPUSet()},
				{ConvergenceID: "ignored", Source: "c", Destination: "a", CPUs: machine.NewCPUSet(2)},
			},
			authorizedCPU: machine.NewCPUSet(0, 2),
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			specs := make([]NodeSpec, 0, len(tc.domains))
			entries := make(map[string]EntryState, len(tc.domains))
			unions := make(map[DomainID]machine.CPUSet, len(tc.domains))
			desired := make(map[string]machine.CPUSet, len(tc.domains))
			for i, domain := range tc.domains {
				specs = append(specs, NodeSpec{Rel: domain, Domain: DomainID(domain), TrustAnchor: true})
				entries[domain] = EntryState{Identity: CgroupIdentity{Inode: uint64(i + 1)}}
				unions[DomainID(domain)] = machine.NewCPUSet()
				desired[domain] = machine.NewCPUSet()
			}
			for _, witness := range tc.witnesses {
				desired[string(witness.Destination)] = desired[string(witness.Destination)].Union(witness.CPUs)
			}
			dag := mustPlanDAG(t, specs)
			snapshot := planSnapshot(entries, unions)
			convergenceID := canonicalConvergenceID(PhasePlanInput{
				Kind: PhaseExpand, DAG: dag, Snapshot: snapshot, DesiredByRel: desired,
				AllowedCPUs: machine.NewCPUSet(0, 1, 2), Selection: DrainSelectionPolicy{RequirePairedSwapProgress: true},
			})
			for i := range tc.witnesses {
				tc.witnesses[i].ConvergenceID = convergenceID
				tc.witnesses[i].SourceEvidenceID = sourceEvidenceID(snapshot, tc.witnesses[i].Source)
				tc.witnesses[i].SourceBoundaryFingerprint = sourceBoundaryFingerprint(snapshot, tc.witnesses[i].Source)
			}
			plan, err := BuildPhasePlan(PhasePlanInput{
				Kind: PhaseExpand, DAG: dag, Snapshot: snapshot, DesiredByRel: desired,
				AllowedCPUs: machine.NewCPUSet(0, 1, 2), Witnesses: tc.witnesses,
				Selection: DrainSelectionPolicy{RequirePairedSwapProgress: true},
				Budget:    NewBudgetTracker(ConvergenceBudget{}),
			})
			if err != nil {
				t.Fatalf("BuildPhasePlan: %v", err)
			}
			authorized := machine.NewCPUSet()
			for _, entering := range plan.AllowedEntering {
				authorized = authorized.Union(entering)
			}
			if !tc.authorizedCPU.IsSubsetOf(authorized) {
				t.Fatalf("valid witnessed CPUs %s not authorized by %s", tc.authorizedCPU.String(), authorized.String())
			}
		})
	}
}

func TestPairedCycleKeepsRelProtectedEdgesPendingWithEmptyDrainBatch(t *testing.T) {
	t.Parallel()

	t.Run("protected", func(t *testing.T) {
		t.Parallel()
		dag := mustPlanDAG(t, []NodeSpec{
			{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
			{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		})
		plan, err := BuildPhasePlan(PhasePlanInput{
			Kind: PhaseDrain, DAG: dag,
			Snapshot: planSnapshot(map[string]EntryState{
				"a": {CPUs: machine.NewCPUSet(0)}, "b": {CPUs: machine.NewCPUSet(1)},
			}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1)}),
			DesiredByRel:   map[string]machine.CPUSet{"a": machine.NewCPUSet(1), "b": machine.NewCPUSet(0)},
			AllowedCPUs:    machine.NewCPUSet(0, 1),
			ProtectedByRel: map[string]machine.CPUSet{"a": machine.NewCPUSet(0)},
			Selection:      DrainSelectionPolicy{RequirePairedSwapProgress: true},
			Budget:         NewBudgetTracker(ConvergenceBudget{}),
		})
		if err != nil {
			t.Fatalf("BuildPhasePlan: %v", err)
		}
		if got := plan.TransferGraph["a"]["b"]; !got.Equals(machine.NewCPUSet(0)) {
			t.Fatalf("protected pending edge disappeared from cycle: %s", got.String())
		}
		if !plan.DrainBatch["a"].IsEmpty() || !plan.DrainBatch["b"].IsEmpty() {
			t.Fatalf("protected SCC batches = a:%s b:%s, want whole SCC blocked", plan.DrainBatch["a"].String(), plan.DrainBatch["b"].String())
		}
		if got, want := plan.TargetByRel["a"].CPUs, machine.NewCPUSet(0); !got.Equals(want) {
			t.Fatalf("protected empty-batch target = %s, want observed %s", got.String(), want.String())
		}
		if len(plan.Operations) != 0 {
			t.Fatalf("protected blocked SCC generated operations: %#v", plan.Operations)
		}
	})
}

func TestPhasePlanDoesNotApplyPrimaryProtectionToReclaimDrain(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "custom-reclaim-root", Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), Role: TopoNodeRoleReclaim, TrustAnchor: true},
	})
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain,
		DAG:  dag,
		Snapshot: planSnapshot(map[string]EntryState{
			"kubepods":            {CPUs: machine.NewCPUSet(0, 1)},
			"custom-reclaim-root": {CPUs: machine.NewCPUSet(2, 3)},
		}, map[DomainID]machine.CPUSet{
			DomainPrimary: machine.NewCPUSet(0, 1),
			DomainReclaim: machine.NewCPUSet(2, 3),
		}),
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods":            machine.NewCPUSet(0, 1, 3),
			"custom-reclaim-root": machine.NewCPUSet(2),
		},
		AllowedCPUs:    machine.NewCPUSet(0, 1, 2, 3),
		ProtectedByRel: map[string]machine.CPUSet{"kubepods/pending-pod": machine.NewCPUSet(3)},
		Budget:         NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got, want := plan.DrainBatch[DomainReclaim], machine.NewCPUSet(3); !got.Equals(want) {
		t.Fatalf("reclaim drain batch = %s, want %s", got.String(), want.String())
	}
	if got := plan.TargetByRel["custom-reclaim-root"].CPUs; !got.Equals(machine.NewCPUSet(2)) {
		t.Fatalf("reclaim target = %s, want 2", got.String())
	}
}

func TestPhasePlanRejectsOverlappingReclaimNUMABucketTargets(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{
			Rel:       "custom-reclaim-root/reclaimed-0",
			ParentRel: "custom-reclaim-root",
			Role:      TopoNodeRoleReclaimNUMABucket,
			CPUs:      machine.NewCPUSet(0),
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "0"},
		},
		{
			Rel:       "custom-reclaim-root/reclaimed-1",
			ParentRel: "custom-reclaim-root",
			Role:      TopoNodeRoleReclaimNUMABucket,
			CPUs:      machine.NewCPUSet(1),
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(1),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "1"},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"custom-reclaim-root":             {CPUs: machine.NewCPUSet(0), Mems: "0-1"},
		"custom-reclaim-root/reclaimed-0": {CPUs: machine.NewCPUSet(0), Mems: "0"},
		"custom-reclaim-root/reclaimed-1": {CPUs: machine.NewCPUSet(0), Mems: "1"},
	}, map[DomainID]machine.CPUSet{
		DomainReclaim: machine.NewCPUSet(0),
	})
	snapshot.Children["custom-reclaim-root"] = []ChildRef{
		{Name: "reclaimed-0"},
		{Name: "reclaimed-1"},
	}

	_, err := BuildPhasePlan(PhasePlanInput{
		Kind:     PhaseDrain,
		DAG:      dag,
		Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"custom-reclaim-root":             machine.NewCPUSet(0),
			"custom-reclaim-root/reclaimed-0": machine.NewCPUSet(0),
			"custom-reclaim-root/reclaimed-1": machine.NewCPUSet(0),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err == nil {
		t.Fatalf("BuildPhasePlan succeeded with overlapping reclaim NUMA bucket targets")
	}
}

func TestPhasePlanAllowsMirroredNUMABucketsUnderDifferentReclaimRoots(t *testing.T) {
	t.Parallel()

	cpus := machine.NewCPUSet(0)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim-a", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: cpus, TrustAnchor: true},
		{
			Rel: "reclaim-a/bucket-0", ParentRel: "reclaim-a", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: cpus,
			Constraint: TopologyConstraint{CPUUpperBound: cpus, Scope: TopologyScopeNUMANode},
		},
		{Rel: "reclaim-b", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: cpus, TrustAnchor: true},
		{
			Rel: "reclaim-b/bucket-0", ParentRel: "reclaim-b", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: cpus,
			Constraint: TopologyConstraint{CPUUpperBound: cpus, Scope: TopologyScopeNUMANode},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"reclaim-a":          {CPUs: cpus},
		"reclaim-a/bucket-0": {CPUs: cpus},
		"reclaim-b":          {CPUs: cpus},
		"reclaim-b/bucket-0": {CPUs: cpus},
	}, map[DomainID]machine.CPUSet{DomainReclaim: cpus})
	snapshot.Children = map[string][]ChildRef{
		"reclaim-a": {{Name: "bucket-0"}},
		"reclaim-b": {{Name: "bucket-0"}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"reclaim-a": DomainReclaim, "reclaim-a/bucket-0": DomainReclaim,
		"reclaim-b": DomainReclaim, "reclaim-b/bucket-0": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim-a": cpus, "reclaim-a/bucket-0": cpus,
			"reclaim-b": cpus, "reclaim-b/bucket-0": cpus,
		},
		AllowedCPUs: cpus,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("mirrored buckets under separate reclaim roots rejected: %v", err)
	}
}

func TestPhasePlanAllowsMirroredExternalNUMABucketsGroupedByStableReclaimIdentity(t *testing.T) {
	t.Parallel()

	cpus := machine.NewCPUSet(0)
	dag := mustPlanDAG(t, []NodeSpec{
		{
			Rel: "reclaim-a", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
			CPUs: cpus, TrustAnchor: true, Metadata: map[string]string{"reclaim-index": "0"},
		},
		{
			Rel: "external/a-bucket-0", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: cpus,
			Constraint: TopologyConstraint{CPUUpperBound: cpus, Scope: TopologyScopeNUMANode},
			Metadata:   map[string]string{"reclaim-index": "0", "numa": "0"},
		},
		{
			Rel: "reclaim-b", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
			CPUs: cpus, TrustAnchor: true, Metadata: map[string]string{"reclaim-index": "1"},
		},
		{
			Rel: "external/b-bucket-0", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: cpus,
			Constraint: TopologyConstraint{CPUUpperBound: cpus, Scope: TopologyScopeNUMANode},
			Metadata:   map[string]string{"reclaim-index": "1", "numa": "0"},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"reclaim-a":           {CPUs: cpus},
		"external/a-bucket-0": {CPUs: cpus},
		"reclaim-b":           {CPUs: cpus},
		"external/b-bucket-0": {CPUs: cpus},
	}, map[DomainID]machine.CPUSet{DomainReclaim: cpus})
	snapshot.DomainByRel = map[string]DomainID{
		"reclaim-a": DomainReclaim, "external/a-bucket-0": DomainReclaim,
		"reclaim-b": DomainReclaim, "external/b-bucket-0": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim-a": cpus, "external/a-bucket-0": cpus,
			"reclaim-b": cpus, "external/b-bucket-0": cpus,
		},
		AllowedCPUs: cpus,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("mirrored external buckets under separate reclaim identities rejected: %v", err)
	}
}

func TestPhasePlanFiltersProtectedAndSelectsStableNUMASMTBatch(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(0, 1, 2, 3), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet()},
		"b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1, 2, 3)},
	}, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(), "b": machine.NewCPUSet(0, 1, 2, 3),
	})
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"a": machine.NewCPUSet(0, 1, 2, 3), "b": machine.NewCPUSet(),
		},
		ProtectedPending: machine.NewCPUSet(2),
		ProtectedByRel:   map[string]machine.CPUSet{"pod": machine.NewCPUSet(3)},
		AllowedCPUs:      machine.NewCPUSet(0, 1, 2, 3),
		AllowEmptyTarget: true,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, CoreID: 10}, 1: {NUMANodeID: 0, CoreID: 10},
			2: {NUMANodeID: 1, CoreID: 20}, 3: {NUMANodeID: 1, CoreID: 20},
		},
		Selection: DrainSelectionPolicy{
			MaxCPUsDrainRatio: 0.5,
			GroupByNUMA:       true,
		},
		Budget: NewBudgetTracker(ConvergenceBudget{}),
	}
	first, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan first: %v", err)
	}
	second, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan second: %v", err)
	}
	if got, want := first.DrainBatch["b"], machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("drain batch = %s, want whole first SMT core %s", got.String(), want.String())
	}
	if first.ConvergenceID != second.ConvergenceID || first.PlanID != second.PlanID ||
		!reflect.DeepEqual(first.Operations, second.Operations) {
		t.Fatalf("planner is not stable: first=%q/%q/%#v second=%q/%q/%#v",
			first.ConvergenceID, first.PlanID, first.Operations,
			second.ConvergenceID, second.PlanID, second.Operations)
	}
}

func TestPhasePlanTreatsProtectedPendingAsPrimaryDomainProtection(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(2), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {CPUs: machine.NewCPUSet(0, 1)},
		"reclaim": {CPUs: machine.NewCPUSet(2)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1),
		DomainReclaim: machine.NewCPUSet(2),
	})
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary,
		"reclaim": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(1),
			"reclaim": machine.NewCPUSet(2),
		},
		ProtectedPending: machine.NewCPUSet(0),
		AllowedCPUs:      machine.NewCPUSet(0, 1, 2),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got := plan.DrainBatch[DomainPrimary]; !got.IsEmpty() {
		t.Fatalf("primary drain batch = %s, want empty because CPU 0 is pending-protected", got.String())
	}
}

func TestPlannerGeneratesStableConvergenceIDFromIntent(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T, mutate func(*PhasePlanInput, *TopoDAG, *CompleteSnapshot)) string {
		t.Helper()
		dag := mustPlanDAG(t, []NodeSpec{
			{
				Rel: "root", Role: TopoNodeRoleReclaim, Domain: "a", CPUs: machine.NewCPUSet(0, 1), Mems: "0",
				ControlledRoot: true, TrustAnchor: true, Constraint: TopologyConstraint{
					CPUUpperBound: machine.NewCPUSet(0, 1, 2), MemUpperBound: machine.NewCPUSet(0, 1),
				},
			},
			{
				Rel: "root/child", ParentRel: "root", Role: TopoNodeRoleReclaimSibling, Domain: "a",
				CPUs: machine.NewCPUSet(0), Mems: "0",
			},
		})
		snapshot := planSnapshot(map[string]EntryState{
			"root":       {Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			"root/child": {Identity: CgroupIdentity{Device: 1, Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0, 1)})
		in := PhasePlanInput{
			Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{
				"root": machine.NewCPUSet(0, 1), "root/child": machine.NewCPUSet(0),
			},
			DesiredMemsByRel: map[string]string{"root": "0", "root/child": "0"},
			AllowedCPUs:      machine.NewCPUSet(0, 1, 2),
			ProtectedPending: machine.NewCPUSet(2),
			ProtectedByRel:   map[string]machine.CPUSet{"pod": machine.NewCPUSet(2)},
			Selection: DrainSelectionPolicy{
				MaxCPUsDrainRatio:         0.5,
				GroupByNUMA:               true,
				RequirePairedSwapProgress: true,
			},
			CPUDetails: machine.CPUDetails{
				0: {NUMANodeID: 0, SocketID: 0, CoreID: 0, L3CacheID: 0},
				1: {NUMANodeID: 0, SocketID: 0, CoreID: 1, L3CacheID: 0},
			},
			Budget: NewBudgetTracker(ConvergenceBudget{}),
		}
		mutate(&in, dag, snapshot)
		plan, err := BuildPhasePlan(in)
		if err != nil {
			t.Fatalf("BuildPhasePlan: %v", err)
		}
		if plan.ConvergenceID == "" {
			t.Fatal("planner generated empty canonical ConvergenceID")
		}
		return plan.ConvergenceID
	}

	baseID := build(t, func(*PhasePlanInput, *TopoDAG, *CompleteSnapshot) {})
	if got := build(t, func(_ *PhasePlanInput, _ *TopoDAG, snapshot *CompleteSnapshot) {
		snapshot.CapturedAt = snapshot.CapturedAt.Add(1)
		snapshot.ID[0]++
	}); got != baseID {
		t.Fatalf("ConvergenceID changed with observation snapshot: %q/%q", baseID, got)
	}
	tests := map[string]func(*PhasePlanInput, *TopoDAG, *CompleteSnapshot){
		"desired CPUs": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.DesiredByRel["root/child"] = machine.NewCPUSet(0, 1)
		},
		"desired mems": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.DesiredMemsByRel["root/child"] = "0-1"
			in.DesiredMemsByRel["root"] = "0-1"
		},
		"protected pending": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.ProtectedPending = machine.NewCPUSet(1, 2)
		},
		"protected by rel": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.ProtectedByRel["pod"] = machine.NewCPUSet(1, 2)
		},
		"selection drain ratio": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.Selection.MaxCPUsDrainRatio = 0.25
		},
		"selection NUMA": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.Selection.GroupByNUMA = false
		},
		"selection paired progress": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.Selection.RequirePairedSwapProgress = false
		},
		"CPU topology used by selection": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			info := in.CPUDetails[1]
			info.NUMANodeID = 1
			in.CPUDetails[1] = info
		},
		"DAG domain identity": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root/child"].Domain = "b"
		},
		"DAG role identity": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root/child"].Role = TopoNodeRolePrimary
		},
		"DAG desired identity": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].CPUs = machine.NewCPUSet(0, 1, 2)
		},
		"DAG controlled root": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].ControlledRoot = false
		},
		"DAG metadata identity": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].Metadata = map[string]string{"class": "reclaim"}
		},
		"DAG trust anchor": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root/child"].TrustAnchor = true
		},
		"DAG CPU constraint": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].Constraint.CPUUpperBound = machine.NewCPUSet(0, 1)
		},
		"DAG mem constraint": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].Constraint.MemUpperBound = machine.NewCPUSet(0)
		},
		"DAG constraint scope": func(_ *PhasePlanInput, dag *TopoDAG, _ *CompleteSnapshot) {
			dag.index["root"].Constraint.Scope = TopologyScopeNUMANode
		},
		"phase envelope": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.AllowedCPUs = machine.NewCPUSet(0, 1)
		},
		"empty target capability": func(in *PhasePlanInput, _ *TopoDAG, _ *CompleteSnapshot) {
			in.AllowEmptyTarget = true
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := build(t, mutate); got == baseID {
				t.Fatalf("canonical ConvergenceID did not change after %s mutation", name)
			}
		})
	}
}

func TestPlanIDBindsKindFreshSnapshotWitnessesAndFinalOperations(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(1, 2), TrustAnchor: true},
	})
	base := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(2)},
	}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0, 1), "b": machine.NewCPUSet(2)})
	input := PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: base,
		DesiredByRel: map[string]machine.CPUSet{"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1, 2)},
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	}
	drain, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan drain: %v", err)
	}

	fresh := clonePlanSnapshot(base)
	fresh.Entries["a"] = EntryState{Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)}
	fresh.DomainUnion["a"] = machine.NewCPUSet(0)
	fresh.ID = fingerprintSnapshot(fresh)
	witness := NewReleaseWitness(drain.ConvergenceID, "a", "b", machine.NewCPUSet(1), fresh)
	input.Kind = PhaseExpand
	input.Snapshot = fresh
	input.Witnesses = []ReleaseWitness{witness}
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	expand, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan expand: %v", err)
	}
	if drain.ConvergenceID != expand.ConvergenceID {
		t.Fatalf("ConvergenceID changed across phases: %q/%q", drain.ConvergenceID, expand.ConvergenceID)
	}
	if drain.PlanID == expand.PlanID {
		t.Fatal("PlanID did not bind phase kind/fresh snapshot/witnesses/operations")
	}

	staleSnapshot := clonePlanSnapshot(fresh)
	staleSnapshot.ID[0]++
	input.Snapshot = staleSnapshot
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	changedSnapshot, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan changed snapshot: %v", err)
	}
	if changedSnapshot.ConvergenceID != expand.ConvergenceID || changedSnapshot.PlanID == expand.PlanID {
		t.Fatalf("fresh SnapshotID must change only PlanID: convergence=%q/%q plan=%q/%q",
			changedSnapshot.ConvergenceID, expand.ConvergenceID, changedSnapshot.PlanID, expand.PlanID)
	}

	input.Snapshot = fresh
	input.Witnesses[0].CPUs = machine.NewCPUSet()
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	changedWitness, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan changed witness: %v", err)
	}
	if changedWitness.ConvergenceID != expand.ConvergenceID || changedWitness.PlanID == expand.PlanID {
		t.Fatal("canonical witness change did not change only PlanID")
	}

	changedOperation := expand
	changedOperation.Operations = append([]PlanOperation(nil), expand.Operations...)
	changedOperation.Operations[0].Target.CPUs = machine.NewCPUSet(2)
	if canonicalExecutionPlanID(changedOperation) == expand.PlanID {
		t.Fatal("final operation change did not change PlanID")
	}
}

func TestPhasePlanNormalizesAndBindsFailClosedRoots(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
	})
	base := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
	}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1)})
	base.ScanBoundary.Roots = []string{"b", "a", "b"}
	base.ID = fingerprintSnapshot(base)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: base,
		DesiredByRel: map[string]machine.CPUSet{
			"a": machine.NewCPUSet(0),
			"b": machine.NewCPUSet(1),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(plan.FailClosedRoots, want) {
		t.Fatalf("FailClosedRoots = %#v, want %#v", plan.FailClosedRoots, want)
	}

	changedRoots := plan
	changedRoots.FailClosedRoots = append([]string(nil), plan.FailClosedRoots...)
	changedRoots.FailClosedRoots[0] = "other"
	if canonicalExecutionPlanID(changedRoots) == plan.PlanID {
		t.Fatal("canonical PlanID did not bind fail-closed roots")
	}
}

func TestExpandPreservesObservedCPUsAfterRatioDrain(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "source", Domain: "source", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "destination", Domain: "destination", CPUs: machine.NewCPUSet(3), TrustAnchor: true},
	})
	before := planSnapshot(map[string]EntryState{
		"source":      {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1, 2)},
		"destination": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet()},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0, 1, 2), "destination": machine.NewCPUSet(),
	})
	drain, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: before,
		DesiredByRel: map[string]machine.CPUSet{
			"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(3),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Selection:   DrainSelectionPolicy{MaxCPUsDrainRatio: 0.5},
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan drain: %v", err)
	}
	if got, want := drain.TargetByRel["source"].CPUs, machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("ratio drain target = %s, want %s", got.String(), want.String())
	}

	after := planSnapshot(map[string]EntryState{
		"source":      {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"destination": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet()},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(),
	})
	witness := NewReleaseWitness(drain.ConvergenceID, "source", "destination", machine.NewCPUSet(1), after)
	expand, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: after,
		DesiredByRel: map[string]machine.CPUSet{
			"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(1, 3),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Witnesses:   []ReleaseWitness{witness},
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan expand: %v", err)
	}
	if got, want := expand.TargetByRel["source"].CPUs, machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("expand target deleted observed CPU: got %s, want %s", got.String(), want.String())
	}
}

func TestExpandDynamicExplicitTargetIsObservedUnionAuthorizedEntering(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "source", Domain: "source", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "destination", Domain: "destination", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"source":         {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 2)},
		"source/dynamic": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 2)},
		"destination":    {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet()},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0, 2), "destination": machine.NewCPUSet(),
	})
	snapshot.Children = map[string][]ChildRef{
		"source": {{Name: "dynamic", Identity: CgroupIdentity{Inode: 2}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"source": "source", "source/dynamic": "source", "destination": "destination",
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	convergenceID := canonicalConvergenceID(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(1),
		},
		DynamicByRel: map[string]machine.CPUSet{"source/dynamic": machine.NewCPUSet(0, 1)},
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2),
	})
	witness := NewReleaseWitness(convergenceID, "source", "destination", machine.NewCPUSet(1), snapshot)
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(1),
		},
		DynamicByRel: map[string]machine.CPUSet{"source/dynamic": machine.NewCPUSet(0, 1)},
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2),
		Witnesses:    []ReleaseWitness{witness},
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got, want := plan.TargetByRel["source/dynamic"].CPUs, machine.NewCPUSet(0, 2); !got.Equals(want) {
		t.Fatalf("dynamic expand target = %s, want observed CPUs preserved and unauthorized entering excluded %s", got.String(), want.String())
	}
	for _, operation := range plan.Operations {
		if operation.Direction == WriteShrink {
			t.Fatalf("expand emitted shrink operation: %#v", operation)
		}
	}
}

func TestExpandPlannerRejectsAnyShrinkOperation(t *testing.T) {
	t.Parallel()

	snapshot := planSnapshot(map[string]EntryState{
		"root": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
	}, map[DomainID]machine.CPUSet{"domain": machine.NewCPUSet(0, 1)})
	_, err := countPlanOperations(
		PhaseExpand,
		HierarchyCapabilities{},
		map[string]CPUSetTarget{"root": {CPUs: machine.NewCPUSet(0)}},
		snapshot,
		nil,
		nil,
	)
	if !errors.Is(err, ErrExpandPlanWouldShrink) {
		t.Fatalf("expand shrink error = %v, want %v", err, ErrExpandPlanWouldShrink)
	}
}

func TestPhasePlanBuildsHierarchyClosureAndDirectionOrder(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "root", Domain: "a", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "root/bucket", ParentRel: "root", Domain: "a", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"root":              {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"root/bucket":       {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1)},
		"root/bucket/pod":   {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0, 1)},
		"root/bucket/pod/c": {Identity: CgroupIdentity{Inode: 4}, CPUs: machine.NewCPUSet(0, 1)},
	}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{
		"root":            {{Name: "bucket", Identity: CgroupIdentity{Inode: 2}}},
		"root/bucket":     {{Name: "pod", Identity: CgroupIdentity{Inode: 3}}},
		"root/bucket/pod": {{Name: "c", Identity: CgroupIdentity{Inode: 4}}},
	}
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"root": machine.NewCPUSet(0), "root/bucket": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	wantOrder := []string{"root/bucket/pod/c", "root/bucket/pod", "root/bucket", "root"}
	gotOrder := make([]string, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		gotOrder = append(gotOrder, op.Rel)
		parent := parentRelInSnapshot(op.Rel, snapshot)
		if parent != "" && !plan.TargetByRel[op.Rel].CPUs.IsSubsetOf(plan.TargetByRel[parent].CPUs) {
			t.Fatalf("child %q target %s is outside parent %q target %s", op.Rel, plan.TargetByRel[op.Rel].CPUs.String(), parent, plan.TargetByRel[parent].CPUs.String())
		}
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("drain order = %#v, want post-order %#v", gotOrder, wantOrder)
	}
}

func TestBuildPlanOperationsV2EmptyConfiguredCPUsOrdersByMemsDirection(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "root", Domain: "a", CPUs: machine.NewCPUSet(), Mems: "0-1", TrustAnchor: true},
		{Rel: "root/child", ParentRel: "root", Domain: "a", CPUs: machine.NewCPUSet(), Mems: "0-1"},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"root": {
			Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"),
			ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1",
		},
		"root/child": {
			Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0-3"),
			ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1",
		},
	}, map[DomainID]machine.CPUSet{"a": machine.MustParse("0-3")})
	snapshot.Children = map[string][]ChildRef{
		"root": {{Name: "child", Identity: CgroupIdentity{Inode: 2}}},
	}
	depthByRel := map[string]int{"root": 0, "root/child": 1}
	domainByRel := map[string]DomainID{"root": "a", "root/child": "a"}
	parentByRel := map[string]string{"root/child": "root"}
	capabilities := HierarchyCapabilities{EmptyConfiguredCPUSet: true}

	for _, tc := range []struct {
		name          string
		kind          PhaseKind
		targetMems    string
		wantOrder     []string
		wantDirection WriteDirection
	}{
		{
			name: "shrink child first", kind: PhaseDrain, targetMems: "0",
			wantOrder: []string{"root/child", "root"}, wantDirection: WriteShrink,
		},
		{
			name: "grow parent first", kind: PhaseExpand, targetMems: "0-2",
			wantOrder: []string{"root", "root/child"}, wantDirection: WriteGrow,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			targets := map[string]CPUSetTarget{
				"root":       {CPUs: machine.NewCPUSet(), Mems: tc.targetMems},
				"root/child": {CPUs: machine.NewCPUSet(), Mems: tc.targetMems},
			}
			operations := buildPlanOperations(
				tc.kind, true, capabilities, targets, snapshot,
				depthByRel, domainByRel, parentByRel, dag, 2, nil, nil,
			)
			gotOrder := make([]string, 0, len(operations))
			for _, operation := range operations {
				gotOrder = append(gotOrder, operation.Rel)
				if operation.Direction != tc.wantDirection || !operation.WriteMems {
					t.Fatalf("operation = %#v, want direction=%s with mems write", operation, tc.wantDirection)
				}
			}
			if !reflect.DeepEqual(gotOrder, tc.wantOrder) {
				t.Fatalf("operation order = %#v, want %#v", gotOrder, tc.wantOrder)
			}
		})
	}
}

func TestBuildPhasePlanCombinesCPUAndMemsDirections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		kind        PhaseKind
		currentCPUs machine.CPUSet
		targetCPUs  machine.CPUSet
		currentMems string
		targetMems  string
		want        WriteDirection
		wantMems    string
	}{
		{
			name: "non-empty CPU unchanged with mems shrink", kind: PhaseDrain,
			currentCPUs: machine.MustParse("0-1"), targetCPUs: machine.MustParse("0-1"),
			currentMems: "0-1", targetMems: "0", want: WriteShrink, wantMems: "0",
		},
		{
			name: "non-empty CPU unchanged with mems grow", kind: PhaseExpand,
			currentCPUs: machine.MustParse("0-1"), targetCPUs: machine.MustParse("0-1"),
			currentMems: "0", targetMems: "0-1", want: WriteGrow, wantMems: "0-1",
		},
		{
			name: "drain applies CPU shrink and defers mems grow", kind: PhaseDrain,
			currentCPUs: machine.MustParse("0-2"), targetCPUs: machine.MustParse("0-1"),
			currentMems: "0", targetMems: "0-1", want: WriteShrink, wantMems: "0",
		},
		{
			name: "expand applies CPU grow after mems shrink phase", kind: PhaseExpand,
			currentCPUs: machine.MustParse("0-1"), targetCPUs: machine.MustParse("0-2"),
			currentMems: "0-1", targetMems: "0", want: WriteGrow, wantMems: "0-1",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dag := mustPlanDAG(t, []NodeSpec{{
				Rel: "root", Domain: "a", CPUs: tc.targetCPUs, Mems: tc.targetMems, TrustAnchor: true,
			}})
			snapshot := planSnapshot(map[string]EntryState{
				"root": {
					Identity: CgroupIdentity{Inode: 1}, CPUs: tc.currentCPUs, Mems: tc.currentMems,
				},
			}, map[DomainID]machine.CPUSet{"a": tc.currentCPUs})

			plan, err := BuildPhasePlan(PhasePlanInput{
				Kind: tc.kind, DAG: dag, Snapshot: snapshot,
				DesiredByRel:     map[string]machine.CPUSet{"root": tc.targetCPUs},
				DesiredMemsByRel: map[string]string{"root": tc.targetMems},
				AllowedCPUs:      machine.MustParse("0-2"),
				Budget:           NewBudgetTracker(ConvergenceBudget{}),
			})
			if err != nil {
				t.Fatalf("BuildPhasePlan() error = %v", err)
			}
			if len(plan.Operations) != 1 {
				t.Fatalf("operations = %+v, want exactly one phase operation", plan.Operations)
			}
			if got := plan.Operations[0].Direction; got != tc.want {
				t.Fatalf("operation direction = %q, want %q", got, tc.want)
			}
			if got := plan.Operations[0].Target.Mems; got != tc.wantMems {
				t.Fatalf("operation target mems = %q, want phase-local %q", got, tc.wantMems)
			}
		})
	}
}

func TestCountPlanOperationsRejectsMalformedMemsForV2EmptyConfiguredCPUs(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "root", Domain: "a", CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"root": {
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.NewCPUSet(), Mems: "bad",
		},
	}, map[DomainID]machine.CPUSet{"a": machine.MustParse("0-3")})

	_, err := countPlanOperations(
		PhaseDrain,
		HierarchyCapabilities{EmptyConfiguredCPUSet: true},
		map[string]CPUSetTarget{"root": {CPUs: machine.NewCPUSet(), Mems: "0"}},
		snapshot,
		dag,
		nil,
	)
	if err == nil {
		t.Fatal("countPlanOperations() accepted malformed mems")
	}
}

func TestExpandClosureUsesSnapshotImmediateEdgesAndProducesExplicitPreorder(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "root", Domain: "dest", CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "root/dynamic/controlled", ParentRel: "root", Domain: "dest", CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"root":                    {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"root/dynamic":            {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0)},
		"root/dynamic/controlled": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{"dest": machine.NewCPUSet(0)})
	snapshot.DomainByRel = map[string]DomainID{
		"root": "dest", "root/dynamic": "dest", "root/dynamic/controlled": "dest",
	}
	snapshot.Children = map[string][]ChildRef{
		"root":         {{Name: "dynamic", Identity: CgroupIdentity{Inode: 2}}},
		"root/dynamic": {{Name: "controlled", Identity: CgroupIdentity{Inode: 3}}},
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"root": machine.NewCPUSet(0, 1), "root/dynamic/controlled": machine.NewCPUSet(0, 1),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1),
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	wantOrder := []string{"root", "root/dynamic", "root/dynamic/controlled"}
	gotOrder := make([]string, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		gotOrder = append(gotOrder, operation.Rel)
		if !operation.Target.CPUs.Equals(machine.NewCPUSet(0, 1)) {
			t.Fatalf("operation %q target = %s, want expand closure 0-1", operation.Rel, operation.Target.CPUs.String())
		}
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("expand order = %#v, want explicit preorder %#v", gotOrder, wantOrder)
	}
}

func TestExpandDynamicLeafPreservesCurrentMemsAndOnlyWritesCPUs(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "root", Domain: "dest", CPUs: machine.NewCPUSet(0, 1), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"root":              {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"root/dynamic":      {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"root/dynamic/leaf": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0-1"},
	}, map[DomainID]machine.CPUSet{"dest": machine.NewCPUSet(0)})
	snapshot.DomainByRel = map[string]DomainID{
		"root": "dest", "root/dynamic": "dest", "root/dynamic/leaf": "dest",
	}
	snapshot.Children = map[string][]ChildRef{
		"root":         {{Name: "dynamic", Identity: CgroupIdentity{Inode: 2}}},
		"root/dynamic": {{Name: "leaf", Identity: CgroupIdentity{Inode: 3}}},
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel:     map[string]machine.CPUSet{"root": machine.NewCPUSet(0, 1)},
		DesiredMemsByRel: map[string]string{"root": "0"},
		AllowedCPUs:      machine.NewCPUSet(0, 1),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got := plan.TargetByRel["root/dynamic/leaf"].Mems; got != "0-1" {
		t.Fatalf("dynamic leaf target mems = %q, want current mems %q", got, "0-1")
	}
	if got := plan.TargetByRel["root"].Mems; got != "0-1" {
		t.Fatalf("controlled ancestor target mems = %q, want dynamic live envelope %q", got, "0-1")
	}
	for _, operation := range plan.Operations {
		if operation.Rel != "root" && operation.WriteMems {
			t.Fatalf("CPU-only expand emitted mems write: %#v", operation)
		}
	}
}

func TestBuildPlanOperationsDynamicMemsDifferenceNeverWritesMems(t *testing.T) {
	t.Parallel()

	snapshot := planSnapshot(map[string]EntryState{
		"dynamic": {
			Identity: CgroupIdentity{Inode: 2},
			CPUs:     machine.NewCPUSet(0),
			Mems:     "0",
		},
	}, map[DomainID]machine.CPUSet{"dest": machine.NewCPUSet(0)})
	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "root", Domain: "dest", CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	operations := buildPlanOperations(
		PhaseExpand,
		false,
		HierarchyCapabilities{},
		map[string]CPUSetTarget{
			"dynamic": {CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
		},
		snapshot,
		map[string]int{"dynamic": 0},
		map[string]DomainID{"dynamic": "dest"},
		nil,
		dag,
		1,
		nil, nil,
	)
	if len(operations) != 1 {
		t.Fatalf("operations = %#v, want one CPU operation", operations)
	}
	if operations[0].OwnsMems || operations[0].WriteMems {
		t.Fatalf("dynamic operation unexpectedly owns/writes mems: %#v", operations[0])
	}
	if operations[0].Target.Mems != "0" {
		t.Fatalf("dynamic operation target mems = %q, want observed mems preserved", operations[0].Target.Mems)
	}

	driver := newFakeHierarchyDriver()
	driver.allowUnwitnessedExpansion = true
	driver.add("dynamic", CgroupIdentity{Inode: 2}, "0", "0")
	plan := PhasePlan{
		ConvergenceID: "dynamic-mems-never-write",
		Kind:          PhaseExpand,
		Base:          snapshot,
		Operations:    operations,
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan); err != nil {
		t.Fatalf("execute dynamic CPU-only operation: %v", err)
	}
	if got := driver.nodes["dynamic"].mems; got != "0" {
		t.Fatalf("dynamic mems after execute = %q, want unchanged 0", got)
	}
	if len(driver.writes) != 1 || driver.writes[0].cpus.String() != "0-1" {
		t.Fatalf("dynamic writes = %#v, want exactly one CPU write", driver.writes)
	}
}

func TestExpandControlledDesiredMemsChangeStillWritesMems(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "root", Domain: "dest", CPUs: machine.NewCPUSet(0), Mems: "0-1", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"root": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{"dest": machine.NewCPUSet(0)})

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel:     map[string]machine.CPUSet{"root": machine.NewCPUSet(0)},
		DesiredMemsByRel: map[string]string{"root": "0-1"},
		AllowedCPUs:      machine.NewCPUSet(0),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("operations = %#v, want one controlled mems write", plan.Operations)
	}
	if operation := plan.Operations[0]; !operation.OwnsMems || !operation.WriteMems || operation.Target.Mems != "0-1" {
		t.Fatalf("controlled mems operation = %#v, want WriteMems target 0-1", operation)
	}
}

func TestPhasePlanRejectsTopologyConstraintDualUpperBounds(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 2), Mems: "0-1",
		Constraint: TopologyConstraint{
			CPUUpperBound: machine.NewCPUSet(0, 1),
			MemUpperBound: machine.NewCPUSet(0),
			Scope:         TopologyScopeNUMANode,
		},
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"reclaim": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(0)})
	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"reclaim": machine.NewCPUSet(0, 2)},
		AllowedCPUs:  machine.NewCPUSet(0, 1, 2),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("CPU upper-bound error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}

	dag.index["reclaim"].CPUs = machine.NewCPUSet(0)
	_, err = BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel:     map[string]machine.CPUSet{"reclaim": machine.NewCPUSet(0)},
		DesiredMemsByRel: map[string]string{"reclaim": "0-1"},
		AllowedCPUs:      machine.NewCPUSet(0, 1),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("mem upper-bound error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}
}

func TestPhasePlanChecksBudgetsBeforePublishingOperations(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(1)},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(0)},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
		"b": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
	}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1)})
	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"a": machine.NewCPUSet(1), "b": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       NewBudgetTracker(ConvergenceBudget{MaxTransferEdges: 1}),
	})
	if !errors.Is(err, ErrTransferEdgeBudgetExceeded) {
		t.Fatalf("edge budget error = %v, want %v", err, ErrTransferEdgeBudgetExceeded)
	}
}

func TestPhasePlanLowBudgetRejectsLargeGraphBeforeAllocatingEdges(t *testing.T) {
	t.Parallel()
	const domains = 256
	specs := make([]NodeSpec, 0, domains)
	entries := make(map[string]EntryState, domains)
	unions := make(map[DomainID]machine.CPUSet, domains)
	desired := make(map[string]machine.CPUSet, domains)
	for i := 0; i < domains; i++ {
		rel := fmt.Sprintf("domain-%03d", i)
		domain := DomainID(rel)
		specs = append(specs, NodeSpec{Rel: rel, Domain: domain, CPUs: machine.NewCPUSet((i + 1) % domains), TrustAnchor: true})
		entries[rel] = EntryState{Identity: CgroupIdentity{Inode: uint64(i + 1)}, CPUs: machine.NewCPUSet(i)}
		unions[domain] = machine.NewCPUSet(i)
		desired[rel] = machine.NewCPUSet((i + 1) % domains)
	}
	stats := &plannerBuildStats{}
	plan, err := buildPhasePlanWithStats(PhasePlanInput{
		Kind: PhaseDrain, DAG: mustPlanDAG(t, specs), Snapshot: planSnapshot(entries, unions),
		DesiredByRel: desired, AllowedCPUs: machine.NewCPUSet(entriesCPUList(domains)...),
		Budget: NewBudgetTracker(ConvergenceBudget{MaxTransferEdges: 1}),
	}, stats)
	if !errors.Is(err, ErrTransferEdgeBudgetExceeded) {
		t.Fatalf("edge budget error = %v, want %v", err, ErrTransferEdgeBudgetExceeded)
	}
	if !reflect.DeepEqual(plan, PhasePlan{}) {
		t.Fatalf("edge budget returned partial plan: %#v", plan)
	}
	if stats.TransferEdgesAllocated != 0 {
		t.Fatalf("allocated transfer edges = %d, want zero before budget reservation", stats.TransferEdgesAllocated)
	}
	if stats.TransferEdgesCounted != domains {
		t.Fatalf("counted transfer edges = %d, want exact lightweight count %d", stats.TransferEdgesCounted, domains)
	}
}

func TestPhasePlanLowBudgetRejectsLargeOperationSetBeforeAllocatingSortKeys(t *testing.T) {
	t.Parallel()
	const nodes = 1024
	dag, snapshot, desired := planTreeFixture(t, "wide", nodes)
	stats := &plannerBuildStats{}
	plan, err := buildPhasePlanWithStats(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: desired, AllowedCPUs: machine.NewCPUSet(0, 1),
		Budget: NewBudgetTracker(ConvergenceBudget{MaxPlanOperations: 1}),
	}, stats)
	if !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("operation budget error = %v, want %v", err, ErrPlanOperationBudgetExceeded)
	}
	if !reflect.DeepEqual(plan, PhasePlan{}) {
		t.Fatalf("operation budget returned partial plan: %#v", plan)
	}
	if stats.SortKeys != 0 {
		t.Fatalf("allocated operation sort keys = %d, want zero before budget reservation", stats.SortKeys)
	}
	if stats.PlanOperationsCounted != nodes {
		t.Fatalf("counted plan operations = %d, want exact lightweight count %d", stats.PlanOperationsCounted, nodes)
	}
}

func entriesCPUList(count int) []int {
	cpus := make([]int, count)
	for i := range cpus {
		cpus[i] = i
	}
	return cpus
}

func TestPhasePlanRequiresExplicitAllowedCPUsAndGeneratesIDs(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(0)}})
	snapshot := planSnapshot(map[string]EntryState{
		"a": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0)})
	base := PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"a": machine.NewCPUSet(0)},
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	}
	if _, err := BuildPhasePlan(base); err == nil {
		t.Fatal("BuildPhasePlan accepted empty AllowedCPUs")
	}
	base.AllowedCPUs = machine.NewCPUSet(0)
	plan, err := BuildPhasePlan(base)
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if plan.ConvergenceID == "" || plan.PlanID == "" {
		t.Fatalf("planner generated empty IDs: convergence=%q plan=%q", plan.ConvergenceID, plan.PlanID)
	}
}

func TestPhasePlanRejectsBucketOutsideDomainParentAndMemsEnvelopes(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(0), Mems: "0"},
		{
			Rel: "reclaim/bucket", ParentRel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaimNUMABucket,
			CPUs: machine.NewCPUSet(0, 1), Mems: "0-1", Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0, 1), MemUpperBound: machine.NewCPUSet(0, 1), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"reclaim":        {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"reclaim/bucket": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(0)})
	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"reclaim": machine.NewCPUSet(0), "reclaim/bucket": machine.NewCPUSet(0, 1),
		},
		DesiredMemsByRel: map[string]string{"reclaim": "0", "reclaim/bucket": "0-1"},
		AllowedCPUs:      machine.NewCPUSet(0, 1),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("bucket envelope error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}
}

func TestPhasePlanRejectsCPUAndMemsClosureForEveryControlledParentChild(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"cpus", "mems"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			dag := mustPlanDAG(t, []NodeSpec{
				{Rel: "root", Role: TopoNodeRolePrimary, Domain: "a", CPUs: machine.NewCPUSet(0), Mems: "0"},
				{
					Rel: "root/child", ParentRel: "root", Role: TopoNodeRoleReclaimSibling, Domain: "a",
					CPUs: machine.NewCPUSet(0), Mems: "0",
				},
			})
			snapshot := planSnapshot(map[string]EntryState{
				"root":       {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
				"root/child": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
			}, map[DomainID]machine.CPUSet{"a": machine.NewCPUSet(0)})
			in := PhasePlanInput{
				Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
				DesiredByRel:     map[string]machine.CPUSet{"root": machine.NewCPUSet(0), "root/child": machine.NewCPUSet(0)},
				DesiredMemsByRel: map[string]string{"root": "0", "root/child": "0"},
				AllowedCPUs:      machine.NewCPUSet(0, 1),
				Budget:           NewBudgetTracker(ConvergenceBudget{}),
			}
			if field == "cpus" {
				in.DesiredByRel["root/child"] = machine.NewCPUSet(0, 1)
			} else {
				in.DesiredMemsByRel["root/child"] = "0-1"
			}
			if _, err := BuildPhasePlan(in); !errors.Is(err, ErrInvalidReclaimBucketTarget) {
				t.Fatalf("controlled %s closure error = %v, want %v", field, err, ErrInvalidReclaimBucketTarget)
			}
		})
	}
}

func TestNUMABucketStacksConstraintParentAndDomainUpperBounds(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T, bucketCPUs machine.CPUSet, bucketMems string) error {
		t.Helper()
		dag := mustPlanDAG(t, []NodeSpec{
			{Rel: "domain", Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			{Rel: "domain/parent", ParentRel: "domain", Domain: DomainReclaim, CPUs: machine.NewCPUSet(0), Mems: "0"},
			{
				Rel: "domain/parent/bucket", ParentRel: "domain/parent", Domain: DomainReclaim,
				Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucketCPUs, Mems: bucketMems,
				Constraint: TopologyConstraint{
					CPUUpperBound: machine.NewCPUSet(0, 2),
					MemUpperBound: machine.NewCPUSet(0, 2),
					Scope:         TopologyScopeNUMANode,
				},
			},
		})
		snapshot := planSnapshot(map[string]EntryState{
			"domain":               {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
			"domain/parent":        {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
			"domain/parent/bucket": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		}, map[DomainID]machine.CPUSet{DomainReclaim: machine.NewCPUSet(0)})
		_, err := BuildPhasePlan(PhasePlanInput{
			Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{
				"domain": machine.NewCPUSet(0, 1), "domain/parent": machine.NewCPUSet(0),
				"domain/parent/bucket": bucketCPUs,
			},
			DesiredMemsByRel: map[string]string{
				"domain": "0-1", "domain/parent": "0", "domain/parent/bucket": bucketMems,
			},
			AllowedCPUs: machine.NewCPUSet(0, 1, 2),
			Budget:      NewBudgetTracker(ConvergenceBudget{}),
		})
		return err
	}
	if err := build(t, machine.NewCPUSet(0), "0"); err != nil {
		t.Fatalf("intersection of all three upper bounds rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		cpus machine.CPUSet
		mems string
	}{
		{name: "bucket constraint", cpus: machine.NewCPUSet(1), mems: "0"},
		{name: "controlled parent", cpus: machine.NewCPUSet(2), mems: "0"},
		{name: "controlled domain mems", cpus: machine.NewCPUSet(0), mems: "2"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := build(t, tc.cpus, tc.mems); !errors.Is(err, ErrInvalidReclaimBucketTarget) {
				t.Fatalf("three-upper-bound error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
			}
		})
	}
}

func TestDrainPlanConfinesFullResetBucketToNUMAUpperBound(t *testing.T) {
	t.Parallel()

	allCPUs := machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	bucket0 := machine.NewCPUSet(0, 1, 4, 5)
	bucket1 := machine.NewCPUSet(2, 3, 6, 7)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{Rel: "custom-reclaim-root", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{
			Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucket0, Mems: "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket0, MemUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode,
			},
		},
		{
			Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucket1, Mems: "1",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket1, MemUpperBound: machine.NewCPUSet(1), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":                        {Identity: CgroupIdentity{Inode: 1}, CPUs: allCPUs, Mems: "0-1"},
		"custom-reclaim-root":             {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0-1"},
		"custom-reclaim-root/reclaimed-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: allCPUs, Mems: "0"},
		"custom-reclaim-root/reclaimed-1": {Identity: CgroupIdentity{Inode: 4}, CPUs: allCPUs, Mems: "1"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: allCPUs,
		DomainReclaim: allCPUs,
	})

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods":                        machine.NewCPUSet(0, 1),
			"custom-reclaim-root":             allCPUs,
			"custom-reclaim-root/reclaimed-0": bucket0,
			"custom-reclaim-root/reclaimed-1": bucket1,
		},
		DesiredMemsByRel: map[string]string{
			"kubepods": "0-1", "custom-reclaim-root": "0-1",
			"custom-reclaim-root/reclaimed-0": "0", "custom-reclaim-root/reclaimed-1": "1",
		},
		AllowedCPUs: allCPUs,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got := plan.TargetByRel["custom-reclaim-root/reclaimed-0"].CPUs; !got.IsSubsetOf(bucket0) {
		t.Fatalf("reclaimed-0 drain target = %s, want subset of %s", got.String(), bucket0.String())
	}
	if got := plan.TargetByRel["custom-reclaim-root/reclaimed-1"].CPUs; !got.IsSubsetOf(bucket1) {
		t.Fatalf("reclaimed-1 drain target = %s, want subset of %s", got.String(), bucket1.String())
	}
}

func TestV1PlannerRejectsActiveReclaimBucketWithEmptyTarget(t *testing.T) {
	t.Parallel()

	allCPUs := machine.NewCPUSet(0, 1, 2, 3)
	bucket0 := machine.NewCPUSet(0, 1)
	bucket1 := machine.NewCPUSet(2, 3)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling, CPUs: bucket1, Mems: "0-1", TrustAnchor: true},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(), Mems: "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket0, MemUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode,
			},
		},
		{
			Rel: "reclaim/bucket-1", ParentRel: "reclaim", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucket1, Mems: "1",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket1, MemUpperBound: machine.NewCPUSet(1), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":          {Identity: CgroupIdentity{Inode: 1}, CPUs: allCPUs, Mems: "0-1"},
		"reclaim":          {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0-1"},
		"reclaim/bucket-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: bucket0, Mems: "0"},
		"reclaim/bucket-1": {Identity: CgroupIdentity{Inode: 4}, CPUs: bucket1, Mems: "1"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: allCPUs,
		DomainReclaim: allCPUs,
	})

	_, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"primary":          allCPUs,
			"reclaim":          bucket1,
			"reclaim/bucket-0": machine.NewCPUSet(),
			"reclaim/bucket-1": bucket1,
		},
		DesiredMemsByRel: map[string]string{
			"primary": "0-1", "reclaim": "0-1",
			"reclaim/bucket-0": "0", "reclaim/bucket-1": "1",
		},
		AllowedCPUs: allCPUs,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	var unsupported *UnsupportedEmptyTargetError
	if !errors.As(err, &unsupported) || unsupported.Rel != "reclaim/bucket-0" {
		t.Fatalf("BuildPhasePlan error = %T %v, want active v1 bucket empty-target rejection", err, err)
	}
}

func TestV1PlannerClampsReclaimBucketPhaseMemsToNUMABound(t *testing.T) {
	t.Parallel()

	allCPUs := machine.NewCPUSet(0, 1, 2, 3)
	bucket0 := machine.NewCPUSet(0, 1)
	bucket1 := machine.NewCPUSet(2, 3)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{Rel: "custom-reclaim-root", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{
			Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucket0, Mems: "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket0, MemUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode,
			},
		},
		{
			Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucket1, Mems: "1",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucket1, MemUpperBound: machine.NewCPUSet(1), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":                        {Identity: CgroupIdentity{Inode: 1}, CPUs: bucket1, Mems: "0-1"},
		"custom-reclaim-root":             {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0-1"},
		"custom-reclaim-root/reclaimed-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: bucket0, Mems: "0"},
		"custom-reclaim-root/reclaimed-1": {Identity: CgroupIdentity{Inode: 4}, CPUs: bucket1, Mems: "0-1"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: bucket1,
		DomainReclaim: allCPUs,
	})

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods":                        bucket1,
			"custom-reclaim-root":             allCPUs,
			"custom-reclaim-root/reclaimed-0": bucket0,
			"custom-reclaim-root/reclaimed-1": bucket1,
		},
		DesiredMemsByRel: map[string]string{
			"kubepods": "0-1", "custom-reclaim-root": "0-1",
			"custom-reclaim-root/reclaimed-0": "0", "custom-reclaim-root/reclaimed-1": "1",
		},
		AllowedCPUs: allCPUs,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got := plan.TargetByRel["custom-reclaim-root/reclaimed-1"].Mems; got != "1" {
		t.Fatalf("reclaimed-1 phase target mems = %q, want NUMA-bound mems %q", got, "1")
	}
}

func TestControlledPhaseEnvelopeDoesNotWidenNUMABucketMems(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{
			Rel: "custom-reclaim-root", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling,
			CPUs: machine.NewCPUSet(0, 1), Mems: "0-1", TrustAnchor: true,
		},
		{
			Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1), Mems: "1",
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(1), MemUpperBound: machine.NewCPUSet(1), Scope: TopologyScopeNUMANode,
			},
		},
	})
	targets := map[string]CPUSetTarget{
		"custom-reclaim-root":             {CPUs: machine.NewCPUSet(0), Mems: "0-1"},
		"custom-reclaim-root/reclaimed-1": {CPUs: machine.NewCPUSet(1), Mems: "1"},
	}

	if err := propagateControlledPhaseTargetEnvelope(targets, dag); err != nil {
		t.Fatalf("propagateControlledPhaseTargetEnvelope: %v", err)
	}
	if got := targets["custom-reclaim-root/reclaimed-1"].Mems; got != "1" {
		t.Fatalf("reclaimed-1 target mems = %q, want unchanged NUMA-bound mems %q", got, "1")
	}
	if got := targets["custom-reclaim-root"].CPUs; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("custom-reclaim-root target cpus = %s, want CPU envelope 0-1", got.String())
	}
}

func TestDrainPlanPropagatesNearestBucketCPUUpperBoundToDynamicDescendants(t *testing.T) {
	t.Parallel()

	allCPUs := machine.NewCPUSet(0, 1, 2, 3)
	bucketCPUs := machine.NewCPUSet(0, 1)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: allCPUs, Mems: "0-1", TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling, CPUs: bucketCPUs, Mems: "0", TrustAnchor: true},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucketCPUs, Mems: "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucketCPUs, MemUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":                          {Identity: CgroupIdentity{Inode: 1}, CPUs: allCPUs, Mems: "0-1"},
		"reclaim":                          {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0"},
		"reclaim/bucket-0":                 {Identity: CgroupIdentity{Inode: 3}, CPUs: allCPUs, Mems: "0"},
		"reclaim/bucket-0/pod":             {Identity: CgroupIdentity{Inode: 4}, CPUs: allCPUs, Mems: "0"},
		"reclaim/bucket-0/pod/container-a": {Identity: CgroupIdentity{Inode: 5}, CPUs: allCPUs, Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: allCPUs,
		DomainReclaim: allCPUs,
	})
	snapshot.Children = map[string][]ChildRef{
		"reclaim":              {{Name: "bucket-0", Identity: CgroupIdentity{Inode: 3}}},
		"reclaim/bucket-0":     {{Name: "pod", Identity: CgroupIdentity{Inode: 4}}},
		"reclaim/bucket-0/pod": {{Name: "container-a", Identity: CgroupIdentity{Inode: 5}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "reclaim": DomainReclaim, "reclaim/bucket-0": DomainReclaim,
		"reclaim/bucket-0/pod": DomainReclaim, "reclaim/bucket-0/pod/container-a": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	build := func(dynamic map[string]machine.CPUSet) (PhasePlan, error) {
		return BuildPhasePlan(PhasePlanInput{
			Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{
				"primary": allCPUs, "reclaim": bucketCPUs, "reclaim/bucket-0": bucketCPUs,
			},
			DynamicByRel: dynamic,
			DesiredMemsByRel: map[string]string{
				"primary": "0-1", "reclaim": "0", "reclaim/bucket-0": "0",
			},
			AllowedCPUs: allCPUs,
			Budget:      NewBudgetTracker(ConvergenceBudget{}),
		})
	}

	t.Run("inherited descendants stay inside nearest bucket phase envelope", func(t *testing.T) {
		t.Parallel()
		plan, err := build(nil)
		if err != nil {
			t.Fatalf("BuildPhasePlan: %v", err)
		}
		parent := "reclaim/bucket-0"
		for _, rel := range []string{"reclaim/bucket-0/pod", "reclaim/bucket-0/pod/container-a"} {
			target := plan.TargetByRel[rel].CPUs
			if !target.IsSubsetOf(bucketCPUs) {
				t.Errorf("%s drain target = %s, want subset of nearest bucket upper %s", rel, target.String(), bucketCPUs.String())
			}
			if !target.IsSubsetOf(plan.TargetByRel[parent].CPUs) {
				t.Errorf("%s drain target = %s, want subset of parent %s target %s",
					rel, target.String(), parent, plan.TargetByRel[parent].CPUs.String())
			}
			parent = rel
		}
	})

	t.Run("explicit allocation outside bucket upper is typed invalid", func(t *testing.T) {
		t.Parallel()
		_, err := build(map[string]machine.CPUSet{
			"reclaim/bucket-0/pod/container-a": machine.NewCPUSet(3),
		})
		if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
			t.Fatalf("explicit allocation envelope error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
		}
	})
}

func TestDrainPlanBuildsBottomUpRequiredEnvelopeForResetFullDynamicTree(t *testing.T) {
	t.Parallel()

	allCPUs := machine.NewCPUSet(0, 1, 2, 3)
	bucketCPUs := machine.NewCPUSet(0, 1)
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: allCPUs, Mems: "0", TrustAnchor: true},
		{Rel: "custom-reclaim-root", Domain: DomainReclaim, Role: TopoNodeRoleReclaimSibling, CPUs: allCPUs, Mems: "0", TrustAnchor: true},
		{
			Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucketCPUs, Mems: "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: bucketCPUs, MemUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode,
			},
		},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":                                      {Identity: CgroupIdentity{Inode: 1}, CPUs: allCPUs, Mems: "0"},
		"kubepods/inherit":                              {Identity: CgroupIdentity{Inode: 2}, CPUs: allCPUs, Mems: "0"},
		"kubepods/inherit/pod":                          {Identity: CgroupIdentity{Inode: 3}, CPUs: allCPUs, Mems: "0"},
		"kubepods/inherit/pod/container":                {Identity: CgroupIdentity{Inode: 4}, CPUs: allCPUs, Mems: "0"},
		"custom-reclaim-root":                           {Identity: CgroupIdentity{Inode: 5}, CPUs: allCPUs, Mems: "0"},
		"custom-reclaim-root/reclaimed-0":               {Identity: CgroupIdentity{Inode: 6}, CPUs: allCPUs, Mems: "0"},
		"custom-reclaim-root/reclaimed-0/pod":           {Identity: CgroupIdentity{Inode: 7}, CPUs: allCPUs, Mems: "0"},
		"custom-reclaim-root/reclaimed-0/pod/container": {Identity: CgroupIdentity{Inode: 8}, CPUs: allCPUs, Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: allCPUs,
		DomainReclaim: allCPUs,
	})
	snapshot.Children = map[string][]ChildRef{
		"kubepods":                            {{Name: "inherit", Identity: CgroupIdentity{Inode: 2}}},
		"kubepods/inherit":                    {{Name: "pod", Identity: CgroupIdentity{Inode: 3}}},
		"kubepods/inherit/pod":                {{Name: "container", Identity: CgroupIdentity{Inode: 4}}},
		"custom-reclaim-root":                 {{Name: "reclaimed-0", Identity: CgroupIdentity{Inode: 6}}},
		"custom-reclaim-root/reclaimed-0":     {{Name: "pod", Identity: CgroupIdentity{Inode: 7}}},
		"custom-reclaim-root/reclaimed-0/pod": {{Name: "container", Identity: CgroupIdentity{Inode: 8}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"kubepods": DomainPrimary, "kubepods/inherit": DomainPrimary,
		"kubepods/inherit/pod": DomainPrimary, "kubepods/inherit/pod/container": DomainPrimary,
		"custom-reclaim-root": DomainReclaim, "custom-reclaim-root/reclaimed-0": DomainReclaim,
		"custom-reclaim-root/reclaimed-0/pod": DomainReclaim, "custom-reclaim-root/reclaimed-0/pod/container": DomainReclaim,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods": allCPUs, "custom-reclaim-root": allCPUs, "custom-reclaim-root/reclaimed-0": bucketCPUs,
		},
		DynamicByRel: map[string]machine.CPUSet{
			"kubepods/inherit":                              machine.NewCPUSet(),
			"kubepods/inherit/pod/container":                machine.NewCPUSet(2),
			"custom-reclaim-root/reclaimed-0/pod/container": machine.NewCPUSet(0),
		},
		ProtectedByRel: map[string]machine.CPUSet{
			"kubepods/inherit/pod/container": machine.NewCPUSet(3),
		},
		DesiredMemsByRel: map[string]string{
			"kubepods": "0", "custom-reclaim-root": "0", "custom-reclaim-root/reclaimed-0": "0",
		},
		AllowedCPUs: allCPUs,
		Budget:      NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	for rel, want := range map[string]machine.CPUSet{
		"kubepods/inherit/pod/container":                machine.NewCPUSet(2, 3),
		"kubepods/inherit/pod":                          machine.NewCPUSet(2, 3),
		"kubepods/inherit":                              machine.NewCPUSet(2, 3),
		"custom-reclaim-root/reclaimed-0":               bucketCPUs,
		"custom-reclaim-root/reclaimed-0/pod":           machine.NewCPUSet(0),
		"custom-reclaim-root/reclaimed-0/pod/container": machine.NewCPUSet(0),
	} {
		if got := plan.TargetByRel[rel].CPUs; !got.Equals(want) {
			t.Errorf("%s drain target = %s, want required envelope %s", rel, got.String(), want.String())
		}
	}
}

func TestDrainPlanNeverRemovesExplicitAllocationInSelectedDomainBatch(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "source", Domain: "source", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "destination", Domain: "destination", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"source":           {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"source/container": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1)},
		"destination":      {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet()},
	}, map[DomainID]machine.CPUSet{"source": machine.NewCPUSet(0, 1), "destination": machine.NewCPUSet()})
	snapshot.Children = map[string][]ChildRef{
		"source": {{Name: "container", Identity: CgroupIdentity{Inode: 2}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"source": "source", "source/container": "source", "destination": "destination",
	}
	snapshot.ID = fingerprintSnapshot(snapshot)

	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(1),
		},
		DynamicByRel: map[string]machine.CPUSet{"source/container": machine.NewCPUSet(1)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got, want := plan.TargetByRel["source/container"].CPUs, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("container drain target = %s, want explicit allocation preserved %s", got.String(), want.String())
	}
}

func TestDynamicDescendantWithoutExplicitAllocationUsesNonEmptyV1Handoff(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":       {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"primary/child": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{
		"primary": {{Name: "child", Identity: CgroupIdentity{Inode: 2}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "primary/child": DomainPrimary,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	input := PhasePlanInput{
		DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	}

	input.Kind = PhaseDrain
	drain, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan drain: %v", err)
	}
	if got, want := drain.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("first drain dynamic target = %s, want current legal value %s", got.String(), want.String())
	}
	if len(drain.Operations) != 0 {
		t.Fatalf("first drain operations = %#v, want no empty cpuset write", drain.Operations)
	}

	input.Kind = PhaseExpand
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	expand, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan expand: %v", err)
	}
	if got, want := expand.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("expand dynamic target = %s, want parent closure with same-domain CPU %s", got.String(), want.String())
	}

	afterExpand := clonePlanSnapshot(snapshot)
	afterExpand.Entries["primary/child"] = EntryState{
		Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}
	afterExpand.ID = fingerprintSnapshot(afterExpand)
	input.Kind = PhaseDrain
	input.Snapshot = afterExpand
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	nextDrain, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan next drain: %v", err)
	}
	if got, want := nextDrain.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("next drain dynamic target = %s, want leaving CPU removed after handoff %s", got.String(), want.String())
	}
}

func TestExplicitDynamicDescendantUsesPhaseAwareV1DisjointBridge(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":       {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"primary/child": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(1), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{
		"primary": {{Name: "child", Identity: CgroupIdentity{Inode: 2}}},
	}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "primary/child": DomainPrimary,
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	input := PhasePlanInput{
		DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
		DynamicByRel: map[string]machine.CPUSet{"primary/child": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
	}

	input.Kind = PhaseDrain
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	drain, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan drain: %v", err)
	}
	if got, want := drain.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("disjoint explicit drain target = %s, want non-empty hold %s", got.String(), want.String())
	}

	input.Kind = PhaseExpand
	input.Budget = NewBudgetTracker(ConvergenceBudget{})
	expand, err := BuildPhasePlan(input)
	if err != nil {
		t.Fatalf("BuildPhasePlan expand: %v", err)
	}
	if got, want := expand.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("disjoint explicit expand target = %s, want authorized bridge %s", got.String(), want.String())
	}
}

func TestDynamicDescendantWithoutExplicitAllocationMayDrainEmptyOnV2(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":       {CPUs: machine.NewCPUSet(0, 1)},
		"primary/child": {CPUs: machine.NewCPUSet(1)},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{"primary": {{Name: "child"}}}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "primary/child": DomainPrimary,
	}
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel:     map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
		AllowedCPUs:      machine.NewCPUSet(0, 1),
		AllowEmptyTarget: true,
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got := plan.TargetByRel["primary/child"].CPUs; !got.IsEmpty() {
		t.Fatalf("v2 dynamic drain target = %s, want valid empty target", got.String())
	}
}

func TestDynamicDescendantWithoutExplicitAllocationHoldsCurrentOnV1(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), TrustAnchor: true,
	}})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":       {CPUs: machine.NewCPUSet(0, 1)},
		"primary/child": {CPUs: machine.NewCPUSet(1)},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	snapshot.Children = map[string][]ChildRef{"primary": {{Name: "child"}}}
	snapshot.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "primary/child": DomainPrimary,
	}
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
		AllowedCPUs:  machine.NewCPUSet(0, 1),
		Budget:       NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	if got, want := plan.TargetByRel["primary/child"].CPUs, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("v1 implicit dynamic drain target = %s, want current hold %s", got.String(), want.String())
	}
	for _, operation := range plan.Operations {
		if operation.Rel == "primary/child" && operation.Target.CPUs.IsEmpty() {
			t.Fatalf("v1 planner emitted empty implicit dynamic operation: %+v", operation)
		}
	}
}

func TestPhaseOperationTargetNeverEmitsV1EmptyDrainFromNonEmptyCurrent(t *testing.T) {
	t.Parallel()

	current := CPUSetTarget{CPUs: machine.NewCPUSet(1, 49), Mems: "0"}
	got := phaseOperationTarget(
		PhaseDrain,
		false,
		current,
		CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
	)
	if !got.CPUs.Equals(current.CPUs) {
		t.Fatalf("v1 phase operation target = %s, want current hold %s",
			got.CPUs.String(), current.CPUs.String())
	}
}

func TestV1PlannerRejectsExplicitOrControlledEmptyTargets(t *testing.T) {
	t.Parallel()

	t.Run("explicit empty dynamic allocation", func(t *testing.T) {
		t.Parallel()
		dag := mustPlanDAG(t, []NodeSpec{{
			Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(0), TrustAnchor: true,
		}})
		snapshot := planSnapshot(map[string]EntryState{
			"primary":       {CPUs: machine.NewCPUSet(0, 1)},
			"primary/child": {CPUs: machine.NewCPUSet(1)},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
		snapshot.Children = map[string][]ChildRef{"primary": {{Name: "child"}}}
		snapshot.DomainByRel = map[string]DomainID{
			"primary": DomainPrimary, "primary/child": DomainPrimary,
		}
		_, err := BuildPhasePlan(PhasePlanInput{
			Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
			DynamicByRel: map[string]machine.CPUSet{"primary/child": machine.NewCPUSet()},
			AllowedCPUs:  machine.NewCPUSet(0, 1),
			Budget:       NewBudgetTracker(ConvergenceBudget{}),
		})
		var targetErr *UnsupportedEmptyTargetError
		if !errors.As(err, &targetErr) || !errors.Is(err, ErrEmptyCPUSetUnsupported) ||
			targetErr.Rel != "primary/child" || targetErr.Source != EmptyTargetSourceExplicitDynamic {
			t.Fatalf("BuildPhasePlan error = %T %v, want explicit dynamic UnsupportedEmptyTargetError", err, err)
		}
	})

	t.Run("controlled empty target", func(t *testing.T) {
		t.Parallel()
		dag := mustPlanDAG(t, []NodeSpec{{
			Rel: "controlled", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(), TrustAnchor: true,
		}})
		snapshot := planSnapshot(map[string]EntryState{
			"controlled": {CPUs: machine.NewCPUSet(1)},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(1)})
		_, err := BuildPhasePlan(PhasePlanInput{
			Kind: PhaseDrain, DAG: dag, Snapshot: snapshot,
			DesiredByRel: map[string]machine.CPUSet{"controlled": machine.NewCPUSet()},
			AllowedCPUs:  machine.NewCPUSet(0, 1),
			Budget:       NewBudgetTracker(ConvergenceBudget{}),
		})
		var targetErr *UnsupportedEmptyTargetError
		if !errors.As(err, &targetErr) || !errors.Is(err, ErrEmptyCPUSetUnsupported) ||
			targetErr.Rel != "controlled" || targetErr.Source != EmptyTargetSourceControlled {
			t.Fatalf("BuildPhasePlan error = %T %v, want controlled UnsupportedEmptyTargetError", err, err)
		}
	})
}

func TestV2PlannerAllowsExplicitAndControlledEmptyTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		dag          *TopoDAG
		snapshot     *CompleteSnapshot
		desiredByRel map[string]machine.CPUSet
		dynamicByRel map[string]machine.CPUSet
		rel          string
	}{
		{
			name: "explicit dynamic leaf",
			dag: mustPlanDAG(t, []NodeSpec{{
				Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: machine.NewCPUSet(0), TrustAnchor: true,
			}}),
			snapshot: planSnapshot(map[string]EntryState{
				"primary":       {CPUs: machine.NewCPUSet(0, 1)},
				"primary/child": {CPUs: machine.NewCPUSet(1)},
			}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)}),
			desiredByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
			dynamicByRel: map[string]machine.CPUSet{"primary/child": machine.NewCPUSet()},
			rel:          "primary/child",
		},
		{
			name: "controlled target",
			dag: mustPlanDAG(t, []NodeSpec{{
				Rel: "controlled", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: machine.NewCPUSet(), TrustAnchor: true,
			}}),
			snapshot: planSnapshot(map[string]EntryState{
				"controlled": {CPUs: machine.NewCPUSet(1)},
			}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(1)}),
			desiredByRel: map[string]machine.CPUSet{"controlled": machine.NewCPUSet()},
			rel:          "controlled",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := BuildPhasePlan(PhasePlanInput{
				Kind: PhaseDrain, DAG: tc.dag, Snapshot: tc.snapshot,
				DesiredByRel: tc.desiredByRel, DynamicByRel: tc.dynamicByRel,
				AllowedCPUs: machine.NewCPUSet(0, 1), AllowEmptyTarget: true,
				Budget: NewBudgetTracker(ConvergenceBudget{}),
			})
			if err != nil {
				t.Fatalf("BuildPhasePlan: %v", err)
			}
			if got := plan.TargetByRel[tc.rel].CPUs; !got.IsEmpty() {
				t.Fatalf("v2 target = %s, want empty", got.String())
			}
		})
	}
}

func mustPlanDAG(t *testing.T, specs []NodeSpec) *TopoDAG {
	t.Helper()
	dag, err := BuildDAG(specs)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	return dag
}

func planSnapshot(entries map[string]EntryState, domains map[DomainID]machine.CPUSet) *CompleteSnapshot {
	snapshot := &CompleteSnapshot{
		Entries: entries, Children: map[string][]ChildRef{}, DomainUnion: domains,
		ScanBoundary: ScanBoundary{Purpose: ScanForPlan},
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	return snapshot
}

func TestSnapshotDepthByRelVisitsWideAndDeepTreesLinearly(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"wide", "deep"} {
		shape := shape
		for _, size := range []int{1000, 4000} {
			size := size
			t.Run(fmt.Sprintf("%s-%d", shape, size), func(t *testing.T) {
				t.Parallel()
				_, snapshot, _ := planTreeFixture(t, shape, size)
				stats := &depthBuildStats{}
				depths, _ := buildSnapshotDepthByRel(snapshot, stats)
				if len(depths) != size {
					t.Fatalf("depth count = %d, want %d", len(depths), size)
				}
				if stats.NodesInitialized != size {
					t.Fatalf("initialized nodes = %d, want %d", stats.NodesInitialized, size)
				}
				if stats.EdgesVisited != size-1 {
					t.Fatalf("visited edges = %d, want %d", stats.EdgesVisited, size-1)
				}
			})
		}
	}
}

func BenchmarkBuildPhasePlanWideDeepTrees(b *testing.B) {
	for _, shape := range []string{"wide", "deep"} {
		for _, size := range []int{1000, 4000} {
			size := size
			b.Run(fmt.Sprintf("%s-%d", shape, size), func(b *testing.B) {
				dag, snapshot, desired := planTreeFixture(b, shape, size)
				input := PhasePlanInput{
					Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
					DesiredByRel: desired, AllowedCPUs: machine.NewCPUSet(0, 1),
				}
				b.ResetTimer()
				indexedWork := 0
				for i := 0; i < b.N; i++ {
					input.Budget = NewBudgetTracker(ConvergenceBudget{})
					stats := &plannerBuildStats{}
					if _, err := buildPhasePlanWithStats(input, stats); err != nil {
						b.Fatal(err)
					}
					indexedWork += stats.DomainEntries + stats.SortKeys + stats.DepthNodes + stats.DepthEdges
				}
				b.ReportMetric(float64(indexedWork)/float64(b.N), "indexed-work/op")
			})
		}
	}
}

func TestBuildPhasePlanDeepTreeIndexAndSortKeyWorkScalesFourfold(t *testing.T) {
	t.Parallel()
	countWork := func(size int) int {
		dag, snapshot, desired := planTreeFixture(t, "deep", size)
		stats := &plannerBuildStats{}
		_, err := buildPhasePlanWithStats(PhasePlanInput{
			Kind: PhaseExpand, DAG: dag, Snapshot: snapshot,
			DesiredByRel: desired, AllowedCPUs: machine.NewCPUSet(0, 1),
			Budget: NewBudgetTracker(ConvergenceBudget{}),
		}, stats)
		if err != nil {
			t.Fatalf("BuildPhasePlan(%d): %v", size, err)
		}
		if stats.DomainEntries != size || stats.SortKeys != size {
			t.Fatalf("stats(%d) = %+v, want one domain entry and sort key per node", size, stats)
		}
		return stats.DomainEntries + stats.SortKeys + stats.DepthNodes + stats.DepthEdges
	}

	small := countWork(1000)
	large := countWork(4000)
	ratio := float64(large) / float64(small)
	if ratio < 3.99 || ratio > 4.01 {
		t.Fatalf("planner indexed work ratio = %.4fx (%d/%d), want near 4x", ratio, large, small)
	}
}

func planTreeFixture(tb testing.TB, shape string, size int) (*TopoDAG, *CompleteSnapshot, map[string]machine.CPUSet) {
	tb.Helper()
	specs := make([]NodeSpec, 0, size)
	entries := make(map[string]EntryState, size)
	children := make(map[string][]ChildRef, size)
	domainByRel := make(map[string]DomainID, size)
	desired := make(map[string]machine.CPUSet, size)
	parent := ""
	deepRel := "root"
	for i := 0; i < size; i++ {
		rel := "root"
		if i > 0 {
			if shape == "wide" {
				rel = fmt.Sprintf("root/n-%04d", i)
				parent = "root"
			} else {
				parent = deepRel
				deepRel = filepath.Join(deepRel, "n")
				rel = deepRel
			}
			children[parent] = append(children[parent], ChildRef{
				Name: filepath.Base(rel), Identity: CgroupIdentity{Inode: uint64(i + 1)},
			})
		}
		specs = append(specs, NodeSpec{
			Rel: rel, ParentRel: parent, Domain: "domain", CPUs: machine.NewCPUSet(0, 1), Mems: "0", TrustAnchor: true,
		})
		entries[rel] = EntryState{Rel: rel, Identity: CgroupIdentity{Inode: uint64(i + 1)}, CPUs: machine.NewCPUSet(0), Mems: "0"}
		domainByRel[rel] = "domain"
		desired[rel] = machine.NewCPUSet(0, 1)
		parent = rel
	}
	dag, err := BuildDAG(specs)
	if err != nil {
		tb.Fatalf("BuildDAG: %v", err)
	}
	snapshot := &CompleteSnapshot{
		Entries: entries, Children: children, DomainByRel: domainByRel,
		DomainUnion:  map[DomainID]machine.CPUSet{"domain": machine.NewCPUSet(0)},
		ScanBoundary: ScanBoundary{Purpose: ScanForPlan},
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	return dag, snapshot, desired
}
