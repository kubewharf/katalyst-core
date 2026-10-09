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
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTracePreflightRejectsInitialSnapshotDriftWithoutWrites(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	driver.nodes[operation.Rel].cpus = operation.ExpectedCurrent.CPUs.Union(machine.NewCPUSet(99))
	driver.nodes[operation.Rel].configuredCPUs = driver.nodes[operation.Rel].cpus.Clone()
	initialState := driver.snapshot()

	err := newTracePreflightWriter(driver).preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var drift *frozenInitialSnapshotDriftError
	require.ErrorAs(t, err, &drift)
	require.NotNil(t, drift.current)
	require.NotNil(t, drift.expected)
	require.Zero(t, driver.PhysicalWriteCount())
	require.Equal(t, initialState, driver.snapshot())
}

func TestCheckEngineDeadlinePreservesBudgetAndContextErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	round := &coordinatorRound{
		round:  3,
		budget: NewBudgetTracker(ConvergenceBudget{}),
	}

	err := round.checkEngineDeadline(ctx)

	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "rounds=3")
}

func TestProjectFrozenTraceOperationsHonorsCancellationBoundaries(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	require.NotEmpty(t, flattenTraceOperations(trace))

	t.Run("before projection", func(t *testing.T) {
		t.Parallel()
		projection, err := newProjectedHierarchy(trace.InitialSnapshot, trace.Capabilities)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err = projectFrozenTraceOperations(ctx, trace, projection)

		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, trace.InitialSnapshot.ID, projection.snapshot.ID)
	})

	t.Run("after operation", func(t *testing.T) {
		t.Parallel()
		projection, err := newProjectedHierarchy(trace.InitialSnapshot, trace.Capabilities)
		require.NoError(t, err)
		operation := flattenTraceOperations(trace)[0]
		before := freezeOperationState(projection.snapshot.Entries[operation.Rel])
		ctx := projectionCancellationContext{
			Context: context.Background(),
			canceled: func() bool {
				return !frozenOperationStateEqual(
					projection.snapshot.Entries[operation.Rel], before)
			},
		}

		_, err = projectFrozenTraceOperations(ctx, trace, projection)

		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, projection.evidenceRebuildCount(),
			"cancellation after an operation must stop before settlement")
	})

	t.Run("after settlement", func(t *testing.T) {
		t.Parallel()
		projection, err := newProjectedHierarchy(trace.InitialSnapshot, trace.Capabilities)
		require.NoError(t, err)
		projection.resetEvidenceRebuildCount()
		ctx := projectionCancellationContext{
			Context: context.Background(),
			canceled: func() bool {
				return projection.evidenceRebuildCount() > 0
			},
		}

		_, err = projectFrozenTraceOperations(ctx, trace, projection)

		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, projection.evidenceRebuildCount(),
			"cancellation after settlement must stop before the next frontier")
	})
}

type projectionCancellationContext struct {
	context.Context
	canceled func() bool
}

func (c projectionCancellationContext) Err() error {
	if c.canceled != nil && c.canceled() {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestTracePreflightAllowsUnrelatedDynamicSiblingChurn(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.driver.add(
		"kubepods/besteffort/unrelated-parent",
		CgroupIdentity{Device: 1, Inode: 1000}, "99", "1")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	fixture.driver.add(
		"kubepods/besteffort/unrelated-parent/churn",
		CgroupIdentity{Device: 1, Inode: 1001}, "100", "1")

	err = newTracePreflightWriter(fixture.driver).
		preflightFrozenTrace(context.Background(), trace)

	require.NoError(t, err)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestTraceFinalizationAllowsUnrelatedDynamicSiblingChurn(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.driver.add(
		"kubepods/besteffort/unrelated-parent",
		CgroupIdentity{Device: 1, Inode: 1000}, "99", "1")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	driver, _ := frozenBoundaryDriver(t, trace.FinalSnapshot, trace.EvaluationInput.DAGSpecs)
	driver.add(
		"kubepods/besteffort/unrelated-parent/churn",
		CgroupIdentity{Device: 1, Inode: 1001}, "100", "1")
	round := frozenExecutionRound(t, trace, driver)

	finalization, err := round.proveFrozenTraceFinalState(context.Background(), trace)

	require.NoError(t, err)
	require.NotNil(t, finalization.snapshot)
	require.True(t, finalization.evaluation.ParentSafety.Safe)
}

func TestFrozenTraceAllowsGrowDirectChildRemoval(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
	live.add("root/ephemeral", CgroupIdentity{Device: 1, Inode: 2}, "9", "0")
	operation := PlanOperation{
		Rel: "root", ExpectedIdentity: live.nodes["root"].identity,
		ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0"},
		Target:          CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
		Direction:       WriteGrow,
	}
	before, err := live.ReadEntry(context.Background(), operation.Rel)
	require.NoError(t, err)
	after := before
	after.ConfiguredCPUs = operation.Target.CPUs.Clone()
	after.CPUs = operation.Target.CPUs.Clone()
	child, err := live.ReadEntry(context.Background(), "root/ephemeral")
	require.NoError(t, err)
	delete(live.nodes, "root/ephemeral")
	preflight := frozenOperationPreflight{
		before: freezeOperationState(before),
		after:  freezeOperationState(after),
		children: stableLiveChildren{
			cpus: child.CPUs.Clone(),
			mems: machine.NewCPUSet(0),
			refs: []ChildRef{{
				Name: "ephemeral", Identity: child.Identity,
			}},
			byRel: map[string]EntryState{"root/ephemeral": child},
		},
	}
	writer := newSafeCPUSetWriter(
		live, NewBudgetTracker(ConvergenceBudget{}), nil)

	current, alreadyAtTarget, err := writer.validateFrozenOperationPredecessor(
		context.Background(), operation, preflight)

	require.NoError(t, err)
	require.False(t, alreadyAtTarget)
	require.Equal(t, before.Identity, current.Identity)
	require.Zero(t, live.PhysicalWriteCount())
}

func TestTracePreflightRejectsShrinkDirectChildAdditionOutsideTargetWithoutWrites(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	shrink := traceOperationByDirection(t, trace, WriteShrink)
	fixture.driver.add(
		filepath.Join(shrink.Rel, "outside-target"),
		CgroupIdentity{Device: 1, Inode: 1001}, "99", "1")
	initialState := fixture.driver.snapshot()

	err = newTracePreflightWriter(fixture.driver).
		preflightFrozenTrace(context.Background(), trace)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Contains(t, err.Error(), "shrink relation direct children changed")
	require.Zero(t, fixture.driver.PhysicalWriteCount())
	require.Equal(t, initialState, fixture.driver.snapshot())
}

func TestTracePreflightRejectsGrowNewRelevantCPUHolderWithoutWrites(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	grow := traceOperationByDirection(t, trace, WriteGrow)
	added := grow.Target.CPUs.Difference(grow.ExpectedCurrent.CPUs)
	require.False(t, added.IsEmpty())
	fixture.driver.add(
		filepath.Join(grow.Rel, "new-holder"),
		CgroupIdentity{Device: 1, Inode: 1002}, added.String(), "0")
	initialState := fixture.driver.snapshot()

	err = newTracePreflightWriter(fixture.driver).
		preflightFrozenTrace(context.Background(), trace)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Contains(t, err.Error(), "relevant CPU holder set changed")
	require.Zero(t, fixture.driver.PhysicalWriteCount())
	require.Equal(t, initialState, fixture.driver.snapshot())
}

func TestFrozenTraceGrowIsBackedByEarlierSourceShrinkReadback(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	round := frozenExecutionRound(t, trace, fixture.driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	_, err = round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.NoError(t, err)
	operations := flattenTraceOperations(trace)
	operationByPlanID := make(map[string]PlanOperation, len(operations))
	for _, operation := range operations {
		operationByPlanID[operation.PlanID] = operation
	}
	var released machine.CPUSet
	backedGrow := false
	for _, applied := range res.Journal {
		operation, ok := operationByPlanID[applied.PlanID]
		require.True(t, ok, "journal plan %q must belong to the frozen trace", applied.PlanID)
		switch applied.Direction {
		case WriteShrink:
			require.True(t, applied.Observed.CPUs.Equals(operation.Target.CPUs),
				"source shrink must be read back before execution advances")
			released = released.Union(
				operation.ExpectedCurrent.CPUs.Difference(applied.Observed.CPUs))
		case WriteGrow:
			added := applied.Observed.CPUs.Difference(operation.ExpectedCurrent.CPUs)
			require.False(t, added.IsEmpty())
			if !added.Intersection(released).IsEmpty() {
				require.True(t, added.IsSubsetOf(released),
					"transfer grow of %s exceeds earlier source shrink readback %s",
					added.String(), released.String())
				backedGrow = true
			}
		}
	}
	require.True(t, backedGrow, "fixture must execute a transfer grow after source shrink readback")
}

func TestFrozenTraceTransferGrowWithoutInvocationReleaseFailsBeforeWrite(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	writer := newTracePreflightWriter(fixture.driver)
	preflight, err := writer.preflightFrozenTraceOperations(context.Background(), trace)
	require.NoError(t, err)

	operations := flattenTraceOperations(trace)
	operationIndex := transferGrowOperationIndex(t, trace, operations)
	operation := operations[operationIndex]
	operationPreflight := preflight[operationIndex]
	fixture.driver.invariants = nil
	current := fixture.driver.nodes[operation.Rel]
	current.configuredCPUs = operationPreflight.before.ConfiguredCPUs.Clone()
	current.cpus = operationPreflight.before.EffectiveCPUs.Clone()
	current.configuredMems = operationPreflight.before.ConfiguredMems
	current.mems = operationPreflight.before.EffectiveMems
	if operation.ParentRel != "" {
		parent := fixture.driver.nodes[operation.ParentRel]
		parent.cpus = parent.cpus.Union(operation.Target.CPUs)
		parent.configuredCPUs = parent.cpus.Clone()
	}
	writesBefore := fixture.driver.PhysicalWriteCount()
	cost := physicalWriteCost(operation.ExpectedCurrent, operation.Target, operation.WriteMems)
	ticket := &ExecutionReservationTicket{
		traceID: "transfer-grow-without-release",
		operations: []frozenOperationAuthorization{{
			operation: clonePlanOperation(operation),
		}},
		reserved: ExecutionReservationCost{Forward: cost},
	}

	_, err = writer.applyFrozenOperation(
		context.Background(), PhaseExpand, 0, operation, operationPreflight,
		&traceMutationStack{}, ticket, ticket.traceID)

	require.Error(t, err)
	require.Equal(t, writesBefore, fixture.driver.PhysicalWriteCount(),
		"transfer grow must be rejected before its first physical write")
}

func TestFrozenTraceTransferGrowRejectsSourceRootReacquisitionBeforeWrite(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	writer := newTracePreflightWriter(fixture.driver)
	preflight, err := writer.preflightFrozenTraceOperations(context.Background(), trace)
	require.NoError(t, err)

	operations := flattenTraceOperations(trace)
	operationIndex := transferGrowOperationIndex(t, trace, operations)
	operation := operations[operationIndex]
	operationPreflight := preflight[operationIndex]
	require.NotEmpty(t, operationPreflight.releaseGuards)
	guard := operationPreflight.releaseGuards[0]
	sourceRoot := ""
	specByRel := make(map[string]NodeSpec, len(trace.EvaluationInput.DAGSpecs))
	for _, spec := range trace.EvaluationInput.DAGSpecs {
		specByRel[spec.Rel] = spec
	}
	for _, spec := range trace.EvaluationInput.DAGSpecs {
		parent, hasParent := specByRel[spec.ParentRel]
		if spec.Domain == guard.sourceDomain &&
			(!hasParent || parent.Domain != guard.sourceDomain) {
			sourceRoot = spec.Rel
			break
		}
	}
	require.NotEmpty(t, sourceRoot)
	fixture.driver.invariants = nil
	current := fixture.driver.nodes[operation.Rel]
	current.configuredCPUs = operationPreflight.before.ConfiguredCPUs.Clone()
	current.cpus = operationPreflight.before.EffectiveCPUs.Clone()
	if operation.ParentRel != "" {
		parent := fixture.driver.nodes[operation.ParentRel]
		parent.cpus = parent.cpus.Union(operation.Target.CPUs)
		parent.configuredCPUs = parent.cpus.Clone()
	}
	source := fixture.driver.nodes[sourceRoot]
	source.cpus = source.cpus.Union(guard.cpus)
	source.configuredCPUs = source.cpus.Clone()
	stack := &traceMutationStack{
		releasedByDomain: map[DomainID]map[int]struct{}{},
	}
	addCPUSetToAccumulator(stack.releasedByDomain, guard.sourceDomain, guard.cpus)
	writesBefore := fixture.driver.PhysicalWriteCount()
	cost := physicalWriteCost(operation.ExpectedCurrent, operation.Target, operation.WriteMems)
	ticket := &ExecutionReservationTicket{
		traceID: "transfer-grow-source-reacquired",
		operations: []frozenOperationAuthorization{{
			operation: clonePlanOperation(operation),
		}},
		reserved: ExecutionReservationCost{Forward: cost},
	}

	_, err = writer.applyFrozenOperation(
		context.Background(), PhaseExpand, 0, operation, operationPreflight,
		stack, ticket, ticket.traceID)

	require.Error(t, err)
	require.Equal(t, writesBefore, fixture.driver.PhysicalWriteCount(),
		"source root reacquisition must be rejected before the transfer grow writes")
}

func TestCompileFrozenGrowReleaseGuardsRejectsCrossDomainGrowWithoutSourceShrink(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	operations := flattenTraceOperations(trace)
	growIndex := transferGrowOperationIndex(t, trace, operations)
	grow := operations[growIndex]
	destination := trace.InitialSnapshot.DomainByRel[grow.Rel]
	added := grow.Target.CPUs.Difference(grow.ExpectedCurrent.CPUs)

	modified := *trace
	modified.Phases = cloneCompiledPhases(trace.Phases)
	for phaseIndex := range modified.Phases {
		filtered := modified.Phases[phaseIndex].Operations[:0]
		for _, operation := range modified.Phases[phaseIndex].Operations {
			source := trace.InitialSnapshot.DomainByRel[operation.Rel]
			released := operation.ExpectedCurrent.CPUs.Difference(operation.Target.CPUs)
			if operation.Direction == WriteShrink && source != destination &&
				!released.Intersection(added).IsEmpty() {
				continue
			}
			filtered = append(filtered, operation)
		}
		modified.Phases[phaseIndex].Operations = filtered
	}

	_, err = compileFrozenGrowReleaseGuards(context.Background(), &modified)

	require.Error(t, err)
	require.ErrorContains(t, err, "source shrink coverage")
}

func TestCompileFrozenGrowReleaseGuardsRejectsSourceDomainStillHoldingCPU(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	operations := flattenTraceOperations(trace)
	growIndex := transferGrowOperationIndex(t, trace, operations)
	grow := operations[growIndex]
	added := grow.Target.CPUs.Difference(grow.ExpectedCurrent.CPUs)
	sourceDomain := DomainID("")
	for sourceIndex := 0; sourceIndex < growIndex; sourceIndex++ {
		source := operations[sourceIndex]
		released := source.ExpectedCurrent.CPUs.Difference(source.Target.CPUs)
		if source.Direction == WriteShrink && !released.Intersection(added).IsEmpty() {
			sourceDomain = trace.InitialSnapshot.DomainByRel[source.Rel]
			break
		}
	}
	require.NotEmpty(t, sourceDomain)

	modified := *trace
	modified.InitialSnapshot = CloneCompleteSnapshot(trace.InitialSnapshot)
	const holderRel = "unreleased-source-holder"
	holderIdentity := CgroupIdentity{Device: 1, Inode: 9001}
	modified.InitialSnapshot.Entries[holderRel] = EntryState{
		Rel: holderRel, Identity: holderIdentity,
		CPUs: added.Clone(), ConfiguredCPUs: added.Clone(),
		Mems: "0", ConfiguredMems: "0",
	}
	modified.InitialSnapshot.DomainByRel[holderRel] = sourceDomain
	modified.InitialSnapshot.ScanBoundary.Roots = append(modified.InitialSnapshot.ScanBoundary.Roots, holderRel)
	modified.InitialSnapshot.DomainUnion[sourceDomain] = modified.InitialSnapshot.DomainUnion[sourceDomain].Union(added)
	modified.InitialSnapshot.ID = fingerprintSnapshot(modified.InitialSnapshot)

	_, err = compileFrozenGrowReleaseGuards(context.Background(), &modified)

	require.Error(t, err)
	require.ErrorContains(t, err, "source domain release")
}

func transferGrowOperationIndex(
	t *testing.T,
	trace *CompiledPhaseTrace,
	operations []PlanOperation,
) int {
	t.Helper()
	for growIndex, grow := range operations {
		if grow.Direction != WriteGrow {
			continue
		}
		added := grow.Target.CPUs.Difference(grow.ExpectedCurrent.CPUs)
		destination := trace.InitialSnapshot.DomainByRel[grow.Rel]
		for sourceIndex := 0; sourceIndex < growIndex; sourceIndex++ {
			source := operations[sourceIndex]
			if source.Direction != WriteShrink ||
				trace.InitialSnapshot.DomainByRel[source.Rel] == destination {
				continue
			}
			released := source.ExpectedCurrent.CPUs.Difference(source.Target.CPUs)
			if !released.Intersection(added).IsEmpty() {
				return growIndex
			}
		}
	}
	t.Fatal("trace has no transfer grow backed by an earlier cross-domain shrink")
	return -1
}

func traceOperationByDirection(
	t *testing.T,
	trace *CompiledPhaseTrace,
	direction WriteDirection,
) PlanOperation {
	t.Helper()
	for _, operation := range flattenTraceOperations(trace) {
		if operation.Direction == direction {
			return operation
		}
	}
	t.Fatalf("trace has no %s operation", direction)
	return PlanOperation{}
}

func TestTracePreflightWrapsRelevantHolderRemovalAsInitialDriftWithoutWrites(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	const removed = "kubepods/runtime-created"
	fixture.driver.add(removed, CgroupIdentity{Device: 1, Inode: 1000}, "1-3", "0")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	require.Contains(t, trace.InitialSnapshot.ScanBoundary.ExpandedRels, removed)
	for rel := range fixture.driver.nodes {
		if rel == removed || strings.HasPrefix(rel, removed+"/") {
			delete(fixture.driver.nodes, rel)
		}
	}
	initialState := fixture.driver.snapshot()

	err = newTracePreflightWriter(fixture.driver).preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var drift *frozenInitialSnapshotDriftError
	require.ErrorAs(t, err, &drift)
	require.Contains(t, err.Error(), "relevant CPU holder set changed")
	require.NotEqual(t, drift.currentEvidenceID, drift.expected.ID)
	require.Equal(t, 0, drift.physicalWritesBefore)
	require.Equal(t, 0, drift.physicalWritesAfter)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
	require.Equal(t, initialState, fixture.driver.snapshot())
}

func TestTracePreflightWrapsVanishedSnapshotEntryAsInitialDriftWithoutWrites(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	delete(driver.nodes, operation.Rel)
	initialState := driver.snapshot()

	err := newTracePreflightWriter(driver).preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var drift *frozenInitialSnapshotDriftError
	require.ErrorAs(t, err, &drift)
	require.Contains(t, err.Error(), `class=stale: no such file or directory`)
	require.NotEqual(t, drift.currentEvidenceID, drift.expected.ID)
	require.Equal(t, 0, drift.physicalWritesBefore)
	require.Equal(t, 0, drift.physicalWritesAfter)
	require.Zero(t, driver.PhysicalWriteCount())
	require.Equal(t, initialState, driver.snapshot())
}

func TestTracePreflightReadsInvocationPhysicalWriteCountBeforeClassifyingDrift(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	const removed = "kubepods/runtime-created"
	fixture.driver.add(removed, CgroupIdentity{Device: 1, Inode: 1000}, "1-3", "0")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	delete(fixture.driver.nodes, removed)

	writer := newTracePreflightWriter(fixture.driver)
	*writer.physicalWriteAttempts = 1
	err = writer.preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var drift *frozenInitialSnapshotDriftError
	require.False(t, errors.As(err, &drift))
}

func TestTracePreflightSnapshotIdentityDriftAfterPhysicalWriteFailsClosed(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	driver.nodes[operation.Rel].cpus = operation.ExpectedCurrent.CPUs.Union(machine.NewCPUSet(99))
	driver.nodes[operation.Rel].configuredCPUs = driver.nodes[operation.Rel].cpus.Clone()
	writer := newTracePreflightWriter(driver)
	*writer.physicalWriteAttempts = 1

	err := writer.preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	var drift *frozenInitialSnapshotDriftError
	require.False(t, errors.As(err, &drift))
	require.Contains(t, err.Error(), "physical_writes_before=1")
	require.Contains(t, err.Error(), "physical_writes_after=1")
}

func TestVerifiedRollbackAuthorizesReplan(t *testing.T) {
	t.Parallel()

	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	drift := &frozenFinalSnapshotDriftError{
		stale: &PlanStaleError{
			Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
			Err: fmt.Errorf("test final snapshot drift"),
		},
	}
	writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)
	res := &ConvergenceResult{Applied: 3, Journal: []AppliedPlanOperation{{PlanID: "seed"}}}
	res.recordForwardWriteAttempt()
	err := writer.failFrozenTrace(
		context.Background(), drift, stack, rollbackOnlyTicket(1, 0), res, 1, 3)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Equal(t, ReplanSafeAfterVerifiedRollback, res.ReplanDisposition)
	require.Equal(t, machine.MustParse("0"), live.nodes["a"].cpus)
	require.Equal(t, 3, res.Applied)
	require.Len(t, res.Journal, 1)
}

func TestRollbackFailurePreservesStaleCauseButDisallowsReplan(t *testing.T) {
	t.Parallel()

	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	initial := trace.InitialSnapshot.Entries[operation.Rel]
	badIdentity := initial.Identity
	badIdentity.Inode++
	stack := &traceMutationStack{writes: []AppliedPhysicalWrite{{
		PlanID: "final-drift", Rel: operation.Rel, Identity: badIdentity,
		Direction: operation.Direction, Resource: HierarchyOperationWriteCPUs,
		Before: initial.ConfiguredCPUs.String(), BeforeEffective: initial.CPUs.String(),
		After: operation.Target.CPUs.String(), AfterEffective: operation.Target.CPUs.String(),
		Impact: PhysicalImpactConfirmed,
	}}}
	drift := &frozenFinalSnapshotDriftError{stale: &PlanStaleError{
		Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
		Err: fmt.Errorf("test final snapshot drift"),
	}}
	writer := safeCPUSetWriter{driver: live}
	res := &ConvergenceResult{}

	err := writer.failFrozenTrace(context.Background(), drift, stack, nil, res, 0, 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), "rollback failed")
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Same(t, drift.stale, stale)
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
	require.NotEmpty(t, res.Journal,
		"unverified rollback must retain physical-impact evidence")
}

func TestRollbackVerificationFailureDisallowsReplan(t *testing.T) {
	t.Parallel()

	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	identity := CgroupIdentity{Device: 1, Inode: 1}
	live.add("a", identity, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	verificationErr := errors.New("injected rollback verification failure")
	driver := &failNthEntryReadDriver{
		HierarchyDriver: live,
		rel:             "a",
		failAt:          2,
		err:             verificationErr,
	}
	res := &ConvergenceResult{}
	res.recordForwardWriteAttempt()
	writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), res)
	drift := &frozenFinalSnapshotDriftError{stale: &PlanStaleError{
		Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
		Err: fmt.Errorf("test final snapshot drift"),
	}}

	err := writer.failFrozenTrace(
		context.Background(), drift, stack, rollbackOnlyTicket(1, 0), res, 0, 0)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.ErrorIs(t, err, verificationErr)
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
}

func TestPartialRollbackDisallowsReplan(t *testing.T) {
	t.Parallel()

	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	live.add("b", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	applyCPUWriteForRollbackTest(t, live, stack, "b", "1", "1-2")
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection:       traceFailureInjection{failRollbackWriteAt: 1},
		injected:        true,
	}
	res := &ConvergenceResult{}
	res.recordForwardWriteAttempt()
	res.recordForwardWriteAttempt()
	writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), res)
	drift := &frozenFinalSnapshotDriftError{stale: &PlanStaleError{
		Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
		Err: fmt.Errorf("test final snapshot drift"),
	}}

	err := writer.failFrozenTrace(
		context.Background(), drift, stack, rollbackOnlyTicket(2, 0), res, 0, 0)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.ErrorContains(t, err, "rollback failed")
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
	require.NotEmpty(t, res.Journal)
}

