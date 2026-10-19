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

package topology

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestPendingContainedDynamicMismatchFailsParentSafeAndRetainsHolder pins the generic
// boundary: a pending holder still present inside the parent whose observed CPUs
// merely differ from its dynamic target (observed != target, dynamic_target_mismatch)
// proves a mismatch only. It must NOT be read as "holder exited/retired" and must
// not be dropped from protection. ParentSafe fails (Safe=false), the leaf is retained
// as an unsafe-required mismatch rather than deferred away, and the holder entry
// stays present in the snapshot (nothing authorizes removal/retirement).
//
// Zero physical writes on an unsafe ParentSafe admission are enforced upstream by
// the admission zero-write gate; this test pins the report decision that gate
// consumes, not a driver write.
func TestPendingContainedDynamicMismatchFailsParentSafeAndRetainsHolder(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {
			Rel: "primary", Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse("0-3"),
		},
		// Holder is alive and present (not retired); observed {0} != target {1}.
		"primary/pod/container": {
			Rel: "primary/pod/container", Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse("0"),
		},
		"reclaim": {
			Rel: "reclaim", Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse("4-5"),
		},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3"),
		DomainReclaim: machine.MustParse("4-5"),
	})

	convergence := ConvergenceReport{NonConvergedTargets: []RelConvergence{{
		Rel: "primary/pod/container",
		// observed {0} vs target {1}: a pure dynamic drift, not a missing hard floor.
		Observed: machine.MustParse("0"),
		Target:   machine.MustParse("1"),
		Reason:   "dynamic_target_mismatch",
	}}}

	// Pending CPUs {0} are contained inside primary; nothing is whitelisted as
	// deferred cleanup and nothing is a required hard floor.
	report := buildParentSafetyReport(
		snapshot, dag, nil, convergence,
		machine.MustParse("0"), // protectedPending, inside primary
		nil,                    // deferredByRel
		nil,                    // deferredMismatchRels
		HierarchyCapabilities{},
	)

	if report.Safe {
		t.Fatalf("parent safety = %+v, want Safe=false for a present pending dynamic mismatch", report)
	}

	// The mismatch leaf must be retained as unsafe-required, not deferred away and
	// not deleted from protection.
	var retained bool
	for _, rel := range report.UnsafeRequiredRels {
		if rel.Rel == "primary/pod/container" {
			retained = true
		}
	}
	if !retained {
		t.Fatalf("unsafe-required = %+v, want present pending mismatch retained, not dropped", report.UnsafeRequiredRels)
	}

	// Pure dynamic mismatch must not be misclassified as a hard-floor deficit.
	if len(report.RequiredFloorDeficit) != 0 || len(report.PendingScopeDeficit) != 0 {
		t.Fatalf("floor deficits = required=%+v pending=%+v, want empty for a pure dynamic mismatch",
			report.RequiredFloorDeficit, report.PendingScopeDeficit)
	}

	// The holder entry is still present in the snapshot; observed != target does not
	// authorize retiring or removing the pending holder.
	holder, ok := snapshot.Entries["primary/pod/container"]
	if !ok {
		t.Fatalf("holder entry missing; dynamic mismatch must not retire/remove the pending holder")
	}
	if holder.Identity.Inode != 2 {
		t.Fatalf("holder identity = %v, want the alive retained holder", holder.Identity)
	}
}

// Generic CPU sets used only as a ParentSafe signature anchor. They are generic
// placeholder sets, not a recorded production event:
//   - oldSet:   current observed CPUs of both live leaves
//   - newSet:   desired target CPUs of both live leaves (inside appliedReclaim)
//   - appliedReclaim: current reclaim union, disjoint from pendingSet
//   - pendingSet: protected pending CPUs, contained in primary, disjoint from reclaim
const (
	relocOldSet         = "7,11,55,59"
	relocNewSet         = "0,4,48,52"
	relocAppliedReclaim = "0-6,10,24-26,28-30,48-54,58,72-74,76-78"
	relocPendingSet     = "7-9,11-23,27,31-47,55-57,59-71,75,79-95"
)

