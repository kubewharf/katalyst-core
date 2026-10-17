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
	"sync"
	"time"
)

const defaultAdjustmentMaxReplans = 3

var (
	ErrInvalidAdjustmentWriteCount    = errors.New("invalid negative adjustment write count")
	ErrAdjustmentWriteBudgetExceeded  = errors.New("cumulative adjustment write budget exceeded")
	ErrAdjustmentReplanBudgetExceeded = errors.New("adjustment replan budget exceeded")
	ErrAdjustmentDeadlineExceeded     = errors.New("adjustment deadline exceeded")
)

// ReplanDisposition records whether the coordinator has enough physical-state
// evidence to authorize its caller to compile and execute another plan.
type ReplanDisposition uint8

const (
	// ReplanNotAllowed is intentionally the zero value so unknown and omitted
	// outcomes terminate rather than accidentally re-entering the writer.
	ReplanNotAllowed ReplanDisposition = iota
	ReplanSafeNoPhysicalWrites
	ReplanSafeAfterVerifiedRollback
	ReplanSafeFromVerifiedFinalState
)

func (d ReplanDisposition) AllowsReplan() bool {
	return d == ReplanSafeNoPhysicalWrites ||
		d == ReplanSafeAfterVerifiedRollback ||
		d == ReplanSafeFromVerifiedFinalState
}

// AdjustmentBudget owns limits shared by every compile/converge attempt in one
// plugin adjustment. The replan limit is deliberately internal: it preserves
// the former four-total-attempt bound without adding another configuration
// surface. The write limit is derived from the existing convergence budget.
type AdjustmentBudget struct {
	mu sync.Mutex

	ctx        context.Context
	baseBudget ConvergenceBudget
	deadline   time.Time
	maxReplans int
	replans    int
	maxWrites  int

	physicalWrites  int
	rollbackWrites  int
	reservedWrites  int
	writeEpoch      uint64
	observedEpoch   uint64
	initialSnapshot *CompleteSnapshot
}

// AdjustmentWriteReservation atomically reserves the complete forward and
// possible rollback cost before a coordinator writer mutates the hierarchy.
type AdjustmentWriteReservation struct {
	mu                sync.Mutex
	budget            *AdjustmentBudget
	reserved          ExecutionReservationCost
	attemptedForward  PhysicalWriteCost
	attemptedRollback PhysicalWriteCost
	settled           bool
}

// AdjustmentBudgetExceededError preserves the stale and context causes while
// reporting the specific cumulative limit that made another compile unsafe.
type AdjustmentBudgetExceededError struct {
	Replans        int
	MaxReplans     int
	PhysicalWrites int
	RollbackWrites int
	MaxWrites      int
	Reason         error
	Cause          error
	Last           error
	History        error
}

func (e *AdjustmentBudgetExceededError) Error() string {
	message := fmt.Sprintf(
		"topology adjustment exhausted: reason=%v replans=%d max_replans=%d physical_writes=%d rollback_writes=%d max_writes=%d: %v",
		e.Reason, e.Replans, e.MaxReplans, e.PhysicalWrites, e.RollbackWrites, e.MaxWrites, e.Last)
	if e.History != nil {
		message += fmt.Sprintf("; prior failure: %v", e.History)
	}
	return message
}

func (e *AdjustmentBudgetExceededError) Unwrap() error { return e.Last }

func (e *AdjustmentBudgetExceededError) Is(target error) bool {
	return target == e.Reason ||
		errors.Is(e.Last, target) ||
		errors.Is(e.Cause, target) ||
		errors.Is(e.History, target)
}

func (e *AdjustmentBudgetExceededError) As(target interface{}) bool {
	return errors.As(e.Last, target) ||
		errors.As(e.Cause, target) ||
		errors.As(e.History, target)
}

// ReplanBudgetExceededError is retained for callers that inspect the detailed
// exhaustion report. Reason identifies whether writes, replans, or time ended
// the adjustment.
type ReplanBudgetExceededError = AdjustmentBudgetExceededError

func NewAdjustmentBudget(ctx context.Context, convergence ConvergenceBudget) *AdjustmentBudget {
	fixed := BudgetWithInvocationDeadline(ctx, convergence, time.Now())
	return &AdjustmentBudget{
		ctx:        ctx,
		baseBudget: fixed,
		deadline:   fixed.Deadline,
		maxReplans: defaultAdjustmentMaxReplans,
		maxWrites:  adjustmentMaxWrites(convergence),
	}
}

func adjustmentMaxWrites(in ConvergenceBudget) int {
	normalized := NormalizeConvergenceBudget(in)
	rounds := in.MaxRounds
	if rounds <= 0 {
		rounds = defaultCoordinatorAutoRounds
	}
	planOperations := in.MaxPlanOperations
	if planOperations <= 0 {
		planOperations = saturatingMultiply(normalized.MaxSnapshotNodes, rounds)
	}
	// One logical plan operation can write both cpuset.mems and cpuset.cpus.
	// Reserve the matching inverse writes as well so rollback can never be
	// denied after the forward mutation has started.
	return saturatingMultiply(planOperations, 4)
}

