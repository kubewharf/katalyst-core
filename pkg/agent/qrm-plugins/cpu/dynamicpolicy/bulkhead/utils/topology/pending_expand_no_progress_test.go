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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestSNBSeedLeafAdmissionCoversRequiredFloor flips the historical no-progress
// pin: a freshly-created SNB seed leaf on cgroup v1 (allowEmptyTarget=false) must
// now compile to a ParentSafe, forward-progressing trace.
//
// Field shape: a brand-new DomainPrimary seed leaf is admitted for a CPU (1) that
// has not yet been folded into the desired non-reclaim pool, so its not-yet-
// projected desired target owns only CPU 0 while RequiredCPUSetByRel demands the
// floor 0-1. computeEffectiveTargets must widen the controlled seed leaf's own
// required floor into its effective target (and deduct that CPU from the reclaim
// bucket). With the widened envelope the ParentSafe fixed-point compilation sheds
// the reclaim CPU and grows the seed leaf to the floor, so it no longer stalls as
// a bare ErrNoProgress and RequiredFloorDeficit clears.
func TestSNBSeedLeafAdmissionCoversRequiredFloor(t *testing.T) {
	t.Parallel()

	fixture := newAdmissionTraceFixture(t)
	for cpu := 0; cpu < 4; cpu++ {
		fixture.cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0, CoreID: cpu}
	}
	// cgroup v1: allowEmptyTarget stays false.
	fixture.addPrimary("kubepods", "0-1", "0")
	// Freshly admitted seed leaf: its desired (seed) target owns only CPU 0.
	fixture.addPrimary("kubepods/seed", "0", "0")
	// Reclaim bucket currently holds the seed's pending CPU plus extra reclaim CPUs.
	fixture.addReclaim("reclaimed-0", "1-3", "0")

	// The seed leaf imposes a required floor (0-1) that its not-yet-projected
	// desired target (CPU 0) does not cover.
	requiredByRel := map[string]machine.CPUSet{
		"kubepods/seed": machine.MustParse("0-1"),
	}
	dag, err := BuildDAG(fixture.specs)
	require.NoError(t, err)
	effective, err := computeEffectiveTargets(dag, false, fixture.cpuDetails, nil, requiredByRel, nil)
	require.NoError(t, err)

	// The DAG desired shape is the post-normalization physical target (the widened
	// primary floor plus the deducted reclaim), exactly as the live coordinator feeds
	// computeEffectiveTargets into the round.
	for i := range fixture.specs {
		fixture.specs[i].CPUs = effective[fixture.specs[i].Rel].Clone()
	}
	fixture.targetByRel = effective
	fixture.requiredByRel = requiredByRel
	// The pending admission protects the seed leaf's required CPU (1); this is the
	// release witness that authorizes shedding it from reclaim and growing the seed.
	fixture.round.protectedPending = machine.MustParse("1")
	fixture.round.pendingRequiredByRel = map[string]machine.CPUSet{
		"kubepods":      machine.MustParse("1"),
		"kubepods/seed": machine.MustParse("1"),
	}

	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(), fixture.snapshot(),
	)
	require.NoError(t, err, "want ParentSafe forward progress, err=%v", err)
	require.False(t, errors.Is(err, ErrNoProgress),
		"err=%v, must not stall as a bare ErrNoProgress", err)

	// Forward progress: the compiled trace must actually shed reclaim and grow the
	// seed leaf rather than emitting an empty drain->expand plan.
	require.NotEmpty(t, flattenTraceOperations(trace), "want forward drain/expand writes")
	require.Contains(t,
		traceRelsByDirection(trace, WriteShrink), "reclaimed-0",
		"want reclaim bucket to shed the protected CPU")
	require.True(t, traceContainsOperation(trace, "kubepods/seed", WriteGrow, machine.MustParse("0-1")),
		"want seed leaf grown to its required floor")

	// RequiredFloorDeficit cleared and ParentSafe holds: the widened effective
	// target covers the seed leaf's required floor.
	parentSafety := trace.FinalEvaluation.ParentSafety
	require.Empty(t, parentSafety.RequiredFloorDeficit,
		"RequiredFloorDeficit=%v, want cleared", parentSafety.RequiredFloorDeficit)
	require.True(t, parentSafety.Safe, "ParentSafety=%+v", parentSafety)
	_ = machine.NewCPUSet
}

// traceRelsByDirection returns the set of rels that carry at least one operation
// of the given direction in the compiled trace.
func traceRelsByDirection(trace *CompiledPhaseTrace, direction WriteDirection) []string {
	if trace == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var rels []string
	for _, op := range flattenTraceOperations(trace) {
		if op.Direction != direction {
			continue
		}
		if _, ok := seen[op.Rel]; ok {
			continue
		}
		seen[op.Rel] = struct{}{}
		rels = append(rels, op.Rel)
	}
	return rels
}
