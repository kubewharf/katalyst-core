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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// completeSnapshotFixture builds a small but fully populated CompleteSnapshot so
// clone-isolation tests can mutate every reference-bearing field of the source
// and prove the clone is unaffected.
func completeSnapshotFixture(t *testing.T) *CompleteSnapshot {
	t.Helper()
	return &CompleteSnapshot{
		CapturedAt:   time.Unix(0, 0),
		Capabilities: HierarchyCapabilities{StableIdentity: true},
		Entries: map[string]EntryState{
			"kubepods": {
				Rel:            "kubepods",
				Identity:       CgroupIdentity{Device: 1, Inode: 1},
				CPUs:           machine.MustParse("0-3"),
				ConfiguredCPUs: machine.MustParse("0-3"),
				Mems:           "0",
				ConfiguredMems: "0",
			},
			"child": {
				Rel:            "child",
				Identity:       CgroupIdentity{Device: 1, Inode: 2},
				CPUs:           machine.MustParse("0-1"),
				ConfiguredCPUs: machine.MustParse("0-1"),
				Mems:           "0",
				ConfiguredMems: "0",
			},
		},
		Children: map[string][]ChildRef{
			"kubepods": {{Name: "child", Identity: CgroupIdentity{Device: 1, Inode: 2}}},
			"child":    {{Name: "grandchild", Identity: CgroupIdentity{Device: 1, Inode: 3}}},
		},
		DomainByRel: map[string]DomainID{
			"kubepods": DomainPrimary,
			"child":    DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3"),
		},
		OwnershipByRel: map[string]machine.CPUSet{
			"child": machine.MustParse("0-1"),
		},
		UnavailableChildren: map[string]UnavailableChildEvidence{
			"child/unavailable": {
				Identity: CgroupIdentity{Device: 1, Inode: 4},
				Reason:   UnavailableChildReasonControllerUnavailable,
			},
		},
		ScanBoundary: ScanBoundary{
			Roots:        []string{"kubepods"},
			ExpandedRels: []string{"kubepods", "child"},
		},
	}
}

// TestCloneCompleteSnapshotIsDeeplyIsolated proves the clone shares no mutable
// map, slice, or CPUSet state with its source.
func TestCloneCompleteSnapshotIsDeeplyIsolated(t *testing.T) {
	t.Parallel()
	original := completeSnapshotFixture(t)
	clone := CloneCompleteSnapshot(original)

	// Mutate an entry's effective CPUs in place; the clone must not observe it.
	original.Entries["child"].CPUs.Add(9)
	// Replace an entry wholesale.
	original.Entries["kubepods"] = EntryState{
		Rel:            "kubepods",
		Identity:       CgroupIdentity{Device: 1, Inode: 1},
		CPUs:           machine.NewCPUSet(9),
		ConfiguredCPUs: machine.NewCPUSet(9),
		Mems:           "1",
		ConfiguredMems: "1",
	}
	// Mutate children, domain maps, and domain unions.
	original.Children["child"] = []ChildRef{{Name: "changed"}}
	original.DomainByRel["child"] = DomainReclaim
	original.DomainUnion[DomainPrimary].Add(9)
	original.OwnershipByRel["child"].Add(9)
	original.UnavailableChildren["child/unavailable"] = UnavailableChildEvidence{
		Identity: CgroupIdentity{Device: 9, Inode: 9},
		Reason:   UnavailableChildReasonControllerUnavailable,
	}
	original.ScanBoundary.Roots[0] = "changed"
	original.ScanBoundary.ExpandedRels[0] = "changed"

	require.Equal(t, "0-1", clone.Entries["child"].CPUs.String())
	require.Equal(t, "0-3", clone.Entries["kubepods"].CPUs.String())
	require.Equal(t, "0", clone.Entries["kubepods"].ConfiguredMems)
	require.Equal(t, []ChildRef{{Name: "grandchild", Identity: CgroupIdentity{Device: 1, Inode: 3}}}, clone.Children["child"])
	require.Equal(t, DomainPrimary, clone.DomainByRel["child"])
	require.Equal(t, "0-3", clone.DomainUnion[DomainPrimary].String())
	require.Equal(t, "0-1", clone.OwnershipByRel["child"].String())
	require.Equal(t, CgroupIdentity{Device: 1, Inode: 4},
		clone.UnavailableChildren["child/unavailable"].Identity)
	require.Equal(t, []string{"kubepods"}, clone.ScanBoundary.Roots)
	require.Equal(t, []string{"kubepods", "child"}, clone.ScanBoundary.ExpandedRels)
}

// TestCloneCompleteSnapshotNilReturnsNil documents the nil contract.
func TestCloneCompleteSnapshotNilReturnsNil(t *testing.T) {
	t.Parallel()
	require.Nil(t, CloneCompleteSnapshot(nil))
}

// v1Capabilities and v2Capabilities expose the immutable cgroup-version
// semantics under stable identity so projection tests exercise the exact
// backend contracts the coordinator relies on.
func v1Capabilities() HierarchyCapabilities { return cgroupV1Policy.capabilities(true) }
func v2Capabilities() HierarchyCapabilities { return cgroupV2Policy.capabilities(true) }

const (
	projectedRootRel        = "kubepods"
	projectedChildRel       = "child"
	projectedInheritRel     = "kubepods/besteffort"
	projectedGrandchildRel  = "kubepods/besteffort/pod"
	projectedRootDeviceID   = uint64(1)
	projectedRootMemsDomain = "0"
)

