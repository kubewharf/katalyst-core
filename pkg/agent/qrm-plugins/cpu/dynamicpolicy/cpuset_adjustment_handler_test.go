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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"
	bulkheadtopology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	podmeta "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cacheSyncRegistrarPodFetcher struct {
	*podmeta.PodFetcherStub
	events       chan podmeta.KubeletPodCacheSyncEvent
	unregistered chan struct{}
}

type cpusetOverrideCommitGuardState struct {
	state.State
	unconditionalCommitCalls int
	conditionalCommitCalls   int
	conditionalRevision      uint64
}

func TestCPUSetAdjustmentStateSnapshotMatchesSameRevision(t *testing.T) {
	t.Parallel()
	source := state.NewCPUPluginState(nil)
	snapshot := &cpuSetAdjustmentStateSnapshot{revision: source.GetRevision()}

	require.True(t, snapshot.matches(source))
}

func TestCPUSetAdjustmentStateSnapshotRejectsChangedRevision(t *testing.T) {
	t.Parallel()
	source := state.NewCPUPluginState(nil)
	snapshot := newCPUSetAdjustmentStateSnapshot(source)
	require.True(t, snapshot.matches(source))

	require.NoError(t, source.SetPodEntries(state.PodEntries{}))

	require.False(t, snapshot.matches(source))
}

func TestCPUSetAdjustmentStateSnapshotAdvisorSnapshotIsIndependent(t *testing.T) {
	t.Parallel()
	allocation := &state.AllocationInfo{AllocationResult: machine.NewCPUSet(0)}
	entries := state.PodEntries{"pod": state.ContainerEntries{"main": allocation}}
	snapshot := &cpuSetAdjustmentStateSnapshot{
		podEntries: entries,
		machineState: state.NUMANodeMap{0: &state.NUMANodeState{
			DefaultCPUSet: machine.NewCPUSet(0),
			PodEntries:    entries.Clone(),
		}},
		revision: 42,
	}
	var reader state.ReadonlyState = snapshot
	copiedEntries, copiedMachine, revision := reader.GetAdvisorStateSnapshot()
	require.Equal(t, uint64(42), revision)
	copiedEntries["pod"]["main"].AllocationResult = machine.NewCPUSet(1)
	copiedMachine[0].PodEntries["pod"]["main"].AllocationResult = machine.NewCPUSet(2)
	copiedMachine[0].DefaultCPUSet = machine.NewCPUSet(3)
	require.True(t, snapshot.podEntries["pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(0)))
	require.True(t, snapshot.machineState[0].PodEntries["pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(0)))
	require.True(t, snapshot.machineState[0].DefaultCPUSet.Equals(machine.NewCPUSet(0)))
}

func TestCPUSetAdjustmentExecutionLeaseStaleContextAfterRelease(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"noop": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			return nil
		},
	}}}

	p.Lock()
	staleLease, err := p.acquireCPUSetAdjustmentExecutionLocked(context.Background())
	require.NoError(t, err)
	staleCtx := context.WithValue(
		context.Background(), cpuSetAdjustmentExecutionLeaseContextKey{}, staleLease)
	staleLease.release()
	activeLease, err := p.acquireCPUSetAdjustmentExecutionLocked(context.Background())
	require.NoError(t, err)
	p.Unlock()

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		p.Lock()
		close(started)
		done <- p.runCPUSetAdjustmentHandlers(staleCtx)
		p.Unlock()
	}()
	<-started

	select {
	case err := <-done:
		activeLease.release()
		t.Fatalf("stale context bypassed the active execution lease: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	activeLease.release()
	require.NoError(t, <-done)
	require.Nil(t, cpuSetAdjustmentExecutionLeaseFromContext(staleCtx, p))

	p.Lock()
	concurrentLease, err := p.acquireCPUSetAdjustmentExecutionLocked(context.Background())
	require.NoError(t, err)
	p.Unlock()
	var releases sync.WaitGroup
	releases.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer releases.Done()
			concurrentLease.release()
		}()
	}
	released := make(chan struct{})
	go func() {
		releases.Wait()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("concurrent execution lease release must be idempotent")
	}
}

func (s *cpusetOverrideCommitGuardState) CommitAdvisorState(
	state.PodEntries,
	state.NUMANodeMap,
	bool,
	bool,
	bool,
	...*state.WritePermit,
) error {
	s.unconditionalCommitCalls++
	return fmt.Errorf("cpuset adjustment override must use CommitAdvisorStateIfRevision")
}

func (s *cpusetOverrideCommitGuardState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap bool,
	disableDedicatedOverlap bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.conditionalCommitCalls++
	s.conditionalRevision = expectedRevision
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, podEntries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (f *cacheSyncRegistrarPodFetcher) RegisterKubeletPodCacheSyncListener(string) (
	<-chan podmeta.KubeletPodCacheSyncEvent, func(),
) {
	return f.events, func() {
		select {
		case <-f.unregistered:
		default:
			close(f.unregistered)
		}
	}
}

func TestCPUSetAdjustmentHandlerTimeoutCoversTopologyConvergenceBudget(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	if cpuSetAdjustmentHandlerTimeout(conf) <= bulkheadconfig.DefaultTopologyConvergenceDeadline {
		t.Fatalf("outer cpuset adjustment timeout %s must exceed topology convergence budget %s",
			cpuSetAdjustmentHandlerTimeout(conf), bulkheadconfig.DefaultTopologyConvergenceDeadline)
	}
}

func TestCPUSetAdjustmentHandlerTimeoutDerivesFromConfiguredTopologyDeadline(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	conf.CPUQRMPluginConfig.BulkheadConfiguration.TopologyConvergenceBudget.DeadlineDuration = 750 * time.Millisecond
	got := cpuSetAdjustmentHandlerTimeout(conf)
	if got <= 750*time.Millisecond || got >= 15*time.Second {
		t.Fatalf("derived outer timeout = %s, want bounded margin above configured 750ms", got)
	}
}

type frozenInitialSnapshotDriftTestError struct{}

func (*frozenInitialSnapshotDriftTestError) Error() string {
	return "frozen trace initial snapshot drift"
}

func (*frozenInitialSnapshotDriftTestError) Unwrap() error {
	return bulkheadtopology.ErrCoordinatorPlanStale
}

func TestAdmissionPropagatesFrozenInitialSnapshotDriftWithoutRetry(t *testing.T) {
	t.Parallel()

	firstCalls := 0
	secondCalls := 0
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"a-stale-once": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			firstCalls++
			if firstCalls == 1 {
				return &frozenInitialSnapshotDriftTestError{}
			}
			return nil
		},
		"b-success": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			secondCalls++
			return nil
		},
	}}}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()

	require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
	require.Equal(t, 1, firstCalls)
	require.Zero(t, secondCalls)
}

func TestAdmissionContinuousFrozenInitialSnapshotDriftCallsPluginOnce(t *testing.T) {
	t.Parallel()
	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(cpuTopology, t.TempDir())
	require.NoError(t, err)
	calls := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"always-stale": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			calls++
			return &frozenInitialSnapshotDriftTestError{}
		},
	}
	beforeRevision := p.state.GetRevision()
	beforeEntries := p.state.GetPodEntries()
	beforeMachineState := p.state.GetMachineState()
	target := &advisorPostCommitTarget{revision: beforeRevision}
	p.adjustment.advisorPostCommitTarget = target
	stagingPath := p.advisorPostCommitStagingPath()
	activePath := p.advisorPostCommitCheckpointPath()
	require.NoError(t, os.WriteFile(stagingPath, []byte("staging-sentinel"), 0o600))
	require.NoError(t, os.WriteFile(activePath, []byte("active-sentinel"), 0o600))

	p.Lock()
	err = p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()

	require.Error(t, err)
	require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
	require.Equal(t, 1, calls)
	require.ErrorContains(t, err, "frozen trace initial snapshot drift")
	require.Equal(t, beforeRevision, p.state.GetRevision())
	require.Equal(t, beforeEntries, p.state.GetPodEntries())
	require.Equal(t, beforeMachineState, p.state.GetMachineState())
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	staging, readErr := os.ReadFile(stagingPath)
	require.NoError(t, readErr)
	require.Equal(t, []byte("staging-sentinel"), staging)
	active, readErr := os.ReadFile(activePath)
	require.NoError(t, readErr)
	require.Equal(t, []byte("active-sentinel"), active)
}

// TestDeferredHoldCommitsNoBusinessState proves the dynamicpolicy adjustment
// layer treats the bulkhead topology Deferred (a deliberate zero-write hold) as
// a non-commit: PodEntries, the reclaim AllocationResult (the desired reclaim
// view), MachineState and the state revision are byte-identical before/after, and
// the round surfaces the typed ErrTopologyDeferred rather than persisting or
// scheduling any business mutation.
func TestDeferredHoldCommitsNoBusinessState(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))

	// Snapshot every business object the zero-write hold must leave untouched.
	beforeRevision := p.state.GetRevision()
	beforeEntries := p.state.GetPodEntries().Clone()
	beforeMachineState := p.state.GetMachineState().Clone()
	beforeReclaimInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, beforeReclaimInfo)
	beforeReclaimResult := beforeReclaimInfo.AllocationResult.Clone()

	// The bulkhead topology owner reports a deliberate zero-write hold. A hold
	// must NOT populate a commit override: there is no desired view to commit.
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"bulkhead": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			require.Empty(t, in.CommitOverride.Source, "a hold must not emit a desired-view commit override")
			return fmt.Errorf("bulkhead topology %w: %s", bulkhead.ErrTopologyDeferred, "full_pending")
		},
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()
	require.ErrorIs(t, err, bulkhead.ErrTopologyDeferred)

	// Zero business commit: revision, PodEntries, MachineState and the reclaim
	// AllocationResult (desired reclaim view) must all be unchanged.
	require.Equal(t, beforeRevision, p.state.GetRevision(), "state revision drifted on a zero-write hold")
	require.Equal(t, beforeEntries, p.state.GetPodEntries(), "PodEntries drifted on a zero-write hold")
	require.Equal(t, beforeMachineState, p.state.GetMachineState(), "MachineState drifted on a zero-write hold")
	afterReclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, afterReclaim)
	require.True(t, beforeReclaimResult.Equals(afterReclaim.AllocationResult),
		"desired reclaim AllocationResult drifted: %s -> %s", beforeReclaimResult, afterReclaim.AllocationResult)
}

type frozenFinalSnapshotDriftUnverifiedTestError struct{}

func (*frozenFinalSnapshotDriftUnverifiedTestError) Error() string {
	return "frozen trace final snapshot drift"
}

func (*frozenFinalSnapshotDriftUnverifiedTestError) FrozenFinalSnapshotDrift() bool { return true }

func TestAdmissionPropagatesFinalSnapshotDriftAfterVerifiedRollbackWithoutRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"final-drift-once": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			calls++
			if calls == 1 {
				return errors.New("frozen trace final snapshot drift after verified rollback")
			}
			return nil
		},
	}}}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()

	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestAdmissionDoesNotRetryUnverifiedFinalSnapshotDrift(t *testing.T) {
	t.Parallel()

	calls := 0
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"unsafe-final-drift": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			calls++
			return &frozenFinalSnapshotDriftUnverifiedTestError{}
		},
	}}}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()

	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestRunCPUSetAdjustmentHandlersPropagatesMode(t *testing.T) {
	t.Parallel()

	got := make(chan cpusetutil.CPUSetAdjustmentMode, 1)
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"mode": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			got <- in.Mode
			return nil
		},
	}}}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()
	if err != nil {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v", err)
	}
	if mode := <-got; mode != cpusetutil.CPUSetAdjustmentModeAdmission {
		t.Fatalf("handler mode = %q, want admission", mode)
	}
}

func TestDeferredFullRetryCoalescesQueuedRequestsIntoTrailingRetry(t *testing.T) {
	t.Parallel()

	retried := make(chan cpusetutil.CPUSetAdjustmentMode, 2)
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == cpusetutil.CPUSetAdjustmentModeAdmission {
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
				return nil
			}
			retried <- in.Mode
			return nil
		},
	}}}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()
	if err != nil {
		t.Fatalf("admission adjustment error = %v", err)
	}
	select {
	case mode := <-retried:
		if mode != cpusetutil.CPUSetAdjustmentModeRetry {
			t.Fatalf("async mode = %q, want retry", mode)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred full retry was not executed")
	}
	select {
	case mode := <-retried:
		if mode != cpusetutil.CPUSetAdjustmentModeRetry {
			t.Fatalf("trailing mode = %q, want retry", mode)
		}
	case <-time.After(time.Second):
		t.Fatal("queued deferred requests were not coalesced into a trailing retry")
	}
	select {
	case mode := <-retried:
		t.Fatalf("queued deferred requests produced more than one trailing retry, extra mode=%q", mode)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDeferredFullRetryRetriesFailureWithBackoff(t *testing.T) {
	t.Parallel()

	attempts := make(chan time.Time, 2)
	attemptCount := 0
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode != cpusetutil.CPUSetAdjustmentModeRetry {
				return nil
			}
			attemptCount++
			attempts <- time.Now()
			if attemptCount == 1 {
				return errors.New("transient retry failure")
			}
			return nil
		},
	}}}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	first := <-attempts
	select {
	case second := <-attempts:
		if elapsed := second.Sub(first); elapsed < 10*time.Millisecond {
			t.Fatalf("retry failure was retried without backoff: elapsed=%s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred latest-state retry was lost after a transient failure")
	}
}

func TestDeferredFullRetryCountsTrailingRoundsTowardAttemptBudget(t *testing.T) {
	t.Parallel()

	attempts := make(chan time.Time, cpuSetAdjustmentRetryMaxAttempts+1)
	var attemptCount int32
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode != cpusetutil.CPUSetAdjustmentModeRetry {
				return nil
			}
			attempt := atomic.AddInt32(&attemptCount, 1)
			attempts <- time.Now()
			if attempt <= cpuSetAdjustmentRetryMaxAttempts {
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
			}
			return nil
		},
	}}}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	deadline := time.Now().Add(time.Second)
	for {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		queued := p.adjustment.cpuSetAdjustmentRetryQueued
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		if !queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("self-scheduled trailing retries did not stop within the bounded attempt window")
		}
		time.Sleep(time.Millisecond)
	}

	require.Equal(t, int32(cpuSetAdjustmentRetryMaxAttempts), atomic.LoadInt32(&attemptCount),
		"every worker round, including successful trailing rounds, must consume the shared attempt budget")
	var previous time.Time
	for i := 0; i < cpuSetAdjustmentRetryMaxAttempts; i++ {
		current := <-attempts
		if !previous.IsZero() {
			require.GreaterOrEqual(t, current.Sub(previous), cpuSetAdjustmentRetryInitialBackoff,
				"trailing retry %d ran without backoff", i+1)
		}
		previous = current
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.False(t, p.adjustment.cpuSetAdjustmentRetryQueued)
	require.False(t, p.adjustment.cpuSetAdjustmentRetryAgain)
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonDeferredLeaf)
}

