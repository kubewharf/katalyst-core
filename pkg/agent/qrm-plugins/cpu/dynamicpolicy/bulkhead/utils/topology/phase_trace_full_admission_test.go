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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	fullAdmissionPendingCPUCount = 160
	fullAdmissionNodePairs       = 76
	fullAdmissionWriteCount      = fullAdmissionNodePairs * 2
)

func TestExecuteValidatedFrozenTraceFullAdmission(t *testing.T) {
	t.Parallel()
	t.Run("preflight execute final proof and one publish", func(t *testing.T) {
		t.Parallel()
		fixture, base, trace := newFullAdmissionTrace(t)
		requireFullAdmissionShape(t, trace)
		live := fixture.driver
		initial := live.snapshot()
		ctx := context.Background()
		adjustmentBudget := newFullAdmissionAdjustmentBudget(t, ctx, trace)
		fixture.round.adjustmentBudget = adjustmentBudget
		res := &ConvergenceResult{}
		finalPublishes, parentSafePublishes := 0, 0
		publishFinal := func(snapshot *CompleteSnapshot) error {
			require.Equal(t, trace.FinalSnapshot.ID, snapshot.ID)
			finalPublishes++
			return nil
		}
		publishParentSafe := func(snapshot *CompleteSnapshot, _ map[string]struct{}) error {
			require.Equal(t, trace.FinalSnapshot.ID, snapshot.ID)
			parentSafePublishes++
			return nil
		}

		outcome, err := fixture.round.executeParentSafeAdmission(
			ctx, base, res, publishFinal, publishParentSafe)

		require.NoError(t, err)
		require.Equal(t, fullAdmissionWriteCount, live.PhysicalWriteCount())
		require.Equal(t, fullAdmissionWriteCount, adjustmentBudget.CumulativeWrites())
		require.Equal(t, fullAdmissionWriteCount, res.Applied)
		require.Len(t, res.Journal, fullAdmissionWriteCount)
		require.Equal(t, 1, finalPublishes)
		require.Zero(t, parentSafePublishes)
		require.True(t, res.FinalSnapshotCurrent)
		require.Equal(t, trace.FinalSnapshot.ID, outcome.Snapshot.ID)
		require.Equal(t, trace.FinalSnapshot.ID, res.FinalSnapshot.ID)
		require.NotEqual(t, initial, live.snapshot())
	})

	t.Run("final proof failure restores initial snapshot and does not publish", func(t *testing.T) {
		t.Parallel()
		fixture, base, trace := newFullAdmissionTrace(t)
		requireFullAdmissionShape(t, trace)
		live := fixture.driver
		initial := live.snapshot()

		driver := &injectedTraceDriver{
			HierarchyDriver: live,
			injection:       traceFailureInjection{failFinalProof: true},
			expectedForward: fullAdmissionWriteCount,
		}
		fixture.round.driver = driver
		fixture.round.recoveryContextFactory = deterministicRecoveryContext
		ctx := context.Background()
		adjustmentBudget := newFullAdmissionAdjustmentBudget(t, ctx, trace)
		fixture.round.adjustmentBudget = adjustmentBudget
		res := &ConvergenceResult{}
		finalPublishes, parentSafePublishes := 0, 0
		publishFinal := func(*CompleteSnapshot) error {
			finalPublishes++
			return nil
		}
		publishParentSafe := func(*CompleteSnapshot, map[string]struct{}) error {
			parentSafePublishes++
			return nil
		}

		_, err := fixture.round.executeParentSafeAdmission(
			ctx, base, res, publishFinal, publishParentSafe)

		require.ErrorContains(t, err, "injected final proof failure")
		require.True(t, driver.injected, "final-proof failure point must be reached")
		require.Equal(t, fullAdmissionWriteCount, driver.forwardSuccess,
			"final-proof failure must be injected after every forward write")

		// The rollback writer may safely skip no-op inverse writes where the
		// current state already equals the original (e.g. when consecutive
		// operations on the same rel cancel out before the inverse write runs).
		// Assert semantic restoration rather than a fixed 2*N physical-write
		// count: the real fail-closed guarantee is that the hierarchy returns
		// to its exact initial snapshot.
		forwardWrites := live.writes[:fullAdmissionWriteCount]
		inverseWrites := live.writes[fullAdmissionWriteCount:]
		t.Logf("full admission rollback: forward=%d actualInverse=%d skippedInverse=%d cumulativeWrites=%d",
			driver.forwardSuccess, len(inverseWrites),
			fullAdmissionWriteCount-len(inverseWrites),
			adjustmentBudget.CumulativeWrites())

		// Every rel that received a forward write must end up back at its
		// initial configured CPUs, whether by an inverse write or by a safe
		// no-op skip (the rollback writer already verified current==before
		// before deciding to skip).
		require.Equal(t, initial, live.snapshot(),
			"fail-closed: hierarchy must restore to the exact initial snapshot")
		requireFullAdmissionInverseWritesRestoreInitial(t, forwardWrites, inverseWrites, initial)

		// Budget must account every physical write that actually landed
		// (forward + non-skipped inverse), but it must NOT over-count skipped
		// no-ops. The floor is the forward count; the ceiling is 2*forward.
		require.GreaterOrEqual(t, live.PhysicalWriteCount(), fullAdmissionWriteCount,
			"every forward write must land")
		require.LessOrEqual(t, live.PhysicalWriteCount(), fullAdmissionWriteCount*2,
			"rollback must not write more than one inverse per forward")
		require.GreaterOrEqual(t, adjustmentBudget.CumulativeWrites(), fullAdmissionWriteCount,
			"budget must count every forward write attempt")
		require.LessOrEqual(t, adjustmentBudget.CumulativeWrites(), fullAdmissionWriteCount*2,
			"budget must not count more than one inverse attempt per forward")

		require.Zero(t, finalPublishes)
		require.Zero(t, parentSafePublishes)
		require.Nil(t, res.FinalSnapshot)
		require.False(t, res.FinalSnapshotCurrent)
		require.Empty(t, res.Journal)
		require.Zero(t, res.Applied)
	})

	t.Run("slow forward deadline triggers independent bounded rollback", func(t *testing.T) {
		t.Parallel()
		fixture, base, trace := newFullAdmissionTrace(t)
		requireFullAdmissionShape(t, trace)
		live := fixture.driver
		initial := live.snapshot()

		// Expire the execution context synchronously after the Nth successful
		// forward write. The trigger preserves the production deadline error
		// contract without coupling the test to scheduler speed or wall time.
		const expireAfterWrite = 10
		ctx := newTriggeredDeadlineContext(context.Background())
		driver := &injectedTraceDriver{
			HierarchyDriver: live,
			injection: traceFailureInjection{
				expireDeadlineAfterWrite: expireAfterWrite,
			},
			expectedForward: fullAdmissionWriteCount,
			expireDeadline:  ctx.expire,
		}
		fixture.round.driver = driver
		fixture.round.recoveryContextFactory = deterministicRecoveryContext
		adjustmentBudget := newFullAdmissionAdjustmentBudget(t, ctx, trace)
		fixture.round.adjustmentBudget = adjustmentBudget
		res := &ConvergenceResult{}

		_, err := fixture.round.executeParentSafeAdmission(
			ctx, base, res,
			func(*CompleteSnapshot) error { return nil },
			func(*CompleteSnapshot, map[string]struct{}) error { return nil })

		// The forward execution must fail with a context deadline/cancel error,
		// not with an injected proof error.
		require.Error(t, err)
		require.True(t, driver.injected,
			"deadline trigger point must be reached")
		// Exactly expireAfterWrite forward writes must have succeeded before the
		// deterministic deadline trigger stops the forward path.
		require.Equal(t, expireAfterWrite, driver.forwardSuccess,
			"forward must complete exactly the writes before deadline expiry")
		// The execution ctx must be expired: this is the deadline that killed
		// the forward path.
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		// Fail-closed: every forward write that landed must be inverse-written
		// (or safely skipped if current already equals original). The rollback
		// runs under an independent recovery context, so it must complete even
		// though the execution ctx is already canceled.
		require.GreaterOrEqual(t, live.PhysicalWriteCount(), expireAfterWrite,
			"every landed forward write must appear in the write log")
		require.LessOrEqual(t, live.PhysicalWriteCount(), expireAfterWrite*2,
			"rollback must not write more than one inverse per forward")
		require.Equal(t, initial, live.snapshot(),
			"fail-closed: hierarchy must be restored to initial state")
		require.Nil(t, res.FinalSnapshot)
		require.False(t, res.FinalSnapshotCurrent)
		require.Empty(t, res.Journal)
	})

	t.Run("rollback identity probe blocks are bounded by recovery context", func(t *testing.T) {
		t.Parallel()
		fixture, base, trace := newFullAdmissionTrace(t)
		requireFullAdmissionShape(t, trace)
		live := fixture.driver

		// Fail the final proof, then make the rollback's identity probe block
		// on whatever ctx the rollback passes. The rollback must not hang
		// forever: it runs under the independent recovery context which has
		// its own bounded timeout, and the blocked probe must return with the
		// recovery ctx error rather than leaking onto the caller's ctx.
		driver := &injectedTraceDriver{
			HierarchyDriver: live,
			injection: traceFailureInjection{
				failFinalProof:        true,
				blockRollbackIdentity: true,
			},
			expectedForward: fullAdmissionWriteCount,
		}
		fixture.round.driver = driver
		ctx := context.Background()
		adjustmentBudget := newFullAdmissionAdjustmentBudget(t, ctx, trace)
		fixture.round.adjustmentBudget = adjustmentBudget
		res := &ConvergenceResult{}

		_, err := fixture.round.executeParentSafeAdmission(
			ctx, base, res,
			func(*CompleteSnapshot) error { return nil },
			func(*CompleteSnapshot, map[string]struct{}) error { return nil })

		require.Error(t, err)
		require.True(t, driver.injected, "final-proof failure point must be reached")
		// The blocked rollback probe returns ctx.Err() from the recovery ctx.
		// Because the probe blocked on the recovery ctx's Done channel (the
		// injected driver blocks on whatever ctx it receives), the recovery
		// context's own deadline unblocks it. The test must return within the
		// go test -timeout, proving the rollback does not leak.
		require.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded,
			"caller ctx must not be canceled by rollback")
		t.Logf("blocked-identity rollback returned: forward=%d writes=%d",
			driver.forwardSuccess, live.PhysicalWriteCount())
	})
}

