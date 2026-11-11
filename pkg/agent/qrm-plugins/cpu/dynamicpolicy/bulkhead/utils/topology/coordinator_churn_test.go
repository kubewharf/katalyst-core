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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestAggregateExecutionErrorsSupportsGo118Matching(t *testing.T) {
	t.Parallel()

	primary := errors.New("primary execution failure")
	secondary := errors.New("reservation settlement failure")
	err := aggregateExecutionErrors(primary, secondary)
	require.ErrorIs(t, err, primary)
	require.ErrorIs(t, err, secondary)
	require.Contains(t, err.Error(), primary.Error())
	require.Contains(t, err.Error(), secondary.Error())
}

func reservedWritesCount(b *AdjustmentBudget) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reservedWrites
}

// newFrozenHolderChurnStale builds the exact PlanStaleError produced when a new
// relevant CPU holder appears between compilation and preflight
// (evaluateFrozenBoundarySnapshot: "new holder appeared").
func newFrozenHolderChurnStale() error {
	return frozenBoundaryStale(
		"dynamic",
		"kubepods/besteffort",
		"[kubepods kubepods/besteffort]",
		fmt.Errorf("relevant CPU holder set changed: new holder appeared"))
}

// newFrozenHolderCoverageDriftStale builds the other Rel="dynamic" PlanStaleError
// evaluateFrozenBoundarySnapshot can surface: when holder-coverage indexing
// disagrees ("relevant holder coverage changed", e.g. a holder's child
// identities shifted). Like the new-holder stale it is produced by the
// preflight pass before any physical write.
func newFrozenHolderCoverageDriftStale() error {
	return frozenBoundaryStale(
		"dynamic",
		"indexFrozenChildIdentities",
		"unique holder coverage",
		fmt.Errorf("relevant holder coverage changed"))
}

// wrapChurnLikePreflight mimics the preflight envelope: the boundary stale is
// wrapped inside an initial-snapshot-drift error whose first PlanStaleError is
// Resource="initial_snapshot", with the frozen_boundary/dynamic stale reachable
// only deeper in the chain.
func wrapChurnLikePreflight(cause error) error {
	return &frozenInitialSnapshotDriftError{
		cause: cause,
		stale: &PlanStaleError{
			Rel:       "controlled",
			Direction: WritePublish,
			Resource:  "initial_snapshot",
			Err:       cause,
		},
	}
}

func TestIsFrozenBoundaryDynamicDrift(t *testing.T) {
	t.Parallel()

	churn := newFrozenHolderChurnStale()
	wrapped := wrapChurnLikePreflight(churn)

	require.True(t, isFrozenBoundaryDynamicDrift(churn),
		"raw frozen_boundary/dynamic new-holder stale must be recognized")
	require.True(t, isFrozenBoundaryDynamicDrift(wrapped),
		"frozen_boundary/dynamic new-holder stale hidden inside the preflight envelope must be recognized")
	require.True(t, isFrozenBoundaryDynamicDrift(fmt.Errorf("preflight: %w", wrapped)),
		"additional wrapping must not hide the classification")

	// The second Rel="dynamic" origin: holder-coverage drift. It must be
	// absorbed exactly like a newly appeared holder, because it is likewise a
	// preflight, zero-write, fresh-snapshot-recompile event.
	coverageDrift := newFrozenHolderCoverageDriftStale()
	require.True(t, isFrozenBoundaryDynamicDrift(coverageDrift),
		"raw frozen_boundary/dynamic coverage-changed stale must be recognized")
	require.True(t, isFrozenBoundaryDynamicDrift(wrapChurnLikePreflight(coverageDrift)),
		"frozen_boundary/dynamic coverage drift inside the preflight envelope must be recognized")

	// A non-dynamic stale: same resource but a controlled (not dynamic) rel.
	controlledStale := frozenBoundaryStale(
		"controlled", "kubepods", "[]", fmt.Errorf("holder coverage drift"))
	require.False(t, isFrozenBoundaryDynamicDrift(controlledStale),
		"controlled rel drift is not dynamic holder drift")

	identityStale := &PlanStaleError{
		Rel: "kubepods", Resource: "container_cpuset", Direction: WritePublish,
	}
	require.False(t, isFrozenBoundaryDynamicDrift(identityStale),
		"unrelated resource stale is not holder drift")

	require.False(t, isFrozenBoundaryDynamicDrift(nil))
	require.False(t, isFrozenBoundaryDynamicDrift(errors.New("something else")))
}