// projectedHierarchyFixture builds a small three-level tree so both the
// capability semantics matrix and the recursive-inheritance tests operate on
// real parent, inherited descendant, and non-inherited leaf state.
//
//	kubepods (0-3)
//	├── child            (leaf used by the semantics matrix)
//	└── kubepods/besteffort        (empty-configured, inherits on v2)
//	    └── kubepods/besteffort/pod (empty-configured, inherits on v2)
func projectedHierarchyFixture(t *testing.T, capabilities HierarchyCapabilities) *projectedHierarchy {
	t.Helper()
	base := &CompleteSnapshot{
		CapturedAt:   time.Unix(0, 0),
		Capabilities: capabilities,
		Entries: map[string]EntryState{
			projectedRootRel: {
				Rel:            projectedRootRel,
				Identity:       CgroupIdentity{Device: projectedRootDeviceID, Inode: 1},
				CPUs:           machine.MustParse("0-3"),
				ConfiguredCPUs: machine.MustParse("0-3"),
				Mems:           projectedRootMemsDomain,
				ConfiguredMems: projectedRootMemsDomain,
			},
			projectedChildRel: {
				Rel:            projectedChildRel,
				Identity:       CgroupIdentity{Device: projectedRootDeviceID, Inode: 2},
				CPUs:           machine.NewCPUSet(),
				ConfiguredCPUs: machine.NewCPUSet(),
				Mems:           projectedRootMemsDomain,
				ConfiguredMems: projectedRootMemsDomain,
			},
			projectedInheritRel: {
				Rel:            projectedInheritRel,
				Identity:       CgroupIdentity{Device: projectedRootDeviceID, Inode: 3},
				CPUs:           machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(),
				Mems:           projectedRootMemsDomain,
				ConfiguredMems: "",
			},
			projectedGrandchildRel: {
				Rel:            projectedGrandchildRel,
				Identity:       CgroupIdentity{Device: projectedRootDeviceID, Inode: 4},
				CPUs:           machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(),
				Mems:           projectedRootMemsDomain,
				ConfiguredMems: "",
			},
		},
		Children: map[string][]ChildRef{
			projectedRootRel: {
				{Name: "child", Identity: CgroupIdentity{Device: projectedRootDeviceID, Inode: 2}},
				{Name: "besteffort", Identity: CgroupIdentity{Device: projectedRootDeviceID, Inode: 3}},
			},
			projectedInheritRel: {
				{Name: "pod", Identity: CgroupIdentity{Device: projectedRootDeviceID, Inode: 4}},
			},
		},
		DomainByRel: map[string]DomainID{
			projectedRootRel:       DomainPrimary,
			projectedChildRel:      DomainPrimary,
			projectedInheritRel:    DomainPrimary,
			projectedGrandchildRel: DomainPrimary,
		},
		DomainUnion:  map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-3")},
		ScanBoundary: ScanBoundary{Purpose: ScanForPlan, Roots: []string{projectedRootRel}},
	}
	hierarchy, err := newProjectedHierarchy(base, capabilities)
	require.NoError(t, err)
	return hierarchy
}

// parentRelOf mirrors the fixture layout so operation builders can resolve the
// parent rel and its expected identity from the projected snapshot.
func parentRelOf(rel string) string {
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		return rel[:idx]
	}
	return ""
}

// setParentTarget rewrites the root effective/configured CPUs so inheritance
// tests can drive descendants purely through the parent state. It is a
// test-only seeding helper on the production type.
func (h *projectedHierarchy) setParentTarget(cpus machine.CPUSet) {
	if !cpus.Initialed {
		return
	}
	root := h.snapshot.Entries[projectedRootRel]
	root.CPUs = cpus.Clone()
	root.ConfiguredCPUs = cpus.Clone()
	h.snapshot.Entries[projectedRootRel] = root
}

func (h *projectedHierarchy) resetEvidenceRebuildCount() {
	h.evidenceRebuilds = 0
}

func (h *projectedHierarchy) evidenceRebuildCount() int {
	return h.evidenceRebuilds
}

// cpuOperation seeds the target rel's current configured/effective state to
// `configured` and returns a CPU PlanOperation toward `target`. Seeding keeps
// applyOperation's ExpectedCurrent and identity validation meaningful while the
// test asserts only the projected result. It is a test-only method on the
// production type (the plan's free `cpuOperation` needs fixture state to seed a
// matching predecessor, so it is bound to the hierarchy here).
func (h *projectedHierarchy) cpuOperation(rel string, configured, target machine.CPUSet) PlanOperation {
	entry := h.snapshot.Entries[rel]
	entry.ConfiguredCPUs = configured.Clone()
	if h.capabilities.EmptyConfiguredCPUSet && configured.IsEmpty() {
		parent := h.snapshot.Entries[parentRelOf(rel)]
		entry.CPUs = parent.CPUs.Clone()
	} else {
		entry.CPUs = configured.Clone()
	}
	h.snapshot.Entries[rel] = entry

	parentRel := parentRelOf(rel)
	direction := WriteShrink
	if configured.IsSubsetOf(target) {
		direction = WriteGrow
	}
	return PlanOperation{
		Rel:                    rel,
		ExpectedIdentity:       entry.Identity,
		ExpectedChildren:       ChildrenFingerprint(h.snapshot.Children[rel]),
		ParentRel:              parentRel,
		ExpectedParentIdentity: h.snapshot.Entries[parentRel].Identity,
		ExpectedCurrent:        CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
		Target:                 CPUSetTarget{CPUs: target.Clone(), Mems: entry.Mems},
		Direction:              direction,
	}
}