func TestDeferredFullRetrySuccessfulTrailingRoundClearsTrimmedRequest(t *testing.T) {
	t.Parallel()

	var attemptCount int32
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode != cpusetutil.CPUSetAdjustmentModeRetry {
				return nil
			}
			if atomic.AddInt32(&attemptCount, 1) == 1 {
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
				in.ScheduleFullRetry(cpusetutil.RetryReasonDeferredLeaf)
			}
			return nil
		},
	}}}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	deadline := time.Now().Add(time.Second)
	for {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		queued := p.adjustment.cpuSetAdjustmentRetryQueued
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		if !queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successful trailing retry did not finish")
		}
		time.Sleep(time.Millisecond)
	}

	require.Equal(t, int32(2), atomic.LoadInt32(&attemptCount))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.False(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Nil(t, p.adjustment.cpuSetAdjustmentRetryReasons)
	require.False(t, p.adjustment.cpuSetAdjustmentRetryAgain)
}

func TestDeferredFullRetryExhaustionStaysDirtyUntilPeriodicLatestStateReconcile(t *testing.T) {
	t.Parallel()

	attempts := make(chan struct{}, 8)
	recovered := false
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Mode == cpusetutil.CPUSetAdjustmentModeRetry {
				attempts <- struct{}{}
			}
			if recovered {
				return nil
			}
			return errors.New("persistent retry failure")
		},
	}}}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	deadline := time.Now().Add(time.Second)
	for {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		queued := p.adjustment.cpuSetAdjustmentRetryQueued
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		if !queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("persistent retry failure did not stop within the bounded retry window")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(attempts); got != 4 {
		t.Fatalf("retry attempts = %d, want bounded 4 attempts", got)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	dirtyAfterExhaustion := p.adjustment.cpuSetAdjustmentRetryDirty
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !dirtyAfterExhaustion {
		t.Fatal("retry exhaustion cleared dirty latest-state reconciliation")
	}

	recovered = true
	p.runBulkheadPeriodicalHandlers(nil, nil, nil, nil, nil)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	dirtyAfterPeriodic := p.adjustment.cpuSetAdjustmentRetryDirty
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if dirtyAfterPeriodic {
		t.Fatal("successful periodic latest-state reconciliation did not clear dirty state")
	}
}

func TestDirtyCPUSetAdjustmentReconcileReturnsFailure(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{adjustment: adjustmentComponent{
		cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
			"persistent-failure": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
				return fmt.Errorf("persistent failure")
			},
		},
		cpuSetAdjustmentRetryDirty: true,
	}}

	if err := p.reconcileDirtyCPUSetAdjustment(); err == nil {
		t.Fatal("reconcileDirtyCPUSetAdjustment() error = nil, want persistent failure")
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !p.adjustment.cpuSetAdjustmentRetryDirty {
		t.Fatal("failed periodic reconciliation cleared dirty state")
	}
}

func TestPeriodicAdjustmentPreservesRetryScheduledDuringRound(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{}}}
	p.adjustment.cpuSetAdjustmentHandlers["schedule-during-round"] = func(
		context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx,
	) error {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		p.adjustment.cpuSetAdjustmentRetryDirty = true
		p.adjustment.cpuSetAdjustmentRetryQueued = true
		p.adjustment.cpuSetAdjustmentRetryReasons = map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
			cpusetutil.RetryReasonDeferredLeaf: {},
		}
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		return nil
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
	p.Unlock()
	if err != nil {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v", err)
	}

	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !p.adjustment.cpuSetAdjustmentRetryDirty {
		t.Fatal("successful periodic round cleared a retry scheduled during handler execution")
	}
	if _, ok := p.adjustment.cpuSetAdjustmentRetryReasons[cpusetutil.RetryReasonDeferredLeaf]; !ok {
		t.Fatal("successful periodic round dropped deferred-leaf retry reason scheduled during handler execution")
	}
}

func TestCPUSetAdjustmentCommitsTopologyReclaimOverride(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if handlerCtx.CommitOverride == nil {
				t.Fatal("CPUSet adjustment runner did not provide a commit override")
			}
			handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(2, 3)
			handlerCtx.CommitOverride.Source = "cpuset_topology"
			return nil
		},
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
	p.Unlock()
	require.NoError(t, err)

	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 3)),
		"reclaim allocation=%s, want topology verified override 2-3", reclaim.AllocationResult)
}

// TestCPUSetAdjustmentRejectsRampUpSharedReclaimOverlapWhenHardPartitionEnabled
// pins the M3 fix on the asynchronous cpuset adjustment commit path: when an
// adjustment override rewrites the reclaim pool onto a ramp-up shared
// allocation, the stacked mutual-exclusion validator must reject the commit
// while the ramp-up reclaim hard partition switch is on — and must not reject
// it when the switch is off (the two switches stay orthogonal on the async
// path as well).
func TestCPUSetAdjustmentRejectsRampUpSharedReclaimOverlapWhenHardPartitionEnabled(t *testing.T) {
	t.Parallel()

	newTest := func(t *testing.T, hardPartitionEnabled bool) *DynamicPolicy {
		t.Helper()

		p, cleanup := newReclaimReuseTestPolicy(t)
		t.Cleanup(cleanup)

		dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
		dynamicConf.EnableReclaim = true
		dynamicConf.EnableRampUpReclaimHardPartition = hardPartitionEnabled

		// setReclaimPoolCPUSet replaces the entire state, so it must run
		// before we add the ramp-up pod on top.
		setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))
		entries := p.state.GetPodEntries().Clone()
		entries["ramp-up-shared-pod"] = state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-shared-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		}
		p.state.SetPodEntries(entries, false)

		p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
			"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
				if handlerCtx.CommitOverride == nil {
					t.Fatal("CPUSet adjustment runner did not provide a commit override")
				}
				// the override moves the reclaim pool straight onto the
				// ramp-up shared allocation {2,3}
				handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(2, 3)
				handlerCtx.CommitOverride.Source = "cpuset_topology"
				return nil
			},
		}
		return p
	}

	t.Run("hard partition enabled rejects the overlapping override", func(t *testing.T) {
		t.Parallel()

		p := newTest(t, true)
		p.Lock()
		err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
		p.Unlock()
		// The override reclaim {2,3} completely covers the ramp-up business {2,3},
		// so the gated rematerialize step fails closed before the precommit
		// validator (it never becomes a membership-only commit). Either fail-closed
		// stage is acceptable, but the override must NOT land.
		require.Error(t, err, "an overlapping override must be rejected while hard partition is on")
		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(0, 1)),
			"reclaim pool=%s must remain the pre-override value {0,1}; the overlapping override was committed",
			reclaim.AllocationResult.String())
	})

	t.Run("hard partition disabled commits the same override", func(t *testing.T) {
		t.Parallel()

		p := newTest(t, false)
		p.Lock()
		err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
		p.Unlock()
		require.NoError(t, err)

		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 3)),
			"reclaim allocation=%s, want override 2-3", reclaim.AllocationResult.String())
	})
}

// TestCPUSetAdjustmentRematerializesRampUpSharedAgainstShiftedOverrideReclaim pins
// the closed root cause on the asynchronous cpuset adjustment commit path. A
// ramp-up non-SNB shared business entry was rematerialized (on the synchronous
// advisor apply path) against reclaim layout A. The background adjustment round
// then stamps a bulkhead-verified override reclaim B that lands on top of that
// business. Before the fix the async path wrote B into the reclaim pool and fed
// the unchanged business-vs-B pair straight to the precommit mutual-exclusion
// validator, which rejected the commit; because the rejected clone is discarded,
// the next round rebuilt the identical B and looped with no progress (fixed point,
// e.g. during removals). The fix rematerializes ramp-up business against the final
// (overridden) reclaim pool inside the async override transaction, mirroring the
// synchronous advisor apply path. It must NOT become a membership-only bypass: an
// override reclaim that fully covers business stays fail-closed.
func TestCPUSetAdjustmentRematerializesRampUpSharedAgainstShiftedOverrideReclaim(t *testing.T) {
	t.Parallel()

	// The fixture is 96 cpus / 2 NUMA / 2 SMT; a physical core c is {c, c+48}.
	// A (initial reclaim) = core0 {0,48}; business = cores 2,3,4 {2,50,3,51,4,52},
	// disjoint from A; override B = cores 2,3 {2,50,3,51}, which overlaps business.
	const (
		reclaimA0, reclaimA1 = 0, 48
		bus0, bus1           = 2, 50
		bus2, bus3           = 3, 51
		bus4, bus5           = 4, 52
		ov0, ov1             = 2, 50
		ov2, ov3             = 3, 51
	)

	newTest := func(t *testing.T, hardPartition bool, businessCPUs, overrideCPUs []int) (*DynamicPolicy, machine.CPUSet) {
		t.Helper()
		p, cleanup := newReclaimReuseTestPolicy(t)
		t.Cleanup(cleanup)

		dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()
		dynamicConf.EnableReclaim = true
		dynamicConf.EnableRampUpReclaimHardPartition = hardPartition

		// setReclaimPoolCPUSet replaces the whole state, so install business on top.
		setReclaimPoolCPUSet(t, p, machine.NewCPUSet(reclaimA0, reclaimA1))
		entries := p.state.GetPodEntries().Clone()
		entries["ramp-up-shared-pod"] = state.ContainerEntries{
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-shared-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(businessCPUs...),
			},
		}
		p.state.SetPodEntries(entries, false)

		override := machine.NewCPUSet(overrideCPUs...)
		p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
			"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
				if handlerCtx.CommitOverride == nil {
					t.Fatal("CPUSet adjustment runner did not provide a commit override")
				}
				handlerCtx.CommitOverride.ReclaimEffective = override.Clone()
				handlerCtx.CommitOverride.Source = "cpuset_topology"
				return nil
			},
		}
		return p, override
	}

	t.Run("partial overlap self-heals against shifted override reclaim", func(t *testing.T) {
		t.Parallel()
		p, override := newTest(t, true,
			[]int{bus0, bus1, bus2, bus3, bus4, bus5},
			[]int{ov0, ov1, ov2, ov3})

		p.Lock()
		err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
		p.Unlock()
		require.NoError(t, err, "rematerialize must bring ramp-up business off the override reclaim and commit")

		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Equals(override),
			"reclaim pool=%s, want stamped override %s", reclaim.AllocationResult, override)

		bus := p.state.GetAllocationInfo("ramp-up-shared-pod", "main")
		require.NotNil(t, bus)
		require.True(t, bus.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
			"business %s must be disjoint from committed reclaim %s", bus.AllocationResult, reclaim.AllocationResult)
	})

	t.Run("total cover stays fail-closed", func(t *testing.T) {
		t.Parallel()
		// override reclaim completely covers the business allocation.
		p, _ := newTest(t, true,
			[]int{ov0, ov1, ov2, ov3},
			[]int{ov0, ov1, ov2, ov3})

		p.Lock()
		err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
		p.Unlock()
		require.Error(t, err, "a reclaim pool that fully covers business must fail closed")
	})

	t.Run("hard partition off commits unchanged", func(t *testing.T) {
		t.Parallel()
		p, override := newTest(t, false,
			[]int{bus0, bus1, bus2, bus3, bus4, bus5},
			[]int{ov0, ov1, ov2, ov3})

		p.Lock()
		err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
		p.Unlock()
		require.NoError(t, err)

		reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
		require.NotNil(t, reclaim)
		require.True(t, reclaim.AllocationResult.Equals(override),
			"reclaim pool=%s, want override %s", reclaim.AllocationResult, override)
	})
}

func TestCPUSetAdjustmentAlignsAdmissionReclaimOverrideToWholeCores(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1, 48, 49))
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	var handlerCalls int32
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if atomic.AddInt32(&handlerCalls, 1) == 1 {
				handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(1, 48, 49)
			} else {
				handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(1, 49)
			}
			handlerCtx.CommitOverride.Source = "cpuset_topology"
			return nil
		},
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeAdmission)
	p.Unlock()
	require.NoError(t, err)

	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(1, 49)),
		"admission override must retain complete physical cores only, got %s", reclaim.AllocationResult)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&handlerCalls) >= 2
	}, time.Second, 10*time.Millisecond,
		"trimming an applied override must schedule a latest-state convergence pass")
}

func TestCPUSetAdjustmentRetrySchedulesAgainWhenReclaimOverrideTrimmed(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1, 48, 49))
	p.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)
	p.adjustment.cpuSetAdjustmentRetryQueued = true
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if handlerCtx.CommitOverride == nil {
				t.Fatal("CPUSet adjustment runner did not provide a commit override")
			}
			handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(1, 48, 49)
			handlerCtx.CommitOverride.Source = "cpuset_topology"
			return nil
		},
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
	p.Unlock()
	require.NoError(t, err)

	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(1, 49)),
		"retry override must commit only complete physical cores, got %s", reclaim.AllocationResult)
	require.True(t, p.adjustment.cpuSetAdjustmentRetryAgain,
		"trimming during a retry must request another latest-state pass to align runtime side effects with the committed checkpoint")
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonRecoveryCommit)
}

func TestCPUSetAdjustmentCommitOverrideUsesRevisionGuard(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))
	guardState := &cpusetOverrideCommitGuardState{State: p.state}
	p.state = guardState
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"topology-override": func(_ context.Context, handlerCtx cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if handlerCtx.CommitOverride == nil {
				t.Fatal("CPUSet adjustment runner did not provide a commit override")
			}
			handlerCtx.CommitOverride.ReclaimEffective = machine.NewCPUSet(2, 3)
			handlerCtx.CommitOverride.Source = "cpuset_topology"
			return nil
		},
	}

	p.Lock()
	err := p.runCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, 0, guardState.unconditionalCommitCalls)
	require.Equal(t, 1, guardState.conditionalCommitCalls)
	require.NotZero(t, guardState.conditionalRevision)

	reclaim := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 3)),
		"reclaim allocation=%s, want topology verified override 2-3", reclaim.AllocationResult)
}