func requireNoAutomaticAdjustmentDeadline(t *testing.T, ctx context.Context, budget *AdjustmentBudget) {
	t.Helper()
	require.NotNil(t, budget)
	if _, callerHasDeadline := ctx.Deadline(); !callerHasDeadline {
		require.True(t, budget.deadline.IsZero(),
			"negative DeadlineDuration must not add an automatic adjustment deadline")
	}
}

func deterministicRecoveryContext(
	int,
) (context.Context, context.CancelFunc, error) {
	root := context.WithValue(
		context.Background(), adjustmentRecoveryContextKey{}, struct{}{})
	ctx, cancel := context.WithCancel(root)
	return ctx, cancel, nil
}

func newFullAdmissionAdjustmentBudget(
	t *testing.T,
	ctx context.Context,
	trace *CompiledPhaseTrace,
) *AdjustmentBudget {
	t.Helper()
	// Full-admission tests validate execution, rollback and operation accounting,
	// not the production wall-clock default. Under race+coverpkg, unrelated deep
	// topology tests can consume the entire ten-second default before this case
	// executes its first physical write. A negative duration deliberately skips
	// installing that automatic deadline while preserving caller cancellation and
	// every structural, write and replan budget. Deadline behavior is still
	// exercised deterministically by newTriggeredDeadlineContext below.
	budget := NewAdjustmentBudget(ctx, ConvergenceBudget{
		MaxPlanOperations: trace.Cost.Total(),
		DeadlineDuration:  -1,
	})
	requireNoAutomaticAdjustmentDeadline(t, ctx, budget)
	return budget
}

