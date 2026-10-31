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
	"math"
	"sync"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestReplanDispositionDefaultsToNotAllowed(t *testing.T) {
	t.Parallel()
	var disposition ReplanDisposition
	if disposition != ReplanNotAllowed {
		t.Fatalf("zero disposition = %v, want ReplanNotAllowed", disposition)
	}
	if disposition.AllowsReplan() {
		t.Fatal("zero disposition must terminate rather than authorize replan")
	}
}

func TestReplanDispositionAllowsOnlyProvenSafeStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		disposition ReplanDisposition
		want        bool
	}{
		{name: "not allowed", disposition: ReplanNotAllowed, want: false},
		{name: "no physical writes", disposition: ReplanSafeNoPhysicalWrites, want: true},
		{name: "verified rollback", disposition: ReplanSafeAfterVerifiedRollback, want: true},
		{name: "verified final state", disposition: ReplanSafeFromVerifiedFinalState, want: true},
		{name: "unknown", disposition: ReplanDisposition(255), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.disposition.AllowsReplan(); got != tt.want {
				t.Fatalf("AllowsReplan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdjustmentBudgetUsesConservativeInternalReplanDefault(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 8,
	})

	for i := 0; i < 3; i++ {
		if err := budget.ConsumeReplan(errors.New("stale")); err != nil {
			t.Fatalf("ConsumeReplan() error at replan %d: %v", i+1, err)
		}
	}
	err := budget.ConsumeReplan(errors.New("stale"))
	if !errors.Is(err, ErrAdjustmentReplanBudgetExceeded) {
		t.Fatalf("fourth replan error = %v, want %v", err, ErrAdjustmentReplanBudgetExceeded)
	}
	if errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("replan exhaustion must not report write-budget exhaustion: %v", err)
	}
}

func TestAdjustmentBudgetDerivesCumulativeWritesFromConvergenceBudget(t *testing.T) {
	t.Parallel()
	explicit := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	reservation, err := explicit.ReserveExecution(ExecutionReservationCost{
		Forward:  PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
		Rollback: PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
	})
	if err != nil {
		t.Fatalf("reserve one dual-write operation: %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1}); err != nil {
		t.Fatalf("RecordWriteAttempt() error = %v", err)
	}
	reservation.Settle(
		PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
		PhysicalWriteCost{},
	)
	if got := explicit.CumulativeWrites(); got != 2 {
		t.Fatalf("actual cumulative writes = %d, want 2", got)
	}
	if got := explicit.RemainingConvergenceBudget().MaxPlanOperations; got != 1 {
		t.Fatalf("remaining logical plan operations = %d, want 1", got)
	}

	automatic := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxSnapshotNodes: 2,
		MaxRounds:        3,
	})
	if got := automatic.RemainingConvergenceBudget().MaxPlanOperations; got != 0 {
		t.Fatalf("automatic plan-operation budget = %d, want coordinator-owned auto value 0", got)
	}
}

func TestAdjustmentBudgetSharesDeadlineAndCumulativeWrites(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	budget := NewAdjustmentBudget(ctx, ConvergenceBudget{
		MaxPlanOperations: 9,
		DeadlineDuration:  10 * time.Minute,
	})

	first := budget.RemainingConvergenceBudget()
	if err := budget.RecordPhysicalWrites(2); err != nil {
		t.Fatalf("RecordPhysicalWrites: %v", err)
	}
	if err := budget.RecordRollbackWrites(3); err != nil {
		t.Fatalf("RecordRollbackWrites: %v", err)
	}
	second := budget.RemainingConvergenceBudget()

	if !first.Deadline.Equal(second.Deadline) || first.Deadline.After(deadline) {
		t.Fatalf("deadline changed across attempts: first=%s second=%s context=%s",
			first.Deadline, second.Deadline, deadline)
	}
	if second.MaxPlanOperations != 9 {
		t.Fatalf("remaining logical plan operations = %d, want 9", second.MaxPlanOperations)
	}
}

