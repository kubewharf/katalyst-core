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
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCompileFrozenBoundaryV1ClassifiesControlledDirectChildrenAndRelevantHolders(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()

	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)

	require.NoError(t, err)
	require.Equal(t, FrozenBoundaryVersionV1, boundary.Version)
	require.Equal(t, []string{"root"}, boundary.Roots)
	require.Equal(t, []string{"root"}, boundary.ControlledRels)
	require.Equal(t, []ChildRef{{
		Name:     "direct",
		Identity: CgroupIdentity{Device: 1, Inode: 2},
	}}, boundary.ShrinkChildrenByRel["root"])
	require.Equal(t, machine.MustParse("0-3"), boundary.RelevantCPUs)
	require.Equal(t, []string{"root/direct", "root/direct/holder"}, boundary.RelevantCPUHolders)
	require.Equal(t, []string{"root/direct", "root/direct/holder"}, boundary.RetirableCPUHolders)
}

func TestCompileFrozenBoundaryV1ExcludesUnrelatedDynamicSibling(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()

	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)

	require.NoError(t, err)
	require.NotContains(t, boundary.RelevantCPUHolders, "root/direct/unrelated")
}

func TestCompileFrozenBoundaryRejectsRelevantHolderWithoutControlledAncestor(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	const rel = "orphan"
	snapshot.Entries[rel] = EntryState{
		Rel: rel, Identity: CgroupIdentity{Device: 1, Inode: 99},
		CPUs: machine.NewCPUSet(0), ConfiguredCPUs: machine.NewCPUSet(0),
		Mems: "0", ConfiguredMems: "0",
	}
	snapshot.DomainByRel[rel] = DomainPrimary
	snapshot.ScanBoundary.ExpandedRels = append(snapshot.ScanBoundary.ExpandedRels, rel)
	snapshot.ID = fingerprintSnapshot(snapshot)

	_, err := compileFrozenBoundaryV1(snapshot, input, phases)

	require.Error(t, err)
	require.ErrorContains(t, err, "controlled ancestor")
}

func TestEvaluateFrozenBoundaryRejectsRelevantHolderParentEdgeIdentityDrift(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	phases[0].Operations[0].Direction = WriteGrow
	phases[0].Operations[0].Target.CPUs = machine.MustParse("0-4")
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	current := CloneCompleteSnapshot(snapshot)
	current.Children["root"][0].Identity = CgroupIdentity{Device: 1, Inode: 200}
	current.ID = fingerprintSnapshot(current)

	err = evaluateFrozenBoundarySnapshot(boundary, snapshot, current)

	require.Error(t, err)
	require.ErrorContains(t, err, "holder coverage")
}

func TestCompileFrozenBoundaryV1RelevantCPUsCoverAllSemanticInputsAndCompleteOperations(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	input.ExpectedByRel = map[string]machine.CPUSet{"expected": machine.NewCPUSet(4)}
	input.TargetByRel = map[string]machine.CPUSet{"target": machine.NewCPUSet(5)}
	input.ParentSafetyTargetByRel = map[string]machine.CPUSet{"parent-safe": machine.NewCPUSet(6)}
	input.RequiredByRel = map[string]machine.CPUSet{"required": machine.NewCPUSet(7)}
	input.DeferredByRel = map[string]machine.CPUSet{"deferred": machine.NewCPUSet(8)}
	input.PendingRequiredByRel = map[string]machine.CPUSet{"pending": machine.NewCPUSet(9)}
	input.ProtectedPending = machine.NewCPUSet(10)
	phases[0].Operations[0].ExpectedCurrent.CPUs = machine.MustParse("11-12")
	phases[0].Operations[0].Target.CPUs = machine.MustParse("12-13")

	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)

	require.NoError(t, err)
	require.Equal(t, machine.MustParse("4-13"), boundary.RelevantCPUs)
}

func TestValidateFrozenBoundaryRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	boundary.Version = FrozenBoundaryVersion(99)

	require.Error(t, validateFrozenBoundary(boundary, snapshot))
}