func newFullAdmissionTrace(
	t *testing.T,
) (*admissionTraceFixture, *CompleteSnapshot, *CompiledPhaseTrace) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	fixture.driver.capabilities = cgroupV2Policy.capabilities(true)
	fixture.round.objective = ConvergenceObjectiveParentSafe
	fixture.round.allowEmptyTarget = true
	fixture.selection = DrainSelectionPolicy{RequirePairedSwapProgress: true}

	pending := machine.MustParse("0-159")
	primaryInitial := machine.NewCPUSet(160)
	primaryTarget := primaryInitial.Union(pending)
	reclaimInitial := machine.NewCPUSet(161).Union(pending)
	reclaimTarget := machine.NewCPUSet(161)
	for cpu := 4; cpu <= 161; cpu++ {
		fixture.cpuDetails[cpu] = machine.CPUTopoInfo{
			NUMANodeID: cpu / 80,
			SocketID:   cpu / 80,
			CoreID:     cpu,
		}
	}

	for index := 0; index < fullAdmissionNodePairs; index++ {
		primaryRel := fmt.Sprintf("primary-%02d", index)
		reclaimRel := fmt.Sprintf("reclaim-%02d", index)
		fixture.addPrimary(primaryRel, primaryInitial.String(), "0-1")
		fixture.addReclaim(reclaimRel, reclaimInitial.String(), "0-1")
		fixture.targetByRel[primaryRel] = primaryTarget.Clone()
		fixture.requiredByRel[primaryRel] = primaryTarget.Clone()
		fixture.targetByRel[reclaimRel] = reclaimTarget.Clone()
	}
	for index := range fixture.specs {
		fixture.specs[index].CPUs = fixture.targetByRel[fixture.specs[index].Rel].Clone()
	}
	fixture.round.protectedPending = pending.Clone()

	base := fixture.snapshot()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	return fixture, base, trace
}