func TestUnknownFailureAfterVerifiedRollbackDisallowsReplan(t *testing.T) {
	t.Parallel()

	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	res := &ConvergenceResult{}
	res.recordForwardWriteAttempt()
	writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), res)

	err := writer.failFrozenTrace(
		context.Background(), errors.New("unknown execution failure"), stack,
		rollbackOnlyTicket(1, 0), res, 0, 0)

	require.ErrorContains(t, err, "unknown execution failure")
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
}

func TestFinalBoundaryOperationalFailuresAreNotMarkedAsFrozenFinalDrift(t *testing.T) {
	t.Parallel()
	readFailure := errors.New("injected final boundary read failure")
	tests := []struct {
		name      string
		prepare   func(*coordinatorRound, *CompiledPhaseTrace) context.Context
		wantError error
	}{
		{
			name: "deadline",
			prepare: func(_ *coordinatorRound, _ *CompiledPhaseTrace) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantError: context.Canceled,
		},
		{
			name: "budget",
			prepare: func(round *coordinatorRound, _ *CompiledPhaseTrace) context.Context {
				round.budget = NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
				return context.Background()
			},
			wantError: ErrHierarchyIOOperationBudgetExceeded,
		},
		{
			name: "read",
			prepare: func(round *coordinatorRound, trace *CompiledPhaseTrace) context.Context {
				round.driver = &readErrorHierarchyDriver{
					HierarchyDriver: round.driver,
					err:             readFailure,
					rel:             trace.FrozenBoundary.ControlledRels[0],
				}
				return context.Background()
			},
			wantError: readFailure,
		},
		{
			name: "invalid boundary",
			prepare: func(_ *coordinatorRound, trace *CompiledPhaseTrace) context.Context {
				trace.FrozenBoundary.Version = FrozenBoundaryVersion(99)
				return context.Background()
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
			round := frozenExecutionRound(t, trace, driver)
			ctx := tt.prepare(round, trace)

			_, err := round.proveFrozenTraceFinalState(ctx, trace)

			require.Error(t, err)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
			}
			var drift *frozenFinalSnapshotDriftError
			require.False(t, errors.As(err, &drift))
			require.False(t, replanRequired(err))
		})
	}
}

func TestTracePreflightRejectsIdentityDriftWithoutWrites(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	driver.bumpIdentity(flattenTraceOperations(trace)[0].Rel)
	initialState := driver.snapshot()

	err := newTracePreflightWriter(driver).preflightFrozenTrace(context.Background(), trace)

	require.Error(t, err)
	require.Zero(t, driver.PhysicalWriteCount())
	require.Equal(t, initialState, driver.snapshot())
}

func TestTracePreflightValidatesLaterOperationsAgainstOverlay(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	operations := flattenTraceOperations(trace)
	var parentOperation, childOperation *PlanOperation
	seen := make(map[string]*PlanOperation)
	for i := range operations {
		operation := &operations[i]
		if parent := seen[operation.ParentRel]; parent != nil {
			initialParent := trace.InitialSnapshot.Entries[operation.ParentRel]
			if !operation.Target.CPUs.IsSubsetOf(initialParent.CPUs) {
				parentOperation = parent
				childOperation = operation
				break
			}
		}
		seen[operation.Rel] = operation
	}
	require.NotNil(t, parentOperation, "fixture must contain an earlier parent grow")
	require.NotNil(t, childOperation, "fixture must contain a child invalid against the raw snapshot")
	initialParent := trace.InitialSnapshot.Entries[childOperation.ParentRel]
	require.False(t, childOperation.Target.CPUs.IsSubsetOf(initialParent.CPUs),
		"counterexample requires raw-snapshot validation to reject the child")
	require.True(t, childOperation.Target.CPUs.IsSubsetOf(parentOperation.Target.CPUs),
		"counterexample requires the earlier parent target to authorize the child")
	initialState := driver.snapshot()

	err := newTracePreflightWriter(driver).preflightFrozenTrace(context.Background(), trace)

	require.NoError(t, err)
	require.Zero(t, driver.PhysicalWriteCount())
	require.Equal(t, initialState, driver.snapshot())
}