func TestAdvisorCPUSetAdjustmentFailureRetainsDesiredStateAndRetries(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	desired := machine.NewCPUSet(0, 1)
	setReclaimPoolCPUSet(t, p, desired)

	retried := make(chan machine.CPUSet, 1)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"transient-cgroup-failure": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			reclaim := in.State.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			if in.Mode == cpusetutil.CPUSetAdjustmentModeRetry {
				retried <- reclaim.AllocationResult.Clone()
				return nil
			}
			return errors.New("transient cgroup write failure")
		},
	}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.ErrorContains(t, err, "transient cgroup write failure")
	require.True(t, p.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult.Equals(desired),
		"failed cgroup apply must retain committed desired state")

	select {
	case got := <-retried:
		require.True(t, got.Equals(desired), "retry must consume retained desired state, got %s", got)
	case <-time.After(time.Second):
		t.Fatal("failed advisor cgroup apply was not retried")
	}
}

func TestPostAdvisorCommitApplyFailureRunsInOrderAndMarksRevisionOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		headroomErr    error
		cgroupErr      error
		adjustmentErr  error
		wantErrStrings []string
	}{
		{
			name:           "headroom failure",
			headroomErr:    errors.New("headroom failure"),
			wantErrStrings: []string{"headroom failure"},
		},
		{
			name:           "cgroup config failure",
			cgroupErr:      errors.New("cgroup config failure"),
			wantErrStrings: []string{"cgroup config failure"},
		},
		{
			name:           "cpuset adjustment failure",
			adjustmentErr:  errors.New("cpuset adjustment failure"),
			wantErrStrings: []string{"cpuset adjustment failure"},
		},
		{
			name:           "headroom and cgroup failures",
			headroomErr:    errors.New("headroom failure"),
			cgroupErr:      errors.New("cgroup config failure"),
			wantErrStrings: []string{"headroom failure", "cgroup config failure"},
		},
		{
			name:           "headroom and cpuset failures",
			headroomErr:    errors.New("headroom failure"),
			adjustmentErr:  errors.New("cpuset adjustment failure"),
			wantErrStrings: []string{"headroom failure", "cpuset adjustment failure"},
		},
		{
			name:           "cgroup and cpuset failures",
			cgroupErr:      errors.New("cgroup config failure"),
			adjustmentErr:  errors.New("cpuset adjustment failure"),
			wantErrStrings: []string{"cgroup config failure", "cpuset adjustment failure"},
		},
		{
			name:           "all apply stages fail",
			headroomErr:    errors.New("headroom failure"),
			cgroupErr:      errors.New("cgroup config failure"),
			adjustmentErr:  errors.New("cpuset adjustment failure"),
			wantErrStrings: []string{"headroom failure", "cgroup config failure", "cpuset adjustment failure"},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			desired := machine.NewCPUSet(0, 1)
			setReclaimPoolCPUSet(t, p, desired)
			revision := p.state.GetRevision()

			var calls []string
			p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
				calls = append(calls, "apply-headroom")
				return tc.headroomErr
			}
			p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error {
				calls = append(calls, "apply-cgroup-configs")
				return tc.cgroupErr
			}
			p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
				calls = append(calls, "adjust-cpuset")
				return tc.adjustmentErr
			}
			p.markAdvisorApplyFailedHook = func(gotRevision uint64) {
				require.Equal(t, revision, gotRevision)
				calls = append(calls, "mark-apply-failed")
			}

			p.Lock()
			target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, revision)
			err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
			p.Unlock()

			for _, wantErr := range tc.wantErrStrings {
				require.ErrorContains(t, err, wantErr)
			}
			require.Equal(t, []string{"apply-headroom", "apply-cgroup-configs", "adjust-cpuset", "mark-apply-failed"}, calls)
			require.True(t, p.state.GetAllocationInfo(
				commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult.Equals(desired),
				"post-commit apply failures must not roll desired state back")
		})
	}
}

func TestAdvisorPostCommitTargetClonesResponseExtraEntries(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{}
	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{
			CgroupPath: "/old",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{"key": "old"},
			},
		}},
	}

	target := p.publishAdvisorPostCommitTarget(resp, 7)
	resp.ExtraEntries[0].CgroupPath = "/mutated"
	resp.ExtraEntries[0].CalculationResult.Values["key"] = "mutated"
	resp.ExtraEntries = nil

	require.Equal(t, uint64(7), target.revision)
	require.Len(t, target.response.ExtraEntries, 1)
	require.Equal(t, "/old", target.response.ExtraEntries[0].CgroupPath)
	require.Equal(t, "old", target.response.ExtraEntries[0].CalculationResult.Values["key"])
}

func TestAdvisorPostCommitRetryReplaysAllStagesUntilConverged(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	var calls []string
	attempt := 0
	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
		calls = append(calls, "headroom")
		return nil
	}
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error {
		calls = append(calls, "cgroup")
		if attempt == 0 {
			return errors.New("transient cgroup failure")
		}
		return nil
	}
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		calls = append(calls, "cpuset")
		attempt++
		return nil
	}
	p.markAdvisorApplyFailedHook = func(_ uint64) {}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.ErrorContains(t, err, "transient cgroup failure")
	require.Equal(t, []string{"headroom", "cgroup", "cpuset"}, calls)
	require.True(t, p.hasPendingAdvisorPostCommitTarget(target.revision))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryDirty = true
	p.adjustment.cpuSetAdjustmentRetryReasons = map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
		cpusetutil.RetryReasonApplyFailed: {},
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, []string{"headroom", "cgroup", "cpuset", "headroom", "cgroup", "cpuset"}, calls)
	require.False(t, p.hasPendingAdvisorPostCommitTarget(target.revision))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.NotContains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

// TestAdvisorPostCommitConvergenceDeadlineReleasesWriterFence verifies that a
// physical_apply failure caused solely by convergence-budget exhaustion drops
// the writer fence immediately (mirroring recoverStuck) so unrelated Allocate
// calls do not wait for the 30s stuck threshold. The target and its WAL must
// remain for the background retry to finish the physical replay.
func TestAdvisorPostCommitConvergenceDeadlineReleasesWriterFence(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		return fmt.Errorf("run cpuset adjustment handler %q: %w",
			"cpuset_topology",
			fmt.Errorf("%w: %s", bulkheadtopology.ErrConvergenceDeadlineExceeded, "deadline=10s"))
	}
	p.markAdvisorApplyFailedHook = func(_ uint64) {}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()

	require.ErrorContains(t, err, "convergence deadline exceeded")
	// Target is still pending (retain WAL for retry), but writer fence must be released.
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, target.writerFenceReleased, "writer fence must be released on convergence deadline")
	require.NotNil(t, p.adjustment.advisorPostCommitTarget, "target must remain for background retry")
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

// TestAdvisorPostCommitGenuineAdjustmentErrorKeepsWriterFence verifies that a
// non-deadline physical_apply failure (e.g. a cgroup write error bubbled up
// through runCPUSetAdjustmentHandlers) keeps the writer fence held, because
// unrelated writers must not race an incomplete physical transaction.
func TestAdvisorPostCommitGenuineAdjustmentErrorKeepsWriterFence(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		return errors.New("real cgroup write failure: invalid argument")
	}
	p.markAdvisorApplyFailedHook = func(_ uint64) {}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()

	require.ErrorContains(t, err, "real cgroup write failure")
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, target.writerFenceReleased, "writer fence must stay held on genuine errors")
	require.NotNil(t, p.adjustment.advisorPostCommitTarget)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

// A generic caller deadline has no proof that topology exhausted its own
// convergence budget, so it must not release the post-commit serialization fence.
func TestAdvisorPostCommitGenericContextDeadlineKeepsWriterFence(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		return context.DeadlineExceeded
	}
	p.markAdvisorApplyFailedHook = func(_ uint64) {}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()

	require.ErrorIs(t, err, context.DeadlineExceeded)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, target.writerFenceReleased,
		"generic context deadline must not release writer fence")
	require.NotNil(t, p.adjustment.advisorPostCommitTarget)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

// Even with an explicit topology deadline, a failed non-topology stage leaves
// the transaction incomplete and must keep the writer fence held.
func TestAdvisorPostCommitConvergenceDeadlineWithStageFailureKeepsWriterFence(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
		return errors.New("headroom apply failed")
	}
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		return fmt.Errorf("%w: deadline=10s", bulkheadtopology.ErrConvergenceDeadlineExceeded)
	}
	p.markAdvisorApplyFailedHook = func(_ uint64) {}

	p.Lock()
	target := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()

	require.ErrorContains(t, err, "headroom apply failed")
	require.ErrorContains(t, err, "convergence deadline exceeded")
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, target.writerFenceReleased,
		"independent stage failure must preserve writer fence")
	require.NotNil(t, p.adjustment.advisorPostCommitTarget)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func TestAdvisorPostCommitNewRevisionSupersedesStaleTarget(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	var paths []string
	p.applyHeadroomHook = func(resp *advisorapi.ListAndWatchResponse) error {
		paths = append(paths, resp.ExtraEntries[0].CgroupPath)
		return nil
	}
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error { return nil }
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error { return nil }

	oldTarget := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/old"}},
	}, p.state.GetRevision())
	p.state.SetAllowSharedCoresOverlapReclaimedCores(
		!p.state.GetAllowSharedCoresOverlapReclaimedCores(), false)
	newTarget := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/new"}},
	}, p.state.GetRevision())

	p.Lock()
	require.NoError(t, p.reconcileAdvisorPostCommitTarget(context.Background(), oldTarget))
	require.NoError(t, p.reconcileAdvisorPostCommitTarget(context.Background(), newTarget))
	p.Unlock()

	require.Equal(t, []string{"/new"}, paths)
	require.False(t, p.hasAnyPendingAdvisorPostCommitTarget())
}

func TestAdvisorPostCommitTargetPreservesWALWhenRevisionChangesBetweenStages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		advanceAfter string
		wantCalls    []string
	}{
		{name: "before headroom", advanceAfter: "publish"},
		{name: "before cgroup", advanceAfter: "headroom", wantCalls: []string{"headroom"}},
		{name: "before cpuset", advanceAfter: "cgroup", wantCalls: []string{"headroom", "cgroup"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			var calls []string

			advanceRevision := func() {
				p.state.SetAllowSharedCoresOverlapReclaimedCores(
					!p.state.GetAllowSharedCoresOverlapReclaimedCores(), false)
			}
			p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
				calls = append(calls, "headroom")
				if tc.advanceAfter == "headroom" {
					advanceRevision()
				}
				return nil
			}
			p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error {
				calls = append(calls, "cgroup")
				if tc.advanceAfter == "cgroup" {
					advanceRevision()
				}
				return nil
			}
			p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
				calls = append(calls, "cpuset")
				return nil
			}

			target := p.publishAdvisorPostCommitTarget(
				&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
			if tc.advanceAfter == "publish" {
				advanceRevision()
			}
			p.Lock()
			err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
			p.Unlock()

			require.Error(t, err)
			var retryable retryablePartitionCommitError
			require.ErrorAs(t, err, &retryable)
			require.True(t, retryable.Retryable())
			require.Equal(t, tc.wantCalls, calls)
			require.Same(t, target, p.currentAdvisorPostCommitTarget(),
				"a revision race must preserve the exact pending target")
		})
	}
}

func TestAdvisorPostCommitTargetChecksCurrentPointerBetweenStages(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	var calls []string

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
		calls = append(calls, "old-headroom")
		p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/new"}},
		}, p.state.GetRevision())
		return nil
	}
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error {
		calls = append(calls, "old-cgroup")
		return nil
	}
	p.runCPUSetAdjustmentHandlersHook = func(_ context.Context, _ ...cpusetutil.CPUSetAdjustmentMode) error {
		calls = append(calls, "old-cpuset")
		return nil
	}

	old := p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/old"}},
	}, p.state.GetRevision())
	p.Lock()
	require.NoError(t, p.reconcileAdvisorPostCommitTarget(context.Background(), old))
	p.Unlock()

	require.Equal(t, []string{"old-headroom"}, calls)
	require.True(t, p.hasAnyPendingAdvisorPostCommitTarget(),
		"superseding target must remain pending")
}

func TestAdvisorPostCommitCheckpointCrashRecoveryAndSuccessfulCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	revision := p.state.GetRevision()
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pool": {Entries: map[string]*advisorapi.CalculationInfo{
				"block": {OwnerPoolName: "persisted-pool"},
			}},
		},
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/persisted"}},
	}

	target := p.publishAdvisorPostCommitTarget(resp, revision)
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))

	restarted := &DynamicPolicy{
		state:                          p.state,
		advisorPostCommitCheckpointDir: dir, adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{}},
	}
	require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
	restored := restarted.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, revision, restored.revision)
	require.True(t, proto.Equal(target.response, restored.response),
		"checkpoint must retain the complete proto response")

	restarted.Lock()
	require.NoError(t, restarted.reconcileAdvisorPostCommitTarget(context.Background(), restored))
	restarted.Unlock()
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

// TestAdvisorPostCommitAppliedRecoveryCleansUpWithoutReplayingPhysicalApply composes with
// TestManagerSoftDeferThinSeamViaRunCPUSetAdjustmentHandlers: the nil-return soft-defer
// contract is covered there, and here the real reconcile consume/persist path marks the
// target applied and writes the WAL. The stub asserts applied != global-reset-complete (the
// global reset stays incomplete). On restart, Cleanup must drop the checkpoint and must NOT
// replay the physical handler. This covers the dynamicpolicy consume/persist/recovery half;
// it does NOT exercise the production NewManager registration end-to-end.
func TestAdvisorPostCommitAppliedRecoveryCleansUpWithoutReplayingPhysicalApply(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	revision := p.state.GetRevision()
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pool": {Entries: map[string]*advisorapi.CalculationInfo{
				"block": {OwnerPoolName: "persisted-pool"},
			}},
		},
	}

	physicalApplyCalls := 0
	target := p.publishAdvisorPostCommitTarget(resp, revision)
	require.False(t, target.applied, "fresh published target must start unapplied")

	// registered handler returns nil (the independently-verified non-blocking return).
	// globalResetComplete stays false: applied marker must not imply global reset complete.
	globalResetComplete := false
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{}
	p.adjustment.cpuSetAdjustmentHandlers["bulkhead"] = func(ctx context.Context, hcp cpusetutil.CPUSetAdjustmentHandlerCtx) error {
		physicalApplyCalls++
		return nil
	}
	p.Lock()
	require.NoError(t, p.reconcileAdvisorPostCommitTarget(context.Background(), target))
	p.Unlock()
	// After the nil handler the applied marker is produced and the WAL records it; persist
	// captures the crash boundary (global reset still incomplete, marker=true separation).
	require.True(t, target.applied, "reconcile after nil handler must mark applied")
	require.False(t, globalResetComplete, "applied marker must not imply global reset complete")
	require.Equal(t, 1, physicalApplyCalls, "consume round must invoke the registered handler once")
	require.NoError(t, p.persistAdvisorPostCommitApplied(target))
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))

	// Restart: restore the persisted applied target, then reconcile must Cleanup and NOT replay.
	restarted := &DynamicPolicy{
		state:                          p.state,
		advisorPostCommitCheckpointDir: dir,
		adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
			"bulkhead": func(ctx context.Context, hcp cpusetutil.CPUSetAdjustmentHandlerCtx) error {
				physicalApplyCalls++
				return nil
			},
		}},
	}

	require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
	restored := restarted.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.True(t, restored.applied, "restored WAL must carry the applied marker")

	restarted.Lock()
	require.NoError(t, restarted.reconcileAdvisorPostCommitTarget(context.Background(), restored))
	restarted.Unlock()

	require.Equal(t, 1, physicalApplyCalls,
		"applied recovery must run Cleanup and must NOT replay the registered physical adjustment handler")
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