// addChurnHolder introduces a new live cgroup that holds a relevant CPU but is
// absent from the compiled boundary.
func addChurnHolder(t *testing.T, fixture *admissionTraceFixture, rel, cpus string) {
	t.Helper()
	fixture.driver.add(rel, CgroupIdentity{Device: 1, Inode: fixture.allocInode()}, cpus, "0")
}

// TestParentSafeAdmissionAbsorbsOneDynamicHolderChurn verifies that a dynamic
// frozen-boundary holder stale with zero physical writes triggers exactly one
// in-transaction fresh-snapshot retry (res.Rounds grows from 1 to 2) without
// burning the caller's replan budget and without leaving the adjustment budget
// reserved.
//
// Fixture: the non-deferred task-9 transfer moves CPU 0 from reclaimed (0,4) to
// kubepods (1-3 -> requires 0-3). A new live kubepods descendant already pinning
// CPU 0 makes round 1's preflight observe a "new relevant holder" dynamic stale
// (it is an expand-parent child, so the controlled shrink-child check does not
// fire first). That stale is absorbed: the loop fetches a fresh snapshot and
// recompiles. To prove the absorbed retry itself performs no physical write even
// when it then hits a *different* (controlled) drift, a concurrent reclaimed
// mutation is injected on the round-2 preflight read; the single bounded retry
// surfaces that controlled stale as replan-safe with zero writes.
func TestParentSafeAdmissionAbsorbsOneDynamicHolderChurn(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9FinalizeFixture(t, false)
	adjustmentBudget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1000,
	})
	fixture.round.adjustmentBudget = adjustmentBudget

	// Round-1 dynamic holder churn: a new live kubepods descendant holding the
	// transferred CPU 0.
	addChurnHolder(t, fixture, "kubepods/churn-holder", "0")

	// Inject a controlled reclaimed drift on the round-2 preflight read so the
	// absorbed retry does not converge. The live driver issues a fixed sequence
	// of reads per admission: round-1 preflight, then the absorbed fresh
	// snapshot, then round-2 preflight. The 8th read lands inside round-2's
	// preflight snapshot; mutating reclaimed there makes round 2 observe a
	// controlled-relation state change (not a dynamic drift), which is returned
	// directly after the single absorption.
	reads := 0
	fixture.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op != HierarchyOperationRead {
			return nil
		}
		reads++
		if reads == 8 {
			n := fixture.driver.nodes["reclaimed"]
			n.cpus = n.cpus.Union(machine.NewCPUSet(99))
			n.configuredCPUs = n.cpus.Clone()
		}
		return nil
	}

	require.Zero(t, adjustmentBudget.CumulativeWrites(),
		"precondition: budget must start with zero cumulative writes")
	require.Zero(t, reservedWritesCount(adjustmentBudget),
		"precondition: budget must start with zero reserved writes")

	res := &ConvergenceResult{}
	outcome, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, res, nil, nil)

	require.Error(t, err)
	require.False(t, res.Published)
	// Round 1 compiles from the holder-free base and fails preflight with the
	// dynamic holder drift; the loop absorbs it once. Round 2 recompiles from
	// the fresh snapshot and fails preflight with the injected controlled
	// drift, which is not absorbed.
	require.Len(t, res.Rounds, 2, "initial drift round + one absorbed retry round")
	require.Zero(t, res.Applied, "churn absorption must not perform physical writes")
	require.Equal(t, ReplanSafeFromVerifiedFinalState, res.ReplanDisposition,
		"zero-write frozen drift must surface its fresh complete snapshot for caller replan")
	require.True(t, res.FinalSnapshotCurrent)
	require.NotNil(t, res.FinalSnapshot)
	require.Equal(t, RoundStatusBlocked, outcome.Status)

	// Zero physical writes were recorded against the budget: both reservations
	// were settled (the absorbed one explicitly at 0,0, the final one with the
	// retry round's zero attempted counts).
	require.Zero(t, adjustmentBudget.CumulativeWrites(),
		"zero-write absorption must leave cumulative writes at 0")
	// CumulativeWrites alone cannot detect a leaked reservation: with zero
	// physical writes it is 0 regardless of whether reservedWrites was released.
	// Assert the absorbed round-1 reservation settled (0,0) and released its
	// reserved slots, and the final round-2 reservation was settled by the defer.
	require.Zero(t, reservedWritesCount(adjustmentBudget),
		"both reservations must release reservedWrites; the absorbed churn reservation must not leak")

	// The absorption did not consume a caller replan: a fresh ConsumeReplan
	// must still be granted. (This call itself consumes the one slot; the test
	// does not rely on the budget afterwards.)
	require.NoError(t, adjustmentBudget.ConsumeReplan(nil),
		"in-transaction absorption must not burn the caller's replan budget")
}