func TestTracePreflightProducesEveryProjectedPhysicalPredecessorIncludingV2EmptyConfigured(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	candidate, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	candidateOperations := flattenTraceOperations(candidate)
	require.NotEmpty(t, candidateOperations)
	inheritedRel := candidateOperations[0].Rel
	fixture.driver.nodes[inheritedRel].configuredCPUs = machine.NewCPUSet()
	fixture.driver.nodes[inheritedRel].configuredMems = ""
	base = fixture.snapshot()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	fixture.round.round = 0

	evidence, err := newTracePreflightWriter(fixture.driver).
		preflightFrozenTraceOperations(context.Background(), trace)

	require.NoError(t, err)
	require.Len(t, evidence, len(flattenTraceOperations(trace)))
	foundInherited := false
	for index, operation := range flattenTraceOperations(trace) {
		require.Equal(t, operation.ExpectedIdentity, evidence[index].before.Identity)
		require.Equal(t, operation.ExpectedIdentity, evidence[index].after.Identity)
		if operation.Rel != inheritedRel {
			continue
		}
		foundInherited = true
		require.True(t, evidence[index].before.ConfiguredCPUs.IsEmpty())
		require.Empty(t, evidence[index].before.ConfiguredMems)
		require.False(t, evidence[index].before.EffectiveCPUs.IsEmpty())
		require.NotEmpty(t, evidence[index].before.EffectiveMems)
	}
	require.True(t, foundInherited,
		"fixture must retain the v2 inherited relation in the global trace")
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestTracePreflightRejectsInvalidV2InheritanceWithoutWrites(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	operation := operationWithParent(t, trace)
	parent := trace.InitialSnapshot.Entries[operation.ParentRel]
	operation.Target.CPUs = parent.CPUs.Union(machine.NewCPUSet(99))
	operation.Direction = WriteGrow
	replaceTraceOperation(t, trace, operation)
	boundary, err := compileFrozenBoundaryV1(
		trace.InitialSnapshot, trace.EvaluationInput, trace.Phases,
	)
	require.NoError(t, err)
	trace.FrozenBoundary = boundary
	initialState := driver.snapshot()

	err = newTracePreflightWriter(driver).preflightFrozenTrace(context.Background(), trace)

	require.ErrorIs(t, err, ErrProjectedParentContainment)
	require.ErrorContains(t, err, "validate frozen phase trace operation")
	require.Zero(t, driver.PhysicalWriteCount())
	require.Equal(t, initialState, driver.snapshot())
}

func TestPreWriteStaleAuthorizesReplanWithoutRollback(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	initial := live.snapshot()
	driver := newPreWriteDriftDriver(live, trace.InitialSnapshot, 0, "")
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Zero(t, driver.forwardWrites,
		"state drift after preflight must be rejected before the first physical write")
	require.Zero(t, ticket.consumedForward.Total())
	require.NotEmpty(t, driver.driftedRel)
	driftedState := live.snapshot()
	delete(initial, driver.driftedRel)
	delete(driftedState, driver.driftedRel)
	require.Equal(t, initial, driftedState,
		"executor must not change any relation after rejecting external drift")
	require.Empty(t, res.Journal)
	require.Zero(t, res.Applied)
	require.Equal(t, ReplanSafeNoPhysicalWrites, res.ReplanDisposition)
}

func TestFrozenTraceDriftBetweenOperationsRollsBackAppliedPrefix(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	operations := flattenTraceOperations(trace)
	require.GreaterOrEqual(t, len(operations), 2)
	nextRel := ""
	for _, operation := range operations[1:] {
		if operation.Rel != operations[0].Rel {
			nextRel = operation.Rel
			break
		}
	}
	require.NotEmpty(t, nextRel, "fixture needs a later operation on another relation")
	initial := live.snapshot()
	driver := newPreWriteDriftDriver(live, trace.InitialSnapshot, 1, nextRel)
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Positive(t, driver.forwardWrites,
		"the first operation must be applied before the injected inter-operation drift")
	driftedState := live.snapshot()
	delete(initial, driver.driftedRel)
	delete(driftedState, driver.driftedRel)
	require.Equal(t, initial, driftedState,
		"the completed physical-write prefix must be rolled back; only external drift may remain")
	require.Empty(t, res.Journal)
	require.Zero(t, res.Applied)
	require.Positive(t, ticket.consumedRollback.Total())
}

func TestFrozenTraceValidatesCompletePredecessorBeforeOperationFirstWrite(t *testing.T) {
	t.Parallel()
	type driftCase struct {
		name     string
		selectOp func(PlanOperation, *CompiledPhaseTrace) bool
		mutate   func(*fakeHierarchyDriver, PlanOperation)
	}
	tests := []driftCase{
		{
			name:     "current identity",
			selectOp: func(PlanOperation, *CompiledPhaseTrace) bool { return true },
			mutate: func(live *fakeHierarchyDriver, operation PlanOperation) {
				live.bumpIdentity(operation.Rel)
			},
		},
		{
			name: "parent identity",
			selectOp: func(operation PlanOperation, _ *CompiledPhaseTrace) bool {
				return operation.ParentRel != ""
			},
			mutate: func(live *fakeHierarchyDriver, operation PlanOperation) {
				live.bumpIdentity(operation.ParentRel)
			},
		},
		{
			name: "parent no longer contains grow target",
			selectOp: func(operation PlanOperation, _ *CompiledPhaseTrace) bool {
				return operation.Direction == WriteGrow && operation.ParentRel != ""
			},
			mutate: func(live *fakeHierarchyDriver, operation PlanOperation) {
				parent := live.nodes[operation.ParentRel]
				parent.cpus = parent.cpus.Difference(
					operation.Target.CPUs.Difference(operation.ExpectedCurrent.CPUs))
				parent.configuredCPUs = parent.cpus.Clone()
			},
		},
		{
			name: "CPU resource before mems write",
			selectOp: func(operation PlanOperation, _ *CompiledPhaseTrace) bool {
				return operation.WriteMems &&
					!operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs)
			},
			mutate: func(live *fakeHierarchyDriver, operation PlanOperation) {
				live.nodes[operation.Rel].configuredCPUs = live.nodes[operation.Rel].configuredCPUs.Union(machine.NewCPUSet(99))
				live.nodes[operation.Rel].cpus = live.nodes[operation.Rel].cpus.Union(machine.NewCPUSet(99))
			},
		},
		{
			name: "mems resource before CPU write",
			selectOp: func(operation PlanOperation, _ *CompiledPhaseTrace) bool {
				return !operation.WriteMems &&
					!operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs)
			},
			mutate: func(live *fakeHierarchyDriver, operation PlanOperation) {
				live.nodes[operation.Rel].configuredMems = "0-99"
				live.nodes[operation.Rel].mems = "0-99"
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trace, live := compiledTraceWithCPUAndMemoryWrites(t)
			live.invariants = nil
			operation := selectTraceOperation(t, trace, tt.selectOp)
			driver := newFrozenPredecessorDriftDriver(live, trace.InitialSnapshot, operation, tt.mutate)
			round := frozenExecutionRound(t, trace, driver)
			ticket := reserveTraceWithBudget(t, round.budget, trace)
			res := &ConvergenceResult{}

			_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

			var stale *PlanStaleError
			require.ErrorAs(t, err, &stale)
			require.True(t, driver.drifted)
			require.Zero(t, driver.writesToDriftedOperation,
				"the drifted operation must perform no physical write")
		})
	}
}

func TestFrozenTraceV2EmptyConfiguredTargetIsNoOpWithoutForwardTicket(t *testing.T) {
	t.Parallel()
	writer, live, operation, ticket := v2InheritedRollbackFixture(
		t, HierarchyOperationWriteCPUs)
	operation.ExpectedCurrent.CPUs = machine.MustParse("0-1")
	operation.Target.CPUs = machine.NewCPUSet()
	ticket.traceID = "v2-empty"
	ticket.operations = []frozenOperationAuthorization{{operation: operation}}
	ticket.reserved.Forward.CPUSetWrites = 1
	current, err := live.ReadEntry(context.Background(), operation.Rel)
	require.NoError(t, err)
	preflight := frozenOperationPreflight{
		before: frozenOperationState{
			Identity:       current.Identity,
			ConfiguredCPUs: machine.MustParse("0-1"),
			EffectiveCPUs:  machine.MustParse("0-1"),
			ConfiguredMems: current.ConfiguredMems,
			EffectiveMems:  current.Mems,
		},
		after: freezeOperationState(current),
	}
	stack := &traceMutationStack{}

	applied, err := writer.applyFrozenOperation(
		context.Background(), PhaseDrain, 0, operation, preflight, stack, ticket, "v2-empty")

	require.NoError(t, err)
	require.Equal(t, operation.PlanID, applied.PlanID)
	require.Zero(t, live.PhysicalWriteCount())
	require.Zero(t, ticket.consumedForward.Total())
	require.Empty(t, stack.writes)
	require.True(t, live.nodes[operation.Rel].configuredCPUs.IsEmpty())
	require.Equal(t, "0-3", live.nodes[operation.Rel].cpus.String())
}

func TestFrozenTraceFailureRollsBackCompleteAppliedPrefix(t *testing.T) {
	t.Parallel()
	type failureCase struct {
		name         string
		traceFactory func(*testing.T) (*CompiledPhaseTrace, *fakeHierarchyDriver)
		point        func([]expectedTraceWrite) traceFailureInjection
	}
	tests := []failureCase{
		{
			name: "second operation memory write",
			point: func(writes []expectedTraceWrite) traceFailureInjection {
				return failAtNthResourceWrite(t, writes, HierarchyOperationWriteMems, 2)
			},
		},
		{
			name: "second operation CPU write",
			point: func(writes []expectedTraceWrite) traceFailureInjection {
				injection := failAtNthResourceWrite(t, writes, HierarchyOperationWriteCPUs, 2)
				injection.mutateThenFailWriteAt = injection.failWriteAt
				injection.failWriteAt = 0
				return injection
			},
		},
		{
			name:         "later drain frontier",
			traceFactory: compiledMultiFrontierTrace,
			point: func(writes []expectedTraceWrite) traceFailureInjection {
				return failAtPhaseWrite(t, writes, PhaseDrain, -1)
			},
		},
		{
			name: "first expand operation",
			point: func(writes []expectedTraceWrite) traceFailureInjection {
				return failAtPhaseWrite(t, writes, PhaseExpand, 0)
			},
		},
		{
			name: "middle expand operation",
			point: func(writes []expectedTraceWrite) traceFailureInjection {
				return failAtPhaseWrite(t, writes, PhaseExpand, 1)
			},
		},
		{name: "post-write read-back", point: func([]expectedTraceWrite) traceFailureInjection {
			return traceFailureInjection{failPostWriteReadback: true}
		}},
		{name: "false-success write", point: func([]expectedTraceWrite) traceFailureInjection {
			return traceFailureInjection{falseSuccessWriteAt: 1}
		}},
		{name: "final ParentSafe proof", point: func([]expectedTraceWrite) traceFailureInjection {
			return traceFailureInjection{failFinalProof: true}
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			traceFactory := tt.traceFactory
			if traceFactory == nil {
				traceFactory = compiledTraceWithCPUAndMemoryWrites
			}
			trace, live := traceFactory(t)
			initial := live.snapshot()
			// The compiled fixture intentionally starts with one transient v2
			// inheritance shape that the fake invariant rejects when restored.
			// Rollback tests exercise executor ordering, not that synthetic
			// fixture invariant.
			live.invariants = nil
			writes := expectedPhysicalWrites(trace)
			require.NotEmpty(t, writes)
			injection := tt.point(writes)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			driver := &injectedTraceDriver{
				HierarchyDriver: live,
				injection:       injection,
				expectedForward: len(writes),
				cancel:          cancel,
			}
			round := frozenExecutionRound(t, trace, driver)
			ticket := reserveTraceWithBudget(t, round.budget, trace)
			seedJournal := []AppliedPlanOperation{{PlanID: "before-invocation"}}
			res := &ConvergenceResult{
				Applied:              7,
				Journal:              append([]AppliedPlanOperation(nil), seedJournal...),
				ParentSafe:           true,
				Converged:            true,
				FinalSnapshotCurrent: true,
			}

			_, err := round.executeFrozenTrace(ctx, trace, ticket, res)

			require.Error(t, err)
			require.True(t, driver.injected, "failure point was not reached")
			require.Equal(t, initial, live.snapshot())
			require.Equal(t, seedJournal, res.Journal)
			require.Equal(t, 7, res.Applied)
			require.False(t, res.ParentSafe)
			require.False(t, res.Converged)
			require.False(t, res.FinalSnapshotCurrent)
			if injection.failWriteAt != 1 && injection.falseSuccessWriteAt == 0 {
				require.Positive(t, ticket.consumedRollback.Total())
			}
		})
	}
}

func TestExecuteValidatedFrozenTraceRollsBackDedicatedReclaimReplacement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		phase      PhaseKind
		phaseIndex int
	}{
		{name: "first drain", phase: PhaseDrain, phaseIndex: 0},
		{name: "non-first drain", phase: PhaseDrain, phaseIndex: 1},
		{name: "first expand", phase: PhaseExpand, phaseIndex: 0},
		{name: "non-first expand", phase: PhaseExpand, phaseIndex: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trace, live := compiledDedicatedReclaimReplacementTrace(t)
			initial := live.snapshot()
			writes := expectedPhysicalWrites(trace)
			require.GreaterOrEqual(t, countTraceWritesByPhase(writes, PhaseDrain), 2)
			require.GreaterOrEqual(t, countTraceWritesByPhase(writes, PhaseExpand), 2)
			injection := failAtPhaseWrite(t, writes, tt.phase, tt.phaseIndex)
			driver := &injectedTraceDriver{
				HierarchyDriver: live,
				injection:       injection,
				expectedForward: len(writes),
			}
			round := frozenExecutionRound(t, trace, driver)
			ticket := reserveTraceWithBudget(t, round.budget, trace)
			res := &ConvergenceResult{}

			_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

			require.Error(t, err)
			require.True(t, driver.injected)
			require.Equal(t,
				expectedWriteAttempts(writes, injection.failWriteAt),
				driver.writeAttempts,
				"runtime rel/resource/target order must follow the compiled drain/expand trace")
			assertEveryHierarchyNodeEqual(t, initial, live.snapshot())
			require.Empty(t, res.Journal)
			require.Zero(t, res.Applied)
			if injection.failWriteAt == 1 {
				require.Zero(t, ticket.consumedRollback.Total(),
					"a failure before the first physical mutation has no rollback prefix")
			} else {
				require.Positive(t, ticket.consumedRollback.Total())
			}
		})
	}
}

func TestExecuteValidatedFrozenTraceReportsReplacementRollbackFailure(t *testing.T) {
	t.Parallel()
	probeTrace, _ := compiledDedicatedReclaimReplacementTrace(t)
	probeWrites := expectedPhysicalWrites(probeTrace)
	forwardFailures := []struct {
		name       string
		phase      PhaseKind
		phaseIndex int
	}{
		{name: "first drain", phase: PhaseDrain, phaseIndex: 0},
		{name: "non-first drain", phase: PhaseDrain, phaseIndex: 1},
		{name: "first expand", phase: PhaseExpand, phaseIndex: 0},
		{name: "non-first expand", phase: PhaseExpand, phaseIndex: 1},
	}
	var coveredExpandInverseWithEarlierDrainThirdState uint32
	var matrixCases int32
	t.Cleanup(func() {
		require.Equal(t, int32(6), atomic.LoadInt32(&matrixCases),
			"four forward failure points must cover every non-empty applied rollback prefix")
		require.True(t, atomic.LoadUint32(&coveredExpandInverseWithEarlierDrainThirdState) != 0,
			"fixture must exercise an Expand inverse failure followed by an earlier Drain third-state")
	})

	for _, forward := range forwardFailures {
		forward := forward
		forwardFailure := failAtPhaseWrite(t, probeWrites, forward.phase, forward.phaseIndex)
		successfulForward := probeWrites[:forwardFailure.failWriteAt-1]
		for forwardIndex := range successfulForward {
			forwardIndex := forwardIndex
			atomic.AddInt32(&matrixCases, 1)
			failedForward := successfulForward[forwardIndex]
			t.Run(fmt.Sprintf("%s/rollback-%02d-%s-%s",
				forward.name, forwardIndex, failedForward.phase, failedForward.rel), func(t *testing.T) {
				t.Parallel()
				trace, live := compiledDedicatedReclaimReplacementTrace(t)
				initial := live.snapshot()
				writes := expectedPhysicalWrites(trace)
				injection := failAtPhaseWrite(t, writes, forward.phase, forward.phaseIndex)
				injection.failRollbackWriteAt = len(successfulForward) - forwardIndex
				driver := &injectedTraceDriver{
					HierarchyDriver: live,
					injection:       injection,
					expectedForward: len(writes),
				}
				round := frozenExecutionRound(t, trace, driver)
				ticket := reserveTraceWithBudget(t, round.budget, trace)
				res := &ConvergenceResult{}

				_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

				require.ErrorContains(t, err, "rollback failed")
				require.True(t, driver.injected)
				require.Equal(t,
					expectedWriteAttemptsWithRollbackFailure(
						writes, injection.failWriteAt, forwardIndex),
					driver.writeAttempts,
					"the rejected inverse and every continued rollback write must be exact")
				failedRollback := driver.writeAttempts[injection.failWriteAt+injection.failRollbackWriteAt-1]
				require.Equal(t, traceWriteAttempt{
					rel:      failedForward.rel,
					resource: failedForward.resource,
					target:   failedForward.before,
				}, failedRollback,
					"the rejected rollback write must target the captured before-image")

				actual := live.snapshot()
				expected := expectedStateAfterRollbackWriteFailure(
					t, initial, successfulForward, forwardIndex)
				assertEveryHierarchyNodeEqual(t, expected, actual)
				if failedForward.phase == PhaseExpand &&
					hasEarlierWriteForSameResource(successfulForward, forwardIndex, PhaseDrain) {
					atomic.StoreUint32(&coveredExpandInverseWithEarlierDrainThirdState, 1)
					require.NotEqual(t, failedForward.before,
						rollbackResourceValue(actual[failedForward.rel], failedForward.resource),
						"the failed Expand inverse must retain a third state that the earlier Drain inverse refuses")
				}

				residualResources := changedHierarchyResources(initial, actual)
				require.Equal(t, len(residualResources), len(res.Journal),
					"journal must contain one entry for every residual physical resource")
				require.Equal(t, len(res.Journal), res.Applied)
				seenResidual := make(map[string]struct{}, len(res.Journal))
				for _, impact := range res.Journal {
					resourceKey := impact.Rel + "\x00" + string(impact.Resource)
					_, changed := residualResources[resourceKey]
					require.True(t, changed, "journal item must describe a residual resource: %+v", impact)
					require.NotContains(t, seenResidual, resourceKey,
						"journal must not duplicate residual resource %q", resourceKey)
					seenResidual[resourceKey] = struct{}{}
					operation, logicalIndex := matchingJournalOperation(t, trace, impact)
					require.Equal(t, logicalIndex, impact.LogicalOperationIndex)
					switch impact.Resource {
					case HierarchyOperationWriteCPUs:
						require.Equal(t, operation.Target.CPUs, impact.Target.CPUs)
						require.Equal(t, actual[impact.Rel].cpus, impact.Observed.CPUs)
					case HierarchyOperationWriteMems:
						require.Equal(t, operation.Target.Mems, impact.Target.Mems)
						require.Equal(t, actual[impact.Rel].mems, impact.Observed.Mems)
					default:
						t.Fatalf("unsupported journal resource %q", impact.Resource)
					}
					if rollbackResourceMatchesTarget(impact) {
						require.Equal(t, PhysicalImpactConfirmed, impact.PhysicalImpact)
					} else {
						require.Equal(t, PhysicalImpactUncertain, impact.PhysicalImpact)
					}
				}
				require.Equal(t, residualResources, seenResidual)
			})
		}
	}
}