const (
	advisorCheckpointSubprocessRoleEnv    = "KATALYST_ADVISOR_CHECKPOINT_SUBPROCESS_ROLE"
	advisorCheckpointSubprocessDirEnv     = "KATALYST_ADVISOR_CHECKPOINT_SUBPROCESS_DIR"
	advisorCheckpointSubprocessReadyFDEnv = "KATALYST_ADVISOR_CHECKPOINT_SUBPROCESS_READY_FD"
	advisorCheckpointSubprocessTimeout    = 10 * time.Second
)

func TestAdvisorCheckpointSubprocessRestoresDisjointPartitionRevisionPendingAndRetry(t *testing.T) {
	t.Parallel()
	switch os.Getenv(advisorCheckpointSubprocessRoleEnv) {
	case "writer":
		runAdvisorCheckpointWriter(t, os.Getenv(advisorCheckpointSubprocessDirEnv))
		return
	case "reader":
		runAdvisorCheckpointReader(t, os.Getenv(advisorCheckpointSubprocessDirEnv))
		return
	case "timeout-probe":
		fmt.Fprintln(os.Stderr, "advisor checkpoint timeout probe started")
		if os.Getenv(advisorCheckpointSubprocessReadyFDEnv) != "3" {
			t.Fatal("timeout probe requires readiness fd 3")
		}
		ready := os.NewFile(3, "advisor-checkpoint-ready")
		if ready == nil {
			t.Fatal("timeout probe requires readiness fd")
		}
		_, err := io.WriteString(ready, "ready")
		require.NoError(t, err)
		require.NoError(t, ready.Close())
		select {}
	}

	dir := t.TempDir()
	run := func(role string) {
		t.Helper()
		_, err := runAdvisorCheckpointSubprocess(role, dir, advisorCheckpointSubprocessTimeout)
		require.NoError(t, err)
	}
	run("writer")
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	run("reader")
}

func TestAdvisorCheckpointSubprocessTimeoutKillsChild(t *testing.T) {
	t.Parallel()
	output, err := runAdvisorCheckpointTimeoutProbe(t, t.TempDir())
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "timeout-probe subprocess was killed after readiness")
	require.Contains(t, string(output), "advisor checkpoint timeout probe started")
}

type checkpointTriggeredDeadlineContext struct {
	parent  context.Context
	done    chan struct{}
	expired uint32
}

func newCheckpointTriggeredDeadlineContext(parent context.Context) *checkpointTriggeredDeadlineContext {
	return &checkpointTriggeredDeadlineContext{parent: parent, done: make(chan struct{})}
}

func (c *checkpointTriggeredDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *checkpointTriggeredDeadlineContext) Done() <-chan struct{}       { return c.done }
func (c *checkpointTriggeredDeadlineContext) Err() error {
	if atomic.LoadUint32(&c.expired) != 0 {
		return context.DeadlineExceeded
	}
	return c.parent.Err()
}

func (c *checkpointTriggeredDeadlineContext) Value(key interface{}) interface{} {
	return c.parent.Value(key)
}

func (c *checkpointTriggeredDeadlineContext) expire() {
	if atomic.CompareAndSwapUint32(&c.expired, 0, 1) {
		close(c.done)
	}
}

func runAdvisorCheckpointTimeoutProbe(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	ctx := newCheckpointTriggeredDeadlineContext(context.Background())
	readyReader, readyWriter, err := os.Pipe()
	require.NoError(t, err)
	defer readyReader.Close()

	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestAdvisorCheckpointSubprocessRestoresDisjointPartitionRevisionPendingAndRetry$",
		"-test.timeout="+(advisorCheckpointSubprocessTimeout+5*time.Second).String())
	cmd.Env = append(advisorCheckpointSubprocessEnv("timeout-probe", dir),
		advisorCheckpointSubprocessReadyFDEnv+"=3")
	cmd.ExtraFiles = []*os.File{readyWriter}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(t, cmd.Start())
	require.NoError(t, readyWriter.Close())

	ready := make([]byte, len("ready"))
	_, readErr := io.ReadFull(readyReader, ready)
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return output.Bytes(), fmt.Errorf("timeout-probe readiness failed: %w", readErr)
	}
	if string(ready) != "ready" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return output.Bytes(), fmt.Errorf("timeout-probe sent invalid readiness marker %q", ready)
	}

	ctx.expire()
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output.Bytes(), fmt.Errorf(
			"timeout-probe subprocess was killed after readiness: %w\ncaptured output:\n%s",
			ctxErr, output.Bytes())
	}
	if waitErr != nil {
		return output.Bytes(), fmt.Errorf(
			"timeout-probe subprocess failed after readiness: %w\ncaptured output:\n%s",
			waitErr, output.Bytes())
	}
	return output.Bytes(), nil
}

func runAdvisorCheckpointSubprocess(role, dir string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	childTestTimeout := timeout + 5*time.Second
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestAdvisorCheckpointSubprocessRestoresDisjointPartitionRevisionPendingAndRetry$",
		"-test.timeout="+childTestTimeout.String())
	cmd.Env = advisorCheckpointSubprocessEnv(role, dir)
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, fmt.Errorf(
			"%s subprocess timed out after %s and was killed: %w\ncaptured output:\n%s",
			role, timeout, ctxErr, output)
	}
	if err != nil {
		return output, fmt.Errorf("%s subprocess failed: %w\ncaptured output:\n%s", role, err, output)
	}
	return output, nil
}

func advisorCheckpointSubprocessEnv(role, dir string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, advisorCheckpointSubprocessRoleEnv+"=") ||
			strings.HasPrefix(entry, advisorCheckpointSubprocessDirEnv+"=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		advisorCheckpointSubprocessRoleEnv+"="+role,
		advisorCheckpointSubprocessDirEnv+"="+dir)
}

func runAdvisorCheckpointWriter(t *testing.T, dir string) {
	t.Helper()
	require.NotEmpty(t, dir)
	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	p.advisorPostCommitCheckpointDir = dir

	dedicated := machine.NewCPUSet(0, 1)
	reclaim := machine.NewCPUSet(2, 3)
	entries := advisorCheckpointPartitionEntries(dedicated, reclaim)
	machineState, err := generateMachineStateFromPodEntries(
		topology, entries, p.state.GetMachineState())
	require.NoError(t, err)
	preCommitRevision := p.state.GetRevision()
	target, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{
			DisableDedicatedCoresOverlapReclaimedCores: true,
		},
		preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			return p.state.CommitAdvisorStateIfRevision(
				preCommitRevision, entries, machineState, false, true, true,
				p.newAdvisorStateWritePermit(target))
		},
	)
	require.NoError(t, err)
	require.Equal(t, preCommitRevision+1, p.state.GetRevision())
	require.Equal(t, p.state.GetRevision(), target.revision)
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

func runAdvisorCheckpointReader(t *testing.T, dir string) {
	t.Helper()
	require.NotEmpty(t, dir)
	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	p.advisorPostCommitCheckpointDir = dir
	retryCalls := make(chan cpusetutil.CPUSetAdjustmentHandlerCtx, 1)
	var retryCallCount int32
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"keep-pending": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			atomic.AddInt32(&retryCallCount, 1)
			select {
			case retryCalls <- in:
			default:
			}
			return errors.New("keep recovered target pending")
		},
	}

	require.NoError(t, p.Start())
	defer func() { require.NoError(t, p.Stop()) }()

	select {
	case call := <-retryCalls:
		require.Equal(t, cpusetutil.CPUSetAdjustmentModeRetry, call.Mode)
	case <-time.After(2 * time.Second):
		t.Fatalf("retry handler was not called after Start; calls=%d", atomic.LoadInt32(&retryCallCount))
	}
	require.GreaterOrEqual(t, atomic.LoadInt32(&retryCallCount), int32(1))

	dedicated := p.state.GetAllocationInfo("pod-dedicated", "main").AllocationResult
	reclaim := p.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult
	require.Equal(t, machine.NewCPUSet(0, 1), dedicated)
	require.Equal(t, machine.NewCPUSet(2, 3), reclaim)
	require.True(t, dedicated.Intersection(reclaim).IsEmpty())
	require.Equal(t, machine.NewCPUSet(0, 1, 2, 3), dedicated.Union(reclaim))
	require.True(t, p.state.GetDisableDedicatedCoresOverlapReclaimedCores())

	target := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	require.Equal(t, p.state.GetRevision(), target.revision)
	require.Greater(t, target.revision, uint64(0))
	require.True(t, p.hasPendingAdvisorPostCommitTarget(p.state.GetRevision()))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	dirty := p.adjustment.cpuSetAdjustmentRetryDirty
	_, hasApplyFailedReason := p.adjustment.cpuSetAdjustmentRetryReasons[cpusetutil.RetryReasonApplyFailed]
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.True(t, dirty)
	require.True(t, hasApplyFailedReason)
}

func advisorCheckpointPartitionEntries(dedicated, reclaim machine.CPUSet) state.PodEntries {
	return state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
				},
				AllocationResult:                 dedicated,
				OriginalAllocationResult:         dedicated,
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: dedicated},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: dedicated},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult:                 reclaim,
				OriginalAllocationResult:         reclaim,
				TopologyAwareAssignments:         map[int]machine.CPUSet{0: reclaim},
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{0: reclaim},
			},
		},
	}
}

func TestAdvisorWriteAheadTargetRejectsRevisionOverflow(t *testing.T) {
	t.Parallel()
	_, err := nextAdvisorRevision(math.MaxUint64)
	require.ErrorContains(t, err, "revision overflow")
}

func TestAdvisorMigrationTransitionFailureRetainsWALAndStopsLaterSideEffects(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	topology := p.machine.machineInfo.CPUTopology
	oldTarget := coresInNUMA(topology, 0, 0, 1)
	newTarget := coresInNUMA(topology, 0, 1, 2)
	require.NoError(t, p.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: "old", target: oldTarget}))
	preCommitRevision := p.state.GetRevision()

	target, err := p.commitAdvisorResponseWithWriteAheadTransition(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{
				CalculationResult: &advisorsvc.CalculationResult{
					Values: map[string]string{
						string(advisorapi.ControlKnobKeyCPUNUMAHeadroom): `{"0":7.5}`,
					},
				},
			}},
		},
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointReplace,
			target: &steadyFakeNUMAMigrationIntent{
				constraintDigest: "new",
				target:           newTarget,
			},
		},
		preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			return p.state.CommitAdvisorStateIfRevision(
				preCommitRevision,
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				p.state.GetAllowSharedCoresOverlapReclaimedCores(),
				p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
				p.newAdvisorStateWritePermit(target),
			)
		},
	)
	require.NoError(t, err)
	require.Equal(t, oldTarget, p.steadyFakeNUMAMigration.current().target,
		"CAS must not perform migration checkpoint I/O")

	checkpointPath := steadyFakeNUMACheckpointFile(p)
	require.NoError(t, os.Remove(checkpointPath))
	require.NoError(t, os.Mkdir(checkpointPath, 0o700))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	var adjustmentCalls int
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"observe": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			adjustmentCalls++
			return nil
		},
	}
	beforeHeadroom := p.state.GetNUMAHeadroom()
	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.ErrorContains(t, err, "migration checkpoint transition")
	require.Equal(t, beforeHeadroom, p.state.GetNUMAHeadroom())
	require.Zero(t, adjustmentCalls)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())

	require.NoError(t, os.Remove(checkpointPath))
	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, map[int]float64{0: 7.5}, p.state.GetNUMAHeadroom())
	require.Equal(t, 1, adjustmentCalls)
	require.Equal(t, "new", p.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, newTarget, p.steadyFakeNUMAMigration.current().target)
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestAdvisorMigrationTransitionSurvivesCommitCrashAndRestart(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopologyWithoutSMT(4, 1, 1)
	require.NoError(t, err)
	dir := t.TempDir()
	first, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	targetCPUSet := coresInNUMA(topology, 0, 2, 4)
	preCommitRevision := first.state.GetRevision()

	_, err = first.commitAdvisorResponseWithWriteAheadTransition(
		&advisorapi.ListAndWatchResponse{},
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointReplace,
			target: &steadyFakeNUMAMigrationIntent{
				constraintDigest: "restart",
				target:           targetCPUSet,
			},
		},
		preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			return first.state.CommitAdvisorStateIfRevision(
				preCommitRevision,
				first.state.GetPodEntries(),
				first.state.GetMachineState(),
				first.state.GetAllowSharedCoresOverlapReclaimedCores(),
				first.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
				first.newAdvisorStateWritePermit(target),
			)
		},
	)
	require.NoError(t, err)
	require.Nil(t, first.steadyFakeNUMAMigration.current())
	require.NoFileExists(t, steadyFakeNUMACheckpointFile(first))

	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
	restored := restarted.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace,
		restored.migrationCheckpointTransition.kind)
	require.Equal(t, "restart",
		restored.migrationCheckpointTransition.target.constraintDigest)

	observedTransition := false
	restarted.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"observe-transition": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			observedTransition = restarted.steadyFakeNUMAMigration.current() != nil &&
				restarted.steadyFakeNUMAMigration.current().constraintDigest == "restart"
			return nil
		},
	}
	restarted.Lock()
	err = restarted.reconcileAdvisorPostCommitTarget(context.Background(), restored)
	restarted.Unlock()
	require.NoError(t, err)
	require.True(t, observedTransition,
		"migration transition must be the first post-commit stage")
	require.Equal(t, targetCPUSet, restarted.steadyFakeNUMAMigration.current().target)
	require.NoFileExists(t, restarted.advisorPostCommitCheckpointPath())
}