func TestCloneFrozenBoundaryIsDeeplyIsolated(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)

	cloned := cloneFrozenBoundary(boundary)
	boundary.Roots[0] = "changed"
	boundary.ControlledRels[0] = "changed"
	boundary.ShrinkChildrenByRel["root"][0].Name = "changed"
	boundary.RelevantCPUHolders[0] = "changed"
	boundary.RetirableCPUHolders[0] = "changed"
	boundary.RelevantCPUs = machine.NewCPUSet(99)

	require.Equal(t, []string{"root"}, cloned.Roots)
	require.Equal(t, []string{"root"}, cloned.ControlledRels)
	require.Equal(t, "direct", cloned.ShrinkChildrenByRel["root"][0].Name)
	require.Equal(t, []string{"root/direct", "root/direct/holder"}, cloned.RelevantCPUHolders)
	require.Equal(t, []string{"root/direct", "root/direct/holder"}, cloned.RetirableCPUHolders)
	require.Equal(t, "0-3", cloned.RelevantCPUs.String())
}

func TestEvaluateFrozenBoundaryAllowsUnrelatedDynamicSiblingChurn(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.nodes["root/direct/unrelated"].configuredCPUs = machine.NewCPUSet(8)
	driver.nodes["root/direct/unrelated"].cpus = machine.NewCPUSet(8)
	driver.nodes["root/direct/unrelated"].configuredMems = "1"
	driver.nodes["root/direct/unrelated"].mems = "1"
	driver.add("root/direct/new-unrelated", CgroupIdentity{Device: 1, Inode: 9}, "9", "1")

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	require.NotNil(t, evaluation.Snapshot)
	require.NotContains(t, evaluation.Snapshot.Entries, "root/direct/new-unrelated")
}

func TestEvaluateFrozenBoundaryRejectsControlledRelDrift(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.nodes["root"].configuredCPUs = machine.MustParse("0-2")
	driver.nodes["root"].cpus = machine.MustParse("0-2")

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
}

func TestEvaluateFrozenBoundaryRejectsShrinkDirectChildAdditionOutsideTarget(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.add("root/new-direct", CgroupIdentity{Device: 1, Inode: 9}, "9", "1")

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
}

func TestEvaluateFrozenBoundaryRejectsShrinkDirectChildUnionDrift(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	addFrozenBoundaryDirectChild(
		snapshot, "ephemeral", CgroupIdentity{Device: 1, Inode: 9}, machine.NewCPUSet(9))
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.nodes["root/ephemeral"].configuredCPUs = machine.NewCPUSet(10)
	driver.nodes["root/ephemeral"].cpus = machine.NewCPUSet(10)

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Contains(t, err.Error(), "shrink direct child CPU union changed")
}

func TestEvaluateFrozenBoundaryAllowsGrowDirectChildRemoval(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	phases[0].Operations[0].Direction = WriteGrow
	phases[0].Operations[0].ExpectedCurrent.CPUs = machine.MustParse("0-1")
	phases[0].Operations[0].Target.CPUs = machine.MustParse("0-3")
	addFrozenBoundaryDirectChild(
		snapshot, "ephemeral", CgroupIdentity{Device: 1, Inode: 9}, machine.NewCPUSet(9))
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	delete(driver.nodes, "root/ephemeral")

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
}

func TestEvaluateFrozenBoundaryRejectsDirectChildStateDrift(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.nodes["root/direct"].configuredMems = "1"
	driver.nodes["root/direct"].mems = "1"

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
}

func TestEvaluateFrozenBoundaryRejectsRelevantCPUHolderDrift(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.nodes["root/direct/holder"].configuredCPUs = machine.NewCPUSet(3)
	driver.nodes["root/direct/holder"].cpus = machine.NewCPUSet(3)

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
}