// memsOperation builds a mems-only PlanOperation so CPU/memory inheritance can
// be proven independent.
func (h *projectedHierarchy) memsOperation(rel, configuredMems, targetMems string) PlanOperation {
	entry := h.snapshot.Entries[rel]
	entry.ConfiguredMems = configuredMems
	h.snapshot.Entries[rel] = entry
	parentRel := parentRelOf(rel)
	return PlanOperation{
		Rel:                    rel,
		ExpectedIdentity:       entry.Identity,
		ExpectedChildren:       ChildrenFingerprint(h.snapshot.Children[rel]),
		ParentRel:              parentRel,
		ExpectedParentIdentity: h.snapshot.Entries[parentRel].Identity,
		ExpectedCurrent:        CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
		Target:                 CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: targetMems},
		Direction:              WriteShrink,
		WriteMems:              true,
		OwnsMems:               true,
	}
}

func TestProjectedHierarchyCPUSetSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		capabilities   HierarchyCapabilities
		configured     machine.CPUSet
		target         machine.CPUSet
		parentTarget   machine.CPUSet
		wantConfigured string
		wantEffective  string
		wantErr        error
	}{
		{
			name:           "v1 non-empty target",
			capabilities:   v1Capabilities(),
			configured:     machine.NewCPUSet(0, 1),
			target:         machine.NewCPUSet(0),
			parentTarget:   machine.MustParse("0-3"),
			wantConfigured: "0",
			wantEffective:  "0",
		},
		{
			name:         "v1 empty target is rejected",
			capabilities: v1Capabilities(),
			configured:   machine.NewCPUSet(0),
			target:       machine.NewCPUSet(),
			parentTarget: machine.MustParse("0-3"),
			wantErr:      ErrEmptyCPUSetUnsupported,
		},
		{
			name:           "v2 empty target inherits parent",
			capabilities:   v2Capabilities(),
			configured:     machine.NewCPUSet(0),
			target:         machine.NewCPUSet(),
			parentTarget:   machine.NewCPUSet(0, 1),
			wantConfigured: "",
			wantEffective:  "0-1",
		},
		{
			name:           "v2 non-empty target keeps configured",
			capabilities:   v2Capabilities(),
			configured:     machine.NewCPUSet(0, 1),
			target:         machine.NewCPUSet(0),
			parentTarget:   machine.MustParse("0-3"),
			wantConfigured: "0",
			wantEffective:  "0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hierarchy := projectedHierarchyFixture(t, tt.capabilities)
			hierarchy.setParentTarget(tt.parentTarget)
			err := hierarchy.applyOperation(hierarchy.cpuOperation(
				projectedChildRel,
				tt.configured,
				tt.target,
			))
			require.ErrorIs(t, err, tt.wantErr)
			if tt.wantErr != nil {
				return
			}
			got := hierarchy.snapshot.Entries[projectedChildRel]
			require.Equal(t, tt.wantConfigured, got.ConfiguredCPUs.String())
			require.Equal(t, tt.wantEffective, got.CPUs.String())
		})
	}
}

// TestProjectedHierarchyParentGrowUpdatesInheritedDescendants proves a parent
// grow re-projects the effective CPUs of every empty-configured descendant.
func TestProjectedHierarchyParentGrowUpdatesInheritedDescendants(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	hierarchy.setParentTarget(machine.MustParse("0-1"))
	require.NoError(t, hierarchy.recomputeEffectiveSubtree(projectedRootRel))
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedGrandchildRel].CPUs.String())

	err := hierarchy.applyOperation(hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-1"),
		machine.MustParse("0-3"),
	))
	require.NoError(t, err)

	require.Equal(t, "0-3", hierarchy.snapshot.Entries[projectedRootRel].CPUs.String())
	require.Equal(t, "0-3", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())
	require.Equal(t, "0-3", hierarchy.snapshot.Entries[projectedGrandchildRel].CPUs.String())
}

// TestProjectedHierarchyParentShrinkRecursivelyUpdatesInheritedDescendants
// proves configured-empty v2 descendants inherit the projected parent shrink.
func TestProjectedHierarchyParentShrinkRecursivelyUpdatesInheritedDescendants(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())

	operation := hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-1"),
	)
	operation.ExpectedChildUnion = machine.MustParse("0-3")
	err := hierarchy.applyOperation(operation)
	require.NoError(t, err)
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedRootRel].CPUs.String())
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedGrandchildRel].CPUs.String())
}

// TestProjectedHierarchyParentShrinkRejectsConfiguredDescendantOutsideTarget
// proves a configured-nonempty descendant cannot be silently clamped.
func TestProjectedHierarchyParentShrinkRejectsConfiguredDescendantOutsideTarget(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())

	err := hierarchy.applyOperation(hierarchy.cpuOperation(
		projectedInheritRel,
		machine.NewCPUSet(),
		machine.MustParse("1-2"),
	))
	require.NoError(t, err)
	require.Equal(t, "1-2", hierarchy.snapshot.Entries[projectedInheritRel].ConfiguredCPUs.String())
	require.Equal(t, "1-2", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())

	operation := hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-1"),
	)
	operation.ExpectedChildUnion = machine.MustParse("1-2")
	err = hierarchy.applyOperation(operation)
	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Equal(t, "child_configured_cpuset", stale.Resource)
	// The rejected parent shrink does not truncate the descendant.
	require.Equal(t, "1-2", hierarchy.snapshot.Entries[projectedInheritRel].ConfiguredCPUs.String())
	require.Equal(t, "1-2", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())
}