func TestLegacyAdvisorWALDefaultsMigrationTransitionToKeep(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	oldTarget := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1)
	require.NoError(t, p.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: "legacy", target: oldTarget}))
	response, err := proto.Marshal(&advisorapi.ListAndWatchResponse{})
	require.NoError(t, err)
	data, err := json.Marshal(advisorPostCommitCheckpoint{
		Revision: p.state.GetRevision(),
		Response: response,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.advisorPostCommitCheckpointPath(), data, 0o600))

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	target := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	require.Equal(t, steadyFakeNUMAMigrationCheckpointKeep,
		target.migrationCheckpointTransition.kind)
	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, "legacy", p.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, oldTarget, p.steadyFakeNUMAMigration.current().target)
}

func TestAdvisorWALV2IsVersionedChecksummedAndFencedFromLegacyReaders(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	preCommitRevision := p.state.GetRevision()
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	response := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/v2"}},
	}
	targetCPUs := coresInNUMA(p.machine.machineInfo.CPUTopology, 0, 0, 1)
	target := cloneAdvisorPostCommitTarget(
		response,
		p.state.GetRevision(),
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointReplace,
			target: &steadyFakeNUMAMigrationIntent{
				constraintDigest: "v2",
				target:           targetCPUs,
			},
		},
	)
	target.preCommitRevision = preCommitRevision

	require.NoError(t, p.storeAdvisorPostCommitTarget(
		target, p.advisorPostCommitCheckpointPath()))
	data, err := os.ReadFile(p.advisorPostCommitCheckpointPath())
	require.NoError(t, err)
	var checkpoint advisorPostCommitCheckpoint
	require.NoError(t, json.Unmarshal(data, &checkpoint))
	require.Equal(t, advisorPostCommitCheckpointVersion, checkpoint.Version)
	require.NotNil(t, checkpoint.PreCommitRevision)
	require.Equal(t, target.preCommitRevision, *checkpoint.PreCommitRevision)
	require.NotEmpty(t, checkpoint.Checksum)
	require.True(t, bytes.HasPrefix(
		checkpoint.Response, []byte("\x00KATALYST_CPU_ADVISOR_WAL_V2\x00")))

	legacyResponse := &advisorapi.ListAndWatchResponse{}
	require.Error(t, proto.Unmarshal(checkpoint.Response, legacyResponse),
		"a legacy reader must fail closed instead of silently dropping the V2 migration transition")

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, target.preCommitRevision, restored.preCommitRevision)
	require.True(t, proto.Equal(response, restored.response))
	require.Equal(t, steadyFakeNUMAMigrationCheckpointReplace,
		restored.migrationCheckpointTransition.kind)
	require.Equal(t, "v2", restored.migrationCheckpointTransition.target.constraintDigest)
	require.Equal(t, targetCPUs, restored.migrationCheckpointTransition.target.target)
}

func TestAdvisorWALV2RejectsMissingOrCorruptedEnvelope(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	preCommitRevision := p.state.GetRevision()
	target := cloneAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{},
		preCommitRevision+1,
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointRemove,
		},
	)
	target.preCommitRevision = preCommitRevision
	require.NoError(t, p.storeAdvisorPostCommitTarget(
		target, p.advisorPostCommitCheckpointPath()))
	original, err := os.ReadFile(p.advisorPostCommitCheckpointPath())
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*advisorPostCommitCheckpoint)
		want   string
	}{
		{
			name: "missing checksum",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				checkpoint.Checksum = ""
			},
			want: "checksum",
		},
		{
			name: "unsupported version",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				checkpoint.Version++
			},
			want: "unsupported advisor checkpoint version",
		},
		{
			name: "revision corruption",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				checkpoint.Revision++
			},
			want: "checksum mismatch",
		},
		{
			name: "pre-commit revision corruption",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				require.NotNil(t, checkpoint.PreCommitRevision)
				*checkpoint.PreCommitRevision++
			},
			want: "checksum mismatch",
		},
		{
			name: "response corruption",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				checkpoint.Response = append(checkpoint.Response, 0)
			},
			want: "checksum mismatch",
		},
		{
			name: "transition corruption",
			mutate: func(checkpoint *advisorPostCommitCheckpoint) {
				checkpoint.MigrationCheckpointTransition.Kind = steadyFakeNUMAMigrationCheckpointReplace
			},
			want: "checksum mismatch",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var checkpoint advisorPostCommitCheckpoint
			require.NoError(t, json.Unmarshal(original, &checkpoint))
			tt.mutate(&checkpoint)
			data, err := json.Marshal(checkpoint)
			require.NoError(t, err)
			sub := &DynamicPolicy{
				state:                          p.state,
				machine:                        p.machine,
				advisorPostCommitCheckpointDir: t.TempDir(),
			}
			require.NoError(t, os.WriteFile(
				sub.advisorPostCommitCheckpointPath(), data, 0o600))
			require.ErrorContains(t, sub.restoreAdvisorPostCommitTarget(), tt.want)
		})
	}
}

func TestAdvisorWALV2RejectsInvalidRevisionTransition(t *testing.T) {
	t.Parallel()
	response, err := proto.Marshal(&advisorapi.ListAndWatchResponse{})
	require.NoError(t, err)

	tests := []struct {
		name string
		pre  uint64
		post uint64
	}{
		{name: "equal revisions", pre: 7, post: 7},
		{name: "skipped revision", pre: 7, post: 9},
		{name: "pre-commit revision overflow", pre: math.MaxUint64, post: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkpoint := advisorPostCommitCheckpoint{
				Version:           advisorPostCommitCheckpointVersion,
				PreCommitRevision: &tt.pre,
				Revision:          tt.post,
				Response:          append([]byte(advisorPostCommitWALV2Magic), response...),
			}
			checkpoint.Checksum = advisorPostCommitCheckpointChecksum(
				checkpoint.Version,
				checkpoint.PreCommitRevision,
				checkpoint.Revision,
				response,
				nil,
			)
			path := filepath.Join(t.TempDir(), advisorPostCommitCheckpointName)
			data, err := json.Marshal(checkpoint)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, data, 0o600))

			_, err = loadAdvisorPostCommitTarget(path, nil)
			require.ErrorContains(t, err, "invalid advisor checkpoint revision transition")
		})
	}
}

func TestAdvisorWALV2UsesStrictJSONDecoding(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	target := cloneAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{},
		p.state.GetRevision(),
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointRemove,
		},
	)
	require.NoError(t, p.storeAdvisorPostCommitTarget(
		target, p.advisorPostCommitCheckpointPath()))
	data, err := os.ReadFile(p.advisorPostCommitCheckpointPath())
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	raw["future_field"] = true
	unknownTopLevel, err := json.Marshal(raw)
	require.NoError(t, err)

	raw = nil
	require.NoError(t, json.Unmarshal(data, &raw))
	raw["migration_checkpoint_transition"].(map[string]any)["future_field"] = true
	unknownTransition, err := json.Marshal(raw)
	require.NoError(t, err)

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "unknown top-level field", data: unknownTopLevel, want: "unknown field"},
		{name: "unknown transition field", data: unknownTransition, want: "unknown field"},
		{name: "trailing JSON value", data: append(data, []byte(` {}`)...), want: "trailing"},
		{name: "trailing garbage", data: append(data, []byte(` garbage`)...), want: "trailing"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest needs its own checkpoint directory so parallel runs do
			// not race on the same checkpoint file; share only the read-only state.
			sub := &DynamicPolicy{
				state:                          p.state,
				machine:                        p.machine,
				advisorPostCommitCheckpointDir: t.TempDir(),
			}
			require.NoError(t, os.WriteFile(
				sub.advisorPostCommitCheckpointPath(), tt.data, 0o600))
			require.ErrorContains(t, sub.restoreAdvisorPostCommitTarget(), tt.want)
		})
	}
}

func TestAdvisorWALV2RejectsReplaceOutsideTopologyOrPartialCoreOnWrite(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	tests := []struct {
		name string
		cpus machine.CPUSet
		want string
	}{
		{name: "outside topology", cpus: machine.NewCPUSet(99), want: "outside topology"},
		{name: "partial SMT core", cpus: machine.NewCPUSet(0), want: "core aligned"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target := cloneAdvisorPostCommitTarget(
				&advisorapi.ListAndWatchResponse{},
				p.state.GetRevision(),
				steadyFakeNUMAMigrationCheckpointTransition{
					kind: steadyFakeNUMAMigrationCheckpointReplace,
					target: &steadyFakeNUMAMigrationIntent{
						constraintDigest: "invalid",
						target:           tt.cpus,
					},
				},
			)
			err := p.storeAdvisorPostCommitTarget(
				target, p.advisorPostCommitCheckpointPath())
			require.ErrorContains(t, err, tt.want)
			require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
		})
	}
}

func TestAdvisorWALV2RejectsReplaceOutsideTopologyOrPartialCoreOnRead(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	response, err := proto.Marshal(&advisorapi.ListAndWatchResponse{})
	require.NoError(t, err)

	tests := []struct {
		name string
		cpus []int
		want string
	}{
		{name: "outside topology", cpus: []int{99}, want: "outside topology"},
		{name: "partial SMT core", cpus: []int{0}, want: "core aligned"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sub := &DynamicPolicy{
				state:                          p.state,
				machine:                        p.machine,
				advisorPostCommitCheckpointDir: t.TempDir(),
			}
			preCommitRevision := p.state.GetRevision()
			checkpoint := advisorPostCommitCheckpoint{
				Version:           advisorPostCommitCheckpointVersion,
				PreCommitRevision: &preCommitRevision,
				Revision:          preCommitRevision + 1,
				Response:          append([]byte(advisorPostCommitWALV2Magic), response...),
				MigrationCheckpointTransition: &advisorMigrationCheckpointTransitionWAL{
					Kind:             steadyFakeNUMAMigrationCheckpointReplace,
					ConstraintDigest: "invalid",
					TargetCPUs:       tt.cpus,
				},
			}
			checkpoint.Checksum = advisorPostCommitCheckpointChecksum(
				checkpoint.Version,
				checkpoint.PreCommitRevision,
				checkpoint.Revision,
				response,
				checkpoint.MigrationCheckpointTransition,
			)
			data, err := json.Marshal(checkpoint)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(
				sub.advisorPostCommitCheckpointPath(), data, 0o600))
			require.ErrorContains(t, sub.restoreAdvisorPostCommitTarget(), tt.want)
		})
	}
}

func TestAdvisorMigrationTransitionStaleCASDoesNotApplyAndRemovesStagingWAL(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	topology := p.machine.machineInfo.CPUTopology
	oldTarget := coresInNUMA(topology, 0, 0, 1)
	require.NoError(t, p.steadyFakeNUMAMigration.storeIntent(
		&steadyFakeNUMAMigrationIntent{constraintDigest: "old", target: oldTarget}))
	revision := p.state.GetRevision()

	_, err := p.commitAdvisorResponseWithWriteAheadTransition(
		&advisorapi.ListAndWatchResponse{},
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointRemove,
		},
		revision,
		func(target *advisorPostCommitTarget) error {
			return p.state.CommitAdvisorStateIfRevision(
				revision+1,
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				p.state.GetAllowSharedCoresOverlapReclaimedCores(),
				p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
				p.newAdvisorStateWritePermit(target),
			)
		},
	)

	require.ErrorIs(t, err, state.ErrStaleStateRevision)
	require.Equal(t, revision, p.state.GetRevision())
	require.Equal(t, "old", p.steadyFakeNUMAMigration.current().constraintDigest)
	require.Equal(t, oldTarget, p.steadyFakeNUMAMigration.current().target)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

func TestAdvisorWriteAheadTargetFailureDoesNotCommitDesired(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte("x"), 0o600))
	p.advisorPostCommitCheckpointDir = blockingFile

	committed := false
	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision(), func(_ *advisorPostCommitTarget) error {
			committed = true
			return nil
		})
	require.Error(t, err)
	require.False(t, committed, "desired state must not commit when WAL target persistence fails")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
}

func TestAdvisorWriteAheadPromoteFailureKeepsCommittedTargetPendingAndRecoverable(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	preCommitRevision := p.state.GetRevision()
	activePath := filepath.Join(dir, advisorPostCommitCheckpointName)

	target, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/committed"}},
		},
		preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			if err := p.state.CommitAdvisorStateIfRevision(
				preCommitRevision,
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				p.state.GetAllowSharedCoresOverlapReclaimedCores(),
				p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
				p.newAdvisorStateWritePermit(target),
			); err != nil {
				return err
			}
			return os.Mkdir(activePath, 0o700)
		},
	)

	require.ErrorContains(t, err, "promote advisor post-commit target")
	require.Nil(t, target)
	require.Equal(t, preCommitRevision+1, p.state.GetRevision())
	pending := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, pending, "committed target must block later frames even when WAL promotion fails")
	require.Equal(t, p.state.GetRevision(), pending.revision)
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))

	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"fail": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			return errors.New("keep committed target pending")
		},
	}
	err = p.allocateByCPUAdvisor(nil, &advisorapi.ListAndWatchResponse{}, nil)
	require.ErrorContains(t, err, "publish committed advisor post-commit target")

	require.NoError(t, os.Remove(activePath))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.advisorPostCommitTarget = nil
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	require.NotNil(t, p.currentAdvisorPostCommitTarget())
	require.Equal(t, "/committed",
		p.currentAdvisorPostCommitTarget().response.ExtraEntries[0].CgroupPath)
	require.FileExists(t, activePath)
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))
}

func TestAdvisorWriteAheadPromoteFailureRetriesPublicationBeforeApply(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	preCommitRevision := p.state.GetRevision()
	activePath := filepath.Join(dir, advisorPostCommitCheckpointName)
	applied := make(chan map[int]float64, 1)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"observe": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if _, err := os.Stat(activePath); err != nil {
				return fmt.Errorf("active WAL is not durable before apply: %w", err)
			}
			applied <- in.State.GetNUMAHeadroom()
			return nil
		},
	}

	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{
				CalculationResult: &advisorsvc.CalculationResult{
					Values: map[string]string{
						string(advisorapi.ControlKnobKeyCPUNUMAHeadroom): `{"0":7.5}`,
					},
				},
			}},
		},
		preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			if err := p.state.CommitAdvisorStateIfRevision(
				preCommitRevision,
				p.state.GetPodEntries(),
				p.state.GetMachineState(),
				p.state.GetAllowSharedCoresOverlapReclaimedCores(),
				p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
				p.newAdvisorStateWritePermit(target),
			); err != nil {
				return err
			}
			return os.Mkdir(activePath, 0o700)
		},
	)
	require.ErrorContains(t, err, "promote advisor post-commit target")

	select {
	case <-applied:
		t.Fatal("post-commit side effects ran before the WAL publication barrier")
	default:
	}
	require.NoError(t, os.Remove(activePath))
	p.Lock()
	err = p.retryLatestCPUSetAdjustment(
		context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
	p.Unlock()
	require.NoError(t, err)
	select {
	case headroom := <-applied:
		require.Equal(t, map[int]float64{0: 7.5}, headroom)
	default:
		t.Fatal("published target was not applied")
	}
}