func expectedStateAfterRollbackWriteFailure(
	t *testing.T,
	initial fakeHierarchyState,
	successfulForward []expectedTraceWrite,
	failedForwardIndex int,
) fakeHierarchyState {
	t.Helper()
	expected := make(fakeHierarchyState, len(initial))
	for rel, node := range initial {
		expected[rel] = node
	}
	for _, write := range successfulForward {
		setRollbackResourceValue(t, expected, write, write.target)
	}
	for index := len(successfulForward) - 1; index >= 0; index-- {
		write := successfulForward[index]
		current := rollbackResourceValue(expected[write.rel], write.resource)
		if current == write.before {
			continue
		}
		if current != write.target || index == failedForwardIndex {
			continue
		}
		setRollbackResourceValue(t, expected, write, write.before)
	}
	return expected
}

func expectedWriteAttemptsWithRollbackFailure(
	writes []expectedTraceWrite,
	failWriteAt, failedForwardIndex int,
) []traceWriteAttempt {
	successfulForward := writes[:failWriteAt-1]
	attempts := make([]traceWriteAttempt, 0, failWriteAt+len(successfulForward))
	current := make(map[string]string)
	for _, write := range successfulForward {
		key := write.rel + "\x00" + string(write.resource)
		if _, ok := current[key]; !ok {
			current[key] = write.before
		}
		current[key] = write.target
		attempts = append(attempts, traceWriteAttempt{
			rel: write.rel, resource: write.resource, target: write.target,
		})
	}
	failedForward := writes[failWriteAt-1]
	attempts = append(attempts, traceWriteAttempt{
		rel: failedForward.rel, resource: failedForward.resource, target: failedForward.target,
	})
	for index := len(successfulForward) - 1; index >= 0; index-- {
		write := successfulForward[index]
		key := write.rel + "\x00" + string(write.resource)
		if current[key] == write.before || current[key] != write.target {
			continue
		}
		attempts = append(attempts, traceWriteAttempt{
			rel: write.rel, resource: write.resource, target: write.before,
		})
		if index != failedForwardIndex {
			current[key] = write.before
		}
	}
	return attempts
}

func matchingJournalOperation(
	t *testing.T,
	trace *CompiledPhaseTrace,
	impact AppliedPlanOperation,
) (PlanOperation, int) {
	t.Helper()
	logicalIndex := 0
	for _, phase := range trace.Phases {
		for _, operation := range phase.Operations {
			if operation.PlanID == impact.PlanID &&
				operation.Rel == impact.Rel &&
				operation.Direction == impact.Direction &&
				phase.Kind == impact.Phase {
				return operation, logicalIndex
			}
			logicalIndex++
		}
	}
	t.Fatalf("journal item has no exact frozen-trace operation: %+v", impact)
	return PlanOperation{}, -1
}

func setRollbackResourceValue(
	t *testing.T,
	state fakeHierarchyState,
	write expectedTraceWrite,
	value string,
) {
	t.Helper()
	node := state[write.rel]
	switch write.resource {
	case HierarchyOperationWriteCPUs:
		node.cpus = machine.MustParse(value)
		node.configuredCPUs = machine.MustParse(value)
	case HierarchyOperationWriteMems:
		node.mems = value
		node.configuredMems = value
	default:
		t.Fatalf("unsupported hierarchy resource %q", write.resource)
	}
	state[write.rel] = node
}

func rollbackResourceValue(node fakeHierarchyNode, resource HierarchyOperation) string {
	switch resource {
	case HierarchyOperationWriteCPUs:
		return node.configuredCPUs.String()
	case HierarchyOperationWriteMems:
		return node.configuredMems
	default:
		return ""
	}
}

func hasEarlierWriteForSameResource(
	writes []expectedTraceWrite,
	index int,
	phase PhaseKind,
) bool {
	for earlier := 0; earlier < index; earlier++ {
		if writes[earlier].rel == writes[index].rel &&
			writes[earlier].resource == writes[index].resource &&
			writes[earlier].phase == phase {
			return true
		}
	}
	return false
}

func changedHierarchyResources(
	initial, actual fakeHierarchyState,
) map[string]struct{} {
	changed := make(map[string]struct{})
	for rel, before := range initial {
		after := actual[rel]
		if !before.configuredCPUs.Equals(after.configuredCPUs) ||
			!before.cpus.Equals(after.cpus) {
			changed[rel+"\x00"+string(HierarchyOperationWriteCPUs)] = struct{}{}
		}
		if !cpusetListValuesEqual(before.configuredMems, after.configuredMems) ||
			!cpusetListValuesEqual(before.mems, after.mems) {
			changed[rel+"\x00"+string(HierarchyOperationWriteMems)] = struct{}{}
		}
	}
	return changed
}

func rollbackResourceMatchesTarget(operation AppliedPlanOperation) bool {
	switch operation.Resource {
	case HierarchyOperationWriteCPUs:
		return operation.Observed.CPUs.Equals(operation.Target.CPUs)
	case HierarchyOperationWriteMems:
		return cpusetListValuesEqual(operation.Observed.Mems, operation.Target.Mems)
	default:
		return false
	}
}

func TestFrozenTraceMutateThenErrorWithFailedPinnedReadback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		identityDrift bool
	}{
		{name: "original identity is rolled back"},
		{name: "replacement identity is never written", identityDrift: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trace, live := compiledTraceWithCPUAndMemoryWrites(t)
			live.invariants = nil
			initial := live.snapshot()
			writes := expectedPhysicalWrites(trace)
			require.GreaterOrEqual(t, len(writes), 3)
			driver := &injectedTraceDriver{
				HierarchyDriver: live,
				injection: traceFailureInjection{
					mutateThenFailWriteAt:         3,
					failUncertainReadback:         true,
					driftIdentityOnFailedReadback: tt.identityDrift,
				},
				expectedForward: len(writes),
			}
			round := frozenExecutionRound(t, trace, driver)
			ticket := reserveTraceWithBudget(t, round.budget, trace)
			seed := AppliedPlanOperation{PlanID: "before-invocation"}
			res := &ConvergenceResult{
				Applied:              4,
				Journal:              []AppliedPlanOperation{seed},
				ParentSafe:           true,
				Converged:            true,
				FinalSnapshotCurrent: true,
			}

			_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

			require.Error(t, err)
			require.ErrorContains(t, err, "injected uncertain")
			require.ErrorContains(t, err, "injected uncertain write read-back failure")
			require.False(t, res.ParentSafe)
			require.False(t, res.Converged)
			require.False(t, res.FinalSnapshotCurrent)
			if tt.identityDrift {
				require.ErrorIs(t, err, ErrCgroupIdentityChanged)
				require.Greater(t, len(res.Journal), 1,
					"unverified physical recovery must retain the invocation journal")
				require.Greater(t, res.Applied, 4,
					"unverified physical recovery must retain applied evidence")
				impact := res.Journal[len(res.Journal)-1]
				require.Equal(t, PhysicalImpactUncertain, impact.PhysicalImpact)
				require.Equal(t, writes[2].resource, impact.Resource)
				require.Equal(t, live.writes[2].rel, impact.Rel)
				for _, write := range live.writes[3:] {
					require.NotEqual(t, write.rel, live.writes[2].rel,
						"rollback must not write the replacement identity")
				}
				return
			}
			require.Equal(t, initial, live.snapshot())
			require.Equal(t, []AppliedPlanOperation{seed}, res.Journal)
			require.Equal(t, 4, res.Applied)
		})
	}
}

func TestFirstUncertainWriteIdentityDriftRetainsPhysicalImpact(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	writes := expectedPhysicalWrites(trace)
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection: traceFailureInjection{
			mutateThenFailWriteAt:         1,
			failUncertainReadback:         true,
			driftIdentityOnFailedReadback: true,
		},
		expectedForward: len(writes),
	}
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	seed := AppliedPlanOperation{PlanID: "before-invocation"}
	res := &ConvergenceResult{
		Applied: 3,
		Journal: []AppliedPlanOperation{seed},
	}

	_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
	require.Len(t, res.Journal, 2)
	require.Equal(t, 4, res.Applied)
	impact := res.Journal[1]
	require.Equal(t, PhysicalImpactUncertain, impact.PhysicalImpact)
	require.Equal(t, writes[0].resource, impact.Resource)
	require.NotEmpty(t, impact.Rel)
}

func TestFrozenTraceRollbackAfterForwardCancellationRestoresInitialState(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	initial := live.snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection:       traceFailureInjection{cancelAfterWrite: 1},
		expectedForward: len(expectedPhysicalWrites(trace)),
		cancel:          cancel,
	}
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	_, err := round.executeFrozenTrace(ctx, trace, ticket, res)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, initial, live.snapshot())
	require.Empty(t, res.Journal)
	require.Zero(t, res.Applied)
	require.Positive(t, ticket.consumedRollback.Total())
	require.NotContains(t, err.Error(), "rollback failed")
}

func TestFrozenTraceRollbackAfterForwardDeadlineRestoresInitialState(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	initial := live.snapshot()
	ctx := newTriggeredDeadlineContext(context.Background())
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection: traceFailureInjection{
			expireDeadlineAfterWrite: 1,
		},
		expectedForward: len(expectedPhysicalWrites(trace)),
		expireDeadline:  ctx.expire,
	}
	round := frozenExecutionRound(t, trace, driver)
	round.budget = NewBudgetTracker(ConvergenceBudget{Deadline: deadline})
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	adjustmentBudget := NewAdjustmentBudget(ctx, ConvergenceBudget{
		MaxPlanOperations: trace.Cost.Total(),
		Deadline:          deadline,
	})
	adjustmentReservation, reserveErr := adjustmentBudget.ReserveExecution(trace.Cost)
	require.NoError(t, reserveErr)
	round.adjustmentBudget = adjustmentBudget
	res := &ConvergenceResult{}

	validated, validateErr := validatedPhaseTraceForTest(ctx, trace)
	require.NoError(t, validateErr)
	_, err := round.executeValidatedFrozenTrace(
		ctx, validated, ticket, res, adjustmentReservation)

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the original forward deadline must remain the primary unwrap chain")
	require.Equal(t, initial, live.snapshot(),
		"rollback must use an independent deadline after the forward deadline expires")
	require.Empty(t, res.Journal)
	require.Zero(t, res.Applied)
	require.Positive(t, ticket.consumedRollback.Total())
	require.Equal(t, 2, adjustmentBudget.CumulativeWrites(),
		"the real adjustment budget must account the forward and rollback attempts")
	require.NotContains(t, err.Error(), "rollback failed")
}

func TestFrozenTraceRecoveryDeadlineRetainsPhysicalImpactEvidence(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection: traceFailureInjection{
			cancelAfterWrite:      1,
			blockRollbackIdentity: true,
		},
		expectedForward: len(expectedPhysicalWrites(trace)),
		cancel:          cancel,
	}
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	adjustmentBudget := NewAdjustmentBudget(ctx, ConvergenceBudget{
		MaxPlanOperations: trace.Cost.Total(),
	})
	adjustmentReservation, reserveErr := adjustmentBudget.ReserveExecution(trace.Cost)
	require.NoError(t, reserveErr)
	round.adjustmentBudget = adjustmentBudget
	res := &ConvergenceResult{}

	validated, validateErr := validatedPhaseTraceForTest(ctx, trace)
	require.NoError(t, validateErr)
	_, err := round.executeValidatedFrozenTrace(
		ctx, validated, ticket, res, adjustmentReservation)

	require.ErrorIs(t, err, context.Canceled,
		"the forward cancellation must remain the primary unwrap chain")
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the bounded recovery deadline must be attached")
	require.Equal(t,
		"execution failed: context canceled; rollback failed: [read entry before rollback write_cpus for \"reclaimed/leaf\": context deadline exceeded, read \"reclaimed/leaf\" after rollback: context deadline exceeded]",
		err.Error(),
	)
	require.NotEmpty(t, res.Journal)
	require.Positive(t, res.Applied)
	require.NotEqual(t, PhysicalImpactNone, res.Journal[len(res.Journal)-1].PhysicalImpact)
}

func compiledMultiFrontierTrace(
	t *testing.T,
) (*CompiledPhaseTrace, *fakeHierarchyDriver) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureMultiFrontierParentSafeDrain()
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	drainWrites := 0
	for _, write := range expectedPhysicalWrites(trace) {
		if write.phase == PhaseDrain {
			drainWrites++
		}
	}
	require.GreaterOrEqual(t, drainWrites, 2)
	return trace, fixture.driver
}