// TestProjectedHierarchyRejectsInvalidParentContainment proves a grow outside
// the projected parent effective set is rejected before mutating the clone.
func TestProjectedHierarchyRejectsInvalidParentContainment(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	hierarchy.setParentTarget(machine.MustParse("0-1"))
	require.NoError(t, hierarchy.recomputeEffectiveSubtree(projectedRootRel))

	err := hierarchy.applyOperation(hierarchy.cpuOperation(
		projectedInheritRel,
		machine.NewCPUSet(),
		machine.MustParse("0-3"),
	))
	require.ErrorIs(t, err, ErrProjectedParentContainment)
	// Clone must be unchanged on rejection.
	require.Equal(t, "0-1", hierarchy.snapshot.Entries[projectedInheritRel].CPUs.String())
}

// TestProjectedHierarchyCPUAndMemsInheritanceAreIndependent proves clearing a
// descendant's configured mems inherits parent mems without touching CPUs.
func TestProjectedHierarchyCPUAndMemsInheritanceAreIndependent(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	root := hierarchy.snapshot.Entries[projectedRootRel]
	root.Mems = "0-1"
	root.ConfiguredMems = "0-1"
	hierarchy.snapshot.Entries[projectedRootRel] = root

	err := hierarchy.applyOperation(hierarchy.memsOperation(projectedInheritRel, "0", ""))
	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Equal(t, "child_union_cpuset.mems", stale.Resource)

	got := hierarchy.snapshot.Entries[projectedInheritRel]
	require.Equal(t, "0", got.ConfiguredMems)
	require.Equal(t, "0", got.Mems)
	require.Equal(t, "0-3", got.CPUs.String())
}

// TestProjectedHierarchyRecomputesEvidence proves projection refreshes domain
// unions and the snapshot fingerprint so downstream proofs bind to end state.
func TestProjectedHierarchyRecomputesEvidence(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	for _, rel := range []string{projectedChildRel, projectedInheritRel, projectedGrandchildRel} {
		entry := hierarchy.snapshot.Entries[rel]
		entry.CPUs = machine.MustParse("0-1")
		entry.ConfiguredCPUs = machine.MustParse("0-1")
		hierarchy.snapshot.Entries[rel] = entry
	}
	require.NoError(t, hierarchy.recomputeEvidence())
	beforeID := hierarchy.snapshot.ID
	beforeUnion := hierarchy.snapshot.DomainUnion[DomainPrimary].String()

	err := hierarchy.applyOperation(hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-1"),
	))
	require.NoError(t, err)

	require.NotEqual(t, beforeID, hierarchy.snapshot.ID)
	require.NotEqual(t, beforeUnion, hierarchy.snapshot.DomainUnion[DomainPrimary].String())
	require.Equal(t, "0-1", hierarchy.snapshot.DomainUnion[DomainPrimary].String())
}

func TestProjectedHierarchySettlesEvidenceOncePerFrontier(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	for _, rel := range []string{projectedChildRel, projectedInheritRel} {
		entry := hierarchy.snapshot.Entries[rel]
		entry.CPUs = machine.NewCPUSet(0)
		entry.ConfiguredCPUs = machine.NewCPUSet(0)
		hierarchy.snapshot.Entries[rel] = entry
	}
	require.NoError(t, hierarchy.recomputeEvidence())
	hierarchy.resetEvidenceRebuildCount()

	session := &projectedPhaseSession{
		hierarchy: hierarchy,
		progress:  make(map[phaseProgressKey]struct{}),
	}
	operations := []PlanOperation{
		hierarchy.cpuOperation(projectedChildRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
		hierarchy.cpuOperation(projectedInheritRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
	}
	plan := PhasePlan{Kind: PhaseExpand, Operations: operations}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}

	result, err := session.Apply(context.Background(), plan)

	require.NoError(t, err)
	require.Equal(t, len(operations), result.Applied)
	require.Equal(t, 1, hierarchy.evidenceRebuildCount())
}

func TestCompiledFrontierRejectsDependency(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	root := hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-2"),
	)
	root.ExpectedChildUnion = machine.MustParse("0-3")
	descendant := hierarchy.cpuOperation(
		projectedGrandchildRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-2"),
	)

	err := validateProjectedFrontierIndependence(hierarchy, []PlanOperation{root, descendant})

	require.ErrorIs(t, err, ErrProjectedFrontierDependency)
}

func projectedV2ExternalInheritanceFixture(t *testing.T) *projectedHierarchy {
	t.Helper()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	base := &CompleteSnapshot{
		Capabilities: v2Capabilities(),
		Entries: map[string]EntryState{
			projectedRootRel: {
				Rel: projectedRootRel, Identity: rootIdentity,
				CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.NewCPUSet(),
				Mems: "0-1", ConfiguredMems: "",
			},
			projectedInheritRel: {
				Rel: projectedInheritRel, Identity: childIdentity,
				CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.NewCPUSet(),
				Mems: "0-1", ConfiguredMems: "",
			},
		},
		Children: map[string][]ChildRef{
			projectedRootRel: {{Name: "besteffort", Identity: childIdentity}},
		},
		DomainByRel: map[string]DomainID{
			projectedRootRel: DomainPrimary, projectedInheritRel: DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{projectedRootRel},
			ExpandedRels: []string{projectedRootRel, projectedInheritRel},
		},
	}
	base.ID = fingerprintSnapshot(base)
	hierarchy, err := newProjectedHierarchy(base, v2Capabilities())
	require.NoError(t, err)
	return hierarchy
}