// TestParentSafeAdmissionChurnAbsorptionConverges proves the positive path: a
// holder churned in between compilation and preflight is absorbed by the single
// fresh-snapshot retry, and the re-compiled trace then converges and publishes
// with real physical writes.
//
// Fixture: the non-deferred task-9 transfer moves CPU 0 from reclaimed (0,4) to
// kubepods (1-3 -> requires 0-3). The churn holder is a kubepods descendant that
// already pins CPU 0: on round 1 the compiled boundary (holder-free base) does
// not expect it, so preflight raises the dynamic "new holder" stale; on the
// fresh snapshot the holder is folded in and the planned transfer (expand
// kubepods to 0-3, shrink reclaimed to 4) lands.
func TestParentSafeAdmissionChurnAbsorptionConverges(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9FinalizeFixture(t, false)
	adjustmentBudget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1000,
	})
	fixture.round.adjustmentBudget = adjustmentBudget

	addChurnHolder(t, fixture, "kubepods/churn-holder", "0")

	require.Zero(t, adjustmentBudget.CumulativeWrites())
	require.Zero(t, reservedWritesCount(adjustmentBudget),
		"precondition: budget must start with zero reserved writes")

	res := &ConvergenceResult{}
	_, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, res, nil, nil)

	require.NoError(t, err, "absorbed fresh-snapshot retry must converge; got %v", err)
	require.True(t, res.Published, "converged trace must publish")
	require.True(t, res.Converged || res.ParentSafe,
		"converged trace must report Converged or ParentSafe: %+v", res)
	require.Len(t, res.Rounds, 2, "initial churned round + absorbed converging round")
	require.Positive(t, res.Applied, "a converging absorption must perform physical writes")
	require.Positive(t, adjustmentBudget.CumulativeWrites(),
		"converging absorption must record the physical writes it performed")
	// The absorbed round-1 reservation was settled explicitly at (0,0) and the
	// converging round-2 reservation was settled by the defer. Both must release
	// their reserved slots: a leaked round-1 reservation would leave a positive
	// reservedWrites even though CumulativeWrites only reflects the writes.
	require.Zero(t, reservedWritesCount(adjustmentBudget),
		"the absorbed churn reservation and the converging reservation must both release reservedWrites")
}

// TestParentSafeAdmissionDoesNotAbsorbNonChurnStale verifies that a stale that
// is NOT a dynamic frozen-boundary holder drift is returned directly without an
// absorbed retry.
func TestParentSafeAdmissionDoesNotAbsorbNonChurnStale(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	fixture.round.adjustmentBudget = NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1000,
	})

	// Mutate a controlled entry's live value after compilation. This produces
	// an initial-snapshot drift, NOT a frozen_boundary/dynamic holder drift.
	entry := fixture.driver.nodes["reclaimed"]
	entry.cpus = entry.cpus.Union(machine.NewCPUSet(99))
	entry.configuredCPUs = entry.cpus.Clone()
	fixture.driver.nodes["reclaimed"] = entry

	res := &ConvergenceResult{}
	_, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, res, nil, nil)

	require.Error(t, err)
	require.False(t, res.Published)
	require.Zero(t, res.Applied)
	require.Len(t, res.Rounds, 1, "non-drift stale must not trigger an absorbed retry")
	require.False(t, isFrozenBoundaryDynamicDrift(err),
		"this drift must not be classified as dynamic holder drift: %v", err)
}