func TestAdvisorWriteAheadTargetIsRemovedWhenDesiredCommitFails(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir

	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision(), func(target *advisorPostCommitTarget) error {
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"),
				"target must be durable before applyBlocks commits desired state")
			require.Same(t, target, p.currentAdvisorPostCommitTarget(),
				"prepared target must fence writers before desired commit")
			return errors.New("applyBlocks failed")
		})
	require.ErrorContains(t, err, "applyBlocks failed")
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))
	require.Nil(t, p.currentAdvisorPostCommitTarget())
}

func TestAdvisorWriteAheadCleanupFailureKeepsPreparedFence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		commitDesired func() error
		wantError     string
	}{
		{
			name: "desired commit failure",
			commitDesired: func() error {
				return errors.New("desired commit failed")
			},
			wantError: "desired commit failed",
		},
		{
			name:          "revision check failure",
			commitDesired: func() error { return nil },
			wantError:     "revision mismatch",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			p.installCPUStateWritePermit()
			p.adjustment.cpuSetAdjustmentRetryMu.Lock()
			p.adjustment.cpuSetAdjustmentRetryStopping = true
			p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
			dir := t.TempDir()
			p.advisorPostCommitCheckpointDir = dir
			stagingPath := filepath.Join(dir, advisorPostCommitCheckpointName+".staging")
			preCommitRevision := p.state.GetRevision()
			var prepared *advisorPostCommitTarget

			_, err := p.commitAdvisorResponseWithWriteAhead(
				&advisorapi.ListAndWatchResponse{},
				preCommitRevision,
				func(target *advisorPostCommitTarget) error {
					prepared = target
					require.NoError(t, os.Remove(stagingPath))
					require.NoError(t, os.Mkdir(stagingPath, 0o700))
					require.NoError(t, os.WriteFile(
						filepath.Join(stagingPath, "blocks-remove"), []byte("x"), 0o600))
					return tc.commitDesired()
				},
			)

			require.ErrorContains(t, err, tc.wantError)
			require.ErrorContains(t, err, "remove")
			require.Same(t, prepared, p.currentAdvisorPostCommitTarget())
			require.True(t, prepared.prepared)
			require.Equal(t, preCommitRevision, p.state.GetRevision())

			err = p.state.SetMachineState(p.state.GetMachineState(), false)
			var retryable retryablePartitionCommitError
			require.ErrorAs(t, err, &retryable,
				"failed staging cleanup must keep unrelated writers fenced")

			require.NoError(t, os.Remove(filepath.Join(stagingPath, "blocks-remove")))
			p.Lock()
			err = p.retryLatestCPUSetAdjustment(
				context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
			p.Unlock()
			require.NoError(t, err)
			require.Nil(t, p.currentAdvisorPostCommitTarget())
			require.NoError(t, p.ensureCPUStateWriterAllowed(p.state.GetRevision(), "test", nil))
		})
	}
}

func TestAdvisorWriterGateSnapshotsPreparedTargetUnderRetryLock(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{}
	target := &advisorPostCommitTarget{
		preCommitRevision: 1,
		prepared:          true,
		revision:          2,
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.advisorPostCommitTarget = target
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	const iterations = 10000
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			p.adjustment.cpuSetAdjustmentRetryMu.Lock()
			target.prepared = i%2 == 0
			target.preCommitRevision = uint64(i)
			target.revision = uint64(i + 1)
			p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		}
	}()
	close(start)
	for i := 0; i < iterations; i++ {
		_ = p.ensureCPUStateWriterAllowed(uint64(i), "test", target)
	}
	wg.Wait()
}

func TestAdvisorWriteAheadPreparedTargetFencesConcurrentStateWriter(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	p.installCPUStateWritePermit()
	preCommitRevision := p.state.GetRevision()

	prepared := make(chan *advisorPostCommitTarget, 1)
	releaseCommit := make(chan struct{})
	type commitResult struct {
		target *advisorPostCommitTarget
		err    error
	}
	committed := make(chan commitResult, 1)
	go func() {
		target, err := p.commitAdvisorResponseWithWriteAhead(
			&advisorapi.ListAndWatchResponse{},
			preCommitRevision,
			func(target *advisorPostCommitTarget) error {
				prepared <- target
				<-releaseCommit
				return p.state.CommitAdvisorStateIfRevision(
					preCommitRevision,
					p.state.GetPodEntries(),
					p.state.GetMachineState(),
					p.state.GetAllowSharedCoresOverlapReclaimedCores(),
					p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
					true,
					p.newAdvisorStateWritePermit(target),
				)
			},
		)
		committed <- commitResult{target: target, err: err}
	}()

	target := <-prepared
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.Equal(t, preCommitRevision, p.state.GetRevision(),
		"publishing the prepared target must not advance canonical state")
	err := p.state.SetMachineState(p.state.GetMachineState(), false)
	require.Error(t, err, "an unrelated writer must be fenced while the durable target is prepared")
	var retryable retryablePartitionCommitError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.Retryable())
	require.Equal(t, preCommitRevision, p.state.GetRevision())

	close(releaseCommit)
	result := <-committed
	require.NoError(t, result.err)
	require.Same(t, target, result.target)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.Equal(t, preCommitRevision+1, p.state.GetRevision())
}

func TestAdvisorWriteAheadCommitFailurePreservesActiveTarget(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	revision := p.state.GetRevision()
	active := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/active"}},
		},
		revision,
	)

	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/staging"}},
		},
		revision,
		func(target *advisorPostCommitTarget) error {
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))
			require.NotSame(t, active, target)
			require.Same(t, target, p.currentAdvisorPostCommitTarget())
			return errors.New("desired commit failed")
		},
	)
	require.ErrorContains(t, err, "desired commit failed")
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))
	require.Same(t, active, p.currentAdvisorPostCommitTarget())

	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.advisorPostCommitTarget = nil
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	require.Equal(t, "/active", p.currentAdvisorPostCommitTarget().response.ExtraEntries[0].CgroupPath)
}

func TestAdvisorWriteAheadRecoverySelectsMainRevisionAndCleansOtherSlot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		commitDesired bool
		wantPath      string
	}{
		{name: "old active survives crash before desired commit", wantPath: "/active"},
		{name: "staging is promoted after desired commit", commitDesired: true, wantPath: "/staging"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
			require.NoError(t, err)
			dir := t.TempDir()
			config := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: dir}
			firstState, err := state.NewCheckpointState(
				config, "cpu_plugin_state", "dynamic", topology, false,
				generateMachineStateFromPodEntries, metrics.DummyMetrics{})
			require.NoError(t, err)
			first := &DynamicPolicy{
				state:                          firstState,
				advisorPostCommitCheckpointDir: dir,
			}
			require.NoError(t, firstState.CommitAdvisorStateIfRevision(
				firstState.GetRevision(),
				firstState.GetPodEntries(),
				firstState.GetMachineState(),
				firstState.GetAllowSharedCoresOverlapReclaimedCores(),
				firstState.GetDisableDedicatedCoresOverlapReclaimedCores(),
				true,
			))
			revision := firstState.GetRevision()
			first.publishAdvisorPostCommitTarget(
				&advisorapi.ListAndWatchResponse{
					ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/active"}},
				},
				revision,
			)
			stagingTarget := cloneAdvisorPostCommitTarget(
				&advisorapi.ListAndWatchResponse{
					ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/staging"}},
				},
				revision+1,
			)
			stagingTarget.preCommitRevision = revision
			require.NoError(t, first.storeAdvisorPostCommitTarget(
				stagingTarget, first.advisorPostCommitStagingPath()))
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))

			if tc.commitDesired {
				require.NoError(t, firstState.CommitAdvisorStateIfRevision(
					revision,
					firstState.GetPodEntries(),
					firstState.GetMachineState(),
					firstState.GetAllowSharedCoresOverlapReclaimedCores(),
					firstState.GetDisableDedicatedCoresOverlapReclaimedCores(),
					true,
				))
			}

			restartedState, err := state.NewCheckpointState(
				config, "cpu_plugin_state", "dynamic", topology, false,
				generateMachineStateFromPodEntries, metrics.DummyMetrics{})
			require.NoError(t, err)
			restarted := &DynamicPolicy{
				state:                          restartedState,
				advisorPostCommitCheckpointDir: dir,
			}
			require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
			require.NotNil(t, restarted.currentAdvisorPostCommitTarget())
			require.Equal(t, tc.wantPath,
				restarted.currentAdvisorPostCommitTarget().response.ExtraEntries[0].CgroupPath)
			require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
			require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName+".staging"))
		})
	}
}

func TestAdvisorWALRecoveryUsesPreCommitRevisionOnlyForV2(t *testing.T) {
	t.Parallel()
	t.Run("V2 pre-commit revision is cleaned without replay", func(t *testing.T) {
		t.Parallel()
		p, cleanup := newReclaimReuseTestPolicy(t)
		defer cleanup()
		p.advisorPostCommitCheckpointDir = t.TempDir()
		preCommitRevision := p.state.GetRevision()
		postCommitRevision, err := nextAdvisorRevision(preCommitRevision)
		require.NoError(t, err)
		target := cloneAdvisorPostCommitTarget(
			&advisorapi.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/v2-uncommitted"}},
			},
			postCommitRevision,
		)
		target.preCommitRevision = preCommitRevision
		require.NoError(t, p.storeAdvisorPostCommitTarget(
			target, p.advisorPostCommitStagingPath()))

		require.NoError(t, p.restoreAdvisorPostCommitTarget())
		require.Nil(t, p.currentAdvisorPostCommitTarget())
		require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
		require.NoFileExists(t, p.advisorPostCommitStagingPath())
	})

	t.Run("V2 post-commit revision is replayed", func(t *testing.T) {
		t.Parallel()
		p, cleanup := newReclaimReuseTestPolicy(t)
		defer cleanup()
		p.advisorPostCommitCheckpointDir = t.TempDir()
		preCommitRevision := p.state.GetRevision()
		postCommitRevision, err := nextAdvisorRevision(preCommitRevision)
		require.NoError(t, err)
		target := cloneAdvisorPostCommitTarget(
			&advisorapi.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/v2-committed"}},
			},
			postCommitRevision,
		)
		target.preCommitRevision = preCommitRevision
		require.NoError(t, p.storeAdvisorPostCommitTarget(
			target, p.advisorPostCommitStagingPath()))
		require.NoError(t, p.state.CommitAdvisorStateIfRevision(
			preCommitRevision,
			p.state.GetPodEntries(),
			p.state.GetMachineState(),
			p.state.GetAllowSharedCoresOverlapReclaimedCores(),
			p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
			true,
		))

		require.NoError(t, p.restoreAdvisorPostCommitTarget())
		restored := p.currentAdvisorPostCommitTarget()
		require.NotNil(t, restored)
		require.Equal(t, preCommitRevision, restored.preCommitRevision)
		require.Equal(t, postCommitRevision, restored.revision)
		require.Equal(t, "/v2-committed", restored.response.ExtraEntries[0].CgroupPath)
		require.FileExists(t, p.advisorPostCommitCheckpointPath())
		require.NoFileExists(t, p.advisorPostCommitStagingPath())
	})

	t.Run("legacy exact revision is replayed without pre-commit metadata", func(t *testing.T) {
		t.Parallel()
		p, cleanup := newReclaimReuseTestPolicy(t)
		defer cleanup()
		p.advisorPostCommitCheckpointDir = t.TempDir()
		response := &advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/legacy"}},
		}
		responseBytes, err := proto.Marshal(response)
		require.NoError(t, err)
		data, err := json.Marshal(advisorPostCommitCheckpoint{
			Revision: p.state.GetRevision(),
			Response: responseBytes,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p.advisorPostCommitStagingPath(), data, 0o600))

		require.NoError(t, p.restoreAdvisorPostCommitTarget())
		restored := p.currentAdvisorPostCommitTarget()
		require.NotNil(t, restored)
		require.Equal(t, "/legacy", restored.response.ExtraEntries[0].CgroupPath)
		require.FileExists(t, p.advisorPostCommitCheckpointPath())
		require.NoFileExists(t, p.advisorPostCommitStagingPath())
	})

	t.Run("legacy mismatched revision is cleaned", func(t *testing.T) {
		t.Parallel()
		p, cleanup := newReclaimReuseTestPolicy(t)
		defer cleanup()
		p.advisorPostCommitCheckpointDir = t.TempDir()
		responseBytes, err := proto.Marshal(&advisorapi.ListAndWatchResponse{})
		require.NoError(t, err)
		data, err := json.Marshal(advisorPostCommitCheckpoint{
			Revision: p.state.GetRevision() + 1,
			Response: responseBytes,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p.advisorPostCommitStagingPath(), data, 0o600))

		require.NoError(t, p.restoreAdvisorPostCommitTarget())
		require.Nil(t, p.currentAdvisorPostCommitTarget())
		require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
		require.NoFileExists(t, p.advisorPostCommitStagingPath())
	})
}

func TestAdvisorV0PostCommitReplayCleansCheckpoint(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	p.installCPUStateWritePermit()
	revision := p.state.GetRevision()
	response := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/legacy-v0"}},
	}
	responseBytes, err := proto.Marshal(response)
	require.NoError(t, err)
	data, err := json.Marshal(advisorPostCommitCheckpoint{
		Revision: revision,
		Response: responseBytes,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.advisorPostCommitCheckpointPath(), data, 0o600))

	applied := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"count": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			return nil
		},
	}
	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Zero(t, restored.checkpointVersion)

	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), restored)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