func TestEvaluateFrozenBoundaryRejectsGrowNewRelevantCPUHolder(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	phases[0].Operations[0].Direction = WriteGrow
	phases[0].Operations[0].ExpectedCurrent.CPUs = machine.MustParse("0-1")
	phases[0].Operations[0].Target.CPUs = machine.MustParse("0-3")
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.add("root/new-holder", CgroupIdentity{Device: 1, Inode: 9}, "2", "0")

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
}

func TestEvaluateFrozenBoundaryAllowsUnreferencedHolderRetirementDuringSnapshot(t *testing.T) {
	t.Parallel()
	for _, retirementOperation := range []HierarchyOperation{
		HierarchyOperationStat,
		HierarchyOperationRead,
	} {
		retirementOperation := retirementOperation
		t.Run(string(retirementOperation), func(t *testing.T) {
			t.Parallel()
			snapshot, input, phases := frozenBoundaryFixture()
			boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
			require.NoError(t, err)
			driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
			deleteHolderDuringRead(
				driver, "root/direct", "root/direct/holder", retirementOperation)

			evaluation, err := EvaluateFrozenBoundary(
				context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
				boundary, snapshot)

			require.NoError(t, err)
			require.NotNil(t, evaluation.Snapshot)
			require.NotContains(t, evaluation.Snapshot.Entries, "root/direct/holder")
			require.NotContains(t, evaluation.Snapshot.Children["root/direct"], ChildRef{
				Name: "holder", Identity: CgroupIdentity{Device: 1, Inode: 3},
			})
		})
	}
}

func TestEvaluateFrozenBoundaryAllowsAuthorizedHolderRetirementDuringList(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationList && rel == "root/direct/holder" {
			delete(driver.nodes, "root/direct/holder")
			return syscall.ENOENT
		}
		return nil
	}

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	assertSnapshotExcludesSubtree(t, evaluation.Snapshot, "root/direct/holder")
}

func TestEvaluateFrozenBoundaryAllowsAuthorizedHolderRetirementAtRecursiveFence(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	stats := 0
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationStat && rel == "root/direct/holder" {
			stats++
			if stats == 3 {
				delete(driver.nodes, rel)
			}
		}
		return nil
	}

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	assertSnapshotExcludesSubtree(t, evaluation.Snapshot, "root/direct/holder")
}

func TestEvaluateFrozenBoundaryDiscardsAuthorizedParentRetiredDuringChildScan(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationRead && rel == "root/direct/holder" {
			delete(driver.nodes, "root/direct")
			delete(driver.nodes, "root/direct/holder")
			delete(driver.nodes, "root/direct/unrelated")
		}
		return nil
	}

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	assertSnapshotExcludesSubtree(t, evaluation.Snapshot, "root/direct")
}

func TestEvaluateFrozenBoundaryRejectsReferencedHolderRetirementDuringSnapshot(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	input.ExpectedByRel = map[string]machine.CPUSet{
		"root/direct/holder": machine.NewCPUSet(2),
	}
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	require.NotContains(t, boundary.RetirableCPUHolders, "root/direct")
	require.NotContains(t, boundary.RetirableCPUHolders, "root/direct/holder")
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	deleteHolderAfterParentListing(driver, "root/direct", "root/direct/holder")

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, syscall.ENOENT)
}

func TestCompileFrozenBoundaryDoesNotRetireTargetOnlyHolder(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	input.TargetByRel = map[string]machine.CPUSet{
		"root/direct/holder": machine.NewCPUSet(2),
	}

	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)

	require.NoError(t, err)
	require.NotContains(t, boundary.RetirableCPUHolders, "root/direct")
	require.NotContains(t, boundary.RetirableCPUHolders, "root/direct/holder")
}

func TestFrozenBoundaryRetirementAuthorizationExcludesControlledRels(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)

	authorizations := frozenBoundaryRetirementAuthorizations(boundary)

	require.NotContains(t, authorizations, "root")
	require.Contains(t, authorizations, "root/direct")
	require.Contains(t, authorizations, "root/direct/holder")
}

