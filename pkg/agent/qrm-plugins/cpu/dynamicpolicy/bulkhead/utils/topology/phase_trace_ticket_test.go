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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestReserveValidatedPhaseTraceHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	validated, err := validatedPhaseTraceForTest(context.Background(), trace)
	require.NoError(t, err)
	require.Greater(t, trace.OperationCount(), 1)

	t.Run("before reservation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		ticket, err := NewBudgetTracker(ConvergenceBudget{}).
			reserveValidatedPhaseTrace(ctx, validated, trace.Cost.Total())

		require.Nil(t, ticket)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("while cloning authorization", func(t *testing.T) {
		t.Parallel()
		ctx := &reservationCancellationContext{
			Context:  context.Background(),
			cancelOn: 4,
		}

		ticket, err := NewBudgetTracker(ConvergenceBudget{}).
			reserveValidatedPhaseTrace(ctx, validated, trace.Cost.Total())

		require.Nil(t, ticket)
		require.ErrorIs(t, err, context.Canceled)
		require.GreaterOrEqual(t, ctx.checks, ctx.cancelOn)
	})
}

type reservationCancellationContext struct {
	context.Context
	checks   int
	cancelOn int
}

func (c *reservationCancellationContext) Err() error {
	c.checks++
	if c.checks >= c.cancelOn {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestReservePhaseTraceRejectsOneWriteShortWithoutPhysicalWrites(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	required := trace.Cost.Total()
	require.Positive(t, trace.Cost.Forward.CPUSetWrites)
	require.Positive(t, trace.Cost.Forward.MemsWrites)
	require.Positive(t, trace.Cost.Rollback.CPUSetWrites)
	require.Positive(t, trace.Cost.Rollback.MemsWrites)

	_, err := NewBudgetTracker(ConvergenceBudget{}).ReservePhaseTrace(trace, required-1)

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
	require.Zero(t, driver.PhysicalWriteCount())
}

func TestReservePhaseTraceAcceptsExactWriteBoundary(t *testing.T) {
	t.Parallel()
	trace, driver := compiledTraceWithCPUAndMemoryWrites(t)
	required := trace.Cost.Total()

	ticket, err := NewBudgetTracker(ConvergenceBudget{}).ReservePhaseTrace(trace, required)

	require.NoError(t, err)
	require.Equal(t, trace.Cost, ticket.reserved)
	require.Equal(t, required, ticket.reserved.Total())
	require.Zero(t, driver.PhysicalWriteCount())
}

func TestReservePhaseTraceCountsExactForwardAndInverseWrites(t *testing.T) {
	t.Parallel()
	operations := []PlanOperation{
		{
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			WriteMems:       true,
		},
		{
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			WriteMems:       true,
		},
		{
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			WriteMems:       true,
		},
	}

	cost := executionReservationCost(
		[]CompiledPhase{{Kind: PhaseExpand, Operations: operations}})

	expected := PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1}
	require.Equal(t, ExecutionReservationCost{
		Forward:  expected,
		Rollback: expected,
	}, cost)
}

func TestReservePhaseTraceRollbackIOCoversRetirementConfirmationWorstCase(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)

	ticket := reserveTraceForTest(t, trace)

	require.Equal(t,
		saturatingMultiply(trace.Cost.Rollback.Total(), 5),
		ticket.rollbackIOOperations,
		"each inverse write may require read/write/stat plus final read/stat confirmation")
}

func TestTraceTicketRejectsSkippedOperation(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operations := flattenTraceOperations(trace)
	require.Greater(t, len(operations), 1)
	ticket := reserveTraceForTest(t, trace)

	err := ticket.AuthorizeNext(trace.TraceID, 1, operations[1])

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
	require.NoError(t, ticket.AuthorizeNext(trace.TraceID, 0, operations[0]))
}

func TestTraceTicketRejectsRepeatedOperation(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	ticket := reserveTraceForTest(t, trace)

	require.NoError(t, ticket.AuthorizeNext(trace.TraceID, 0, operation))
	err := ticket.AuthorizeNext(trace.TraceID, 0, operation)

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
}

