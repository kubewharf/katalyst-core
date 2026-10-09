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
	"fmt"
	"sync"
)

type frozenOperationAuthorization struct {
	phaseIndex          int
	phaseOperationIndex int
	operation           PlanOperation
}

// ExecutionReservationTicket authorizes one frozen trace in strict global
// operation order and accounts forward and rollback writes independently.
type ExecutionReservationTicket struct {
	mu               sync.Mutex
	traceID          string
	operations       []frozenOperationAuthorization
	nextOperation    int
	reserved         ExecutionReservationCost
	consumedForward  PhysicalWriteCost
	consumedRollback PhysicalWriteCost
	// Rollback hierarchy I/O is isolated from the ordinary convergence budget,
	// but remains finite. Every reserved inverse write permits one identity
	// check, one write, one retirement-confirmation stat, one final read-back,
	// and one final-verification confirmation stat. Both stats are needed in
	// the worst case when the first confirmation fails closed and final
	// verification independently observes typed ENOENT.
	rollbackIOOperations         int
	consumedRollbackIOOperations int
	released                     bool
}

// reserveValidatedPhaseTrace is the sole owner of reserving write authority for
// a compiler-produced trace. The carrier has already been cloned, closed, and
// validated once, so reservation derives ordered forward and rollback authority
// only from that immutable evidence. Failure returns no ticket and consumes no
// operation authorization, making reservation atomic from the caller's view.
func (b *BudgetTracker) reserveValidatedPhaseTrace(
	ctx context.Context,
	validated *validatedPhaseTrace,
	maxRequiredWrites int,
) (*ExecutionReservationTicket, error) {
	if b == nil {
		return nil, fmt.Errorf("phase trace reservation requires budget tracker")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxRequiredWrites < 0 {
		return nil, fmt.Errorf("%w: maximum required writes must not be negative: %d",
			ErrAdmissionReservationExceeded, maxRequiredWrites)
	}

	frozen, err := validated.trace()
	if err != nil {
		return nil, err
	}
	reserved := frozen.Cost
	required := reserved.Total()
	if maxRequiredWrites > 0 && required > maxRequiredWrites {
		return nil, fmt.Errorf("%w before frozen trace execution: limit=%d required=%d",
			ErrAdmissionReservationExceeded, maxRequiredWrites, required)
	}

	operations := make([]frozenOperationAuthorization, 0)
	for phaseIndex, phase := range frozen.Phases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for phaseOperationIndex, operation := range phase.Operations {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			operations = append(operations, frozenOperationAuthorization{
				phaseIndex:          phaseIndex,
				phaseOperationIndex: phaseOperationIndex,
				operation:           clonePlanOperation(operation),
			})
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &ExecutionReservationTicket{
		traceID:              frozen.TraceID,
		operations:           operations,
		reserved:             reserved,
		rollbackIOOperations: saturatingMultiply(reserved.Rollback.Total(), 5),
	}, nil
}

// AuthorizeNext accepts exactly the next global operation of the frozen trace.
// A rejected authorization does not advance the ticket.
func (t *ExecutionReservationTicket) AuthorizeNext(
	traceID string,
	operationIndex int,
	operation PlanOperation,
) error {
	if t == nil {
		return fmt.Errorf("%w: execution reservation ticket is nil",
			ErrAdmissionReservationExceeded)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.released {
		return fmt.Errorf("%w: ticket already released", ErrAdmissionReservationExceeded)
	}
	if traceID != t.traceID {
		return fmt.Errorf("%w: trace identity mismatch", ErrAdmissionReservationExceeded)
	}
	if operationIndex != t.nextOperation {
		return fmt.Errorf("%w: operation index out of order: got=%d want=%d",
			ErrAdmissionReservationExceeded, operationIndex, t.nextOperation)
	}
	if t.nextOperation >= len(t.operations) {
		return fmt.Errorf("%w: operation index %d exceeds frozen trace length %d",
			ErrAdmissionReservationExceeded, operationIndex, len(t.operations))
	}
	expected := t.operations[t.nextOperation]
	if !planOperationsEqual(expected.operation, operation) {
		return fmt.Errorf(
			"%w: operation %d does not match frozen phase %d operation %d",
			ErrAdmissionReservationExceeded, operationIndex,
			expected.phaseIndex, expected.phaseOperationIndex)
	}
	t.nextOperation++
	return nil
}

func (t *ExecutionReservationTicket) consumeForward(cost PhysicalWriteCost) error {
	if t == nil {
		return fmt.Errorf("%w: execution reservation ticket is nil",
			ErrAdmissionReservationExceeded)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return fmt.Errorf("%w: ticket already released", ErrAdmissionReservationExceeded)
	}
	if !validPhysicalWriteCost(cost) {
		return fmt.Errorf("%w: invalid forward write cost %+v",
			ErrAdmissionReservationExceeded, cost)
	}
	remaining := subtractPhysicalWriteCost(t.reserved.Forward, t.consumedForward)
	if !physicalWriteCostFits(cost, remaining) {
		return fmt.Errorf("%w: forward remaining=%+v requested=%+v",
			ErrAdmissionReservationExceeded, remaining, cost)
	}
	t.consumedForward = addPhysicalWriteCost(t.consumedForward, cost)
	return nil
}

func (t *ExecutionReservationTicket) consumeRollback(cost PhysicalWriteCost) error {
	if t == nil {
		return fmt.Errorf("%w: execution reservation ticket is nil",
			ErrAdmissionReservationExceeded)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return fmt.Errorf("%w: ticket already released", ErrAdmissionReservationExceeded)
	}
	if !validPhysicalWriteCost(cost) {
		return fmt.Errorf("%w: invalid rollback write cost %+v",
			ErrAdmissionReservationExceeded, cost)
	}
	remaining := subtractPhysicalWriteCost(t.reserved.Rollback, t.consumedRollback)
	if !physicalWriteCostFits(cost, remaining) {
		return fmt.Errorf("%w: rollback remaining=%+v requested=%+v",
			ErrAdmissionReservationExceeded, remaining, cost)
	}
	t.consumedRollback = addPhysicalWriteCost(t.consumedRollback, cost)
	return nil
}

func (t *ExecutionReservationTicket) consumeRollbackIO() error {
	if t == nil {
		return fmt.Errorf("%w: execution reservation ticket is nil",
			ErrAdmissionReservationExceeded)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return fmt.Errorf("%w: ticket already released", ErrAdmissionReservationExceeded)
	}
	if t.consumedRollbackIOOperations >= t.rollbackIOOperations {
		return fmt.Errorf("%w: rollback hierarchy I/O remaining=0 requested=1",
			ErrAdmissionReservationExceeded)
	}
	t.consumedRollbackIOOperations++
	return nil
}

func (t *ExecutionReservationTicket) ReleaseUnused() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.released = true
}

func validPhysicalWriteCost(cost PhysicalWriteCost) bool {
	return cost.CPUSetWrites >= 0 && cost.MemsWrites >= 0
}

func clonePlanOperation(operation PlanOperation) PlanOperation {
	operation.ExpectedChildUnion = operation.ExpectedChildUnion.Clone()
	operation.ExpectedCurrent = cloneCPUSetTarget(operation.ExpectedCurrent)
	operation.Target = cloneCPUSetTarget(operation.Target)
	return operation
}

func planOperationsEqual(left, right PlanOperation) bool {
	return left.PlanID == right.PlanID &&
		left.Rel == right.Rel &&
		left.ExpectedIdentity == right.ExpectedIdentity &&
		left.ExpectedChildren == right.ExpectedChildren &&
		left.ExpectedChildUnion.Equals(right.ExpectedChildUnion) &&
		left.ParentRel == right.ParentRel &&
		left.ExpectedParentIdentity == right.ExpectedParentIdentity &&
		left.ExpectedCurrent.Mems == right.ExpectedCurrent.Mems &&
		left.ExpectedCurrent.CPUs.Equals(right.ExpectedCurrent.CPUs) &&
		left.Target.Mems == right.Target.Mems &&
		left.Target.CPUs.Equals(right.Target.CPUs) &&
		left.Direction == right.Direction &&
		left.OwnsMems == right.OwnsMems &&
		left.WriteMems == right.WriteMems &&
		left.Requirement == right.Requirement
}