func TestRollbackPrefixContinuesAfterIntermediateRollbackFailure(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	live.add("b", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	live.add("c", CgroupIdentity{Device: 1, Inode: 3}, "2", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	applyCPUWriteForRollbackTest(t, live, stack, "b", "1", "1-2")
	applyCPUWriteForRollbackTest(t, live, stack, "c", "2", "2-3")
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection:       traceFailureInjection{failWriteAt: 2},
	}
	ticket := rollbackOnlyTicket(3, 0)
	writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil)

	err := writer.rollbackTracePrefix(context.Background(), stack, ticket)

	require.Error(t, err)
	require.Equal(t, 3, driver.writeCalls, "rollback must continue after the middle inverse fails")
	require.Equal(t, machine.MustParse("0"), live.nodes["a"].cpus)
	require.Equal(t, machine.MustParse("1-2"), live.nodes["b"].cpus)
	require.Equal(t, machine.MustParse("2"), live.nodes["c"].cpus)
	require.Equal(t, 3, ticket.consumedRollback.CPUSetWrites)
	require.Zero(t, ticket.consumedForward.Total())
}

func TestFailedRollbackRetainsOnlyNetPhysicalImpact(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	live.add("b", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	applyCPUWriteForRollbackTest(t, live, stack, "b", "1", "1-2")
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection:       traceFailureInjection{failWriteAt: 1},
	}
	writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil)
	seed := AppliedPlanOperation{PlanID: "existing"}
	res := &ConvergenceResult{
		Applied: 7,
		Journal: []AppliedPlanOperation{
			seed,
			{Rel: "a", Resource: HierarchyOperationWriteCPUs},
			{Rel: "b", Resource: HierarchyOperationWriteCPUs},
		},
	}

	err := writer.failFrozenTrace(
		context.Background(), errors.New("forward failed"), stack,
		rollbackOnlyTicket(2, 0), res, 1, 5)

	require.ErrorContains(t, err, "rollback failed")
	require.Equal(t, machine.MustParse("0"), live.nodes["a"].cpus,
		"successfully restored resource must not remain as net progress")
	require.Equal(t, machine.MustParse("1-2"), live.nodes["b"].cpus)
	require.Equal(t, 6, res.Applied)
	require.Len(t, res.Journal, 2)
	require.Equal(t, seed, res.Journal[0])
	require.Equal(t, "b", res.Journal[1].Rel)
	require.Equal(t, HierarchyOperationWriteCPUs, res.Journal[1].Resource)
	require.Equal(t, PhysicalImpactConfirmed, res.Journal[1].PhysicalImpact)
	require.Equal(t, "1-2", res.Journal[1].Observed.CPUs.String())
}

func TestPartialRollbackAttributesRepeatedResourceToStillEffectiveLogicalOperation(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	stack := &traceMutationStack{
		writes: []AppliedPhysicalWrite{
			{
				PlanID: "zero-to-one", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
				Before: "0", BeforeEffective: "0", After: "0-1",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 3, Phase: PhaseExpand,
			},
			{
				PlanID: "one-to-two", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
				Before: "0-1", BeforeEffective: "0-1", After: "0-2",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 8, Phase: PhaseExpand,
			},
		},
		rollbackObservations: map[string]rollbackObservation{
			"a": {
				current: EntryState{
					Rel: "a", Identity: identity,
					CPUs: machine.MustParse("0-1"), ConfiguredCPUs: machine.MustParse("0-1"),
				},
			},
		},
	}
	res := &ConvergenceResult{}
	writer := newSafeCPUSetWriter(nil, nil, nil)

	writer.rebuildPhysicalImpactEvidence(stack, res, 0, 0)

	require.Equal(t, 1, res.Applied)
	require.Len(t, res.Journal, 1)
	evidence := res.Journal[0]
	require.Equal(t, "zero-to-one", evidence.PlanID)
	require.Equal(t, 3, evidence.LogicalOperationIndex)
	require.Equal(t, PhaseExpand, evidence.Phase)
	require.Equal(t, "0-1", evidence.Target.CPUs.String())
	require.Equal(t, "0-1", evidence.Observed.CPUs.String())
	require.Equal(t, PhysicalImpactConfirmed, evidence.PhysicalImpact)
}

func TestPartialRollbackMergesCPUAndMemsForOneLogicalOperation(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	stack := &traceMutationStack{
		writes: []AppliedPhysicalWrite{
			{
				PlanID: "joint", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteMems,
				Before: "0", BeforeEffective: "0", After: "0-1",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 5, Phase: PhaseExpand,
			},
			{
				PlanID: "joint", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
				Before: "0", BeforeEffective: "0", After: "0-1",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 5, Phase: PhaseExpand,
			},
		},
		rollbackObservations: map[string]rollbackObservation{
			"a": {
				current: EntryState{
					Rel: "a", Identity: identity,
					CPUs: machine.MustParse("0-1"), ConfiguredCPUs: machine.MustParse("0-1"),
					Mems: "0-1", ConfiguredMems: "0-1",
				},
			},
		},
	}
	res := &ConvergenceResult{}
	writer := newSafeCPUSetWriter(nil, nil, nil)

	writer.rebuildPhysicalImpactEvidence(stack, res, 0, 0)

	require.Equal(t, 1, res.Applied)
	require.Len(t, res.Journal, 1)
	evidence := res.Journal[0]
	require.Equal(t, "joint", evidence.PlanID)
	require.Equal(t, 5, evidence.LogicalOperationIndex)
	require.Equal(t, PhaseExpand, evidence.Phase)
	require.Equal(t, "0-1", evidence.Target.CPUs.String())
	require.Equal(t, "0-1", evidence.Target.Mems)
	require.Equal(t, evidence.Target, evidence.Observed)
	require.Equal(t, PhysicalImpactConfirmed, evidence.PhysicalImpact)
}

func TestPartialRollbackReportsUnmatchedIntermediateAsUncertainObservedValue(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	stack := &traceMutationStack{
		writes: []AppliedPhysicalWrite{
			{
				PlanID: "zero-to-two", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
				Before: "0", BeforeEffective: "0", After: "0-2",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 2, Phase: PhaseExpand,
			},
		},
		rollbackObservations: map[string]rollbackObservation{
			"a": {
				current: EntryState{
					Rel: "a", Identity: identity,
					CPUs: machine.MustParse("0-1"), ConfiguredCPUs: machine.MustParse("0-1"),
				},
			},
		},
	}
	res := &ConvergenceResult{}
	writer := newSafeCPUSetWriter(nil, nil, nil)

	writer.rebuildPhysicalImpactEvidence(stack, res, 0, 0)

	require.Equal(t, 1, res.Applied)
	require.Len(t, res.Journal, 1)
	require.Equal(t, PhysicalImpactUncertain, res.Journal[0].PhysicalImpact)
	require.Equal(t, "0-2", res.Journal[0].Target.CPUs.String())
	require.Equal(t, "0-1", res.Journal[0].Observed.CPUs.String())
}

func TestUncertainPartialWriteReadbackRetainsIntermediateValue(t *testing.T) {
	t.Parallel()
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		t.Run(string(resource), func(t *testing.T) {
			t.Parallel()
			live := newFakeHierarchyDriver()
			live.allowUnwitnessedExpansion = true
			identity := CgroupIdentity{Device: 1, Inode: 1}
			live.add("a", identity, "0", "0")
			writer := newSafeCPUSetWriter(
				live, NewBudgetTracker(ConvergenceBudget{}), nil)
			operation := PlanOperation{
				PlanID: "plan", Rel: "a", ExpectedIdentity: identity,
				Direction: WriteGrow,
			}
			after := "0-2"
			write, err := writer.capturePhysicalWriteBefore(
				context.Background(), operation, resource, after, 0, PhaseExpand)
			require.NoError(t, err)
			switch resource {
			case HierarchyOperationWriteCPUs:
				live.nodes["a"].configuredCPUs = machine.MustParse("0-1")
				live.nodes["a"].cpus = machine.MustParse("0-1")
			case HierarchyOperationWriteMems:
				live.nodes["a"].configuredMems = "0-1"
				live.nodes["a"].mems = "0-1"
			}
			stack := &traceMutationStack{}

			err = writer.recordUncertainPhysicalWrite(
				context.Background(), write, stack)

			require.NoError(t, err)
			require.Len(t, stack.writes, 1,
				"a successful read-back that differs from Before must be rolled back")
			require.Equal(t, PhysicalImpactUncertain, stack.writes[0].Impact)
		})
	}
}

func TestRollbackUsesTicketAfterOrdinaryHierarchyIOBudgetIsExhausted(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	require.NoError(t, budget.beforeHierarchyIOOperation(context.Background()))
	writer := newSafeCPUSetWriter(NewBudgetedHierarchyDriver(live, budget), budget, nil)
	ticket := rollbackOnlyTicket(1, 0)

	err := writer.rollbackTracePrefix(context.Background(), stack, ticket)

	require.NoError(t, err)
	require.Equal(t, machine.MustParse("0"), live.nodes["a"].cpus)
	require.Equal(t, 3, ticket.consumedRollbackIOOperations)
}

func TestRollbackTicketBoundsIndependentHierarchyIO(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	ticket := rollbackOnlyTicket(1, 0)
	ticket.rollbackIOOperations = 1
	writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)

	err := writer.rollbackTracePrefix(context.Background(), stack, ticket)

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
	require.Equal(t, machine.MustParse("0-1"), live.nodes["a"].cpus)
	require.Equal(t, 1, ticket.consumedRollbackIOOperations)
}

func TestExecutionRollbackErrorPreservesPlanStaleChain(t *testing.T) {
	t.Parallel()
	executionErr := &PlanStaleError{
		Rel: "a", Direction: WriteShrink, Resource: "cpuset.cpus",
		Current: "0-1", Target: "0",
	}
	rollbackErr := errors.New("rollback failed")

	err := newExecutionRollbackError(executionErr, rollbackErr)

	require.Equal(t,
		"execution failed: coordinator plan is stale and requires replan: rel=\"a\" direction=shrink resource=cpuset.cpus current=0-1 target=0; rollback failed: rollback failed",
		err.Error())
	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	var stale *PlanStaleError
	require.ErrorAs(t, err, &stale)
	require.Same(t, executionErr, stale)
	_, hasSingleUnwrap := err.(interface{ Unwrap() error })
	require.True(t, hasSingleUnwrap, "Go 1.18 requires a single-error unwrap chain")
}

func TestRollbackIdentityDriftDoesNotWriteReplacementGeneration(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	identity := CgroupIdentity{Device: 1, Inode: 1}
	live.add("a", identity, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	writesBeforeRollback := live.PhysicalWriteCount()
	live.bumpIdentity("a")
	writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)

	err := writer.rollbackTracePrefix(context.Background(), stack, rollbackOnlyTicket(1, 0))

	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
	require.Equal(t, writesBeforeRollback, live.PhysicalWriteCount())
	require.Equal(t, machine.MustParse("0-1"), live.nodes["a"].cpus)
}

func TestRollbackENOENTRequiresExactFrozenWrittenIdentity(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	newStack := func(retirements []FrozenRollbackRetirement) *traceMutationStack {
		return &traceMutationStack{
			writes: []AppliedPhysicalWrite{{
				PlanID: "plan", Rel: "holder", Identity: identity,
				Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
				Before: "0", BeforeEffective: "0",
				After: "0-1", AfterEffective: "0-1",
				Impact: PhysicalImpactConfirmed,
			}},
			rollbackRetirements: retirements,
		}
	}
	authorized := []FrozenRollbackRetirement{{Rel: "holder", Identity: identity}}

	t.Run("exact authorized identity with confirmed absence", func(t *testing.T) {
		t.Parallel()
		live := newFakeHierarchyDriver()
		writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)
		ticket := rollbackOnlyTicket(1, 0)

		err := writer.rollbackTracePrefix(context.Background(), newStack(authorized), ticket)

		require.NoError(t, err)
		require.Zero(t, live.PhysicalWriteCount())
		require.Equal(t, 3, ticket.consumedRollbackIOOperations,
			"rollback must confirm absence and verify the retired result")
	})

	tests := []struct {
		name        string
		retirements []FrozenRollbackRetirement
		identity    CgroupIdentity
		err         error
		prepare     func(*fakeHierarchyDriver)
	}{
		{
			name:     "unlisted write",
			identity: identity,
		},
		{
			name:        "zero identity",
			retirements: []FrozenRollbackRetirement{{Rel: "holder"}},
		},
		{
			name: "authorization identity mismatch",
			retirements: []FrozenRollbackRetirement{{
				Rel: "holder", Identity: CgroupIdentity{Device: 1, Inode: 2},
			}},
			identity: identity,
		},
		{
			name: "duplicate authorization",
			retirements: []FrozenRollbackRetirement{
				{Rel: "holder", Identity: identity},
				{Rel: "holder", Identity: identity},
			},
			identity: identity,
		},
		{
			name:        "untyped absence",
			retirements: authorized,
			identity:    identity,
			err:         errors.New("read cpuset.cpus: no such file or directory"),
		},
		{
			name:        "same identity still present",
			retirements: authorized,
			identity:    identity,
			err:         syscall.ENOENT,
			prepare: func(live *fakeHierarchyDriver) {
				live.add("holder", identity, "0-1", "0")
			},
		},
		{
			name:        "replacement identity",
			retirements: authorized,
			identity:    identity,
			err:         syscall.ENOENT,
			prepare: func(live *fakeHierarchyDriver) {
				live.add("holder", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			live := newFakeHierarchyDriver()
			if tt.prepare != nil {
				tt.prepare(live)
			}
			if tt.err != nil {
				failed := false
				live.beforeCall = func(operation HierarchyOperation, rel string) error {
					if !failed && operation == HierarchyOperationRead && rel == "holder" {
						failed = true
						return tt.err
					}
					return nil
				}
			}
			stack := newStack(tt.retirements)
			stack.writes[0].Identity = tt.identity
			writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)
			drift := &frozenFinalSnapshotDriftError{stale: &PlanStaleError{
				Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
				Err: fmt.Errorf("test final snapshot drift"),
			}}
			res := &ConvergenceResult{}

			err := writer.failFrozenTrace(
				context.Background(), drift, stack, rollbackOnlyTicket(1, 0), res, 0, 0)

			require.Error(t, err)
			require.ErrorContains(t, err, "rollback failed")
			require.Zero(t, live.PhysicalWriteCount(),
				"rejected disappearance must never write a live or replacement cgroup")
		})
	}
}

func TestRollbackAuthorizedRetirementAcrossInverseWriteWindows(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		for _, window := range []rollbackRetirementWindow{
			rollbackRetirementOnInverseWrite,
			rollbackRetirementAfterInverseWrite,
		} {
			window := window
			t.Run(string(resource)+"/"+string(window), func(t *testing.T) {
				t.Parallel()
				live := newFakeHierarchyDriver()
				live.allowUnwitnessedExpansion = true
				live.add("holder", identity, "0-1", "0-1")
				driver := &rollbackRetirementWindowDriver{
					HierarchyDriver: live,
					live:            live,
					rel:             "holder",
					resource:        resource,
					window:          window,
				}
				write := AppliedPhysicalWrite{
					PlanID: "plan", Rel: "holder", Identity: identity,
					Direction: WriteGrow, Resource: resource,
					Impact: PhysicalImpactConfirmed,
				}
				cpuWrites, memWrites := 0, 0
				switch resource {
				case HierarchyOperationWriteCPUs:
					write.Before, write.BeforeEffective = "0", "0"
					write.After, write.AfterEffective = "0-1", "0-1"
					cpuWrites = 1
				case HierarchyOperationWriteMems:
					write.Before, write.BeforeEffective = "0", "0"
					write.After, write.AfterEffective = "0-1", "0-1"
					memWrites = 1
				}
				stack := &traceMutationStack{
					writes: []AppliedPhysicalWrite{write},
					rollbackRetirements: []FrozenRollbackRetirement{{
						Rel: "holder", Identity: identity,
					}},
				}
				ticket := rollbackOnlyTicket(cpuWrites, memWrites)
				writer := newSafeCPUSetWriter(
					driver, NewBudgetTracker(ConvergenceBudget{}), nil)

				err := writer.rollbackTracePrefix(context.Background(), stack, ticket)

				require.NoError(t, err)
				require.True(t, driver.retired)
				require.Equal(t, 1, driver.absentStats,
					"typed ENOENT must use identity-bound retirement confirmation")
				require.Equal(t, 4, ticket.consumedRollbackIOOperations,
					"successful retirement uses read/write/stat/final-read")
			})
		}
	}
}