func TestFrozenBoundaryRetirementAuthorizationExcludesSharedRequiredPath(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	requiredRel := "root/direct/required"
	requiredIdentity := CgroupIdentity{Device: 1, Inode: 5}
	snapshot.Entries[requiredRel] = EntryState{
		Rel: requiredRel, Identity: requiredIdentity,
		CPUs: machine.NewCPUSet(2), ConfiguredCPUs: machine.NewCPUSet(2),
		Mems: "0", ConfiguredMems: "0",
	}
	snapshot.Children["root/direct"] = append(snapshot.Children["root/direct"], ChildRef{
		Name: "required", Identity: requiredIdentity,
	})
	snapshot.DomainByRel[requiredRel] = DomainPrimary
	snapshot.ScanBoundary.ExpandedRels = append(snapshot.ScanBoundary.ExpandedRels, requiredRel)
	input.ExpectedByRel = map[string]machine.CPUSet{requiredRel: machine.NewCPUSet(2)}

	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	authorizations := frozenBoundaryRetirementAuthorizations(boundary)

	require.Equal(t, map[string]CgroupIdentity{
		"root/direct/holder": snapshot.Entries["root/direct/holder"].Identity,
	}, authorizations)
}

func TestFrozenBoundaryRejectsDuplicateRetirableHolders(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	boundary.RetirableCPUHolders = append(
		boundary.RetirableCPUHolders, boundary.RetirableCPUHolders[0])

	err = validateFrozenBoundary(boundary, snapshot)

	require.ErrorContains(t, err, "duplicate")
}

func TestFrozenBoundaryRejectsControlledRetirableHolder(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	boundary.RetirableCPUHolders = append(boundary.RetirableCPUHolders, "root")

	err = validateFrozenBoundary(boundary, snapshot)

	require.ErrorContains(t, err, "controlled")
}

func TestEvaluateFrozenBoundaryAllowsRetirableShrinkDirectChildRemovalDuringSnapshot(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	addFrozenBoundaryDirectChild(
		snapshot, "retired", CgroupIdentity{Device: 1, Inode: 9}, machine.NewCPUSet(2))
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	require.Contains(t, boundary.RetirableCPUHolders, "root/retired")
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	deleteHolderAfterParentListing(driver, "root", "root/retired")

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	require.NotNil(t, evaluation.Snapshot)
	require.NotContains(t, evaluation.Snapshot.Entries, "root/retired")
	require.NotContains(t, evaluation.Snapshot.Children["root"], ChildRef{
		Name: "retired", Identity: CgroupIdentity{Device: 1, Inode: 9},
	})
}

func TestEvaluateFrozenBoundaryAllowsMultipleRetirementsWithinOneSnapshot(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	secondRel := "root/direct/holder-second"
	secondIdentity := CgroupIdentity{Device: 1, Inode: 5}
	snapshot.Entries[secondRel] = EntryState{
		Rel: secondRel, Identity: secondIdentity,
		CPUs: machine.NewCPUSet(2), ConfiguredCPUs: machine.NewCPUSet(2),
		Mems: "0", ConfiguredMems: "0",
	}
	snapshot.Children["root/direct"] = append(
		snapshot.Children["root/direct"],
		ChildRef{Name: "holder-second", Identity: secondIdentity},
	)
	snapshot.DomainByRel[secondRel] = DomainPrimary
	snapshot.ScanBoundary.ExpandedRels = append(
		snapshot.ScanBoundary.ExpandedRels, secondRel)
	snapshot.ID = fingerprintSnapshot(snapshot)
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	parentLists := 0
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationList && rel == "root/direct" {
			parentLists++
		}
		switch {
		case operation == HierarchyOperationStat &&
			rel == "root/direct/holder" && parentLists == 1:
			delete(driver.nodes, rel)
		case operation == HierarchyOperationStat &&
			rel == secondRel && parentLists == 1:
			delete(driver.nodes, rel)
		}
		return nil
	}

	evaluation, err := EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.NoError(t, err)
	require.NotContains(t, evaluation.Snapshot.Entries, "root/direct/holder")
	require.NotContains(t, evaluation.Snapshot.Entries, secondRel)
	require.Equal(t, 2, parentLists)
}

