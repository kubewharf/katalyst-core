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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type diagnosticFailureDriver struct {
	HierarchyDriver
	live         *fakeHierarchyDriver
	finalFailed  bool
	failRecovery bool
}

func (d *diagnosticFailureDriver) ReadEntryWithActivity(ctx context.Context, rel string, id CgroupIdentity) (EntryState, error) {
	return d.live.ReadEntryWithActivity(ctx, rel, id)
}

func (d *diagnosticFailureDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	if !d.finalFailed && len(d.live.writes) == 26 && !isAdjustmentRecoveryContext(ctx) {
		d.finalFailed = true
		return nil, errors.New("diagnostic injected final proof failure")
	}
	return d.HierarchyDriver.ListChildren(ctx, rel)
}

func (d *diagnosticFailureDriver) Roots(ctx context.Context) ([]RootRef, error) {
	if !d.finalFailed && len(d.live.writes) == 26 {
		d.finalFailed = true
		return nil, errors.New("diagnostic injected final proof failure")
	}
	return d.HierarchyDriver.Roots(ctx)
}

func (d *diagnosticFailureDriver) WriteCPUs(ctx context.Context, rel string, id CgroupIdentity, cpus machine.CPUSet) error {
	if d.failRecovery && isAdjustmentRecoveryContext(ctx) {
		return errors.New("diagnostic injected recovery write failure")
	}
	return d.HierarchyDriver.WriteCPUs(ctx, rel, id, cpus)
}

func TestAdmissionStageDiagnosticsFinalAndRecoveryFailures(t *testing.T) {
	t.Parallel()

	for _, recoveryFailed := range []bool{false, true} {
		name := "final_proof"
		if recoveryFailed {
			name = "rollback"
		}
		recoveryFailed := recoveryFailed
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, base := newDirectoryChurnReplay(t)
			initial := f.driver.snapshot()
			b := NewAdjustmentBudget(context.Background(), ConvergenceBudget{DeadlineDuration: -1, MaxPlanOperations: 10000})
			f.round.adjustmentBudget = b
			f.round.recoveryContextFactory = deterministicRecoveryContext
			driver := &diagnosticFailureDriver{HierarchyDriver: f.driver, live: f.driver, failRecovery: recoveryFailed}
			f.round.driver = driver
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
			require.ErrorContains(t, err, "diagnostic injected final proof failure")
			require.True(t, driver.finalFailed)
			require.False(t, res.Published)
			require.Equal(t, name, res.stageDiagnostics.failureStage)
			require.Positive(t, res.stageDiagnostics.finalProof)
			require.Positive(t, res.stageDiagnostics.rollbackVerification)
			require.Equal(t, 1, res.stageDiagnostics.rollbackCalls)
			require.Equal(t, 26, res.forwardWriteAttempts)
			require.Equal(t, 26, res.rollbackWriteAttempts)
			require.Zero(t, reservedWritesCount(b))
			if !recoveryFailed {
				require.Equal(t, initial, f.driver.snapshot())
			} else {
				require.ErrorContains(t, err, "diagnostic injected recovery write failure")
				require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
				require.NotEqual(t, initial, f.driver.snapshot())
			}
		})
	}
}

func TestAdmissionStageDiagnosticsReservationFailure(t *testing.T) {
	t.Parallel()

	f, base := newDirectoryChurnReplay(t)
	b := NewAdjustmentBudget(context.Background(), ConvergenceBudget{DeadlineDuration: -1, MaxPlanOperations: 10000})
	b.maxWrites = 1
	f.round.adjustmentBudget = b
	res := &ConvergenceResult{}
	_, err := f.round.executeParentSafeAdmission(context.Background(), base, res, nil, nil)
	require.ErrorIs(t, err, ErrAdjustmentWriteBudgetExceeded)
	require.Equal(t, "reservation", res.stageDiagnostics.failureStage)
	require.Equal(t, 1, res.stageDiagnostics.compileCalls)
	require.Zero(t, res.stageDiagnostics.preflightCalls)
	require.Zero(t, res.forwardWriteAttempts)
	require.False(t, res.Published)
	require.Zero(t, reservedWritesCount(b))
}