func TestProjectedHierarchyV2RootMemsOnlyWritePreservesExternalInheritedCPUs(t *testing.T) {
	t.Parallel()
	hierarchy := projectedV2ExternalInheritanceFixture(t)
	root := hierarchy.snapshot.Entries[projectedRootRel]
	operation := PlanOperation{
		Rel: projectedRootRel, ExpectedIdentity: root.Identity,
		ExpectedChildren: ChildrenFingerprint(hierarchy.snapshot.Children[projectedRootRel]),
		ExpectedCurrent:  CPUSetTarget{CPUs: root.CPUs.Clone(), Mems: root.Mems},
		Target:           CPUSetTarget{CPUs: root.CPUs.Clone(), Mems: root.Mems},
		Direction:        WriteGrow, OwnsMems: true, WriteMems: true,
	}

	require.NoError(t, hierarchy.applyOperation(operation))

	projectedRoot := hierarchy.snapshot.Entries[projectedRootRel]
	projectedChild := hierarchy.snapshot.Entries[projectedInheritRel]
	require.True(t, projectedRoot.ConfiguredCPUs.IsEmpty())
	require.Equal(t, "0-3", projectedRoot.CPUs.String())
	require.Equal(t, "0-3", projectedChild.CPUs.String())
	require.Equal(t, "0-3", hierarchy.snapshot.DomainUnion[DomainPrimary].String())
	require.Equal(t, fingerprintSnapshot(hierarchy.snapshot), hierarchy.snapshot.ID)

	live := newFakeHierarchyDriver()
	live.capabilities = v2Capabilities()
	live.add(projectedRootRel, root.Identity, "0-3", "0-1")
	live.nodes[projectedRootRel].configuredCPUs = machine.NewCPUSet()
	require.NoError(t, live.WriteMems(context.Background(), projectedRootRel, root.Identity, root.Mems))
	liveRoot, err := live.ReadEntry(context.Background(), projectedRootRel)
	require.NoError(t, err)
	require.Equal(t, liveRoot.CPUs, projectedRoot.CPUs)
	require.Equal(t, liveRoot.Mems, projectedRoot.Mems)
}

func TestProjectedHierarchyV2RootCPUOnlyWritePreservesExternalInheritedMems(t *testing.T) {
	t.Parallel()
	hierarchy := projectedV2ExternalInheritanceFixture(t)
	root := hierarchy.snapshot.Entries[projectedRootRel]
	operation := PlanOperation{
		Rel: projectedRootRel, ExpectedIdentity: root.Identity,
		ExpectedChildren:   ChildrenFingerprint(hierarchy.snapshot.Children[projectedRootRel]),
		ExpectedChildUnion: machine.MustParse("0-3"),
		ExpectedCurrent:    CPUSetTarget{CPUs: root.CPUs.Clone(), Mems: root.Mems},
		Target:             CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: root.Mems},
		Direction:          WriteShrink,
	}

	require.NoError(t, hierarchy.applyOperation(operation))

	projectedRoot := hierarchy.snapshot.Entries[projectedRootRel]
	projectedChild := hierarchy.snapshot.Entries[projectedInheritRel]
	require.Equal(t, "0-1", projectedRoot.CPUs.String())
	require.Equal(t, "0-1", projectedChild.CPUs.String())
	require.Empty(t, projectedRoot.ConfiguredMems)
	require.Equal(t, "0-1", projectedRoot.Mems)
	require.Equal(t, "0-1", projectedChild.Mems)
	require.Equal(t, "0-1", hierarchy.snapshot.DomainUnion[DomainPrimary].String())
	require.Equal(t, fingerprintSnapshot(hierarchy.snapshot), hierarchy.snapshot.ID)

	live := newFakeHierarchyDriver()
	live.capabilities = v2Capabilities()
	live.add(projectedRootRel, root.Identity, "0-3", "0-1")
	live.nodes[projectedRootRel].configuredCPUs = machine.NewCPUSet()
	require.NoError(t, live.WriteCPUs(
		context.Background(), projectedRootRel, root.Identity, operation.Target.CPUs,
	))
	liveRoot, err := live.ReadEntry(context.Background(), projectedRootRel)
	require.NoError(t, err)
	require.Equal(t, liveRoot.CPUs, projectedRoot.CPUs)
	require.Equal(t, liveRoot.Mems, projectedRoot.Mems)
}

func TestProjectedHierarchyV2RootInheritanceRequiresInitialEffectiveEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*EntryState)
	}{
		{
			name: "CPU effective is empty",
			mutate: func(entry *EntryState) {
				entry.CPUs = machine.NewCPUSet()
			},
		},
		{
			name: "mems effective is empty",
			mutate: func(entry *EntryState) {
				entry.Mems = ""
			},
		},
		{
			name: "mems effective is malformed",
			mutate: func(entry *EntryState) {
				entry.Mems = "not-a-node-list"
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := projectedV2ExternalInheritanceFixture(t).snapshot
			entry := base.Entries[projectedRootRel]
			tc.mutate(&entry)
			base.Entries[projectedRootRel] = entry

			_, err := newProjectedHierarchy(base, v2Capabilities())

			require.Error(t, err)
			require.ErrorContains(t, err, "external inheritance")
		})
	}
}

