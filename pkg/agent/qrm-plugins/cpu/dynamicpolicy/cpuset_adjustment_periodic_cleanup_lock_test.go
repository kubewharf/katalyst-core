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

package dynamicpolicy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// periodicCleanupLockReproEnv selects the self-re-executed child role. Each role
// runs in its own process so the process-global SyncBulkhead healthz check is
// isolated between the success and error scenarios.
const (
	periodicCleanupLockReproEnv = "KATALYST_PERIODIC_CLEANUP_LOCK_REPRO"
	periodicCleanupRoleSuccess  = "success"
	periodicCleanupRoleError    = "error"
	periodicCleanupRoleFatal    = "fatal"
	periodicRoleInFlight        = "in-flight"
	periodicRoleConverged       = "converged"
)

var errPeriodicCleanupSynthetic = errors.New("periodic cleanup synthetic failure")

// newPeriodicCleanupPolicy builds a minimal DynamicPolicy whose authoritative
// bulkhead gate reports a pending disabled reset (gate=true) and whose canonical
// adjustment map is non-empty, so the real periodic entry reaches the execution
// lease instead of returning on the empty-handler early exit.
func newPeriodicCleanupPolicy(t *testing.T, fail bool, calls *int) *DynamicPolicy {
	t.Helper()
	dyn := dynamicconfig.NewDynamicAgentConfiguration()
	manager, err := bulkhead.NewManager(nil)
	require.NoError(t, err)
	require.True(t, manager.HasPendingDisabledReset(nil),
		"fixture must present a pending disabled reset so the cleanup gate is open")

	return &DynamicPolicy{
		config:   configComponent{dynamicConfig: dyn},
		bulkhead: bulkheadComponent{bulkheadManager: manager},
		adjustment: adjustmentComponent{
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"periodic-cleanup-recorder": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					*calls++
					require.Equal(t, cpusetutil.CPUSetAdjustmentModePeriodic, in.Mode)
					if fail {
						return errPeriodicCleanupSynthetic
					}
					return nil
				},
			},
		},
	}
}

// childSuccess runs the success scenario inside an isolated child process: the
// cleanup round must run the handler once, return the policy lock to the caller,
// drain the execution lease, and leave SyncBulkhead ready.
func childPeriodicCleanupSuccess(t *testing.T) {
	t.Helper()
	general.RegisterReportCheck(cpuconsts.SyncBulkhead, 0, general.HealthzCheckStateReady)
	t.Cleanup(func() { general.UnregisterHealthCheck(cpuconsts.SyncBulkhead) })

	var calls int
	p := newPeriodicCleanupPolicy(t, false, &calls)
	p.runBulkheadPeriodicalHandlers(nil, nil, p.config.dynamicConfig, nil, nil)
	require.Equal(t, 1, calls, "the non-empty canonical handler must run on the cleanup round")

	// The boundary must have released the policy lock before returning; a
	// still-held lock would block this acquisition past the deadline.
	acquired := make(chan struct{})
	go func() {
		p.Lock()
		close(acquired)
		p.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("policy lock was not returned after the periodic cleanup round")
	}

	// The execution lease token must be drained after a successful round.
	require.NotNil(t, p.adjustment.cpuSetAdjustmentExecution)
	require.Equal(t, 0, len(p.adjustment.cpuSetAdjustmentExecution),
		"execution lease must be released after the cleanup round")

	res := general.GetRegisterReadinessCheckResult()
	ready, ok := res[general.HealthzCheckName(cpuconsts.SyncBulkhead)]
	require.True(t, ok)
	require.True(t, ready.Ready, "successful cleanup must leave SyncBulkhead ready")
}

// childPeriodicCleanupError runs the failing-cleanup scenario inside an isolated
// child process: the failing handler runs once, the round returns before the
// ordinary periodical handlers, and SyncBulkhead is marked not ready with the
// cleanup error.
func childPeriodicCleanupError(t *testing.T) {
	t.Helper()
	general.RegisterReportCheck(cpuconsts.SyncBulkhead, 0, general.HealthzCheckStateReady)
	t.Cleanup(func() { general.UnregisterHealthCheck(cpuconsts.SyncBulkhead) })

	var calls int
	p := newPeriodicCleanupPolicy(t, true, &calls)
	p.runBulkheadPeriodicalHandlers(nil, nil, p.config.dynamicConfig, nil, nil)
	require.Equal(t, 1, calls, "the failing cleanup handler must still run once")

	// A failed cleanup returns before the ordinary periodical handlers; the
	// SyncBulkhead check is marked not ready with the cleanup error.
	res := general.GetRegisterReadinessCheckResult()
	state, ok := res[general.HealthzCheckName(cpuconsts.SyncBulkhead)]
	require.True(t, ok)
	require.False(t, state.Ready, "failed cleanup must suppress the ordinary periodical and mark not ready")
	require.Contains(t, state.Message, errPeriodicCleanupSynthetic.Error())
}

// childPeriodicCleanupFatal drives the real periodic entry once. It returns on
// the patched build; on the unlocked base it fatals inside the execution lease
// before returning, which the parent captures as the base red evidence.
func childPeriodicCleanupFatal(t *testing.T) {
	t.Helper()
	var calls int
	p := newPeriodicCleanupPolicy(t, false, &calls)
	p.runBulkheadPeriodicalHandlers(nil, nil, p.config.dynamicConfig, nil, nil)
	require.Equal(t, 1, calls)
}