func TestAdjustmentBudgetReservationFailureDoesNotConsumeBudget(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	budget.maxWrites = 3
	cost := ExecutionReservationCost{
		Forward:  PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
		Rollback: PhysicalWriteCost{CPUSetWrites: 1, MemsWrites: 1},
	}

	reservation, err := budget.ReserveExecution(cost)
	if !errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("ReserveExecution() error = %v, want %v", err, ErrAdjustmentWriteBudgetExceeded)
	}
	if reservation != nil {
		t.Fatalf("failed reservation = %#v, want nil", reservation)
	}
	if got := budget.CumulativeWrites(); got != 0 {
		t.Fatalf("failed reservation consumed %d writes, want 0", got)
	}
}

func TestAdjustmentBudgetReserveExecutionChecksFixedDeadlineAndContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		newBudget func() *AdjustmentBudget
		wantCause error
	}{
		{
			name: "context canceled",
			newBudget: func() *AdjustmentBudget {
				ctx, cancel := context.WithCancel(context.Background())
				budget := NewAdjustmentBudget(ctx, ConvergenceBudget{MaxPlanOperations: 1})
				cancel()
				return budget
			},
			wantCause: context.Canceled,
		},
		{
			name: "fixed deadline expired",
			newBudget: func() *AdjustmentBudget {
				return NewAdjustmentBudget(context.Background(), ConvergenceBudget{
					MaxPlanOperations: 1,
					Deadline:          time.Now().Add(-time.Second),
				})
			},
			wantCause: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			budget := tt.newBudget()
			reservation, err := budget.ReserveExecution(ExecutionReservationCost{
				Forward: PhysicalWriteCost{CPUSetWrites: 1},
			})
			if reservation != nil {
				t.Fatalf("expired reservation = %#v, want nil", reservation)
			}
			if !errors.Is(err, ErrAdjustmentDeadlineExceeded) ||
				!errors.Is(err, tt.wantCause) {
				t.Fatalf("ReserveExecution() error = %v, want deadline sentinel and %v", err, tt.wantCause)
			}
			if got := budget.CumulativeWrites(); got != 0 {
				t.Fatalf("expired reservation consumed %d writes, want 0", got)
			}
		})
	}
}

func TestAdjustmentReservationRechecksDeadlineBeforeWriteAttempt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	budget := NewAdjustmentBudget(ctx, ConvergenceBudget{MaxPlanOperations: 1})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	cancel()

	err = reservation.RecordWriteAttempt(ctx, false, PhysicalWriteCost{CPUSetWrites: 1})

	if !errors.Is(err, ErrAdjustmentDeadlineExceeded) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("RecordWriteAttempt() error = %v, want deadline sentinel and context cancellation", err)
	}
	if got := budget.CumulativeWrites(); got != 0 {
		t.Fatalf("expired deferred write recorded %d writes, want 0", got)
	}
	if err := reservation.Settle(PhysicalWriteCost{}, PhysicalWriteCost{}); err != nil {
		t.Fatalf("settle zero-write expired reservation: %v", err)
	}
}

func TestAdjustmentReservationRollbackUsesIndependentRecoveryContext(t *testing.T) {
	t.Parallel()
	adjustmentCtx, cancelAdjustment := context.WithCancel(context.Background())
	budget := NewAdjustmentBudget(adjustmentCtx, ConvergenceBudget{MaxPlanOperations: 1})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward:  PhysicalWriteCost{MemsWrites: 1},
		Rollback: PhysicalWriteCost{MemsWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		adjustmentCtx, false, PhysicalWriteCost{MemsWrites: 1}); err != nil {
		t.Fatalf("forward RecordWriteAttempt() error = %v", err)
	}
	cancelAdjustment()

	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), time.Second)
	defer cancelRecovery()
	if err := reservation.RecordWriteAttempt(
		recoveryCtx, true, PhysicalWriteCost{MemsWrites: 1}); err != nil {
		t.Fatalf("rollback RecordWriteAttempt() error = %v, want reserved recovery after adjustment cancellation", err)
	}
	if got := budget.CumulativeWrites(); got != 2 {
		t.Fatalf("cumulative writes = %d, want forward and rollback attempts", got)
	}
}

