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
)

func validatedPhaseTraceForTest(
	ctx context.Context,
	trace *CompiledPhaseTrace,
) (*validatedPhaseTrace, error) {
	frozen, err := freezePhaseTrace(ctx, trace)
	if err != nil {
		return nil, err
	}
	return &validatedPhaseTrace{frozen: frozen}, nil
}

func (b *BudgetTracker) ReservePhaseTrace(
	trace *CompiledPhaseTrace,
	maxRequiredWrites int,
) (*ExecutionReservationTicket, error) {
	validated, err := validatedPhaseTraceForTest(context.Background(), trace)
	if err != nil {
		return nil, err
	}
	return b.reserveValidatedPhaseTrace(context.Background(), validated, maxRequiredWrites)
}

func (w safeCPUSetWriter) preflightFrozenTrace(
	ctx context.Context,
	trace *CompiledPhaseTrace,
) error {
	_, err := w.preflightFrozenTraceOperations(ctx, trace)
	return err
}

func (w safeCPUSetWriter) preflightFrozenTraceOperations(
	ctx context.Context,
	trace *CompiledPhaseTrace,
) ([]frozenOperationPreflight, error) {
	validated, err := validatedPhaseTraceForTest(ctx, trace)
	if err != nil {
		return nil, fmt.Errorf("freeze phase trace before preflight: %w", err)
	}
	return w.preflightValidatedTraceOperations(ctx, validated)
}

func (r *coordinatorRound) executeFrozenTrace(
	ctx context.Context,
	trace *CompiledPhaseTrace,
	ticket *ExecutionReservationTicket,
	res *ConvergenceResult,
	finalizers ...frozenTraceFinalizer,
) (RoundOutcome, error) {
	validated, err := validatedPhaseTraceForTest(ctx, trace)
	if err != nil {
		return RoundOutcome{Status: RoundStatusBlocked}, err
	}
	return r.executeValidatedFrozenTrace(ctx, validated, ticket, res, nil, finalizers...)
}
