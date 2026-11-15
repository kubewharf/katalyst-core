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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestBuildParentSafetyReportAllowsOnlySafeDeferredLeafSuperset(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":               {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"primary/pod":           {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1)},
		"primary/pod/container": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0, 1)},
		"reclaim":               {Identity: CgroupIdentity{Inode: 4}, CPUs: machine.NewCPUSet(2, 3)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1),
		DomainReclaim: machine.NewCPUSet(2, 3),
	})
	snapshot.Children = map[string][]ChildRef{
		"primary":     {{Name: "pod"}},
		"primary/pod": {{Name: "container"}},
	}
	report := ConvergenceReport{
		NonConvergedTargets: []RelConvergence{{
			Rel: "primary/pod/container", Observed: machine.NewCPUSet(0, 1),
			Target: machine.NewCPUSet(1), Reason: "dynamic_target_mismatch",
		}},
	}

	got := buildParentSafetyReport(
		snapshot, dag,
		map[string]machine.CPUSet{"primary": machine.NewCPUSet(0, 1), "reclaim": machine.NewCPUSet(2, 3)},
		report, machine.NewCPUSet(1),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil,
		HierarchyCapabilities{},
	)
	if !got.Safe {
		t.Fatalf("parent safety report = %+v, want safe", got)
	}

	snapshot.DomainUnion[DomainReclaim] = machine.NewCPUSet(1, 2, 3)
	got = buildParentSafetyReport(
		snapshot, dag,
		map[string]machine.CPUSet{"primary": machine.NewCPUSet(0), "reclaim": machine.NewCPUSet(1, 2, 3)},
		report, machine.NewCPUSet(1),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil,
		HierarchyCapabilities{},
	)
	if got.Safe || !got.PendingInsideReclaim.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("parent safety report = %+v, want pending/reclaim overlap rejected", got)
	}

	freshProof, err := evaluateCoordinatorSnapshot(
		snapshot,
		dag,
		map[string]machine.CPUSet{"primary": machine.NewCPUSet(0), "reclaim": machine.NewCPUSet(1, 2, 3)},
		map[string]machine.CPUSet{"primary": machine.NewCPUSet(0), "reclaim": machine.NewCPUSet(1, 2, 3)},
		nil,
		map[DomainID]machine.CPUSet{
			DomainPrimary: machine.NewCPUSet(0, 1),
			DomainReclaim: machine.NewCPUSet(2, 3),
		},
		machine.NewCPUSet(0, 1, 2, 3),
		nil,
		nil,
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil,
		machine.NewCPUSet(1),
		nil,
		HierarchyCapabilities{},
		false,
	)
	if err != nil {
		t.Fatalf("evaluateCoordinatorSnapshot(fresh) error = %v", err)
	}
	if freshProof.ParentSafety.Safe {
		t.Fatalf("fresh proof = %+v, want fresh reclaim overlap to invalidate the earlier parent-safe decision", freshProof)
	}
}

func TestEvaluateParentSafetyRejectsScopedAncestorDeficit(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7), TrustAnchor: true},
		{Rel: "kubepods/burstable", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(8, 9), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":           {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		"kubepods/burstable": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5)},
		"reclaim":            {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(8, 9)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		DomainReclaim: machine.NewCPUSet(8, 9),
	})
	pendingRequiredByRel := map[string]machine.CPUSet{
		"kubepods":           machine.NewCPUSet(6, 7),
		"kubepods/burstable": machine.NewCPUSet(6, 7),
	}

	report := buildParentSafetyReportWithScopedPending(
		snapshot, dag, nil, ConvergenceReport{}, machine.NewCPUSet(6, 7),
		pendingRequiredByRel, nil, nil, nil, HierarchyCapabilities{},
	)

	if report.Safe {
		t.Fatalf("parent safety report = %+v, want scoped ancestor deficit rejected", report)
	}
	if deficit := report.PendingScopeDeficit["kubepods/burstable"]; !deficit.Equals(machine.NewCPUSet(6, 7)) {
		t.Fatalf("pending scope deficit = %+v, want kubepods/burstable=6-7", report.PendingScopeDeficit)
	}
}

func TestEvaluateParentSafetyAcceptsSatisfiedScopedAncestors(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "kubepods", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7), TrustAnchor: true},
		{Rel: "kubepods/burstable", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(8, 9), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"kubepods":           {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		"kubepods/burstable": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)},
		"reclaim":            {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(8, 9)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		DomainReclaim: machine.NewCPUSet(8, 9),
	})

	report := buildParentSafetyReportWithScopedPending(
		snapshot, dag, nil, ConvergenceReport{}, machine.NewCPUSet(6, 7),
		map[string]machine.CPUSet{
			"kubepods":           machine.NewCPUSet(6, 7),
			"kubepods/burstable": machine.NewCPUSet(6, 7),
		},
		nil, nil, nil, HierarchyCapabilities{},
	)

	if !report.Safe || len(report.PendingScopeDeficit) != 0 {
		t.Fatalf("parent safety report = %+v, want scoped ancestors safe", report)
	}
}