func TestAdvisorWriteAheadTargetRealRestartAtCommitCrashPoints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		commitDesired bool
		publishTarget bool
		wantRecovered bool
	}{
		{name: "after target before desired commit", wantRecovered: false},
		{name: "after desired commit before memory publish", commitDesired: true, wantRecovered: true},
		{name: "after memory publish before reconcile", commitDesired: true, publishTarget: true, wantRecovered: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(2, 1, 1)
			require.NoError(t, err)
			dir := t.TempDir()
			config := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: dir}
			firstState, err := state.NewCheckpointState(
				config, "cpu_plugin_state", "dynamic", topology, false,
				generateMachineStateFromPodEntries, metrics.DummyMetrics{})
			require.NoError(t, err)
			first := &DynamicPolicy{
				state:                          firstState,
				advisorPostCommitCheckpointDir: dir,
			}
			postCommitRevision, err := nextAdvisorRevision(firstState.GetRevision())
			require.NoError(t, err)
			target := cloneAdvisorPostCommitTarget(
				&advisorapi.ListAndWatchResponse{ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/durable"}}},
				postCommitRevision)
			target.preCommitRevision = firstState.GetRevision()
			require.NoError(t, first.storeAdvisorPostCommitTarget(
				target, first.advisorPostCommitStagingPath()))
			require.NotNil(t, target)

			if tc.commitDesired {
				require.NoError(t, firstState.CommitAdvisorStateIfRevision(
					firstState.GetRevision(),
					firstState.GetPodEntries(),
					firstState.GetMachineState(),
					firstState.GetAllowSharedCoresOverlapReclaimedCores(),
					firstState.GetDisableDedicatedCoresOverlapReclaimedCores(),
					true))
				require.Equal(t, postCommitRevision, firstState.GetRevision())
			}
			if tc.publishTarget {
				first.publishAdvisorPostCommitTargetInMemory(target)
				require.Same(t, target, first.currentAdvisorPostCommitTarget())
			}

			restartedState, err := state.NewCheckpointState(
				config, "cpu_plugin_state", "dynamic", topology, false,
				generateMachineStateFromPodEntries, metrics.DummyMetrics{})
			require.NoError(t, err)
			restarted := &DynamicPolicy{
				state:                          restartedState,
				advisorPostCommitCheckpointDir: dir,
			}
			require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
			if tc.wantRecovered {
				require.NotNil(t, restarted.currentAdvisorPostCommitTarget())
				require.Equal(t, postCommitRevision, restarted.currentAdvisorPostCommitTarget().revision)
				require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
			} else {
				require.Nil(t, restarted.currentAdvisorPostCommitTarget())
				require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
			}
		})
	}
}

func TestAdvisorPostCommitCheckpointStopStartRequeuesPendingTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/pending"}}},
		p.state.GetRevision())

	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryDirty = false
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.NoError(t, p.prepareAdvisorPostCommitTargetOnStart())

	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName),
		"Stop/Start must retain the pending checkpoint")
}

func TestAdvisorPostCommitCheckpointRevisionMismatchIsCleaned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	p.publishAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.state.SetAllowSharedCoresOverlapReclaimedCores(
		!p.state.GetAllowSharedCoresOverlapReclaimedCores(), false)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.advisorPostCommitTarget = nil
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

func TestAdvisorPostCommitInitialStoreFailureCleansStagingAndFence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	stagingPath := p.advisorPostCommitStagingPath()
	require.NoError(t, os.Mkdir(stagingPath, 0o750))
	commitCalled := false

	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{},
		p.state.GetRevision(),
		func(*advisorPostCommitTarget) error {
			commitCalled = true
			return nil
		},
	)

	require.ErrorContains(t, err, "persist advisor post-commit target")
	require.False(t, commitCalled)
	require.NoFileExists(t, stagingPath)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoError(t, p.ensureCPUStateWriterAllowed(p.state.GetRevision(), "test", nil))
}

func TestAdvisorPostCommitInitialStoreCleanupFailureRetainsFence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	stagingPath := p.advisorPostCommitStagingPath()
	require.NoError(t, os.Mkdir(stagingPath, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(stagingPath, "blocker"), []byte("x"), 0o600))

	_, err := p.commitAdvisorResponseWithWriteAhead(
		&advisorapi.ListAndWatchResponse{},
		p.state.GetRevision(),
		func(*advisorPostCommitTarget) error {
			return nil
		},
	)

	require.ErrorContains(t, err, "remove incomplete target while retaining writer fence")
	target := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	require.True(t, target.prepared)
	require.Error(t, p.ensureCPUStateWriterAllowed(p.state.GetRevision(), "test", nil))
}

func TestAdvisorPostCommitTargetStatusPreservesWALOnUnrelatedRevision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	stagingPath := filepath.Join(dir, advisorPostCommitCheckpointName+".staging")
	require.NoError(t, p.storeAdvisorPostCommitTarget(target, stagingPath))
	p.state.SetAllowSharedCoresOverlapReclaimedCores(
		!p.state.GetAllowSharedCoresOverlapReclaimedCores(), false)

	current, err := p.advisorPostCommitTargetStatus(target)
	require.False(t, current)
	require.Error(t, err)
	p.Lock()
	err = p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.Error(t, err)
	var retryable retryablePartitionCommitError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.Retryable())

	require.Same(t, target, p.currentAdvisorPostCommitTarget(),
		"an unrelated revision must not silently discard the pending target")
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.FileExists(t, stagingPath)
}

func TestAdvisorPostCommitExactReconcileMayCommitAdjustmentOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	p.installCPUStateWritePermit()
	setReclaimPoolCPUSet(t, p, machine.NewCPUSet(0, 1))
	revision := p.state.GetRevision()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, revision)
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"override": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			in.CommitOverride.ReclaimEffective = machine.NewCPUSet(2, 3)
			in.CommitOverride.Source = "exact advisor reconcile"
			return nil
		},
	}

	p.Lock()
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()

	require.NoError(t, err)
	require.Equal(t, revision+1, p.state.GetRevision())
	require.True(t, p.state.GetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
	).AllocationResult.Equals(machine.NewCPUSet(2, 3)))
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
}

func TestAdvisorPostCommitReconcileCleansActiveAndStagingCheckpoints(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	stagingPath := filepath.Join(dir, advisorPostCommitCheckpointName+".staging")
	require.NoError(t, p.storeAdvisorPostCommitTarget(target, stagingPath))

	p.Lock()
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.NoError(t, err)

	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.NoFileExists(t, stagingPath)
}

func TestAdvisorPostCommitCleanupRetryDoesNotRepeatSideEffects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	stagingPath := p.advisorPostCommitStagingPath()
	require.NoError(t, os.Mkdir(stagingPath, 0o750))
	blocker := filepath.Join(stagingPath, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	applied := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"count": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			return nil
		},
	}

	p.Lock()
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), target)
	p.Unlock()
	require.Error(t, err)
	require.Equal(t, 1, applied)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryDirty = true
	p.adjustment.cpuSetAdjustmentRetryReasons = map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
		cpusetutil.RetryReasonApplyFailed: {},
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	require.NoError(t, os.Remove(blocker))
	p.Lock()
	err = p.retryLatestCPUSetAdjustment(
		context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
	p.Unlock()
	require.NoError(t, err)
	require.Equal(t, 1, applied, "cleanup retry must not repeat post-commit side effects")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.False(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Nil(t, p.adjustment.cpuSetAdjustmentRetryReasons)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func TestAdvisorPostCommitAppliedMarkerSkipsSideEffectsAfterRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	require.NoError(t, p.state.SetMachineState(p.state.GetMachineState(), false))
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	target.applied = true
	require.NoError(t, p.storeAdvisorPostCommitTarget(
		target, p.advisorPostCommitCheckpointPath()))
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.advisorPostCommitTarget = nil
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.True(t, restored.applied)
	applied := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"count": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			return nil
		},
	}

	p.Lock()
	err := p.reconcileAdvisorPostCommitTarget(context.Background(), restored)
	p.Unlock()

	require.NoError(t, err)
	require.Zero(t, applied, "restored applied target must only clean its WAL")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestAdvisorPostCommitCheckpointCorruptionFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = dir
	checkpointPath := filepath.Join(dir, advisorPostCommitCheckpointName)
	require.NoError(t, os.WriteFile(checkpointPath, []byte("{broken"), 0o600))

	err := p.restoreAdvisorPostCommitTarget()

	require.ErrorContains(t, err, "corrupted active advisor post-commit checkpoint")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, checkpointPath,
		"a corrupted checkpoint for the committed revision must remain for operator recovery")
}

func TestAdvisorPostCommitProgressIdentityPhaseGenerationAndNotification(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{}
	target := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, 7)

	p.publishAdvisorPostCommitTargetInMemory(target)
	published := p.currentAdvisorPostCommitProgress()
	require.Same(t, target, published.target)
	require.Equal(t, advisorPostCommitPhasePublished, published.phase)
	require.NotZero(t, published.generation)
	require.False(t, published.createdAt.IsZero())
	require.False(t, published.lastProgressAt.IsZero())

	_, changed := p.currentAdvisorPostCommitTargetAndChange()
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("progress update did not notify waiters")
	}

	applying := p.currentAdvisorPostCommitProgress()
	require.Same(t, target, applying.target)
	require.Equal(t, advisorPostCommitPhasePhysicalApply, applying.phase)
	require.Greater(t, applying.generation, published.generation)
	require.Equal(t, published.createdAt, applying.createdAt)
	require.False(t, applying.lastProgressAt.Before(published.lastProgressAt))

	replacedTargetChanged := applying.changed
	replacement := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, 8)
	p.publishAdvisorPostCommitTargetInMemory(replacement)
	select {
	case <-replacedTargetChanged:
	case <-time.After(time.Second):
		t.Fatal("replacing a non-nil target did not close the previous target's changed channel")
	}
	replaced := p.currentAdvisorPostCommitProgress()
	require.Same(t, replacement, replaced.target)
	require.NotEqual(t, applying.target, replaced.target)
	require.Equal(t, advisorPostCommitPhasePublished, replaced.phase)
}

func TestAdvisorPostCommitProgressStuckOnlyForAgedPhysicalApply(t *testing.T) {
	t.Parallel()
	p := &DynamicPolicy{config: configComponent{conf: config.NewConfiguration()}}
	threshold := advisorPostCommitStuckThreshold(p.config.conf)
	for _, tc := range []struct {
		name        string
		phase       advisorPostCommitPhase
		progressAge time.Duration
		wantStuck   bool
	}{
		{
			name:        "aged prepared",
			phase:       advisorPostCommitPhasePrepared,
			progressAge: threshold + time.Second,
		},
		{
			name:        "aged published",
			phase:       advisorPostCommitPhasePublished,
			progressAge: threshold + time.Second,
		},
		{
			name:        "fresh physical apply",
			phase:       advisorPostCommitPhasePhysicalApply,
			progressAge: threshold - time.Second,
		},
		{
			name:        "aged physical apply",
			phase:       advisorPostCommitPhasePhysicalApply,
			progressAge: threshold + time.Second,
			wantStuck:   true,
		},
		{
			name:        "aged applied marker",
			phase:       advisorPostCommitPhaseAppliedMarker,
			progressAge: threshold + time.Second,
		},
		{
			name:        "aged cleanup",
			phase:       advisorPostCommitPhaseCleanup,
			progressAge: threshold + time.Second,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &advisorPostCommitTarget{
				phase:          tc.phase,
				lastProgressAt: time.Now().Add(-tc.progressAge),
			}
			require.Equal(t, tc.wantStuck, p.advisorPostCommitProgressStuck(advisorPostCommitProgress{
				target:         target,
				phase:          target.phase,
				lastProgressAt: target.lastProgressAt,
			}))
		})
	}
}

func recoverStuckAdvisorPostCommitTargetForTest(
	p *DynamicPolicy,
	target *advisorPostCommitTarget,
) (bool, error) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	target.lastProgressAt = time.Now().Add(-advisorPostCommitStuckThreshold(p.config.conf) - time.Second)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	p.Lock()
	defer p.Unlock()
	return p.recoverStuckAdvisorPostCommitTargetLocked(context.Background(), target)
}

