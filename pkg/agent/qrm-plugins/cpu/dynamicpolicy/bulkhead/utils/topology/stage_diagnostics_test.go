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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The diagnostic fields must reflect actual execution, not authorize it.
func TestAdmissionStageDiagnostics(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"success", "deleted", "deadline_in_recovery", "preflight_drift", "compile_canceled"} {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, base := newDirectoryChurnReplay(t)
			initial := f.driver.snapshot()
			ctx := newTriggeredDeadlineContext(context.Background())
			b := NewAdjustmentBudget(ctx, ConvergenceBudget{DeadlineDuration: -1, MaxPlanOperations: 10000})
			f.round.adjustmentBudget = b
			f.round.recoveryContextFactory = deterministicRecoveryContext
			if scenario == "deleted" || scenario == "deadline_in_recovery" {
				d := &directoryChurnDriver{HierarchyDriver: f.driver, live: f.driver, rel: "kubepods/pod-09"}
				if scenario == "deadline_in_recovery" {
					d.expire = ctx.expire
				}
				f.round.driver = d
			}
			if scenario == "preflight_drift" {
				f.driver.bumpIdentity("reclaimed")
			}
			if scenario == "compile_canceled" {
				ctx.expire()
			}
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(ctx, base, res, nil, nil)
			d := res.stageDiagnostics
			require.False(t, d.started.IsZero())
			require.Equal(t, 1, d.compileCalls)
			require.GreaterOrEqual(t, d.compile, time.Duration(0))
			require.GreaterOrEqual(t, d.forward, time.Duration(0))
			require.GreaterOrEqual(t, d.rollback, time.Duration(0))
			require.GreaterOrEqual(t, d.rollback, d.rollbackVerification)
			wantStage := map[string]string{"success": "", "deleted": "forward", "deadline_in_recovery": "forward", "preflight_drift": "preflight", "compile_canceled": "compile"}[scenario]
			require.Equal(t, wantStage, d.failureStage)
			if scenario == "compile_canceled" {
				require.Error(t, err)
				require.Zero(t, d.preflightCalls)
				require.Zero(t, d.operations)
				require.Zero(t, res.forwardWriteAttempts)
				require.False(t, res.Published)
			} else {
				require.Equal(t, 1, d.preflightCalls)
				require.NotEmpty(t, d.traceID)
				require.Equal(t, base.ID, d.snapshotID)
				require.Equal(t, len(base.Entries), d.entries)
				require.Positive(t, d.operations)
				require.Positive(t, d.phases)
				switch scenario {
				case "success":
					require.NoError(t, err)
					require.True(t, res.Published)
					require.Positive(t, d.finalProof)
					require.Zero(t, d.rollbackCalls)
					require.Zero(t, res.rollbackWriteAttempts)
					require.Equal(t, 26, res.forwardWriteAttempts)
				case "preflight_drift":
					require.ErrorIs(t, err, ErrCoordinatorPlanStale)
					require.False(t, res.Published)
					require.Zero(t, d.rollbackCalls)
					require.Zero(t, res.forwardWriteAttempts)
					require.Zero(t, d.forward)
				default:
					require.ErrorIs(t, err, ErrCoordinatorPlanStale)
					require.False(t, res.Published)
					require.Equal(t, 1, d.rollbackCalls)
					require.Equal(t, 9, res.forwardWriteAttempts)
					require.Equal(t, 9, res.rollbackWriteAttempts)
					require.Zero(t, d.finalProof)
					for rel, node := range f.driver.snapshot() {
						require.Equal(t, initial[rel], node, "survivor restored %s", rel)
					}
				}
			}
			require.Zero(t, reservedWritesCount(b))
			t.Logf("DIAGNOSTIC scenario=%s trace=%s entries=%d operations=%d compile_calls=%d preflight_calls=%d rollback_calls=%d forward=%d rollback=%d error_type=%s", scenario, d.traceID, d.entries, d.operations, d.compileCalls, d.preflightCalls, d.rollbackCalls, res.forwardWriteAttempts, res.rollbackWriteAttempts, fmt.Sprintf("%T", err))
		})
	}
}