func TestProjectedHierarchyJointWriteMatchesSafeWriterPhaseAndFinalSnapshot(t *testing.T) {
	t.Parallel()
	const (
		rootRel  = "root"
		childRel = "root/child"
	)
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	capabilities := v2Capabilities()
	initialCPUs := machine.NewCPUSet(0)
	targetCPUs := machine.MustParse("0-1")

	base := &CompleteSnapshot{
		Capabilities: capabilities,
		Entries: map[string]EntryState{
			rootRel: {
				Rel: rootRel, Identity: rootIdentity,
				CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
				Mems: "0-1", ConfiguredMems: "0-1",
			},
			childRel: {
				Rel: childRel, Identity: childIdentity,
				CPUs: initialCPUs.Clone(), ConfiguredCPUs: initialCPUs.Clone(),
				Mems: "0", ConfiguredMems: "0",
			},
		},
		Children: map[string][]ChildRef{
			rootRel: {{Name: "child", Identity: childIdentity}},
		},
		DomainByRel: map[string]DomainID{
			rootRel: DomainPrimary, childRel: DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{rootRel}, ExpandedRels: []string{rootRel, childRel},
		},
	}
	base.ID = fingerprintSnapshot(base)

	operation := PlanOperation{
		Rel: childRel, ExpectedIdentity: childIdentity,
		ExpectedChildren: ChildrenFingerprint(nil), ExpectedChildUnion: machine.NewCPUSet(),
		ParentRel: rootRel, ExpectedParentIdentity: rootIdentity,
		ExpectedCurrent: CPUSetTarget{CPUs: initialCPUs.Clone(), Mems: "0"},
		Target:          CPUSetTarget{CPUs: targetCPUs.Clone(), Mems: "0-1"},
		Direction:       WriteGrow, OwnsMems: true, WriteMems: true,
	}
	plan := PhasePlan{
		ConvergenceID: "joint-cpu-mems",
		Kind:          PhaseExpand,
		Capabilities:  capabilities,
		Operations:    []PlanOperation{operation},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	projected, err := newProjectedHierarchy(base, capabilities)
	require.NoError(t, err)
	require.NoError(t, projected.applyOperation(plan.Operations[0]))

	live := newFakeHierarchyDriver()
	live.capabilities = capabilities
	live.allowUnwitnessedExpansion = true
	live.add(rootRel, rootIdentity, "0-3", "0-1")
	live.add(childRel, childIdentity, "0", "0")
	result := &ConvergenceResult{}
	require.NoError(t, newSafeCPUSetWriter(
		live, NewBudgetTracker(ConvergenceBudget{}), result,
	).execute(context.Background(), plan))

	require.Len(t, live.writes, 2)
	require.Equal(t, initialCPUs, live.writes[0].cpus)
	require.Equal(t, "0-1", live.writes[0].mems)
	require.Equal(t, targetCPUs, live.writes[1].cpus)
	require.Equal(t, "0-1", live.writes[1].mems)

	projectedEntry := projected.snapshot.Entries[childRel]
	liveEntry, err := live.ReadEntry(context.Background(), childRel)
	require.NoError(t, err)
	require.Equal(t, liveEntry.CPUs, projectedEntry.CPUs)
	require.Equal(t, liveEntry.Mems, projectedEntry.Mems)
	require.Equal(t, liveEntry.ConfiguredCPUs, projectedEntry.ConfiguredCPUs)
	require.Equal(t, liveEntry.ConfiguredMems, projectedEntry.ConfiguredMems)
	require.Len(t, result.Journal, 1)
	require.Equal(t, plan.Operations[0].Target, result.Journal[0].Target)
	require.Equal(t, CPUSetTarget{CPUs: projectedEntry.CPUs, Mems: projectedEntry.Mems}, result.Journal[0].Observed)
}

func TestProjectedHierarchyJointGrowRejectsMemsOutsideParentLikeSafeWriter(t *testing.T) {
	t.Parallel()
	const (
		rootRel  = "root"
		childRel = "root/child"
	)
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	capabilities := v2Capabilities()
	initialCPUs := machine.NewCPUSet(0)
	targetCPUs := machine.MustParse("0-1")
	operation := PlanOperation{
		Rel: childRel, ExpectedIdentity: childIdentity,
		ExpectedChildren: ChildrenFingerprint(nil), ExpectedChildUnion: machine.NewCPUSet(),
		ParentRel: rootRel, ExpectedParentIdentity: rootIdentity,
		ExpectedCurrent: CPUSetTarget{CPUs: initialCPUs.Clone(), Mems: "0"},
		Target:          CPUSetTarget{CPUs: targetCPUs.Clone(), Mems: "0-1"},
		Direction:       WriteGrow, OwnsMems: true, WriteMems: true,
	}
	plan := PhasePlan{
		ConvergenceID: "joint-grow-invalid-parent-mems",
		Kind:          PhaseExpand,
		Capabilities:  capabilities,
		Operations:    []PlanOperation{operation},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	base := &CompleteSnapshot{
		Capabilities: capabilities,
		Entries: map[string]EntryState{
			rootRel: {
				Rel: rootRel, Identity: rootIdentity,
				CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
				Mems: "0", ConfiguredMems: "0",
			},
			childRel: {
				Rel: childRel, Identity: childIdentity,
				CPUs: initialCPUs.Clone(), ConfiguredCPUs: initialCPUs.Clone(),
				Mems: "0", ConfiguredMems: "0",
			},
		},
		Children: map[string][]ChildRef{
			rootRel: {{Name: "child", Identity: childIdentity}},
		},
		DomainByRel: map[string]DomainID{
			rootRel: DomainPrimary, childRel: DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{rootRel}, ExpandedRels: []string{rootRel, childRel},
		},
	}
	base.ID = fingerprintSnapshot(base)

	projected, err := newProjectedHierarchy(base, capabilities)
	require.NoError(t, err)
	projectedBefore := CloneCompleteSnapshot(projected.snapshot)
	projectedErr := projected.applyOperation(plan.Operations[0])

	live := newFakeHierarchyDriver()
	live.capabilities = capabilities
	live.allowUnwitnessedExpansion = true
	live.add(rootRel, rootIdentity, "0-3", "0")
	live.add(childRel, childIdentity, "0", "0")
	liveErr := newSafeCPUSetWriter(
		live, NewBudgetTracker(ConvergenceBudget{}), &ConvergenceResult{},
	).execute(context.Background(), plan)

	var projectedStale, liveStale *PlanStaleError
	require.ErrorAs(t, projectedErr, &projectedStale)
	require.ErrorAs(t, liveErr, &liveStale)
	require.Equal(t, "parent_cpuset.mems", liveStale.Resource)
	require.Equal(t, liveStale.Resource, projectedStale.Resource)
	require.Equal(t, liveStale.Current, projectedStale.Current)
	require.Equal(t, liveStale.Target, projectedStale.Target)
	require.Equal(t, projectedBefore, projected.snapshot)
	require.Zero(t, live.PhysicalWriteCount())
}

func TestProjectedHierarchyRejectedJointWriteIsAtomic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		capabilities HierarchyCapabilities
		parentCPUs   machine.CPUSet
		targetCPUs   machine.CPUSet
		wantErr      error
	}{
		{
			name:         "v1 empty CPU target",
			capabilities: v1Capabilities(),
			parentCPUs:   machine.MustParse("0-3"),
			targetCPUs:   machine.NewCPUSet(),
			wantErr:      ErrEmptyCPUSetUnsupported,
		},
		{
			name:         "v2 target outside parent",
			capabilities: v2Capabilities(),
			parentCPUs:   machine.MustParse("0-1"),
			targetCPUs:   machine.MustParse("0-3"),
			wantErr:      ErrProjectedParentContainment,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hierarchy := projectedHierarchyFixture(t, tc.capabilities)
			hierarchy.setParentTarget(tc.parentCPUs)
			require.NoError(t, hierarchy.recomputeEffectiveSubtree(projectedRootRel))
			entry := hierarchy.snapshot.Entries[projectedInheritRel]
			entry.CPUs = machine.NewCPUSet(0)
			entry.ConfiguredCPUs = machine.NewCPUSet(0)
			hierarchy.snapshot.Entries[projectedInheritRel] = entry
			require.NoError(t, hierarchy.recomputeEvidence())
			before := CloneCompleteSnapshot(hierarchy.snapshot)
			direction := WriteGrow
			if tc.targetCPUs.IsSubsetOf(entry.CPUs) {
				direction = WriteShrink
			}
			operation := PlanOperation{
				Rel: projectedInheritRel, ExpectedIdentity: entry.Identity,
				ExpectedChildren:       ChildrenFingerprint(hierarchy.snapshot.Children[projectedInheritRel]),
				ParentRel:              projectedRootRel,
				ExpectedParentIdentity: hierarchy.snapshot.Entries[projectedRootRel].Identity,
				ExpectedCurrent:        CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
				Target:                 CPUSetTarget{CPUs: tc.targetCPUs.Clone(), Mems: "1"},
				Direction:              direction,
				OwnsMems:               true,
				WriteMems:              true,
			}

			err := hierarchy.applyOperation(operation)
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, before.Entries[projectedInheritRel], hierarchy.snapshot.Entries[projectedInheritRel])
			require.Equal(t, before.ID, hierarchy.snapshot.ID)
			require.Equal(t, before.DomainUnion, hierarchy.snapshot.DomainUnion)
		})
	}
}