func TestAdjustmentRecoveryDurationDerivesBoundedWindowFromRollbackWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		writes int
		want   time.Duration
	}{
		{name: "no writes", writes: 0, want: adjustmentRecoveryBase},
		{name: "one write", writes: 1, want: adjustmentRecoveryBase + adjustmentRecoveryPerWrite},
		{
			name:   "full admission",
			writes: fullAdmissionWriteCount,
			want:   adjustmentRecoveryBase + fullAdmissionWriteCount*adjustmentRecoveryPerWrite,
		},
		{name: "at cap", writes: int((adjustmentRecoveryCap - adjustmentRecoveryBase) / adjustmentRecoveryPerWrite), want: adjustmentRecoveryCap},
		{name: "above cap", writes: int((adjustmentRecoveryCap-adjustmentRecoveryBase)/adjustmentRecoveryPerWrite) + 1, want: adjustmentRecoveryCap},
		{name: "overflow input", writes: math.MaxInt, want: adjustmentRecoveryCap},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := adjustmentRecoveryDuration(tt.writes)
			if err != nil {
				t.Fatalf("adjustmentRecoveryDuration(%d) error = %v", tt.writes, err)
			}
			if got != tt.want {
				t.Fatalf("adjustmentRecoveryDuration(%d) = %s, want %s", tt.writes, got, tt.want)
			}
		})
	}
}

func TestAdjustmentRecoveryDurationRejectsNegativeWritesFailClosed(t *testing.T) {
	t.Parallel()
	if _, err := adjustmentRecoveryDuration(-1); !errors.Is(err, ErrInvalidAdjustmentWriteCount) {
		t.Fatalf("adjustmentRecoveryDuration(-1) error = %v, want %v",
			err, ErrInvalidAdjustmentWriteCount)
	}
	if ctx, cancel, err := newAdjustmentRecoveryContext(-1); err == nil || ctx != nil || cancel != nil {
		t.Fatalf("newAdjustmentRecoveryContext(-1) = (%v, %v, %v), want nil context/cancel and error",
			ctx, cancel, err)
	}
}

func TestAdjustmentRecoveryContextUsesHardCappedDeadline(t *testing.T) {
	t.Parallel()
	start := time.Now()
	ctx, cancel, err := newAdjustmentRecoveryContext(math.MaxInt)
	if err != nil {
		t.Fatalf("newAdjustmentRecoveryContext() error = %v", err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("recovery context has no deadline")
	}
	if remaining := deadline.Sub(start); remaining < adjustmentRecoveryCap ||
		remaining > adjustmentRecoveryCap+100*time.Millisecond {
		t.Fatalf("recovery deadline from start = %s, want hard cap %s", remaining, adjustmentRecoveryCap)
	}
}

func TestAdjustmentReservationRollbackRecoveryDeadlineFailsClosed(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward:  PhysicalWriteCost{CPUSetWrites: 1},
		Rollback: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		t.Fatalf("forward RecordWriteAttempt() error = %v", err)
	}
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	cancelRecovery()

	err = reservation.RecordWriteAttempt(
		recoveryCtx, true, PhysicalWriteCost{CPUSetWrites: 1})

	if !errors.Is(err, ErrAdjustmentDeadlineExceeded) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("rollback RecordWriteAttempt() error = %v, want recovery deadline/context causes", err)
	}
	if got := budget.CumulativeWrites(); got != 1 {
		t.Fatalf("cumulative writes = %d, want only the forward attempt", got)
	}
}

func TestAdjustmentBudgetWriteExhaustionPreservesWriteAndStaleCauses(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	stale := &PlanStaleError{Rel: "root", Resource: "test"}
	budget.maxWrites = 0

	err := budget.ConsumeReplan(stale)

	if !errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("exhaustion error = %v, want write-budget cause", err)
	}
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("exhaustion error = %v, want last stale cause", err)
	}
}

func TestAdjustmentBudgetDeadlineExhaustionPreservesDeadlineAndStaleCauses(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	budget := NewAdjustmentBudget(ctx, ConvergenceBudget{MaxPlanOperations: 1})
	stale := &PlanStaleError{Rel: "root", Resource: "test"}
	cancel()

	err := budget.ConsumeReplan(stale)

	if !errors.Is(err, ErrAdjustmentDeadlineExceeded) {
		t.Fatalf("exhaustion error = %v, want deadline sentinel", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("exhaustion error = %v, want context cause", err)
	}
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("exhaustion error = %v, want stale cause", err)
	}
	if errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("deadline exhaustion must not report write-budget exhaustion: %v", err)
	}
}