func TestProjectFrozenBoundaryRetiresNestedAndSiblingUnavailableOnlyDescendants(t *testing.T) {
	t.Parallel()
	expected, input, phases := frozenBoundaryFixture()
	expected.UnavailableChildren = make(map[string]UnavailableChildEvidence)
	expected.Children["root/direct/unrelated"] = nil
	secondRel := "root/direct/holder-second"
	secondIdentity := CgroupIdentity{Device: 1, Inode: 5}
	expected.Entries[secondRel] = EntryState{
		Rel: secondRel, Identity: secondIdentity,
		CPUs: machine.NewCPUSet(2), ConfiguredCPUs: machine.NewCPUSet(2),
		Mems: "0", ConfiguredMems: "0",
	}
	expected.Children["root/direct"] = append(expected.Children["root/direct"], ChildRef{
		Name: "holder-second", Identity: secondIdentity,
	})
	expected.DomainByRel[secondRel] = DomainPrimary
	expected.ScanBoundary.ExpandedRels = append(expected.ScanBoundary.ExpandedRels, secondRel)
	for index, holderRel := range []string{"root/direct/holder", secondRel} {
		unavailableRel := filepath.Join(holderRel, "unavailable")
		unavailableIdentity := CgroupIdentity{Device: 1, Inode: uint64(10 + index)}
		expected.Children[holderRel] = []ChildRef{{
			Name: "unavailable", Identity: unavailableIdentity,
		}}
		expected.UnavailableChildren[unavailableRel] = UnavailableChildEvidence{
			Identity: unavailableIdentity,
			Reason:   UnavailableChildReasonControllerUnavailable,
		}
	}
	expected.DomainUnion = map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3,9"),
	}
	expected.ID = fingerprintSnapshot(expected)
	require.NoError(t, validateCompleteSnapshotEvidence(expected))
	boundary, err := compileFrozenBoundaryV1(expected, input, phases)
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]string{"root/direct", "root/direct/holder", secondRel},
		boundary.RetirableCPUHolders)

	current := CloneCompleteSnapshot(expected)
	for _, holderRel := range []string{"root/direct/holder", secondRel} {
		delete(current.Entries, holderRel)
		delete(current.Children, holderRel)
		delete(current.DomainByRel, holderRel)
		delete(current.UnavailableChildren, filepath.Join(holderRel, "unavailable"))
	}
	current.Children["root/direct"] = []ChildRef{{
		Name:     "unrelated",
		Identity: expected.Entries["root/direct/unrelated"].Identity,
	}}
	current.ScanBoundary.ExpandedRels = []string{"root", "root/direct", "root/direct/unrelated"}
	current.DomainUnion = map[DomainID]machine.CPUSet{
		DomainPrimary: machine.MustParse("0-3,9"),
	}
	require.NoError(t, validateCompleteSnapshotEvidence(current))

	projected := projectFrozenBoundarySnapshot(boundary, expected, current)

	require.NoError(t, validateCompleteSnapshotEvidence(projected))
	assertSnapshotExcludesSubtree(t, projected, "root/direct/holder")
	assertSnapshotExcludesSubtree(t, projected, secondRel)
	require.Contains(t, projected.Entries, "root/direct/unrelated")
}

func TestProjectFrozenBoundaryRetiresOutermostMissingRetirablePath(t *testing.T) {
	t.Parallel()
	expected, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(expected, input, phases)
	require.NoError(t, err)
	boundary.RelevantCPUHolders = []string{"root/direct/holder"}
	boundary.RetirableCPUHolders = []string{"root/direct/holder"}
	boundary.RelevantHolderPaths = map[string][]FrozenRelIdentity{
		"root/direct/holder": boundary.RelevantHolderPaths["root/direct/holder"],
	}
	require.NoError(t, validateFrozenBoundary(boundary, expected))

	current := CloneCompleteSnapshot(expected)
	deleteProjectedSubtree(current, "root/direct")
	current.Children["root"] = nil
	current.DomainUnion = map[DomainID]machine.CPUSet{
		DomainPrimary: expected.Entries["root"].CPUs.Clone(),
	}
	current.ID = fingerprintSnapshot(current)
	require.NoError(t, validateCompleteSnapshotEvidence(current))

	projected := projectFrozenBoundarySnapshot(boundary, expected, current)

	require.NoError(t, validateCompleteSnapshotEvidence(projected))
	assertSnapshotExcludesSubtree(t, projected, "root/direct")
}