func TestFinalSnapshotDriftReplanAllowsConfirmedAuthorizedRollbackRetirementOnly(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	stack := &traceMutationStack{
		writes: []AppliedPhysicalWrite{{
			PlanID: "plan", Rel: "holder", Identity: identity,
			Direction: WriteGrow, Resource: HierarchyOperationWriteCPUs,
			Before: "0", BeforeEffective: "0",
			After: "0-1", AfterEffective: "0-1",
			Impact: PhysicalImpactConfirmed,
		}},
		rollbackRetirements: []FrozenRollbackRetirement{{
			Rel: "holder", Identity: identity,
		}},
	}
	drift := &frozenFinalSnapshotDriftError{stale: &PlanStaleError{
		Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
		Err: fmt.Errorf("test final snapshot drift"),
	}}
	writer := newSafeCPUSetWriter(
		newFakeHierarchyDriver(), NewBudgetTracker(ConvergenceBudget{}), nil)
	res := &ConvergenceResult{Applied: 1, Journal: []AppliedPlanOperation{{PlanID: "plan"}}}

	err := writer.failFrozenTrace(
		context.Background(), drift, stack, rollbackOnlyTicket(1, 0), res, 0, 0)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Zero(t, res.Applied)
	require.Empty(t, res.Journal)
}

func TestRollbackPinnedReadSkipsAlreadyRestoredStateWithoutWriteBudget(t *testing.T) {
	t.Parallel()
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		t.Run(string(resource), func(t *testing.T) {
			t.Parallel()
			live := newFakeHierarchyDriver()
			live.allowUnwitnessedExpansion = true
			identity := CgroupIdentity{Device: 1, Inode: 1}
			live.add("a", identity, "0", "0")
			write := AppliedPhysicalWrite{
				PlanID: "plan", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: resource,
				Before: "0", BeforeEffective: "0",
				After: "0-1", AfterEffective: "0-1",
				Impact: PhysicalImpactConfirmed,
			}
			stack := &traceMutationStack{writes: []AppliedPhysicalWrite{write}}
			cpuWrites, memWrites := 0, 0
			if resource == HierarchyOperationWriteCPUs {
				cpuWrites = 1
			} else {
				memWrites = 1
			}
			ticket := rollbackOnlyTicket(cpuWrites, memWrites)
			writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)

			err := writer.rollbackTracePrefix(context.Background(), stack, ticket)

			require.NoError(t, err)
			require.Zero(t, live.PhysicalWriteCount())
			require.Zero(t, ticket.consumedRollback.Total())
		})
	}
}

func TestRollbackPinnedReadRejectsSameGenerationThirdStateAndPreservesImpact(t *testing.T) {
	t.Parallel()
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		t.Run(string(resource), func(t *testing.T) {
			t.Parallel()
			live := newFakeHierarchyDriver()
			live.allowUnwitnessedExpansion = true
			identity := CgroupIdentity{Device: 1, Inode: 1}
			live.add("a", identity, "0", "0")
			write := AppliedPhysicalWrite{
				PlanID: "plan", Rel: "a", Identity: identity,
				Direction: WriteGrow, Resource: resource,
				Before: "0", BeforeEffective: "0",
				After: "0-1", AfterEffective: "0-1",
				Impact:                PhysicalImpactConfirmed,
				LogicalOperationIndex: 2, Phase: PhaseExpand,
			}
			cpuWrites, memWrites := 0, 0
			switch resource {
			case HierarchyOperationWriteCPUs:
				cpuWrites = 1
				live.nodes["a"].configuredCPUs = machine.MustParse(write.After)
				live.nodes["a"].cpus = machine.MustParse("0-2")
			case HierarchyOperationWriteMems:
				memWrites = 1
				live.nodes["a"].configuredMems = write.After
				live.nodes["a"].mems = "0-2"
			}
			stack := &traceMutationStack{writes: []AppliedPhysicalWrite{write}}
			ticket := rollbackOnlyTicket(cpuWrites, memWrites)
			res := &ConvergenceResult{}
			writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), res)

			err := writer.failFrozenTrace(
				context.Background(), errors.New("forward failed"), stack, ticket, res, 0, 0)

			require.ErrorContains(t, err, "neither rollback after nor before")
			require.Zero(t, live.PhysicalWriteCount())
			require.Zero(t, ticket.consumedRollback.Total())
			require.Len(t, res.Journal, 1)
			require.Equal(t, PhysicalImpactUncertain, res.Journal[0].PhysicalImpact)
			if resource == HierarchyOperationWriteCPUs {
				require.Equal(t, "0-2", res.Journal[0].Observed.CPUs.String())
			} else {
				require.Equal(t, "0-2", res.Journal[0].Observed.Mems)
			}
		})
	}
}

func TestRepeatedRelationRollsBackToInvocationInitialValue(t *testing.T) {
	t.Parallel()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.add("a", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	stack := &traceMutationStack{}
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0", "0-1")
	applyCPUWriteForRollbackTest(t, live, stack, "a", "0-1", "0-2")
	writer := newSafeCPUSetWriter(live, NewBudgetTracker(ConvergenceBudget{}), nil)

	err := writer.rollbackTracePrefix(context.Background(), stack, rollbackOnlyTicket(2, 0))

	require.NoError(t, err)
	require.Equal(t, machine.MustParse("0"), live.nodes["a"].cpus)
	require.Equal(t, []string{"0-1", "0"}, cpuWriteTargets(live.writes[2:]))
}

func TestFrozenTraceRollbackRestoresV2EmptyConfiguredState(t *testing.T) {
	t.Parallel()
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		t.Run(string(resource), func(t *testing.T) {
			t.Parallel()
			writer, live, operation, ticket := v2InheritedRollbackFixture(t, resource)
			ticket.operations = make([]frozenOperationAuthorization, 12)
			for i := range ticket.operations {
				ticket.operations[i].operation = operation
			}
			ticket.nextOperation = 11
			stack := &traceMutationStack{}

			_, err := writer.applyFrozenOperation(
				context.Background(), PhaseDrain, 11, operation,
				preflightForDirectOperation(t, live, operation), stack, ticket, "direct")
			require.NoError(t, err)
			require.Len(t, stack.writes, 1)
			require.Equal(t, 11, stack.writes[0].LogicalOperationIndex)
			require.Equal(t, PhaseDrain, stack.writes[0].Phase)
			require.Empty(t, stack.writes[0].Before,
				"mutation before must preserve the empty configured inheritance value")

			require.NoError(t, writer.rollbackTracePrefix(
				context.Background(), stack, ticket))
			assertV2InheritedState(t, live, operation.Rel)
		})
	}
}

func TestFrozenTraceUncertainWriteRollbackRestoresV2EmptyConfiguredState(t *testing.T) {
	t.Parallel()
	for _, resource := range []HierarchyOperation{
		HierarchyOperationWriteCPUs,
		HierarchyOperationWriteMems,
	} {
		resource := resource
		t.Run(string(resource), func(t *testing.T) {
			t.Parallel()
			writer, live, operation, ticket := v2InheritedRollbackFixture(t, resource)
			injected := &injectedTraceDriver{
				HierarchyDriver: live,
				injection: traceFailureInjection{
					mutateThenFailWriteAt: 1,
					failUncertainReadback: true,
				},
			}
			writer.driver = injected
			stack := &traceMutationStack{}

			_, err := writer.applyFrozenOperation(
				context.Background(), PhaseDrain, 0, operation,
				preflightForDirectOperation(t, live, operation), stack, ticket, "direct")
			require.ErrorContains(t, err, "injected uncertain")
			require.Len(t, stack.writes, 1)
			require.Equal(t, PhysicalImpactUncertain, stack.writes[0].Impact)
			require.Empty(t, stack.writes[0].Before,
				"uncertain mutation must retain the real empty configured value")

			require.NoError(t, writer.rollbackTracePrefix(
				context.Background(), stack, ticket))
			assertV2InheritedState(t, live, operation.Rel)
		})
	}
}

func v2InheritedRollbackFixture(
	t *testing.T,
	resource HierarchyOperation,
) (safeCPUSetWriter, *fakeHierarchyDriver, PlanOperation, *ExecutionReservationTicket) {
	t.Helper()
	live := newFakeHierarchyDriver()
	live.allowUnwitnessedExpansion = true
	live.capabilities = cgroupV2Policy.capabilities(true)
	live.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0-1")
	live.add("root/leaf", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0-1")
	live.nodes["root/leaf"].configuredCPUs = machine.NewCPUSet()
	live.nodes["root/leaf"].configuredMems = ""

	operation := PlanOperation{
		Rel:              "root/leaf",
		ExpectedIdentity: live.nodes["root/leaf"].identity,
		ExpectedCurrent:  CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0-1"},
		Target:           CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0-1"},
		Direction:        WriteShrink,
		OwnsMems:         true,
	}
	forward := PhysicalWriteCost{}
	switch resource {
	case HierarchyOperationWriteCPUs:
		operation.Target.CPUs = machine.MustParse("0-1")
		forward.CPUSetWrites = 1
	case HierarchyOperationWriteMems:
		operation.Target.Mems = "0"
		operation.WriteMems = true
		forward.MemsWrites = 1
	default:
		t.Fatalf("unsupported resource %q", resource)
	}
	ticket := &ExecutionReservationTicket{
		traceID:    "direct",
		operations: []frozenOperationAuthorization{{operation: operation}},
		reserved: ExecutionReservationCost{
			Forward:  forward,
			Rollback: forward,
		},
		rollbackIOOperations: 3,
	}
	return newSafeCPUSetWriter(
		live, NewBudgetTracker(ConvergenceBudget{}), nil,
	), live, operation, ticket
}

func preflightForDirectOperation(
	t *testing.T,
	live *fakeHierarchyDriver,
	operation PlanOperation,
) frozenOperationPreflight {
	t.Helper()
	current, err := live.ReadEntry(context.Background(), operation.Rel)
	require.NoError(t, err)
	after := current
	if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
		after.ConfiguredCPUs = operation.Target.CPUs.Clone()
		after.CPUs = operation.Target.CPUs.Clone()
	}
	if operation.WriteMems {
		after.ConfiguredMems = operation.Target.Mems
		after.Mems = operation.Target.Mems
	}
	return frozenOperationPreflight{
		before: freezeOperationState(current),
		after:  freezeOperationState(after),
		children: stableLiveChildren{
			cpus:  machine.NewCPUSet(),
			mems:  machine.NewCPUSet(),
			byRel: make(map[string]EntryState),
		},
	}
}

func assertV2InheritedState(t *testing.T, live *fakeHierarchyDriver, rel string) {
	t.Helper()
	current, err := live.ReadEntry(context.Background(), rel)
	require.NoError(t, err)
	require.True(t, current.ConfiguredCPUs.IsEmpty())
	require.Empty(t, current.ConfiguredMems)
	require.Equal(t, "0-3", current.CPUs.String())
	require.Equal(t, "0-1", current.Mems)
}

func TestRollbackVerificationRelsAreSorted(t *testing.T) {
	t.Parallel()
	stack := &traceMutationStack{writes: []AppliedPhysicalWrite{
		{Rel: "z"},
		{Rel: "a"},
		{Rel: "m"},
		{Rel: "a"},
	}}

	require.Equal(t, []string{"a", "m", "z"}, rollbackVerificationRels(stack))
}

func TestUncertainPhysicalImpactIsNotVerifiedProgress(t *testing.T) {
	t.Parallel()
	outcome := RoundOutcome{Journal: []AppliedPlanOperation{{
		Rel: "a",
		Target: CPUSetTarget{
			CPUs: machine.NewCPUSet(0),
			Mems: "0",
		},
		Observed: CPUSetTarget{
			CPUs: machine.NewCPUSet(0),
			Mems: "0",
		},
		PhysicalImpact: PhysicalImpactUncertain,
	}}}

	require.False(t, roundOutcomeMadeNetProgress(outcome))
}

func TestSuccessfulRollbackRemovesNetJournalAndAppliedProgress(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	driver := &injectedTraceDriver{
		HierarchyDriver: live,
		injection:       traceFailureInjection{failWriteAt: 3},
	}
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	seed := AppliedPlanOperation{PlanID: "existing"}
	res := &ConvergenceResult{
		Applied:              4,
		Journal:              []AppliedPlanOperation{seed},
		ParentSafe:           true,
		Converged:            true,
		FinalSnapshotCurrent: true,
	}

	_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.Error(t, err)
	require.Equal(t, []AppliedPlanOperation{seed}, res.Journal)
	require.Equal(t, 4, res.Applied)
	require.False(t, res.ParentSafe)
	require.False(t, res.Converged)
	require.False(t, res.FinalSnapshotCurrent)
	require.Positive(t, ticket.consumedRollback.Total())
}

func TestCompiledTraceRollbackRejectsControlledPrimaryAndReclaimRetirement(t *testing.T) {
	t.Parallel()
	for _, domain := range []DomainID{DomainPrimary, DomainReclaim} {
		domain := domain
		t.Run(string(domain), func(t *testing.T) {
			t.Parallel()
			trace, live := compiledTraceWithCPUAndMemoryWrites(t)
			live.invariants = nil
			operation := selectTraceOperation(t, trace, func(
				operation PlanOperation,
				trace *CompiledPhaseTrace,
			) bool {
				return trace.InitialSnapshot.DomainByRel[operation.Rel] == domain &&
					(!operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) ||
						(operation.WriteMems &&
							operation.ExpectedCurrent.Mems != operation.Target.Mems))
			})
			require.Contains(t, trace.FrozenBoundary.ControlledRels, operation.Rel)
			driver := &retireWrittenRelOnFinalProofDriver{
				HierarchyDriver: live,
				live:            live,
				rel:             operation.Rel,
				identity:        operation.ExpectedIdentity,
			}
			round := frozenExecutionRound(t, trace, driver)
			ticket := reserveTraceWithBudget(t, round.budget, trace)

			_, err := round.executeFrozenTrace(
				context.Background(), trace, ticket, &ConvergenceResult{})

			require.Error(t, err)
			require.ErrorContains(t, err, "rollback failed")
			require.True(t, driver.retired)
			require.NotContains(t, live.nodes, operation.Rel)
		})
	}
}