func TestRecoverStuckAdvisorPostCommitTargetReleasesFenceButRetainsDurableReplay(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()

	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	require.NoError(t, p.storeAdvisorPostCommitTarget(target, p.advisorPostCommitStagingPath()))
	targetPermit := p.newAdvisorStateWritePermit(target)
	otherTarget := cloneAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, target.revision+1)
	otherPermit := p.newAdvisorStateWritePermit(otherTarget)

	released, err := recoverStuckAdvisorPostCommitTargetForTest(p, target)
	require.NoError(t, err)
	require.True(t, released)

	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.FileExists(t, p.advisorPostCommitStagingPath())
	require.Equal(t, advisorPostCommitPhaseCanonicalReconcile,
		p.currentAdvisorPostCommitProgress().phase)
	fenceTarget, _ := p.currentAdvisorPostCommitTargetAndChange()
	require.Nil(t, fenceTarget)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	_, targetPermitExists := p.adjustment.advisorStateWritePermits[targetPermit]
	require.False(t, targetPermitExists)
	require.Same(t, otherTarget, p.adjustment.advisorStateWritePermits[otherPermit])
	require.True(t, p.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	p.Lock()
	err = p.retryLatestCPUSetAdjustment(
		context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
	p.Unlock()
	require.NoError(t, err)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

func TestRecoverStuckAdvisorPostCommitTargetRetainsWALWhenPayloadReplayFails(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()

	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)

	p.applyHeadroomHook = func(_ *advisorapi.ListAndWatchResponse) error {
		return nil
	}
	p.applyCgroupConfigsHook = func(_ *advisorapi.ListAndWatchResponse) error {
		return errors.New("cgroup payload unavailable")
	}

	released, err := recoverStuckAdvisorPostCommitTargetForTest(p, target)

	require.ErrorContains(t, err, "cgroup payload unavailable")
	require.False(t, released)
	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestRecoverStuckAdvisorPostCommitTargetRestoresDurableReplayAfterRestart(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	dir := t.TempDir()
	first, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.NoError(t, first.state.CommitAdvisorState(
		first.state.GetPodEntries(), first.state.GetMachineState(), false, false, true))
	require.NotZero(t, first.state.GetRevision())
	target := first.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, first.state.GetRevision())
	first.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	released, err := recoverStuckAdvisorPostCommitTargetForTest(first, target)
	require.NoError(t, err)
	require.True(t, released)
	require.FileExists(t, first.advisorPostCommitCheckpointPath())
	require.NoError(t, first.state.CommitAdvisorState(
		first.state.GetPodEntries(), first.state.GetMachineState(), false, false, true))
	require.Greater(t, first.state.GetRevision(), target.revision)

	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.NoError(t, restarted.prepareAdvisorPostCommitTargetOnStart())

	require.Nil(t, restarted.currentAdvisorPostCommitTarget(),
		"a newer canonical revision supersedes the handed-off target")
	require.NoFileExists(t, restarted.advisorPostCommitCheckpointPath())
	restarted.adjustment.cpuSetAdjustmentRetryMu.Lock()
	require.True(t, restarted.adjustment.cpuSetAdjustmentRetryDirty)
	require.Contains(t, restarted.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	restarted.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func TestRetrySupersededAdvisorPostCommitTargetReconcilesLatestCanonicalState(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	released, err := recoverStuckAdvisorPostCommitTargetForTest(p, target)
	require.NoError(t, err)
	require.True(t, released)
	require.NoError(t, p.state.CommitAdvisorState(
		p.state.GetPodEntries(), p.state.GetMachineState(), false, false, true))
	require.Greater(t, p.state.GetRevision(), target.revision)
	reconciled := 0
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"latest-canonical": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			reconciled++
			return nil
		},
	}

	p.Lock()
	err = p.retryLatestCPUSetAdjustment(
		context.Background(), cpusetutil.CPUSetAdjustmentModePeriodic)
	p.Unlock()

	require.NoError(t, err)
	require.Equal(t, 1, reconciled)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
}

func TestRecoverStuckAdvisorPostCommitTargetWhileStoppingKeepsWAL(t *testing.T) {
	t.Parallel()
	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	target := p.publishAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{}, p.state.GetRevision())
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	released, err := recoverStuckAdvisorPostCommitTargetForTest(p, target)
	require.NoError(t, err)
	require.True(t, released)

	require.Same(t, target, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.Equal(t, advisorPostCommitPhaseCanonicalReconcile,
		p.currentAdvisorPostCommitProgress().phase)
}

func TestRecoveredAdvisorPostCommitTargetStartsFreshProgressClock(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	dir := t.TempDir()
	first, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	first.state.SetPodEntries(state.PodEntries{}, true)
	target := cloneAdvisorPostCommitTarget(&advisorapi.ListAndWatchResponse{}, first.state.GetRevision())
	require.NoError(t, first.storeAdvisorPostCommitTarget(target, first.advisorPostCommitCheckpointPath()))

	before := time.Now()
	restarted, err := getTestDynamicPolicyWithoutInitialization(topology, dir)
	require.NoError(t, err)
	require.NoError(t, restarted.restoreAdvisorPostCommitTarget())
	progress := restarted.currentAdvisorPostCommitProgress()
	require.NotNil(t, progress.target)
	require.Equal(t, advisorPostCommitPhasePublished, progress.phase)
	require.NotZero(t, progress.generation)
	require.False(t, progress.createdAt.Before(before))
	require.False(t, progress.lastProgressAt.Before(before))
}

func TestCgroupCreateRetriesOnlyDeferredLeafDirtyAdjustment(t *testing.T) {
	t.Parallel()

	t.Run("deferred leaf schedules retry", func(t *testing.T) {
		t.Parallel()
		attempted := make(chan struct{}, 1)
		p := &DynamicPolicy{adjustment: adjustmentComponent{
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					if in.Mode == cpusetutil.CPUSetAdjustmentModeRetry {
						attempted <- struct{}{}
					}
					return nil
				},
			},
			cpuSetAdjustmentRetryDirty: true,
			cpuSetAdjustmentRetryReasons: map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
				cpusetutil.RetryReasonDeferredLeaf: {},
			},
		}}

		p.handleCgroupCreateEvent()
		select {
		case <-attempted:
		case <-time.After(time.Second):
			t.Fatal("deferred leaf dirty adjustment was not retried after cgroup create")
		}
	})

	t.Run("unrelated dirty reason does not schedule retry", func(t *testing.T) {
		t.Parallel()
		attempted := make(chan struct{}, 1)
		p := &DynamicPolicy{adjustment: adjustmentComponent{
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					if in.Mode == cpusetutil.CPUSetAdjustmentModeRetry {
						attempted <- struct{}{}
					}
					return nil
				},
			},
			cpuSetAdjustmentRetryDirty: true,
			cpuSetAdjustmentRetryReasons: map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
				cpusetutil.RetryReasonStaleState: {},
			},
		}}

		p.handleCgroupCreateEvent()
		select {
		case <-attempted:
			t.Fatal("cgroup create retried a non-deferred dirty adjustment")
		case <-time.After(100 * time.Millisecond):
		}
	})
}

func TestDynamicPolicyConsumesRegisteredCacheSyncEvents(t *testing.T) {
	t.Parallel()

	attempted := make(chan struct{}, 1)
	fetcher := &cacheSyncRegistrarPodFetcher{
		PodFetcherStub: &podmeta.PodFetcherStub{},
		events:         make(chan podmeta.KubeletPodCacheSyncEvent, 1),
		unregistered:   make(chan struct{}),
	}
	stopCh := make(chan struct{})
	p := &DynamicPolicy{
		stopCh: stopCh,

		meta: metaComponent{metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
		}}, adjustment: adjustmentComponent{
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					if in.Mode == cpusetutil.CPUSetAdjustmentModeRetry {
						attempted <- struct{}{}
					}
					return nil
				},
			},
			cpuSetAdjustmentRetryDirty: true,
			cpuSetAdjustmentRetryReasons: map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}{
				cpusetutil.RetryReasonDeferredLeaf: {},
			},
		},
	}
	p.startKubeletPodCacheSyncDrivenCPUSetRetry()
	fetcher.events <- podmeta.KubeletPodCacheSyncEvent{
		CgroupCreated: true,
		Revision:      1,
		SyncedAt:      time.Now(),
	}
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("cache sync event did not trigger deferred CPUSet retry")
	}
	close(stopCh)
	select {
	case <-fetcher.unregistered:
	case <-time.After(time.Second):
		t.Fatal("cache sync listener was not unregistered on stop")
	}
}

func TestStopCancelsCPUSetAdjustmentRetryWorker(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	stopCh := make(chan struct{})
	p := &DynamicPolicy{
		started: true,
		stopCh:  stopCh, adjustment: adjustmentComponent{
			cpuSetAdjustmentRetryStopCh: stopCh,
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"wait-for-stop": func(ctx context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					select {
					case <-started:
					default:
						close(started)
					}
					<-ctx.Done()
					return ctx.Err()
				},
			},
		},
	}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("retry worker did not start")
	}

	stopped := make(chan error, 1)
	go func() {
		stopped <- p.Stop()
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() did not cancel and wait for retry worker")
	}
}

func TestScheduleCPUSetAdjustmentRetryNoGoroutineLeak(t *testing.T) {
	t.Parallel()

	stopCh := make(chan struct{})
	attempted := make(chan struct{}, 1)
	p := &DynamicPolicy{
		stopCh: stopCh, adjustment: adjustmentComponent{
			cpuSetAdjustmentRetryStopCh: stopCh,
			cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"noop": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					select {
					case attempted <- struct{}{}:
					default:
					}
					return nil
				},
			},
		},
	}

	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)

	select {
	case <-attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("retry attempt never ran")
	}

	// The retry worker (func1) must wind down on its own after a successful
	// attempt; the per-attempt stopCh watcher (func2) must exit on ctx.Done().
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()

	close(stopCh)
	time.Sleep(200 * time.Millisecond)

	for _, marker := range []string{
		"dynamicpolicy.(*DynamicPolicy).scheduleCPUSetAdjustmentRetry.func1",
		"dynamicpolicy.(*DynamicPolicy).scheduleCPUSetAdjustmentRetry.func2",
	} {
		require.Eventually(t, func() bool {
			return goroutineFramesContaining(t, marker) == 0
		}, 2*time.Second, 30*time.Millisecond, "leaked retry goroutine frame: %s", marker)
	}
}

func TestRunCPUSetAdjustmentHandlersDoesNotHoldPolicyLockDuringExecution(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"blocking-io": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			close(started)
			<-release
			return nil
		},
	}}}

	runDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		runDone <- p.runCPUSetAdjustmentHandlers(context.Background())
	}()
	<-started

	lockAcquired := make(chan struct{})
	go func() {
		p.Lock()
		close(lockAcquired)
		p.Unlock()
	}()

	select {
	case <-lockAcquired:
	case <-time.After(time.Second):
		close(release)
		<-runDone
		t.Fatal("DynamicPolicy lock remained held while adjustment handler executed")
	}
	close(release)
	if err := <-runDone; err != nil {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v", err)
	}
}

func TestRunCPUSetAdjustmentHandlersFenceRejectsStaleStateBeforeRetry(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	committed := make(chan bool, 2)
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 4)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error = %v", err)
	}
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	if err != nil {
		t.Fatalf("getTestDynamicPolicyWithInitialization() error = %v", err)
	}
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"blocking-io": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Generation == 1 {
				close(started)
				<-release
			}
			committed <- in.CommitIfGenerationCurrent(in.Generation, func() {})
			return nil
		},
	}

	runDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		runDone <- p.runCPUSetAdjustmentHandlers(context.Background())
	}()
	<-started

	p.Lock()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	p.Unlock()
	close(release)

	if err := <-runDone; err != nil {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v", err)
	}
	if <-committed {
		t.Fatal("generation fence accepted a result calculated from stale policy state")
	}
	if !<-committed {
		t.Fatal("latest generation did not converge after rejecting stale policy state")
	}
}

func TestRunCPUSetAdjustmentHandlersRetriesLatestStateAfterFenceRejection(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	committedGenerations := make(chan uint64, 1)
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 4)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error = %v", err)
	}
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	if err != nil {
		t.Fatalf("getTestDynamicPolicyWithInitialization() error = %v", err)
	}
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"generation-aware": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Generation == 1 {
				close(firstStarted)
				<-firstRelease
			}
			if in.CommitIfGenerationCurrent(in.Generation, func() {}) {
				committedGenerations <- in.Generation
			}
			return nil
		},
	}

	runDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		runDone <- p.runCPUSetAdjustmentHandlers(context.Background())
	}()
	<-firstStarted
	p.Lock()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	p.Unlock()
	close(firstRelease)

	if err := <-runDone; err != nil {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v", err)
	}
	select {
	case generation := <-committedGenerations:
		if generation != 2 {
			t.Fatalf("committed generation = %d, want latest generation 2", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("latest policy state was not scheduled after stale generation rejection")
	}
}

func TestRunCPUSetAdjustmentHandlersSchedulesLatestStateAfterCanceledStaleRound(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	latestCommitted := make(chan struct{})
	topology, err := machine.GenerateDummyCPUTopology(8, 1, 4)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error = %v", err)
	}
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	if err != nil {
		t.Fatalf("getTestDynamicPolicyWithInitialization() error = %v", err)
	}
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"generation-aware": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Generation == 1 {
				close(firstStarted)
				<-firstRelease
			}
			if in.CommitIfGenerationCurrent(in.Generation, func() {}) && in.Generation > 1 {
				close(latestCommitted)
			}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		runDone <- p.runCPUSetAdjustmentHandlers(ctx)
	}()
	<-firstStarted
	p.Lock()
	p.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	p.Unlock()
	cancel()
	close(firstRelease)

	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("runCPUSetAdjustmentHandlers() error = %v, want context.Canceled", err)
	}
	select {
	case <-latestCommitted:
	case <-time.After(time.Second):
		t.Fatal("latest policy state was not scheduled after canceled stale round")
	}
}

func TestRunCPUSetAdjustmentHandlersSerializesLockFreeRounds(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	p := &DynamicPolicy{}
	p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"blocking-io": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if in.Generation == 1 {
				close(firstStarted)
				<-firstRelease
			} else {
				close(secondStarted)
			}
			return nil
		},
	}

	run := func(done chan<- error) {
		p.Lock()
		defer p.Unlock()
		done <- p.runCPUSetAdjustmentHandlers(context.Background())
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go run(firstDone)
	<-firstStarted
	go run(secondDone)

	select {
	case <-secondStarted:
		close(firstRelease)
		t.Fatal("second adjustment round executed concurrently with the first")
	case <-time.After(100 * time.Millisecond):
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second run error = %v", err)
	}
}

func TestRunCPUSetAdjustmentHandlersCancelsWhileWaitingForExecutionLock(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	var calls int
	p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
		"blocking-io": func(_ context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			calls++
			if calls == 1 {
				close(firstStarted)
				<-firstRelease
			}
			return nil
		},
	}}}

	firstDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		firstDone <- p.runCPUSetAdjustmentHandlers(context.Background())
	}()
	<-firstStarted

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		p.Lock()
		defer p.Unlock()
		secondDone <- p.runCPUSetAdjustmentHandlers(ctx, cpusetutil.CPUSetAdjustmentModeAdmission)
	}()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued admission error = %v, want context deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("queued admission ignored its deadline while waiting for the execution lock")
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want canceled queued admission not to execute", calls)
	}

	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run error = %v", err)
	}
}

func TestQueuedRetryRequestsAlwaysProduceOneTrailingLatestStateRound(t *testing.T) {
	t.Parallel()
	for _, reason := range []cpusetutil.CPUSetAdjustmentRetryReason{
		cpusetutil.RetryReasonDeferredLeaf,
		cpusetutil.RetryReasonStaleState,
	} {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			firstStarted := make(chan struct{})
			firstRelease := make(chan struct{})
			rounds := make(chan struct{}, 3)
			calls := 0
			p := &DynamicPolicy{adjustment: adjustmentComponent{cpuSetAdjustmentHandlers: map[string]cpusetutil.CPUSetAdjustmentHandler{
				"retry": func(_ context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
					if in.Mode != cpusetutil.CPUSetAdjustmentModeRetry {
						return nil
					}
					calls++
					rounds <- struct{}{}
					if calls == 1 {
						close(firstStarted)
						<-firstRelease
					}
					return nil
				},
			}}}

			p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
			<-firstStarted
			p.scheduleCPUSetAdjustmentRetry(reason)
			close(firstRelease)

			for i := 0; i < 2; i++ {
				select {
				case <-rounds:
				case <-time.After(time.Second):
					t.Fatalf("retry rounds = %d, want initial plus one trailing latest-state round", i)
				}
			}
			select {
			case <-rounds:
				t.Fatal("queued retry requests produced more than one trailing round")
			case <-time.After(100 * time.Millisecond):
			}
			deadline := time.Now().Add(time.Second)
			for {
				p.adjustment.cpuSetAdjustmentRetryMu.Lock()
				queued := p.adjustment.cpuSetAdjustmentRetryQueued
				p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
				if !queued {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("retry worker did not finish")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