func (b *AdjustmentBudget) ConsumeReplan(last error) error {
	if b == nil {
		return ErrAdjustmentReplanBudgetExceeded
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextErrLocked(); err != nil {
		return b.exhaustionErrorLocked(ErrAdjustmentDeadlineExceeded, err, last)
	}
	if saturatingAdd(
		saturatingAdd(b.physicalWrites, b.rollbackWrites),
		b.reservedWrites,
	) >= b.maxWrites {
		return b.exhaustionErrorLocked(ErrAdjustmentWriteBudgetExceeded, nil, last)
	}
	if b.replans >= b.maxReplans {
		return b.exhaustionErrorLocked(ErrAdjustmentReplanBudgetExceeded, nil, last)
	}
	b.replans++
	return nil
}

func (b *AdjustmentBudget) RecordPhysicalWrites(count int) error {
	if count < 0 {
		return ErrInvalidAdjustmentWriteCount
	}
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	exceeds := b.writeIncrementExceedsLocked(count)
	b.physicalWrites = saturatingAdd(b.physicalWrites, count)
	if count != 0 {
		b.writeEpoch++
		b.initialSnapshot = nil
	}
	if exceeds {
		return ErrAdjustmentWriteBudgetExceeded
	}
	return b.checkWritesLocked()
}

func (b *AdjustmentBudget) RecordRollbackWrites(count int) error {
	if count < 0 {
		return ErrInvalidAdjustmentWriteCount
	}
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	exceeds := b.writeIncrementExceedsLocked(count)
	b.rollbackWrites = saturatingAdd(b.rollbackWrites, count)
	if count != 0 {
		b.writeEpoch++
		b.initialSnapshot = nil
	}
	if exceeds {
		return ErrAdjustmentWriteBudgetExceeded
	}
	return b.checkWritesLocked()
}

func (b *AdjustmentBudget) RecordConvergence(result ConvergenceResult) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	forwardExceeds := b.writeIncrementExceedsLocked(result.forwardWriteAttempts)
	b.physicalWrites = saturatingAdd(b.physicalWrites, result.forwardWriteAttempts)
	rollbackExceeds := b.writeIncrementExceedsLocked(result.rollbackWriteAttempts)
	b.rollbackWrites = saturatingAdd(b.rollbackWrites, result.rollbackWriteAttempts)
	if result.forwardWriteAttempts != 0 || result.rollbackWriteAttempts != 0 {
		b.writeEpoch++
		b.initialSnapshot = nil
	}
	if forwardExceeds || rollbackExceeds {
		return ErrAdjustmentWriteBudgetExceeded
	}
	return b.checkWritesLocked()
}

func (b *AdjustmentBudget) writeIncrementExceedsLocked(count int) bool {
	used := saturatingAdd(
		saturatingAdd(b.physicalWrites, b.rollbackWrites),
		b.reservedWrites,
	)
	return count > b.maxWrites-used
}

func (b *AdjustmentBudget) checkWritesLocked() error {
	if saturatingAdd(
		saturatingAdd(b.physicalWrites, b.rollbackWrites),
		b.reservedWrites,
	) > b.maxWrites {
		return ErrAdjustmentWriteBudgetExceeded
	}
	return nil
}

func (b *AdjustmentBudget) ReserveExecution(
	cost ExecutionReservationCost,
) (*AdjustmentWriteReservation, error) {
	if b == nil {
		return &AdjustmentWriteReservation{reserved: cost}, nil
	}
	if !validPhysicalWriteCost(cost.Forward) || !validPhysicalWriteCost(cost.Rollback) {
		return nil, ErrInvalidAdjustmentWriteCount
	}
	required := cost.Total()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.contextErrLocked(); err != nil {
		return nil, b.exhaustionErrorLocked(ErrAdjustmentDeadlineExceeded, err, nil)
	}
	if b.writeIncrementExceedsLocked(required) {
		return nil, ErrAdjustmentWriteBudgetExceeded
	}
	b.reservedWrites = saturatingAdd(b.reservedWrites, required)
	return &AdjustmentWriteReservation{budget: b, reserved: cost}, nil
}

func (r *AdjustmentWriteReservation) Settle(forward, rollback PhysicalWriteCost) error {
	return r.settleTotals(forward.Total(), rollback.Total())
}