func TestTraceTicketRejectsReorderedOperation(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operations := flattenTraceOperations(trace)
	require.Greater(t, len(operations), 1)
	require.NotEqual(t, operations[0], operations[1])
	ticket := reserveTraceForTest(t, trace)

	err := ticket.AuthorizeNext(trace.TraceID, 0, operations[1])

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
	require.NoError(t, ticket.AuthorizeNext(trace.TraceID, 0, operations[0]))
}

func TestTraceTicketRejectsTamperedTarget(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	ticket := reserveTraceForTest(t, trace)
	operation.Target.CPUs = operation.Target.CPUs.Union(machine.NewCPUSet(999))

	err := ticket.AuthorizeNext(trace.TraceID, 0, operation)

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
}

func TestTraceTicketRejectsTamperedIdentity(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operations := flattenTraceOperations(trace)

	t.Run("operation identity", func(t *testing.T) {
		t.Parallel()
		ticket := reserveTraceForTest(t, trace)
		operation := operations[0]
		operation.ExpectedIdentity.Inode++
		require.ErrorIs(t,
			ticket.AuthorizeNext(trace.TraceID, 0, operation),
			ErrAdmissionReservationExceeded)
	})

	t.Run("parent identity", func(t *testing.T) {
		t.Parallel()
		index := operationWithParentIndex(t, operations)
		ticket := reserveTraceForTest(t, trace)
		for i := 0; i < index; i++ {
			require.NoError(t, ticket.AuthorizeNext(trace.TraceID, i, operations[i]))
		}
		operation := operations[index]
		operation.ExpectedParentIdentity.Inode++
		require.ErrorIs(t,
			ticket.AuthorizeNext(trace.TraceID, index, operation),
			ErrAdmissionReservationExceeded)
	})
}

func TestTraceTicketRejectsWrongTraceID(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	ticket := reserveTraceForTest(t, trace)

	err := ticket.AuthorizeNext("different-trace", 0, operation)

	require.ErrorIs(t, err, ErrAdmissionReservationExceeded)
	require.NoError(t, ticket.AuthorizeNext(trace.TraceID, 0, operation))
}

func TestTraceTicketAuthorizesRepeatedRelationByIndex(t *testing.T) {
	t.Parallel()
	base, _ := compiledTraceWithCPUAndMemoryWrites(t)
	trace := traceWithRepeatedRelation(t, base)
	operations := flattenTraceOperations(trace)
	repeatedRel := repeatedOperationRel(t, operations)
	ticket := reserveTraceForTest(t, trace)

	authorizedForRel := 0
	for i, operation := range operations {
		require.NoError(t, ticket.AuthorizeNext(trace.TraceID, i, operation))
		if operation.Rel == repeatedRel {
			authorizedForRel++
		}
	}

	require.GreaterOrEqual(t, authorizedForRel, 2)
}

func TestTraceTicketSeparatesForwardAndRollbackConsumption(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	ticket := reserveTraceForTest(t, trace)

	require.NoError(t, ticket.consumeRollback(ticket.reserved.Rollback))
	require.Equal(t, ticket.reserved.Rollback, ticket.consumedRollback)
	require.Equal(t, PhysicalWriteCost{}, ticket.consumedForward)

	require.NoError(t, ticket.consumeForward(ticket.reserved.Forward))
	require.Equal(t, ticket.reserved.Forward, ticket.consumedForward)
	require.ErrorIs(t,
		ticket.consumeForward(PhysicalWriteCost{CPUSetWrites: 1}),
		ErrAdmissionReservationExceeded)
	require.ErrorIs(t,
		ticket.consumeRollback(PhysicalWriteCost{MemsWrites: 1}),
		ErrAdmissionReservationExceeded)
}

func TestTraceTicketReleaseRejectsAuthorizationAndConsumption(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	operation := flattenTraceOperations(trace)[0]
	ticket := reserveTraceForTest(t, trace)

	ticket.ReleaseUnused()
	ticket.ReleaseUnused()

	require.ErrorIs(t,
		ticket.AuthorizeNext(trace.TraceID, 0, operation),
		ErrAdmissionReservationExceeded)
	require.ErrorIs(t,
		ticket.consumeForward(PhysicalWriteCost{CPUSetWrites: 1}),
		ErrAdmissionReservationExceeded)
	require.ErrorIs(t,
		ticket.consumeRollback(PhysicalWriteCost{CPUSetWrites: 1}),
		ErrAdmissionReservationExceeded)
}