func TestDeleteProjectedSubtreeRemovesExpandedRels(t *testing.T) {
	t.Parallel()
	snapshot := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"root":        {Rel: "root"},
			"root/gone":   {Rel: "root/gone"},
			"root/gone/x": {Rel: "root/gone/x"},
		},
		Children:            make(map[string][]ChildRef),
		UnavailableChildren: make(map[string]UnavailableChildEvidence),
		DomainByRel:         make(map[string]DomainID),
		ScanBoundary: ScanBoundary{
			ExpandedRels: []string{"root", "root/gone", "root/gone/x"},
		},
	}

	deleteProjectedSubtree(snapshot, "root/gone")

	require.Equal(t, []string{"root"}, snapshot.ScanBoundary.ExpandedRels)
}

func TestEvaluateFrozenBoundaryDoesNotRetryRetirableHolderIdentityError(t *testing.T) {
	t.Parallel()
	snapshot, input, phases := frozenBoundaryFixture()
	boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
	require.NoError(t, err)
	driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
	parentListed := false
	holderStats := 0
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationList && rel == "root/direct" {
			parentListed = true
		}
		if operation == HierarchyOperationStat && rel == "root/direct/holder" && parentListed {
			holderStats++
			return ErrCgroupIdentityChanged
		}
		return nil
	}

	_, err = EvaluateFrozenBoundary(
		context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
		boundary, snapshot)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
	require.Equal(t, 1, holderStats)
}

func TestEvaluateFrozenBoundaryRejectsUnauthorizedRetirementWindows(t *testing.T) {
	t.Parallel()
	for _, window := range []string{"list", "recursive-fence"} {
		window := window
		t.Run("semantic-"+window, func(t *testing.T) {
			t.Parallel()
			snapshot, input, phases := frozenBoundaryFixture()
			input.ExpectedByRel = map[string]machine.CPUSet{
				"root/direct/holder": machine.NewCPUSet(2),
			}
			boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
			require.NoError(t, err)
			driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
			injectFrozenRetirementWindow(driver, "root/direct/holder", window, false)

			_, err = EvaluateFrozenBoundary(
				context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
				boundary, snapshot)

			require.Error(t, err)
		})

		t.Run("controlled-"+window, func(t *testing.T) {
			t.Parallel()
			snapshot, input, phases := frozenBoundaryFixture()
			boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
			require.NoError(t, err)
			driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
			injectFrozenRetirementWindow(driver, "root", window, false)

			_, err = EvaluateFrozenBoundary(
				context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
				boundary, snapshot)

			require.Error(t, err)
		})

		t.Run("replacement-"+window, func(t *testing.T) {
			t.Parallel()
			snapshot, input, phases := frozenBoundaryFixture()
			boundary, err := compileFrozenBoundaryV1(snapshot, input, phases)
			require.NoError(t, err)
			driver, dag := frozenBoundaryDriver(t, snapshot, input.DAGSpecs)
			injectFrozenRetirementWindow(driver, "root/direct/holder", window, true)

			_, err = EvaluateFrozenBoundary(
				context.Background(), driver, dag, NewBudgetTracker(ConvergenceBudget{}),
				boundary, snapshot)

			require.Error(t, err)
			require.ErrorIs(t, err, ErrCgroupIdentityChanged)
		})
	}
}