func TestAdjustmentBudgetRequiresFreshSnapshotAfterExternalWrite(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 2})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		t.Fatalf("RecordWriteAttempt() error = %v", err)
	}
	if err := reservation.Settle(PhysicalWriteCost{CPUSetWrites: 1}, PhysicalWriteCost{}); err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	if budget.ReplanSafe(ReplanSafeNoPhysicalWrites) {
		t.Fatal("unobserved deferred-leaf write authorized a replan")
	}

	budget.ObserveCurrentSnapshot(budget.BeginSnapshotObservation())
	if !budget.ReplanSafe(ReplanSafeNoPhysicalWrites) {
		t.Fatal("fresh snapshot did not authorize replan after observing deferred-leaf write")
	}
}

func TestAdjustmentBudgetSnapshotDoesNotObserveConcurrentLaterWrite(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 2})
	epoch := budget.BeginSnapshotObservation()
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		t.Fatalf("RecordWriteAttempt() error = %v", err)
	}
	if err := reservation.Settle(PhysicalWriteCost{CPUSetWrites: 1}, PhysicalWriteCost{}); err != nil {
		t.Fatalf("Settle() error = %v", err)
	}

	budget.ObserveCurrentSnapshot(epoch)

	if budget.ReplanSafe(ReplanSafeNoPhysicalWrites) {
		t.Fatal("snapshot started before a concurrent write must not observe that write")
	}
}

func TestAdjustmentBudgetWriteAttemptInvalidatesSnapshotBeforeSettle(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 2})
	epoch := budget.BeginSnapshotObservation()
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}

	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		t.Fatalf("RecordWriteAttempt() error = %v", err)
	}
	budget.ObserveCurrentSnapshot(epoch)

	if budget.ReplanSafe(ReplanSafeFromVerifiedFinalState) {
		t.Fatal("snapshot started before a real write attempt must not authorize a replan")
	}
	if got := budget.CumulativeWrites(); got != 1 {
		t.Fatalf("cumulative writes before settle = %d, want 1", got)
	}
	if err := reservation.Settle(
		PhysicalWriteCost{CPUSetWrites: 1}, PhysicalWriteCost{}); err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	if got := budget.CumulativeWrites(); got != 1 {
		t.Fatalf("settle advanced cumulative writes to %d, want 1", got)
	}
}

func TestCoordinatorFreshSnapshotObservesExternalWrite(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 2})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Forward: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	if err := reservation.RecordWriteAttempt(
		context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		t.Fatalf("RecordWriteAttempt() error = %v", err)
	}
	if err := reservation.Settle(PhysicalWriteCost{CPUSetWrites: 1}, PhysicalWriteCost{}); err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	round := &coordinatorRound{
		budget:           NewBudgetTracker(ConvergenceBudget{}),
		adjustmentBudget: budget,
		snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
			return &CompleteSnapshot{Entries: map[string]EntryState{}}, nil
		},
	}

	if _, err := round.nextRawSnapshot(context.Background()); err != nil {
		t.Fatalf("nextRawSnapshot() error = %v", err)
	}
	if !budget.ReplanSafe(ReplanSafeNoPhysicalWrites) {
		t.Fatal("coordinator's newly collected snapshot did not observe the deferred write")
	}
}

func TestCoordinatorRawSnapshotReturnsTypedStaleWithoutInternalRetry(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 2})
	calls := 0
	stale := &SnapshotError{
		Operation: HierarchyOperationRead,
		Rel:       "root",
		Class:     HierarchyErrorStale,
		Identity:  CgroupIdentity{Device: 1, Inode: 2},
		Err:       errors.New("replaced while scanning"),
	}
	round := &coordinatorRound{
		budget:           NewBudgetTracker(ConvergenceBudget{}),
		adjustmentBudget: budget,
		snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
			calls++
			if calls == 1 {
				return nil, stale
			}
			return &CompleteSnapshot{Entries: map[string]EntryState{}}, nil
		},
	}

	snapshot, err := round.nextRawSnapshot(context.Background())

	if snapshot != nil || !errors.Is(err, stale) {
		t.Fatalf("nextRawSnapshot() = (%#v, %v), want original typed stale", snapshot, err)
	}
	if calls != 1 {
		t.Fatalf("snapshot source calls = %d, want one owner-visible compile attempt", calls)
	}
}