func TestParentSafetyAllowsPlannerDeferredCleanupMismatch(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"reclaim": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(2, 3)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1),
		DomainReclaim: machine.NewCPUSet(2, 3),
	})
	report := ConvergenceReport{NonConvergedTargets: []RelConvergence{{
		Rel: "reclaim", Observed: machine.NewCPUSet(2, 3), Target: machine.NewCPUSet(2),
		Reason: convergenceReasonTargetMismatch,
	}}}

	got := buildParentSafetyReport(
		snapshot, dag, nil, report, machine.NewCPUSet(1), nil,
		map[string]struct{}{"reclaim": {}}, HierarchyCapabilities{},
	)
	if !got.Safe || len(got.DeferredLeafMismatches) != 1 {
		t.Fatalf("parent safety report = %+v, want planner-deferred cleanup accepted", got)
	}
}

func TestParentSafetyRejectsDeferredNUMAHardFloorDeficit(t *testing.T) {
	t.Parallel()

	missing := machine.MustParse("0-1,96-97")
	requiredNUMA0 := machine.MustParse(
		"0-1,7-8,10,13-16,96-97,103-104,106,109-112")
	currentNUMA0 := requiredNUMA0.Difference(missing)
	primaryCPUs := machine.MustParse("2-6,9,11-12,98-102,105,107-108")
	if currentNUMA0.Size() != 14 || requiredNUMA0.Size() != 18 || missing.Size() != 4 {
		t.Fatalf("invalid fixture: observed=%d required=%d deficit=%d, want 14/18/4",
			currentNUMA0.Size(), requiredNUMA0.Size(), missing.Size())
	}

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "custom-domain-root/dedicated-0", Domain: DomainPrimary, CPUs: primaryCPUs, TrustAnchor: true},
		{Rel: "custom-domain-root/reclaimed-0", Domain: DomainReclaim, CPUs: requiredNUMA0, TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"custom-domain-root/dedicated-0": {
			Identity: CgroupIdentity{Inode: 1},
			CPUs:     primaryCPUs,
		},
		"custom-domain-root/reclaimed-0": {
			Identity: CgroupIdentity{Inode: 2},
			CPUs:     currentNUMA0,
		},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: primaryCPUs,
		DomainReclaim: currentNUMA0,
	})
	const requiredRel = "custom-domain-root/reclaimed-0"
	const ordinaryDeferredRel = "primary/history"
	convergence := ConvergenceReport{NonConvergedTargets: []RelConvergence{
		{
			Rel:      requiredRel,
			Observed: currentNUMA0,
			Target:   requiredNUMA0,
			Reason:   convergenceReasonTargetMismatch,
		},
		{
			Rel:      ordinaryDeferredRel,
			Observed: machine.NewCPUSet(2),
			Target:   machine.NewCPUSet(2, 3),
			Reason:   convergenceReasonTargetMismatch,
		},
	}}
	requiredByRel := map[string]machine.CPUSet{
		requiredRel: requiredNUMA0,
	}

	report := buildParentSafetyReportWithRequired(
		snapshot,
		dag,
		map[string]machine.CPUSet{
			"custom-domain-root/dedicated-0": primaryCPUs,
			"custom-domain-root/reclaimed-0": requiredNUMA0,
		},
		convergence,
		machine.NewCPUSet(),
		requiredByRel,
		nil,
		map[string]struct{}{
			requiredRel:         {},
			ordinaryDeferredRel: {},
		},
		HierarchyCapabilities{},
	)
	if report.Safe {
		t.Fatalf("parent safety report = %+v, want deferred NUMA hard-floor deficit %s rejected",
			report, missing.String())
	}
	if len(report.RequiredFloorDeficit) != 1 {
		t.Fatalf("required floor deficit = %+v, want exactly one", report.RequiredFloorDeficit)
	}
	deficit, ok := report.RequiredFloorDeficit[requiredRel]
	if !ok || !deficit.Equals(missing) {
		t.Fatalf("required floor deficit = %+v, want %q=%s",
			report.RequiredFloorDeficit, requiredRel, missing.String())
	}
	if len(report.DeferredLeafMismatches) != 1 ||
		report.DeferredLeafMismatches[0].Rel != ordinaryDeferredRel {
		t.Fatalf("deferred mismatches = %+v, want only ordinary mismatch %q deferred",
			report.DeferredLeafMismatches, ordinaryDeferredRel)
	}

	snapshot.Entries[requiredRel] = EntryState{
		Identity: CgroupIdentity{Inode: 2},
		CPUs:     requiredNUMA0,
	}
	snapshot.DomainUnion[DomainReclaim] = requiredNUMA0
	convergence.NonConvergedTargets = convergence.NonConvergedTargets[1:]
	report = buildParentSafetyReportWithRequired(
		snapshot,
		dag,
		map[string]machine.CPUSet{
			"custom-domain-root/dedicated-0": primaryCPUs,
			"custom-domain-root/reclaimed-0": requiredNUMA0,
		},
		convergence,
		machine.NewCPUSet(),
		requiredByRel,
		nil,
		map[string]struct{}{
			requiredRel:         {},
			ordinaryDeferredRel: {},
		},
		HierarchyCapabilities{},
	)
	if requiredNUMA0.Size() != 18 || snapshot.Entries[requiredRel].CPUs.Size() != 18 {
		t.Fatalf("invalid control fixture: observed=%d required=%d, want 18/18",
			snapshot.Entries[requiredRel].CPUs.Size(), requiredNUMA0.Size())
	}
	if !report.Safe || len(report.RequiredFloorDeficit) != 0 {
		t.Fatalf("parent safety report = %+v, want safe with empty required floor deficit", report)
	}
}