func injectFrozenRetirementWindow(
	driver *fakeHierarchyDriver,
	rel, window string,
	replacement bool,
) {
	stats := 0
	driver.beforeCall = func(operation HierarchyOperation, calledRel string) error {
		if calledRel != rel {
			return nil
		}
		trigger := operation == HierarchyOperationList && window == "list"
		if operation == HierarchyOperationStat {
			stats++
			trigger = trigger || window == "recursive-fence" && stats == 3
		}
		if !trigger {
			return nil
		}
		old := driver.nodes[rel]
		delete(driver.nodes, rel)
		if replacement {
			driver.nodes[rel] = &fakeHierarchyNode{
				identity:       CgroupIdentity{Device: old.identity.Device, Inode: old.identity.Inode + 100},
				cpus:           old.cpus.Clone(),
				configuredCPUs: old.configuredCPUs.Clone(),
				mems:           old.mems,
				configuredMems: old.configuredMems,
			}
		}
		if window == "list" {
			return syscall.ENOENT
		}
		return nil
	}
}

func deleteHolderAfterParentListing(
	driver *fakeHierarchyDriver,
	parentRel, holderRel string,
) {
	deleteHolderDuringRead(driver, parentRel, holderRel, HierarchyOperationStat)
}

func deleteHolderDuringRead(
	driver *fakeHierarchyDriver,
	parentRel, holderRel string,
	retirementOperation HierarchyOperation,
) {
	parentListed := false
	retired := false
	holderStats := 0
	driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if operation == HierarchyOperationList && rel == parentRel {
			parentListed = true
			return nil
		}
		if operation == HierarchyOperationStat && rel == holderRel {
			holderStats++
		}
		retireNow := operation == retirementOperation
		if rel == holderRel && parentListed && !retired && retireNow {
			delete(driver.nodes, holderRel)
			retired = true
		}
		return nil
	}
}

func assertSnapshotExcludesSubtree(t *testing.T, snapshot *CompleteSnapshot, retiredRel string) {
	t.Helper()
	require.NotNil(t, snapshot)
	prefix := retiredRel + "/"
	for rel := range snapshot.Entries {
		require.False(t, rel == retiredRel || strings.HasPrefix(rel, prefix), "Entries contains retired rel %q", rel)
	}
	for rel, children := range snapshot.Children {
		require.False(t, rel == retiredRel || strings.HasPrefix(rel, prefix), "Children contains retired parent %q", rel)
		for _, child := range children {
			childRel := filepath.Join(rel, child.Name)
			require.False(t, childRel == retiredRel || strings.HasPrefix(childRel, prefix),
				"Children contains retired child %q", childRel)
		}
	}
	for rel := range snapshot.DomainByRel {
		require.False(t, rel == retiredRel || strings.HasPrefix(rel, prefix), "DomainByRel contains retired rel %q", rel)
	}
	for rel := range snapshot.UnavailableChildren {
		require.False(t, rel == retiredRel || strings.HasPrefix(rel, prefix), "UnavailableChildren contains retired rel %q", rel)
	}
	for _, rel := range snapshot.ScanBoundary.ExpandedRels {
		require.False(t, rel == retiredRel || strings.HasPrefix(rel, prefix), "ExpandedRels contains retired rel %q", rel)
	}
	union := make(map[DomainID]machine.CPUSet)
	for rel, entry := range snapshot.Entries {
		union[snapshot.DomainByRel[rel]] = union[snapshot.DomainByRel[rel]].Union(entry.CPUs)
	}
	require.Equal(t, union, snapshot.DomainUnion)
}

func addFrozenBoundaryDirectChild(
	snapshot *CompleteSnapshot,
	name string,
	identity CgroupIdentity,
	cpus machine.CPUSet,
) {
	rel := "root/" + name
	snapshot.Entries[rel] = EntryState{
		Rel: rel, Identity: identity,
		CPUs: cpus.Clone(), ConfiguredCPUs: cpus.Clone(),
		Mems: "0", ConfiguredMems: "0",
	}
	snapshot.Children["root"] = append(snapshot.Children["root"], ChildRef{
		Name: name, Identity: identity,
	})
	snapshot.DomainByRel[rel] = DomainPrimary
	snapshot.ScanBoundary.ExpandedRels = append(snapshot.ScanBoundary.ExpandedRels, rel)
	snapshot.ID = fingerprintSnapshot(snapshot)
}