// TestSyntheticDualLiveDynamicMismatchFailsParentSafeSafetyOnly is a SYNTHETIC
// ParentSafe signature anchor, NOT an exact replay and NOT a root-cause proof.
// Two live leaves each hold the FULL oldSet and desire the FULL newSet; the
// appliedReclaim union stays disjoint from pendingSet; every pending CPU is
// contained in primary. Per-NUMA / per-core SMT mapping and physical cgroup
// occupancy are unknown and are NOT fabricated here.
//
// Scope: pins only the safety report decision (Safe=false, both live mismatches
// retained as unsafe-required, no floor deficit, holders stay present). Exact
// relocation replay through the manager/plugin flow and physical convergence are
// owned by another layer; this test must not be cited as reproducing a root cause
// or as a physical write.
func TestSyntheticDualLiveDynamicMismatchFailsParentSafeSafetyOnly(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, CPUs: machine.MustParse(relocPendingSet), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, CPUs: machine.MustParse(relocAppliedReclaim), TrustAnchor: true},
	})
	snapshot := planSnapshot(map[string]EntryState{
		"primary": {
			Rel: "primary", Identity: CgroupIdentity{Inode: 1}, CPUs: machine.MustParse(relocPendingSet),
		},
		// Live leaf 1: FULL observed oldSet vs FULL desired newSet, drift only.
		"primary/pod/leaf-one": {
			Rel: "primary/pod/leaf-one", Identity: CgroupIdentity{Inode: 2}, CPUs: machine.MustParse(relocOldSet),
		},
		// Live leaf 2: SAME full observed oldSet vs full desired newSet.
		"primary/pod/leaf-two": {
			Rel: "primary/pod/leaf-two", Identity: CgroupIdentity{Inode: 3}, CPUs: machine.MustParse(relocOldSet),
		},
		"reclaim": {
			Rel: "reclaim", Identity: CgroupIdentity{Inode: 4}, CPUs: machine.MustParse(relocAppliedReclaim),
		},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse(relocPendingSet),
		DomainReclaim: machine.MustParse(relocAppliedReclaim),
	})

	convergence := ConvergenceReport{NonConvergedTargets: []RelConvergence{
		{Rel: "primary/pod/leaf-one", Observed: machine.MustParse(relocOldSet), Target: machine.MustParse(relocNewSet), Reason: "dynamic_target_mismatch"},
		{Rel: "primary/pod/leaf-two", Observed: machine.MustParse(relocOldSet), Target: machine.MustParse(relocNewSet), Reason: "dynamic_target_mismatch"},
	}}

	// All reported pendingSet CPUs are contained in primary and disjoint from reclaim.
	report := buildParentSafetyReport(
		snapshot, dag, nil, convergence,
		machine.MustParse(relocPendingSet),
		nil, // deferredByRel
		nil, // deferredMismatchRels
		HierarchyCapabilities{},
	)

	if report.Safe {
		t.Fatalf("parent safety = %+v, want Safe=false for live pending mismatches", report)
	}

	// Every live mismatch must be retained as unsafe-required; none released/deferred.
	wantRetained := map[string]bool{
		"primary/pod/leaf-one": false,
		"primary/pod/leaf-two": false,
	}
	for _, rel := range report.UnsafeRequiredRels {
		if _, ok := wantRetained[rel.Rel]; ok {
			wantRetained[rel.Rel] = true
		}
	}
	for rel, retained := range wantRetained {
		if !retained {
			t.Fatalf("unsafe-required = %+v, want live mismatch %s retained, not released", report.UnsafeRequiredRels, rel)
		}
	}

	// Pure dynamic drift must not become a hard-floor / pending-scope deficit.
	if len(report.RequiredFloorDeficit) != 0 || len(report.PendingScopeDeficit) != 0 {
		t.Fatalf("floor deficits = required=%+v pending=%+v, want empty", report.RequiredFloorDeficit, report.PendingScopeDeficit)
	}

	// pendingSet is contained: nothing outside primary, nothing inside reclaim, no overlap.
	if !report.PendingOutsidePrimary.IsEmpty() {
		t.Fatalf("pending_outside_primary = %s, want empty", report.PendingOutsidePrimary)
	}
	if !report.PendingInsideReclaim.IsEmpty() {
		t.Fatalf("pending_inside_reclaim = %s, want empty", report.PendingInsideReclaim)
	}
	if !report.PrimaryReclaimOverlap.IsEmpty() {
		t.Fatalf("primary_reclaim_overlap = %s, want empty (disjoint unions)", report.PrimaryReclaimOverlap)
	}

	// Neither live holder is retired/removed; mismatch only proves drift.
	for _, rel := range []string{"primary/pod/leaf-one", "primary/pod/leaf-two"} {
		if _, ok := snapshot.Entries[rel]; !ok {
			t.Fatalf("holder %s missing; dynamic mismatch must not retire/remove a live holder", rel)
		}
	}
}