// childPeriodicInFlight drives the real periodic entry while a newly published
// advisor post-commit target is still being applied. This normal transient state
// must not make SyncBulkhead not ready; actual reconcile/apply failures remain
// responsible for reporting their own health errors.
func childPeriodicInFlight(t *testing.T) {
	t.Helper()
	general.RegisterReportCheck(cpuconsts.SyncBulkhead, 0, general.HealthzCheckStateReady)
	t.Cleanup(func() { general.UnregisterHealthCheck(cpuconsts.SyncBulkhead) })

	dyn := dynamicconfig.NewDynamicAgentConfiguration()
	manager, err := bulkhead.NewManager(nil)
	require.NoError(t, err)
	p := &DynamicPolicy{
		config:   configComponent{dynamicConfig: dyn},
		bulkhead: bulkheadComponent{bulkheadManager: manager},
		adjustment: adjustmentComponent{
			advisorPostCommitTarget: &advisorPostCommitTarget{revision: 7},
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"periodic-in-flight-recorder": func(_ context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					return nil
				},
			},
		},
	}

	p.runBulkheadPeriodicalHandlers(nil, nil, p.config.dynamicConfig, nil, nil)

	res := general.GetRegisterReadinessCheckResult()
	state, ok := res[general.HealthzCheckName(cpuconsts.SyncBulkhead)]
	require.True(t, ok)
	require.True(t, state.Ready, "a fresh in-flight target must not make SyncBulkhead not ready")
}

// childPeriodicConverged drives the real periodic entry with no pending advisor
// post-commit target (recovery fully completed). The manager may run and
// SyncBulkhead must reach Ready.
func childPeriodicConverged(t *testing.T) {
	t.Helper()
	general.RegisterReportCheck(cpuconsts.SyncBulkhead, 0, general.HealthzCheckStateReady)
	t.Cleanup(func() { general.UnregisterHealthCheck(cpuconsts.SyncBulkhead) })

	dyn := dynamicconfig.NewDynamicAgentConfiguration()
	manager, err := bulkhead.NewManager(nil)
	require.NoError(t, err)
	p := &DynamicPolicy{
		config:   configComponent{dynamicConfig: dyn},
		bulkhead: bulkheadComponent{bulkheadManager: manager},
		adjustment: adjustmentComponent{
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"periodic-gate-recorder": func(_ context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					return nil
				},
			},
		},
	}

	p.runBulkheadPeriodicalHandlers(nil, nil, p.config.dynamicConfig, nil, nil)

	res := general.GetRegisterReadinessCheckResult()
	state, ok := res[general.HealthzCheckName(cpuconsts.SyncBulkhead)]
	require.True(t, ok)
	require.True(t, state.Ready, "converged recovery must leave SyncBulkhead ready")
}

// runPeriodicCleanupSubprocess re-executes the test binary in an isolated child
// process for the requested role and fails the parent on a non-zero exit.
func runPeriodicCleanupSubprocess(t *testing.T, role string) {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestBulkheadPeriodicalCleanupAcquiresPolicyLockAndReleasesLease$",
		"-test.v")
	cmd.Env = append(os.Environ(), periodicCleanupLockReproEnv+"="+role)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Run(), "isolated %s scenario must exit cleanly; output:\n%s", role, out.String())
	if role == periodicCleanupRoleFatal {
		require.NotContains(t, out.String(), "Unlock of unlocked RWMutex")
	}
}

// TestBulkheadPeriodicalCleanupAcquiresPolicyLockAndReleasesLease exercises the
// real periodic entry with an open disabled-cleanup gate and a non-empty handler
// map. Each scenario runs in its own child process to isolate the process-global
// healthz check; the cleanup boundary must hold the policy lock while acquiring
// the execution lease, return the lock after the round, drain the lease, and
// suppress the ordinary periodical when cleanup fails.
func TestBulkheadPeriodicalCleanupAcquiresPolicyLockAndReleasesLease(t *testing.T) {
	t.Parallel()

	switch os.Getenv(periodicCleanupLockReproEnv) {
	case periodicCleanupRoleSuccess:
		childPeriodicCleanupSuccess(t)
		return
	case periodicCleanupRoleError:
		childPeriodicCleanupError(t)
		return
	case periodicCleanupRoleFatal:
		childPeriodicCleanupFatal(t)
		return
	case periodicRoleInFlight:
		childPeriodicInFlight(t)
		return
	case periodicRoleConverged:
		childPeriodicConverged(t)
		return
	}

	t.Run("success returns lock and drains lease", func(t *testing.T) {
		t.Parallel()
		runPeriodicCleanupSubprocess(t, periodicCleanupRoleSuccess)
	})

	t.Run("cleanup error suppresses ordinary periodical", func(t *testing.T) {
		t.Parallel()
		runPeriodicCleanupSubprocess(t, periodicCleanupRoleError)
	})

	t.Run("patched subprocess returns cleanly without unlocked-RWMutex fatal", func(t *testing.T) {
		t.Parallel()
		runPeriodicCleanupSubprocess(t, periodicCleanupRoleFatal)
	})

	t.Run("fresh in-flight target leaves SyncBulkhead ready", func(t *testing.T) {
		t.Parallel()
		runPeriodicCleanupSubprocess(t, periodicRoleInFlight)
	})

	t.Run("converged recovery leaves SyncBulkhead ready", func(t *testing.T) {
		t.Parallel()
		runPeriodicCleanupSubprocess(t, periodicRoleConverged)
	})
}