func TestParentSafetyAllowsDeferredLeafRelocationInsidePrimary(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":               {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"primary/pod/container": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0)},
		"reclaim":               {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(2, 3)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1),
		DomainReclaim: machine.NewCPUSet(2, 3),
	})
	report := ConvergenceReport{NonConvergedTargets: []RelConvergence{{
		Rel: "primary/pod/container", Observed: machine.NewCPUSet(0),
		Target: machine.NewCPUSet(1), Reason: convergenceReasonTargetMismatch,
	}}}

	got := buildParentSafetyReport(
		snapshot, dag, nil, report, machine.NewCPUSet(),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil, HierarchyCapabilities{},
	)
	if !got.Safe || len(got.DeferredLeafMismatches) != 1 {
		t.Fatalf("parent safety report = %+v, want primary-internal relocation deferred", got)
	}

	snapshot.DomainUnion[DomainReclaim] = machine.NewCPUSet(0, 2, 3)
	got = buildParentSafetyReport(
		snapshot, dag, nil, report, machine.NewCPUSet(),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil, HierarchyCapabilities{},
	)
	if got.Safe {
		t.Fatalf("parent safety report = %+v, want observed reclaim overlap rejected", got)
	}

	snapshot.DomainUnion[DomainReclaim] = machine.NewCPUSet(1, 2, 3)
	got = buildParentSafetyReport(
		snapshot, dag, nil, report, machine.NewCPUSet(),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(1)},
		nil, HierarchyCapabilities{},
	)
	if got.Safe {
		t.Fatalf("parent safety report = %+v, want target reclaim overlap rejected", got)
	}
}

func TestParentSafetyTargetCompilationNoneUsesEmptyLogicalReclaim(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1, 2, 3), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary":               {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1, 2, 3)},
		"primary/pod/container": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0)},
		"reclaim":               {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0, 1, 2, 3)},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(0, 1, 2, 3),
		DomainReclaim: machine.NewCPUSet(0, 1, 2, 3),
	})
	report := ConvergenceReport{NonConvergedTargets: []RelConvergence{{
		Rel: "primary/pod/container", Observed: machine.NewCPUSet(0),
		Target: machine.NewCPUSet(2), Reason: convergenceReasonTargetMismatch,
	}}}

	got := buildParentSafetyReport(
		snapshot, dag, map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1, 2, 3),
			"reclaim": machine.NewCPUSet(),
		}, report, machine.NewCPUSet(),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(2)},
		nil, HierarchyCapabilities{},
	)
	if !got.Safe ||
		!got.PendingInsideReclaim.IsEmpty() ||
		!got.PrimaryReclaimOverlap.IsEmpty() ||
		len(got.DeferredLeafMismatches) != 1 ||
		len(got.UnsafeRequiredRels) != 0 {
		t.Fatalf("parent safety report = %+v, want ownership-none leaf defer", got)
	}

	got = buildParentSafetyReport(
		snapshot, dag, map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1, 2, 3),
			"reclaim": machine.NewCPUSet(0, 1, 2, 3),
		}, report, machine.NewCPUSet(),
		map[string]machine.CPUSet{"primary/pod/container": machine.NewCPUSet(2)},
		nil, HierarchyCapabilities{},
	)
	if got.Safe || !got.PrimaryReclaimOverlap.Equals(machine.NewCPUSet(0, 1, 2, 3)) {
		t.Fatalf("parent safety report = %+v, want hard reclaim ownership preserved", got)
	}
}