func TestProjectedHierarchyRejectsStalePredecessorBeforeMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		wantResource string
		mutate       func(*PlanOperation)
	}{
		{
			name:         "expected cpus",
			wantResource: "cpuset.cpus",
			mutate: func(operation *PlanOperation) {
				operation.ExpectedCurrent.CPUs = machine.NewCPUSet(3)
			},
		},
		{
			name:         "expected mems",
			wantResource: "cpuset.mems",
			mutate: func(operation *PlanOperation) {
				operation.ExpectedCurrent.Mems = "1"
			},
		},
		{
			name:         "expected identity",
			wantResource: "identity",
			mutate: func(operation *PlanOperation) {
				operation.ExpectedIdentity = CgroupIdentity{Device: 9, Inode: 9}
			},
		},
		{
			name:         "expected parent identity",
			wantResource: "parent_identity",
			mutate: func(operation *PlanOperation) {
				operation.ExpectedParentIdentity = CgroupIdentity{Device: 9, Inode: 9}
			},
		},
		{
			name:         "expected children fingerprint",
			wantResource: "children",
			mutate: func(operation *PlanOperation) {
				operation.Direction = WriteShrink
				operation.ExpectedChildren = "stale-children"
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hierarchy := projectedHierarchyFixture(t, v2Capabilities())
			operation := hierarchy.cpuOperation(
				projectedChildRel,
				machine.NewCPUSet(),
				machine.NewCPUSet(0),
			)
			operation.ParentRel = projectedRootRel
			operation.ExpectedParentIdentity = hierarchy.snapshot.Entries[projectedRootRel].Identity
			operation.ExpectedChildren = ChildrenFingerprint(hierarchy.snapshot.Children[projectedChildRel])
			operation.WriteMems = true
			operation.OwnsMems = true
			operation.Target.Mems = "0-1"
			tc.mutate(&operation)
			before := CloneCompleteSnapshot(hierarchy.snapshot)

			err := hierarchy.applyOperation(operation)

			var stale *PlanStaleError
			require.ErrorAs(t, err, &stale)
			require.ErrorIs(t, err, ErrCoordinatorPlanStale)
			require.Equal(t, tc.wantResource, stale.Resource)
			if tc.wantResource == "identity" || tc.wantResource == "parent_identity" {
				require.ErrorIs(t, stale.Err, ErrCgroupIdentityChanged)
			}
			require.Equal(t, before, hierarchy.snapshot)
		})
	}
}