// RecordWriteAttempt transfers one or more already-reserved write slots into
// cumulative usage immediately before the corresponding physical syscall.
// Forward attempts remain constrained by both ctx and the original adjustment
// deadline. Rollback attempts have already reserved their capacity and are
// constrained only by the independent recovery ctx, so expiry of the original
// adjustment cannot strand an already-started mutation.
func (r *AdjustmentWriteReservation) RecordWriteAttempt(
	ctx context.Context,
	rollback bool,
	cost PhysicalWriteCost,
) error {
	if r == nil {
		return nil
	}
	if !validPhysicalWriteCost(cost) || cost.Total() == 0 {
		return ErrInvalidAdjustmentWriteCount
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled {
		return ErrInvalidAdjustmentWriteCount
	}
	attempted := &r.attemptedForward
	reserved := r.reserved.Forward
	if rollback {
		attempted = &r.attemptedRollback
		reserved = r.reserved.Rollback
	}
	next := addPhysicalWriteCost(*attempted, cost)
	if next.CPUSetWrites > reserved.CPUSetWrites ||
		next.MemsWrites > reserved.MemsWrites {
		return ErrInvalidAdjustmentWriteCount
	}
	if r.budget == nil {
		*attempted = next
		return nil
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	var contextErr error
	if ctx == nil {
		contextErr = context.Canceled
	} else {
		contextErr = ctx.Err()
	}
	if contextErr == nil && !rollback {
		contextErr = r.budget.contextErrLocked()
	}
	if contextErr != nil {
		return r.budget.exhaustionErrorLocked(
			ErrAdjustmentDeadlineExceeded, contextErr, nil)
	}
	*attempted = next
	r.budget.reservedWrites -= cost.Total()
	if rollback {
		r.budget.rollbackWrites = saturatingAdd(r.budget.rollbackWrites, cost.Total())
	} else {
		r.budget.physicalWrites = saturatingAdd(r.budget.physicalWrites, cost.Total())
	}
	r.budget.writeEpoch++
	r.budget.initialSnapshot = nil
	return nil
}

func (r *AdjustmentWriteReservation) settleTotals(forward, rollback int) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled {
		return nil
	}
	if forward < 0 || rollback < 0 ||
		forward != r.attemptedForward.Total() ||
		rollback != r.attemptedRollback.Total() {
		return ErrInvalidAdjustmentWriteCount
	}
	r.settled = true
	if r.budget == nil {
		return nil
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	unused := r.reserved.Total() - forward - rollback
	r.budget.reservedWrites -= unused
	return nil
}

func (b *AdjustmentBudget) CumulativeWrites() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return saturatingAdd(b.physicalWrites, b.rollbackWrites)
}

func (b *AdjustmentBudget) RemainingConvergenceBudget() ConvergenceBudget {
	if b == nil {
		return ConvergenceBudget{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.baseBudget
	out.Deadline = b.deadline
	return out
}

func (b *AdjustmentBudget) StartFromVerifiedFinalSnapshot(snapshot *CompleteSnapshot) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.initialSnapshot = CloneCompleteSnapshot(snapshot)
}

func (b *AdjustmentBudget) ClearInitialSnapshot() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.initialSnapshot = nil
}

func (b *AdjustmentBudget) InitialSnapshot() *CompleteSnapshot {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.observedEpoch != b.writeEpoch {
		return nil
	}
	return CloneCompleteSnapshot(b.initialSnapshot)
}

// BeginSnapshotObservation captures the latest write-attempt epoch before a
// complete hierarchy scan starts.
func (b *AdjustmentBudget) BeginSnapshotObservation() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeEpoch
}

// ObserveCurrentSnapshot records only writes that preceded the corresponding
// complete snapshot. A concurrent write settled during or after the scan
// remains unobserved and cannot authorize a replan.
func (b *AdjustmentBudget) ObserveCurrentSnapshot(epoch uint64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if epoch >= b.observedEpoch && epoch == b.writeEpoch {
		b.observedEpoch = epoch
	}
}

func (b *AdjustmentBudget) ReplanSafe(disposition ReplanDisposition) bool {
	if b == nil || !disposition.AllowsReplan() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observedEpoch == b.writeEpoch
}

func (b *AdjustmentBudget) ExhaustionError(reason, last error) error {
	if b == nil {
		return last
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var cause error
	if reason == ErrAdjustmentDeadlineExceeded {
		cause = b.contextErrLocked()
	}
	return b.exhaustionErrorLocked(reason, cause, last)
}

// DeadlineErrorWithHistory keeps the current execution failure as the primary
// unwrap chain while retaining stale evidence from an earlier attempt.
func (b *AdjustmentBudget) DeadlineErrorWithHistory(current, history error) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cause := b.contextErrLocked()
	if cause == nil {
		return nil
	}
	exhaustion := &AdjustmentBudgetExceededError{
		Replans:        b.replans,
		MaxReplans:     b.maxReplans,
		PhysicalWrites: b.physicalWrites,
		RollbackWrites: b.rollbackWrites,
		MaxWrites:      b.maxWrites,
		Reason:         ErrAdjustmentDeadlineExceeded,
		Cause:          cause,
		Last:           current,
		History:        history,
	}
	return exhaustion
}

func (b *AdjustmentBudget) contextErrLocked() error {
	if b.ctx != nil {
		if err := b.ctx.Err(); err != nil {
			return err
		}
	}
	if !b.deadline.IsZero() && !time.Now().Before(b.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (b *AdjustmentBudget) exhaustionErrorLocked(reason, cause, last error) error {
	return &AdjustmentBudgetExceededError{
		Replans:        b.replans,
		MaxReplans:     b.maxReplans,
		PhysicalWrites: b.physicalWrites,
		RollbackWrites: b.rollbackWrites,
		MaxWrites:      b.maxWrites,
		Reason:         reason,
		Cause:          cause,
		Last:           last,
	}
}