func TestCompiledTraceRollbackAllowsWrittenDynamicLeafRetirement(t *testing.T) {
	t.Parallel()
	trace, live, dynamicRel := compiledTraceWithNonControlledDynamicWrite(t)
	live.invariants = nil
	operation := selectTraceOperation(t, trace, func(
		operation PlanOperation,
		_ *CompiledPhaseTrace,
	) bool {
		return operation.Rel == dynamicRel &&
			!operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs)
	})
	require.NotContains(t, trace.FrozenBoundary.ControlledRels, operation.Rel)
	require.Contains(t, trace.FrozenBoundary.RelevantCPUHolders, operation.Rel)
	driver := &retireWrittenRelOnFinalProofDriver{
		HierarchyDriver: live,
		live:            live,
		rel:             operation.Rel,
		identity:        operation.ExpectedIdentity,
	}
	round := frozenExecutionRound(t, trace, driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	_, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "rollback failed")
	require.True(t, driver.retired, "compiled write identity was not retired")
	require.NotContains(t, live.nodes, operation.Rel)
	require.Empty(t, res.Journal)
	require.Zero(t, res.Applied)
	require.Equal(t, 2, driver.absentReads)
	require.Equal(t, 1, driver.absentStats,
		"rollback retirement requires read/stat/read typed-ENOENT confirmation")
	writtenRels := make(map[string]struct{})
	require.LessOrEqual(t,
		ticket.consumedRollbackIOOperations, ticket.rollbackIOOperations)
	cpuWritesToRetiredRel := 0
	for _, write := range live.writes {
		writtenRels[write.rel] = struct{}{}
		if write.rel == operation.Rel {
			if !write.cpus.IsEmpty() {
				cpuWritesToRetiredRel++
			}
		}
	}
	require.Positive(t, cpuWritesToRetiredRel)
	require.Equal(t, 3*len(writtenRels), ticket.consumedRollbackIOOperations,
		"each retired written identity consumes one read/stat/read confirmation")
}

func TestFrozenTraceExecutionPublishesOnlyFreshFinalProof(t *testing.T) {
	t.Parallel()
	trace, live := compiledTraceWithCPUAndMemoryWrites(t)
	live.invariants = nil
	round := frozenExecutionRound(t, trace, live)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	outcome, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.NoError(t, err)
	require.Equal(t, trace.FinalSnapshot.ID, outcome.Snapshot.ID)
	require.Equal(t, trace.FinalSnapshot.ID, res.FinalSnapshot.ID)
	require.True(t, res.FinalSnapshotCurrent)
	require.True(t, res.ParentSafe)
	require.Equal(t, len(flattenTraceOperations(trace)), res.Applied)
	require.Equal(t, res.Applied, len(res.Journal))
	require.Equal(t, res.Journal, outcome.Journal)
	require.Zero(t, ticket.consumedRollback.Total())
}

func TestFrozenTraceAllowsAuthorizedHolderRetirementDuringFinalProof(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	const holder = "kubepods/retiring-holder"
	fixture.driver.add(holder, CgroupIdentity{Device: 1, Inode: 1000}, "1", "0")
	fixture.driver.add(holder+"/child", CgroupIdentity{Device: 1, Inode: 1001}, "1", "0")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	makeFrozenTraceHolderRetirable(t, trace, holder)
	require.Contains(t, trace.InitialSnapshot.Entries, holder)
	require.Contains(t, trace.FrozenBoundary.RelevantCPUHolders, holder)
	require.Contains(t, trace.FrozenBoundary.RetirableCPUHolders, holder)
	fixture.driver.invariants = nil
	retired := false
	fixture.driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if !retired && fixture.driver.PhysicalWriteCount() > 0 &&
			operation == HierarchyOperationList && rel == holder {
			deleteFakeHierarchySubtree(fixture.driver, holder)
			retired = true
			return syscall.ENOENT
		}
		return nil
	}
	round := frozenExecutionRound(t, trace, fixture.driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{}

	outcome, err := round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.NoError(t, err)
	require.True(t, retired, "final proof retirement window was not reached")
	require.True(t, res.FinalSnapshotCurrent)
	assertSnapshotExcludesSubtree(t, outcome.Snapshot, holder)
	assertSnapshotExcludesSubtree(t, res.FinalSnapshot, holder)
	require.Zero(t, ticket.consumedRollback.Total())
}

func TestFrozenTraceRequiredHolderRetirementDuringFinalProofRollsBack(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	const holder = "kubepods/required-holder"
	fixture.driver.add(holder, CgroupIdentity{Device: 1, Inode: 1000}, "1", "0")
	fixture.requiredByRel[holder] = machine.NewCPUSet(1)
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	require.Contains(t, trace.InitialSnapshot.Entries, holder)
	require.Contains(t, trace.FrozenBoundary.RelevantCPUHolders, holder)
	require.NotContains(t, trace.FrozenBoundary.RetirableCPUHolders, holder)
	initial := fixture.driver.snapshot()
	fixture.driver.invariants = nil
	retired := false
	fixture.driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if !retired && fixture.driver.PhysicalWriteCount() > 0 &&
			operation == HierarchyOperationList && rel == holder {
			deleteFakeHierarchySubtree(fixture.driver, holder)
			retired = true
			return syscall.ENOENT
		}
		return nil
	}
	round := frozenExecutionRound(t, trace, fixture.driver)
	ticket := reserveTraceWithBudget(t, round.budget, trace)
	res := &ConvergenceResult{FinalSnapshotCurrent: true}

	_, err = round.executeFrozenTrace(context.Background(), trace, ticket, res)

	require.Error(t, err)
	require.True(t, retired, "final proof retirement window was not reached")
	require.False(t, res.FinalSnapshotCurrent)
	require.Positive(t, ticket.consumedRollback.Total())
	delete(initial, holder)
	require.Equal(t, initial, fixture.driver.snapshot(),
		"the complete physical write prefix must be rolled back")
}

func TestTracePreflightAllowsAuthorizedHolderRetirementWithoutWrites(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	const holder = "kubepods/preflight-retiring-holder"
	fixture.driver.add(holder, CgroupIdentity{Device: 1, Inode: 1000}, "1", "0")
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	makeFrozenTraceHolderRetirable(t, trace, holder)
	require.Contains(t, trace.InitialSnapshot.Entries, holder)
	require.Contains(t, trace.FrozenBoundary.RelevantCPUHolders, holder)
	require.Contains(t, trace.FrozenBoundary.RetirableCPUHolders, holder)
	retired := false
	fixture.driver.beforeCall = func(operation HierarchyOperation, rel string) error {
		if !retired && operation == HierarchyOperationList && rel == holder {
			deleteFakeHierarchySubtree(fixture.driver, holder)
			retired = true
			return syscall.ENOENT
		}
		return nil
	}

	err = newTracePreflightWriter(fixture.driver).
		preflightFrozenTrace(context.Background(), trace)

	require.NoError(t, err)
	require.True(t, retired, "preflight retirement window was not reached")
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func deleteFakeHierarchySubtree(driver *fakeHierarchyDriver, rel string) {
	for candidate := range driver.nodes {
		if candidate == rel || strings.HasPrefix(candidate, rel+"/") {
			delete(driver.nodes, candidate)
		}
	}
}

func makeFrozenTraceHolderRetirable(t *testing.T, trace *CompiledPhaseTrace, holder string) {
	t.Helper()
	for _, rels := range []map[string]machine.CPUSet{
		trace.EvaluationInput.TargetByRel,
		trace.EvaluationInput.ParentSafetyTargetByRel,
		trace.EvaluationInput.ExpectedByRel,
		trace.EvaluationInput.RequiredByRel,
		trace.EvaluationInput.DeferredByRel,
		trace.EvaluationInput.PendingRequiredByRel,
	} {
		for rel := range rels {
			if rel == holder || strings.HasPrefix(rel, holder+"/") {
				delete(rels, rel)
			}
		}
	}
	for rel := range trace.EvaluationInput.DeferredCleanupRels {
		if rel == holder || strings.HasPrefix(rel, holder+"/") {
			delete(trace.EvaluationInput.DeferredCleanupRels, rel)
		}
	}
	boundary, err := compileFrozenBoundaryV1(
		trace.InitialSnapshot, trace.EvaluationInput, trace.Phases)
	require.NoError(t, err)
	trace.FrozenBoundary = boundary
	trace.TraceID = ""
}

type expectedTraceWrite struct {
	phase    PhaseKind
	resource HierarchyOperation
	rel      string
	before   string
	target   string
}

type traceWriteAttempt struct {
	rel      string
	resource HierarchyOperation
	target   string
}

func expectedPhysicalWrites(trace *CompiledPhaseTrace) []expectedTraceWrite {
	var writes []expectedTraceWrite
	for _, phase := range trace.Phases {
		for _, operation := range phase.Operations {
			if operation.WriteMems && operation.ExpectedCurrent.Mems != operation.Target.Mems {
				writes = append(writes, expectedTraceWrite{
					phase: phase.Kind, resource: HierarchyOperationWriteMems,
					rel: operation.Rel, before: operation.ExpectedCurrent.Mems, target: operation.Target.Mems,
				})
			}
			if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
				writes = append(writes, expectedTraceWrite{
					phase: phase.Kind, resource: HierarchyOperationWriteCPUs,
					rel: operation.Rel, before: operation.ExpectedCurrent.CPUs.String(),
					target: operation.Target.CPUs.String(),
				})
			}
		}
	}
	return writes
}

func expectedWriteAttempts(
	writes []expectedTraceWrite,
	failWriteAt int,
) []traceWriteAttempt {
	forwardAttempts := failWriteAt
	if forwardAttempts == 0 {
		forwardAttempts = len(writes)
	}
	attempts := make([]traceWriteAttempt, 0, forwardAttempts*2)
	for _, write := range writes[:forwardAttempts] {
		attempts = append(attempts, traceWriteAttempt{
			rel: write.rel, resource: write.resource, target: write.target,
		})
	}
	successfulForward := writes[:forwardAttempts-1]
	for i := len(successfulForward) - 1; i >= 0; i-- {
		write := successfulForward[i]
		attempts = append(attempts, traceWriteAttempt{
			rel: write.rel, resource: write.resource, target: write.before,
		})
	}
	return attempts
}

func assertEveryHierarchyNodeEqual(
	t *testing.T,
	expected, actual fakeHierarchyState,
) {
	t.Helper()
	require.Equal(t, len(expected), len(actual))
	for rel, before := range expected {
		require.Equal(t, before, actual[rel],
			"hierarchy node %q must equal its complete before-image", rel)
	}
}

func failAtNthResourceWrite(
	t *testing.T,
	writes []expectedTraceWrite,
	resource HierarchyOperation,
	n int,
) traceFailureInjection {
	t.Helper()
	seen := 0
	for i, write := range writes {
		if write.resource == resource {
			seen++
			if seen == n {
				return traceFailureInjection{failWriteAt: i + 1}
			}
		}
	}
	t.Fatalf("trace has only %d %s writes, need %d", seen, resource, n)
	return traceFailureInjection{}
}

func failAtPhaseWrite(
	t *testing.T,
	writes []expectedTraceWrite,
	phase PhaseKind,
	phaseWriteIndex int,
) traceFailureInjection {
	t.Helper()
	var indexes []int
	for i, write := range writes {
		if write.phase == phase {
			indexes = append(indexes, i+1)
		}
	}
	require.NotEmpty(t, indexes, "trace has no %s writes", phase)
	if phaseWriteIndex < 0 {
		phaseWriteIndex = len(indexes) - 1
	}
	require.Less(t, phaseWriteIndex, len(indexes))
	return traceFailureInjection{failWriteAt: indexes[phaseWriteIndex]}
}

type triggeredDeadlineContext struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	expired  uint32
}

func newTriggeredDeadlineContext(parent context.Context) *triggeredDeadlineContext {
	return &triggeredDeadlineContext{
		parent:   parent,
		deadline: time.Now().Add(24 * time.Hour),
		done:     make(chan struct{}),
	}
}

func (c *triggeredDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *triggeredDeadlineContext) Done() <-chan struct{}       { return c.done }
func (c *triggeredDeadlineContext) Err() error {
	if atomic.LoadUint32(&c.expired) != 0 {
		return context.DeadlineExceeded
	}
	return c.parent.Err()
}
func (c *triggeredDeadlineContext) Value(key interface{}) interface{} { return c.parent.Value(key) }
func (c *triggeredDeadlineContext) expire() {
	if atomic.CompareAndSwapUint32(&c.expired, 0, 1) {
		close(c.done)
	}
}

type traceFailureInjection struct {
	failWriteAt                   int
	failRollbackWriteAt           int
	mutateThenFailWriteAt         int
	falseSuccessWriteAt           int
	failPostWriteReadback         bool
	failUncertainReadback         bool
	driftIdentityOnFailedReadback bool
	failFinalProof                bool
	cancelAfterWrite              int
	expireDeadlineAfterWrite      int
	blockRollbackIdentity         bool
}

type injectedTraceDriver struct {
	HierarchyDriver
	injection          traceFailureInjection
	expectedForward    int
	writeCalls         int
	forwardSuccess     int
	rollbackWriteCalls int
	writeAttempts      []traceWriteAttempt
	pendingReadback    bool
	injected           bool
	cancel             context.CancelFunc
	expireDeadline     func()
}

type retireWrittenRelOnFinalProofDriver struct {
	HierarchyDriver
	live        *fakeHierarchyDriver
	rel         string
	identity    CgroupIdentity
	retired     bool
	absentReads int
	absentStats int
}

type rollbackRetirementWindow string

const (
	rollbackRetirementOnInverseWrite    rollbackRetirementWindow = "read-success-write-ENOENT"
	rollbackRetirementAfterInverseWrite rollbackRetirementWindow = "write-success-verify-ENOENT"
)

type rollbackRetirementWindowDriver struct {
	HierarchyDriver
	live        *fakeHierarchyDriver
	rel         string
	resource    HierarchyOperation
	window      rollbackRetirementWindow
	retired     bool
	absentStats int
}

func (d *rollbackRetirementWindowDriver) retire() {
	deleteFakeHierarchySubtree(d.live, d.rel)
	d.retired = true
}

func (d *rollbackRetirementWindowDriver) WriteCPUs(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	cpus machine.CPUSet,
) error {
	if rel != d.rel || d.resource != HierarchyOperationWriteCPUs {
		return d.HierarchyDriver.WriteCPUs(ctx, rel, expected, cpus)
	}
	if d.window == rollbackRetirementOnInverseWrite {
		d.retire()
		return syscall.ENOENT
	}
	err := d.HierarchyDriver.WriteCPUs(ctx, rel, expected, cpus)
	if err == nil {
		d.retire()
	}
	return err
}

func (d *rollbackRetirementWindowDriver) WriteMems(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	mems string,
) error {
	if rel != d.rel || d.resource != HierarchyOperationWriteMems {
		return d.HierarchyDriver.WriteMems(ctx, rel, expected, mems)
	}
	if d.window == rollbackRetirementOnInverseWrite {
		d.retire()
		return syscall.ENOENT
	}
	err := d.HierarchyDriver.WriteMems(ctx, rel, expected, mems)
	if err == nil {
		d.retire()
	}
	return err
}

func (d *rollbackRetirementWindowDriver) StatIdentity(
	ctx context.Context,
	rel string,
) (CgroupIdentity, error) {
	identity, err := d.HierarchyDriver.StatIdentity(ctx, rel)
	if d.retired && rel == d.rel && isCgroupPathAbsent(err) {
		d.absentStats++
	}
	return identity, err
}

func (d *retireWrittenRelOnFinalProofDriver) Roots(
	ctx context.Context,
) ([]RootRef, error) {
	if err := d.retireForFinalProof(); err != nil {
		return nil, err
	}
	return d.HierarchyDriver.Roots(ctx)
}

func (d *retireWrittenRelOnFinalProofDriver) ListChildren(
	ctx context.Context,
	rel string,
) ([]ChildRef, error) {
	if err := d.retireForFinalProof(); err != nil {
		return nil, err
	}
	return d.HierarchyDriver.ListChildren(ctx, rel)
}