func frozenBoundaryDriver(
	t *testing.T,
	snapshot *CompleteSnapshot,
	specs []NodeSpec,
) (*fakeHierarchyDriver, *TopoDAG) {
	t.Helper()
	driver := newFakeHierarchyDriver()
	driver.capabilities = snapshot.Capabilities
	rels := sortedStringKeys(snapshot.Entries)
	for _, rel := range rels {
		entry := snapshot.Entries[rel]
		driver.add(rel, entry.Identity, entry.CPUs.String(), entry.Mems)
		driver.nodes[rel].configuredCPUs = entry.ConfiguredCPUs.Clone()
		driver.nodes[rel].configuredMems = entry.ConfiguredMems
	}
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	return driver, dag
}

func frozenBoundaryFixture() (*CompleteSnapshot, FrozenCoordinatorEvaluationInput, []CompiledPhase) {
	identities := map[string]CgroupIdentity{
		"root":                  {Device: 1, Inode: 1},
		"root/direct":           {Device: 1, Inode: 2},
		"root/direct/holder":    {Device: 1, Inode: 3},
		"root/direct/unrelated": {Device: 1, Inode: 4},
	}
	entries := map[string]EntryState{
		"root": {
			Rel: "root", Identity: identities["root"],
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
			Mems: "0", ConfiguredMems: "0",
		},
		"root/direct": {
			Rel: "root/direct", Identity: identities["root/direct"],
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
			Mems: "0", ConfiguredMems: "0",
		},
		"root/direct/holder": {
			Rel: "root/direct/holder", Identity: identities["root/direct/holder"],
			CPUs: machine.NewCPUSet(2), ConfiguredCPUs: machine.NewCPUSet(2),
			Mems: "0", ConfiguredMems: "0",
		},
		"root/direct/unrelated": {
			Rel: "root/direct/unrelated", Identity: identities["root/direct/unrelated"],
			CPUs: machine.NewCPUSet(9), ConfiguredCPUs: machine.NewCPUSet(9),
			Mems: "0", ConfiguredMems: "0",
		},
	}
	snapshot := &CompleteSnapshot{
		Capabilities: v2Capabilities(),
		Entries:      entries,
		Children: map[string][]ChildRef{
			"root": {{Name: "direct", Identity: identities["root/direct"]}},
			"root/direct": {
				{Name: "holder", Identity: identities["root/direct/holder"]},
				{Name: "unrelated", Identity: identities["root/direct/unrelated"]},
			},
			"root/direct/holder":    nil,
			"root/direct/unrelated": nil,
		},
		DomainByRel: map[string]DomainID{
			"root":                  DomainPrimary,
			"root/direct":           DomainPrimary,
			"root/direct/holder":    DomainPrimary,
			"root/direct/unrelated": DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-3,9"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan,
			Roots:   []string{"root"},
			ExpandedRels: []string{
				"root",
				"root/direct",
				"root/direct/holder",
				"root/direct/unrelated",
			},
		},
	}
	snapshot.ID = fingerprintSnapshot(snapshot)
	input := FrozenCoordinatorEvaluationInput{
		DAGSpecs: []NodeSpec{{
			Rel: "root", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
			ControlledRoot: true,
		}},
		RequiredByRel:           map[string]machine.CPUSet{"root": machine.NewCPUSet(0)},
		ParentSafetyTargetByRel: map[string]machine.CPUSet{"root": machine.MustParse("0-1")},
		Capabilities:            snapshot.Capabilities,
	}
	phases := []CompiledPhase{{
		Kind: PhaseDrain,
		Operations: []PlanOperation{{
			Rel:             "root",
			ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0"},
			Direction:       WriteShrink,
		}},
	}}
	return snapshot, input, phases
}