func TestProjectedHierarchyParentShrinkRejectsChildCPUUnionBeforeMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		childCPUs          machine.CPUSet
		expectedChildUnion machine.CPUSet
		targetCPUs         machine.CPUSet
	}{
		{
			name:               "expected child union outside target",
			childCPUs:          machine.NewCPUSet(0),
			expectedChildUnion: machine.MustParse("0-1"),
			targetCPUs:         machine.NewCPUSet(0),
		},
		{
			name:               "projected live child union outside target",
			childCPUs:          machine.MustParse("0-1"),
			expectedChildUnion: machine.NewCPUSet(0),
			targetCPUs:         machine.NewCPUSet(0),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hierarchy := projectedHierarchyFixture(t, v2Capabilities())
			child := hierarchy.snapshot.Entries[projectedChildRel]
			child.CPUs = tc.childCPUs.Clone()
			child.ConfiguredCPUs = tc.childCPUs.Clone()
			hierarchy.snapshot.Entries[projectedChildRel] = child
			require.NoError(t, hierarchy.recomputeEvidence())

			operation := hierarchy.cpuOperation(
				projectedRootRel,
				machine.MustParse("0-3"),
				tc.targetCPUs,
			)
			operation.ExpectedChildUnion = tc.expectedChildUnion.Clone()
			before := CloneCompleteSnapshot(hierarchy.snapshot)

			err := hierarchy.applyOperation(operation)

			var stale *PlanStaleError
			require.ErrorAs(t, err, &stale)
			require.ErrorIs(t, err, ErrCoordinatorPlanStale)
			require.Equal(t, before, hierarchy.snapshot)
		})
	}
}

func TestProjectedHierarchyJointMemsShrinkRejectsChildUnionBeforeMutation(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	root := hierarchy.snapshot.Entries[projectedRootRel]
	root.Mems = "0-1"
	root.ConfiguredMems = "0-1"
	hierarchy.snapshot.Entries[projectedRootRel] = root
	child := hierarchy.snapshot.Entries[projectedChildRel]
	child.Mems = "1"
	child.ConfiguredMems = "1"
	hierarchy.snapshot.Entries[projectedChildRel] = child
	require.NoError(t, hierarchy.recomputeEvidence())

	operation := hierarchy.cpuOperation(
		projectedRootRel,
		machine.MustParse("0-3"),
		machine.MustParse("0-3"),
	)
	operation.Direction = WriteShrink
	operation.ExpectedChildUnion = machine.MustParse("0-3")
	operation.ExpectedCurrent.Mems = "0-1"
	operation.Target.Mems = "0"
	operation.OwnsMems = true
	operation.WriteMems = true
	before := CloneCompleteSnapshot(hierarchy.snapshot)

	err := hierarchy.applyOperation(operation)

	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Equal(t, "child_union_cpuset.mems", stale.Resource)
	require.Equal(t, before, hierarchy.snapshot)
}

func TestProjectedHierarchyParentShrinkMatchesSafeWriterUnavailableChildSkip(t *testing.T) {
	t.Parallel()
	const (
		rootRel  = "root"
		childRel = "root/dynamic"
	)
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	capabilities := v2Capabilities()
	base := &CompleteSnapshot{
		Capabilities: capabilities,
		Entries: map[string]EntryState{
			rootRel: {
				Rel: rootRel, Identity: rootIdentity,
				CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
				Mems: "0", ConfiguredMems: "0",
			},
		},
		Children: map[string][]ChildRef{
			rootRel: {{Name: "dynamic", Identity: childIdentity}},
		},
		UnavailableChildren: map[string]UnavailableChildEvidence{
			childRel: {
				Identity: childIdentity,
				Reason:   UnavailableChildReasonControllerUnavailable,
			},
		},
		DomainByRel: map[string]DomainID{rootRel: DomainPrimary},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{rootRel}, ExpandedRels: []string{rootRel},
		},
	}
	base.ID = fingerprintSnapshot(base)
	operation := PlanOperation{
		Rel: rootRel, ExpectedIdentity: rootIdentity,
		ExpectedChildren:   ChildrenFingerprint(base.Children[rootRel]),
		ExpectedChildUnion: machine.NewCPUSet(),
		ExpectedCurrent: CPUSetTarget{
			CPUs: machine.MustParse("0-3"), Mems: "0",
		},
		Target: CPUSetTarget{
			CPUs: machine.MustParse("0-1"), Mems: "0",
		},
		Direction: WriteShrink,
	}
	plan := PhasePlan{
		ConvergenceID: "unavailable-child-shrink", Kind: PhaseDrain,
		Base: base, Capabilities: capabilities, Operations: []PlanOperation{operation},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	projected, err := newProjectedHierarchy(base, capabilities)
	require.NoError(t, err)
	require.NoError(t, projected.applyOperation(plan.Operations[0]))

	live := newFakeHierarchyDriver()
	live.capabilities = capabilities
	live.add(rootRel, rootIdentity, "0-3", "0")
	live.add(childRel, childIdentity, "0-1", "0")
	live.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == childRel {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}
	require.NoError(t, newSafeCPUSetWriter(
		live, NewBudgetTracker(ConvergenceBudget{}), &ConvergenceResult{},
	).execute(context.Background(), plan))
	require.Equal(t, live.nodes[rootRel].cpus, projected.snapshot.Entries[rootRel].CPUs)
}