func (d *retireWrittenRelOnFinalProofDriver) retireForFinalProof() error {
	if !d.retired && d.live.PhysicalWriteCount() > 0 {
		requireIdentity := d.live.nodes[d.rel]
		if requireIdentity == nil || requireIdentity.identity != d.identity {
			return fmt.Errorf("retirement target identity changed")
		}
		deleteFakeHierarchySubtree(d.live, d.rel)
		d.retired = true
		return errors.New("injected final proof failure after retirement")
	}
	return nil
}

func (d *retireWrittenRelOnFinalProofDriver) ReadEntry(
	ctx context.Context,
	rel string,
) (EntryState, error) {
	entry, err := d.HierarchyDriver.ReadEntry(ctx, rel)
	if d.retired && rel == d.rel && isCgroupPathAbsent(err) {
		d.absentReads++
	}
	return entry, err
}

func (d *retireWrittenRelOnFinalProofDriver) StatIdentity(
	ctx context.Context,
	rel string,
) (CgroupIdentity, error) {
	identity, err := d.HierarchyDriver.StatIdentity(ctx, rel)
	if d.retired && rel == d.rel && isCgroupPathAbsent(err) {
		d.absentStats++
	}
	return identity, err
}

type preWriteDriftDriver struct {
	HierarchyDriver
	live                 *fakeHierarchyDriver
	initialRels          map[string]struct{}
	preflightSeen        map[string]struct{}
	preflightComplete    bool
	driftAfterWriteCount int
	driftRel             string
	driftedRel           string
	drifted              bool
	forwardWrites        int
}

type frozenPredecessorDriftDriver struct {
	HierarchyDriver
	live                     *fakeHierarchyDriver
	initialRels              map[string]struct{}
	preflightSeen            map[string]struct{}
	preflightComplete        bool
	operation                PlanOperation
	mutate                   func(*fakeHierarchyDriver, PlanOperation)
	drifted                  bool
	writesToDriftedOperation int
}

type readErrorHierarchyDriver struct {
	HierarchyDriver
	err error
	rel string
}

type failNthEntryReadDriver struct {
	HierarchyDriver
	rel    string
	failAt int
	reads  int
	err    error
}

func (d *failNthEntryReadDriver) ReadEntry(
	ctx context.Context,
	rel string,
) (EntryState, error) {
	if rel == d.rel {
		d.reads++
		if d.reads == d.failAt {
			return EntryState{}, d.err
		}
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func (d *readErrorHierarchyDriver) ReadEntry(
	ctx context.Context,
	rel string,
) (EntryState, error) {
	if rel == d.rel {
		return EntryState{}, d.err
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func selectTraceOperation(
	t *testing.T,
	trace *CompiledPhaseTrace,
	matches func(PlanOperation, *CompiledPhaseTrace) bool,
) PlanOperation {
	t.Helper()
	for _, operation := range flattenTraceOperations(trace) {
		if matches(operation, trace) {
			return operation
		}
	}
	t.Fatal("compiled trace has no operation matching predecessor drift case")
	return PlanOperation{}
}

func newFrozenPredecessorDriftDriver(
	live *fakeHierarchyDriver,
	initial *CompleteSnapshot,
	operation PlanOperation,
	mutate func(*fakeHierarchyDriver, PlanOperation),
) *frozenPredecessorDriftDriver {
	rels := make(map[string]struct{}, len(initial.Entries))
	for rel := range initial.Entries {
		rels[rel] = struct{}{}
	}
	return &frozenPredecessorDriftDriver{
		HierarchyDriver: live,
		live:            live,
		initialRels:     rels,
		preflightSeen:   make(map[string]struct{}, len(rels)),
		operation:       operation,
		mutate:          mutate,
	}
}

func (d *frozenPredecessorDriftDriver) ReadEntry(
	ctx context.Context,
	rel string,
) (EntryState, error) {
	if !d.preflightComplete {
		if _, expected := d.initialRels[rel]; expected {
			d.preflightSeen[rel] = struct{}{}
			if len(d.preflightSeen) == len(d.initialRels) {
				d.preflightComplete = true
			}
		}
		return d.HierarchyDriver.ReadEntry(ctx, rel)
	}
	if !d.drifted && rel == d.operation.Rel {
		d.mutate(d.live, d.operation)
		d.drifted = true
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func (d *frozenPredecessorDriftDriver) WriteCPUs(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	cpus machine.CPUSet,
) error {
	if d.drifted && rel == d.operation.Rel {
		d.writesToDriftedOperation++
	}
	return d.HierarchyDriver.WriteCPUs(ctx, rel, identity, cpus)
}

func (d *frozenPredecessorDriftDriver) WriteMems(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	mems string,
) error {
	if d.drifted && rel == d.operation.Rel {
		d.writesToDriftedOperation++
	}
	return d.HierarchyDriver.WriteMems(ctx, rel, identity, mems)
}

func newPreWriteDriftDriver(
	live *fakeHierarchyDriver,
	initial *CompleteSnapshot,
	driftAfterWriteCount int,
	driftRel string,
) *preWriteDriftDriver {
	rels := make(map[string]struct{}, len(initial.Entries))
	for rel := range initial.Entries {
		rels[rel] = struct{}{}
	}
	return &preWriteDriftDriver{
		HierarchyDriver:      live,
		live:                 live,
		initialRels:          rels,
		preflightSeen:        make(map[string]struct{}, len(rels)),
		driftAfterWriteCount: driftAfterWriteCount,
		driftRel:             driftRel,
	}
}

func (d *preWriteDriftDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if !d.preflightComplete {
		if _, expected := d.initialRels[rel]; expected {
			d.preflightSeen[rel] = struct{}{}
			if len(d.preflightSeen) == len(d.initialRels) {
				d.preflightComplete = true
			}
		}
		return d.HierarchyDriver.ReadEntry(ctx, rel)
	}
	if !d.drifted && d.forwardWrites >= d.driftAfterWriteCount &&
		(d.driftRel == "" || d.driftRel == rel) {
		node := d.live.nodes[rel]
		node.configuredCPUs = node.configuredCPUs.Union(machine.NewCPUSet(99))
		node.cpus = node.cpus.Union(machine.NewCPUSet(99))
		d.driftedRel = rel
		d.drifted = true
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func (d *preWriteDriftDriver) WriteCPUs(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	cpus machine.CPUSet,
) error {
	d.forwardWrites++
	return d.HierarchyDriver.WriteCPUs(ctx, rel, identity, cpus)
}

func (d *preWriteDriftDriver) WriteMems(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	mems string,
) error {
	d.forwardWrites++
	return d.HierarchyDriver.WriteMems(ctx, rel, identity, mems)
}

func (d *injectedTraceDriver) StatIdentity(ctx context.Context, rel string) (CgroupIdentity, error) {
	if d.injected && d.injection.blockRollbackIdentity {
		<-ctx.Done()
		return CgroupIdentity{}, ctx.Err()
	}
	return d.HierarchyDriver.StatIdentity(ctx, rel)
}

func (d *injectedTraceDriver) Roots(ctx context.Context) ([]RootRef, error) {
	if !d.injected && d.injection.failFinalProof && d.forwardSuccess == d.expectedForward {
		d.injected = true
		return nil, errors.New("injected final proof failure")
	}
	return d.HierarchyDriver.Roots(ctx)
}

func (d *injectedTraceDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if !d.injected && d.pendingReadback {
		d.pendingReadback = false
		d.injected = true
		if d.injection.driftIdentityOnFailedReadback {
			if live, ok := d.HierarchyDriver.(*fakeHierarchyDriver); ok {
				live.bumpIdentity(rel)
			}
		}
		return EntryState{}, errors.New("injected post-write read-back failure")
	}
	if d.pendingReadback {
		d.pendingReadback = false
		if d.injection.driftIdentityOnFailedReadback {
			if live, ok := d.HierarchyDriver.(*fakeHierarchyDriver); ok {
				live.bumpIdentity(rel)
			}
		}
		return EntryState{}, errors.New("injected uncertain write read-back failure")
	}
	if d.injected && d.injection.blockRollbackIdentity {
		<-ctx.Done()
		return EntryState{}, ctx.Err()
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func (d *injectedTraceDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	if !d.injected && d.injection.failFinalProof && d.forwardSuccess == d.expectedForward {
		d.injected = true
		return nil, errors.New("injected final proof failure")
	}
	return d.HierarchyDriver.ListChildren(ctx, rel)
}

func (d *injectedTraceDriver) WriteCPUs(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	cpus machine.CPUSet,
) error {
	return d.write(ctx, HierarchyOperationWriteCPUs, rel, cpus.String(), func() error {
		return d.HierarchyDriver.WriteCPUs(ctx, rel, identity, cpus)
	})
}

func (d *injectedTraceDriver) WriteMems(
	ctx context.Context,
	rel string,
	identity CgroupIdentity,
	mems string,
) error {
	return d.write(ctx, HierarchyOperationWriteMems, rel, mems, func() error {
		return d.HierarchyDriver.WriteMems(ctx, rel, identity, mems)
	})
}

func (d *injectedTraceDriver) write(
	ctx context.Context,
	resource HierarchyOperation,
	rel string,
	target string,
	delegate func() error,
) error {
	d.writeCalls++
	d.writeAttempts = append(d.writeAttempts, traceWriteAttempt{
		rel: rel, resource: resource, target: target,
	})
	if d.injected && d.injection.failRollbackWriteAt > 0 {
		d.rollbackWriteCalls++
		if d.rollbackWriteCalls == d.injection.failRollbackWriteAt {
			return fmt.Errorf("injected rollback %s failure for %q", resource, rel)
		}
	}
	if !d.injected && d.injection.failWriteAt == d.writeCalls {
		d.injected = true
		return fmt.Errorf("injected %s failure for %q", resource, rel)
	}
	if !d.injected && d.injection.mutateThenFailWriteAt == d.writeCalls {
		if err := delegate(); err != nil {
			return err
		}
		if d.injection.failUncertainReadback {
			d.pendingReadback = true
		}
		d.injected = true
		return fmt.Errorf("injected uncertain %s failure for %q", resource, rel)
	}
	if !d.injected && d.injection.falseSuccessWriteAt == d.writeCalls {
		d.injected = true
		d.forwardSuccess++
		return nil
	}
	err := delegate()
	if err != nil {
		return err
	}
	if !d.injected {
		d.forwardSuccess++
		if d.injection.failPostWriteReadback {
			d.pendingReadback = true
		}
		if d.injection.expireDeadlineAfterWrite == d.forwardSuccess {
			d.injected = true
			if d.expireDeadline != nil {
				d.expireDeadline()
			}
		}
		if d.injection.cancelAfterWrite == d.forwardSuccess {
			d.injected = true
			if d.cancel != nil {
				d.cancel()
			}
		}
	}
	return nil
}

func compiledDedicatedReclaimReplacementTrace(
	t *testing.T,
) (*CompiledPhaseTrace, *fakeHierarchyDriver) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	fixture.driver.capabilities = cgroupV1Policy.capabilities(true)
	fixture.round.allowEmptyTarget = true
	fixture.round.objective = ConvergenceObjectiveFull
	fixture.addPrimary("dedicated", "0-1", "0")
	fixture.addReclaim("reclaimed", "2-3", "0")
	fixture.requireCPUSet("dedicated", "1-2")
	fixture.requireCPUSet("reclaimed", "0,3")

	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot())
	require.NoError(t, err)
	require.True(t, traceContainsOperation(
		trace, "dedicated", WriteShrink, machine.NewCPUSet(1)))
	require.True(t, traceContainsOperation(
		trace, "reclaimed", WriteShrink, machine.NewCPUSet(3)))
	require.True(t, traceContainsOperation(
		trace, "dedicated", WriteGrow, machine.MustParse("1-2")))
	require.True(t, traceContainsOperation(
		trace, "reclaimed", WriteGrow, machine.MustParse("0,3")))
	return trace, fixture.driver
}

func countTraceWritesByPhase(writes []expectedTraceWrite, phase PhaseKind) int {
	count := 0
	for _, write := range writes {
		if write.phase == phase {
			count++
		}
	}
	return count
}

func frozenExecutionRound(
	t *testing.T,
	trace *CompiledPhaseTrace,
	driver HierarchyDriver,
) *coordinatorRound {
	t.Helper()
	return &coordinatorRound{
		dag:       mustBuildTraceDAG(t, trace),
		driver:    driver,
		budget:    NewBudgetTracker(ConvergenceBudget{}),
		objective: trace.Objective,
	}
}

func reserveTraceWithBudget(
	t *testing.T,
	budget *BudgetTracker,
	trace *CompiledPhaseTrace,
) *ExecutionReservationTicket {
	t.Helper()
	ticket, err := budget.ReservePhaseTrace(trace, trace.Cost.Total())
	require.NoError(t, err)
	return ticket
}

func rollbackOnlyTicket(cpuWrites, memWrites int) *ExecutionReservationTicket {
	rollback := PhysicalWriteCost{CPUSetWrites: cpuWrites, MemsWrites: memWrites}
	return &ExecutionReservationTicket{
		reserved: ExecutionReservationCost{
			Rollback: rollback,
		},
		rollbackIOOperations: saturatingMultiply(rollback.Total(), 5),
	}
}

func applyCPUWriteForRollbackTest(
	t *testing.T,
	driver *fakeHierarchyDriver,
	stack *traceMutationStack,
	rel, before, after string,
) {
	t.Helper()
	identity := driver.nodes[rel].identity
	require.NoError(t, driver.WriteCPUs(context.Background(), rel, identity, machine.MustParse(after)))
	stack.writes = append(stack.writes, AppliedPhysicalWrite{
		Rel: rel, Identity: identity, Resource: HierarchyOperationWriteCPUs,
		Before: before, BeforeEffective: before, After: after,
	})
}

func cpuWriteTargets(writes []fakeHierarchyWrite) []string {
	targets := make([]string, 0, len(writes))
	for _, write := range writes {
		targets = append(targets, write.cpus.String())
	}
	return targets
}

func newTracePreflightWriter(driver *fakeHierarchyDriver) safeCPUSetWriter {
	return newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil)
}

func operationWithParent(t *testing.T, trace *CompiledPhaseTrace) PlanOperation {
	t.Helper()
	for _, operation := range flattenTraceOperations(trace) {
		if operation.ParentRel != "" {
			return operation
		}
	}
	t.Fatal("compiled trace has no operation with a parent")
	return PlanOperation{}
}

func replaceTraceOperation(t *testing.T, trace *CompiledPhaseTrace, replacement PlanOperation) {
	t.Helper()
	for phaseIndex := range trace.Phases {
		for operationIndex := range trace.Phases[phaseIndex].Operations {
			operation := &trace.Phases[phaseIndex].Operations[operationIndex]
			if operation.PlanID == replacement.PlanID && operation.Rel == replacement.Rel {
				*operation = replacement
				trace.TraceID = ""
				trace.Cost = executionReservationCost(trace.Phases)
				return
			}
		}
	}
	t.Fatalf("compiled trace operation for rel %q was not found", replacement.Rel)
}

func mustBuildTraceDAG(t *testing.T, trace *CompiledPhaseTrace) *TopoDAG {
	t.Helper()
	dag, err := BuildDAG(cloneNodeSpecs(trace.EvaluationInput.DAGSpecs))
	require.NoError(t, err)
	return dag
}