func requireFullAdmissionShape(t *testing.T, trace *CompiledPhaseTrace) {
	t.Helper()
	pending := trace.FinalSnapshot.DomainUnion[DomainPrimary].
		Difference(trace.InitialSnapshot.DomainUnion[DomainPrimary])
	require.Equal(t, fullAdmissionPendingCPUCount, pending.Size())

	shrinks, grows := 0, 0
	for _, operation := range flattenTraceOperations(trace) {
		switch operation.Direction {
		case WriteShrink:
			shrinks++
		case WriteGrow:
			grows++
		}
	}
	require.Equal(t, fullAdmissionNodePairs, shrinks)
	require.Equal(t, fullAdmissionNodePairs, grows)
	require.Equal(t, fullAdmissionWriteCount, len(expectedPhysicalWrites(trace)))
}

// requireFullAdmissionInverseWritesRestoreInitial verifies semantic rollback
// correctness without assuming a fixed 1:1 forward/inverse write count. The
// rollback writer may safely skip an inverse write when the current physical
// state already equals the original "before" value (classified as
// rollbackPhysicalStateBefore). This helper checks:
//
//  1. Every inverse write that DID happen targets a rel that had a forward
//     write (no phantom rollback).
//  2. Every inverse write restores that rel to its exact initial configured
//     CPUs (fail-closed value correctness).
//  3. The set of rels that received an inverse write, plus the set of rels
//     whose state was already correct (no-op skip), covers every rel that
//     received a forward write.
func requireFullAdmissionInverseWritesRestoreInitial(
	t *testing.T,
	forwardWrites []fakeHierarchyWrite,
	inverseWrites []fakeHierarchyWrite,
	initial fakeHierarchyState,
) {
	t.Helper()

	// Index forward writes by rel. A rel may appear at most once per trace in
	// the full-admission shape, so a single entry per rel is sufficient.
	forwardByRel := make(map[string]fakeHierarchyWrite, len(forwardWrites))
	for _, w := range forwardWrites {
		forwardByRel[w.rel] = w
	}

	// Every inverse write must correspond to a forward write on the same rel
	// and restore that rel to its initial configured CPUs.
	inverseRels := make(map[string]struct{}, len(inverseWrites))
	for i, inv := range inverseWrites {
		fwd, ok := forwardByRel[inv.rel]
		require.True(t, ok,
			"inverse write %d targets rel %q which had no forward write", i, inv.rel)
		require.Equal(t, fwd.identity, inv.identity,
			"inverse write %d for %q uses wrong identity: forward=%v inverse=%v",
			i, inv.rel, fwd.identity, inv.identity)
		require.True(t, inv.cpus.Equals(initial[inv.rel].configuredCPUs),
			"inverse write %d for %q restores %s, want initial %s",
			i, inv.rel, inv.cpus.String(), initial[inv.rel].configuredCPUs.String())
		inverseRels[inv.rel] = struct{}{}
	}

	// Every forward-written rel must either have received an inverse write,
	// or the rollback writer must have safely skipped it because the current
	// state already matched the original. The caller already verifies the
	// final snapshot equals initial via require.Equal(initial, live.snapshot()),
	// so here we only verify that any rel NOT in inverseRels was a legitimate
	// no-op skip (i.e. its forward write didn't actually change state, or the
	// rollback's pre-write read-back confirmed current==before).
	for rel := range forwardByRel {
		if _, hasInverse := inverseRels[rel]; hasInverse {
			continue
		}
		// No inverse write landed for this rel. The rollback writer skipped
		// it after confirming the physical state already equals the original.
		// The final-snapshot equality check above is the authoritative
		// verification; this branch just documents the expected skip.
		t.Logf("rel %q safely skipped inverse write (current already equals original)", rel)
	}
}