func TestAdjustmentBudgetConcurrentReserveAndIdempotentSettle(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{MaxPlanOperations: 1})
	const workers = 16
	var wg sync.WaitGroup
	reservations := make(chan *AdjustmentWriteReservation, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, err := budget.ReserveExecution(ExecutionReservationCost{
				Forward: PhysicalWriteCost{CPUSetWrites: 1},
			})
			if err == nil {
				reservations <- reservation
				return
			}
			if !errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
				t.Errorf("ReserveExecution() error = %v, want write-budget exhaustion", err)
			}
		}()
	}
	wg.Wait()
	close(reservations)

	var settled sync.WaitGroup
	count := 0
	for reservation := range reservations {
		count++
		if err := reservation.RecordWriteAttempt(
			context.Background(), false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
			t.Fatalf("RecordWriteAttempt() error = %v", err)
		}
		for i := 0; i < 2; i++ {
			settled.Add(1)
			go func(r *AdjustmentWriteReservation) {
				defer settled.Done()
				if err := r.Settle(PhysicalWriteCost{CPUSetWrites: 1}, PhysicalWriteCost{}); err != nil {
					t.Errorf("Settle() error = %v", err)
				}
			}(reservation)
		}
	}
	settled.Wait()
	if count != 4 {
		t.Fatalf("successful reservations = %d, want 4", count)
	}
	if got := budget.CumulativeWrites(); got != 4 {
		t.Fatalf("idempotently settled cumulative writes = %d, want 4", got)
	}
}

func TestAdjustmentBudgetRejectsNegativeAndSaturatesCumulativeCounts(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: math.MaxInt,
	})
	if err := budget.RecordPhysicalWrites(-1); !errors.Is(err, ErrInvalidAdjustmentWriteCount) {
		t.Fatalf("negative physical writes error = %v, want %v", err, ErrInvalidAdjustmentWriteCount)
	}
	if err := budget.RecordRollbackWrites(-1); !errors.Is(err, ErrInvalidAdjustmentWriteCount) {
		t.Fatalf("negative rollback writes error = %v, want %v", err, ErrInvalidAdjustmentWriteCount)
	}
	if err := budget.RecordPhysicalWrites(math.MaxInt - 1); err != nil {
		t.Fatalf("record near-limit physical writes: %v", err)
	}
	err := budget.RecordRollbackWrites(10)
	if !errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("overflowing cumulative writes error = %v, want %v", err, ErrAdjustmentWriteBudgetExceeded)
	}
	if got := budget.CumulativeWrites(); got != math.MaxInt {
		t.Fatalf("saturated cumulative writes = %d, want %d", got, math.MaxInt)
	}
}

func TestAdjustmentBudgetRecordsCoordinatorWriteEvidence(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	result := ConvergenceResult{
		forwardWriteAttempts: 2,
	}

	if err := budget.RecordConvergence(result); err != nil {
		t.Fatalf("successful convergence must not fail after two physical writes: %v", err)
	}
	if got := budget.CumulativeWrites(); got != 2 {
		t.Fatalf("cumulative writes = %d, want 2", got)
	}
	if got := budget.RemainingConvergenceBudget().MaxPlanOperations; got != 1 {
		t.Fatalf("remaining logical operation budget = %d, want 1", got)
	}
}

func TestAdjustmentBudgetClonesVerifiedFinalSnapshot(t *testing.T) {
	t.Parallel()
	budget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	snapshot := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"root": {Rel: "root", CPUs: machine.NewCPUSet(0, 1)},
		},
	}

	budget.StartFromVerifiedFinalSnapshot(snapshot)
	snapshot.Entries["root"] = EntryState{Rel: "root", CPUs: machine.NewCPUSet(9)}
	got := budget.InitialSnapshot()
	if got == nil || !got.Entries["root"].CPUs.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("stored verified snapshot was aliased: %#v", got)
	}
	got.Entries["root"] = EntryState{Rel: "root", CPUs: machine.NewCPUSet(8)}
	again := budget.InitialSnapshot()
	if !again.Entries["root"].CPUs.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("returned verified snapshot was aliased: %#v", again)
	}
}