func compiledTraceWithCPUAndMemoryWrites(
	t *testing.T,
) (*CompiledPhaseTrace, *fakeHierarchyDriver) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	for i := range fixture.specs {
		if fixture.specs[i].Domain == DomainPrimary {
			fixture.specs[i].Mems = "0-1"
		}
	}
	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	return trace, fixture.driver
}

func compiledTraceWithNonControlledDynamicWrite(
	t *testing.T,
) (*CompiledPhaseTrace, *fakeHierarchyDriver, string) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	fixture.selection.MaxCPUsDrainRatio = 0.5
	fixture.driver.capabilities = cgroupV2Policy.capabilities(true)
	fixture.round.allowEmptyTarget = true
	fixture.cpuDetails[4] = machine.CPUTopoInfo{NUMANodeID: 0}
	fixture.addPrimary("kubepods", "1-3", "0")
	const dynamicRel = "kubepods/besteffort"
	fixture.driver.add(dynamicRel,
		CgroupIdentity{Device: 1, Inode: fixture.allocInode()}, "1-3", "0")
	fixture.dynamicByRel[dynamicRel] = machine.MustParse("0-3")
	fixture.requireCPUSet(dynamicRel, "0-3")
	fixture.addReclaim("reclaimed", "4", "0")
	fixture.addReclaim("reclaimed/leaf", "0", "0")
	fixture.specs[len(fixture.specs)-1].CPUs = machine.NewCPUSet()
	fixture.targetByRel["reclaimed/leaf"] = machine.NewCPUSet()
	fixture.requireCPUSet("kubepods", "0-3")

	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)
	require.True(t, traceContainsOperation(
		trace, dynamicRel, WriteGrow, machine.MustParse("0-3")))
	return trace, fixture.driver, dynamicRel
}

func reserveTraceForTest(
	t *testing.T,
	trace *CompiledPhaseTrace,
) *ExecutionReservationTicket {
	t.Helper()
	ticket, err := NewBudgetTracker(ConvergenceBudget{}).ReservePhaseTrace(
		trace,
		trace.Cost.Total(),
	)
	require.NoError(t, err)
	return ticket
}

func traceWithRepeatedRelation(
	t *testing.T,
	base *CompiledPhaseTrace,
) *CompiledPhaseTrace {
	t.Helper()
	trace := *base
	trace.TraceID = ""
	trace.Phases = cloneCompiledPhases(base.Phases)
	for phaseIndex := range trace.Phases {
		for operationIndex, operation := range trace.Phases[phaseIndex].Operations {
			if operation.Target.CPUs.IsEmpty() {
				continue
			}
			repeated := clonePlanOperation(operation)
			repeated.ExpectedCurrent = cloneCPUSetTarget(operation.Target)
			repeated.WriteMems = false
			operations := trace.Phases[phaseIndex].Operations
			operations = append(operations, PlanOperation{})
			copy(operations[operationIndex+2:], operations[operationIndex+1:])
			operations[operationIndex+1] = repeated
			trace.Phases[phaseIndex].Operations = operations
			trace.Cost = executionReservationCost(trace.Phases)
			frozen, err := FreezePhaseTrace(&trace)
			require.NoError(t, err)
			return frozen
		}
	}
	t.Fatal("compiled trace has no non-empty target operation")
	return nil
}

func operationWithParentIndex(t *testing.T, operations []PlanOperation) int {
	t.Helper()
	for i, operation := range operations {
		if operation.ParentRel != "" {
			return i
		}
	}
	t.Fatal("compiled trace has no operation with a parent")
	return -1
}

func repeatedOperationRel(t *testing.T, operations []PlanOperation) string {
	t.Helper()
	counts := make(map[string]int)
	for _, operation := range operations {
		counts[operation.Rel]++
		if counts[operation.Rel] == 2 {
			return operation.Rel
		}
	}
	t.Fatal("compiled trace has no repeated relation")
	return ""
}
